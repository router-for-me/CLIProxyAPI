package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

// TestClaudeStreamErrorEventFinalizesApplyPatch: an OpenAI Responses stream with an unfinished
// apply_patch tool call that Claude ends with an SSE error event ends the same way as when the
// stream simply stops: one sanitized response.failed and the gateway error, nothing raw. The
// failure's usage keeps the input tokens message_start reported.
func TestClaudeStreamErrorEventFinalizesApplyPatch(t *testing.T) {
	for _, provider := range []string{"claude", "claude-oauth"} {
		t.Run(provider, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, errReadAll := io.ReadAll(r.Body)
				if errReadAll != nil {
					t.Error(errReadAll)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fixture := task6ProviderFixture(provider, "eof", gjson.GetBytes(body, "tools.0.name").String())
				fixture = strings.TrimRight(fixture, "\n") + "\n\n" +
					"event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"Overloaded\"}}\n\n"
				_, _ = io.WriteString(w, fixture)
			}))
			defer server.Close()
			exec := task6Executor(provider)
			auth := &cliproxyauth.Auth{ID: "task6", Provider: exec.Identifier(), Attributes: map[string]string{"api_key": "test", "base_url": server.URL}}
			if provider == "claude-oauth" {
				auth.Attributes["api_key"] = "sk-ant-oat-test"
				auth.Metadata = map[string]any{"account_uuid": "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"}
			}
			checkUsage := task6CaptureFailureUsageWithCheck(t, auth.ID, func(record usage.Record) {
				if record.Detail.InputTokens != 5 {
					t.Errorf("failure usage input tokens = %d, want 5 from message_start", record.Detail.InputTokens)
				}
			})
			defer checkUsage()
			req := cliproxyexecutor.Request{Model: "claude-sonnet-4-6", Payload: []byte(task6PatchRequest)}
			opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, OriginalRequest: req.Payload}
			stream, errExecuteStream := exec.ExecuteStream(context.Background(), auth, req, opts)
			if errExecuteStream != nil {
				t.Fatal(errExecuteStream)
			}
			assertTask6FailedStream(t, stream.Chunks)
		})
	}
}
