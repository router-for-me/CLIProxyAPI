package cliproxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/api"
	internalregistry "github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/watcher"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func TestRuntimeAuthSyncHook_SynchronousModelAndSchedulerRestoration(t *testing.T) {
	reg := internalregistry.GetGlobalRegistry()
	authID := "codex-sync-test-auth"
	reg.UnregisterClient(authID)
	t.Cleanup(func() {
		reg.UnregisterClient(authID)
	})

	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(&syncTestExecutor{})
	auth := &coreauth.Auth{
		ID:       authID,
		Provider: "codex",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"plan_type": "pro",
			"path":      "/path/to/codex-sync.json",
		},
	}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	// Set up a mock watcher wrapper where dispatchPersistedAuth records updates
	// but DOES NOT consume or execute anything asynchronously, verifying that
	// the synchronous restoration does not depend on the watcher background queue.
	var persistedUpdates []watcher.AuthUpdate
	watcherWrapper := &WatcherWrapper{
		dispatchPersistedAuth: func(update watcher.AuthUpdate) bool {
			persistedUpdates = append(persistedUpdates, update)
			return true
		},
	}

	service := &Service{
		cfg:         &config.Config{},
		coreManager: manager,
		watcher:     watcherWrapper,
	}

	hook := service.runtimeAuthSyncHook()
	if hook == nil {
		t.Fatal("runtimeAuthSyncHook() returned nil")
	}

	// 1. Initial sync (enabled -> registers models)
	if err := hook(context.Background(), auth); err != nil {
		t.Fatalf("hook failed for initial sync: %v", err)
	}
	manager.RegisterExecutor(&syncTestExecutor{})
	models := reg.GetModelsForClient(authID)
	if len(models) == 0 {
		t.Fatal("expected models registered for active auth, got none")
	}

	// Verify scheduler selection works for enabled auth
	resp, errExec := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-6-astra"}, cliproxyexecutor.Options{})
	if errExec != nil || string(resp.Payload) != authID {
		t.Fatalf("expected scheduler to select active auth, got resp=%q, err=%v", string(resp.Payload), errExec)
	}

	// 2. Disable the credential
	disabledAuth := auth.Clone()
	disabledAuth.Disabled = true
	disabledAuth.Status = coreauth.StatusDisabled
	disabledAuth.StatusMessage = "disabled via management API"
	disabledAuth.UpdatedAt = time.Now()

	if _, errUpdate := manager.Update(context.Background(), disabledAuth); errUpdate != nil {
		t.Fatalf("manager update disable failed: %v", errUpdate)
	}
	if err := hook(context.Background(), disabledAuth); err != nil {
		t.Fatalf("hook failed for disable: %v", err)
	}
	manager.RegisterExecutor(&syncTestExecutor{})

	// Verify immediately: models must be unregistered from global registry
	modelsAfterDisable := reg.GetModelsForClient(authID)
	if len(modelsAfterDisable) != 0 {
		t.Fatalf("expected 0 models for disabled client, got %d", len(modelsAfterDisable))
	}

	// Verify scheduler selection fails for disabled auth
	if _, errExecDisabled := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-6-astra"}, cliproxyexecutor.Options{}); errExecDisabled == nil {
		t.Fatal("expected scheduler execution to fail for disabled auth, but got nil error")
	}

	// 3. Re-enable the credential without quota
	enabledAuth := disabledAuth.Clone()
	enabledAuth.Disabled = false
	enabledAuth.Status = coreauth.StatusActive
	enabledAuth.StatusMessage = ""
	enabledAuth.UpdatedAt = time.Now()

	if _, errUpdate := manager.Update(context.Background(), enabledAuth); errUpdate != nil {
		t.Fatalf("manager update enable failed: %v", errUpdate)
	}

	// 4. Re-enable hook invocation
	if err := hook(context.Background(), enabledAuth); err != nil {
		t.Fatalf("hook failed for re-enable: %v", err)
	}
	manager.RegisterExecutor(&syncTestExecutor{})

	// Verify immediately: models must be restored in the global registry without restart
	modelsAfterEnable := reg.GetModelsForClient(authID)
	if len(modelsAfterEnable) == 0 {
		t.Fatal("expected models to be restored synchronously upon re-enable, got none")
	}

	hasAstra := false
	for _, m := range modelsAfterEnable {
		if m != nil && m.ID == "gpt-6-astra" {
			hasAstra = true
			break
		}
	}
	if !hasAstra {
		t.Fatalf("expected gpt-6-astra in restored models: %+v", modelsAfterEnable)
	}

	// Verify scheduler selection is restored for the re-enabled auth
	respAfterEnable, errExecEnable := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-6-astra"}, cliproxyexecutor.Options{})
	if errExecEnable != nil || string(respAfterEnable.Payload) != authID {
		t.Fatalf("expected scheduler to select restored auth, got resp=%q, err=%v", string(respAfterEnable.Payload), errExecEnable)
	}

	// 5. Verify quota cooldown separation: set quota exceeded on the enabled auth
	quotaAuth := enabledAuth.Clone()
	quotaAuth.Quota = coreauth.QuotaState{
		Exceeded:      true,
		Reason:        "credential_quota",
		NextRecoverAt: time.Now().Add(10 * time.Minute),
	}
	if _, errUpdate := manager.Update(context.Background(), quotaAuth); errUpdate != nil {
		t.Fatalf("manager update quota failed: %v", errUpdate)
	}
	if err := hook(context.Background(), quotaAuth); err != nil {
		t.Fatalf("hook failed for quota update: %v", err)
	}
	manager.RegisterExecutor(&syncTestExecutor{})

	// Verify quota separation: models remain registered in global registry
	modelsWithQuota := reg.GetModelsForClient(authID)
	if len(modelsWithQuota) == 0 {
		t.Fatal("expected models to remain registered when auth is in quota cooldown")
	}
	// But scheduler blocks execution due to quota exhaustion
	if _, errQuotaExec := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-6-astra"}, cliproxyexecutor.Options{}); errQuotaExec == nil {
		t.Fatal("expected scheduler to block execution when quota exceeded")
	}

	// Verify watcher notification occurred
	if len(persistedUpdates) == 0 {
		t.Fatal("expected watcher to be notified of persisted auth updates")
	}
}

