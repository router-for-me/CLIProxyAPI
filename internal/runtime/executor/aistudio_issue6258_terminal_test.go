package executor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/wsrelay"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestIssue6258AIStudioCleanStreamEnd(t *testing.T) {
	for _, test := range []struct {
		name, body, payload string
		httpStatus          int
	}{
		{"no_finish", issue6258ExecutorContent, `{"input":"hello","stream":true}`, 0},
		{"stop_without_usage", `{"responseId":"executor-6258","candidates":[{"content":{"parts":[{"text":"answer"}]},"finishReason":"STOP"}]}`, `{"input":"hello","stream":true}`, 0},
		{"valid_apply_patch_stop_control", executorPatchResponse, executorPatchRequest, 0},
		{"http_response_success", issue6258ExecutorContent, `{"input":"hello","stream":true}`, http.StatusOK},
		{"http_response_201_success", issue6258ExecutorContent, `{"input":"hello","stream":true}`, http.StatusCreated},
		{"http_response_error_after_start", issue6258ExecutorContent, `{"input":"hello","stream":true}`, http.StatusServiceUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			authID := t.Name()
			connected := make(chan struct{})
			relay := wsrelay.NewManager(wsrelay.Options{
				ProviderFactory: func(*http.Request) (string, error) { return authID, nil },
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
			<-connected
			clientDone := make(chan error, 1)
			go func() {
				var request wsrelay.Message
				if errReadJSON := conn.ReadJSON(&request); errReadJSON != nil {
					clientDone <- errReadJSON
					return
				}
				responses := []wsrelay.Message{
					{ID: request.ID, Type: wsrelay.MessageTypeStreamStart, Payload: map[string]any{"status": 200}},
					{ID: request.ID, Type: wsrelay.MessageTypeStreamChunk, Payload: map[string]any{"data": "data: " + test.body + "\n\n"}},
					{ID: request.ID, Type: wsrelay.MessageTypeStreamEnd},
				}
				if test.httpStatus > 0 {
					responses = []wsrelay.Message{{ID: request.ID, Type: wsrelay.MessageTypeHTTPResp, Payload: map[string]any{"status": test.httpStatus, "body": test.body}}}
					if test.httpStatus >= 400 {
						responses = append([]wsrelay.Message{{ID: request.ID, Type: wsrelay.MessageTypeStreamStart, Payload: map[string]any{"status": 200}}}, responses...)
					}
				}
				for _, response := range responses {
					if errWriteJSON := conn.WriteJSON(response); errWriteJSON != nil {
						clientDone <- errWriteJSON
						return
					}
				}
				clientDone <- nil
			}()
			executor := NewAIStudioExecutor(&config.Config{}, "aistudio", relay)
			auth := &cliproxyauth.Auth{ID: authID, Provider: "aistudio"}
			chunks := issue6258StartStream(t, t.Context(), executor, auth, test.payload)
			if test.httpStatus >= 400 {
				errors := 0
				for chunk := range chunks {
					if chunk.Err != nil {
						errors++
					}
					for _, event := range issue6258Events(chunk.Payload) {
						kind := event.Get("type").String()
						if kind == "response.completed" || kind == "response.incomplete" {
							t.Errorf("unsuccessful HTTPResp synthesized %s", kind)
						}
					}
				}
				if errors != 1 {
					t.Errorf("unsuccessful HTTPResp errors=%d, want 1", errors)
				}
				if errClient := <-clientDone; errClient != nil {
					t.Fatal(errClient)
				}
				return
			}
			response := issue6258ExecutorTerminal(t, chunks)
			if test.name == "valid_apply_patch_stop_control" {
				assertExecutorPatchOutput(t, []byte(response.Raw))
			} else if response.Get("output.0.content.0.text").String() != "answer" {
				t.Errorf("stream_end lost content: %s", response.Raw)
			}
			if errClient := <-clientDone; errClient != nil {
				t.Fatal(errClient)
			}
		})
	}
}
