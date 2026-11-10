package executor

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestClaudeExecutorUserAgentFieldsPreserveNativePayload(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		stabilize bool
	}{
		{name: "default"},
		{name: "stabilized", stabilize: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			testClaudeExecutorUserAgentFields(t, testCase.stabilize)
		})
	}
}

func testClaudeExecutorUserAgentFields(t *testing.T, stabilize bool) {
	t.Helper()
	for _, path := range []string{"messages", "stream", "count_tokens"} {
		t.Run(path, func(t *testing.T) {
			doc := ojson.NewDoc([]byte(`{"model":"claude-sonnet-4-6","system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.312; cc_entrypoint=cli; cch=00000;"},{"type":"text","text":"Stable caller instructions","cache_control":{"type":"ephemeral","ttl":"1h"}}],"messages":[{"role":"user","content":[{"type":"text","text":"Check the task","cache_control":{"type":"ephemeral","ttl":"1h"}}]}],"tools":[{"name":"Read","description":"Read a file","input_schema":{"type":"object","properties":{}}}]}`))
			if path != "count_tokens" {
				if errSet := doc.Set("metadata.user_id", claudeNativeHelperUserID); errSet != nil {
					t.Fatal(errSet)
				}
			} else if errDelete := doc.Delete("system"); errDelete != nil {
				t.Fatal(errDelete)
			}
			payload := doc.Bytes()
			var nativeSystem string
			for _, testCase := range []struct {
				userAgent, stabilizedUserAgent string
			}{
				{userAgent: "claude-cli/2.1.353 (external, cli)", stabilizedUserAgent: "claude-cli/2.1.353 (external, cli)"},
				{userAgent: "claude-cli/2.1.353 (external, cli, workload/cron)", stabilizedUserAgent: "claude-cli/2.1.353 (external, cli, workload/cron)"},
				{userAgent: "claude-cli/2.1.353 (external, cli, future-feature/value, agent-sdk/0.2.133)", stabilizedUserAgent: "claude-cli/2.1.353 (external, cli, agent-sdk/0.2.133, future-feature/value)"},
				{userAgent: "claude-cli/2.1.353 (external, cli, request-phase/background, agent-sdk/0.2.133, workload/cron)", stabilizedUserAgent: "claude-cli/2.1.353 (external, cli, agent-sdk/0.2.133, request-phase/background, workload/cron)"},
			} {
				t.Run(testCase.userAgent, func(t *testing.T) {
					var upstreamBody []byte
					var upstreamHeaders http.Header
					transport := roundTripperFunc(func(request *http.Request) (*http.Response, error) {
						body, errRead := io.ReadAll(request.Body)
						if errRead != nil {
							return nil, errRead
						}
						upstreamBody, upstreamHeaders = body, request.Header.Clone()
						contentType := "application/json"
						responseBody := `{"id":"msg_ua_fields","type":"message","role":"assistant","model":"claude-sonnet-4-6","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`
						if path == "count_tokens" {
							responseBody = `{"input_tokens":1}`
						} else if path == "stream" {
							contentType = "text/event-stream"
							responseBody = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_ua_fields\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-sonnet-4-6\",\"content\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
						}
						return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {contentType}}, Body: io.NopCloser(strings.NewReader(responseBody)), Request: request}, nil
					})
					ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(transport))
					headers := http.Header{}
					headers.Set("User-Agent", testCase.userAgent)
					headers.Set("X-App", "cli")
					headers.Set("Anthropic-Beta", "claude-code-20250219,oauth-2025-04-20,extended-cache-ttl-2025-04-11")
					auth := &cliproxyauth.Auth{ID: "ua-fields-" + t.Name(), Metadata: claudeOAuthTestMetadata(), Attributes: map[string]string{"api_key": "sk-ant-oat-ua-fields"}}
					executor := NewClaudeExecutor(&config.Config{ClaudeHeaderDefaults: config.ClaudeHeaderDefaults{
						UserAgent: "claude-cli/2.1.353 (external, cli)", StabilizeDeviceProfile: &stabilize,
					}})
					request := cliproxyexecutor.Request{Model: "claude-sonnet-4-6", Payload: payload}
					options := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, OriginalRequest: payload, Headers: headers}
					switch path {
					case "messages":
						if _, errExecute := executor.Execute(ctx, auth, request, options); errExecute != nil {
							t.Fatal(errExecute)
						}
					case "stream":
						result, errStream := executor.ExecuteStream(ctx, auth, request, options)
						if errStream != nil {
							t.Fatal(errStream)
						}
						for chunk := range result.Chunks {
							if chunk.Err != nil {
								t.Fatal(chunk.Err)
							}
						}
					case "count_tokens":
						if _, errCount := executor.CountTokens(ctx, auth, request, options); errCount != nil {
							t.Fatal(errCount)
						}
					}
					if upstreamBody == nil {
						t.Fatal("upstream was not called")
					}
					for _, field := range []string{"messages", "tools"} {
						if got, want := ojson.GetBytes(upstreamBody, field).Raw, ojson.GetBytes(payload, field).Raw; got != want {
							t.Fatalf("native %s changed\n got: %s\nwant: %s", field, got, want)
						}
					}
					// Signing may update billing, but both UAs must produce the same prefix.
					system := ojson.GetBytes(upstreamBody, "system")
					if path == "count_tokens" {
						if system.Exists() {
							t.Fatalf("native count_tokens gained a system prompt: %s", system.Raw)
						}
					} else if len(system.Array()) != 2 || system.Get("1").Raw != ojson.GetBytes(payload, "system.1").Raw {
						t.Fatalf("native system blocks were rebuilt: %s", system.Raw)
					}
					if nativeSystem == "" {
						nativeSystem = system.Raw
					} else if system.Raw != nativeSystem {
						t.Fatalf("extended UA changed the native prefix\n got: %s\nwant: %s", system.Raw, nativeSystem)
					}
					wantUserAgent := testCase.userAgent
					if stabilize {
						wantUserAgent = testCase.stabilizedUserAgent
					}
					if got := upstreamHeaders.Get("User-Agent"); got != wantUserAgent {
						t.Fatalf("upstream UA = %q, want %q", got, wantUserAgent)
					}
				})
			}
		})
	}
}
