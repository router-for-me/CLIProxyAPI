package tui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNewClientWithBaseURL(t *testing.T) {
	tests := []struct {
		name        string
		inputURL    string
		expectedURL string
	}{
		{
			name:        "standard http url",
			inputURL:    "http://192.168.1.100:8317",
			expectedURL: "http://192.168.1.100:8317",
		},
		{
			name:        "https url with trailing slash",
			inputURL:    "https://proxy.example.com/",
			expectedURL: "https://proxy.example.com",
		},
		{
			name:        "uppercase https url with trailing slash",
			inputURL:    "HTTPS://proxy.example.com/",
			expectedURL: "HTTPS://proxy.example.com",
		},
		{
			name:        "url without scheme",
			inputURL:    "proxy.example.com:9000",
			expectedURL: "http://proxy.example.com:9000",
		},
		{
			name:        "url with subpath and trailing slash",
			inputURL:    "https://proxy.example.com/prefix/",
			expectedURL: "https://proxy.example.com/prefix",
		},
		{
			name:        "empty url falls back to default localhost",
			inputURL:    "",
			expectedURL: "http://127.0.0.1:8317",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			client := NewClientWithBaseURL(tc.inputURL, "secret")
			if client.baseURL != tc.expectedURL {
				t.Fatalf("expected baseURL %q, got %q", tc.expectedURL, client.baseURL)
			}
		})
	}
}

func TestNewClient_BackwardsCompatibility(t *testing.T) {
	client := NewClient(8317, "test-secret")
	expected := "http://127.0.0.1:8317"
	if client.baseURL != expected {
		t.Fatalf("expected baseURL %q, got %q", expected, client.baseURL)
	}
}

func TestClient_RemoteServerInteraction(t *testing.T) {
	var requestedPath string
	var authHeader string

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestedPath = r.URL.Path
		authHeader = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
	}))
	defer ts.Close()

	client := NewClientWithBaseURL(ts.URL, "remote-secret-key")
	cfg, err := client.GetConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg["status"] != "ok" {
		t.Fatalf("expected status ok, got %v", cfg["status"])
	}
	if requestedPath != "/v0/management/config" {
		t.Fatalf("expected path /v0/management/config, got %q", requestedPath)
	}
	if authHeader != "Bearer remote-secret-key" {
		t.Fatalf("expected Authorization Bearer remote-secret-key, got %q", authHeader)
	}
}

func TestNewAppWithBaseURL(t *testing.T) {
	app := NewAppWithBaseURL("https://proxy.example.com", "secret", nil)
	if app.client.baseURL != "https://proxy.example.com" {
		t.Fatalf("expected app client baseURL https://proxy.example.com, got %q", app.client.baseURL)
	}
}

func TestClient_GetAuthFilesUsesV8CredentialsAndRendersRecoveryState(t *testing.T) {
	var requestedPath string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestedPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"files": []map[string]any{
				{
					"name":                    "codex-a.json",
					"provider":                "codex",
					"email":                   "a@example.com",
					"status":                  "active",
					"disabled":                false,
					"unavailable":             false,
					"requires_sign_in":        true,
					"renewal_state":           "sign_in_required",
					"recovery_stage":          "sign_in_required",
					"access_token_expires_at": "2026-10-31T00:00:00Z",
					"next_action":             "sign_in",
					"recovery_hint":           "Re-authenticate this account on this proxy instance via OAuth login.",
				},
			},
		})
	}))
	defer ts.Close()

	client := NewClientWithBaseURL(ts.URL, "secret")
	files, err := client.GetAuthFiles()
	if err != nil {
		t.Fatalf("GetAuthFiles error: %v", err)
	}
	if requestedPath != "/v8/management/credentials" {
		t.Fatalf("requestedPath = %q, want /v8/management/credentials", requestedPath)
	}
	if len(files) != 1 {
		t.Fatalf("len(files) = %d, want 1", len(files))
	}

	tab := newAuthTabModel(client)
	tab.SetSize(120, 40)
	tab.files = files
	tab.expanded = 0
	view := tab.renderContent()
	for _, expected := range []string{
		"sign-in required",
		"sign_in_required",
		"2026-10-31T00:00:00Z",
		"sign_in",
		"Re-authenticate this account on this proxy instance via OAuth login.",
	} {
		if !containsString(view, expected) {
			t.Fatalf("expected TUI auth tab view to contain %q, got:\n%s", expected, view)
		}
	}
}

func containsString(haystack, needle string) bool {
	return len(needle) == 0 || (len(haystack) >= len(needle) && (haystack == needle || indexString(haystack, needle) >= 0))
}

func indexString(s, substr string) int {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}
