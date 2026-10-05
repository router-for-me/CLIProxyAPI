package test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

func contractAccounts(prefix string, only ...contract) []upstreamAccount {
	if len(only) == 0 {
		only = contracts
	}
	accounts := make([]upstreamAccount, 0, len(only))
	for _, c := range only {
		accounts = append(accounts, upstreamAccount{
			id: prefix + "-" + c.name, provider: c.provider, apiKey: "acct-" + c.name,
			models: []string{c.model}, priority: 1, websockets: c.provider == "codex",
		})
	}
	return accounts
}

// upstreamShape reduces what the provider received to values that must not depend on accounting.
func upstreamShape(calls []upstreamCall) []string {
	shapes := make([]string, 0, len(calls))
	for _, call := range calls {
		headers := make([]string, 0, len(call.Header))
		for name, values := range call.Header {
			switch name {
			case "Accept-Encoding", "Content-Length", "Connection", "Sec-Websocket-Key":
				continue
			}
			headers = append(headers, name+"="+uuidPattern.ReplaceAllString(strings.Join(values, ","), "<uuid>"))
		}
		sort.Strings(headers)
		shapes = append(shapes, fmt.Sprintf("%s %s\n%s\n%s", call.Account, call.Path, strings.Join(headers, "\n"), uuidPattern.ReplaceAllString(call.Body, "<uuid>")))
	}
	return shapes
}

type run struct {
	observed observed
	upstream []upstreamCall
	events   []usage.AccountingEvent
	gateway  *gateway
}

func runContract(t *testing.T, mode accountingMode, c contract, stream bool) run {
	t.Helper()
	upstream := newFakeUpstream(t, nil)
	// Every mode waits for its own usage record, which is delivered asynchronously, so that
	// a late record cannot leak into the next gateway. The recorder does not touch inference.
	recorder := recordAccounting(t)
	g := newGateway(t, gatewayOptions{mode: mode, upstreamURL: upstream.server.URL, accounts: contractAccounts("iso-"+string(mode), c)})

	got := run{gateway: g}
	got.observed = post(t, context.Background(), g.URL+c.path, c.request(stream), nil)
	got.upstream = upstream.snapshot()
	events := recorder.waitEvents(t, 1)
	if mode != accountingDisabled {
		got.events = g.durableEvents(t, events)
	}
	g.shutdown()
	return got
}

// TestAccountingDoesNotAlterInference proves that, for each harness contract, the client sees
// byte-identical status, headers, and body, and the provider receives identical requests,
// whether accounting is off, on, or exporting to a LiteLLM that is offline, stalled or failing.
func TestAccountingDoesNotAlterInference(t *testing.T) {
	for _, c := range contracts {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/%s/stream=%t", c.harness, c.name, stream), func(t *testing.T) {
				baseline := runContract(t, accountingDisabled, c, stream)
				if baseline.observed.Status != http.StatusOK {
					t.Fatalf("baseline failed: %d %s", baseline.observed.Status, baseline.observed.Body)
				}
				for _, tool := range []string{"get_weather", "Oslo"} {
					if !strings.Contains(baseline.observed.Body, tool) {
						t.Fatalf("baseline lost the tool call %q: %s", tool, baseline.observed.Body)
					}
				}

				for _, mode := range allAccountingModes[1:] {
					got := runContract(t, mode, c, stream)
					if want, have := baseline.observed.shape(), got.observed.shape(); want != have {
						t.Errorf("%s changed the client-visible response\nwant: %s\n got: %s", mode, want, have)
					}
					if stream && got.observed.Chunks != baseline.observed.Chunks {
						t.Errorf("%s changed the stream framing: %d lines, want %d", mode, got.observed.Chunks, baseline.observed.Chunks)
					}
					if want, have := upstreamShape(baseline.upstream), upstreamShape(got.upstream); strings.Join(want, "\n--\n") != strings.Join(have, "\n--\n") {
						t.Errorf("%s changed the upstream request\nwant: %v\n got: %v", mode, want, have)
					}
					assertSafeSucceededEvent(t, c, got.events)
				}
			})
		}
	}
}

func assertSafeSucceededEvent(t *testing.T, c contract, events []usage.AccountingEvent) {
	t.Helper()
	if len(events) != 1 {
		t.Fatalf("events = %d, want exactly one terminal event per upstream attempt", len(events))
	}
	event := events[0]
	if event.Status != "succeeded" || event.Provider != c.provider || event.ExecutedModel != c.model || event.ExecutionID == "" || event.TraceID == "" {
		t.Errorf("unexpected event identity: %+v", event)
	}
	if event.Tokens == nil || event.Tokens.Breakdown.Input.TotalTokens != c.usage.input || event.Tokens.Breakdown.Output.TotalTokens != c.usage.output || event.Tokens.Breakdown.TotalTokens != c.usage.total {
		t.Errorf("tokens = %+v, want %+v", event.Tokens, c.usage)
	}
	if event.Estimate == nil || event.Estimate.Status != "priced" {
		t.Errorf("estimate = %+v, want priced", event.Estimate)
	}
	if event.ClientKeyID != clientKeyID(gatewayClientKey) {
		t.Errorf("client key id = %q, want the salted client hash", event.ClientKeyID)
	}
	raw, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{gatewayClientKey, "acct-", gatewayAdminSecret, "Bearer", "weather in Oslo"} {
		if strings.Contains(string(raw), secret) {
			t.Errorf("accounting event leaks %q: %s", secret, raw)
		}
	}
}

