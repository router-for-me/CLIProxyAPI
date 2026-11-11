package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/api/handlers/management"
)

func TestCodexCallbackRejectsDeviceState(t *testing.T) {
	s := newTestServer(t)
	state := "device-root-callback-state"
	management.RegisterDeviceOAuthSession(state, "codex")
	defer management.CancelOAuthSession(state)
	callback := httptest.NewRecorder()
	s.engine.ServeHTTP(callback, httptest.NewRequest(http.MethodGet, "/codex/callback?state="+state+"&code=browser-code", nil))
	if callback.Code != http.StatusConflict || !strings.Contains(callback.Body.String(), "device login does not accept callbacks") {
		t.Fatalf("callback status=%d, body=%s", callback.Code, callback.Body.String())
	}
	entries, errRead := os.ReadDir(s.cfg.AuthDir)
	if errRead != nil {
		t.Fatal(errRead)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".oauth") {
			t.Fatalf("rejected callback created OAuth file %q", entry.Name())
		}
	}
	if !management.IsOAuthSessionPending(state, "codex") {
		t.Fatal("rejected callback consumed device session")
	}
}
