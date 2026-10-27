package management

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/authpublication"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/watcher/synthesizer"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestManagementStatusPersistsPendingPublication(t *testing.T) {
	for _, provider := range []string{"codex", "claude"} {
		for _, existing := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/existing=%t", provider, existing), func(t *testing.T) {
				h, path, backend := newCommittedPublicationHandler(t, provider, existing)
				router := gin.New()
				router.PATCH("/credentials/status", h.PatchAuthFileStatus)
				for _, disabled := range []bool{true, false} {
					body := fmt.Sprintf(`{"name":"account.json","disabled":%t}`, disabled)
					req := httptest.NewRequest(http.MethodPatch, "/credentials/status", bytes.NewBufferString(body))
					req.Header.Set("Content-Type", "application/json")
					response := httptest.NewRecorder()
					router.ServeHTTP(response, req)
					if response.Code != http.StatusOK {
						t.Fatalf("status update: %d, %s", response.Code, response.Body)
					}
					var remote map[string]any
					if err := json.Unmarshal(backend(), &remote); err != nil || remote["disabled"] != disabled || remote["access_token"] != "committed" {
						t.Errorf("status did not persist the committed credential: %#v, %v", remote, err)
					}
					if authpublication.HasPending(path) {
						t.Error("status save skipped publication repair")
					}
					scanned, err := synthesizer.NewFileSynthesizer().Synthesize(&synthesizer.SynthesisContext{Config: h.cfg, AuthDir: h.cfg.AuthDir})
					if err != nil || len(scanned) != 1 {
						t.Fatalf("status rescan: %#v, %v", scanned, err)
					}
					if _, errUpdate := h.authManager.Update(coreauth.WithSkipPersist(context.Background()), scanned[0]); errUpdate != nil {
						t.Fatal(errUpdate)
					}
					active, ok := h.authManager.GetByID("account.json")
					if !ok || active.Disabled != disabled || active.Metadata["access_token"] != "committed" {
						t.Fatalf("rescan reverted status: %#v", active)
					}
				}
			})
		}
	}
}
