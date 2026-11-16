package executor

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/wsrelay"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/httptransport"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestAIStudioSubmissionRequiresParsedRefusal(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, tc := range []struct {
			name     string
			messages []wsrelay.Message
			refusal  bool
		}{
			{name: "missing_payload", messages: []wsrelay.Message{{Type: wsrelay.MessageTypeHTTPResp}}},
			{name: "missing_stream_start_payload", messages: []wsrelay.Message{{Type: wsrelay.MessageTypeStreamStart}, {Type: wsrelay.MessageTypeStreamEnd}}},
			{name: "malformed_after_stream_start", messages: []wsrelay.Message{{Type: wsrelay.MessageTypeStreamStart, Payload: map[string]any{"status": 200}}, {Type: wsrelay.MessageTypeHTTPResp}}},
			{name: "invalid_error_json", messages: []wsrelay.Message{{Type: wsrelay.MessageTypeHTTPResp, Payload: map[string]any{"status": 503, "body": `{"error":{"message":"refused"`}}}},
			{name: "empty_error_object", messages: []wsrelay.Message{{Type: wsrelay.MessageTypeHTTPResp, Payload: map[string]any{"status": 503, "body": `{"error":{}}`}}}},
			{name: "parsed_refusal", messages: []wsrelay.Message{{Type: wsrelay.MessageTypeHTTPResp, Payload: map[string]any{"status": 503, "body": `{"error":{"code":503,"message":"provider refused generation"}}`}}}, refusal: true},
			{name: "parsed_stream_refusal", messages: []wsrelay.Message{{Type: wsrelay.MessageTypeStreamStart, Payload: map[string]any{"status": 503}}, {Type: wsrelay.MessageTypeStreamChunk, Payload: map[string]any{"data": `{"error":{"message":`}}, {Type: wsrelay.MessageTypeStreamChunk, Payload: map[string]any{"data": `"provider refused generation"}}`}}, {Type: wsrelay.MessageTypeStreamEnd}}, refusal: true},
		} {
			t.Run(fmt.Sprintf("stream=%t/%s", stream, tc.name), func(t *testing.T) {
				var frames atomic.Int32
				authIDs := []string{t.Name() + "a", t.Name() + "b"}
				connected := make(chan string, 2)
				relay := wsrelay.NewManager(wsrelay.Options{
					ProviderFactory: func(r *http.Request) (string, error) { return r.URL.Query().Get("id"), nil },
					OnConnected:     func(id string) { connected <- id },
				})
				server := httptest.NewServer(relay.Handler())
				t.Cleanup(server.Close)
				t.Cleanup(func() {
					if errStop := relay.Stop(context.Background()); errStop != nil {
						t.Error(errStop)
					}
				})
				for index, authID := range authIDs {
					conn, _, errDial := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+relay.Path()+"?id="+authID, nil)
					if errDial != nil {
						t.Fatal(errDial)
					}
					t.Cleanup(func() {
						if errClose := conn.Close(); errClose != nil {
							t.Error(errClose)
						}
					})
					<-connected
					go func() {
						var request wsrelay.Message
						if errRead := conn.ReadJSON(&request); errRead != nil {
							return
						}
						frames.Add(1)
						if index == 0 {
							for _, message := range tc.messages {
								message.ID = request.ID
								if errWrite := conn.WriteJSON(message); errWrite != nil {
									t.Error(errWrite)
									return
								}
							}
							return
						}
						body := `{"candidates":[{"content":{"role":"model","parts":[{"text":"safe-fallback"}]},"finishReason":"STOP"}]}`
						if errWrite := writeSubmissionRelayResponse(conn, request.ID, http.StatusOK, body); errWrite != nil {
							t.Error(errWrite)
						}
					}()
				}
				manager := coreauth.NewManager(nil, &coreauth.FillFirstSelector{}, nil)
				manager.SetRetryConfig(0, 0, 0)
				manager.RegisterExecutor(NewAIStudioExecutor(&config.Config{}, "aistudio", relay))
				const model = "gemini-2.5-flash"
				for _, authID := range authIDs {
					if _, errRegister := manager.Register(context.Background(), &coreauth.Auth{ID: authID, Provider: "aistudio", Status: coreauth.StatusActive}); errRegister != nil {
						t.Fatal(errRegister)
					}
					registry.GetGlobalRegistry().RegisterClient(authID, "aistudio", []*registry.ModelInfo{{ID: model}})
					t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(authID) })
				}
				request := coreexecutor.Request{Model: model, Payload: []byte(`{"input":"hello"}`)}
				options := coreexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, ResponseFormat: sdktranslator.FormatOpenAIResponse, Stream: stream}
				var errExecute error
				if stream {
					result, errStream := manager.ExecuteStream(context.Background(), []string{"aistudio"}, request, options)
					errExecute = errStream
					if result != nil {
						for chunk := range result.Chunks {
							if chunk.Err != nil {
								errExecute = chunk.Err
							}
						}
					}
				} else {
					_, errExecute = manager.Execute(context.Background(), []string{"aistudio"}, request, options)
				}
				if tc.refusal {
					if errExecute != nil || frames.Load() != 2 {
						t.Fatalf("parsed refusal lost failover: frames=%d err=%v", frames.Load(), errExecute)
					}
				} else if frames.Load() != 1 || !httptransport.IsOutcomeUnknown(errExecute) {
					t.Fatalf("unparseable relay response replayed: frames=%d err=%v", frames.Load(), errExecute)
				}
			})
		}
	}
}
