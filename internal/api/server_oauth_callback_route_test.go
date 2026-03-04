package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/api/handlers/management"
)

func TestOAuthUnifiedCallbackRoute_WritesPendingSessionFile(t *testing.T) {
	server := newTestServer(t)
	state := "state-unified-callback-test"
	management.RegisterOAuthSession(state, "codex")
	defer management.CompleteOAuthSession(state)

	req := httptest.NewRequest(http.MethodGet, "/oauth/callback/codex?code=abc&state="+state, nil)
	rr := httptest.NewRecorder()
	server.engine.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("unexpected status code: %d body=%s", rr.Code, rr.Body.String())
	}

	callbackFile := filepath.Join(server.cfg.AuthDir, ".oauth-codex-"+state+".oauth")
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(callbackFile); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("expected callback file to be created: %s", callbackFile)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
