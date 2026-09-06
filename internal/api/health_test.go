package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestEvaluateReadiness_EmptyConfigIsReady(t *testing.T) {
	ready, reason, usable, total := evaluateReadiness(&config.Config{}, auth.NewManager(nil, nil, nil))
	if !ready || reason != "ok" || usable != 0 || total != 0 {
		t.Fatalf("evaluateReadiness() = ready=%t reason=%q usable=%d total=%d", ready, reason, usable, total)
	}
}

func TestEvaluateReadiness_ConfiguredProviderWithoutAuthIsNotReady(t *testing.T) {
	cfg := &config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{{
			Name:    "example",
			BaseURL: "https://example.invalid/v1",
		}},
	}
	ready, reason, _, _ := evaluateReadiness(cfg, auth.NewManager(nil, nil, nil))
	if ready || reason != "no_usable_auth" {
		t.Fatalf("evaluateReadiness() = ready=%t reason=%q, want not ready", ready, reason)
	}
}

func TestEvaluateReadiness_HomeEnabledWithoutLocalAuthIsReady(t *testing.T) {
	cfg := &config.Config{}
	cfg.Home.Enabled = true
	ready, reason, _, _ := evaluateReadiness(cfg, auth.NewManager(nil, nil, nil))
	if !ready || reason != "ok" {
		t.Fatalf("evaluateReadiness() = ready=%t reason=%q, want ready with Home enabled", ready, reason)
	}
}

func TestEvaluateReadiness_AllAuthsUnusableIsNotReady(t *testing.T) {
	manager := auth.NewManager(nil, nil, nil)
	if _, err := manager.Register(context.Background(), &auth.Auth{
		ID:          "broken.json",
		Provider:    "codex",
		Status:      auth.StatusError,
		Unavailable: true,
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	ready, reason, usable, total := evaluateReadiness(&config.Config{}, manager)
	if ready || reason != "no_usable_auth" || usable != 0 || total != 1 {
		t.Fatalf("evaluateReadiness() = ready=%t reason=%q usable=%d total=%d", ready, reason, usable, total)
	}
}

func TestEvaluateReadiness_ActiveAuthIsReady(t *testing.T) {
	manager := auth.NewManager(nil, nil, nil)
	if _, err := manager.Register(context.Background(), &auth.Auth{
		ID:       "ok.json",
		Provider: "codex",
		Status:   auth.StatusActive,
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	ready, reason, usable, total := evaluateReadiness(&config.Config{}, manager)
	if !ready || reason != "ok" || usable != 1 || total != 1 {
		t.Fatalf("evaluateReadiness() = ready=%t reason=%q usable=%d total=%d", ready, reason, usable, total)
	}
}

func TestReadyzAndMetricsRoutes(t *testing.T) {
	auth.ResetUpstreamMetricsForTest()

	server := newTestServer(t)
	t.Run("readyz empty is 200", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
		rr := httptest.NewRecorder()
		server.engine.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("readyz status = %d, want %d body=%s", rr.Code, http.StatusOK, rr.Body.String())
		}
		var resp struct {
			Ready  bool   `json:"ready"`
			Status string `json:"status"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
			t.Fatalf("parse readyz: %v", err)
		}
		if !resp.Ready || resp.Status != "ok" {
			t.Fatalf("readyz body = %+v", resp)
		}
	})

	t.Run("readyz HEAD", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodHead, "/readyz", nil)
		rr := httptest.NewRecorder()
		server.engine.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("HEAD readyz status = %d, want %d", rr.Code, http.StatusOK)
		}
		if rr.Body.Len() != 0 {
			t.Fatalf("HEAD readyz body = %q, want empty", rr.Body.String())
		}
	})

	t.Run("readyz 503 when only error auths exist", func(t *testing.T) {
		if _, err := server.handlers.AuthManager.Register(context.Background(), &auth.Auth{
			ID:          "dead.json",
			Provider:    "openai-compatibility",
			Status:      auth.StatusError,
			Unavailable: true,
		}); err != nil {
			t.Fatalf("register: %v", err)
		}
		req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
		rr := httptest.NewRecorder()
		server.engine.ServeHTTP(rr, req)
		if rr.Code != http.StatusServiceUnavailable {
			t.Fatalf("readyz status = %d, want %d body=%s", rr.Code, http.StatusServiceUnavailable, rr.Body.String())
		}
	})

	t.Run("healthz stays 200 while readyz is 503", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
		rr := httptest.NewRecorder()
		server.engine.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("healthz status = %d, want %d", rr.Code, http.StatusOK)
		}
	})

	t.Run("metrics includes auth gauges and upstream counters", func(t *testing.T) {
		server.handlers.AuthManager.MarkResult(context.Background(), auth.Result{
			AuthID:   "dead.json",
			Provider: "openai-compatibility",
			Success:  false,
			Error:    &auth.Error{HTTPStatus: http.StatusPaymentRequired, Message: "quota"},
		})
		req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
		rr := httptest.NewRecorder()
		server.engine.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("metrics status = %d, want %d body=%s", rr.Code, http.StatusOK, rr.Body.String())
		}
		body := rr.Body.String()
		for _, want := range []string{
			"cliproxy_auth_status",
			`cliproxy_auth_status{status="error"}`,
			"cliproxy_auth_unavailable",
			`cliproxy_upstream_errors_total{status="402"}`,
		} {
			if !strings.Contains(body, want) {
				t.Fatalf("metrics missing %q:\n%s", want, body)
			}
		}
	})
}
