package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/MoeForge/moego/json"
	"github.com/MoeForge/moego/ojson"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

const codexPromptCacheTTLCompleted = `{"type":"response.completed","response":{"id":"resp_1","status":"completed","model":"gpt-5.6","output":[{"type":"message","id":"msg_1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":3,"output_tokens":1,"total_tokens":4}}}`

func codexPromptCacheTTLPayload(options string) []byte {
	payload := `{"model":"gpt-5.6","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}],"prompt_cache_key":"sess-ttl","prompt_cache_retention":"24h"`
	if options != "" {
		payload += `,"prompt_cache_options":` + options
	}
	return []byte(payload + `}`)
}

func codexPromptCacheTTLAPIKeyAuth(baseURL string) *cliproxyauth.Auth {
	return &cliproxyauth.Auth{
		ID:       "codex-apikey-ttl",
		Provider: "codex",
		Attributes: map[string]string{
			"base_url": baseURL,
			"api_key":  "sk-test",
		},
	}
}

func codexPromptCacheTTLOAuthAuth(baseURL string) *cliproxyauth.Auth {
	return &cliproxyauth.Auth{
		ID:         "codex-oauth-ttl",
		Provider:   "codex",
		Attributes: map[string]string{"base_url": baseURL},
		Metadata: map[string]any{
			"type":         "codex",
			"access_token": "oauth-access-token",
			"account_id":   "acct-1",
		},
	}
}

// runCodexPromptCacheTTLRequest sends one request through Execute or ExecuteStream
// and returns the body the upstream received.
func runCodexPromptCacheTTLRequest(t *testing.T, cfg *config.Config, newAuth func(string) *cliproxyauth.Auth, payload []byte, stream bool) map[string]any {
	t.Helper()
	var gotBody map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		gotBody = nil
		_ = json.Unmarshal(raw, &gotBody)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: " + codexPromptCacheTTLCompleted + "\n\n"))
	}))
	defer upstream.Close()

	if cfg == nil {
		cfg = &config.Config{}
	}
	executor := NewCodexExecutor(cfg)
	req := cliproxyexecutor.Request{Model: "gpt-5.6", Payload: payload}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse}
	auth := newAuth(upstream.URL + "/v1")
	if stream {
		opts.Stream = true
		result, errStream := executor.ExecuteStream(context.Background(), auth, req, opts)
		if errStream != nil {
			t.Fatalf("ExecuteStream error: %v", errStream)
		}
		for range result.Chunks {
		}
	} else if _, errExec := executor.Execute(context.Background(), auth, req, opts); errExec != nil {
		t.Fatalf("Execute error: %v", errExec)
	}
	if gotBody == nil {
		t.Fatal("upstream received no JSON body")
	}
	return gotBody
}

func codexPromptCacheTTLModes() []struct {
	name   string
	stream bool
} {
	return []struct {
		name   string
		stream bool
	}{{name: "execute", stream: false}, {name: "stream", stream: true}}
}

func TestCodexAPIKeyForwardsCallerPromptCacheTTL(t *testing.T) {
	for _, mode := range codexPromptCacheTTLModes() {
		t.Run(mode.name, func(t *testing.T) {
			body := runCodexPromptCacheTTLRequest(t, nil, codexPromptCacheTTLAPIKeyAuth, codexPromptCacheTTLPayload(`{"mode":"explicit","ttl":" 12h "}`), mode.stream)

			options, ok := body["prompt_cache_options"].(map[string]any)
			if !ok {
				t.Fatalf("prompt_cache_options = %#v, want an object carrying ttl", body["prompt_cache_options"])
			}
			if len(options) != 1 || options["ttl"] != "12h" {
				t.Fatalf("prompt_cache_options = %#v, want only ttl=12h", options)
			}
			if _, exists := body["prompt_cache_retention"]; exists {
				t.Fatalf("prompt_cache_retention should stay removed, got %#v", body["prompt_cache_retention"])
			}
			if body["prompt_cache_key"] != "sess-ttl" {
				t.Fatalf("prompt_cache_key = %#v, want sess-ttl", body["prompt_cache_key"])
			}
		})
	}
}

