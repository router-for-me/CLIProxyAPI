package executor

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
)

func TestCircuitBreakerModelID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		opts     cliproxyexecutor.Options
		fallback string
		want     string
	}{
		{
			name: "requested model from metadata",
			opts: cliproxyexecutor.Options{
				Metadata: map[string]any{
					cliproxyexecutor.RequestedModelMetadataKey: "gpt-5.3-codex",
				},
			},
			fallback: "gpt-5.3-codex-high",
			want:     "gpt-5.3-codex",
		},
		{
			name: "requested model strips suffix",
			opts: cliproxyexecutor.Options{
				Metadata: map[string]any{
					cliproxyexecutor.RequestedModelMetadataKey: "gpt-5.3-codex(high)",
				},
			},
			fallback: "gpt-5.3-codex-high",
			want:     "gpt-5.3-codex",
		},
		{
			name:     "fallback strips suffix",
			opts:     cliproxyexecutor.Options{},
			fallback: "gpt-5.3-codex-high(high)",
			want:     "gpt-5.3-codex-high",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := circuitBreakerModelID(tt.opts, tt.fallback)
			if got != tt.want {
				t.Fatalf("circuitBreakerModelID() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestOpenAICompatExecutorCircuitBreakerUsesRequestedModel(t *testing.T) {
	// Ensure clean capability resolver state for the auth ID used by this test.
	globalResponsesCapabilityResolver.Invalidate("cb-openai-compat-auth")
	defer globalResponsesCapabilityResolver.Invalidate("cb-openai-compat-auth")

	const (
		providerName  = "cb-openai-compat"
		authID        = "cb-openai-compat-auth"
		aliasModel    = "cb-alias-model"
		upstreamModel = "cb-upstream-model"
	)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusGatewayTimeout)
		_, _ = w.Write([]byte(`{"error":{"message":"timeout","type":"server_error","code":"internal_server_error"}}`))
	}))
	defer upstream.Close()

	executor := NewOpenAICompatExecutor(providerName, &config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{{
			Name:                           providerName,
			CircuitBreakerFailureThreshold: 2,
			CircuitBreakerRecoveryTimeout:  43200,
		}},
	})
	auth := &cliproxyauth.Auth{
		ID:       authID,
		Provider: providerName,
		Attributes: map[string]string{
			"base_url":     upstream.URL + "/v1",
			"api_key":      "test-key",
			"compat_name":  providerName,
			"provider_key": providerName,
		},
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(authID, providerName, []*registry.ModelInfo{{ID: aliasModel}})
	t.Cleanup(func() {
		reg.ResetCircuitBreaker(authID, aliasModel)
		reg.ResetCircuitBreaker(authID, upstreamModel)
		reg.UnregisterClient(authID)
	})

	req := cliproxyexecutor.Request{
		Model:   upstreamModel,
		Payload: []byte(fmt.Sprintf(`{"model":"%s","messages":[{"role":"user","content":"hi"}]}`, aliasModel)),
	}
	opts := cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAI,
		Metadata: map[string]any{
			cliproxyexecutor.RequestedModelMetadataKey: aliasModel,
		},
	}

	for i := 0; i < 2; i++ {
		_, err := executor.Execute(context.Background(), auth, req, opts)
		if err == nil {
			t.Fatalf("attempt %d: expected upstream error", i+1)
		}
	}

	if !reg.IsCircuitOpen(authID, aliasModel) {
		t.Fatalf("expected circuit to open for requested model %q", aliasModel)
	}
	if reg.IsCircuitOpen(authID, upstreamModel) {
		t.Fatalf("did not expect circuit to open for upstream model %q", upstreamModel)
	}
}
