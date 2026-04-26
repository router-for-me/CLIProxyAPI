package auth

import (
	"context"
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
)

func TestManagerMarkResult_OpenAICompatProviderNameAuthSkipsDuplicateCircuitBreakerReport(t *testing.T) {
	const (
		authID       = "openai-compat-abrdns-auth"
		providerName = "abrdns"
		modelID      = "claude-opus-test"
	)

	manager := NewManager(nil, nil, nil)
	auth := &Auth{
		ID:       authID,
		Provider: providerName,
		Status:   StatusActive,
		Attributes: map[string]string{
			"api_key":      "test-key",
			"base_url":     "https://example.invalid/v1",
			"compat_name":  providerName,
			"provider_key": providerName,
		},
	}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(authID, providerName, []*registry.ModelInfo{{ID: modelID}})
	t.Cleanup(func() {
		reg.ResetCircuitBreaker(authID, modelID)
		reg.UnregisterClient(authID)
	})

	result := Result{
		AuthID:   authID,
		Provider: providerName,
		Model:    modelID,
		Success:  false,
		Error: &Error{
			HTTPStatus: http.StatusTooManyRequests,
			Message:    "quota",
		},
	}
	if err := manager.markResult(context.Background(), result); err != nil {
		t.Fatalf("markResult error: %v", err)
	}

	status := reg.GetCircuitBreakerStatus()
	if perModel, ok := status[authID]; ok {
		if _, recorded := perModel[modelID]; recorded {
			t.Fatalf("expected conductor to skip circuit breaker registry reporting for openai-compatible auth %s/%s", authID, modelID)
		}
	}
}
