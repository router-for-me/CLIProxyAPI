package executor

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	dshelp "github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps/deepseek"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func TestDeepSeekExecutorExecute(t *testing.T) {
	target := dshelp.DeepSeekHashV1([]byte("salt_123_0"))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer token-1" {
			t.Fatalf("Authorization = %q", got)
		}
		switch r.URL.Path {
		case deepSeekCreateSessionPath:
			writeJSON(t, w, map[string]any{
				"code": 0,
				"data": map[string]any{
					"biz_code": 0,
					"biz_data": map[string]any{"id": "session-1"},
				},
			})
		case deepSeekCreatePowPath:
			writeJSON(t, w, map[string]any{
				"code": 0,
				"data": map[string]any{
					"biz_code": 0,
					"biz_data": map[string]any{
						"challenge": map[string]any{
							"algorithm":   "DeepSeekHashV1",
							"challenge":   hex.EncodeToString(target[:]),
							"salt":        "salt",
							"expire_at":   123,
							"difficulty":  1,
							"signature":   "sig",
							"target_path": deepSeekCompletionPath,
						},
					},
				},
			})
		case deepSeekCompletionPath:
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("Decode completion payload: %v", err)
			}
			if payload["chat_session_id"] != "session-1" {
				t.Fatalf("chat_session_id = %v", payload["chat_session_id"])
			}
			if payload["model_type"] != "default" {
				t.Fatalf("model_type = %v", payload["model_type"])
			}
			if payload["thinking_enabled"] != true {
				t.Fatalf("thinking_enabled = %v", payload["thinking_enabled"])
			}
			rawHeader, err := base64.StdEncoding.DecodeString(r.Header.Get("x-ds-pow-response"))
			if err != nil {
				t.Fatalf("decode pow header: %v", err)
			}
			var pow map[string]any
			if err := json.Unmarshal(rawHeader, &pow); err != nil {
				t.Fatalf("unmarshal pow header: %v", err)
			}
			if got := int(pow["answer"].(float64)); got != 0 {
				t.Fatalf("pow answer = %d, want 0", got)
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte(`data: {"v":{"response":{"status":"WIP","fragments":[{"type":"THINK","content":"thinking "},{"type":"RESPONSE","content":"hello"}]}}}` + "\n\n"))
			_, _ = w.Write([]byte(`data: {"p":"response/content","v":" world"}` + "\n\n"))
			_, _ = w.Write([]byte(`data: {"p":"response/status","v":"FINISHED"}` + "\n\n"))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	exec := NewDeepSeekExecutor(nil)
	reqPayload := []byte(`{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"hello"}]}`)
	resp, err := exec.Execute(context.Background(), &cliproxyauth.Auth{
		ID:       "auth-1",
		Provider: "deepseek",
		Attributes: map[string]string{
			"api_key":  "token-1",
			"base_url": server.URL,
		},
	}, cliproxyexecutor.Request{
		Model:   "deepseek-v4-flash",
		Payload: reqPayload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(resp.Payload, &body); err != nil {
		t.Fatalf("Unmarshal response: %v\n%s", err, string(resp.Payload))
	}
	choices := body["choices"].([]any)
	message := choices[0].(map[string]any)["message"].(map[string]any)
	if got := message["content"]; got != "hello world" {
		t.Fatalf("content = %q, want hello world", got)
	}
	if got := message["reasoning_content"]; got != "thinking " {
		t.Fatalf("reasoning_content = %q, want thinking", got)
	}
	if !strings.HasPrefix(body["id"].(string), "chatcmpl-") {
		t.Fatalf("id = %v", body["id"])
	}
}

func writeJSON(t *testing.T, w http.ResponseWriter, body map[string]any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(body); err != nil {
		t.Fatalf("Encode JSON: %v", err)
	}
}
