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