func TestCodexAPIKeyDoesNotInjectPromptCacheOptions(t *testing.T) {
	for _, options := range []string{"", `{"mode":"explicit"}`, `{"ttl":""}`, `{"ttl":"   "}`, `{"ttl":null}`, `{"ttl":43200}`, `"12h"`} {
		for _, mode := range codexPromptCacheTTLModes() {
			t.Run(mode.name+"/"+options, func(t *testing.T) {
				body := runCodexPromptCacheTTLRequest(t, nil, codexPromptCacheTTLAPIKeyAuth, codexPromptCacheTTLPayload(options), mode.stream)
				if _, exists := body["prompt_cache_options"]; exists {
					t.Fatalf("prompt_cache_options should be omitted for caller value %q, got %#v", options, body["prompt_cache_options"])
				}
			})
		}
	}
}

func TestCodexOAuthStillStripsPromptCacheOptions(t *testing.T) {
	for _, mode := range codexPromptCacheTTLModes() {
		t.Run(mode.name, func(t *testing.T) {
			body := runCodexPromptCacheTTLRequest(t, nil, codexPromptCacheTTLOAuthAuth, codexPromptCacheTTLPayload(`{"ttl":"12h"}`), mode.stream)
			for _, forbidden := range []string{"prompt_cache_options", "prompt_cache_retention"} {
				if _, exists := body[forbidden]; exists {
					t.Fatalf("field %q should be omitted for OAuth credentials, got %#v", forbidden, body[forbidden])
				}
			}
		})
	}
}

func TestCodexAPIKeyIgnoresPromptCacheTTLFromOtherFormats(t *testing.T) {
	doc := func(body []byte) *ojson.Doc { return ojson.NewDoc(body) }
	auth := codexPromptCacheTTLAPIKeyAuth("https://api.openai.com/v1")
	source := []byte(`{"prompt_cache_options":{"ttl":"12h"}}`)
	for _, from := range []sdktranslator.Format{sdktranslator.FormatClaude, sdktranslator.FormatOpenAI, sdktranslator.FormatGemini} {
		body := doc([]byte(`{"model":"gpt-5.6"}`))
		if syncCodexAPIKeyPromptCacheTTLDoc(body, auth, from, source) || body.Get("prompt_cache_options").Exists() {
			t.Fatalf("source format %q must not carry prompt_cache_options, got %s", from, body.Bytes())
		}
	}
	for _, from := range []sdktranslator.Format{sdktranslator.FormatOpenAIResponse, sdktranslator.FormatCodex} {
		body := doc([]byte(`{"model":"gpt-5.6"}`))
		if !syncCodexAPIKeyPromptCacheTTLDoc(body, auth, from, source) || body.Get("prompt_cache_options.ttl").String() != "12h" {
			t.Fatalf("source format %q should carry ttl=12h, got %s", from, body.Bytes())
		}
	}
}

func TestCodexAPIKeyPromptCacheTTLPayloadRules(t *testing.T) {
	rule := []config.PayloadRule{{
		Models: []config.PayloadModelRule{{Name: "gpt-5.6", Protocol: "codex"}},
		Params: map[string]any{"prompt_cache_options.ttl": "30m"},
	}}
	for _, mode := range codexPromptCacheTTLModes() {
		t.Run(mode.name+"/default keeps the caller value", func(t *testing.T) {
			cfg := &config.Config{Payload: config.PayloadConfig{Default: rule}}
			body := runCodexPromptCacheTTLRequest(t, cfg, codexPromptCacheTTLAPIKeyAuth, codexPromptCacheTTLPayload(`{"ttl":"12h"}`), mode.stream)
			if options, _ := body["prompt_cache_options"].(map[string]any); options["ttl"] != "12h" {
				t.Fatalf("prompt_cache_options = %#v, want the caller's ttl=12h", body["prompt_cache_options"])
			}
		})
		t.Run(mode.name+"/default fills a missing value", func(t *testing.T) {
			cfg := &config.Config{Payload: config.PayloadConfig{Default: rule}}
			body := runCodexPromptCacheTTLRequest(t, cfg, codexPromptCacheTTLAPIKeyAuth, codexPromptCacheTTLPayload(""), mode.stream)
			if options, _ := body["prompt_cache_options"].(map[string]any); options["ttl"] != "30m" {
				t.Fatalf("prompt_cache_options = %#v, want the rule's ttl=30m", body["prompt_cache_options"])
			}
		})
		t.Run(mode.name+"/override wins", func(t *testing.T) {
			cfg := &config.Config{Payload: config.PayloadConfig{Override: rule}}
			body := runCodexPromptCacheTTLRequest(t, cfg, codexPromptCacheTTLAPIKeyAuth, codexPromptCacheTTLPayload(`{"ttl":"12h"}`), mode.stream)
			if options, _ := body["prompt_cache_options"].(map[string]any); options["ttl"] != "30m" {
				t.Fatalf("prompt_cache_options = %#v, want the rule's ttl=30m", body["prompt_cache_options"])
			}
		})
	}
}

