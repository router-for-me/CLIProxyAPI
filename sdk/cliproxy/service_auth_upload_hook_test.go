package cliproxy

import (
	"bytes"
	"context"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/api/handlers/management"
	internalregistry "github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

// An upload's post-persist hook carries the auth the upload just saved (versioned by
// the manager). If that hook is delivered after a newer upload of the same file has
// been saved, it must not bring back the older content.
func TestUploadPostPersistHookDeliveredAfterNewerUploadDoesNotRevert(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)
	const fileName = "codex-upload-hook@example.com.json"
	reg := internalregistry.GetGlobalRegistry()
	reg.UnregisterClient(fileName)
	t.Cleanup(func() { reg.UnregisterClient(fileName) })

	authDir := t.TempDir()
	cfg := &config.Config{AuthDir: authDir}
	manager := coreauth.NewManager(snapshotTestStore{}, nil, nil)
	manager.RegisterExecutor(&syncTestExecutor{})
	svc := &Service{cfg: cfg, coreManager: manager}
	h := management.NewHandlerWithoutConfigFilePath(cfg, manager)

	// Hold each upload's hook instead of running it, to deliver them late.
	var held []*coreauth.Auth
	h.SetPostAuthPersistHook(func(_ context.Context, auth *coreauth.Auth) error {
		held = append(held, auth.Clone())
		return nil
	})
	upload := func(prefix string) {
		t.Helper()
		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		part, err := writer.CreateFormFile("file", fileName)
		if err != nil {
			t.Fatalf("create multipart file: %v", err)
		}
		content := fmt.Sprintf(`{"type":"codex","email":"upload-hook@example.com","access_token":"token","prefix":%q}`, prefix)
		if _, err = part.Write([]byte(content)); err != nil {
			t.Fatalf("write multipart content: %v", err)
		}
		if err = writer.Close(); err != nil {
			t.Fatalf("close multipart writer: %v", err)
		}
		rec := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(rec)
		req := httptest.NewRequest(http.MethodPost, "/v0/management/auth-files", &body)
		req.Header.Set("Content-Type", writer.FormDataContentType())
		ctx.Request = req
		h.UploadAuthFile(ctx)
		if rec.Code != http.StatusOK {
			t.Fatalf("upload %s: status %d, body %s", prefix, rec.Code, rec.Body.String())
		}
	}

	upload("pfx-old")
	upload("pfx-new")
	if len(held) != 2 {
		t.Fatalf("held %d hooks, want 2", len(held))
	}
	if held[0].Generation >= held[1].Generation {
		t.Fatalf("hook generations = %d, %d, want the older upload versioned below the newer", held[0].Generation, held[1].Generation)
	}

	hook := svc.runtimeAuthSyncHook()
	// The older upload's hook arrives after the newer upload was saved.
	if err := hook(context.Background(), held[0]); err != nil {
		t.Fatalf("older hook: %v", err)
	}
	if err := hook(context.Background(), held[1]); err != nil {
		t.Fatalf("newer hook: %v", err)
	}

	current, ok := manager.GetByID(fileName)
	if !ok || current == nil {
		t.Fatal("uploaded auth missing")
	}
	if current.Prefix != "pfx-new" {
		t.Fatalf("core auth prefix = %q, want pfx-new (the older upload's late hook reverted it)", current.Prefix)
	}
}
