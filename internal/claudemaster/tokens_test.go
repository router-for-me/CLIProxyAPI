package claudemaster

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

const tokenTestSSE = "event: message_start\r\n" +
	`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-opus-5","content":[],"usage":{"input_tokens":11,"cache_read_input_tokens":300,"cache_creation_input_tokens":40,"output_tokens":1}}}` + "\r\n\r\n" +
	"event: content_block_delta\r\n" +
	`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"usage: {\"output_tokens\":999}"}}` + "\r\n\r\n" +
	"event: message_delta\r\n" +
	`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":25}}` + "\r\n\r\n" +
	"event: message_stop\r\n" + `data: {"type":"message_stop"}` + "\r\n\r\n"

const tokenTestJSON = `{"id":"msg_1","type":"message","role":"assistant","model":"claude-opus-5","content":[{"type":"text","text":"hi"}],"stop_reason":"end_turn","usage":{"input_tokens":11,"output_tokens":25,"cache_read_input_tokens":300,"cache_creation_input_tokens":40}}`

var tokenTestWant = tokenCounts{input: 11, output: 25, cacheRead: 300, cacheCreation: 40}

func scanUsage(status int, contentType string, chunks ...string) (tokenCounts, bool) {
	var u usageScanner
	u.start(status, contentType, "")
	for _, chunk := range chunks {
		u.write([]byte(chunk))
	}
	return u.result()
}

func TestUsageScannerReadsAStreamWhereverItIsSplit(t *testing.T) {
	// Every split point, including inside "data:" and inside the JSON.
	for at := 0; at <= len(tokenTestSSE); at++ {
		got, ok := scanUsage(200, "text/event-stream", tokenTestSSE[:at], tokenTestSSE[at:])
		if !ok || got != tokenTestWant {
			t.Fatalf("split at %d: %+v, %v; want %+v", at, got, ok, tokenTestWant)
		}
	}
	bytewise := make([]string, len(tokenTestSSE))
	for i := range tokenTestSSE {
		bytewise[i] = tokenTestSSE[i : i+1]
	}
	if got, ok := scanUsage(200, "text/event-stream; charset=utf-8", bytewise...); !ok || got != tokenTestWant {
		t.Fatalf("byte by byte: %+v, %v", got, ok)
	}
}

func TestUsageScannerTakesTheCumulativeDelta(t *testing.T) {
	// A newer message_delta repeats input and cache counts cumulatively; it wins over message_start.
	stream := `data: {"type":"message_start","message":{"usage":{"input_tokens":5,"output_tokens":1}}}` + "\n\n" +
		`data: {"type":"message_delta","usage":{"input_tokens":7,"cache_read_input_tokens":2,"output_tokens":40}}` + "\n\n"
	if got, ok := scanUsage(200, "text/event-stream", stream); !ok || got != (tokenCounts{input: 7, output: 40, cacheRead: 2}) {
		t.Fatalf("%+v, %v", got, ok)
	}
}

func TestUsageScannerPassesOverLongLinesWithoutHoldingThem(t *testing.T) {
	long := "data: " + `{"type":"content_block_delta","delta":{"text":"` + strings.Repeat("x", 3*usageLineLimit) + `"}}` + "\n\n"
	var u usageScanner
	u.start(200, "text/event-stream", "")
	u.write([]byte(tokenTestSSE[:strings.Index(tokenTestSSE, "event: content_block_delta")]))
	for i := 0; i < len(long); i += 4096 {
		u.write([]byte(long[i:min(i+4096, len(long))]))
		if cap(u.line) > 2*usageLineLimit {
			t.Fatalf("the scanner holds %d bytes of one line", cap(u.line))
		}
	}
	u.write([]byte(tokenTestSSE[strings.Index(tokenTestSSE, "event: message_delta"):]))
	if got, ok := u.result(); !ok || got != tokenTestWant {
		t.Fatalf("after a long line: %+v, %v", got, ok)
	}
}

func TestUsageScannerReadsAJSONBodyOfAnySize(t *testing.T) {
	if got, ok := scanUsage(200, "application/json", tokenTestJSON[:40], tokenTestJSON[40:]); !ok || got != tokenTestWant {
		t.Fatalf("small body: %+v, %v", got, ok)
	}
	// A body far larger than the kept tail, with a "usage" in the text the model wrote.
	big := strings.Replace(tokenTestJSON, `"text":"hi"`, `"text":"\"usage\":{\"output_tokens\":1} `+strings.Repeat("y", 3*usageTailLimit)+`"`, 1)
	var chunks []string
	for i := 0; i < len(big); i += 8192 {
		chunks = append(chunks, big[i:min(i+8192, len(big))])
	}
	if got, ok := scanUsage(200, "application/json", chunks...); !ok || got != tokenTestWant {
		t.Fatalf("large body: %+v, %v", got, ok)
	}
}

