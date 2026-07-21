package auth

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

type planRefreshExecutor struct{}

type blockingPlanRefreshExecutor struct {
	planRefreshExecutor
	entered chan struct{}
	release chan struct{}
}

type refreshFailingStore struct {
	err error
}

type fileRefreshStore struct {
	path string
}

func (s *refreshFailingStore) List(context.Context) ([]*Auth, error) { return nil, nil }

func (s *refreshFailingStore) Save(context.Context, *Auth) (string, error) {
	return "", s.err
}

func (s *refreshFailingStore) Delete(context.Context, string) error { return nil }

func (s *fileRefreshStore) List(context.Context) ([]*Auth, error) { return nil, nil }

func (s *fileRefreshStore) Save(_ context.Context, auth *Auth) (string, error) {
	value := authMetadataString(auth, "access_token")
	if errWrite := os.WriteFile(s.path, []byte(value), 0o600); errWrite != nil {
		return "", errWrite
	}
	return s.path, nil
}

func (s *fileRefreshStore) Delete(context.Context, string) error {
	return os.Remove(s.path)
}

func (planRefreshExecutor) Identifier() string { return "codex" }

func (planRefreshExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (planRefreshExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, nil
}

func (planRefreshExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	if auth.Attributes == nil {
		auth.Attributes = make(map[string]string)
	}
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	auth.Attributes["plan_type"] = "plus"
	auth.Metadata["plan_type"] = "plus"
	auth.Metadata["access_token"] = "fresh-access-token"
	return auth, nil
}

func (e blockingPlanRefreshExecutor) Refresh(ctx context.Context, auth *Auth) (*Auth, error) {
	e.entered <- struct{}{}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-e.release:
	}
	return e.planRefreshExecutor.Refresh(ctx, auth)
}

func (planRefreshExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (planRefreshExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func TestManager_RefreshAuthInvokesPostRefreshHook(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, nil, nil)
	manager.RegisterExecutor(planRefreshExecutor{})
	auth := &Auth{
		ID:         "codex-plan-refresh",
		Provider:   "codex",
		Attributes: map[string]string{"plan_type": "free"},
		Metadata: map[string]any{
			"access_token":  "stale-access-token",
			"refresh_token": "refresh-token",
		},
	}
	if _, errRegister := manager.Register(ctx, auth); errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}

	var hooked *Auth
	manager.SetPostRefreshHook(func(_ context.Context, refreshed *Auth) error {
		hooked = refreshed.Clone()
		return nil
	})

	refreshed, errRefresh := manager.RefreshAuth(ctx, auth.ID)
	if errRefresh != nil {
		t.Fatalf("RefreshAuth() error = %v", errRefresh)
	}
	if refreshed == nil {
		t.Fatal("RefreshAuth() auth = nil")
	}
	if got := refreshed.Attributes["plan_type"]; got != "plus" {
		t.Fatalf("refreshed plan_type = %q, want plus", got)
	}
	if hooked == nil {
		t.Fatal("post-refresh hook was not called")
	}
	if got := hooked.Attributes["plan_type"]; got != "plus" {
		t.Fatalf("hooked plan_type = %q, want plus", got)
	}
	if got, _ := hooked.Metadata["access_token"].(string); got != "fresh-access-token" {
		t.Fatalf("hooked access_token = %q, want fresh-access-token", got)
	}
}

func TestManager_RefreshAuthRejectsAuthWithoutRefreshMechanism(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, nil, nil)
	manager.RegisterExecutor(planRefreshExecutor{})
	auth := &Auth{
		ID:       "codex-not-refreshable",
		Provider: "codex",
		Metadata: map[string]any{"access_token": "access-token"},
	}
	if _, errRegister := manager.Register(ctx, auth); errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}

	refreshed, errRefresh := manager.RefreshAuth(ctx, auth.ID)
	if !errors.Is(errRefresh, ErrAuthNotRefreshable) {
		t.Fatalf("RefreshAuth() error = %v, want ErrAuthNotRefreshable", errRefresh)
	}
	if refreshed != nil {
		t.Fatalf("RefreshAuth() auth = %#v, want nil", refreshed)
	}
	current, ok := manager.GetByID(auth.ID)
	if !ok || current == nil {
		t.Fatal("auth missing after rejected refresh")
	}
	if !current.LastRefreshedAt.IsZero() {
		t.Fatalf("LastRefreshedAt = %v, want zero", current.LastRefreshedAt)
	}
}

