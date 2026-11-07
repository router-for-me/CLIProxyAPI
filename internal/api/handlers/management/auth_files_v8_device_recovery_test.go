package management

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"testing/synctest"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestStartOAuthV8CodexDeviceRecoveryMatchesTargetBeforeSaving(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name, email, accountID string
		wantSave               bool
		verify                 bool
		verifyFails            bool
	}{
		{name: "matching", email: "a@example.com", accountID: "acc-a", wantSave: true},
		{name: "matching with verification", email: "a@example.com", accountID: "acc-a", wantSave: true, verify: true},
		{name: "verification failure", email: "a@example.com", accountID: "acc-a", wantSave: true, verify: true, verifyFails: true},
		{name: "different account", email: "a@example.com", accountID: "acc-b"},
		{name: "different email", email: "b@example.com", accountID: "acc-a"},
		{name: "missing account", email: "a@example.com"},
		{name: "missing email", accountID: "acc-a"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				replaceOAuthSessionStoreForTest(t, newOAuthSessionStore(time.Minute))
				cfg := &config.Config{AuthDir: t.TempDir()}
				manager := coreauth.NewManager(nil, nil, nil)
				manager.SetConfig(cfg)
				executor := &failingRefreshManagementExecutor{provider: "codex", streamPayloads: [][]byte{[]byte("data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n")}}
				if tc.verifyFails {
					executor.streamErr = errors.New("verification unavailable")
				}
				manager.RegisterExecutor(executor)
				registerAuthForLookupTest(t, manager, &coreauth.Auth{
					ID: "target.json", FileName: "target.json", Provider: "codex", Status: coreauth.StatusActive,
					RequiresSignIn: true,
					Metadata:       map[string]any{"type": "codex", "email": "a@example.com", "account_id": "acc-a", "access_token": "old-access"},
				})
				h := &Handler{cfg: cfg, authManager: manager, tokenStore: sdkAuth.NewFileTokenStore()}
				workerContext := make(chan context.Context, 1)
				h.codexDeviceAuthenticator = codexDeviceAuthenticatorFunc(func(ctx context.Context, _ *config.Config, opts *sdkAuth.LoginOptions) (*coreauth.Auth, error) {
					workerContext <- ctx
					opts.OnDeviceCode(sdkAuth.DeviceCodeInfo{UserCode: "ABCD-EFGH", VerificationURL: "https://auth.openai.com/codex/device", ExpiresIn: 900})
					return &coreauth.Auth{
						ID: "generated.json", FileName: "generated.json", Provider: "codex",
						Metadata: map[string]any{"type": "codex", "email": tc.email, "account_id": tc.accountID, "access_token": "fresh-access", "refresh_token": "fresh-refresh"},
					}, nil
				})
				router := gin.New()
				router.GET("/start", h.StartOAuthV8)
				router.GET("/status", h.GetAuthStatus)
				start := httptest.NewRecorder()
				url := "/start?provider=codex&flow=device&name=target.json"
				if tc.verify {
					url += "&verify=true&verify_model=gpt-5"
				}
				router.ServeHTTP(start, httptest.NewRequest(http.MethodGet, url, nil))
				ctx := <-workerContext
				if start.Code != http.StatusOK {
					t.Fatalf("start status = %d: %s", start.Code, start.Body.String())
				}
				var response struct{ State string }
				var startBody map[string]any
				if errDecode := json.Unmarshal(start.Body.Bytes(), &startBody); errDecode != nil {
					t.Fatal(errDecode)
				}
				response.State, _ = startBody["state"].(string)
				if startBody["target_auth_id"] != "target.json" {
					t.Fatalf("target missing from start response: %s", start.Body.String())
				}
				<-ctx.Done()
				synctest.Wait()
				status := httptest.NewRecorder()
				router.ServeHTTP(status, httptest.NewRequest(http.MethodGet, "/status?state="+response.State, nil))
				var result map[string]any
				if errDecode := json.Unmarshal(status.Body.Bytes(), &result); errDecode != nil {
					t.Fatal(errDecode)
				}
				current, _ := manager.GetByID("target.json")
				if tc.wantSave {
					if result["status"] != "ok" || result["auth_id"] != "target.json" || result["saved"] != true || result["adopted"] != true {
						t.Fatalf("recovery outcome = %s", status.Body.String())
					}
					if tc.verify && !tc.verifyFails && (result["inference_verified"] != true || result["recovery_stage"] != coreauth.RecoveryStageInferenceVerified) {
						t.Fatalf("device recovery did not verify inference: %s", status.Body.String())
					}
					if tc.verifyFails && result["verification_error"] == nil {
						t.Fatalf("missing verification failure: %s", status.Body.String())
					}
					if current.Metadata["access_token"] != "fresh-access" || current.RequiresSignIn {
						t.Fatal("target did not adopt the fresh device credential")
					}
					if _, errStat := os.Stat(filepath.Join(cfg.AuthDir, "target.json")); errStat != nil {
						t.Fatal(errStat)
					}
				} else {
					if result["status"] != "error" || result["error"] != "Authenticated account does not match target credential" {
						t.Fatalf("mismatch outcome = %s", status.Body.String())
					}
					if current.Metadata["access_token"] != "old-access" || !current.RequiresSignIn {
						t.Fatal("mismatched login changed the target credential")
					}
					if _, errStat := os.Stat(filepath.Join(cfg.AuthDir, "target.json")); !os.IsNotExist(errStat) {
						t.Fatalf("mismatched credential was saved: %v", errStat)
					}
				}
				if _, ok := manager.GetByID("generated.json"); ok {
					t.Fatal("targeted recovery created a second account")
				}
			})
		})
	}
}

func TestNotifyPersistedTokenRecordAdoptsCommittedBytesWithoutHook(t *testing.T) {
	cfg := &config.Config{AuthDir: t.TempDir()}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.SetConfig(cfg)
	h := &Handler{cfg: cfg, authManager: manager}
	path := filepath.Join(cfg.AuthDir, "committed.json")
	if errWrite := os.WriteFile(path, []byte(`{"type":"codex","access_token":"stale-mirror"}`), 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	record := &coreauth.Auth{ID: "committed.json", FileName: "committed.json", Provider: "codex"}
	record.BindCommittedAuthFile(path, []byte(`{"type":"codex","access_token":"remote-committed"}`))
	if errNotify := h.notifyPersistedTokenRecord(context.Background(), path, record); errNotify != nil {
		t.Fatal(errNotify)
	}
	current, ok := manager.GetByID(record.ID)
	if !ok || current.Metadata["access_token"] != "remote-committed" {
		t.Fatal("default runtime registration used the stale mirror")
	}
}
