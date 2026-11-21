package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// newDualTransportCodexTestServer serves both the websocket and the HTTP/SSE
// Codex transports. Each served request reports "websocket" or "http" on the
// returned channel; responses carry the matching marker text.
func newDualTransportCodexTestServer(t *testing.T) (url string, transports chan string) {
	t.Helper()
	transports = make(chan string, 8)
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if websocket.IsWebSocketUpgrade(r) {
			conn, errUpgrade := upgrader.Upgrade(w, r, nil)
			if errUpgrade != nil {
				t.Errorf("upgrade websocket: %v", errUpgrade)
				return
			}
			defer func() { _ = conn.Close() }()
			_, _, errRead := conn.ReadMessage()
			if errRead != nil {
				return
			}
			transports <- "websocket"
			_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.created","response":{"id":"resp-ws","output":[]}}`))
			_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.completed","response":{"id":"resp-ws","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ws-answer"}]}],"usage":{"input_tokens":3,"output_tokens":2,"total_tokens":5}}}`))
			_, _, _ = conn.ReadMessage()
			return
		}
		_, _ = io.ReadAll(r.Body)
		transports <- "http"
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w,
			"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-http\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"http-answer\"}]}],\"usage\":{\"input_tokens\":4,\"output_tokens\":2,\"total_tokens\":6}}}\n\n")
	}))
	t.Cleanup(server.Close)
	return server.URL, transports
}

func awaitTransport(t *testing.T, transports chan string) string {
	t.Helper()
	select {
	case transport := <-transports:
		return transport
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for upstream transport report")
		return ""
	}
}

func TestCodexAutoExecutorServeHTTPOverWebsocketsExecute(t *testing.T) {
	for _, tc := range []struct {
		name           string
		optIn          bool
		authWebsockets string
		wantTransport  string
		wantText       string
	}{
		{name: "opt-in enabled with websocket auth", optIn: true, authWebsockets: "true", wantTransport: "websocket", wantText: "ws-answer"},
		{name: "opt-in disabled keeps HTTP", optIn: false, authWebsockets: "true", wantTransport: "http", wantText: "http-answer"},
		{name: "opt-in enabled without websocket auth keeps HTTP", optIn: true, authWebsockets: "false", wantTransport: "http", wantText: "http-answer"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			baseURL, transports := newDualTransportCodexTestServer(t)
			exec := NewCodexAutoExecutor(&config.Config{
				SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationAll},
				Codex:     config.CodexConfig{ServeHTTPOverWebsockets: tc.optIn},
			})
			auth := &cliproxyauth.Auth{
				ID:       "codex-http-downstream",
				Provider: "codex",
				Attributes: map[string]string{
					"api_key":    "sk-test",
					"base_url":   baseURL,
					"websockets": tc.authWebsockets,
				},
			}
			resp, errExecute := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
				Model:   "gpt-5.4",
				Payload: []byte(`{"model":"gpt-5.4","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`),
			}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-response")})
			if errExecute != nil {
				t.Fatalf("Execute() error = %v", errExecute)
			}
			if got := awaitTransport(t, transports); got != tc.wantTransport {
				t.Fatalf("upstream transport = %q, want %q", got, tc.wantTransport)
			}
			var text string
			for _, item := range ojson.GetBytes(resp.Payload, "output").Array() {
				for _, part := range item.Get("content").Array() {
					if part.Get("type").String() == "output_text" {
						text = part.Get("text").String()
					}
				}
			}
			if text != tc.wantText {
				t.Fatalf("response output text = %q, want %q; payload=%s", text, tc.wantText, resp.Payload)
			}
		})
	}
}

func TestCodexAutoExecutorServeHTTPOverWebsocketsExecuteStream(t *testing.T) {
	for _, tc := range []struct {
		name           string
		optIn          bool
		authWebsockets string
		wantTransport  string
		wantText       string
	}{
		{name: "opt-in enabled streams over websocket", optIn: true, authWebsockets: "true", wantTransport: "websocket", wantText: "ws-answer"},
		{name: "opt-in disabled streams over HTTP", optIn: false, authWebsockets: "true", wantTransport: "http", wantText: "http-answer"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			baseURL, transports := newDualTransportCodexTestServer(t)
			exec := NewCodexAutoExecutor(&config.Config{
				SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationAll},
				Codex:     config.CodexConfig{ServeHTTPOverWebsockets: tc.optIn},
			})
			auth := &cliproxyauth.Auth{
				ID:       "codex-http-downstream-stream",
				Provider: "codex",
				Attributes: map[string]string{
					"api_key":    "sk-test",
					"base_url":   baseURL,
					"websockets": tc.authWebsockets,
				},
			}
			result, errStream := exec.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
				Model:   "gpt-5.4",
				Payload: []byte(`{"model":"gpt-5.4","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`),
			}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-response"), Stream: true})
			if errStream != nil {
				t.Fatalf("ExecuteStream() error = %v", errStream)
			}
			sawText := false
			streamDone := false
			for !streamDone {
				select {
				case chunk, ok := <-result.Chunks:
					if !ok {
						streamDone = true
						continue
					}
					if chunk.Err != nil {
						t.Fatalf("stream chunk error = %v", chunk.Err)
					}
					if ojson.GetBytes(chunk.Payload, "type").String() == "response.completed" &&
						ojson.GetBytes(chunk.Payload, "response.output.0.content.0.text").String() == tc.wantText {
						sawText = true
					}
				case <-time.After(5 * time.Second):
					t.Fatal("timed out waiting for stream completion")
				}
			}
			if got := awaitTransport(t, transports); got != tc.wantTransport {
				t.Fatalf("upstream transport = %q, want %q", got, tc.wantTransport)
			}
			if !sawText {
				t.Fatalf("stream never delivered %q", tc.wantText)
			}
		})
	}
}

func TestCodexAutoExecutorServeHTTPOverWebsocketsUpgradeRejectionFallsBackToHTTP(t *testing.T) {
	transports := make(chan string, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if websocket.IsWebSocketUpgrade(r) {
			transports <- "websocket-rejected"
			w.WriteHeader(http.StatusUpgradeRequired)
			_, _ = w.Write([]byte(`{"error":{"message":"websocket not available"}}`))
			return
		}
		_, _ = io.ReadAll(r.Body)
		transports <- "http"
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w,
			"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-http-fallback\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"http-answer\"}]}],\"usage\":{\"input_tokens\":4,\"output_tokens\":2,\"total_tokens\":6}}}\n\n")
	}))
	defer server.Close()

	exec := NewCodexAutoExecutor(&config.Config{
		SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationAll},
		Codex:     config.CodexConfig{ServeHTTPOverWebsockets: true},
	})
	auth := &cliproxyauth.Auth{
		ID:       "codex-http-downstream-fallback",
		Provider: "codex",
		Attributes: map[string]string{
			"api_key":    "sk-test",
			"base_url":   server.URL,
			"websockets": "true",
		},
	}
	resp, errExecute := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.4",
		Payload: []byte(`{"model":"gpt-5.4","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-response")})
	if errExecute != nil {
		t.Fatalf("Execute() error = %v", errExecute)
	}
	if got := awaitTransport(t, transports); got != "websocket-rejected" {
		t.Fatalf("first upstream transport = %q, want websocket-rejected", got)
	}
	if got := awaitTransport(t, transports); got != "http" {
		t.Fatalf("fallback upstream transport = %q, want http", got)
	}
	if got := ojson.GetBytes(resp.Payload, "output.0.content.0.text").String(); got != "http-answer" {
		t.Fatalf("fallback response output text = %q, want http-answer; payload=%s", got, resp.Payload)
	}
}
