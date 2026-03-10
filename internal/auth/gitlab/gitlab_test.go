package gitlab

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNormalizeBaseURL(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "default", in: "", want: DefaultBaseURL},
		{name: "plain host", in: "gitlab.example.com", want: "https://gitlab.example.com"},
		{name: "trim trailing slash", in: "https://gitlab.example.com/", want: "https://gitlab.example.com"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := NormalizeBaseURL(tc.in); got != tc.want {
				t.Fatalf("NormalizeBaseURL(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestFetchDirectAccess_ParsesModelDetails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("expected POST, got %s", r.Method)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer pat-123" {
			t.Fatalf("expected Authorization header, got %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"base_url":"https://gateway.gitlab.example.com/v1",
			"token":"duo-gateway-token",
			"expires_at":2000000000,
			"headers":{
				"X-Gitlab-Realm":"saas",
				"X-Gitlab-Host-Name":"gitlab.example.com"
			},
			"model_details":{
				"model_provider":"anthropic",
				"model_name":"claude-sonnet-4-5"
			}
		}`))
	}))
	defer server.Close()

	client := &AuthClient{httpClient: server.Client()}
	direct, err := client.FetchDirectAccess(context.Background(), server.URL, "pat-123")
	if err != nil {
		t.Fatalf("FetchDirectAccess returned error: %v", err)
	}
	if direct.BaseURL != "https://gateway.gitlab.example.com/v1" {
		t.Fatalf("unexpected base_url %q", direct.BaseURL)
	}
	if direct.Token != "duo-gateway-token" {
		t.Fatalf("unexpected token %q", direct.Token)
	}
	if direct.ModelDetails == nil || direct.ModelDetails.ModelName != "claude-sonnet-4-5" {
		t.Fatalf("unexpected model details: %+v", direct.ModelDetails)
	}
	if direct.Headers["X-Gitlab-Realm"] != "saas" {
		t.Fatalf("expected X-Gitlab-Realm header, got %+v", direct.Headers)
	}
}
