package management

import (
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

func performOAuthURLRequest(t *testing.T, h *Handler, path string, call func(*gin.Context)) (string, string) {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", path, nil)

	call(c)

	var resp struct {
		URL   string `json:"url"`
		State string `json:"state"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp.URL == "" {
		t.Fatalf("expected auth URL in response, got empty")
	}
	if resp.State != "" {
		CompleteOAuthSession(resp.State)
	}
	return resp.URL, resp.State
}

func queryValue(t *testing.T, rawURL, key string) string {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("failed to parse url %q: %v", rawURL, err)
	}
	return u.Query().Get(key)
}

func newOAuthCallbackEnabledHandler(t *testing.T) *Handler {
	t.Helper()
	cfg := &config.Config{
		AuthDir: t.TempDir(),
		OAuthCallback: config.OAuthCallbackConfig{
			Enabled:         true,
			ExternalBaseURL: "https://cliproxy.example.com",
			ProviderPaths: map[string]string{
				"anthropic":   "/oauth/callback/anthropic",
				"codex":       "/oauth/callback/codex",
				"gemini":      "/oauth/callback/gemini",
				"iflow":       "/oauth/callback/iflow",
				"antigravity": "/oauth/callback/antigravity",
			},
		},
	}
	return &Handler{cfg: cfg}
}

func TestRequestAnthropicTokenRedirectURI(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := newOAuthCallbackEnabledHandler(t)
	authURL, _ := performOAuthURLRequest(t, h, "/v0/management/anthropic-auth-url", h.RequestAnthropicToken)
	if got := queryValue(t, authURL, "redirect_uri"); got != "https://cliproxy.example.com/oauth/callback/anthropic" {
		t.Fatalf("unexpected redirect_uri: %s", got)
	}
}

func TestRequestCodexTokenRedirectURI(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := newOAuthCallbackEnabledHandler(t)
	authURL, _ := performOAuthURLRequest(t, h, "/v0/management/codex-auth-url", h.RequestCodexToken)
	if got := queryValue(t, authURL, "redirect_uri"); got != "https://cliproxy.example.com/oauth/callback/codex" {
		t.Fatalf("unexpected redirect_uri: %s", got)
	}
}

func TestRequestGeminiCLITokenRedirectURI(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := newOAuthCallbackEnabledHandler(t)
	authURL, _ := performOAuthURLRequest(t, h, "/v0/management/gemini-cli-auth-url", h.RequestGeminiCLIToken)
	if got := queryValue(t, authURL, "redirect_uri"); got != "https://cliproxy.example.com/oauth/callback/gemini" {
		t.Fatalf("unexpected redirect_uri: %s", got)
	}
}

func TestRequestIFlowTokenRedirectURI(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := newOAuthCallbackEnabledHandler(t)
	authURL, _ := performOAuthURLRequest(t, h, "/v0/management/iflow-auth-url", h.RequestIFlowToken)
	if got := queryValue(t, authURL, "redirect"); got != "https://cliproxy.example.com/oauth/callback/iflow" {
		t.Fatalf("unexpected redirect: %s", got)
	}
}

func TestRequestAntigravityTokenRedirectURI(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := newOAuthCallbackEnabledHandler(t)
	authURL, _ := performOAuthURLRequest(t, h, "/v0/management/antigravity-auth-url", h.RequestAntigravityToken)
	if got := queryValue(t, authURL, "redirect_uri"); got != "https://cliproxy.example.com/oauth/callback/antigravity" {
		t.Fatalf("unexpected redirect_uri: %s", got)
	}
}
