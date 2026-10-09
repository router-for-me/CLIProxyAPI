package executor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/wsrelay"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	core "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestClaudePayloadConditionsEvaluateOnceAtFinalBarrier(t *testing.T) {
	cfg := &config.Config{Payload: config.PayloadConfig{
		Override: []config.PayloadRule{{
			Models: []config.PayloadModelRule{{Name: "*", Match: []map[string]any{{"max_tokens": 100}}}},
			Params: map[string]any{"max_tokens": 200, "temperature": 0.2, "diagnostics": map[string]any{"user": true}},
		}, {
			Models: []config.PayloadModelRule{{Name: "*", Match: []map[string]any{{"max_tokens": 200}}}},
			Params: map[string]any{"top_p": 0.4},
		}},
		Filter: []config.PayloadFilterRule{{Models: []config.PayloadModelRule{{Name: "*", Match: []map[string]any{{"max_tokens": 200}}}}, Params: []string{"messages.0"}}},
	}}
	for _, stream := range []bool{false, true} {
		body := executeClaudeContextManagementRequest(t, cfg, []byte(`{"model":"claude-opus-5","max_tokens":100,"messages":[{"role":"user","content":"first"},{"role":"assistant","content":"second"},{"role":"user","content":"third"}]}`), stream)
		if gjson.GetBytes(body, "max_tokens").Int() != 200 || gjson.GetBytes(body, "temperature").Float() != 0.2 || gjson.GetBytes(body, "top_p").Float() != 0.4 || !gjson.GetBytes(body, "diagnostics.user").Bool() {
			t.Fatalf("conditional rule lost fields (stream=%v): %s", stream, body)
		}
		if gjson.GetBytes(body, "messages.#").Int() != 2 {
			t.Fatalf("conditional filter applied more than once (stream=%v): %s", stream, body)
		}
	}
}

func TestAIStudioCountPayloadAfterCleanup(t *testing.T) {
	cfg := &config.Config{Payload: config.PayloadConfig{Override: []config.PayloadRule{{
		Models: []config.PayloadModelRule{{Name: "*"}},
		Params: map[string]any{"generationConfig.temperature": 0.2, "tools": []any{map[string]any{"googleSearch": map[string]any{}}}, "safetySettings": []any{map[string]any{"category": "configured"}}},
	}}}}
	req := core.Request{Model: "gemini-3.7-flash", Payload: []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`), Metadata: map[string]any{"action": "countTokens"}}
	connected := make(chan struct{})
	relay := wsrelay.NewManager(wsrelay.Options{
		ProviderFactory: func(*http.Request) (string, error) { return "count-test", nil },
		OnConnected:     func(string) { close(connected) },
	})
	server := httptest.NewServer(relay.Handler())
	defer server.Close()
	defer func() {
		if errStop := relay.Stop(context.Background()); errStop != nil {
			t.Error(errStop)
		}
	}()
	conn, _, errDial := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+relay.Path(), nil)
	if errDial != nil {
		t.Fatal(errDial)
	}
	defer func() {
		if errClose := conn.Close(); errClose != nil {
			t.Error(errClose)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	select {
	case <-connected:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	for _, configured := range []bool{false, true} {
		selected := &config.Config{}
		if configured {
			selected = cfg
		}
		captured := make(chan []byte, 1)
		clientErrors := make(chan error, 1)
		go func() {
			var message wsrelay.Message
			if errRead := conn.ReadJSON(&message); errRead != nil {
				clientErrors <- errRead
				return
			}
			captured <- []byte(message.Payload["body"].(string))
			clientErrors <- conn.WriteJSON(wsrelay.Message{ID: message.ID, Type: wsrelay.MessageTypeHTTPResp, Payload: map[string]any{"status": float64(http.StatusOK), "headers": map[string]any{"Content-Type": "application/json"}, "body": `{"totalTokens":1}`}})
		}()
		executor := NewAIStudioExecutor(selected, "aistudio", relay)
		if _, errCount := executor.CountTokens(ctx, &cliproxyauth.Auth{ID: "count-test", Provider: "aistudio"}, req, core.Options{SourceFormat: sdktranslator.FormatGemini}); errCount != nil {
			t.Fatal(errCount)
		}
		if errClient := <-clientErrors; errClient != nil {
			t.Fatal(errClient)
		}
		body := <-captured
		for _, field := range []string{"generationConfig", "tools", "safetySettings"} {
			if gjson.GetBytes(body, field).Exists() != configured {
				t.Fatalf("count cleanup precedence for %s: %s", field, body)
			}
		}
	}
}

func TestCodexDuplexSteerPayloadBarrier(t *testing.T) {
	for _, filter := range []bool{false, true} {
		t.Run(map[bool]string{false: "override", true: "filter"}[filter], func(t *testing.T) {
			captured := make(chan []byte, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, errUpgrade := (&websocket.Upgrader{}).Upgrade(w, r, nil)
				if errUpgrade != nil {
					t.Error(errUpgrade)
					return
				}
				defer func() { _ = conn.Close() }()
				if _, _, errRead := conn.ReadMessage(); errRead != nil {
					t.Error(errRead)
					return
				}
				if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.created","response":{"id":"parent","output":[]}}`)); errWrite != nil {
					t.Error(errWrite)
					return
				}
				_, body, errRead := conn.ReadMessage()
				if errRead != nil {
					t.Error(errRead)
					return
				}
				captured <- body
				_, _, _ = conn.ReadMessage()
			}))
			defer server.Close()
			models := []config.PayloadModelRule{{Name: "*", Match: []map[string]any{{"type": "response.steer"}}}}
			cfg := &config.Config{Codex: config.CodexConfig{ResponseSteering: true}, Payload: config.PayloadConfig{Override: []config.PayloadRule{{Models: models, Params: map[string]any{"input": "configured", "type": "response.create"}}}}}
			if filter {
				cfg.Payload = config.PayloadConfig{Filter: []config.PayloadFilterRule{{Models: models, Params: []string{"input", "type"}}}}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			input := make(chan core.WebsocketInput, 1)
			ctx = core.WithWebsocketInput(core.WithDownstreamWebsocket(ctx), input)
			executor := NewCodexWebsocketsExecutor(cfg)
			executor.store = &codexWebsocketSessionStore{sessions: make(map[string]*codexWebsocketSession)}
			auth := &cliproxyauth.Auth{ID: t.Name(), Provider: "codex", Attributes: map[string]string{"api_key": "test", "base_url": server.URL, "websockets": "true"}}
			result, errStream := executor.ExecuteStream(ctx, auth, core.Request{Model: "gpt-6-astra", Payload: []byte(`{"input":[]}`)}, core.Options{SourceFormat: sdktranslator.FormatCodex})
			if errStream != nil {
				t.Fatal(errStream)
			}
			input <- core.WebsocketInput{Payload: []byte(`{"type":"response.steer","previous_response_id":"parent","input":"original"}`)}
			for {
				select {
				case body := <-captured:
					if gjson.GetBytes(body, "type").String() != "response.steer" || gjson.GetBytes(body, "previous_response_id").String() != "parent" {
						t.Fatalf("steer framing changed: %s", body)
					}
					if filter && gjson.GetBytes(body, "input").Exists() || !filter && gjson.GetBytes(body, "input").String() != "configured" {
						t.Fatalf("steer payload bypassed config: %s", body)
					}
					cancel()
					for range result.Chunks {
					}
					return
				case chunk, ok := <-result.Chunks:
					if !ok || chunk.Err != nil {
						t.Fatalf("stream ended before steer: %v", chunk.Err)
					}
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
			}
		})
	}
}

