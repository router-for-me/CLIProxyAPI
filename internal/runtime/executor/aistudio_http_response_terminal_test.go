package executor

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/wsrelay"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

// Adapt the non-bridge HTTPResp acceptance cases from upstream 5ec31442.
func TestAIStudioBufferedResponseTerminal(t *testing.T) {
	for _, tc := range []struct {
		name, finish, status string
		httpStatus           int
		startFirst           bool
	}{
		{"no_finish", "", "completed", http.StatusOK, false},
		{"stop_without_usage", "STOP", "completed", http.StatusOK, false},
		{"created_success", "", "completed", http.StatusCreated, false},
		{"max_tokens", "MAX_TOKENS", "incomplete", http.StatusOK, false},
		{"error_after_start", "", "", http.StatusServiceUnavailable, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			authID := t.Name()
			connected := make(chan struct{})
			relay := wsrelay.NewManager(wsrelay.Options{
				ProviderFactory: func(*http.Request) (string, error) { return authID, nil },
				OnConnected:     func(string) { close(connected) },
			})
			server := httptest.NewServer(relay.Handler())
			defer server.Close()
			defer func() {
				if err := relay.Stop(context.Background()); err != nil {
					t.Error(err)
				}
			}()
			conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+relay.Path(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := conn.Close(); err != nil {
					t.Error(err)
				}
			}()
			<-connected
			done := make(chan error, 1)
			go func() {
				var request wsrelay.Message
				if err := conn.ReadJSON(&request); err != nil {
					done <- err
					return
				}
				if tc.startFirst {
					if err := conn.WriteJSON(wsrelay.Message{ID: request.ID, Type: wsrelay.MessageTypeStreamStart, Payload: map[string]any{"status": http.StatusOK}}); err != nil {
						done <- err
						return
					}
				}
				candidate := map[string]any{"content": map[string]any{"parts": []any{map[string]any{"text": "answer"}}}}
				if tc.finish != "" {
					candidate["finishReason"] = tc.finish
				}
				body := map[string]any{"responseId": "buffered-response", "candidates": []any{candidate}}
				if tc.httpStatus >= 400 {
					body = map[string]any{"error": map[string]any{"message": "fixture unavailable"}}
				}
				encoded, err := json.Marshal(body)
				if err != nil {
					done <- err
					return
				}
				done <- conn.WriteJSON(wsrelay.Message{
					ID: request.ID, Type: wsrelay.MessageTypeHTTPResp,
					Payload: map[string]any{"status": tc.httpStatus, "body": string(encoded)},
				})
			}()
			exec := NewAIStudioExecutor(&config.Config{}, "aistudio", relay)
			request := []byte(`{"input":"hello","stream":true}`)
			result, err := exec.ExecuteStream(t.Context(), &cliproxyauth.Auth{ID: authID, Provider: "aistudio"},
				cliproxyexecutor.Request{Model: "gemini-fixture", Payload: request},
				cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, ResponseFormat: sdktranslator.FormatOpenAIResponse, OriginalRequest: request, Stream: true})
			if err != nil {
				t.Fatal(err)
			}
			var terminal gjson.Result
			terminals, failures := 0, 0
			for chunk := range result.Chunks {
				if chunk.Err != nil {
					failures++
				}
				for _, line := range strings.Split(string(chunk.Payload), "\n") {
					if !strings.HasPrefix(line, "data:") {
						continue
					}
					event := gjson.Parse(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
					switch event.Get("type").String() {
					case "response.completed", "response.incomplete":
						terminals++
						terminal = event.Get("response")
					}
				}
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			if tc.httpStatus >= 400 {
				if failures != 1 || terminals != 0 {
					t.Fatalf("error response: failures=%d terminals=%d", failures, terminals)
				}
			} else if failures != 0 || terminals != 1 || terminal.Get("status").String() != tc.status ||
				terminal.Get("output.0.content.0.text").String() != "answer" {
				t.Fatalf("success response: failures=%d terminals=%d response=%s", failures, terminals, terminal.Raw)
			}
		})
	}
}
