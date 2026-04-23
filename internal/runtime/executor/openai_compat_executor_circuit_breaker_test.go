package executor

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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

func TestOpenAICompatExecutorCircuitBreakerDefaultRecoveryTimeoutIs60Seconds(t *testing.T) {
	const (
		providerName = "cb-openai-compat-default-timeout"
		authID       = "cb-openai-compat-default-timeout-auth"
		modelID      = "cb-default-timeout-model"
	)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"error":{"message":"temporary upstream error"}}`))
	}))
	defer upstream.Close()

	executor := NewOpenAICompatExecutor(providerName, &config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{{
			Name:                           providerName,
			CircuitBreakerFailureThreshold: 1,
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
	reg.RegisterClient(authID, providerName, []*registry.ModelInfo{{ID: modelID}})
	t.Cleanup(func() {
		reg.ResetCircuitBreaker(authID, modelID)
		reg.UnregisterClient(authID)
	})

	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   modelID,
		Payload: []byte(fmt.Sprintf(`{"model":"%s","messages":[{"role":"user","content":"hi"}]}`, modelID)),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAI,
	})
	if err == nil {
		t.Fatal("expected upstream error")
	}

	if !reg.IsCircuitOpen(authID, modelID) {
		t.Fatalf("expected circuit to open for model %q", modelID)
	}
	snapshot := reg.SnapshotCircuitBreakersPersist()
	modelSnapshot, ok := snapshot[modelID]
	if !ok {
		t.Fatalf("missing circuit breaker snapshot for model %q", modelID)
	}
	status, ok := modelSnapshot[authID]
	if !ok {
		t.Fatalf("missing circuit breaker snapshot for auth %q", authID)
	}
	got := status.RecoveryAt.Sub(status.LastFailure)
	if got < 58*time.Second || got > 62*time.Second {
		t.Fatalf("recovery timeout = %v, want around 60s", got)
	}
}

func TestOpenAICompatExecutorExecuteStream_MidStreamReadFailureRecordsCircuitBreaker(t *testing.T) {
	const (
		providerName = "cb-openai-compat-stream-failure"
		authID       = "cb-openai-compat-stream-failure-auth"
		modelID      = "cb-stream-failure-model"
	)

	oversizedLine := "data: " + strings.Repeat("x", 52_430_000) + "\n\n"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(oversizedLine))
	}))
	defer upstream.Close()

	executor := NewOpenAICompatExecutor(providerName, &config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{{
			Name:                           providerName,
			CircuitBreakerFailureThreshold: 1,
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
	reg.RegisterClient(authID, providerName, []*registry.ModelInfo{{ID: modelID}})
	t.Cleanup(func() {
		reg.ResetCircuitBreaker(authID, modelID)
		reg.UnregisterClient(authID)
	})

	stream, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   modelID,
		Payload: []byte(fmt.Sprintf(`{"model":"%s","messages":[{"role":"user","content":"hi"}],"stream":true}`, modelID)),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAI,
		Stream:       true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}
	if stream == nil {
		t.Fatal("expected stream result")
	}

	var gotChunkErr error
	for chunk := range stream.Chunks {
		if chunk.Err != nil {
			gotChunkErr = chunk.Err
			break
		}
	}
	if gotChunkErr == nil {
		t.Fatal("expected stream chunk error from oversized SSE line")
	}
	if !reg.IsCircuitOpen(authID, modelID) {
		t.Fatalf("expected circuit to open after mid-stream read failure (model=%q)", modelID)
	}
}
