package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/translator"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

type capturedCodexRequest struct {
	path        string
	model       gjson.Result
	routingHint string
	hasHint     bool
	serviceTier gjson.Result
}

func newCodexRoutingHintServer(t *testing.T, captured *capturedCodexRequest) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		captured.path = r.URL.Path
		captured.model = gjson.GetBytes(body, "model")
		_, captured.hasHint = r.Header[http.CanonicalHeaderKey(codexRoutingHintHeader)]
		captured.routingHint = r.Header.Get(codexRoutingHintHeader)
		captured.serviceTier = gjson.GetBytes(body, "service_tier")
		if r.URL.Path == "/responses/compact" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"cmp_1","object":"response.compaction","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"type":"response.completed","response":{"id":"resp_1","object":"response","status":"completed","model":"gpt-5.5","service_tier":"default","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}` + "\n\n"))
	}))
}

func codexOAuthTestAuth(baseURL string) *cliproxyauth.Auth {
	return &cliproxyauth.Auth{
		Provider:   "codex",
		Attributes: map[string]string{"base_url": baseURL},
		Metadata:   map[string]any{"type": "codex", "access_token": "oauth-token", "account_id": "acct"},
	}
}

func codexAPIKeyTestAuth(baseURL string) *cliproxyauth.Auth {
	return &cliproxyauth.Auth{
		Provider:   "codex",
		Attributes: map[string]string{"base_url": baseURL, "api_key": "sk-test"},
	}
}

// A Claude Messages request with speed=fast (Claude Code Fast Mode) must reach
// the ChatGPT Codex backend the way native Codex 0.155 sends Fast: the body
// carries service_tier=priority and the routing hint names the same tier.
func TestCodexExecutorRoutingHintCarriesRequestedTier(t *testing.T) {
	const fastClaude = `{"model":"gpt-5.5","max_tokens":64,"speed":"fast","messages":[{"role":"user","content":"hi"}]}`
	const plainClaude = `{"model":"gpt-5.5","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`
	const aliasedClaude = `{"model":"client-alias","max_tokens":64,"speed":"fast","messages":[{"role":"user","content":"hi"}]}`

	cases := []struct {
		name     string
		apiKey   bool
		stream   bool
		model    string
		payload  string
		wantHint string
		wantTier string
	}{
		{name: "oauth stream fast", stream: true, payload: fastClaude, wantHint: "model=gpt-5.5;tier=priority", wantTier: "priority"},
		{name: "oauth non-stream fast", stream: false, payload: fastClaude, wantHint: "model=gpt-5.5;tier=priority", wantTier: "priority"},
		{name: "oauth stream alias and thinking suffix", stream: true, model: "gpt-5.5(low)", payload: aliasedClaude, wantHint: "model=gpt-5.5;tier=priority", wantTier: "priority"},
		{name: "oauth non-stream alias and thinking suffix", model: "gpt-5.5(low)", payload: aliasedClaude, wantHint: "model=gpt-5.5;tier=priority", wantTier: "priority"},
		{name: "oauth stream standard", stream: true, payload: plainClaude, wantHint: "model=gpt-5.5"},
		{name: "api key keeps body tier without hint", apiKey: true, stream: true, payload: fastClaude, wantTier: "priority"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var captured capturedCodexRequest
			server := newCodexRoutingHintServer(t, &captured)
			defer server.Close()

			auth := codexOAuthTestAuth(server.URL)
			if tc.apiKey {
				auth = codexAPIKeyTestAuth(server.URL)
			}
			executor := NewCodexExecutor(&config.Config{})
			model := tc.model
			if model == "" {
				model = "gpt-5.5"
			}
			req := cliproxyexecutor.Request{Model: model, Payload: []byte(tc.payload)}
			opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("claude"), Stream: tc.stream}

			if tc.stream {
				result, err := executor.ExecuteStream(context.Background(), auth, req, opts)
				if err != nil {
					t.Fatalf("ExecuteStream error: %v", err)
				}
				for chunk := range result.Chunks {
					if chunk.Err != nil {
						t.Fatalf("stream chunk error: %v", chunk.Err)
					}
				}
			} else if _, err := executor.Execute(context.Background(), auth, req, opts); err != nil {
				t.Fatalf("Execute error: %v", err)
			}

			if captured.model.Type != gjson.String || captured.model.String() != "gpt-5.5" {
				t.Fatalf("body model = %s, want gpt-5.5", captured.model.Raw)
			}
			if tc.wantHint == "" {
				if captured.hasHint {
					t.Fatalf("routing hint = %q, want header absent", captured.routingHint)
				}
			} else if captured.routingHint != tc.wantHint {
				t.Fatalf("routing hint = %q, want %q", captured.routingHint, tc.wantHint)
			}
			if tc.wantTier == "" {
				if captured.serviceTier.Exists() {
					t.Fatalf("body service_tier = %s, want absent", captured.serviceTier.Raw)
				}
			} else if captured.serviceTier.String() != tc.wantTier {
				t.Fatalf("body service_tier = %q, want %q", captured.serviceTier.String(), tc.wantTier)
			}
		})
	}
}

