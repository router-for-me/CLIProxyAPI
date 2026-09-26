package executor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

type codexWebsocketHintFrame struct {
	conn               int
	model              string
	tier               string
	previousResponseID string
}

type codexWebsocketHintRecorder struct {
	mu         sync.Mutex
	handshakes []string
	frames     []codexWebsocketHintFrame
}

func newCodexWebsocketHintServer(t *testing.T, rec *codexWebsocketHintRecorder) *httptest.Server {
	t.Helper()
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.mu.Lock()
		rec.handshakes = append(rec.handshakes, r.Header.Get(codexRoutingHintHeader))
		connIndex := len(rec.handshakes) - 1
		rec.mu.Unlock()
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade websocket: %v", err)
			return
		}
		defer func() { _ = conn.Close() }()
		for {
			_, payload, errRead := conn.ReadMessage()
			if errRead != nil {
				return
			}
			rec.mu.Lock()
			rec.frames = append(rec.frames, codexWebsocketHintFrame{
				conn:               connIndex,
				model:              gjson.GetBytes(payload, "model").String(),
				tier:               gjson.GetBytes(payload, "service_tier").String(),
				previousResponseID: gjson.GetBytes(payload, "previous_response_id").String(),
			})
			rec.mu.Unlock()
			completed := []byte(`{"type":"response.completed","response":{"id":"resp-1","object":"response","status":"completed","model":"gpt-5.5","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`)
			if errWrite := conn.WriteMessage(websocket.TextMessage, completed); errWrite != nil {
				return
			}
		}
	}))
}

// Native Codex sends the routing hint only in the websocket handshake and keeps
// an open connection when the tier changes. The gateway mirrors that: an
// incremental request that turns on Fast continues on the same socket with the
// original handshake hint (its body carries the new tier), and the next fresh
// connection is dialed with the current hint. Whether the backend honors a
// mid-connection tier change is not established by this test.
func TestCodexWebsocketsFastToggleKeepsIncrementalConnection(t *testing.T) {
	var rec codexWebsocketHintRecorder
	server := newCodexWebsocketHintServer(t, &rec)
	defer server.Close()

	exec := NewCodexWebsocketsExecutor(&config.Config{SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationAll}})
	const executionSessionID = "ws-routing-hint-toggle"
	t.Cleanup(func() { exec.CloseExecutionSession(executionSessionID) })
	auth := codexOAuthTestAuth(server.URL)
	auth.ID = "codex-oauth-test"
	opts := cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Metadata:     map[string]any{cliproxyexecutor.ExecutionSessionMetadataKey: executionSessionID},
	}
	execute := func(payload string) {
		t.Helper()
		if _, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{Model: "gpt-5.5", Payload: []byte(payload)}, opts); err != nil {
			t.Fatalf("Execute(%s) error: %v", payload, err)
		}
	}

	execute(`{"model":"gpt-5.5","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`)
	execute(`{"model":"gpt-5.5","service_tier":"priority","previous_response_id":"resp-1","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"again"}]}]}`)
	exec.CloseExecutionSession(executionSessionID)
	execute(`{"model":"gpt-5.5","service_tier":"priority","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"fresh"}]}]}`)

	rec.mu.Lock()
	defer rec.mu.Unlock()
	wantHandshakes := []string{"model=gpt-5.5", "model=gpt-5.5;tier=priority"}
	if len(rec.handshakes) != len(wantHandshakes) || rec.handshakes[0] != wantHandshakes[0] || rec.handshakes[1] != wantHandshakes[1] {
		t.Fatalf("handshakes = %q, want %q", rec.handshakes, wantHandshakes)
	}
	wantFrames := []codexWebsocketHintFrame{
		{conn: 0, model: "gpt-5.5"},
		{conn: 0, model: "gpt-5.5", tier: "priority", previousResponseID: "resp-1"},
		{conn: 1, model: "gpt-5.5", tier: "priority"},
	}
	if len(rec.frames) != len(wantFrames) {
		t.Fatalf("frames = %+v, want %+v", rec.frames, wantFrames)
	}
	for i := range wantFrames {
		if rec.frames[i] != wantFrames[i] {
			t.Fatalf("frames = %+v, want %+v", rec.frames, wantFrames)
		}
	}
}

