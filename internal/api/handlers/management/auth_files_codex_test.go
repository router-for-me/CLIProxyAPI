package management

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

func TestCodexRedirectURLUsesRequestHostForWebUI(t *testing.T) {
	gin.SetMode(gin.TestMode)

	h := NewHandlerWithoutConfigFilePath(&config.Config{}, nil)
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, "http://172.16.0.180:8318/v0/management/codex-auth-url?is_webui=true", nil)

	got, err := h.codexRedirectURL(ctx)
	if err != nil {
		t.Fatalf("codexRedirectURL() error = %v", err)
	}

	want := "http://172.16.0.180:1455/auth/callback"
	if got != want {
		t.Fatalf("codexRedirectURL() = %q, want %q", got, want)
	}
}

func TestCodexRedirectURLUsesExplicitOverride(t *testing.T) {
	gin.SetMode(gin.TestMode)

	h := NewHandlerWithoutConfigFilePath(&config.Config{
		CodexOAuth: config.CodexOAuthConfig{
			RedirectURL: "https://oauth.example.com/custom/callback",
		},
	}, nil)
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, "http://172.16.0.180:8318/v0/management/codex-auth-url?is_webui=true", nil)

	got, err := h.codexRedirectURL(ctx)
	if err != nil {
		t.Fatalf("codexRedirectURL() error = %v", err)
	}

	want := "https://oauth.example.com/custom/callback"
	if got != want {
		t.Fatalf("codexRedirectURL() = %q, want %q", got, want)
	}
}

func TestCodexManagementCallbackURLUsesConfiguredPublicBaseURL(t *testing.T) {
	gin.SetMode(gin.TestMode)

	h := NewHandlerWithoutConfigFilePath(&config.Config{
		CodexOAuth: config.CodexOAuthConfig{
			PublicBaseURL: "https://proxy.example.com:9443",
		},
	}, nil)
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, "http://172.16.0.180:8318/v0/management/codex-auth-url?is_webui=true", nil)

	got, err := h.codexManagementCallbackURL(ctx, "/codex/callback")
	if err != nil {
		t.Fatalf("codexManagementCallbackURL() error = %v", err)
	}

	want := "https://proxy.example.com:9443/codex/callback"
	if got != want {
		t.Fatalf("codexManagementCallbackURL() = %q, want %q", got, want)
	}
}

func TestCodexCallbackBindHostDefaultsAndOverride(t *testing.T) {
	defaultHandler := NewHandlerWithoutConfigFilePath(&config.Config{}, nil)
	if got := defaultHandler.codexCallbackBindHost(); got != "127.0.0.1" {
		t.Fatalf("default bind host = %q, want 127.0.0.1", got)
	}

	overrideHandler := NewHandlerWithoutConfigFilePath(&config.Config{
		CodexOAuth: config.CodexOAuthConfig{
			BindHost: "0.0.0.0",
		},
	}, nil)
	if got := overrideHandler.codexCallbackBindHost(); got != "0.0.0.0" {
		t.Fatalf("override bind host = %q, want 0.0.0.0", got)
	}

	if got := overrideHandler.codexPublicCallbackPort(); got != codex.DefaultCallbackPort {
		t.Fatalf("default public callback port = %d, want %d", got, codex.DefaultCallbackPort)
	}
}