func TestCodexWebsocketsAPIKeyForwardsCallerPromptCacheTTL(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	var gotFrame map[string]any
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, errUpgrade := upgrader.Upgrade(w, r, nil)
		if errUpgrade != nil {
			t.Errorf("upgrade websocket: %v", errUpgrade)
			return
		}
		defer func() { _ = conn.Close() }()
		_, payload, errRead := conn.ReadMessage()
		if errRead != nil {
			t.Errorf("read upstream websocket message: %v", errRead)
			return
		}
		gotFrame = nil
		_ = json.Unmarshal(payload, &gotFrame)
		_ = conn.WriteMessage(websocket.TextMessage, []byte(codexPromptCacheTTLCompleted))
	}))
	defer wsServer.Close()

	wsCtx := cliproxyexecutor.WithDownstreamWebsocket(context.Background())
	payload := codexPromptCacheTTLPayload(`{"mode":"explicit","ttl":"12h"}`)
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse}
	for _, tc := range []struct {
		name    string
		auth    *cliproxyauth.Auth
		wantTTL bool
	}{
		{name: "api key", auth: codexPromptCacheTTLAPIKeyAuth(wsServer.URL + "/v1"), wantTTL: true},
		{name: "oauth", auth: codexPromptCacheTTLOAuthAuth(wsServer.URL + "/v1"), wantTTL: false},
	} {
		tc.auth.Attributes["websockets"] = "true"
		check := func(t *testing.T) {
			t.Helper()
			if gotFrame == nil {
				t.Fatal("upstream received no websocket frame")
			}
			options, exists := gotFrame["prompt_cache_options"].(map[string]any)
			if tc.wantTTL {
				if !exists || len(options) != 1 || options["ttl"] != "12h" {
					t.Fatalf("prompt_cache_options = %#v, want only ttl=12h", gotFrame["prompt_cache_options"])
				}
			} else if _, present := gotFrame["prompt_cache_options"]; present {
				t.Fatalf("prompt_cache_options should be omitted, got %#v", gotFrame["prompt_cache_options"])
			}
			if _, present := gotFrame["prompt_cache_retention"]; present {
				t.Fatalf("prompt_cache_retention should stay removed, got %#v", gotFrame["prompt_cache_retention"])
			}
		}
		t.Run(tc.name+"/execute", func(t *testing.T) {
			gotFrame = nil
			executor := NewCodexAutoExecutor(&config.Config{})
			if _, errExec := executor.Execute(wsCtx, tc.auth, cliproxyexecutor.Request{Model: "gpt-5.6", Payload: payload}, opts); errExec != nil {
				t.Fatalf("Execute error: %v", errExec)
			}
			check(t)
		})
		t.Run(tc.name+"/stream", func(t *testing.T) {
			gotFrame = nil
			executor := NewCodexAutoExecutor(&config.Config{})
			result, errStream := executor.ExecuteStream(wsCtx, tc.auth, cliproxyexecutor.Request{Model: "gpt-5.6", Payload: payload}, opts)
			if errStream != nil {
				t.Fatalf("ExecuteStream error: %v", errStream)
			}
			for range result.Chunks {
			}
			check(t)
		})
	}
}
