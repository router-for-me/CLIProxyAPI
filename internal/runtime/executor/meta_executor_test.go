package executor

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func TestMetaExecutor_Identifier(t *testing.T) {
	exec := NewMetaExecutor(&config.Config{})
	if exec.Identifier() != "meta" {
		t.Fatalf("expected 'meta', got '%s'", exec.Identifier())
	}
}

func TestMetaExecutor_MetaCredsResolution(t *testing.T) {
	t.Run("from attributes", func(t *testing.T) {
		auth := &cliproxyauth.Auth{
			Attributes: map[string]string{
				"api_key":  "attr-key",
				"base_url": "https://custom.meta.com/v1",
			},
		}
		baseURL, token := metaCreds(auth)
		if baseURL != "https://custom.meta.com/v1" || token != "attr-key" {
			t.Errorf("unexpected creds: base=%s, token=%s", baseURL, token)
		}
	})

	t.Run("from metadata", func(t *testing.T) {
		auth := &cliproxyauth.Auth{
			Metadata: map[string]any{
				"access_token": "meta-oauth-token",
			},
		}
		baseURL, token := metaCreds(auth)
		if baseURL != "https://api.meta.ai/v1" || token != "meta-oauth-token" {
			t.Errorf("unexpected creds: base=%s, token=%s", baseURL, token)
		}
	})

	t.Run("from env var", func(t *testing.T) {
		t.Setenv("META_API_KEY", "env-secret-key")
		baseURL, token := metaCreds(nil)
		if baseURL != "https://api.meta.ai/v1" || token != "env-secret-key" {
			t.Errorf("unexpected creds: base=%s, token=%s", baseURL, token)
		}
	})

	t.Run("from local muse auth", func(t *testing.T) {
		t.Setenv("META_API_KEY", "")
		tempDir := t.TempDir()
		authPath := filepath.Join(tempDir, "auth.json")
		content := `{"schema_version":1,"providers":{"meta":{"api_key":"local-muse-key","api_base_url":"https://api.meta.ai/v1"}}}`
		_ = os.WriteFile(authPath, []byte(content), 0600)
		t.Setenv("MUSE_AUTH_PATH", authPath)

		baseURL, token := metaCreds(nil)
		if baseURL != "https://api.meta.ai/v1" || token != "local-muse-key" {
			t.Errorf("unexpected creds: base=%s, token=%s", baseURL, token)
		}
	})
}

func TestMetaExecutor_ParseRetryAfter(t *testing.T) {
	now := time.Now()
	futureEpoch := now.Add(45 * time.Minute).Unix()

	body := []byte(mapToJSON(map[string]any{
		"error": map[string]any{
			"code":      "rate_limit_exceeded",
			"message":   "Subscription quota exhausted.",
			"resets_at": futureEpoch,
			"type":      "rate_limit_error",
		},
	}))

	retryAfter := parseMetaRetryAfter(http.StatusTooManyRequests, body, now)
	if retryAfter == nil {
		t.Fatalf("expected non-nil retryAfter")
	}
	if *retryAfter < 40*time.Minute || *retryAfter > 50*time.Minute {
		t.Errorf("unexpected retryAfter duration: %v", *retryAfter)
	}

	// Non-429 status returns nil
	if got := parseMetaRetryAfter(http.StatusOK, body, now); got != nil {
		t.Errorf("expected nil for 200 OK")
	}

	// Past epoch returns nil
	pastBody := []byte(mapToJSON(map[string]any{
		"error": map[string]any{
			"resets_at": now.Add(-10 * time.Minute).Unix(),
		},
	}))
	if got := parseMetaRetryAfter(http.StatusTooManyRequests, pastBody, now); got != nil {
		t.Errorf("expected nil for past reset time")
	}
}

func TestMetaExecutor_ExecuteSuccessAndRateLimit(t *testing.T) {
	now := time.Now()
	resetEpoch := now.Add(2 * time.Hour).Unix()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer valid-token" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
			return
		}

		if r.URL.Path == "/chat/completions" {
			// Check user agent
			if ua := r.Header.Get("User-Agent"); ua != "muse-code/1.0.2" {
				t.Errorf("expected User-Agent muse-code/1.0.2, got %s", ua)
			}
			// Simulate 429 rate limit
			w.WriteHeader(http.StatusTooManyRequests)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error": map[string]any{
					"code":      "rate_limit_exceeded",
					"message":   "Subscription quota exhausted. Your usage window resets soon.",
					"resets_at": resetEpoch,
					"type":      "rate_limit_error",
				},
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	cfg := &config.Config{}
	exec := NewMetaExecutor(cfg)

	auth := &cliproxyauth.Auth{
		Attributes: map[string]string{
			"api_key":  "valid-token",
			"base_url": server.URL,
		},
	}

	req := cliproxyexecutor.Request{
		Model:   "muse-spark-1.3",
		Payload: []byte(`{"model":"muse-spark-1.3","messages":[{"role":"user","content":"hello"}]}`),
	}
	opts := cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
	}

	_, err := exec.Execute(context.Background(), auth, req, opts)
	if err == nil {
		t.Fatalf("expected error from 429 response")
	}

	if se, ok := err.(statusErr); ok {
		if se.StatusCode() != http.StatusTooManyRequests {
			t.Errorf("expected 429 status code, got %d", se.StatusCode())
		}
		if se.RetryAfter() == nil {
			t.Fatalf("expected non-nil RetryAfter")
		}
		if *se.RetryAfter() < 1*time.Hour || *se.RetryAfter() > 3*time.Hour {
			t.Errorf("unexpected RetryAfter: %v", *se.RetryAfter())
		}
	} else {
		t.Fatalf("expected statusErr, got %T: %v", err, err)
	}
}

func mapToJSON(m map[string]any) string {
	b, _ := json.Marshal(m)
	return string(b)
}
