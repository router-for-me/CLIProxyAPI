package executor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func TestClaudeExecutor_SubagentExplicitCacheTTL(t *testing.T) {
	for _, test := range []struct {
		name    string
		native  bool
		header  bool
		marker  bool
		ttl     bool
		ttlPath string
		beta    bool
		probe   bool
		wantTTL bool
	}{
		{name: "native subagent", native: true, header: true, marker: true, ttl: true, beta: true, wantTTL: true},
		{name: "header only subagent", native: true, header: true, ttl: true, beta: true, wantTTL: true},
		{name: "billing only subagent", native: true, marker: true, ttl: true, beta: true, wantTTL: true},
		{name: "tool breakpoint only", native: true, header: true, marker: true, ttl: true, ttlPath: "tools.0", beta: true, wantTTL: true},
		{name: "system breakpoint only", native: true, header: true, marker: true, ttl: true, ttlPath: "system.1", beta: true, wantTTL: true},
		{name: "message breakpoint only", native: true, header: true, marker: true, ttl: true, ttlPath: "messages.0.content.0", beta: true, wantTTL: true},
		{name: "no explicit TTL", native: true, header: true, marker: true},
		{name: "missing beta", native: true, header: true, marker: true, ttl: true},
		{name: "beta without TTL", native: true, header: true, marker: true, beta: true},
		{name: "unconfirmed caller", header: true, marker: true, ttl: true, beta: true},
		{name: "probe", native: true, header: true, marker: true, ttl: true, beta: true, probe: true},
		{name: "main conversation", native: true, ttl: true, beta: true, wantTTL: true},
	} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", test.name, stream), func(t *testing.T) {
				payload := []byte(`{"model":"claude-sonnet-5","max_tokens":1024,"tools":[{"name":"read_file","input_schema":{"type":"object"},"cache_control":{"type":"ephemeral"}}],"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.258; cc_entrypoint=cli; cch=00000;"},{"type":"text","text":"Inspect the repository.","cache_control":{"type":"ephemeral"}}],"messages":[{"role":"user","content":[{"type":"text","text":"Review the code.","cache_control":{"type":"ephemeral"}}]}]}`)
				paths := []string{"tools.0", "system.1", "messages.0.content.0"}
				if test.ttlPath != "" {
					for _, path := range paths {
						if path != test.ttlPath {
							payload, _ = sjson.DeleteBytes(payload, path+".cache_control")
						}
					}
					paths = []string{test.ttlPath}
				}
				if test.ttl {
					for _, path := range paths {
						payload, _ = sjson.SetBytes(payload, path+".cache_control.ttl", "1h")
					}
				}
				if test.marker {
					payload, _ = sjson.SetBytes(payload, "system.0.text", gjson.GetBytes(payload, "system.0.text").String()+" cc_is_subagent=true;")
				}
				if test.probe {
					payload, _ = sjson.SetBytes(payload, "max_tokens", 1)
					payload, _ = sjson.DeleteBytes(payload, "tools")
					payload, _ = sjson.SetBytes(payload, "messages.0.content.0.text", "quota")
				}
				if got := helps.IsClaudeProbeOrHelperRequest(payload); got != test.probe {
					t.Fatalf("fixture probe detection = %v, want %v", got, test.probe)
				}
				headers := http.Header{}
				headers.Set("Anthropic-Beta", claudeCodeBeta)
				if test.beta {
					headers.Add("Anthropic-Beta", claudeExtendedCacheTTLBeta)
				}
				if test.header {
					headers.Set("X-Claude-Code-Agent-Id", "test-subagent")
				}
				if test.native {
					headers.Set("User-Agent", "claude-cli/2.1.258 (external, cli)")
					headers.Set("X-App", "cli")
					payload, _ = sjson.SetBytes(payload, "metadata.user_id", `{"device_id":"0000000000000000000000000000000000000000000000000000000000000000","account_uuid":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa","session_id":"11111111-2222-4333-8444-555555555555"}`)
				}
				if got := helps.DetectClaudeCodeRequest(headers, payload, false).Confirmed; got != test.native {
					t.Fatalf("fixture native detection = %v, want %v", got, test.native)
				}

				var upstreamBody []byte
				var upstreamHeaders http.Header
				transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
					var errRead error
					upstreamBody, errRead = io.ReadAll(req.Body)
					if errRead != nil {
						return nil, errRead
					}
					upstreamHeaders = req.Header.Clone()
					contentType := "application/json"
					response := `{"id":"msg_test","type":"message","role":"assistant","model":"claude-sonnet-5","content":[],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":0}}`
					if stream {
						contentType = "text/event-stream"
						response = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":" + response + "}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
					}
					return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {contentType}}, Body: io.NopCloser(strings.NewReader(response)), Request: req}, nil
				})
				ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(transport))
				auth := &cliproxyauth.Auth{ID: "subagent-cache-ttl", Metadata: claudeOAuthTestMetadata(), Attributes: map[string]string{"api_key": "sk-ant-oat-subagent-cache-ttl"}}
				executor := NewClaudeExecutor(&config.Config{})
				request := cliproxyexecutor.Request{Model: "claude-sonnet-5", Payload: payload}
				options := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, Headers: headers, Stream: stream}
				if stream {
					result, errStream := executor.ExecuteStream(ctx, auth, request, options)
					if errStream != nil {
						t.Fatal(errStream)
					}
					for chunk := range result.Chunks {
						if chunk.Err != nil {
							t.Fatal(chunk.Err)
						}
					}
				} else if _, errExecute := executor.Execute(ctx, auth, request, options); errExecute != nil {
					t.Fatal(errExecute)
				}
				if upstreamBody == nil {
					t.Fatal("request did not reach upstream")
				}
				betas := helps.HeaderValueCaseInsensitive(upstreamHeaders, "Anthropic-Beta")
				if got := strings.Contains(betas, claudeExtendedCacheTTLBeta); got != test.wantTTL {
					t.Errorf("extended cache beta = %v, want %v; betas=%s", got, test.wantTTL, betas)
				}
				if test.wantTTL {
					for _, path := range paths {
						if got := gjson.GetBytes(upstreamBody, path+".cache_control.ttl").String(); got != "1h" {
							t.Errorf("%s cache TTL = %q, want 1h", path, got)
						}
					}
				} else {
					forEachClaudeCacheControlBlock(upstreamBody, func(path string, block gjson.Result) {
						if got := block.Get("cache_control.ttl"); got.Exists() {
							t.Errorf("%s cache TTL = %s, want omitted", path, got.Raw)
						}
					})
				}
			})
		}
	}
}
