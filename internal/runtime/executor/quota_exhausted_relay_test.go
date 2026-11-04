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
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// AI Studio relays Google errors; a QUOTA_EXHAUSTED keeps its evidence.
func TestAIStudioReportsGoogleQuotaExhaustion(t *testing.T) {
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
		var request wsrelay.Message
		if conn.ReadJSON(&request) != nil {
			return
		}
		_ = conn.WriteJSON(wsrelay.Message{ID: request.ID, Type: wsrelay.MessageTypeHTTPResp, Payload: map[string]any{"status": http.StatusTooManyRequests, "body": googleQuotaExhaustedBody}})
	}()
	exec := NewAIStudioExecutor(&config.Config{}, "aistudio", relay)
	_, err := exec.Execute(context.Background(), &cliproxyauth.Auth{ID: authID, Provider: "aistudio"}, cliproxyexecutor.Request{
		Model: "gemini-2.5-pro", Payload: []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatGemini})
	if exhausted, _, _ := quotaEvidenceOf(err); !exhausted || err.Error() != googleQuotaExhaustedBody {
		t.Fatalf("err = %T %v, want QUOTA_EXHAUSTED evidence with the error unchanged", err, err)
	}
}

// The OpenAI-compatible image stream reports insufficient_quota like the
// other request paths.
func TestOpenAICompatImageStreamReportsQuotaExhaustion(t *testing.T) {
	const body = `{"error":{"type":"insufficient_quota","code":"insufficient_quota","message":"quota"}}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	if exhausted, _, _ := quotaEvidenceOf(err); !exhausted || err.Error() != body || (hint != nil && hint.RetryAfter() != nil) {
		t.Fatalf("err = %T %v, want insufficient_quota evidence with the error and retry hint unchanged", err, err)
	}
}