func TestCodexExecutorCompactRoutingHintCarriesRequestedTier(t *testing.T) {
	var captured capturedCodexRequest
	server := newCodexRoutingHintServer(t, &captured)
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	_, err := executor.Execute(context.Background(), codexOAuthTestAuth(server.URL), cliproxyexecutor.Request{
		Model:   "gpt-5.5(low)",
		Payload: []byte(`{"model":"client-alias","service_tier":"priority","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Alt:          "responses/compact",
	})
	if err != nil {
		t.Fatalf("compact Execute error: %v", err)
	}
	if captured.path != "/responses/compact" {
		t.Fatalf("upstream path = %q, want /responses/compact", captured.path)
	}
	if captured.model.Type != gjson.String || captured.model.String() != "gpt-5.5" {
		t.Fatalf("body model = %s, want gpt-5.5", captured.model.Raw)
	}
	if captured.serviceTier.String() != "priority" {
		t.Fatalf("body service_tier = %q, want priority", captured.serviceTier.String())
	}
	if captured.routingHint != "model="+captured.model.String()+";tier="+captured.serviceTier.String() {
		t.Fatalf("routing hint = %q, does not match body model %q and tier %q", captured.routingHint, captured.model.String(), captured.serviceTier.String())
	}
}

func TestCodexExecutorOperatorRoutingHintRuleWins(t *testing.T) {
	var captured capturedCodexRequest
	server := newCodexRoutingHintServer(t, &captured)
	defer server.Close()

	auth := codexOAuthTestAuth(server.URL)
	auth.Attributes["header:X-Codex-Routing-Hint"] = "model=operator-pinned"
	clientHeaders := http.Header{}
	clientHeaders.Set(codexRoutingHintHeader, "model=gpt-5.4-client")
	executor := NewCodexExecutor(&config.Config{})
	result, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.5",
		Payload: []byte(`{"model":"gpt-5.5","max_tokens":64,"speed":"fast","messages":[{"role":"user","content":"hi"}]}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("claude"), Stream: true, Headers: clientHeaders})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error: %v", chunk.Err)
		}
	}
	if captured.routingHint != "model=operator-pinned" {
		t.Fatalf("routing hint = %q, want operator rule value", captured.routingHint)
	}
}

func TestApplyCodexRoutingHint(t *testing.T) {
	oauth := codexOAuthTestAuth("")

	t.Run("replaces a hint that did not come from an operator rule", func(t *testing.T) {
		headers := http.Header{}
		headers.Set(codexRoutingHintHeader, "model=gpt-5.4-client")
		applyCodexRoutingHint(context.Background(), headers, oauth, "gpt-5.5", []byte(`{"model":"gpt-5.5","service_tier":"priority"}`), nil)
		if got := headers.Get(codexRoutingHintHeader); got != "model=gpt-5.5;tier=priority" {
			t.Fatalf("routing hint = %q, want %q", got, "model=gpt-5.5;tier=priority")
		}
	})

	t.Run("keeps the value from an operator auth header rule", func(t *testing.T) {
		operator := codexOAuthTestAuth("")
		operator.Attributes["header:x-codex-routing-hint"] = "model=operator"
		headers := http.Header{}
		headers.Set(codexRoutingHintHeader, "model=operator")
		applyCodexRoutingHint(context.Background(), headers, operator, "gpt-5.5", []byte(`{"model":"gpt-5.5","service_tier":"priority"}`), nil)
		if got := headers.Get(codexRoutingHintHeader); got != "model=operator" {
			t.Fatalf("routing hint = %q, want operator value kept", got)
		}
	})

	t.Run("derives the hint when a dynamic operator rule resolves to nothing", func(t *testing.T) {
		operator := codexOAuthTestAuth("")
		operator.Attributes["header:X-Codex-Routing-Hint"] = "$X-Operator-Hint"
		headers := http.Header{}
		headers.Set(codexRoutingHintHeader, "model=gpt-5.4-client")
		applyCodexRoutingHint(context.Background(), headers, operator, "gpt-5.5", []byte(`{"model":"gpt-5.5","service_tier":"priority"}`), nil)
		if got := headers.Get(codexRoutingHintHeader); got != "model=gpt-5.5;tier=priority" {
			t.Fatalf("routing hint = %q, want %q", got, "model=gpt-5.5;tier=priority")
		}
	})

	t.Run("leaves API-key requests untouched", func(t *testing.T) {
		headers := http.Header{}
		headers.Set(codexRoutingHintHeader, "model=client")
		applyCodexRoutingHint(context.Background(), headers, codexAPIKeyTestAuth(""), "gpt-5.5", []byte(`{"model":"gpt-5.5","service_tier":"priority"}`), nil)
		if got := headers.Get(codexRoutingHintHeader); got != "model=client" {
			t.Fatalf("routing hint = %q, want API-key headers unchanged", got)
		}
	})

	t.Run("drops a forwarded hint when the resolved model is empty", func(t *testing.T) {
		headers := http.Header{}
		headers.Set(codexRoutingHintHeader, "model=gpt-5.4-client")
		applyCodexRoutingHint(context.Background(), headers, oauth, "", []byte(`{"model":"gpt-5.5","service_tier":"priority"}`), nil)
		if _, ok := headers[http.CanonicalHeaderKey(codexRoutingHintHeader)]; ok {
			t.Fatalf("routing hint = %q, want header absent", headers.Get(codexRoutingHintHeader))
		}
	})

	t.Run("ignores a non-string tier", func(t *testing.T) {
		headers := http.Header{}
		applyCodexRoutingHint(context.Background(), headers, oauth, "gpt-5.5", []byte(`{"model":"gpt-5.5","service_tier":null}`), nil)
		if got := headers.Get(codexRoutingHintHeader); got != "model=gpt-5.5" {
			t.Fatalf("routing hint = %q, want %q", got, "model=gpt-5.5")
		}
	})
}
