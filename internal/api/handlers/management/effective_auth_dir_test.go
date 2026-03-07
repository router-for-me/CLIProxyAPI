package management

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v6/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

type mirroredAuthDirStore struct {
	authDir string
}

func (s *mirroredAuthDirStore) List(context.Context) ([]*coreauth.Auth, error)       { return nil, nil }
func (s *mirroredAuthDirStore) Save(context.Context, *coreauth.Auth) (string, error) { return "", nil }
func (s *mirroredAuthDirStore) Delete(context.Context, string) error                 { return nil }
func (s *mirroredAuthDirStore) AuthDir() string                                      { return s.authDir }

func TestPostOAuthCallbackUsesEffectiveAuthDir(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	configuredAuthDir := t.TempDir()
	mirroredAuthDir := t.TempDir()
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: configuredAuthDir}, nil)
	h.tokenStore = &mirroredAuthDirStore{authDir: mirroredAuthDir}

	state := "codex-state-1"
	RegisterOAuthSession(state, "codex")

	body, err := json.Marshal(map[string]string{
		"provider": "codex",
		"state":    state,
		"code":     "test-code",
	})
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}

	resp := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(resp)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v0/management/oauth/callback", bytes.NewReader(body))
	ctx.Request.Header.Set("Content-Type", "application/json")

	h.PostOAuthCallback(ctx)
	if resp.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, resp.Code, resp.Body.String())
	}

	callbackPath := filepath.Join(mirroredAuthDir, ".oauth-codex-"+state+".oauth")
	if _, err := os.Stat(callbackPath); err != nil {
		t.Fatalf("expected callback file in mirrored auth dir: %v", err)
	}
	configuredPath := filepath.Join(configuredAuthDir, ".oauth-codex-"+state+".oauth")
	if _, err := os.Stat(configuredPath); !os.IsNotExist(err) {
		t.Fatalf("expected configured auth dir to remain untouched, stat err=%v", err)
	}
}

func TestAuthIDForPathUsesEffectiveAuthDir(t *testing.T) {
	configuredAuthDir := t.TempDir()
	mirroredAuthDir := t.TempDir()
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: configuredAuthDir}, nil)
	h.tokenStore = &mirroredAuthDirStore{authDir: mirroredAuthDir}

	path := filepath.Join(mirroredAuthDir, "nested", "demo.json")
	got := h.authIDForPath(path)
	want := sdkAuth.NormalizeFileAuthID(path, mirroredAuthDir)
	if got != want {
		t.Fatalf("authIDForPath = %q, want %q", got, want)
	}
}