func TestManager_RefreshAuthDoesNotRestoreConcurrentlyRemovedAuth(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, nil, nil)
	executor := blockingPlanRefreshExecutor{
		entered: make(chan struct{}, 1),
		release: make(chan struct{}),
	}
	manager.RegisterExecutor(executor)
	auth := &Auth{
		ID:       "codex-removed-during-refresh",
		Provider: "codex",
		Metadata: map[string]any{"refresh_token": "refresh-token"},
	}
	if _, errRegister := manager.Register(ctx, auth); errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}
	hookCalled := false
	manager.SetPostRefreshHook(func(context.Context, *Auth) error {
		hookCalled = true
		return nil
	})

	type refreshResult struct {
		auth *Auth
		err  error
	}
	result := make(chan refreshResult, 1)
	go func() {
		refreshed, errRefresh := manager.RefreshAuth(ctx, auth.ID)
		result <- refreshResult{auth: refreshed, err: errRefresh}
	}()

	<-executor.entered
	removeDone := make(chan struct{})
	go func() {
		manager.Remove(ctx, auth.ID)
		close(removeDone)
	}()
	close(executor.release)
	got := <-result
	if got.err != nil {
		t.Fatalf("RefreshAuth() error = %v", got.err)
	}
	if got.auth == nil {
		t.Fatal("RefreshAuth() auth = nil")
	}
	<-removeDone
	if _, ok := manager.GetByID(auth.ID); ok {
		t.Fatal("concurrently removed auth was restored")
	}
	if !hookCalled {
		t.Fatal("post-refresh hook was not called before serialized removal")
	}
}

func TestManager_RemoveWithCleanupDeletesCredentialAfterConcurrentRefresh(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "codex.json")
	store := &fileRefreshStore{path: path}
	manager := NewManager(store, nil, nil)
	executor := blockingPlanRefreshExecutor{
		entered: make(chan struct{}, 1),
		release: make(chan struct{}),
	}
	manager.RegisterExecutor(executor)
	auth := &Auth{
		ID:       "codex-delete-during-refresh",
		Provider: "codex",
		Metadata: map[string]any{
			"access_token":  "stale-access-token",
			"refresh_token": "refresh-token",
		},
	}
	if _, errRegister := manager.Register(ctx, auth); errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}

	refreshResult := make(chan error, 1)
	go func() {
		_, errRefresh := manager.RefreshAuth(ctx, auth.ID)
		refreshResult <- errRefresh
	}()
	<-executor.entered

	removeStarted := make(chan struct{})
	cleanupEntered := make(chan struct{})
	removeResult := make(chan error, 1)
	go func() {
		close(removeStarted)
		removeResult <- manager.RemoveWithCleanup(ctx, auth.ID, func() error {
			close(cleanupEntered)
			return os.Remove(path)
		})
	}()
	<-removeStarted
	select {
	case <-cleanupEntered:
		t.Fatal("cleanup ran before the in-flight refresh released its auth lock")
	case <-time.After(50 * time.Millisecond):
	}

	close(executor.release)
	if errRefresh := <-refreshResult; errRefresh != nil {
		t.Fatalf("RefreshAuth() error = %v", errRefresh)
	}
	if errRemove := <-removeResult; errRemove != nil {
		t.Fatalf("RemoveWithCleanup() error = %v", errRemove)
	}
	if _, errStat := os.Stat(path); !os.IsNotExist(errStat) {
		t.Fatalf("credential file still exists after serialized removal: %v", errStat)
	}
	if _, ok := manager.GetByID(auth.ID); ok {
		t.Fatal("auth remains registered after serialized removal")
	}
}