type syncTestExecutor struct{}

func (e *syncTestExecutor) Identifier() string { return "codex" }

func (e *syncTestExecutor) Execute(ctx context.Context, a *coreauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	if a != nil && a.Metadata != nil {
		if tok, ok := a.Metadata["access_token"].(string); ok && tok != "" {
			return cliproxyexecutor.Response{Payload: []byte(tok)}, nil
		}
	}
	return cliproxyexecutor.Response{Payload: []byte(a.ID)}, nil
}

func (e *syncTestExecutor) ExecuteStream(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, &coreauth.Error{HTTPStatus: http.StatusNotImplemented, Message: "not implemented"}
}

func (e *syncTestExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *syncTestExecutor) CountTokens(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, &coreauth.Error{HTTPStatus: http.StatusNotImplemented, Message: "not implemented"}
}

func (e *syncTestExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, &coreauth.Error{HTTPStatus: http.StatusNotImplemented, Message: "not implemented"}
}

func TestRuntimeAuthSyncHook_NilAndEmptyAuth(t *testing.T) {
	service := &Service{}
	hook := service.runtimeAuthSyncHook()
	if hook == nil {
		t.Fatal("runtimeAuthSyncHook() returned nil")
	}

	if err := hook(context.Background(), nil); err != nil {
		t.Fatalf("expected nil error for nil auth, got: %v", err)
	}

	if err := hook(context.Background(), &coreauth.Auth{}); err != nil {
		t.Fatalf("expected nil error for empty auth, got: %v", err)
	}
}

