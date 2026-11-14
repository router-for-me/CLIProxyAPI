package management

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestDeleteAuthFileAllRemovesPluginVirtualAuths(t *testing.T) {
	for _, version := range []string{"v0", "v8"} {
		t.Run(version, func(t *testing.T) {
			authDir := t.TempDir()
			path := filepath.Join(authDir, "multi.json")
			if err := os.WriteFile(path, []byte(`{"type":"plugin-provider"}`), 0o600); err != nil {
				t.Fatal(err)
			}

			manager := coreauth.NewManager(nil, nil, nil)
			for _, id := range []string{"plugin-a", "plugin-b"} {
				auth := &coreauth.Auth{
					ID:       id,
					FileName: "multi.json",
					Provider: "plugin-provider",
					Attributes: map[string]string{
						coreauth.AttributePath:          path,
						coreauth.AttributeVirtualSource: path,
						coreauth.AttributeSource:        path,
						coreauth.AttributeSourceBackend: coreauth.AuthSourceFile,
						coreauth.AttributePluginVirtual: "true",
					},
				}
				if _, err := manager.Register(context.Background(), auth); err != nil {
					t.Fatal(err)
				}
			}

			h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager)
			h.tokenStore = &memoryAuthStore{}
			rec := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(rec)
			ctx.Request = httptest.NewRequest(http.MethodDelete, "/"+version+"/management/auth-files?all=true", nil)
			if version == "v8" {
				h.DeleteAuthFileV8(ctx)
			} else {
				h.DeleteAuthFile(ctx)
			}

			if rec.Code != http.StatusOK {
				t.Fatalf("delete-all status = %d, body=%s", rec.Code, rec.Body.String())
			}
			for _, id := range []string{"plugin-a", "plugin-b"} {
				_, exists := manager.GetByID(id)
				if exists != (version == "v0") {
					t.Fatalf("virtual auth %q exists=%v after %s delete-all", id, exists, version)
				}
			}
		})
	}
}