func TestManager_RefreshAuthReturnsPersistenceFailureAfterPostRefreshHook(t *testing.T) {
	ctx := context.Background()
	persistErr := errors.New("save failed")
	manager := NewManager(&refreshFailingStore{err: persistErr}, nil, nil)
	manager.RegisterExecutor(planRefreshExecutor{})
	auth := &Auth{
		ID:       "codex-refresh-persist-failure",
		Provider: "codex",
		Metadata: map[string]any{"refresh_token": "refresh-token"},
	}
	if _, errRegister := manager.Register(ctx, auth); errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}
	hookCalled := false
	manager.SetPostRefreshHook(func(context.Context, *Auth) error {
		hookCalled = true
		return nil
	})

	refreshed, errRefresh := manager.RefreshAuth(ctx, auth.ID)
	if !errors.Is(errRefresh, persistErr) {
		t.Fatalf("RefreshAuth() error = %v, want persistence failure", errRefresh)
	}
	if refreshed == nil {
		t.Fatal("RefreshAuth() auth = nil, want updated runtime auth")
	}
	if !hookCalled {
		t.Fatal("post-refresh hook was not called for the updated runtime auth")
	}
}

func TestManager_TryRefreshAfterUnauthorizedRetriesWithRuntimeAuthAfterPersistenceFailure(t *testing.T) {
	ctx := context.Background()
	persistErr := errors.New("save failed")
	manager := NewManager(&refreshFailingStore{err: persistErr}, nil, nil)
	manager.RegisterExecutor(planRefreshExecutor{})
	auth := &Auth{
		ID:       "codex-401-refresh-persist-failure",
		Provider: "codex",
		Metadata: map[string]any{
			"access_token":  "stale-access-token",
			"refresh_token": "refresh-token",
		},
	}
	if _, errRegister := manager.Register(ctx, auth); errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}

	refreshed, retry := manager.tryRefreshAfterUnauthorized(ctx, auth, &Error{
		Code:       "unauthorized",
		Message:    "access token expired",
		HTTPStatus: http.StatusUnauthorized,
	}, false)
	if !retry {
		t.Fatal("tryRefreshAfterUnauthorized() retry = false, want true")
	}
	if refreshed == nil {
		t.Fatal("tryRefreshAfterUnauthorized() auth = nil")
	}
	if got := authAccessToken(refreshed); got != "fresh-access-token" {
		t.Fatalf("refreshed access token = %q, want fresh-access-token", got)
	}
	current, ok := manager.GetByID(auth.ID)
	if !ok || current == nil {
		t.Fatal("refreshed runtime auth missing from manager")
	}
	if got := authAccessToken(current); got != "fresh-access-token" {
		t.Fatalf("runtime access token = %q, want fresh-access-token", got)
	}
}

type rawJSONRefreshStorage struct {
	raw []byte
}

func (s *rawJSONRefreshStorage) SaveTokenToFile(string) error { return nil }

func (s *rawJSONRefreshStorage) RawJSON() []byte {
	if s == nil {
		return nil
	}
	return append([]byte(nil), s.raw...)
}

func TestManager_RefreshAuthAllowsPluginStorageWithoutRefreshToken(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, nil, nil)
	manager.RegisterExecutor(planRefreshExecutor{})
	auth := &Auth{
		ID:       "plugin-storage-refresh",
		Provider: "codex",
		Storage:  &rawJSONRefreshStorage{raw: []byte(`{"type":"plugin","token":"stored-refresh-material"}`)},
		Metadata: map[string]any{"access_token": "access-token"},
	}
	if _, errRegister := manager.Register(ctx, auth); errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}

	refreshed, errRefresh := manager.RefreshAuth(ctx, auth.ID)
	if errRefresh != nil {
		t.Fatalf("RefreshAuth() error = %v", errRefresh)
	}
	if refreshed == nil {
		t.Fatal("RefreshAuth() auth = nil")
	}
	if got := authAccessToken(refreshed); got != "fresh-access-token" {
		t.Fatalf("access token = %q, want fresh-access-token", got)
	}
}

func TestManager_RefreshAuthRejectsTypeOnlyStoragePayload(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, nil, nil)
	manager.RegisterExecutor(planRefreshExecutor{})
	auth := &Auth{
		ID:       "plugin-type-only-storage",
		Provider: "codex",
		Storage:  &rawJSONRefreshStorage{raw: []byte(`{"type":"plugin","access_token":"access-only"}`)},
		Metadata: map[string]any{"access_token": "access-only"},
	}
	if _, errRegister := manager.Register(ctx, auth); errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}

	refreshed, errRefresh := manager.RefreshAuth(ctx, auth.ID)
	if !errors.Is(errRefresh, ErrAuthNotRefreshable) {
		t.Fatalf("RefreshAuth() error = %v, want ErrAuthNotRefreshable", errRefresh)
	}
	if refreshed != nil {
		t.Fatalf("RefreshAuth() auth = %#v, want nil", refreshed)
	}
}

