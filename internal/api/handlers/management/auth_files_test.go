package management

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v6/sdk/auth"
)

func TestAuthIDForPath_UsesSharedFileAuthNormalization(t *testing.T) {
	authDir := filepath.Join(`C:\`, "Auths")
	path := filepath.Join(authDir, "Nested", "Foo.json")
	h := &Handler{cfg: &config.Config{AuthDir: authDir}}

	if got, want := h.authIDForPath(path), sdkAuth.NormalizeFileAuthID(path, authDir); got != want {
		t.Fatalf("auth ID = %q, want %q", got, want)
	}
}

func TestDownloadAuthFile_RejectsPathTraversalSeparators(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, nil)
	tests := []string{
		"nested/file.json",
		`nested\\file.json`,
		`C:\\temp\\file.json`,
	}

	for _, name := range tests {
		t.Run(name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(rec)
			ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/auth-files/download?name="+url.QueryEscape(name), nil)

			h.DownloadAuthFile(ctx)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
			}
		})
	}
}