func TestClaudePayloadConditionsObserveBuiltinThinkingRemoval(t *testing.T) {
	for _, stream := range []bool{false, true} {
		present := []config.PayloadModelRule{{Name: "*", Exist: []string{"thinking"}}}
		absent := []config.PayloadModelRule{{Name: "*", NotExist: []string{"thinking"}}}
		cfg := &config.Config{Payload: config.PayloadConfig{
			Default:     []config.PayloadRule{{Models: absent, Params: map[string]any{"default_matched": true}}, {Models: present, Params: map[string]any{"default_unmatched": true}}},
			DefaultRaw:  []config.PayloadRule{{Models: absent, Params: map[string]any{"default_raw_matched": `true`}}, {Models: present, Params: map[string]any{"default_raw_unmatched": `true`}}},
			Override:    []config.PayloadRule{{Models: absent, Params: map[string]any{"override_matched": true}}, {Models: present, Params: map[string]any{"override_unmatched": true}}},
			OverrideRaw: []config.PayloadRule{{Models: absent, Params: map[string]any{"override_raw_matched": `true`}}, {Models: present, Params: map[string]any{"override_raw_unmatched": `true`}}},
			Filter:      []config.PayloadFilterRule{{Models: absent, Params: []string{"metadata"}}, {Models: present, Params: []string{"system"}}},
		}}
		payload := []byte(`{"model":"claude-opus-5","max_tokens":100,"thinking":{"type":"adaptive"},"tool_choice":{"type":"any"},"tools":[{"name":"lookup","input_schema":{"type":"object"}}],"messages":[{"role":"user","content":"hi"}]}`)
		body := executeClaudeContextManagementRequest(t, cfg, payload, stream)
		if gjson.GetBytes(body, "thinking").Exists() {
			t.Fatalf("builtin did not remove forced-tool thinking: %s", body)
		}
		for _, prefix := range []string{"default", "default_raw", "override", "override_raw"} {
			if !gjson.GetBytes(body, prefix+"_matched").Bool() || gjson.GetBytes(body, prefix+"_unmatched").Exists() {
				t.Fatalf("%s matched pre-builtin conditions (stream=%v): %s", prefix, stream, body)
			}
		}
		if gjson.GetBytes(body, "metadata").Exists() || !gjson.GetBytes(body, "system").Exists() {
			t.Fatalf("filters matched pre-builtin conditions (stream=%v): %s", stream, body)
		}
	}
}