func TestUsageScannerIgnoresErrorsAndOtherBodies(t *testing.T) {
	for _, tc := range []struct {
		status      int
		contentType string
		body        string
	}{
		{429, "application/json", tokenTestJSON},
		{500, "text/event-stream", tokenTestSSE},
		{200, "text/plain", tokenTestJSON},
		{200, "application/json", `{"type":"message","content":[]}`},
		{200, "text/event-stream", `data: {"type":"message_start","message":{"usage":"none"}}` + "\n"},
	} {
		if got, ok := scanUsage(tc.status, tc.contentType, tc.body); ok {
			t.Errorf("%d %s: counted %+v", tc.status, tc.contentType, got)
		}
	}
}

// tokenUpstream answers like Anthropic, with usage: an SSE stream or one JSON message.
type tokenUpstream struct{}

func (u *tokenUpstream) RoundTripperFor(*coreauth.Auth) http.RoundTripper { return u }

func (*tokenUpstream) RoundTrip(r *http.Request) (*http.Response, error) {
	body, _ := io.ReadAll(r.Body)
	if strings.Contains(string(body), `"stream":true`) {
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(tokenTestSSE)), Request: r}, nil
	}
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(tokenTestJSON)), Request: r}, nil
}

// Through the real backend and native Claude executor: the tokens of a streamed and a non-streamed
// answer are counted by the request's profile, user account and client, and the client receives
// the response byte for byte.
func TestTokensOfAnInferenceRequestAreCounted(t *testing.T) {
	for _, stream := range []bool{true, false} {
		t.Run(fmt.Sprint("stream=", stream), func(t *testing.T) {
			reader := startMetrics(t, map[string]string{testAccountUUID: "colton"})
			backend := newBackendNativeResponseFixture(t)
			backend.seriesSelector.names = map[string]string{backend.authIDs[0]: "claude-connor"}
			backend.manager.SetRoundTripperProvider(&tokenUpstream{})
			rec := serve(t, backend, inferenceRequest(testAccountUUID, stream))
			want := tokenTestJSON
			if stream {
				want = tokenTestSSE
			}
			if rec.Code != http.StatusOK || rec.Body.String() != want {
				t.Fatalf("status %d, body changed: %q", rec.Code, rec.Body.String())
			}
			rm := collect(t, reader)
			for typ, n := range map[string]int64{"input": 11, "output": 25, "cache_read": 300, "cache_creation": 40} {
				if got := sumOf(rm, "claude_master.inference.tokens", map[string]string{"profile": "claude-connor", "type": typ}); got != n {
					t.Errorf("tokens{profile,type=%s} = %d, want %d", typ, got, n)
				}
				if got := sumOf(rm, "claude_master.inference.tokens.by_client_account", map[string]string{"client_account": "colton", "type": typ}); got != n {
					t.Errorf("tokens.by_client_account{type=%s} = %d, want %d", typ, got, n)
				}
				if got := sumOf(rm, "claude_master.inference.tokens.by_client", map[string]string{"client": "unknown", "type": typ}); got != n {
					t.Errorf("tokens.by_client{type=%s} = %d, want %d", typ, got, n)
				}
				// No tunnel named a project.
				if got := sumOf(rm, "claude_master.inference.tokens.by_project", map[string]string{"project": "none", "type": typ}); got != n {
					t.Errorf("tokens.by_project{project=none,type=%s} = %d, want %d", typ, got, n)
				}
			}
		})
	}
}

// A request on a tunnel that named a project counts its tokens under that project.
func TestTokensAreCountedByTheProjectTheTunnelNamed(t *testing.T) {
	reader := startMetrics(t, nil)
	observeRequest(requestObservation{
		Route: "inference", Profile: "claude-test", Client: "box", Account: "unknown", Project: "my-app",
		Status: http.StatusOK, Tokens: tokenCounts{input: 7, output: 3}, HasTokens: true,
	})
	rm := collect(t, reader)
	for typ, n := range map[string]int64{"input": 7, "output": 3} {
		if got := sumOf(rm, "claude_master.inference.tokens.by_project", map[string]string{"project": "my-app", "type": typ}); got != n {
			t.Errorf("tokens.by_project{project=my-app,type=%s} = %d, want %d", typ, got, n)
		}
	}
}

// count_tokens reports input_tokens too, but it is a question, not consumption: it is not counted.
func TestCountTokensIsNotCountedAsTokens(t *testing.T) {
	reader := startMetrics(t, nil)
	observeRequest(requestObservation{Route: "count_tokens", Client: "box", Account: "unknown", Tokens: tokenCounts{input: 50}, HasTokens: true})
	if findMetric(collect(t, reader), "claude_master.inference.tokens") != nil {
		t.Fatal("count_tokens was counted as tokens")
	}
}