// An operator rule that points at a client header the request does not carry
// is skipped by the header resolver. It must not stop the gateway from
// replacing the forwarded client hint with the one derived from the body, and
// a rule that does resolve must win over the derived hint.
func TestCodexWebsocketsRoutingHintDynamicOperatorRule(t *testing.T) {
	cases := []struct {
		name         string
		operatorHint string
		want         string
	}{
		{name: "missing reference falls back to derived hint", want: "model=gpt-5.5;tier=priority"},
		{name: "resolved reference wins", operatorHint: "model=operator-dynamic", want: "model=operator-dynamic"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var rec codexWebsocketHintRecorder
			server := newCodexWebsocketHintServer(t, &rec)
			defer server.Close()

			cfg := &config.Config{
				SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationAll},
				Codex:     config.CodexConfig{DisableCodexCloaking: true},
			}
			exec := NewCodexWebsocketsExecutor(cfg)
			auth := codexOAuthTestAuth(server.URL)
			auth.ID = "codex-oauth-test"
			auth.Attributes["header:X-Codex-Routing-Hint"] = "$X-Operator-Hint"
			clientHeaders := http.Header{}
			clientHeaders.Set("X-OpenAI-Internal-Codex-Responses-Lite", "true")
			clientHeaders.Set(codexRoutingHintHeader, "model=gpt-5.4-client")
			if tc.operatorHint != "" {
				clientHeaders.Set("X-Operator-Hint", tc.operatorHint)
			}

			_, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
				Model:   "gpt-5.5",
				Payload: []byte(`{"model":"gpt-5.4-client","service_tier":"priority","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`),
			}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-response"), Headers: clientHeaders})
			if err != nil {
				t.Fatalf("Execute error: %v", err)
			}

			rec.mu.Lock()
			defer rec.mu.Unlock()
			if len(rec.handshakes) != 1 || rec.handshakes[0] != tc.want {
				t.Fatalf("handshakes = %q, want [%q]", rec.handshakes, tc.want)
			}
		})
	}
}

// A native Codex client forwards its own routing hint when cloaking is
// disabled. That hint names the model and tier the client asked for, not the
// ones the gateway resolved, so the handshake must carry the hint derived from
// the final upstream body.
func TestCodexWebsocketsRoutingHintOverridesForwardedClientHint(t *testing.T) {
	var rec codexWebsocketHintRecorder
	server := newCodexWebsocketHintServer(t, &rec)
	defer server.Close()

	cfg := &config.Config{
		SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationAll},
		Codex:     config.CodexConfig{DisableCodexCloaking: true},
		Payload: config.PayloadConfig{Override: []config.PayloadRule{{
			Models: []config.PayloadModelRule{{Name: "gpt-5.5"}},
			Params: map[string]any{"service_tier": "priority"},
		}}},
	}
	exec := NewCodexWebsocketsExecutor(cfg)
	auth := codexOAuthTestAuth(server.URL)
	auth.ID = "codex-oauth-test"
	clientHeaders := http.Header{}
	clientHeaders.Set("X-OpenAI-Internal-Codex-Responses-Lite", "true")
	clientHeaders.Set(codexRoutingHintHeader, "model=gpt-5.4-client")

	_, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.5(low)",
		Payload: []byte(`{"model":"gpt-5.4-client","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Headers:      clientHeaders,
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.handshakes) != 1 || rec.handshakes[0] != "model=gpt-5.5;tier=priority" {
		t.Fatalf("handshakes = %q, want [\"model=gpt-5.5;tier=priority\"]", rec.handshakes)
	}
	if len(rec.frames) != 1 || rec.frames[0].model != "gpt-5.5" || rec.frames[0].tier != "priority" {
		t.Fatalf("frames = %+v, want one frame with model=gpt-5.5 and service_tier=priority", rec.frames)
	}
	if rec.handshakes[0] != "model="+rec.frames[0].model+";tier="+rec.frames[0].tier {
		t.Fatalf("handshake hint = %q, does not match frame %+v", rec.handshakes[0], rec.frames[0])
	}
}

func TestCodexWebsocketsStreamRoutingHintMatchesBodyModel(t *testing.T) {
	var rec codexWebsocketHintRecorder
	server := newCodexWebsocketHintServer(t, &rec)
	defer server.Close()

	exec := NewCodexWebsocketsExecutor(&config.Config{SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationAll}})
	auth := codexOAuthTestAuth(server.URL)
	auth.ID = "codex-oauth-stream-test"
	result, err := exec.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.5(low)",
		Payload: []byte(`{"model":"client-alias","service_tier":"priority","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-response"), Stream: true})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error: %v", chunk.Err)
		}
	}

	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.handshakes) != 1 || len(rec.frames) != 1 {
		t.Fatalf("handshakes = %q, frames = %+v, want one each", rec.handshakes, rec.frames)
	}
	if rec.frames[0].model != "gpt-5.5" || rec.frames[0].tier != "priority" {
		t.Fatalf("frame = %+v, want model=gpt-5.5 and service_tier=priority", rec.frames[0])
	}
	if rec.handshakes[0] != "model="+rec.frames[0].model+";tier="+rec.frames[0].tier {
		t.Fatalf("handshake hint = %q, does not match frame %+v", rec.handshakes[0], rec.frames[0])
	}
}