// TestAccountingDoesNotAlterCodexWebsocket covers the downstream WebSocket path the Codex
// harness uses, with the upstream connection also a WebSocket.
func TestAccountingDoesNotAlterCodexWebsocket(t *testing.T) {
	var codex contract
	for _, c := range contracts {
		if c.provider == "codex" {
			codex = c
		}
	}
	create := `{"type":"response.create","model":"gpt-5.4","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"weather in Oslo?"}]}],"tools":[{"type":"function","name":"get_weather","parameters":` + weatherTool + `}]}`

	var baseline []string
	var baselineUpstream []string
	for _, mode := range allAccountingModes {
		upstream := newFakeUpstream(t, nil)
		recorder := recordAccounting(t)
		g := newGateway(t, gatewayOptions{mode: mode, upstreamURL: upstream.server.URL, accounts: contractAccounts("ws-"+string(mode), codex)})

		frames := readResponsesWebsocket(t, g.URL, create)
		for i := range frames {
			frames[i] = uuidPattern.ReplaceAllString(frames[i], "<uuid>")
		}
		shape := upstreamShape(upstream.snapshot())
		events := recorder.waitEvents(t, 1)
		if mode == accountingDisabled {
			baseline, baselineUpstream = frames, shape
			if len(frames) < len(responsesFixtureEvents) || !strings.Contains(strings.Join(frames, ""), "get_weather") {
				t.Fatalf("baseline websocket lost the tool call: %v", frames)
			}
			g.shutdown()
			continue
		}
		if strings.Join(frames, "\n") != strings.Join(baseline, "\n") {
			t.Errorf("%s changed websocket events\nwant: %v\n got: %v", mode, baseline, frames)
		}
		if strings.Join(shape, "\n") != strings.Join(baselineUpstream, "\n") {
			t.Errorf("%s changed the upstream websocket request\nwant: %v\n got: %v", mode, baselineUpstream, shape)
		}
		assertSafeSucceededEvent(t, codex, g.durableEvents(t, events))
		g.shutdown()
	}
}

// TestCancellationReachesUpstreamAndIsAccounted cancels each streaming contract mid-stream.
// The provider must see the cancellation in every mode, with no retry caused by accounting.
func TestCancellationReachesUpstreamAndIsAccounted(t *testing.T) {
	for _, c := range contracts {
		t.Run(c.name, func(t *testing.T) {
			head := strings.Join(strings.SplitAfter(c.stream.body, "\n\n")[:3], "")
			for _, mode := range allAccountingModes {
				upstream := newFakeUpstream(t, nil)
				upstream.handle = func(w http.ResponseWriter, r *http.Request, call upstreamCall) bool {
					upstream.holdStream(w, r, call, "text/event-stream", head)
					return true
				}
				recorder := recordAccounting(t)
				g := newGateway(t, gatewayOptions{mode: mode, upstreamURL: upstream.server.URL, accounts: contractAccounts("cancel-"+string(mode), c)})

				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
				req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.URL+c.path, strings.NewReader(c.request(true)))
				if err != nil {
					t.Fatal(err)
				}
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("Authorization", "Bearer "+gatewayClientKey)
				resp, err := http.DefaultClient.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				buf := make([]byte, 1)
				if _, err := resp.Body.Read(buf); err != nil {
					t.Fatalf("%s: stream never started: %v", mode, err)
				}
				cancel()
				_ = resp.Body.Close()

				select {
				case <-upstream.canceled:
				case <-time.After(10 * time.Second):
					t.Fatalf("%s: upstream never observed the client cancellation", mode)
				}
				if calls := upstream.snapshot(); len(calls) != 1 {
					t.Errorf("%s: upstream attempts = %d, want 1", mode, len(calls))
				}
				recorded := recorder.waitEvents(t, 1)
				if mode == accountingDisabled {
					g.shutdown()
					continue
				}
				events := g.durableEvents(t, recorded)
				g.shutdown()
				if len(events) != 1 || (events[0].Status != "interrupted" && events[0].Status != "failed") {
					t.Errorf("%s: cancelled attempt must be a single non-success event, got %+v", mode, events)
				}
			}
		})
	}
}
