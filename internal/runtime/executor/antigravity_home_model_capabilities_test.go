package executor

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executionregistry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

type antigravityHomeModelCapabilityDispatcher struct {
	baseURL string
}

func (*antigravityHomeModelCapabilityDispatcher) HeartbeatOK() bool { return true }

func (d *antigravityHomeModelCapabilityDispatcher) RPopAuth(context.Context, string, string, http.Header, int) ([]byte, error) {
	return json.Marshal(map[string]any{
		"model":    "gemini-3.8-flash-high",
		"provider": "antigravity",
		"model_info": map[string]any{
			"id":             "gemini-3.8-flash-high",
			"type":           "gemini",
			"context_length": 1048576,
			"thinking": map[string]any{
				"levels": []string{"low", "medium", "high"},
			},
			"user_defined": false,
		},
		"auth": cliproxyauth.Auth{
			ID:       "home-antigravity-3.8-auth",
			Provider: "antigravity",
			Status:   cliproxyauth.StatusActive,
			Attributes: map[string]string{
				cliproxyauth.AttributeAuthKind: cliproxyauth.AuthKindOAuth,
				"base_url":                     d.baseURL,
			},
			Metadata: map[string]any{
				"access_token": "token",
				"project_id":   "project-1",
				"expired":      time.Now().Add(time.Hour).Format(time.RFC3339),
			},
		},
	})
}

func (*antigravityHomeModelCapabilityDispatcher) AbortAmbiguousDispatch() {}

func TestAntigravityHomeModelThinkingLevelRemainsLevel(t *testing.T) {
	requestBodies := make(chan []byte, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			http.Error(w, errRead.Error(), http.StatusInternalServerError)
			return
		}
		requestBodies <- body
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}}`)
	}))
	t.Cleanup(server.Close)

	cfg := &config.Config{RequestRetry: 1}
	cfg.Home.Enabled = true
	manager := cliproxyauth.NewManager(nil, nil, nil)
	manager.SetConfig(cfg)
	manager.RegisterExecutor(NewAntigravityExecutor(cfg))
	manager.PublishHomeDispatch(&antigravityHomeModelCapabilityDispatcher{baseURL: server.URL}, executionregistry.New(), 1)

	payload := []byte(`{"model":"gemini-3.8-flash-high","reasoning_effort":"medium","messages":[{"role":"user","content":"hello"}]}`)
	_, errExecute := manager.Execute(context.Background(), []string{"antigravity"}, cliproxyexecutor.Request{
		Model:   "gemini-3.8-flash-high",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatOpenAI,
		ResponseFormat:  sdktranslator.FormatOpenAI,
		OriginalRequest: payload,
	})
	if errExecute != nil {
		t.Fatalf("Execute() error = %v", errExecute)
	}

	upstreamBody := <-requestBodies
	if got := gjson.GetBytes(upstreamBody, "request.generationConfig.thinkingConfig.thinkingLevel").String(); got != "medium" {
		t.Fatalf("thinkingLevel = %q, want medium; body=%s", got, upstreamBody)
	}
	if budget := gjson.GetBytes(upstreamBody, "request.generationConfig.thinkingConfig.thinkingBudget"); budget.Exists() {
		t.Fatalf("thinkingBudget should be absent, got %s; body=%s", budget.Raw, upstreamBody)
	}
}
