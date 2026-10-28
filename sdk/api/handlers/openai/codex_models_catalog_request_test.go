package openai

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestIsCodexModelsCatalogRequest(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cases := []struct {
		name   string
		path   string
		ua     string
		want   bool
		wantVer string
	}{
		{
			name:    "client_version query",
			path:    "/v1/models?client_version=0.159.0",
			want:    true,
			wantVer: "0.159.0",
		},
		{
			name:    "empty client_version query",
			path:    "/v1/models?client_version=",
			want:    true,
			wantVer: "",
		},
		{
			name:    "codex_cli_rs user agent",
			path:    "/v1/models",
			ua:      "codex_cli_rs/0.159.0 (Mac OS 26.3.1; arm64) iTerm.app/3.6.9",
			want:    true,
			wantVer: "0.159.0",
		},
		{
			name:    "codex-tui user agent",
			path:    "/v1/models",
			ua:      "codex-tui/0.154.0",
			want:    true,
			wantVer: "0.154.0",
		},
		{
			name:    "plain request",
			path:    "/v1/models",
			ua:      "curl/8.0",
			want:    false,
			wantVer: "",
		},
		{
			name:    "claude-cli stays non-codex without client_version",
			path:    "/v1/models",
			ua:      "claude-cli/1.0",
			want:    false,
			wantVer: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			if tc.ua != "" {
				req.Header.Set("User-Agent", tc.ua)
			}
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = req

			got := IsCodexModelsCatalogRequest(c)
			if got != tc.want {
				t.Fatalf("IsCodexModelsCatalogRequest = %v, want %v", got, tc.want)
			}
			if !tc.want {
				return
			}
			if gotVer := CodexModelsCatalogClientVersion(c); gotVer != tc.wantVer {
				t.Fatalf("CodexModelsCatalogClientVersion = %q, want %q", gotVer, tc.wantVer)
			}
		})
	}
}

func TestCodexModelsCatalogClientVersionPrefersVersionHeader(t *testing.T) {
	gin.SetMode(gin.TestMode)
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("User-Agent", "codex_cli_rs/0.159.0")
	req.Header.Set("Version", "0.160.1")
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = req

	if !IsCodexModelsCatalogRequest(c) {
		t.Fatal("expected Codex catalog request")
	}
	if got := CodexModelsCatalogClientVersion(c); got != "0.160.1" {
		t.Fatalf("version = %q, want 0.160.1", got)
	}
}

func TestCodexModelsCatalogClientVersionPrefersQuery(t *testing.T) {
	gin.SetMode(gin.TestMode)
	req := httptest.NewRequest(http.MethodGet, "/v1/models?client_version=0.149.1", nil)
	req.Header.Set("User-Agent", "codex_cli_rs/0.159.0")
	req.Header.Set("Version", "0.160.1")
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = req

	if got := CodexModelsCatalogClientVersion(c); got != "0.149.1" {
		t.Fatalf("version = %q, want 0.149.1", got)
	}
}