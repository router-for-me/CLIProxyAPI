package copilot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// roundTripFunc lets us inject a custom transport for testing.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// newTestClient returns an *http.Client whose requests are redirected to the given test server,
// regardless of the original URL host.
func newTestClient(srv *httptest.Server) *http.Client {
	return &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			req2 := req.Clone(req.Context())
			req2.URL.Scheme = "http"
			req2.URL.Host = strings.TrimPrefix(srv.URL, "http://")
			return srv.Client().Transport.RoundTrip(req2)
		}),
	}
}

// TestFetchUserInfo_FullProfile verifies that FetchUserInfo returns login, email, and name.
func TestFetchUserInfo_FullProfile(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"login": "octocat",
			"email": "octocat@github.com",
			"name":  "The Octocat",
		})
	}))
	defer srv.Close()

	client := &DeviceFlowClient{httpClient: newTestClient(srv)}
	info, err := client.FetchUserInfo(context.Background(), "test-token")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info.Login != "octocat" {
		t.Errorf("Login: got %q, want %q", info.Login, "octocat")
	}
	if info.Email != "octocat@github.com" {
		t.Errorf("Email: got %q, want %q", info.Email, "octocat@github.com")
	}
	if info.Name != "The Octocat" {
		t.Errorf("Name: got %q, want %q", info.Name, "The Octocat")
	}
}

// TestFetchUserInfo_EmptyEmail verifies graceful handling when email is absent (private account).
func TestFetchUserInfo_EmptyEmail(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// GitHub returns null for private emails.
		_, _ = w.Write([]byte(`{"login":"privateuser","email":null,"name":"Private User"}`))
	}))
	defer srv.Close()

	client := &DeviceFlowClient{httpClient: newTestClient(srv)}
	info, err := client.FetchUserInfo(context.Background(), "test-token")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info.Login != "privateuser" {
		t.Errorf("Login: got %q, want %q", info.Login, "privateuser")
	}
	if info.Email != "" {
		t.Errorf("Email: got %q, want empty string", info.Email)
	}
	if info.Name != "Private User" {
		t.Errorf("Name: got %q, want %q", info.Name, "Private User")
	}
}

// TestFetchUserInfo_EmptyToken verifies error is returned for empty access token.
func TestFetchUserInfo_EmptyToken(t *testing.T) {
	client := &DeviceFlowClient{httpClient: http.DefaultClient}
	_, err := client.FetchUserInfo(context.Background(), "")
	if err == nil {
		t.Fatal("expected error for empty token, got nil")
	}
}

// TestFetchUserInfo_EmptyLogin verifies error is returned when API returns no login.
func TestFetchUserInfo_EmptyLogin(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"email":"someone@example.com","name":"No Login"}`))
	}))
	defer srv.Close()

	client := &DeviceFlowClient{httpClient: newTestClient(srv)}
	_, err := client.FetchUserInfo(context.Background(), "test-token")
	if err == nil {
		t.Fatal("expected error for empty login, got nil")
	}
}

// TestFetchUserInfo_HTTPError verifies error is returned on non-2xx response.
func TestFetchUserInfo_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"Bad credentials"}`))
	}))
	defer srv.Close()

	client := &DeviceFlowClient{httpClient: newTestClient(srv)}
	_, err := client.FetchUserInfo(context.Background(), "bad-token")
	if err == nil {
		t.Fatal("expected error for 401 response, got nil")
	}
}

// TestCopilotTokenStorage_EmailNameFields verifies Email and Name serialise correctly.
func TestCopilotTokenStorage_EmailNameFields(t *testing.T) {
	ts := &CopilotTokenStorage{
		AccessToken: "ghu_abc",
		TokenType:   "bearer",
		Scope:       "read:user user:email",
		Username:    "octocat",
		Email:       "octocat@github.com",
		Name:        "The Octocat",
		Type:        "github-copilot",
	}

	data, err := json.Marshal(ts)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}

	var out map[string]any
	if err = json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	for _, key := range []string{"access_token", "username", "email", "name", "type"} {
		if _, ok := out[key]; !ok {
			t.Errorf("expected key %q in JSON output, not found", key)
		}
	}
	if out["email"] != "octocat@github.com" {
		t.Errorf("email: got %v, want %q", out["email"], "octocat@github.com")
	}
	if out["name"] != "The Octocat" {
		t.Errorf("name: got %v, want %q", out["name"], "The Octocat")
	}
}

// TestCopilotTokenStorage_OmitEmptyEmailName verifies email/name are omitted when empty (omitempty).
func TestCopilotTokenStorage_OmitEmptyEmailName(t *testing.T) {
	ts := &CopilotTokenStorage{
		AccessToken: "ghu_abc",
		Username:    "octocat",
		Type:        "github-copilot",
	}

	data, err := json.Marshal(ts)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}

	var out map[string]any
	if err = json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	if _, ok := out["email"]; ok {
		t.Error("email key should be omitted when empty (omitempty), but was present")
	}
	if _, ok := out["name"]; ok {
		t.Error("name key should be omitted when empty (omitempty), but was present")
	}
}

// TestCopilotAuthBundle_EmailNameFields verifies bundle carries email and name through the pipeline.
func TestCopilotAuthBundle_EmailNameFields(t *testing.T) {
	bundle := &CopilotAuthBundle{
		TokenData: &CopilotTokenData{AccessToken: "ghu_abc"},
		Username:  "octocat",
		Email:     "octocat@github.com",
		Name:      "The Octocat",
	}
	if bundle.Email != "octocat@github.com" {
		t.Errorf("bundle.Email: got %q, want %q", bundle.Email, "octocat@github.com")
	}
	if bundle.Name != "The Octocat" {
		t.Errorf("bundle.Name: got %q, want %q", bundle.Name, "The Octocat")
	}
}

// TestGitHubUserInfo_Struct verifies the exported GitHubUserInfo struct fields are accessible.
func TestGitHubUserInfo_Struct(t *testing.T) {
	info := GitHubUserInfo{
		Login: "octocat",
		Email: "octocat@github.com",
		Name:  "The Octocat",
	}
	if info.Login == "" || info.Email == "" || info.Name == "" {
		t.Error("GitHubUserInfo fields should not be empty")
	}
}
