package test

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tidwall/gjson"
)

// quotaFixture is a controlled provider quota rejection. retryAfter and body carry the
// provider's own reset information, and errorType is the value the harness keys on.
type quotaFixture struct {
	contract    contract
	errorType   string
	retryAfter  string
	body        string
	resetSource string
}

func quotaFixtures(reset string) []quotaFixture {
	return []quotaFixture{
		{contract: contracts[1], errorType: "usage_limit_reached", resetSource: "body resets_in_seconds",
			body: `{"error":{"type":"usage_limit_reached","message":"The usage limit has been reached","plan_type":"plus","resets_in_seconds":` + reset + `}}`},
		{contract: contracts[2], errorType: "rate_limit_error", retryAfter: reset, resetSource: "Retry-After header",
			body: `{"type":"error","error":{"type":"rate_limit_error","message":"This request would exceed your account's rate limit."}}`},
		{contract: contracts[0], errorType: "insufficient_quota", retryAfter: reset, resetSource: "Retry-After header",
			body: `{"error":{"message":"You exceeded your current quota","type":"insufficient_quota","code":"insufficient_quota"}}`},
	}
}

// quotaUpstream rejects every request until recover is set, then serves the contract fixture.
func quotaUpstream(t *testing.T, f quotaFixture, recovered *atomic.Bool) *fakeUpstream {
	t.Helper()
	return newFakeUpstream(t, func(w http.ResponseWriter, _ *http.Request, _ upstreamCall) bool {
		if recovered != nil && recovered.Load() {
			return false
		}
		if f.retryAfter != "" {
			w.Header().Set("Retry-After", f.retryAfter)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(f.body))
		return true
	})
}

func retryAfterSeconds(t *testing.T, got observed) int {
	t.Helper()
	value := got.Header.Get("Retry-After")
	seconds, err := strconv.Atoi(value)
	if err != nil {
		t.Fatalf("Retry-After = %q, want integer seconds", value)
	}
	return seconds
}

// TestQuotaExhaustionIsIndependentOfAccounting exercises provider quota rejections. The
// outcome must be the provider's own signal, must not depend on accounting state, and must
// not consult accounting costs as if they were quota.
func TestQuotaExhaustionIsIndependentOfAccounting(t *testing.T) {
	const reset = 3600
	for _, f := range quotaFixtures(strconv.Itoa(reset)) {
		t.Run(f.contract.harness+"/"+f.errorType, func(t *testing.T) {
			var firstDisabled string
			for _, mode := range []accountingMode{accountingDisabled, accountingEnabled, accountingLiteLLMStalled} {
				upstream := quotaUpstream(t, f, nil)
				recorder := recordAccounting(t)
				g := newGateway(t, gatewayOptions{mode: mode, upstreamURL: upstream.server.URL, accounts: contractAccounts("quota-"+string(mode), f.contract), exportClients: true})

				first := post(t, context.Background(), g.URL+f.contract.path, f.contract.request(false), nil)
				if first.Status != http.StatusTooManyRequests {
					t.Fatalf("%s: status = %d, want 429: %s", mode, first.Status, first.Body)
				}
				if got := gjson.Get(first.Body, "error.type").String(); got != f.errorType {
					t.Errorf("%s: error type = %q, want the provider's %q: %s", mode, got, f.errorType, first.Body)
				}
				if f.contract.provider == "codex" && gjson.Get(first.Body, "error.resets_in_seconds").Int() != reset {
					t.Errorf("%s: codex reset information was altered: %s", mode, first.Body)
				}
				if firstDisabled == "" {
					firstDisabled = first.shape()
				} else if first.shape() != firstDisabled {
					t.Errorf("%s changed the quota response\nwant: %s\n got: %s", mode, firstDisabled, first.shape())
				}

				// The next request is answered from the cooldown without another upstream attempt.
				second := post(t, context.Background(), g.URL+f.contract.path, f.contract.request(false), nil)
				if second.Status != http.StatusTooManyRequests {
					t.Errorf("%s: cooling request status = %d, want 429", mode, second.Status)
				}
				if len(upstream.snapshot()) != 1 {
					t.Errorf("%s: upstream attempts = %d, want 1 while cooling", mode, len(upstream.snapshot()))
				}
				// Claude adds up to 30s of jitter after the provider's reset.
				if seconds := retryAfterSeconds(t, second); seconds < reset-5 || seconds > reset+31 {
					t.Errorf("%s: Retry-After = %ds, want the provider's %ds reset (plus Claude jitter)", mode, seconds, reset)
				}

				recorded := recorder.waitEvents(t, 1)
				if mode == accountingDisabled {
					g.shutdown()
					continue
				}
				events := g.durableEvents(t, recorded)
				g.shutdown()
				if len(events) != 1 || events[0].Status != "failed" || events[0].StatusCode != http.StatusTooManyRequests || events[0].Tokens != nil {
					t.Errorf("%s: want exactly one failed 429 attempt without tokens, got %+v", mode, events)
				}
				if events[0].Estimate != nil && events[0].Estimate.Status == "priced" {
					t.Errorf("%s: a rejected attempt must not be priced: %+v", mode, events[0].Estimate)
				}
			}
		})
	}
}

