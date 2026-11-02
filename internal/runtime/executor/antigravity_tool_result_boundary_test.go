package executor

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestAntigravityGeminiToolResultBoundary(t *testing.T) {
	payload := []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"work"}]},{"role":"model","parts":[{"functionCall":{"id":"call-1","name":"exec_command","args":{"cmd":"sleep 30"}},"thoughtSignature":"preserved"}]},{"role":"model","parts":[{"functionResponse":{"id":"call-1","name":"exec_command","response":{"result":"Process exited with code 0\nOutput:\n"}}}]}]}}`)
	got := ensureAntigravityGeminiBoundaryUserContent("gemini-4-argon-medium", payload)
	if role := gjson.GetBytes(got, "request.contents.2.role").String(); role != "user" {
		t.Fatalf("terminal result role = %q, want user", role)
	}
	if text := gjson.GetBytes(got, "request.contents.2.parts.1.text").String(); text != "exec_command" {
		t.Fatalf("terminal result label = %q, want exec_command", text)
	}
	for _, path := range []string{"request.contents.0", "request.contents.1", "request.contents.2.parts.0"} {
		if gjson.GetBytes(got, path).Raw != gjson.GetBytes(payload, path).Raw {
			t.Fatalf("changed history or tool result at %s", path)
		}
	}
	if twice := ensureAntigravityGeminiBoundaryUserContent("gemini-4-argon-medium", got); !bytes.Equal(twice, got) {
		t.Fatal("normalization is not idempotent")
	}
	if claude := ensureAntigravityGeminiBoundaryUserContent("claude-opus-5-5", payload); !bytes.Equal(claude, payload) {
		t.Fatal("changed Claude payload")
	}
}

func TestAntigravityGeminiToolResultBoundaryWireAndPayloadOverride(t *testing.T) {
	for _, override := range []bool{false, true} {
		name := "normal"
		if override {
			name = "configured"
		}
		t.Run(name, func(t *testing.T) {
			captured := make(chan []byte, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				captured <- body
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "data: {\"response\":{\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"ok\"}]},\"finishReason\":\"STOP\"}]}}\n\n")
			}))
			defer server.Close()
			cfg := &config.Config{}
			if override {
				cfg.Payload.Override = []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: "*"}}, Params: map[string]any{"contents.2.role": "model", "contents.2.parts.1.text": "configured"}}}
			}
			payload := []byte(`{"contents":[{"role":"user","parts":[{"text":"work"}]},{"role":"model","parts":[{"functionCall":{"id":"call-1","name":"exec_command","args":{"cmd":"sleep 30"}}}]},{"role":"user","parts":[{"functionResponse":{"id":"call-1","name":"exec_command","response":{"result":"done"}}}]}]}`)
			auth := testAntigravityAuth(server.URL)
			auth.Metadata["project_id"] = "boundary-test"
			result, err := NewAntigravityExecutor(cfg).ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{Model: "gemini-4-argon-medium", Payload: payload}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatGemini, ResponseFormat: sdktranslator.FormatGemini, OriginalRequest: payload, Stream: true})
			if err != nil {
				t.Fatal(err)
			}
			for chunk := range result.Chunks {
				if chunk.Err != nil {
					t.Fatal(chunk.Err)
				}
			}
			body := <-captured
			role, text := "user", "exec_command"
			if override {
				role, text = "model", "configured"
			}
			if got := gjson.GetBytes(body, "request.contents.2.role").String(); got != role {
				t.Fatalf("wire role = %q, want %q", got, role)
			}
			if got := gjson.GetBytes(body, "request.contents.2.parts.1.text").String(); got != text {
				t.Fatalf("wire label = %q, want %q", got, text)
			}
		})
	}
}
