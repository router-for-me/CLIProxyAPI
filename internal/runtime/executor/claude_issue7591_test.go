package executor

import (
	"context"
	"fmt"
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

func claudeIssue7591Payload(reqModel, advisorModel string, stream bool) []byte {
	streamField := ""
	if stream {
		streamField = `"stream":true,`
	}
	return []byte(fmt.Sprintf(`{`+
		`"model":%q,`+
		`"max_tokens":256,`+
		`%s`+
		`"messages":[{"role":"user","content":"say ok"}],`+
		`"tools":[`+
		`{"name":"Read","input_schema":{"type":"object"}},`+
		`{"type":"advisor_20260301","name":"advisor","model":%q,"defer_loading":true},`+
		`{"type":"custom_tool","name":"helper","model":"keep-me(high)"}`+
		`]}`, reqModel, streamField, advisorModel))
}

func TestClaudeIssue7591AdvisorToolModelSuffixStripped(t *testing.T) {
	cases := []struct {
		name             string
		reqModel         string
		advisorModel     string
		wantTopModel     string
		wantAdvisorModel string
	}{
		{
			name:             "same model with level suffix",
			reqModel:         "claude-opus-5-5(high)",
			advisorModel:     "claude-opus-5-5(high)",
			wantTopModel:     "claude-opus-5-5",
			wantAdvisorModel: "claude-opus-5-5",
		},
		{
			name:             "distinct advisor model keeps its own base model",
			reqModel:         "claude-sonnet-5-5(medium)",
			advisorModel:     "claude-opus-5-5(high)",
			wantTopModel:     "claude-sonnet-5-5",
			wantAdvisorModel: "claude-opus-5-5",
		},
		{
			name:             "numeric budget suffix on advisor",
			reqModel:         "claude-opus-5-5(16384)",
			advisorModel:     "claude-haiku-4-5-20251001(8192)",
			wantTopModel:     "claude-opus-5-5",
			wantAdvisorModel: "claude-haiku-4-5-20251001",
		},
		{
			name:             "advisor without suffix stays unchanged",
			reqModel:         "claude-opus-5-5(high)",
			advisorModel:     "claude-haiku-4-5-20251001",
			wantTopModel:     "claude-opus-5-5",
			wantAdvisorModel: "claude-haiku-4-5-20251001",
		},
	}

	authConfigs := []struct {
		name      string
		newAuth   func() *cliproxyauth.Auth
		wantAuthz string
		wantKey   string
	}{
		{
			name: "api_key",
			newAuth: func() *cliproxyauth.Auth {
				return &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "key-7591"}}
			},
			wantKey: "key-7591",
		},
		{
			name: "oauth",
			newAuth: func() *cliproxyauth.Auth {
				return &cliproxyauth.Auth{
					Metadata: map[string]any{
						"access_token": "sk-ant-oat-issue-7591",
						"account_uuid": "11111111-1111-4111-8111-111111111111",
					},
				}
			},
			wantAuthz: "Bearer sk-ant-oat-issue-7591",
		},
	}

	for _, authCfg := range authConfigs {
		for _, tc := range cases {
			for _, method := range []string{"Execute", "ExecuteStream", "CountTokens"} {
				t.Run(authCfg.name+"/"+tc.name+"/"+method, func(t *testing.T) {
					var upstreamBody []byte
					var upstreamHeaders http.Header
					transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
						upstreamBody, _ = io.ReadAll(req.Body)
						upstreamHeaders = req.Header.Clone()
						contentType := "application/json"
						body := fmt.Sprintf(`{"id":"msg_7591","type":"message","role":"assistant","model":%q,"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`, tc.wantTopModel)
						switch method {
						case "ExecuteStream":
							contentType = "text/event-stream"
							body = fmt.Sprintf(
								"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_7591\",\"type\":\"message\",\"role\":\"assistant\",\"model\":%q,\"content\":[],\"stop_reason\":null,\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n"+
									"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n"+
									"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\n"+
									"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n"+
									"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\",\"stop_sequence\":null},\"usage\":{\"output_tokens\":1}}\n\n"+
									"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
								tc.wantTopModel,
							)
						case "CountTokens":
							body = `{"input_tokens":42}`
						}
						return &http.Response{
							StatusCode: http.StatusOK,
							Header:     http.Header{"Content-Type": []string{contentType}},
							Body:       io.NopCloser(strings.NewReader(body)),
							Request:    req,
						}, nil
					})

					ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(transport))
					executor := NewClaudeExecutor(&config.Config{})
					auth := authCfg.newAuth()
					stream := method == "ExecuteStream"
					req := cliproxyexecutor.Request{
						Model:   tc.reqModel,
						Payload: claudeIssue7591Payload(tc.reqModel, tc.advisorModel, stream),
					}
					opts := cliproxyexecutor.Options{
						SourceFormat: sdktranslator.FormatClaude,
						Stream:       stream,
					}

					switch method {
					case "Execute":
						if _, err := executor.Execute(ctx, auth, req, opts); err != nil {
							t.Fatalf("Execute() error = %v", err)
						}
					case "ExecuteStream":
						res, err := executor.ExecuteStream(ctx, auth, req, opts)
						if err != nil {
							t.Fatalf("ExecuteStream() error = %v", err)
						}
						for chunk := range res.Chunks {
							if chunk.Err != nil {
								t.Fatalf("ExecuteStream() chunk error = %v", chunk.Err)
							}
						}
					case "CountTokens":
						if _, err := executor.CountTokens(ctx, auth, req, opts); err != nil {
							t.Fatalf("CountTokens() error = %v", err)
						}
					}

					if authCfg.wantAuthz != "" {
						if got := upstreamHeaders.Get("Authorization"); got != authCfg.wantAuthz {
							t.Fatalf("Authorization = %q, want %q", got, authCfg.wantAuthz)
						}
						if got := upstreamHeaders.Get("X-Api-Key"); got != "" {
							t.Fatalf("X-Api-Key = %q, want empty for OAuth", got)
						}
					} else {
						if got := upstreamHeaders.Get("X-Api-Key"); got != authCfg.wantKey {
							t.Fatalf("X-Api-Key = %q, want %q", got, authCfg.wantKey)
						}
						if got := upstreamHeaders.Get("Authorization"); got != "" {
							t.Fatalf("Authorization = %q, want empty for API key", got)
						}
					}
					if got := ojson.GetBytes(upstreamBody, "model").String(); got != tc.wantTopModel {
						t.Fatalf("upstream model = %q, want %q", got, tc.wantTopModel)
					}
					if got := ojson.GetBytes(upstreamBody, "tools.1.model").String(); got != tc.wantAdvisorModel {
						t.Fatalf("upstream tools.1.model = %q, want %q; tools=%s", got, tc.wantAdvisorModel, ojson.GetBytes(upstreamBody, "tools").Raw)
					}
					if got := ojson.GetBytes(upstreamBody, "tools.2.model").String(); got != "keep-me(high)" {
						t.Fatalf("non-advisor tools.2.model = %q, want %q", got, "keep-me(high)")
					}
				})
			}
		}
	}
}

