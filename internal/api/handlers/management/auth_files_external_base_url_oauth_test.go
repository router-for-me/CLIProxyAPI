package management

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/auth/antigravity"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

func TestWebUIExternalCallbackURL_UsesConfiguredBaseURL(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		wantURL  string
	}{
		{
			name:     "codex",
			provider: "codex",
			wantURL:  "https://example.com/base/codex/callback",
		},
		{
			name:     "claude",
			provider: "claude",
			wantURL:  "https://example.com/base/anthropic/callback",
		},
		{
			name:     "gemini",
			provider: "gemini",
			wantURL:  "https://example.com/base/google/callback",
		},
		{
			name:     "antigravity",
			provider: "antigravity",
			wantURL:  "https://example.com/base/antigravity/callback",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := NewHandlerWithoutConfigFilePath(&config.Config{
				RemoteManagement: config.RemoteManagement{ExternalBaseURL: " https://example.com/base/?foo=bar#fragment "},
			}, nil)

			gotURL, ok, err := h.webUIExternalCallbackURL(tt.provider)
			if err != nil {
				t.Fatalf("webUIExternalCallbackURL(%q) error = %v", tt.provider, err)
			}
			if !ok {
				t.Fatalf("webUIExternalCallbackURL(%q) ok = false, want true", tt.provider)
			}
			if gotURL != tt.wantURL {
				t.Fatalf("webUIExternalCallbackURL(%q) = %q, want %q", tt.provider, gotURL, tt.wantURL)
			}
		})
	}
}

func TestWebUIExternalCallbackURL_InvalidBaseURL(t *testing.T) {
	h := NewHandlerWithoutConfigFilePath(&config.Config{
		RemoteManagement: config.RemoteManagement{ExternalBaseURL: "not-a-valid-url"},
	}, nil)

	gotURL, ok, err := h.webUIExternalCallbackURL("codex")
	if err == nil {
		t.Fatalf("webUIExternalCallbackURL returned nil error, want error")
	}
	if ok {
		t.Fatalf("webUIExternalCallbackURL ok = true, want false")
	}
	if gotURL != "" {
		t.Fatalf("webUIExternalCallbackURL url = %q, want empty", gotURL)
	}
}

func TestRequestOAuthToken_ExternalBaseURL_UsesPublicCallback(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name         string
		requestURL   string
		providerPath string
		callbackPort int
		call         func(h *Handler, c *gin.Context)
	}{
		{
			name:         "codex",
			requestURL:   "http://cpa.example.com/v0/management/codex-auth-url?is_webui=true",
			providerPath: "/codex/callback",
			callbackPort: codexCallbackPort,
			call:         (*Handler).RequestCodexToken,
		},
		{
			name:         "claude",
			requestURL:   "http://cpa.example.com/v0/management/anthropic-auth-url?is_webui=true",
			providerPath: "/anthropic/callback",
			callbackPort: anthropicCallbackPort,
			call:         (*Handler).RequestAnthropicToken,
		},
		{
			name:         "gemini",
			requestURL:   "http://cpa.example.com/v0/management/gemini-cli-auth-url?is_webui=true",
			providerPath: "/google/callback",
			callbackPort: geminiCallbackPort,
			call:         (*Handler).RequestGeminiCLIToken,
		},
		{
			name:         "antigravity",
			requestURL:   "http://cpa.example.com/v0/management/antigravity-auth-url?is_webui=true",
			providerPath: "/antigravity/callback",
			callbackPort: antigravity.CallbackPort,
			call:         (*Handler).RequestAntigravityToken,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := NewHandlerWithoutConfigFilePath(&config.Config{
				AuthDir: t.TempDir(),
				Port:    49794,
				RemoteManagement: config.RemoteManagement{
					ExternalBaseURL: "https://cpa.example.com/base/",
				},
			}, nil)
			h.tokenStore = &memoryAuthStore{}

			rec := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(rec)
			req := httptest.NewRequest(http.MethodGet, tt.requestURL, nil)
			req.Host = "cpa.example.com"
			ctx.Request = req

			tt.call(h, ctx)

			if rec.Code != http.StatusOK {
				t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
			}

			var resp struct {
				Status string `json:"status"`
				URL    string `json:"url"`
				State  string `json:"state"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatalf("failed to decode response: %v", err)
			}
			if resp.Status != "ok" {
				t.Fatalf("status = %q, want %q", resp.Status, "ok")
			}
			if resp.State == "" {
				t.Fatalf("expected non-empty oauth state")
			}

			parsed, err := url.Parse(resp.URL)
			if err != nil {
				t.Fatalf("failed to parse auth url: %v", err)
			}
			if got := parsed.Query().Get("redirect_uri"); got != "https://cpa.example.com/base"+tt.providerPath {
				t.Fatalf("redirect_uri = %q, want %q", got, "https://cpa.example.com/base"+tt.providerPath)
			}

			callbackForwardersMu.Lock()
			_, exists := callbackForwarders[tt.callbackPort]
			callbackForwardersMu.Unlock()
			if exists {
				t.Fatalf("expected no local callback forwarder for %s external webui request", tt.name)
			}

			CompleteOAuthSession(resp.State)
			time.Sleep(600 * time.Millisecond)
		})
	}
}
