package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	internalcache "github.com/router-for-me/CLIProxyAPI/v7/internal/cache"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func TestAntigravityReasoningReplayClearsOnInvalidSignature400(t *testing.T) {
	internalcache.ClearAntigravityReasoningReplayCache()
	t.Cleanup(internalcache.ClearAntigravityReasoningReplayCache)

	model := "gemini-3-flash-agent"
	sessionKey := "session:pr3900-invalid-sig"
	bad := []byte(`{"type":"thought_signature","thoughtSignature":"INVALID_REPLAY_SIGNATURE_PR3900_XXXXXXXXX","contentIndex":1,"partIndex":0}`)
	if !internalcache.CacheAntigravityReasoningReplayItems(model, sessionKey, [][]byte{bad}) {
		t.Fatal("failed to seed replay cache")
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"Invalid thoughtSignature in model content","code":400}}`))
	}))
	defer server.Close()

	exec := NewAntigravityExecutor(&config.Config{RequestRetry: 1})
	auth := &cliproxyauth.Auth{
		ID: "auth-pr3900-invalid-sig",
		Attributes: map[string]string{
			"base_url": server.URL,
		},
		Metadata: map[string]any{
			"access_token": "token",
			"project_id":   "project-1",
			"expired":      time.Now().Add(1 * time.Hour).Format(time.RFC3339),
		},
	}

	payload := []byte(`{"sessionId":"pr3900-invalid-sig","request":{"contents":[{"role":"user","parts":[{"text":"hi"}]},{"role":"user","parts":[{"functionResponse":{"id":"id1","name":"Bash","response":{"result":"ok"}}}]}]}}`)
	_, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   model,
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatAntigravity,
		Stream:       false,
	})
	if err == nil {
		t.Fatal("expected upstream 400 error")
	}
	if _, ok, errGet := internalcache.GetAntigravityReasoningReplayItemsRequired(context.Background(), model, sessionKey); errGet != nil {
		t.Fatalf("get after clear: %v", errGet)
	} else if ok {
		t.Fatal("invalid signature 400 should clear cached replay item")
	}
}
