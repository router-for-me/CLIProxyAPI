package executor

import (
	"net/http"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestClaudeExecutorIssue7066NativeHeaders(t *testing.T) {
	const model = "claude-opus-5-5"
	doc := ojson.NewDocString(`{"model":"claude-opus-5-5","max_tokens":64,"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.321.d7b; cc_entrypoint=cli; cch=00000;"},{"type":"text","text":"You are Claude Code, Anthropic's official CLI for Claude."}],"messages":[{"role":"user","content":"hello"}]}`)
	_ = doc.Set("metadata.user_id", claudeNativeHelperUserID)
	payload := doc.Bytes()
	for _, method := range []string{"Execute", "ExecuteStream", "CountTokens"} {
		for _, oauth := range []bool{false, true} {
			name := method + "/api-key"
			if oauth {
				name = method + "/oauth"
			}
			t.Run(name, func(t *testing.T) {
				incoming := claudeNativeHelperHeaders("claude-code-20250219,"+claudeNativeHelperCoreBetas, "gzip, deflate, br, zstd")
				reported := http.Header{
					"x-claude-code-sandy-plover": {"native-state"},
					"anthropic-dispatch-id":      {"11111111-2222-4333-8444-555555555555"},
					"x-claude-code-prompt-id":    {"66666666-7777-4888-8999-aaaaaaaaaaaa"},
					"anthropic-usage-limit":      {"test-limit"},
				}
				// Preserve lowercase keys to exercise case-insensitive source lookup.
				for key, values := range reported {
					incoming[key] = values
				}
				if !helps.DetectClaudeCodeRequest(incoming, payload, method == "CountTokens").Confirmed {
					t.Fatal("fixture must be recognized as native Claude Code")
				}
				auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "key-issue7066"}}
				if oauth {
					auth = claudeNativeHelperOAuthAuth("https://api.anthropic.com")
				}
				_, headers := captureClaudeIssue7000(t, NewClaudeExecutor(&config.Config{}), auth,
					cliproxyexecutor.Request{Model: model, Payload: payload},
					cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, OriginalRequest: payload, Headers: incoming}, method)
				for key, values := range reported {
					if got := helps.HeaderValueCaseInsensitive(headers, key); got != values[0] {
						t.Errorf("%s = %q, want %q", key, got, values[0])
					}
				}
			})
		}
	}
}

func TestApplyClaudeHeadersIssue7066MissingAndOverriddenHeaders(t *testing.T) {
	keys := []string{"X-Claude-Code-Sandy-Plover", "Anthropic-Dispatch-Id", "X-Claude-Code-Prompt-Id", "Anthropic-Usage-Limit"}
	for _, override := range []bool{false, true} {
		name := "absent headers stay absent"
		if override {
			name = "operator overrides caller"
		}
		t.Run(name, func(t *testing.T) {
			auth := fixtureAuth()
			incoming := http.Header{}
			want := ""
			if override {
				want = "configured-value"
				for _, key := range keys {
					incoming.Set(key, "caller-value")
					auth.Attributes["header:"+key] = want
				}
			}
			req := newClaudeHeaderTestRequest(t, incoming)
			if errHeaders := applyClaudeHeaders(req, auth, auth.Attributes[cliproxyauth.AttributeAPIKey], false, nil,
				[]byte(`{"model":"claude-opus-5-5"}`), &config.Config{}, incoming, true); errHeaders != nil {
				t.Fatal(errHeaders)
			}
			for _, key := range keys {
				if got := req.Header.Get(key); got != want {
					t.Errorf("%s = %q, want %q", key, got, want)
				}
			}
		})
	}
}