func TestEndToEndStatusPatch_RestoresModelsThroughServerPipeline(t *testing.T) {
	authDir := t.TempDir()
	fileName := "codex-e2e.json"
	filePath := filepath.Join(authDir, fileName)
	if errWrite := os.WriteFile(filePath, []byte(`{"type":"codex","disabled":false}`), 0o600); errWrite != nil {
		t.Fatalf("write auth file: %v", errWrite)
	}

	reg := internalregistry.GetGlobalRegistry()
	authID := fileName
	reg.UnregisterClient(authID)
	t.Cleanup(func() {
		reg.UnregisterClient(authID)
	})

	secretKey := "test-secret"
	hashedSecret, errHash := bcrypt.GenerateFromPassword([]byte(secretKey), bcrypt.DefaultCost)
	if errHash != nil {
		t.Fatalf("bcrypt hash failed: %v", errHash)
	}

	cfg := &config.Config{
		AuthDir: authDir,
		RemoteManagement: config.RemoteManagement{
			SecretKey:   string(hashedSecret),
			AllowRemote: true,
		},
	}

	service, errBuild := NewBuilder().
		WithConfig(cfg).
		WithConfigPath(filepath.Join(authDir, "config.yaml")).
		Build()
	if errBuild != nil {
		t.Fatalf("Build() failed: %v", errBuild)
	}

	server := api.NewServer(service.cfg, service.coreManager, service.accessManager, service.configPath, service.serverOptions...)
	if server == nil {
		t.Fatal("NewServer() returned nil")
	}

	auth := &coreauth.Auth{
		ID:       authID,
		FileName: fileName,
		Provider: "codex",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"plan_type": "pro",
			"path":      filePath,
		},
	}
	if _, errRegister := service.coreManager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	// Initial model sync
	syncHook := service.runtimeAuthSyncHook()
	if err := syncHook(context.Background(), auth); err != nil {
		t.Fatalf("initial sync failed: %v", err)
	}

	handler := server.Handler()

	// 1. Confirm models exist before disabling
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v0/management/auth-files/models?name="+fileName, nil)
	req.Header.Set("Authorization", "Bearer test-secret")
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GetAuthFileModels initial status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Models []map[string]any `json:"models"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil || len(resp.Models) == 0 {
		t.Fatalf("expected initial models, got %s", rec.Body.String())
	}

	// 2. Disable credential via PATCH /v0/management/auth-files/status
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPatch, "/v0/management/auth-files/status", strings.NewReader(`{"name":"`+fileName+`","disabled":true}`))
	req.Header.Set("Authorization", "Bearer test-secret")
	req.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH status disable failed: %s", rec.Body.String())
	}

	// 3. Confirm GET /v0/management/auth-files/models returns empty list immediately
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/v0/management/auth-files/models?name="+fileName, nil)
	req.Header.Set("Authorization", "Bearer test-secret")
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GetAuthFileModels after disable status = %d, body = %s", rec.Code, rec.Body.String())
	}
	resp.Models = nil
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil || len(resp.Models) != 0 {
		t.Fatalf("expected 0 models after disable, got: %s", rec.Body.String())
	}

	// 4. Re-enable credential via PATCH /v0/management/auth-files/status
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPatch, "/v0/management/auth-files/status", strings.NewReader(`{"name":"`+fileName+`","disabled":false}`))
	req.Header.Set("Authorization", "Bearer test-secret")
	req.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH status re-enable failed: %s", rec.Body.String())
	}

	// 5. Confirm models are synchronously restored immediately without restart
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/v0/management/auth-files/models?name="+fileName, nil)
	req.Header.Set("Authorization", "Bearer test-secret")
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GetAuthFileModels after re-enable status = %d, body = %s", rec.Code, rec.Body.String())
	}
	resp.Models = nil
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil || len(resp.Models) == 0 {
		t.Fatalf("expected models to be restored after re-enable, got: %s", rec.Body.String())
	}
}

func TestRuntimeAuthSync_StaleAsyncEventDoesNotOverwriteSynchronousEnable(t *testing.T) {
	reg := internalregistry.GetGlobalRegistry()
	authID := "codex-stale-test-auth"
	reg.UnregisterClient(authID)
	t.Cleanup(func() {
		reg.UnregisterClient(authID)
	})

	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(&syncTestExecutor{})

	var currentRevision uint64
	watcherWrapper := &WatcherWrapper{
		dispatchPersistedAuthWithRev: func(update *watcher.AuthUpdate) (bool, uint64) {
			currentRevision++
			return true, currentRevision
		},
	}

	service := &Service{
		cfg:         &config.Config{},
		coreManager: manager,
		watcher:     watcherWrapper,
	}

	auth := &coreauth.Auth{
		ID:       authID,
		Provider: "codex",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"plan_type": "pro",
		},
	}
	registeredAuth, errRegister := manager.Register(context.Background(), auth)
	if errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	hook := service.runtimeAuthSyncHook()

	// Initial sync
	if err := hook(context.Background(), registeredAuth); err != nil {
		t.Fatalf("initial hook sync failed: %v", err)
	}
	if len(reg.GetModelsForClient(authID)) == 0 {
		t.Fatal("expected models after initial sync")
	}

	// Step 1: Credential is disabled (Generation advances)
	disabledAuth := registeredAuth.Clone()
	disabledAuth.Disabled = true
	disabledAuth.Status = coreauth.StatusDisabled
	disabledAuth.UpdatedAt = time.Now()

	updatedDisabled, errDisable := manager.Update(context.Background(), disabledAuth)
	if errDisable != nil {
		t.Fatalf("update disable failed: %v", errDisable)
	}

	// Capture the stale async event that would normally sit in an async queue
	staleAsyncDisableUpdate := watcher.AuthUpdate{
		Action: watcher.AuthUpdateActionModify,
		ID:     authID,
		Auth:   updatedDisabled.Clone(),
	}
	staleAsyncDisableUpdate.SetRevision(2)

	// Step 2: Synchronous disable hook runs (assigns revision 2)
	if err := hook(context.Background(), updatedDisabled); err != nil {
		t.Fatalf("hook disable failed: %v", err)
	}
	if len(reg.GetModelsForClient(authID)) != 0 {
		t.Fatal("expected 0 models after disable")
	}

	// Step 3: Credential is immediately re-enabled (advances revision to 3)
	enabledAuth := updatedDisabled.Clone()
	enabledAuth.Disabled = false
	enabledAuth.Status = coreauth.StatusActive
	enabledAuth.UpdatedAt = time.Now()

	updatedEnabled, errEnable := manager.Update(context.Background(), enabledAuth)
	if errEnable != nil {
		t.Fatalf("update enable failed: %v", errEnable)
	}

	// Step 4: Synchronous enable hook runs (assigns revision 3)
	if err := hook(context.Background(), updatedEnabled); err != nil {
		t.Fatalf("hook enable failed: %v", err)
	}
	manager.RegisterExecutor(&syncTestExecutor{})

	// Models must be restored immediately
	if len(reg.GetModelsForClient(authID)) == 0 {
		t.Fatal("expected models restored immediately upon synchronous enable")
	}

	// Step 5: The delayed stale disable update from Step 1 is delivered now via consumer
	service.handleAuthUpdate(context.Background(), staleAsyncDisableUpdate)
	manager.RegisterExecutor(&syncTestExecutor{})

	// Step 6: Verify the stale async event was prevented from overwriting the newer enable state!
	currentAuth, ok := manager.GetByID(authID)
	if !ok || currentAuth == nil {
		t.Fatal("expected auth to exist in manager")
	}
	if currentAuth.Disabled {
		t.Fatal("stale async event overwrote the synchronous enable state in manager!")
	}
	modelsAfterStale := reg.GetModelsForClient(authID)
	if len(modelsAfterStale) == 0 {
		t.Fatal("stale async event unregistered models from global registry!")
	}

	// Scheduler execution must still succeed and select the enabled auth
	resp, errExec := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-6-astra"}, cliproxyexecutor.Options{})
	if errExec != nil || string(resp.Payload) != authID {
		t.Fatalf("expected scheduler to select active auth, got resp=%q err=%v", string(resp.Payload), errExec)
	}
}

func TestRuntimeAuthSync_StaleWatcherUnversionedEventDoesNotOverwriteSynchronousEnable(t *testing.T) {
	reg := internalregistry.GetGlobalRegistry()
	authID := "codex-unversioned-stale-test-auth"
	reg.UnregisterClient(authID)
	t.Cleanup(func() {
		reg.UnregisterClient(authID)
	})

	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(&syncTestExecutor{})

	var currentRevision uint64
	watcherWrapper := &WatcherWrapper{
		dispatchPersistedAuthWithRev: func(update *watcher.AuthUpdate) (bool, uint64) {
			currentRevision++
			return true, currentRevision
		},
	}

	service := &Service{
		cfg:         &config.Config{},
		coreManager: manager,
		watcher:     watcherWrapper,
	}

	auth := &coreauth.Auth{
		ID:       authID,
		Provider: "codex",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"plan_type": "pro",
		},
	}
	registeredAuth, errRegister := manager.Register(context.Background(), auth)
	if errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	hook := service.runtimeAuthSyncHook()

	// Initial sync
	if err := hook(context.Background(), registeredAuth); err != nil {
		t.Fatalf("initial hook sync failed: %v", err)
	}
	manager.RegisterExecutor(&syncTestExecutor{})
	if len(reg.GetModelsForClient(authID)) == 0 {
		t.Fatal("expected models after initial sync")
	}

	// Step 1: Simulate the watcher generating an unversioned Auth snapshot during a disable
	// event on disk (as synthesizer.SynthesizeAuthFile produces: RegistrationEpoch=0, Generation=0).
	// Timestamp is recorded at observation time T1.
	unversionedDisableTime := time.Now().Add(-50 * time.Millisecond)
	unversionedDisableAuth := &coreauth.Auth{
		ID:                authID,
		Provider:          "codex",
		Disabled:          true,
		Status:            coreauth.StatusDisabled,
		CreatedAt:         unversionedDisableTime,
		UpdatedAt:         unversionedDisableTime,
		RegistrationEpoch: 0, // Unversioned from file watcher synthesizer
		Generation:        0, // Unversioned from file watcher synthesizer
		Attributes: map[string]string{
			"plan_type": "pro",
		},
	}
	delayedWatcherUpdate := watcher.AuthUpdate{
		Action: watcher.AuthUpdateActionModify,
		ID:     authID,
		Auth:   unversionedDisableAuth,
	}
	delayedWatcherUpdate.SetRevision(2)

	// Step 2: In the meantime, the credential was synchronously re-enabled via management API
	// at T2 > T1, updating coreManager with RegistrationEpoch >= 1, Generation >= 2, UpdatedAt = T2.
	enabledAuth := registeredAuth.Clone()
	enabledAuth.Disabled = false
	enabledAuth.Status = coreauth.StatusActive
	enabledAuth.UpdatedAt = time.Now()

	updatedEnabled, errEnable := manager.Update(context.Background(), enabledAuth)
	if errEnable != nil {
		t.Fatalf("update enable failed: %v", errEnable)
	}

	// Synchronous enable hook runs and restores models in GlobalModelRegistry
	if err := hook(context.Background(), updatedEnabled); err != nil {
		t.Fatalf("hook enable failed: %v", err)
	}
	manager.RegisterExecutor(&syncTestExecutor{})

	if len(reg.GetModelsForClient(authID)) == 0 {
		t.Fatal("expected models restored immediately upon synchronous enable")
	}

	// Step 3: Now the delayed unversioned watcher event from Step 1 arrives via the queue consumer
	service.handleAuthUpdate(context.Background(), delayedWatcherUpdate)
	manager.RegisterExecutor(&syncTestExecutor{})

	// Scheduler execution must still succeed and select the enabled auth
	resp, errExec := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-6-astra"}, cliproxyexecutor.Options{})
	if errExec != nil || string(resp.Payload) != authID {
		t.Fatalf("expected scheduler to select active auth, got resp=%q err=%v", string(resp.Payload), errExec)
	}
}

func TestRuntimeAuthSync_FileSnapshotEnqueuedThenRequestExecutionThenConsume(t *testing.T) {
	reg := internalregistry.GetGlobalRegistry()
	authID := "codex-interleaved-request-auth"
	reg.UnregisterClient(authID)
	t.Cleanup(func() {
		reg.UnregisterClient(authID)
	})

	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(&syncTestExecutor{})

	service := &Service{
		cfg:         &config.Config{},
		coreManager: manager,
	}

	auth := &coreauth.Auth{
		ID:       authID,
		Provider: "codex",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"plan_type": "pro",
		},
	}
	registeredAuth, errRegister := manager.Register(context.Background(), auth)
	if errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	hook := service.runtimeAuthSyncHook()

	// Initial sync (Revision 1)
	if err := hook(context.Background(), registeredAuth); err != nil {
		t.Fatalf("initial hook sync failed: %v", err)
	}
	manager.RegisterExecutor(&syncTestExecutor{})
	if len(reg.GetModelsForClient(authID)) == 0 {
		t.Fatal("expected models after initial sync")
	}

	// Step 1: A file change occurs on disk (e.g. user disables credential in auth file).
	// The watcher generates an AuthUpdate with revision 2 and enqueues it.
	fileSnapshotAuth := registeredAuth.Clone()
	fileSnapshotAuth.Disabled = true
	fileSnapshotAuth.Status = coreauth.StatusDisabled
	fileSnapshotAuth.UpdatedAt = time.Now()

	fileUpdate := watcher.AuthUpdate{
		Action: watcher.AuthUpdateActionModify,
		ID:     authID,
		Auth:   fileSnapshotAuth,
	}
	fileUpdate.SetRevision(2)

	// Step 2: Before the queue consumer can process fileUpdate, an API request completes.
	// MarkResult updates the auth in coreManager: auth.Generation++ and auth.UpdatedAt = now.
	manager.MarkResult(context.Background(), coreauth.Result{
		AuthID:   authID,
		Provider: "codex",
		Model:    "gpt-6-astra",
		Success:  true,
	})

	// Step 3: Now the queue consumer processes the fileUpdate (revision 2).
	// Because revision 2 > revision 1, the file update MUST NOT be discarded due to
	// the intermediate request's UpdatedAt timestamp!
	service.handleAuthUpdate(context.Background(), fileUpdate)
	manager.RegisterExecutor(&syncTestExecutor{})

	// Step 4: Verify the file update successfully took effect!
	currentAuth, ok := manager.GetByID(authID)
	if !ok || currentAuth == nil {
		t.Fatal("expected auth to exist in manager")
	}
	if !currentAuth.Disabled {
		t.Fatal("file update was incorrectly dropped after intermediate request execution!")
	}
	modelsAfterDisable := reg.GetModelsForClient(authID)
	if len(modelsAfterDisable) != 0 {
		t.Fatalf("expected 0 models after file update disable, got %d", len(modelsAfterDisable))
	}
}

func TestRuntimeAuthSyncHook_ContextCancellationDoesNotAbortSync(t *testing.T) {
	reg := internalregistry.GetGlobalRegistry()
	authID := "codex-canceled-ctx-auth"
	reg.UnregisterClient(authID)
	t.Cleanup(func() {
		reg.UnregisterClient(authID)
	})

	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(&syncTestExecutor{})

	service := &Service{
		cfg:         &config.Config{},
		coreManager: manager,
	}

	auth := &coreauth.Auth{
		ID:       authID,
		Provider: "codex",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"plan_type": "pro",
		},
	}
	registeredAuth, errRegister := manager.Register(context.Background(), auth)
	if errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	hook := service.runtimeAuthSyncHook()

	// Create a context that is ALREADY canceled (e.g. client aborted request right after save)
	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	// Calling hook with canceled context must NOT abort registration
	if err := hook(canceledCtx, registeredAuth); err != nil {
		t.Fatalf("hook failed with canceled context: %v", err)
	}
	manager.RegisterExecutor(&syncTestExecutor{})

	// Verify models were registered successfully despite canceled incoming context
	models := reg.GetModelsForClient(authID)
	if len(models) == 0 {
		t.Fatal("expected models to be registered even when incoming context is canceled")
	}

	// Verify scheduler can execute
	resp, errExec := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-6-astra"}, cliproxyexecutor.Options{})
	if errExec != nil || string(resp.Payload) != authID {
		t.Fatalf("expected scheduler execution to succeed, got resp=%q err=%v", string(resp.Payload), errExec)
	}
}

func TestEndToEndAuthFileReplacement_RestoresModelsInV1ModelsWithoutRestart(t *testing.T) {
	authDir := t.TempDir()
	fileName := "codex-team.json"
	filePath := filepath.Join(authDir, fileName)

	// Synthetic JWT containing CodexAuthInfo claims with chatgpt_plan_type: team
	teamIDToken := "eyJhbGciOiJub25lIn0.eyJlbWFpbCI6ICJ1c2VyQGV4YW1wbGUuY29tIiwgImh0dHBzOi8vYXBpLm9wZW5haS5jb20vYXV0aCI6IHsiY2hhdGdwdF9wbGFuX3R5cGUiOiAidGVhbSIsICJjaGF0Z3B0X2FjY291bnRfaWQiOiAiYWNjLTEyMyJ9fQ.sig"
	initialContent := fmt.Sprintf(`{"type":"codex","email":"user@example.com","access_token":"token-old","id_token":%q}`, teamIDToken)
	if errWrite := os.WriteFile(filePath, []byte(initialContent), 0o600); errWrite != nil {
		t.Fatalf("write auth file: %v", errWrite)
	}

	reg := internalregistry.GetGlobalRegistry()
	authID := fileName
	reg.UnregisterClient(authID)
	t.Cleanup(func() {
		reg.UnregisterClient(authID)
	})

	secretKey := "test-secret"
	hashedSecret, errHash := bcrypt.GenerateFromPassword([]byte(secretKey), bcrypt.DefaultCost)
	if errHash != nil {
		t.Fatalf("bcrypt hash failed: %v", errHash)
	}

	cfg := &config.Config{
		AuthDir: authDir,
		RemoteManagement: config.RemoteManagement{
			SecretKey:   string(hashedSecret),
			AllowRemote: true,
		},
	}

	service, errBuild := NewBuilder().
		WithConfig(cfg).
		WithConfigPath(filepath.Join(authDir, "config.yaml")).
		Build()
	if errBuild != nil {
		t.Fatalf("Build() failed: %v", errBuild)
	}

	server := api.NewServer(service.cfg, service.coreManager, service.accessManager, service.configPath, service.serverOptions...)
	if server == nil {
		t.Fatal("NewServer() returned nil")
	}
	handler := server.Handler()
	service.coreManager.RegisterExecutor(&syncTestExecutor{})

	auth := &coreauth.Auth{
		ID:       authID,
		FileName: fileName,
		Provider: "codex",
		Status:   coreauth.StatusActive,
		Metadata: map[string]any{
			"type":         "codex",
			"email":        "user@example.com",
			"access_token": "token-old",
			"id_token":     teamIDToken,
		},
		Attributes: map[string]string{
			"plan_type": "team",
			"path":      filePath,
		},
	}
	if _, errRegister := service.coreManager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	// Initial model sync
	syncHook := service.runtimeAuthSyncHook()
	if err := syncHook(context.Background(), auth); err != nil {
		t.Fatalf("initial sync failed: %v", err)
	}

	// 1. Confirm all team models are initially visible in GET /v1/models
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /v1/models initial status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var modelsResp struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &modelsResp); err != nil {
		t.Fatalf("failed to decode /v1/models response: %v", err)
	}
	hasModel := func(modelID string) bool {
		for _, m := range modelsResp.Data {
			if id, _ := m["id"].(string); id == modelID {
				return true
			}
		}
		return false
	}
	teamModels := []string{"gpt-6-astra", "gpt-5.6-sol", "gpt-5.6-terra", "codex-auto-review"}
	for _, m := range teamModels {
		if !hasModel(m) {
			t.Fatalf("expected %q to be in initial /v1/models list: %s", m, rec.Body.String())
		}
	}

	// 2. Simulate terminal authentication error on all 4 models (invalidated token)
	unauthErr := &coreauth.Error{
		HTTPStatus: http.StatusUnauthorized,
		Code:       "unauthorized",
		Message:    "Encountered invalidated oauth token: test-token",
	}
	for _, m := range teamModels {
		service.coreManager.MarkResult(context.Background(), coreauth.Result{
			AuthID:     authID,
			Provider:   "codex",
			Model:      m,
			RouteModel: m,
			Success:    false,
			Error:      unauthErr,
		})
	}

	// Confirm all 4 models are now omitted from /v1/models due to active error/cooldown
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	handler.ServeHTTP(rec, req)
	modelsResp.Data = nil
	if err := json.Unmarshal(rec.Body.Bytes(), &modelsResp); err != nil {
		t.Fatalf("failed to decode /v1/models response: %v", err)
	}
	for _, m := range teamModels {
		if hasModel(m) {
			t.Fatalf("expected %q to be absent after terminal auth failure", m)
		}
	}

	// 3. Replace credential via management upload (POST /v0/management/auth-files)
	newContent := fmt.Sprintf(`{"type":"codex","email":"user@example.com","access_token":"token-new","id_token":%q}`, teamIDToken)
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", fileName)
	if err != nil {
		t.Fatalf("failed to create multipart file: %v", err)
	}
	if _, err = part.Write([]byte(newContent)); err != nil {
		t.Fatalf("failed to write multipart content: %v", err)
	}
	if err = writer.Close(); err != nil {
		t.Fatalf("failed to close multipart writer: %v", err)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/v0/management/auth-files", &body)
	req.Header.Set("Authorization", "Bearer test-secret")
	req.Header.Set("Content-Type", writer.FormDataContentType())
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("upload failed: status=%d, body=%s", rec.Code, rec.Body.String())
	}

	// 4. Verify that GET /v1/models immediately includes all team models without restart
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	handler.ServeHTTP(rec, req)
	modelsResp.Data = nil
	if err := json.Unmarshal(rec.Body.Bytes(), &modelsResp); err != nil {
		t.Fatalf("failed to decode /v1/models response: %v", err)
	}
	for _, m := range teamModels {
		if !hasModel(m) {
			t.Fatalf("expected %q to be restored in /v1/models after credential replacement: %s", m, rec.Body.String())
		}
	}

	// 5. Verify direct inference request succeeds using the replacement credential token
	service.coreManager.RegisterExecutor(&syncTestExecutor{})
	resp, errExec := service.coreManager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-6-astra"}, cliproxyexecutor.Options{})
	if errExec != nil || string(resp.Payload) != "token-new" {
		t.Fatalf("expected inference execution to use replacement token, got resp=%q err=%v", string(resp.Payload), errExec)
	}
}

func TestEndToEndAuthFilePatch_RestoresModelsInV1ModelsWithoutRestart(t *testing.T) {
	authDir := t.TempDir()
	fileName := "codex-patch.json"
	filePath := filepath.Join(authDir, fileName)

	teamIDToken := "eyJhbGciOiJub25lIn0.eyJlbWFpbCI6ICJ1c2VyQGV4YW1wbGUuY29tIiwgImh0dHBzOi8vYXBpLm9wZW5haS5jb20vYXV0aCI6IHsiY2hhdGdwdF9wbGFuX3R5cGUiOiAidGVhbSIsICJjaGF0Z3B0X2FjY291bnRfaWQiOiAiYWNjLTEyMyJ9fQ.sig"
	initialContent := fmt.Sprintf(`{"type":"codex","email":"user@example.com","access_token":"token-old","id_token":%q}`, teamIDToken)
	if errWrite := os.WriteFile(filePath, []byte(initialContent), 0o600); errWrite != nil {
		t.Fatalf("write auth file: %v", errWrite)
	}

	reg := internalregistry.GetGlobalRegistry()
	authID := fileName
	reg.UnregisterClient(authID)
	t.Cleanup(func() {
		reg.UnregisterClient(authID)
	})

	secretKey := "test-secret"
	hashedSecret, errHash := bcrypt.GenerateFromPassword([]byte(secretKey), bcrypt.DefaultCost)
	if errHash != nil {
		t.Fatalf("bcrypt hash failed: %v", errHash)
	}

	cfg := &config.Config{
		AuthDir: authDir,
		RemoteManagement: config.RemoteManagement{
			SecretKey:   string(hashedSecret),
			AllowRemote: true,
		},
	}

	service, errBuild := NewBuilder().
		WithConfig(cfg).
		WithConfigPath(filepath.Join(authDir, "config.yaml")).
		Build()
	if errBuild != nil {
		t.Fatalf("Build() failed: %v", errBuild)
	}

	server := api.NewServer(service.cfg, service.coreManager, service.accessManager, service.configPath, service.serverOptions...)
	if server == nil {
		t.Fatal("NewServer() returned nil")
	}
	handler := server.Handler()
	service.coreManager.RegisterExecutor(&syncTestExecutor{})

	auth := &coreauth.Auth{
		ID:       authID,
		FileName: fileName,
		Provider: "codex",
		Status:   coreauth.StatusActive,
		Metadata: map[string]any{
			"type":         "codex",
			"email":        "user@example.com",
			"access_token": "token-old",
			"id_token":     teamIDToken,
		},
		Attributes: map[string]string{
			"plan_type": "team",
			"path":      filePath,
		},
	}
	if _, errRegister := service.coreManager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	syncHook := service.runtimeAuthSyncHook()
	if err := syncHook(context.Background(), auth); err != nil {
		t.Fatalf("initial sync failed: %v", err)
	}

	// 1. Simulate terminal auth failure on all 4 models
	unauthErr := &coreauth.Error{
		HTTPStatus: http.StatusUnauthorized,
		Code:       "unauthorized",
		Message:    "Encountered invalidated oauth token: test-token",
	}
	teamModels := []string{"gpt-6-astra", "gpt-5.6-sol", "gpt-5.6-terra", "codex-auto-review"}
	for _, m := range teamModels {
		service.coreManager.MarkResult(context.Background(), coreauth.Result{
			AuthID:     authID,
			Provider:   "codex",
			Model:      m,
			RouteModel: m,
			Success:    false,
			Error:      unauthErr,
		})
	}

	// Confirm all 4 models are now omitted from /v1/models due to active error/cooldown
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	handler.ServeHTTP(rec, req)
	var modelsResp struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &modelsResp); err != nil {
		t.Fatalf("failed to decode /v1/models response: %v", err)
	}
	hasModel := func(modelID string) bool {
		for _, m := range modelsResp.Data {
			if id, _ := m["id"].(string); id == modelID {
				return true
			}
		}
		return false
	}
	for _, m := range teamModels {
		if hasModel(m) {
			t.Fatalf("expected %q to be absent after terminal auth failure", m)
		}
	}

	// 2. Patch auth file fields with fresh access token
	patchBody := fmt.Sprintf(`{"name":%q,"access_token":"token-fresh","id_token":%q}`, fileName, teamIDToken)
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPatch, "/v0/management/auth-files/fields", strings.NewReader(patchBody))
	req.Header.Set("Authorization", "Bearer test-secret")
	req.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH fields failed: status=%d, body=%s", rec.Code, rec.Body.String())
	}

	// 3. Confirm all team models are restored in /v1/models
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	handler.ServeHTTP(rec, req)
	modelsResp.Data = nil
	if err := json.Unmarshal(rec.Body.Bytes(), &modelsResp); err != nil {
		t.Fatalf("failed to decode /v1/models response: %v", err)
	}
	for _, m := range teamModels {
		if !hasModel(m) {
			t.Fatalf("expected %q to be restored in /v1/models after PATCH: %s", m, rec.Body.String())
		}
	}

	// 4. Verify direct inference request succeeds using fresh token after PATCH
	service.coreManager.RegisterExecutor(&syncTestExecutor{})
	resp, errExec := service.coreManager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-6-astra"}, cliproxyexecutor.Options{})
	if errExec != nil || string(resp.Payload) != "token-fresh" {
		t.Fatalf("expected inference execution to use fresh token after PATCH, got resp=%q err=%v", string(resp.Payload), errExec)
	}
}
