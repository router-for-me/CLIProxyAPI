package executor

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	"golang.org/x/oauth2"
)

func TestDiscoverGeminiCLIModels_OnlyKeepsProbeSuccessModels(t *testing.T) {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		switch r.URL.Path {
		case "/v1internal:retrieveUserQuota":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"buckets": []map[string]any{
					{"modelId": "gemini-2.5-pro"},
					{"modelId": "gemini-3-pro-preview"},
					{"modelId": "gemini-2.5-pro"},
				},
			})
		case "/v1internal:generateContent":
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode payload: %v", err)
			}
			model, _ := payload["model"].(string)
			switch model {
			case "gemini-2.5-pro":
				_ = json.NewEncoder(w).Encode(map[string]any{
					"candidates": []map[string]any{
						{"content": map[string]any{"parts": []map[string]any{{"text": "ok"}}}},
					},
				})
			case "gemini-3-pro-preview":
				w.WriteHeader(http.StatusNotFound)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"error": map[string]any{"message": "model not found"},
				})
			default:
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"error": map[string]any{"message": "unexpected model"},
				})
			}
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	now := time.Date(2026, 4, 24, 10, 0, 0, 0, time.UTC)
	result, err := discoverGeminiCLIModels(
		context.Background(),
		&internalconfig.Config{},
		&cliproxyauth.Auth{ID: "auth-1", Provider: "gemini-cli"},
		geminiCLIDiscoveryDeps{
			baseURL: server.URL,
			prepareTokenSource: func(context.Context, *internalconfig.Config, *cliproxyauth.Auth) (oauth2.TokenSource, map[string]any, error) {
				return oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "token"}), nil, nil
			},
			resolveProjectID: func(*cliproxyauth.Auth) string { return "project-1" },
			newHTTPClient: func(context.Context, *internalconfig.Config, *cliproxyauth.Auth, time.Duration) *http.Client {
				return server.Client()
			},
			applyHeaders: func(r *http.Request, model string) {
				r.Header.Set("X-Test-Model", model)
			},
			now: func() time.Time { return now },
		},
	)
	if err != nil {
		t.Fatalf("discoverGeminiCLIModels() error = %v", err)
	}

	if result.AuthID != "auth-1" {
		t.Fatalf("AuthID = %q, want auth-1", result.AuthID)
	}
	if result.ProjectID != "project-1" {
		t.Fatalf("ProjectID = %q, want project-1", result.ProjectID)
	}
	if !result.DiscoveredAt.Equal(now) {
		t.Fatalf("DiscoveredAt = %v, want %v", result.DiscoveredAt, now)
	}
	if len(result.AvailableModels) != 1 {
		t.Fatalf("available models = %d, want 1", len(result.AvailableModels))
	}
	if got := result.AvailableModels[0].ID; got != "gemini-2.5-pro" {
		t.Fatalf("available model = %q, want gemini-2.5-pro", got)
	}
	if result.AvailableModels[0].UserDefined {
		t.Fatalf("expected static-known model metadata, got UserDefined=true")
	}

	if len(result.ProbeStatuses) != 2 {
		t.Fatalf("probe status count = %d, want 2", len(result.ProbeStatuses))
	}
	if !result.ProbeStatuses[0].Available || result.ProbeStatuses[0].ModelID != "gemini-2.5-pro" {
		t.Fatalf("first probe = %+v, want successful gemini-2.5-pro", result.ProbeStatuses[0])
	}
	if result.ProbeStatuses[1].Available || result.ProbeStatuses[1].StatusCode != http.StatusNotFound {
		t.Fatalf("second probe = %+v, want unavailable 404", result.ProbeStatuses[1])
	}
}
