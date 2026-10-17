package cliproxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	management "github.com/router-for-me/CLIProxyAPI/v8/internal/api/handlers/management"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/watcher/synthesizer"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestCodexExcludedModelsCredentialPriorityAfterPatchAndReload(t *testing.T) {
	const globalModel = "gpt-6-luna"
	const credentialModel = "gpt-6.1-sol"
	cfg := &config.Config{AuthDir: t.TempDir(), OAuthExcludedModels: map[string][]string{"codex": {globalModel}}}
	manager := coreauth.NewManager(nil, nil, nil)
	service := &Service{cfg: cfg, coreManager: manager}
	path := filepath.Join(cfg.AuthDir, "exclusion-priority.json")
	sctx := &synthesizer.SynthesisContext{Config: cfg, AuthDir: cfg.AuthDir, Now: time.Now(), IDGenerator: synthesizer.NewStableIDGenerator()}
	load := func(raw string) *coreauth.Auth {
		t.Helper()
		auths, err := synthesizer.SynthesizeAuthFile(sctx, path, []byte(raw))
		if err != nil || len(auths) != 1 {
			t.Fatalf("synthesis: %v, count=%d", err, len(auths))
		}
		return auths[0]
	}
	auth := load(`{"type":"codex"}`)
	defer registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatal(err)
	}
	hook := service.runtimeAuthSyncHook()
	sync := func(auth *coreauth.Auth) {
		t.Helper()
		if err := hook(context.Background(), auth); err != nil {
			t.Fatal(err)
		}
	}
	check := func(stage string, wantGlobal, wantCredential bool) {
		t.Helper()
		present := map[string]bool{}
		for _, model := range registry.GetGlobalRegistry().GetModelsForClient(auth.ID) {
			present[model.ID] = true
		}
		if present[globalModel] != wantGlobal || present[credentialModel] != wantCredential {
			t.Fatalf("%s: model availability=(%v,%v), want=(%v,%v)", stage, present[globalModel], present[credentialModel], wantGlobal, wantCredential)
		}
	}
	sync(auth)
	check("global fallback", false, true)
	h := management.NewHandlerWithoutConfigFilePath(cfg, manager)
	h.SetPostAuthPersistHook(hook)
	router := gin.New()
	router.PATCH("/v8/management/credentials/fields", h.PatchAuthFileFields)
	patch := func(rules string) {
		t.Helper()
		req := httptest.NewRequest(http.MethodPatch, "/v8/management/credentials/fields", strings.NewReader(`{"name":"exclusion-priority.json","excluded_models":`+rules+`}`))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("patch status=%d body=%s", rec.Code, rec.Body.String())
		}
	}
	patch(`["` + credentialModel + `"]`)
	check("immediate credential override", true, false)
	sync(load(`{"type":"codex","excluded_models":["` + credentialModel + `"]}`))
	check("reloaded credential override", true, false)
	patch(`[]`)
	check("immediate empty fallback", false, true)
	inherited := load(`{"type":"codex","excluded_models":[]}`)
	if value, ok := inherited.Attributes["excluded_models"]; ok {
		t.Fatalf("inherited credential stores excluded_models=%q, want global rules resolved at registration", value)
	}
	sync(inherited)
	check("reloaded empty fallback", false, true)

	// A later global config change applies to the same credential on the next registration.
	cfg.OAuthExcludedModels = map[string][]string{"codex": {credentialModel}}
	sync(inherited)
	check("updated global fallback", true, false)
}
