package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/httptransport"
)

func TestAcceptedExecutorErrorClassification(t *testing.T) {
	codexErr := func(payload string) error {
		err, _, ok := codexTerminalFailureErr([]byte(payload))
		if !ok {
			t.Fatalf("Codex fixture did not reach its terminal error parser: %s", payload)
		}
		return err
	}
	xaiErr := func(payload string) error {
		err, ok := xaiTerminalFailureErr("error", []byte(payload))
		if !ok {
			t.Fatalf("xAI fixture did not reach its terminal error parser: %s", payload)
		}
		return err
	}
	openaiErr, ok := openAICompatStreamDataError([]byte(`{"error":{"message":"refused","code":503}}`), "error")
	if !ok {
		t.Fatal("OpenAI fixture did not reach its error parser")
	}
	for _, tc := range []struct {
		name     string
		err      error
		provider bool
	}{
		{"local translation", statusErr{code: 502, msg: helps.ApplyPatchUpstreamErrorMessage}, false},
		{"local error envelope", statusErr{code: 502, msg: `{"error":{"message":"local failure"}}`}, false},
		{"Claude local validation", validateClaudeStreamingResponse([]byte("data: {\"type\":\"message_start\",\"message\":{}}\n\n")), false},
		{"Codex synthesized error", codexErr(`{"type":"response.failed","response":{}}`), false},
		{"Codex malformed source", codexErr(`{"type":"response.failed","response":{"error":{"message":"broken"}}`), false},
		{"xAI synthesized error", xaiErr(`{"type":"response.failed","response":{}}`), false},
		{"xAI malformed source", xaiErr(`{"error":{"message":"broken"}`), false},
		{"Meta synthesized error", metaStreamEventError([]byte(`{"type":"response.failed"}`)), false},
		{"OpenAI provider error", openaiErr, true},
		{"Codex top-level provider message", codexErr(`{"type":"error","message":"provider overloaded"}`), true},
		{"xAI top-level provider message", xaiErr(`{"type":"error","message":"provider overloaded"}`), true},
		{"Claude provider error", validateClaudeStreamingResponse([]byte("data: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"refused\"}}\n\n")), true},
		{"Codex provider error", codexErr(`{"type":"response.failed","response":{"error":{"code":"server_error","message":"refused"}}}`), true},
		{"xAI provider error", xaiErr(`{"type":"error","error":{"message":"refused"}}`), true},
		{"Meta provider error", metaStreamEventError([]byte(`{"type":"response.failed","response":{"error":{"message":"refused"}}}`)), true},
		{"Antigravity provider error", newAntigravityStatusErr(503, []byte(`{"error":{"code":503,"message":"refused"}}`)), true},
		{"Devin provider error", newDevinStatusError(503, nil, []byte(`{"error":{"message":"refused"}}`)), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.err == nil {
				t.Fatal("fixture produced no error")
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				w.WriteHeader(http.StatusOK)
			}))
			defer server.Close()
			ctx := httptransport.WithSubmissionTracking(context.Background())
			client := *server.Client()
			client.Transport = httptransport.GenerationTransport(ctx, client.Transport)
			response, errDo := client.Post(server.URL, "application/json", strings.NewReader(`{}`))
			if errDo != nil {
				t.Fatal(errDo)
			}
			if errClose := response.Body.Close(); errClose != nil {
				t.Fatal(errClose)
			}
			classified := httptransport.ClassifySubmissionError(ctx, tc.err)
			if tc.provider {
				if classified != tc.err {
					t.Fatalf("parsed provider error changed: %v", classified)
				}
			} else if !httptransport.IsOutcomeUnknown(classified) {
				t.Fatalf("local executor error escaped accepted default: %v", classified)
			}
		})
	}
}
