package executor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestCodexCompatCompanionContext(t *testing.T) {
	for _, transport := range []string{"http", "http-stream", "compact", "websocket", "websocket-stream", "websocket-fallback", "websocket-stream-fallback"} {
		for _, source := range []sdktranslator.Format{sdktranslator.FormatOpenAIResponse, sdktranslator.FormatCodex} {
			for _, policy := range []string{"enabled", "disabled", "oauth", "oauth-cached-key", "home", "payload-override"} {
				t.Run(fmt.Sprintf("%s/%s/%s", transport, source, policy), func(t *testing.T) {
					testCodexCompatCompanionContext(t, transport, source, policy)
				})
			}
		}
	}
}

func testCodexCompatCompanionContext(t *testing.T, transport string, source sdktranslator.Format, policy string) {
	t.Helper()
	const model = "step-5-preview"
	const apiKey = "synthetic-companion-key"
	const input = `[{"type":"function_call_output","id":"fco_page","name":"codex_apps_open_page","namespace":"agent","output":"{\"page_id\":null}"},{"type":"function_call_output","id":"fco_snapshot","name":"codex_apps_selected_page_part_1_of_1","namespace":"agent","output":"Companion Page snapshot\n{\"page_id\":null,\"document_view\":null}"},{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]},{"type":"function_call","call_id":"call_ok","name":"lookup","arguments":"{}"},{"type":"function_call_output","call_id":"call_ok","output":"paired result"},{"type":"function_call_output","call_id":"call_previous","output":"previous turn result"}]`
	const operatorInput = `[{"type":"function_call_output","name":"operator_context","output":"operator supplied"}]`
	const completed = `{"type":"response.completed","response":{"id":"resp_context","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`
	const errorBody = `{"error":{"message":"responses function_call_output call_id is required","type":"request_params_invalid"}}`
	captured := make(chan []byte, 2)
	upgrades := make(chan struct{}, 1)
	validate := func(body []byte) bool {
		captured <- body
		if !ojson.ValidBytes(body) {
			t.Errorf("invalid upstream JSON: %s", body)
			return false
		}
		for _, item := range ojson.GetBytesNoCopy(body, "input").Array() {
			if item.Get("type").String() == "function_call_output" && strings.TrimSpace(item.Get("call_id").String()) == "" {
				return false
			}
		}
		return true
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if websocket.IsWebSocketUpgrade(r) {
			upgrades <- struct{}{}
			if strings.HasSuffix(transport, "fallback") {
				w.WriteHeader(http.StatusUpgradeRequired)
				return
			}
			conn, errUpgrade := (&websocket.Upgrader{}).Upgrade(w, r, nil)
			if errUpgrade != nil {
				t.Errorf("upgrade: %v", errUpgrade)
				return
			}
			defer func() { _ = conn.Close() }()
			_, body, errRead := conn.ReadMessage()
			if errRead != nil {
				t.Errorf("read websocket request: %v", errRead)
				return
			}
			if !validate(body) {
				_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"error","status":400,"error":{"type":"invalid_request_error","message":"responses function_call_output call_id is required"}}`))
				return
			}
			_ = conn.WriteMessage(websocket.TextMessage, []byte(completed))
			return
		}
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Errorf("read HTTP request: %v", errRead)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if !validate(body) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, errorBody)
			return
		}
		if transport == "compact" {
			if r.URL.Path != "/responses/compact" {
				t.Errorf("compact path = %q", r.URL.Path)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":"resp_context","object":"response.compaction","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: "+completed+"\n\n")
	}))
	defer server.Close()

	cfg := &config.Config{
		SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationAll},
		CodexKey:  []config.CodexKey{{APIKey: apiKey, BaseURL: server.URL, Models: []config.CodexModel{{Name: model, IsCompat: policy != "disabled"}}}},
	}
	auth := &cliproxyauth.Auth{ID: "companion-context", Provider: "codex", Attributes: map[string]string{"api_key": apiKey, "base_url": server.URL}}
	if policy == "oauth" {
		delete(auth.Attributes, "api_key")
		auth.Metadata = map[string]any{"access_token": "synthetic-oauth-token"}
	}
	if policy == "oauth-cached-key" {
		auth.Attributes[cliproxyauth.AttributeAuthKind] = cliproxyauth.AuthKindOAuth
		auth.Metadata = map[string]any{"access_token": "synthetic-oauth-token"}
	}
	if policy == "home" {
		cfg.Home.Enabled = true
		cfg.CodexKey = nil
		auth.Metadata = map[string]any{"credential_options": map[string]any{"models": []config.CodexModel{{Name: model, IsCompat: true}}}}
	}
	if policy == "payload-override" {
		protocol := sdktranslator.FormatCodex.String()
		if transport == "compact" {
			protocol = sdktranslator.FormatOpenAIResponse.String()
		}
		rules := []config.PayloadModelRule{{Name: model, Protocol: protocol}}
		cfg.Payload.OverrideRaw = []config.PayloadRule{{Models: rules, Params: map[string]any{"input": operatorInput}}}
		cfg.Payload.Override = []config.PayloadRule{{Models: rules, Params: map[string]any{"audit.-1": "once"}}}
	}
	stream := strings.Contains(transport, "stream")
	req := cliproxyexecutor.Request{Model: model, Payload: []byte(`{"model":"step-5-preview","input":` + input + `}`)}
	opts := cliproxyexecutor.Options{SourceFormat: source, Stream: stream, Headers: http.Header{"User-Agent": {"codex_desktop_rs/0.167.0"}, codexResponsesLiteHeader: {"true"}}}
	if transport == "compact" {
		opts.Alt = "responses/compact"
	}
	exec := NewCodexExecutor(cfg)
	if strings.HasPrefix(policy, "oauth") && !exec.resolveCodexModelIsCompat(auth, req, model) {
		t.Fatal("OAuth control must resolve is-compat=true to exercise the auth-kind guard")
	}
	var client interface {
		Execute(context.Context, *cliproxyauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error)
		ExecuteStream(context.Context, *cliproxyauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error)
	} = exec
	if strings.HasPrefix(transport, "websocket") {
		client = NewCodexWebsocketsExecutor(cfg)
	}
	var err error
	if stream {
		result, errStream := client.ExecuteStream(context.Background(), auth, req, opts)
		err = errStream
		if err == nil {
			for chunk := range result.Chunks {
				if chunk.Err != nil {
					err = chunk.Err
				}
			}
		}
	} else {
		_, err = client.Execute(context.Background(), auth, req, opts)
	}
	wantConverted := policy == "enabled" || policy == "home"
	wantStatus := http.StatusBadRequest
	if wantConverted {
		wantStatus = http.StatusOK
	}
	status := http.StatusOK
	if err != nil {
		var statusErr cliproxyexecutor.StatusError
		if !errors.As(err, &statusErr) {
			t.Fatalf("unexpected execution error: %v", err)
		}
		status = statusErr.StatusCode()
	}
	if status != wantStatus {
		t.Fatalf("status = %d, want %d: %v", status, wantStatus, err)
	}
	var body []byte
	select {
	case body = <-captured:
	default:
		t.Fatal("request did not reach the local strict upstream")
	}
	if strings.HasPrefix(transport, "websocket") && len(upgrades) != 1 {
		t.Fatal("websocket transport was not exercised")
	}
	if policy == "payload-override" {
		if got := ojson.GetBytesNoCopy(body, "input").Raw; got != operatorInput {
			t.Fatalf("compatibility processing overwrote operator input: %s", body)
		}
		if got := ojson.GetBytesNoCopy(body, "audit").Array(); len(got) != 1 || got[0].String() != "once" {
			t.Fatalf("payload rules did not run exactly once: %s", body)
		}
		return
	}
	if !wantConverted {
		if got := ojson.GetBytesNoCopy(body, "input").Raw; got != input {
			t.Fatalf("native input changed: %s", body)
		}
		return
	}
	items := ojson.GetBytesNoCopy(body, "input").Array()
	if len(items) != 6 {
		t.Fatalf("input items lost or added: %s", body)
	}
	originalItems := ojson.Parse(input).Array()
	for i, item := range items {
		if i < 2 {
			original := originalItems[i]
			wantText := "Tool output from agent." + original.Get("name").String() + ":\n" + original.Get("output").String()
			if item.Get("type").String() != "message" || item.Get("role").String() != "user" || item.Get("content.0.text").String() != wantText {
				t.Fatalf("companion context was not preserved as user text: %s", item.Raw)
			}
		} else if item.Raw != originalItems[i].Raw {
			t.Fatalf("unrelated input %d changed: %s", i, body)
		}
	}
}

func TestCodexCompatCompanionContextTokenCount(t *testing.T) {
	const input = `[{"type":"function_call_output","id":"fco_context","name":"codex_apps_open_page","namespace":"agent","output":"page context"}]`
	for _, policy := range []string{"enabled", "disabled", "payload-override", "payload-filter"} {
		t.Run(policy, func(t *testing.T) {
			cfg := &config.Config{CodexKey: []config.CodexKey{{APIKey: "synthetic", Models: []config.CodexModel{{Name: "step-5-preview", IsCompat: policy != "disabled"}}}}}
			rules := []config.PayloadModelRule{{Name: "step-5-preview", Protocol: "codex"}}
			if policy == "payload-override" {
				cfg.Payload.OverrideRaw = []config.PayloadRule{{Models: rules, Params: map[string]any{"input": input}}}
			}
			if policy == "payload-filter" {
				cfg.Payload.Filter = []config.PayloadFilterRule{{Models: rules, Params: []string{"input"}}}
			}
			auth := &cliproxyauth.Auth{Provider: "codex", Attributes: map[string]string{"api_key": "synthetic"}}
			resp, errCount := NewCodexExecutor(cfg).CountTokens(t.Context(), auth, cliproxyexecutor.Request{
				Model: "step-5-preview", Payload: []byte(`{"input":` + input + `}`),
			}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatCodex, Headers: http.Header{codexResponsesLiteHeader: {"true"}}})
			if errCount != nil {
				t.Fatal(errCount)
			}
			text := "page context"
			if policy == "enabled" {
				text = "Tool output from agent.codex_apps_open_page:\n" + text
			}
			enc, errTokenizer := tokenizerForCodexModel("step-5-preview")
			if errTokenizer != nil {
				t.Fatal(errTokenizer)
			}
			want, errEncode := enc.Count(text)
			if errEncode != nil {
				t.Fatal(errEncode)
			}
			if policy == "payload-filter" {
				want = 0
			}
			got := ojson.GetBytesNoCopy(resp.Payload, "response.usage.input_tokens")
			if !got.Exists() || got.Int() != int64(want) {
				t.Fatalf("token count should describe the final context: want %d, response=%s", want, resp.Payload)
			}
		})
	}
}

func TestCodexCompatCompanionContextDuplexTurns(t *testing.T) {
	for _, frameType := range []string{"response.steer", "response.create", "response.append"} {
		for _, policy := range []string{"enabled", "disabled", "payload-override"} {
			t.Run(frameType+"/"+policy, func(t *testing.T) {
				const input = `[{"type":"function_call_output","id":"fco_turn","name":"codex_apps_open_page","namespace":"agent","output":"page context"}]`
				completedID := "r2"
				if frameType == "response.steer" {
					completedID = "r1"
				}
				captured := make(chan []byte, 1)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					conn, errUpgrade := (&websocket.Upgrader{}).Upgrade(w, r, nil)
					if errUpgrade != nil {
						t.Errorf("upgrade: %v", errUpgrade)
						return
					}
					defer func() { _ = conn.Close() }()
					if _, _, errRead := conn.ReadMessage(); errRead != nil {
						t.Errorf("initial request: %v", errRead)
						return
					}
					_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.created","response":{"id":"r1","output":[]}}`))
					if frameType != "response.steer" {
						_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.completed","response":{"id":"r1","output":[]}}`))
					}
					_, next, errRead := conn.ReadMessage()
					if errRead != nil {
						t.Errorf("next turn: %v", errRead)
						return
					}
					captured <- next
					if frameType == "response.steer" {
						_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.steer.accepted","steer":{"id":"s1","previous_response_id":"r1"}}`))
					} else {
						_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.created","response":{"id":"r2","output":[]}}`))
					}
					_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.completed","response":{"id":"`+completedID+`","output":[]}}`))
					_, _, _ = conn.ReadMessage()
				}))
				defer server.Close()
				// Bound test failures without adding production network deadlines.
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				turns := make(chan cliproxyexecutor.WebsocketInput, 1)
				ctx = cliproxyexecutor.WithWebsocketInput(cliproxyexecutor.WithDownstreamWebsocket(ctx), turns)
				cfg := &config.Config{
					SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationAll},
					Codex:     config.CodexConfig{ResponseSteering: true},
					CodexKey:  []config.CodexKey{{APIKey: "synthetic", BaseURL: server.URL, Models: []config.CodexModel{{Name: "step-5-preview", IsCompat: policy != "disabled"}}}},
				}
				if policy == "payload-override" {
					rules := []config.PayloadModelRule{{Name: "step-5-preview", Protocol: "codex"}}
					cfg.Payload.OverrideRaw = []config.PayloadRule{{Models: rules, Params: map[string]any{"input": input}}}
					cfg.Payload.Override = []config.PayloadRule{{Models: rules, Params: map[string]any{"audit.-1": "once"}}}
				}
				exec := NewCodexWebsocketsExecutor(cfg)
				exec.store = &codexWebsocketSessionStore{sessions: make(map[string]*codexWebsocketSession)}
				defer exec.CloseExecutionSession("companion-turn")
				auth := &cliproxyauth.Auth{ID: "companion-turn", Provider: "codex", Attributes: map[string]string{"api_key": "synthetic", "base_url": server.URL}}
				result, errStream := exec.ExecuteStream(ctx, auth, cliproxyexecutor.Request{Model: "step-5-preview", Payload: []byte(`{"model":"step-5-preview","input":"hi"}`)}, cliproxyexecutor.Options{
					SourceFormat: sdktranslator.FormatCodex, Headers: http.Header{codexResponsesLiteHeader: {"true"}},
					Metadata: map[string]any{cliproxyexecutor.ExecutionSessionMetadataKey: "companion-turn"},
				})
				if errStream != nil {
					t.Fatal(errStream)
				}
				completed := false
				for chunk := range result.Chunks {
					if chunk.Err != nil {
						t.Fatal(chunk.Err)
					}
					event := ojson.GetBytesNoCopy(chunk.Payload, "type").String()
					id := ojson.GetBytesNoCopy(chunk.Payload, "response.id").String()
					if id == "r1" && ((frameType == "response.steer" && event == "response.created") || (frameType != "response.steer" && event == "response.completed")) {
						turns <- cliproxyexecutor.WebsocketInput{Payload: []byte(`{"type":"` + frameType + `","previous_response_id":"r1","input":` + input + `}`)}
					}
					if event == "response.completed" && id == completedID {
						completed = true
						cancel()
						break
					}
				}
				if !completed {
					t.Fatal("upstream did not complete the expected response")
				}
				select {
				case body := <-captured:
					wantType := "response.create"
					if frameType == "response.steer" {
						wantType = frameType
					}
					if ojson.GetBytesNoCopy(body, "type").String() != wantType || ojson.GetBytesNoCopy(body, "previous_response_id").String() != "r1" {
						t.Fatalf("next-turn routing changed: %s", body)
					}
					item := ojson.GetBytesNoCopy(body, "input.0")
					if policy == "enabled" {
						if item.Get("type").String() != "message" || item.Get("content.0.text").String() != "Tool output from agent.codex_apps_open_page:\npage context" {
							t.Fatalf("next-turn context was not normalized: %s", body)
						}
					} else if ojson.GetBytesNoCopy(body, "input").Raw != input {
						t.Fatalf("next-turn input should remain untouched: %s", body)
					}
					if policy == "payload-override" && len(ojson.GetBytesNoCopy(body, "audit").Array()) != 1 {
						t.Fatalf("next-turn payload rules must run exactly once: %s", body)
					}
				default:
					t.Fatal("next turn did not reach the same upstream connection")
				}
			})
		}
	}
}
