package executor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/wsrelay"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// AI Studio relays Google errors; a QUOTA_EXHAUSTED keeps its evidence on
// generation and token counting.
func TestAIStudioReportsGoogleQuotaExhaustion(t *testing.T) {
	for _, path := range []string{"execute", "count"} {
		t.Run(path, func(t *testing.T) {
			authID := t.Name()
			connected := make(chan struct{})
			relay := wsrelay.NewManager(wsrelay.Options{
				ProviderFactory: func(*http.Request) (string, error) { return authID, nil },
				OnConnected:     func(string) { close(connected) },
			})
			server := httptest.NewServer(relay.Handler())
			t.Cleanup(server.Close)
			t.Cleanup(func() { _ = relay.Stop(context.Background()) })
			conn, _, errDial := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+relay.Path(), nil)
			if errDial != nil {
				t.Fatal(errDial)
			}
			t.Cleanup(func() { _ = conn.Close() })
			<-connected
			go func() {
				_, request, errRead := conn.ReadMessage()
				if errRead != nil {
					return
				}
				response := ojson.NewDocString(`{}`)
				for path, value := range map[string]any{"id": ojson.GetBytes(request, "id").String(), "type": wsrelay.MessageTypeHTTPResp, "payload.status": http.StatusTooManyRequests, "payload.body": googleQuotaExhaustedBody} {
					if errSet := response.Set(path, value); errSet != nil {
						return
					}
				}
				_ = conn.WriteMessage(websocket.TextMessage, response.Bytes())
			}()
			exec := NewAIStudioExecutor(&config.Config{}, "aistudio", relay)
			auth := &cliproxyauth.Auth{ID: authID, Provider: "aistudio"}
			req := cliproxyexecutor.Request{Model: "gemini-2.5-pro", Payload: []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)}
			opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatGemini}
			var err error
			if path == "count" {
				_, err = exec.CountTokens(context.Background(), auth, req, opts)
			} else {
				_, err = exec.Execute(context.Background(), auth, req, opts)
			}
			if exhausted, _, _ := quotaEvidenceOf(err); !exhausted || err.Error() != googleQuotaExhaustedBody {
				t.Fatalf("err = %T %v, want QUOTA_EXHAUSTED evidence with the error unchanged", err, err)
			}
		})
	}
}

// The OpenAI-compatible image stream reports insufficient_quota like the
// other request paths.
func TestOpenAICompatImageStreamReportsQuotaExhaustion(t *testing.T) {
	const body = `{"error":{"type":"insufficient_quota","code":"insufficient_quota","message":"quota"}}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "7200")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	exec := NewOpenAICompatExecutor("openai-compatibility", &config.Config{})
	auth := &cliproxyauth.Auth{Provider: "openai-compatibility", Attributes: map[string]string{"base_url": server.URL, "api_key": "test"}}
	_, err := exec.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model: "gpt-image-2", Payload: []byte(`{"model":"gpt-image-2","prompt":"test"}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-image"), Stream: true})
	hint, _ := err.(interface{ RetryAfter() *time.Duration })
	if exhausted, _, reset := quotaEvidenceOf(err); !exhausted || reset != 2*time.Hour || err.Error() != body || (hint != nil && hint.RetryAfter() != nil) {
		t.Fatalf("err = %T %v, want insufficient_quota evidence with the Retry-After reset and the error and retry hint unchanged", err, err)
	}
}