func TestClaudeIssue7591LocalCountTokens(t *testing.T) {
	executor := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "key-7591-local",
		"base_url": "https://custom.gateway.example",
	}}
	req := cliproxyexecutor.Request{
		Model:   "claude-opus-5-5(high)",
		Payload: claudeIssue7591Payload("claude-opus-5-5(high)", "claude-opus-5-5(high)", false),
	}
	resp, err := executor.CountTokens(context.Background(), auth, req, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude})
	if err != nil {
		t.Fatalf("local CountTokens() error = %v", err)
	}
	if len(resp.Payload) == 0 {
		t.Fatal("local CountTokens() returned empty payload")
	}
}

func TestClaudeIssue7591PayloadRuleWinsOverAdvisorSanitizer(t *testing.T) {
	const configuredOverride = "claude-custom-override(high)"
	for _, method := range []string{"Execute", "ExecuteStream", "CountTokens"} {
		t.Run(method, func(t *testing.T) {
			var upstreamBody []byte
			stream := method == "ExecuteStream"
			transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				upstreamBody, _ = io.ReadAll(req.Body)
				contentType := "application/json"
				body := `{"id":"msg_7591","type":"message","role":"assistant","model":"claude-opus-5-5","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`
				switch method {
				case "ExecuteStream":
					contentType = "text/event-stream"
					body = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_7591\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-opus-5-5\",\"content\":[],\"stop_reason\":null,\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n" +
						"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\",\"stop_sequence\":null},\"usage\":{\"output_tokens\":1}}\n\n" +
						"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
				case "CountTokens":
					body = `{"input_tokens":42}`
				}
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{contentType}},
					Body:       io.NopCloser(strings.NewReader(body)),
					Request:    req,
				}, nil
			})
			ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(transport))
			cfg := &config.Config{
				Payload: config.PayloadConfig{
					Override: []config.PayloadRule{{
						Models: []config.PayloadModelRule{{Name: "claude-opus-5-5"}},
						Params: map[string]any{"tools.1.model": configuredOverride},
					}},
				},
			}
			executor := NewClaudeExecutor(cfg)
			auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "key-7591"}}
			req := cliproxyexecutor.Request{
				Model:   "claude-opus-5-5(high)",
				Payload: claudeIssue7591Payload("claude-opus-5-5(high)", "claude-opus-5-5(high)", stream),
			}
			opts := cliproxyexecutor.Options{
				SourceFormat: sdktranslator.FormatClaude,
				Stream:       stream,
			}
			switch method {
			case "Execute":
				if _, err := executor.Execute(ctx, auth, req, opts); err != nil {
					t.Fatalf("Execute() error = %v", err)
				}
			case "ExecuteStream":
				res, err := executor.ExecuteStream(ctx, auth, req, opts)
				if err != nil {
					t.Fatalf("ExecuteStream() error = %v", err)
				}
				for chunk := range res.Chunks {
					if chunk.Err != nil {
						t.Fatalf("ExecuteStream() chunk error = %v", chunk.Err)
					}
				}
			case "CountTokens":
				if _, err := executor.CountTokens(ctx, auth, req, opts); err != nil {
					t.Fatalf("CountTokens() error = %v", err)
				}
			}
			if got := ojson.GetBytes(upstreamBody, "tools.1.model").String(); got != configuredOverride {
				t.Fatalf("payload override tools.1.model = %q, want %q", got, configuredOverride)
			}
		})
	}
}

