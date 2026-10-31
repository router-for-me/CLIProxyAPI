package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"testing/synctest"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/watcher/synthesizer"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestStartOAuthV8CodexDeviceRecoveryPreservesNestedTarget(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, omitPath := range []bool{false, true} {
		name := "watcher path"
		if omitPath {
			name = "auth ID fallback"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				replaceOAuthSessionStoreForTest(t, newOAuthSessionStore(time.Minute))
				cfg := &config.Config{AuthDir: t.TempDir()}
				targetID := "team/target.json"
				targetPath := filepath.Join(cfg.AuthDir, filepath.FromSlash(targetID))
				if errMkdir := os.MkdirAll(filepath.Dir(targetPath), 0o700); errMkdir != nil {
					t.Fatal(errMkdir)
				}
				if errWrite := os.WriteFile(targetPath, []byte(`{"type":"codex","email":"a@example.com","account_id":"acc-a","access_token":"old-access"}`), 0o600); errWrite != nil {
					t.Fatal(errWrite)
				}
				synthesis := &synthesizer.SynthesisContext{Config: cfg, AuthDir: cfg.AuthDir, Now: time.Now(), IDGenerator: synthesizer.NewStableIDGenerator()}
				loaded, errLoad := synthesizer.NewFileSynthesizer().Synthesize(synthesis)
				if errLoad != nil || len(loaded) != 1 {
					t.Fatalf("watcher fixture = %v, %v", loaded, errLoad)
				}
				if loaded[0].ID != targetID || loaded[0].FileName != "target.json" {
					t.Fatalf("watcher target = %#v", loaded[0])
				}
				loaded[0].RequiresSignIn = true
				if omitPath {
					delete(loaded[0].Attributes, coreauth.AttributePath)
				}
				manager := coreauth.NewManager(nil, nil, nil)
				manager.SetConfig(cfg)
				registerAuthForLookupTest(t, manager, loaded[0])
				h := &Handler{cfg: cfg, authManager: manager, tokenStore: sdkAuth.NewFileTokenStore()}
				workerContext := make(chan context.Context, 1)
				h.codexDeviceAuthenticator = codexDeviceAuthenticatorFunc(func(ctx context.Context, _ *config.Config, opts *sdkAuth.LoginOptions) (*coreauth.Auth, error) {
					workerContext <- ctx
					opts.OnDeviceCode(sdkAuth.DeviceCodeInfo{UserCode: "ABCD-EFGH", VerificationURL: "https://auth.openai.com/codex/device", ExpiresIn: 900})
					return &coreauth.Auth{ID: "generated.json", FileName: "generated.json", Provider: "codex", Metadata: map[string]any{"type": "codex", "email": "a@example.com", "account_id": "acc-a", "access_token": "fresh-access", "refresh_token": "fresh-refresh"}}, nil
				})
				router := gin.New()
				router.GET("/start", h.StartOAuthV8)
				router.GET("/status", h.GetAuthStatus)
				start := httptest.NewRecorder()
				router.ServeHTTP(start, httptest.NewRequest(http.MethodGet, "/start?provider=codex&flow=device&name="+targetID, nil))
				if start.Code != http.StatusOK {
					t.Fatalf("start status = %d: %s", start.Code, start.Body.String())
				}
				ctx := <-workerContext
				var started struct {
					State        string `json:"state"`
					TargetAuthID string `json:"target_auth_id"`
				}
				if errDecode := json.Unmarshal(start.Body.Bytes(), &started); errDecode != nil {
					t.Fatal(errDecode)
				}
				if started.TargetAuthID != targetID {
					t.Fatalf("start target = %q", started.TargetAuthID)
				}
				<-ctx.Done()
				synctest.Wait()
				status := httptest.NewRecorder()
				router.ServeHTTP(status, httptest.NewRequest(http.MethodGet, "/status?state="+started.State, nil))
				var result map[string]any
				if errDecode := json.Unmarshal(status.Body.Bytes(), &result); errDecode != nil {
					t.Fatal(errDecode)
				}
				if result["status"] != "ok" || result["auth_id"] != targetID || result["saved"] != true || result["adopted"] != true {
					t.Fatalf("recovery outcome = %s", status.Body.String())
				}
				var saved map[string]any
				rawSaved, errRead := os.ReadFile(targetPath)
				if errRead != nil || json.Unmarshal(rawSaved, &saved) != nil || saved["access_token"] != "fresh-access" {
					t.Fatalf("nested target retained old credential: %s, %v", rawSaved, errRead)
				}
				if _, errStat := os.Stat(filepath.Join(cfg.AuthDir, "target.json")); !os.IsNotExist(errStat) {
					t.Fatalf("recovery created a root duplicate: %v", errStat)
				}
				rescanned, errRescan := synthesizer.NewFileSynthesizer().Synthesize(synthesis)
				if errRescan != nil || len(rescanned) != 1 {
					t.Fatalf("watcher rescan = %v, %v", rescanned, errRescan)
				}
				if _, errUpdate := manager.Update(coreauth.WithSkipPersist(context.Background()), rescanned[0]); errUpdate != nil {
					t.Fatal(errUpdate)
				}
				current, okCurrent := manager.GetByID(targetID)
				if !okCurrent || current.Metadata["access_token"] != "fresh-access" || len(manager.List()) != 1 {
					t.Fatal("watcher rescan reverted recovery or added a duplicate")
				}
			})
		})
	}
}
