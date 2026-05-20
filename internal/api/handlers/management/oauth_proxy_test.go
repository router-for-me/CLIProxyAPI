package management

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func init() {
	gin.SetMode(gin.TestMode)
}

func TestResolveLoginProxyURL_Empty(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("GET", "/foo", nil)

	got, ok := resolveLoginProxyURL(c)
	if !ok {
		t.Fatalf("expected ok=true for empty proxy_url")
	}
	if got != "" {
		t.Fatalf("expected empty result, got %q", got)
	}
}

func TestResolveLoginProxyURL_ValidSocks5(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("GET", "/foo?proxy_url=socks5%3A%2F%2Fhost%3A1080", nil)

	got, ok := resolveLoginProxyURL(c)
	if !ok {
		t.Fatalf("expected ok=true for valid socks5 url")
	}
	if got != "socks5://host:1080" {
		t.Fatalf("expected socks5://host:1080, got %q", got)
	}
}

func TestResolveLoginProxyURL_DirectKeyword(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("GET", "/foo?proxy_url=direct", nil)

	got, ok := resolveLoginProxyURL(c)
	if !ok {
		t.Fatalf("expected ok=true for direct keyword")
	}
	if got != "direct" {
		t.Fatalf("expected 'direct', got %q", got)
	}
}

func TestResolveLoginProxyURL_InvalidScheme(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest("GET", "/foo?proxy_url=ftp%3A%2F%2Fhost", nil)

	got, ok := resolveLoginProxyURL(c)
	if ok {
		t.Fatalf("expected ok=false for invalid scheme")
	}
	if got != "" {
		t.Fatalf("expected empty result on error, got %q", got)
	}
	if rec.Code != 400 {
		t.Fatalf("expected 400 response, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "invalid_proxy_url") {
		t.Fatalf("expected invalid_proxy_url in response, got %q", rec.Body.String())
	}
}

func TestValidateLoginProxyURL_BodyMode(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest("POST", "/foo", nil)

	// Empty body value -> ok, empty
	got, ok := validateLoginProxyURL(c, "")
	if !ok || got != "" {
		t.Fatalf("expected (\"\", true) for empty body, got (%q, %v)", got, ok)
	}

	// Whitespace-only -> ok, empty
	got, ok = validateLoginProxyURL(c, "   \t\n")
	if !ok || got != "" {
		t.Fatalf("expected (\"\", true) for whitespace, got (%q, %v)", got, ok)
	}

	// Valid -> trimmed
	got, ok = validateLoginProxyURL(c, "  http://h:8080 ")
	if !ok || got != "http://h:8080" {
		t.Fatalf("expected (\"http://h:8080\", true), got (%q, %v)", got, ok)
	}
}

func TestWithLoginProxy_NilSafe(t *testing.T) {
	if got := withLoginProxy(nil, "socks5://x:1"); got != nil {
		t.Fatalf("expected nil cfg pass-through, got %v", got)
	}

	cfg := &config.Config{}
	if got := withLoginProxy(cfg, ""); got != cfg {
		t.Fatalf("expected same cfg pointer for empty proxy, got %v", got)
	}
	if got := withLoginProxy(cfg, "   "); got != cfg {
		t.Fatalf("expected same cfg pointer for whitespace proxy, got %v", got)
	}
}

func TestWithLoginProxy_OverridesProxyURL(t *testing.T) {
	cfg := &config.Config{}
	cfg.SDKConfig.ProxyURL = "http://global:8080"

	got := withLoginProxy(cfg, "socks5://login:1080")
	if got == cfg {
		t.Fatalf("expected fresh config copy when proxy non-empty")
	}
	if got.ProxyURL != "socks5://login:1080" {
		t.Fatalf("expected ProxyURL=socks5://login:1080, got %q", got.ProxyURL)
	}
	if cfg.ProxyURL != "http://global:8080" {
		t.Fatalf("original cfg should not be mutated; got %q", cfg.ProxyURL)
	}
}
