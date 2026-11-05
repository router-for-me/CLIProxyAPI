package cliproxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	management "github.com/router-for-me/CLIProxyAPI/v8/internal/api/handlers/management"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	fileauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

type credentialPriorityUpdateHook struct {
	coreauth.NoopHook
	afterUpdate func(context.Context, *coreauth.Auth)
}

func (h *credentialPriorityUpdateHook) OnAuthUpdated(ctx context.Context, auth *coreauth.Auth) {
	h.afterUpdate(ctx, auth)
}

func TestCredentialPriorityCASConcurrentDeleteDoesNotRevive(t *testing.T) {
	dir := t.TempDir()
	const name = "claude-priority-delete.json"
	file := filepath.Join(dir, name)
	store := fileauth.NewFileTokenStore()
	store.SetBaseDir(dir)
	hook := &credentialPriorityUpdateHook{}
	manager := coreauth.NewManager(store, nil, hook)
	_, err := manager.Register(context.Background(), &coreauth.Auth{
		ID: name, FileName: name, Provider: "claude", Status: coreauth.StatusActive,
		Attributes: map[string]string{"path": file}, Metadata: map[string]any{"type": "claude"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer GlobalModelRegistry().UnregisterClient(name)
	cfg := &config.Config{AuthDir: dir}
	service := &Service{cfg: cfg, coreManager: manager}
	handler := management.NewHandlerWithoutConfigFilePath(cfg, manager)
	handler.SetPostAuthPersistHook(service.runtimeAuthSyncHook())
	router := gin.New()
	router.PATCH("/v8/management/credentials/priority", handler.PatchCredentialPriority)
	router.DELETE("/v8/management/credentials", handler.DeleteAuthFileV8)
	deleted := false
	hook.afterUpdate = func(_ context.Context, auth *coreauth.Auth) {
		// Modify has committed and released its mutation gate. Complete DELETE
		// deterministically before the handler can submit its old snapshot for sync.
		if auth.ID != name || deleted {
			return
		}
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodDelete, "/v8/management/credentials?name="+name, nil))
		if recorder.Code != http.StatusOK {
			t.Fatalf("DELETE = %d %s", recorder.Code, recorder.Body.String())
		}
		if _, exists := manager.GetByID(name); exists {
			t.Fatal("DELETE did not remove the runtime credential")
		}
		deleted = true
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPatch, "/v8/management/credentials/priority", strings.NewReader(`{"name":"`+name+`","priority":4,"expected_priority":null}`))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || !deleted {
		t.Fatalf("PATCH = %d, concurrent DELETE completed=%v", recorder.Code, deleted)
	}
	if _, exists := manager.GetByID(name); exists {
		t.Fatal("priority post-persist sync revived the deleted credential")
	}
	if _, err = os.Stat(file); !os.IsNotExist(err) {
		t.Fatal("priority mutation recreated the deleted credential file")
	}
}