func TestStoragePayloadHasRefreshMaterial(t *testing.T) {
	if !storagePayloadHasRefreshMaterial([]byte(`{"type":"plugin","token":"x"}`)) {
		t.Fatal("expected token field to count as refresh material")
	}
	if storagePayloadHasRefreshMaterial([]byte(`{"type":"plugin","access_token":"x"}`)) {
		t.Fatal("access_token alone must not count as refresh material")
	}
	if storagePayloadHasRefreshMaterial([]byte(`{"type":"plugin"}`)) {
		t.Fatal("type-only payload must not count as refresh material")
	}
	if !storagePayloadHasRefreshMaterial([]byte("opaque-refresh-blob")) {
		t.Fatal("opaque non-JSON payload should count as refresh material")
	}
}

func TestManager_RemovePathWithCleanupLocksSiblingAuthsBeforeDelete(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "plugin-shared.json")
	if errWrite := os.WriteFile(path, []byte("stale-credential"), 0o600); errWrite != nil {
		t.Fatalf("WriteFile() error = %v", errWrite)
	}
	store := &fileRefreshStore{path: path}
	manager := NewManager(store, nil, nil)
	executor := blockingPlanRefreshExecutor{
		entered: make(chan struct{}, 1),
		release: make(chan struct{}),
	}
	manager.RegisterExecutor(executor)

	authA := &Auth{
		ID:       "plugin-shared-a",
		Provider: "codex",
		Attributes: map[string]string{
			AttributePath:          path,
			AttributeVirtualSource: path,
			AttributePluginVirtual: pluginVirtualAttrEnabled,
		},
		Metadata: map[string]any{
			"access_token":  "stale-access-a",
			"refresh_token": "refresh-token-a",
		},
	}
	authB := &Auth{
		ID:       "plugin-shared-b",
		Provider: "codex",
		Attributes: map[string]string{
			AttributePath:          path,
			AttributeVirtualSource: path,
			AttributePluginVirtual: pluginVirtualAttrEnabled,
		},
		Metadata: map[string]any{
			"access_token":  "stale-access-b",
			"refresh_token": "refresh-token-b",
		},
	}
	if _, errRegister := manager.Register(ctx, authA); errRegister != nil {
		t.Fatalf("Register(A) error = %v", errRegister)
	}
	if _, errRegister := manager.Register(ctx, authB); errRegister != nil {
		t.Fatalf("Register(B) error = %v", errRegister)
	}

	refreshResult := make(chan error, 1)
	go func() {
		_, errRefresh := manager.RefreshAuth(ctx, authB.ID)
		refreshResult <- errRefresh
	}()
	<-executor.entered

	cleanupEntered := make(chan struct{})
	removeResult := make(chan error, 1)
	go func() {
		removeResult <- manager.RemovePathWithCleanup(ctx, []string{authA.ID, authB.ID}, func() error {
			close(cleanupEntered)
			return os.Remove(path)
		})
	}()

	select {
	case <-cleanupEntered:
		t.Fatal("cleanup ran before the sibling refresh released its auth lock")
	case <-time.After(50 * time.Millisecond):
	}

	close(executor.release)
	if errRefresh := <-refreshResult; errRefresh != nil {
		t.Fatalf("RefreshAuth() error = %v", errRefresh)
	}
	if errRemove := <-removeResult; errRemove != nil {
		t.Fatalf("RemovePathWithCleanup() error = %v", errRemove)
	}
	if _, errStat := os.Stat(path); !os.IsNotExist(errStat) {
		t.Fatalf("credential file still exists after multi-auth removal: %v", errStat)
	}
	if _, ok := manager.GetByID(authA.ID); ok {
		t.Fatal("auth A remains registered after multi-auth removal")
	}
	if _, ok := manager.GetByID(authB.ID); ok {
		t.Fatal("auth B remains registered after multi-auth removal")
	}
}
