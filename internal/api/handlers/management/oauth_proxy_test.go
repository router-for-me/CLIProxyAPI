package management

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

func newOAuthProxyTestContext(rawURL string) *gin.Context {
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, rawURL, nil)
	return ctx
}

func TestOAuthProxyURLFromRequest(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	h := &Handler{}
	tests := []struct {
		name    string
		rawURL  string
		want    string
		wantErr bool
	}{
		{name: "empty", rawURL: "/v0/management/codex-auth-url", want: ""},
		{name: "snake query", rawURL: "/v0/management/codex-auth-url?proxy_url=direct", want: "direct"},
		{name: "dash query", rawURL: "/v0/management/codex-auth-url?proxy-url=none", want: "none"},
		{name: "http proxy", rawURL: "/v0/management/codex-auth-url?proxy_url=http%3A%2F%2Fproxy.example.com%3A8080", want: "http://proxy.example.com:8080"},
		{name: "socks5 proxy", rawURL: "/v0/management/codex-auth-url?proxy_url=socks5%3A%2F%2F127.0.0.1%3A1080", want: "socks5://127.0.0.1:1080"},
		{name: "invalid", rawURL: "/v0/management/codex-auth-url?proxy_url=bad-value", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := h.oauthProxyURLFromRequest(newOAuthProxyTestContext(tt.rawURL))
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("proxy_url = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestConfigWithOAuthProxyDoesNotMutateGlobalConfig(t *testing.T) {
	cfg := &config.Config{SDKConfig: config.SDKConfig{ProxyURL: "http://global.example.com:8080"}, AuthDir: "/auth"}
	h := &Handler{cfg: cfg}

	override := h.configWithOAuthProxy("direct")
	if override == cfg {
		t.Fatal("expected copied config when proxy override is provided")
	}
	if override.ProxyURL != "direct" {
		t.Fatalf("override proxy-url = %q, want direct", override.ProxyURL)
	}
	if override.AuthDir != cfg.AuthDir {
		t.Fatalf("auth-dir = %q, want %q", override.AuthDir, cfg.AuthDir)
	}
	if cfg.ProxyURL != "http://global.example.com:8080" {
		t.Fatalf("global proxy-url mutated to %q", cfg.ProxyURL)
	}

	inherited := h.configWithOAuthProxy("")
	if inherited != cfg {
		t.Fatal("expected original config when no proxy override is provided")
	}
}

func TestApplyOAuthProxyToRecord(t *testing.T) {
	record := &coreauth.Auth{
		Metadata: map[string]any{"email": "user@example.com"},
	}

	applyOAuthProxyToRecord(record, "direct")

	if record.ProxyURL != "direct" {
		t.Fatalf("record.ProxyURL = %q, want direct", record.ProxyURL)
	}
	if got, _ := record.Metadata["proxy_url"].(string); got != "direct" {
		t.Fatalf("metadata.proxy_url = %q, want direct", got)
	}
	if got, _ := record.Metadata["email"].(string); got != "user@example.com" {
		t.Fatalf("metadata.email = %q, want user@example.com", got)
	}
}

func TestApplyOAuthProxyToRecordEmptyIsNoop(t *testing.T) {
	record := &coreauth.Auth{}

	applyOAuthProxyToRecord(record, "")

	if record.ProxyURL != "" {
		t.Fatalf("record.ProxyURL = %q, want empty", record.ProxyURL)
	}
	if record.Metadata != nil {
		t.Fatalf("metadata = %#v, want nil", record.Metadata)
	}
}
