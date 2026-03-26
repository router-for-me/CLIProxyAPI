package auth

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestExtractAccessToken(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		metadata map[string]any
		expected string
	}{
		{
			"antigravity top-level access_token",
			map[string]any{"access_token": "tok-abc"},
			"tok-abc",
		},
		{
			"gemini nested token.access_token",
			map[string]any{
				"token": map[string]any{"access_token": "tok-nested"},
			},
			"tok-nested",
		},
		{
			"top-level takes precedence over nested",
			map[string]any{
				"access_token": "tok-top",
				"token":        map[string]any{"access_token": "tok-nested"},
			},
			"tok-top",
		},
		{
			"empty metadata",
			map[string]any{},
			"",
		},
		{
			"whitespace-only access_token",
			map[string]any{"access_token": "   "},
			"",
		},
		{
			"wrong type access_token",
			map[string]any{"access_token": 12345},
			"",
		},
		{
			"token is not a map",
			map[string]any{"token": "not-a-map"},
			"",
		},
		{
			"nested whitespace-only",
			map[string]any{
				"token": map[string]any{"access_token": "  "},
			},
			"",
		},
		{
			"fallback to nested when top-level empty",
			map[string]any{
				"access_token": "",
				"token":        map[string]any{"access_token": "tok-fallback"},
			},
			"tok-fallback",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := extractAccessToken(tt.metadata)
			if got != tt.expected {
				t.Errorf("extractAccessToken() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestFileTokenStoreReadAuthFile_HydratesGeminiProjectID(t *testing.T) {
	tempDir := t.TempDir()
	authPath := filepath.Join(tempDir, "gemini-auth.json")
	if err := os.WriteFile(authPath, []byte(`{
		"type":"gemini",
		"email":"user@example.com",
		"token":{
			"refresh_token":"refresh-token",
			"client_id":"client-id",
			"client_secret":"client-secret",
			"token_uri":"https://oauth2.googleapis.com/token"
		}
	}`), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	prevClient := fileTokenStoreHTTPClient
	prevFetch := fileTokenStoreFetchProjectIDFn
	t.Cleanup(func() {
		fileTokenStoreHTTPClient = prevClient
		fileTokenStoreFetchProjectIDFn = prevFetch
	})

	fileTokenStoreHTTPClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.Method != http.MethodPost || req.URL.String() != "https://oauth2.googleapis.com/token" {
				t.Fatalf("unexpected refresh request: %s %s", req.Method, req.URL.String())
			}
			body, err := io.ReadAll(req.Body)
			if err != nil {
				t.Fatalf("ReadAll(request body) error = %v", err)
			}
			if err := req.Body.Close(); err != nil {
				t.Fatalf("request body close error = %v", err)
			}
			payload := string(body)
			for _, needle := range []string{
				"grant_type=refresh_token",
				"refresh_token=refresh-token",
				"client_id=client-id",
				"client_secret=client-secret",
			} {
				if !strings.Contains(payload, needle) {
					t.Fatalf("refresh payload %q missing %q", payload, needle)
				}
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"access_token":"refreshed-token"}`)),
			}, nil
		}),
	}

	fileTokenStoreFetchProjectIDFn = func(ctx context.Context, accessToken string, client *http.Client) (string, error) {
		if ctx == nil {
			t.Fatal("fetch project id ctx is nil")
		}
		if accessToken != "refreshed-token" {
			t.Fatalf("access token = %q, want %q", accessToken, "refreshed-token")
		}
		if client != fileTokenStoreHTTPClient {
			t.Fatal("fetch project id client mismatch")
		}
		return "proj-123", nil
	}

	store := NewFileTokenStore()
	auth, err := store.readAuthFile(authPath, tempDir)
	if err != nil {
		t.Fatalf("readAuthFile() error = %v", err)
	}
	if auth == nil {
		t.Fatal("readAuthFile() returned nil auth")
	}
	if got := auth.Metadata["project_id"]; got != "proj-123" {
		t.Fatalf("project_id = %v, want %q", got, "proj-123")
	}

	tokenMap, ok := auth.Metadata["token"].(map[string]any)
	if !ok {
		t.Fatalf("token metadata type = %T, want map[string]any", auth.Metadata["token"])
	}
	if got := tokenMap["access_token"]; got != "refreshed-token" {
		t.Fatalf("token.access_token = %v, want %q", got, "refreshed-token")
	}

	raw, err := os.ReadFile(authPath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	var persisted map[string]any
	if err := json.Unmarshal(raw, &persisted); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if got := persisted["project_id"]; got != "proj-123" {
		t.Fatalf("persisted project_id = %v, want %q", got, "proj-123")
	}
	persistedToken, ok := persisted["token"].(map[string]any)
	if !ok {
		t.Fatalf("persisted token type = %T, want map[string]any", persisted["token"])
	}
	if got := persistedToken["access_token"]; got != "refreshed-token" {
		t.Fatalf("persisted token.access_token = %v, want %q", got, "refreshed-token")
	}
}