func TestSanitizeClaudeAdvisorToolModelsDoc_EdgeCases(t *testing.T) {
	if sanitizeClaudeAdvisorToolModelsDoc(nil) {
		t.Fatal("nil doc should return false")
	}

	noTools := ojson.NewDocString(`{"model":"claude-opus-5-5"}`)
	if sanitizeClaudeAdvisorToolModelsDoc(noTools) {
		t.Fatal("missing tools should return false")
	}

	doc := ojson.NewDocString(`{
		"tools": [
			{"type":"advisor_20260301","name":"adv1","model":" claude-opus-5-5(high) "},
			{"type":"ADVISOR_20260401","name":"adv2","model":"claude-sonnet-5-5(custom)"},
			{"type":"advisor_20260301","name":"adv3","model":"claude-haiku-4-5-20251001"},
			{"type":"advisor_20260301","name":"adv4","model":""},
			{"type":"advisor_20260301","name":"adv5","model":"   "},
			{"type":"advisor_20260301","name":"adv6","model":"(high)"},
			{"type":"advisor_20260301","name":"adv7","model":123},
			{"type":"advisor_20260301","name":"adv8","model":null},
			{"type":"custom_tool","name":"helper","model":"keep-me(high)"}
		]
	}`)
	if !sanitizeClaudeAdvisorToolModelsDoc(doc) {
		t.Fatal("expected advisor tool models to be sanitized")
	}
	if got := doc.Get("tools.0.model").String(); got != "claude-opus-5-5" {
		t.Fatalf("tools.0.model = %q, want %q", got, "claude-opus-5-5")
	}
	if got := doc.Get("tools.1.model").String(); got != "claude-sonnet-5-5" {
		t.Fatalf("tools.1.model = %q, want %q", got, "claude-sonnet-5-5")
	}
	if got := doc.Get("tools.2.model").String(); got != "claude-haiku-4-5-20251001" {
		t.Fatalf("tools.2.model = %q, want %q", got, "claude-haiku-4-5-20251001")
	}
	if got := doc.Get("tools.3.model").String(); got != "" {
		t.Fatalf("tools.3.model = %q, want empty", got)
	}
	if got := doc.Get("tools.4.model").String(); got != "   " {
		t.Fatalf("tools.4.model = %q, want whitespace preserved", got)
	}
	if got := doc.Get("tools.5.model").String(); got != "(high)" {
		t.Fatalf("tools.5.model = %q, want %q", got, "(high)")
	}
	if got := doc.Get("tools.6.model").Int(); got != 123 {
		t.Fatalf("tools.6.model = %d, want 123", got)
	}
	if doc.Get("tools.7.model").Type != ojson.Null {
		t.Fatalf("tools.7.model type = %v, want Null", doc.Get("tools.7.model").Type)
	}
	if got := doc.Get("tools.8.model").String(); got != "keep-me(high)" {
		t.Fatalf("tools.8.model = %q, want %q", got, "keep-me(high)")
	}
}