// TestQuotaWithoutProviderResetDoesNotInventOne checks that a rejection with no reset
// information is passed through without a fabricated reset or Retry-After on that response.
func TestQuotaWithoutProviderResetDoesNotInventOne(t *testing.T) {
	for _, f := range quotaFixtures("0") {
		t.Run(f.contract.harness, func(t *testing.T) {
			f.retryAfter = ""
			f.body = strings.Replace(f.body, `,"resets_in_seconds":0`, "", 1)
			upstream := quotaUpstream(t, f, nil)
			g := newGateway(t, gatewayOptions{mode: accountingDisabled, upstreamURL: upstream.server.URL, accounts: contractAccounts("noreset", f.contract)})

			got := post(t, context.Background(), g.URL+f.contract.path, f.contract.request(false), nil)
			if got.Status != http.StatusTooManyRequests || got.Header.Get("Retry-After") != "" || strings.Contains(got.Body, "reset") {
				t.Errorf("response invented reset information: status=%d retry-after=%q body=%s", got.Status, got.Header.Get("Retry-After"), got.Body)
			}
		})
	}
}

// TestQuotaRecoversAfterProviderReset waits out the shortest cooldown the gateway applies
// and verifies exactly one probe reaches the provider, which then succeeds. The gateway floors
// provider hints at 10s and adds up to 30s of Claude jitter, so this takes real time.
func TestQuotaRecoversAfterProviderReset(t *testing.T) {
	if testing.Short() {
		t.Skip("waits for the gateway's minimum quota cooldown")
	}
	for _, f := range quotaFixtures("1") {
		t.Run(f.contract.harness, func(t *testing.T) {
			t.Parallel()
			var recovered atomic.Bool
			upstream := quotaUpstream(t, f, &recovered)
			g := newGateway(t, gatewayOptions{mode: accountingDisabled, upstreamURL: upstream.server.URL, accounts: contractAccounts("recover", f.contract)})

			if first := post(t, context.Background(), g.URL+f.contract.path, f.contract.request(false), nil); first.Status != http.StatusTooManyRequests {
				t.Fatalf("status = %d, want 429: %s", first.Status, first.Body)
			}
			recovered.Store(true)

			start := time.Now()
			deadline := start.Add(60 * time.Second)
			for {
				got := post(t, context.Background(), g.URL+f.contract.path, f.contract.request(false), nil)
				if got.Status == http.StatusOK {
					t.Logf("%s recovered %s after the rejection (provider reset hint: 1s)", f.contract.harness, time.Since(start).Round(100*time.Millisecond))
					break
				}
				if got.Status != http.StatusTooManyRequests {
					t.Fatalf("unexpected status %d while cooling: %s", got.Status, got.Body)
				}
				if time.Now().After(deadline) {
					t.Fatalf("no recovery within 60s")
				}
				time.Sleep(250 * time.Millisecond)
			}
			if attempts := len(upstream.snapshot()); attempts != 2 {
				t.Errorf("upstream attempts = %d, want the rejection plus exactly one probe", attempts)
			}
		})
	}
}
