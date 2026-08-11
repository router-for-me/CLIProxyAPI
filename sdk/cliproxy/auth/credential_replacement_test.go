package auth

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	baseauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth"
	claudeauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/claude"
	codexauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/redisqueue"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

type opaqueCredentialStorage struct{}

func (*opaqueCredentialStorage) SaveTokenToFile(string) error { return nil }

type rawJSONCredentialStorage struct {
	payload []byte
}

func (*rawJSONCredentialStorage) SaveTokenToFile(string) error { return nil }

func (s *rawJSONCredentialStorage) RawJSON() []byte {
	if s == nil {
		return nil
	}
	return append([]byte(nil), s.payload...)
}

type coherentCredentialSnapshotStorage struct {
	payload  []byte
	metadata map[string]any
}

func (*coherentCredentialSnapshotStorage) SaveTokenToFile(string) error { return nil }

func (s *coherentCredentialSnapshotStorage) RawJSON() []byte {
	if s == nil {
		return nil
	}
	return append([]byte(nil), s.payload...)
}

func (s *coherentCredentialSnapshotStorage) CredentialSnapshot() ([]byte, map[string]any, baseauth.CredentialFingerprintMaterial) {
	if s == nil {
		return nil, nil, baseauth.CredentialFingerprintMaterial{}
	}
	metadata := make(map[string]any, len(s.metadata))
	for key, value := range s.metadata {
		metadata[key] = value
	}
	payload := append([]byte(nil), s.payload...)
	return payload, metadata, baseauth.CredentialFingerprintMaterial{Opaque: string(payload)}
}

type mutableCredentialSnapshotStorage struct {
	mu       sync.RWMutex
	metadata map[string]any
}

func (*mutableCredentialSnapshotStorage) SaveTokenToFile(string) error { return nil }

func (s *mutableCredentialSnapshotStorage) SetMetadata(metadata map[string]any) {
	s.mu.Lock()
	s.metadata = cloneCredentialMetadataValue(metadata).(map[string]any)
	s.mu.Unlock()
}

func (s *mutableCredentialSnapshotStorage) CredentialSnapshot() ([]byte, map[string]any, baseauth.CredentialFingerprintMaterial) {
	s.mu.RLock()
	metadata := cloneCredentialMetadataValue(s.metadata).(map[string]any)
	s.mu.RUnlock()
	token, _ := metadata["token"].(string)
	return []byte(token), metadata, baseauth.CredentialFingerprintMaterial{Opaque: token}
}

type blockedMetadataPersistenceStore struct {
	blockNext chan struct{}
	blocked   chan struct{}
	release   chan struct{}
}

func (*blockedMetadataPersistenceStore) List(context.Context) ([]*Auth, error) { return nil, nil }
func (*blockedMetadataPersistenceStore) Delete(context.Context, string) error  { return nil }

func (s *blockedMetadataPersistenceStore) Save(_ context.Context, auth *Auth) (string, error) {
	select {
	case <-s.blockNext:
		close(s.blocked)
		<-s.release
	default:
	}
	if setter, ok := auth.Storage.(interface{ SetMetadata(map[string]any) }); ok {
		setter.SetMetadata(auth.Metadata)
	}
	return auth.ID, nil
}

type fingerprintedRawJSONCredentialStorage struct {
	payload  []byte
	material baseauth.CredentialFingerprintMaterial
}

func (*fingerprintedRawJSONCredentialStorage) SaveTokenToFile(string) error { return nil }

func (s *fingerprintedRawJSONCredentialStorage) RawJSON() []byte {
	if s == nil {
		return nil
	}
	return append([]byte(nil), s.payload...)
}

func (s *fingerprintedRawJSONCredentialStorage) CredentialFingerprintMaterial() baseauth.CredentialFingerprintMaterial {
	if s == nil {
		return baseauth.CredentialFingerprintMaterial{}
	}
	return s.material
}

type orderedCredentialStore struct {
	mu             sync.Mutex
	blockNext      bool
	blocked        bool
	concurrentSeen bool
	blockedSave    chan struct{}
	releaseSave    chan struct{}
	concurrentSave chan struct{}
	lastPersisted  *Auth
	lastDeletedID  string
}

func (*orderedCredentialStore) List(context.Context) ([]*Auth, error) { return nil, nil }

func (s *orderedCredentialStore) Save(_ context.Context, auth *Auth) (string, error) {
	s.mu.Lock()
	if s.blockNext {
		s.blockNext = false
		s.blocked = true
		blockedSave := s.blockedSave
		releaseSave := s.releaseSave
		s.mu.Unlock()

		close(blockedSave)
		<-releaseSave

		s.mu.Lock()
		s.blocked = false
		s.lastPersisted = auth.Clone()
		s.mu.Unlock()
		return orderedCredentialStoreSaveID(auth), nil
	}
	if s.blocked && !s.concurrentSeen {
		s.concurrentSeen = true
		close(s.concurrentSave)
	}
	s.lastPersisted = auth.Clone()
	s.mu.Unlock()
	return orderedCredentialStoreSaveID(auth), nil
}

func orderedCredentialStoreSaveID(auth *Auth) string {
	if auth == nil {
		return ""
	}
	if auth.FileName != "" {
		return auth.FileName
	}
	return auth.ID
}

func (s *orderedCredentialStore) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	s.lastPersisted = nil
	s.lastDeletedID = id
	s.mu.Unlock()
	return nil
}

func (s *orderedCredentialStore) blockNextSave() {
	s.mu.Lock()
	s.blockNext = true
	s.blockedSave = make(chan struct{})
	s.releaseSave = make(chan struct{})
	s.concurrentSave = make(chan struct{})
	s.mu.Unlock()
}

func (s *orderedCredentialStore) lastAuth() *Auth {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastPersisted.Clone()
}

func (s *orderedCredentialStore) lastDeleteID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastDeletedID
}

type targetTrackingCredentialStore struct {
	mu          sync.Mutex
	blockNext   bool
	blockedSave chan struct{}
	releaseSave chan struct{}
	persisted   map[string]*Auth
}

func (*targetTrackingCredentialStore) List(context.Context) ([]*Auth, error) { return nil, nil }

func (s *targetTrackingCredentialStore) Save(_ context.Context, auth *Auth) (string, error) {
	target := orderedCredentialStoreSaveID(auth)
	s.mu.Lock()
	if s.blockNext {
		s.blockNext = false
		blockedSave := s.blockedSave
		releaseSave := s.releaseSave
		s.mu.Unlock()

		close(blockedSave)
		<-releaseSave

		s.mu.Lock()
	}
	if s.persisted == nil {
		s.persisted = make(map[string]*Auth)
	}
	s.persisted[target] = auth.Clone()
	s.mu.Unlock()
	return target, nil
}

func (s *targetTrackingCredentialStore) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	delete(s.persisted, id)
	s.mu.Unlock()
	return nil
}

func (s *targetTrackingCredentialStore) blockNextSaveCall() {
	s.mu.Lock()
	s.blockNext = true
	s.blockedSave = make(chan struct{})
	s.releaseSave = make(chan struct{})
	s.mu.Unlock()
}

func (s *targetTrackingCredentialStore) authAt(target string) *Auth {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.persisted[target].Clone()
}

type blockingUnauthorizedCredentialExecutor struct {
	started  chan struct{}
	release  chan struct{}
	provider string
}

func (e *blockingUnauthorizedCredentialExecutor) Identifier() string {
	if e.provider != "" {
		return e.provider
	}
	return "codex"
}

func (e *blockingUnauthorizedCredentialExecutor) Execute(ctx context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	select {
	case <-e.started:
	default:
		close(e.started)
	}
	select {
	case <-e.release:
		return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusUnauthorized, Message: "old token unauthorized"}
	case <-ctx.Done():
		return cliproxyexecutor.Response{}, ctx.Err()
	}
}

func (e *blockingUnauthorizedCredentialExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, &Error{HTTPStatus: http.StatusNotImplemented, Message: "not implemented"}
}

func (e *blockingUnauthorizedCredentialExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *blockingUnauthorizedCredentialExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusNotImplemented, Message: "not implemented"}
}

func (e *blockingUnauthorizedCredentialExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, &Error{HTTPStatus: http.StatusNotImplemented, Message: "not implemented"}
}

type blockingSuccessfulCredentialExecutor struct {
	started chan struct{}
	release chan struct{}
}

func (*blockingSuccessfulCredentialExecutor) Identifier() string { return "codex" }

func (e *blockingSuccessfulCredentialExecutor) Execute(ctx context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	close(e.started)
	select {
	case <-e.release:
		return cliproxyexecutor.Response{Payload: []byte("old credential succeeded")}, nil
	case <-ctx.Done():
		return cliproxyexecutor.Response{}, ctx.Err()
	}
}

func (*blockingSuccessfulCredentialExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, &Error{HTTPStatus: http.StatusNotImplemented, Message: "not implemented"}
}

func (*blockingSuccessfulCredentialExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (*blockingSuccessfulCredentialExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusNotImplemented, Message: "not implemented"}
}

func (*blockingSuccessfulCredentialExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, &Error{HTTPStatus: http.StatusNotImplemented, Message: "not implemented"}
}

func TestExecuteIgnoresFailureFromCredentialReplacedInFlight(t *testing.T) {
	executor := &blockingUnauthorizedCredentialExecutor{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	m := NewManager(nil, nil, nil)
	m.RegisterExecutor(executor)
	m.SetRetryConfig(0, 0, 1)
	auth := &Auth{
		ID:       "codex-inflight.json",
		Provider: "codex",
		Status:   StatusActive,
		Metadata: map[string]any{"access_token": "old-access-token"},
	}
	if _, err := m.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "gpt-test"}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
	m.RefreshSchedulerEntry(auth.ID)

	done := make(chan error, 1)
	go func() {
		_, err := m.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-test"}, cliproxyexecutor.Options{})
		done <- err
	}()
	select {
	case <-executor.started:
	case <-time.After(time.Second):
		t.Fatal("execution did not start")
	}

	current, _ := m.GetByID(auth.ID)
	replacement := current.Clone()
	replacement.Metadata["access_token"] = "new-access-token"
	if _, err := m.Update(context.Background(), replacement); err != nil {
		t.Fatalf("Update replacement: %v", err)
	}
	close(executor.release)
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Execute error = nil, want old credential unauthorized")
		}
	case <-time.After(time.Second):
		t.Fatal("execution did not finish")
	}

	current, _ = m.GetByID(auth.ID)
	if current == nil || current.Status != StatusActive || current.Unavailable || current.LastError != nil {
		t.Fatalf("in-flight old failure changed replacement auth: %#v", current)
	}
	if len(current.ModelStates) != 0 {
		t.Fatalf("in-flight old failure created model state: %#v", current.ModelStates)
	}
}

type lateUnauthorizedStreamCredentialExecutor struct {
	provider string
	release  chan struct{}
}

func (e *lateUnauthorizedStreamCredentialExecutor) Identifier() string { return e.provider }

func (*lateUnauthorizedStreamCredentialExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusNotImplemented, Message: "not implemented"}
}

func (e *lateUnauthorizedStreamCredentialExecutor) ExecuteStream(ctx context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	chunks := make(chan cliproxyexecutor.StreamChunk, 2)
	chunks <- cliproxyexecutor.StreamChunk{Payload: []byte("started")}
	go func() {
		defer close(chunks)
		select {
		case <-e.release:
			chunks <- cliproxyexecutor.StreamChunk{Err: &Error{HTTPStatus: http.StatusUnauthorized, Message: "old stream unauthorized"}}
		case <-ctx.Done():
		}
	}()
	return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
}

func (e *lateUnauthorizedStreamCredentialExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (*lateUnauthorizedStreamCredentialExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusNotImplemented, Message: "not implemented"}
}

func (*lateUnauthorizedStreamCredentialExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, &Error{HTTPStatus: http.StatusNotImplemented, Message: "not implemented"}
}

func TestExecuteCapturesOpaquePluginCredentialBeforeDispatch(t *testing.T) {
	const provider = "opaque-plugin"
	executor := &blockingUnauthorizedCredentialExecutor{
		started:  make(chan struct{}),
		release:  make(chan struct{}),
		provider: provider,
	}
	m := NewManager(nil, nil, nil)
	m.RegisterExecutor(executor)
	m.SetRetryConfig(0, 0, 1)
	storage := &rawJSONCredentialStorage{payload: []byte(`{"token":"old-token"}`)}
	auth := &Auth{ID: "opaque-plugin-inflight.json", Provider: provider, Status: StatusActive, Storage: storage}
	if _, err := m.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "plugin-model"}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
	m.RefreshSchedulerEntry(auth.ID)

	done := make(chan error, 1)
	go func() {
		_, err := m.Execute(context.Background(), []string{provider}, cliproxyexecutor.Request{Model: "plugin-model"}, cliproxyexecutor.Options{})
		done <- err
	}()
	select {
	case <-executor.started:
	case <-time.After(time.Second):
		t.Fatal("execution did not start")
	}

	storage.payload = []byte(`{"token":"new-token"}`)
	current, _ := m.GetByID(auth.ID)
	if _, err := m.Update(context.Background(), current.Clone()); err != nil {
		t.Fatalf("Update replacement: %v", err)
	}
	close(executor.release)
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Execute error = nil, want old credential unauthorized")
		}
	case <-time.After(time.Second):
		t.Fatal("execution did not finish")
	}

	current, _ = m.GetByID(auth.ID)
	if current == nil || current.Status != StatusActive || current.Unavailable || current.LastError != nil {
		t.Fatalf("in-flight old failure changed replacement auth: %#v", current)
	}
	if len(current.ModelStates) != 0 {
		t.Fatalf("in-flight old failure created model state: %#v", current.ModelStates)
	}
}

func TestConditionalUpdatePersistsBeforeReplacementTakesOwnership(t *testing.T) {
	store := &orderedCredentialStore{}
	m := NewManager(store, nil, nil)
	auth := &Auth{
		ID:       "refresh-persist-race.json",
		Provider: "opaque-plugin",
		Metadata: map[string]any{"access_token": "old-token"},
	}
	if _, err := m.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register: %v", err)
	}

	m.mu.RLock()
	expectedCurrent := m.auths[auth.ID]
	m.mu.RUnlock()
	if expectedCurrent == nil {
		t.Fatal("registered auth missing")
	}

	refreshed := expectedCurrent.Clone()
	refreshed.Metadata["access_token"] = "refreshed-old-token"
	replacement := expectedCurrent.Clone()
	replacement.Metadata["access_token"] = "replacement-token"

	store.blockNextSave()
	refreshDone := make(chan error, 1)
	go func() {
		_, _, err := m.updateAuth(context.Background(), refreshed, expectedCurrent)
		refreshDone <- err
	}()
	select {
	case <-store.blockedSave:
	case <-time.After(time.Second):
		t.Fatal("refresh persistence did not block")
	}

	replacementDone := make(chan error, 1)
	go func() {
		_, err := m.Update(context.Background(), replacement)
		replacementDone <- err
	}()
	select {
	case <-store.concurrentSave:
	case <-time.After(250 * time.Millisecond):
	}
	close(store.releaseSave)

	select {
	case err := <-refreshDone:
		if err != nil {
			t.Fatalf("conditional update: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("conditional update did not finish")
	}
	select {
	case err := <-replacementDone:
		if err != nil {
			t.Fatalf("replacement update: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("replacement update did not finish")
	}

	persisted := store.lastAuth()
	if got := authMetadataString(persisted, "access_token"); got != "replacement-token" {
		t.Fatalf("last persisted access token = %q, want replacement token", got)
	}
}

func TestUpdateSynchronizesMutableCredentialStorageBeforePublication(t *testing.T) {
	store := &blockedMetadataPersistenceStore{
		blockNext: make(chan struct{}),
		blocked:   make(chan struct{}),
		release:   make(chan struct{}),
	}
	storage := &mutableCredentialSnapshotStorage{metadata: map[string]any{"token": "old-token"}}
	m := NewManager(store, &RoundRobinSelector{}, nil)
	auth := &Auth{
		ID:       "plugin-metadata-patch.json",
		Provider: "opaque-plugin",
		Metadata: map[string]any{"token": "old-token"},
		Storage:  storage,
	}
	if _, err := m.Register(WithSkipPersist(context.Background()), auth); err != nil {
		t.Fatalf("Register: %v", err)
	}

	patched, _ := m.GetByID(auth.ID)
	patched.Metadata["token"] = "new-token"
	close(store.blockNext)
	updateDone := make(chan error, 1)
	go func() {
		_, err := m.Update(context.Background(), patched)
		updateDone <- err
	}()
	select {
	case <-store.blocked:
	case <-time.After(time.Second):
		t.Fatal("persistence did not block")
	}

	published, ok := m.GetByID(auth.ID)
	if !ok || published == nil {
		t.Fatal("published auth missing")
	}
	execution, _ := snapshotAuthCredential(published)
	if token, _ := execution.Metadata["token"].(string); token != "new-token" {
		t.Fatalf("dispatch snapshot token = %q, want new-token", token)
	}

	close(store.release)
	select {
	case err := <-updateDone:
		if err != nil {
			t.Fatalf("Update: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Update did not finish")
	}
}

func TestConditionalUpdateDoesNotPublishStaleOwnerAfterReplacement(t *testing.T) {
	store := &orderedCredentialStore{}
	m := NewManager(store, &RoundRobinSelector{}, nil)
	auth := &Auth{
		ID:       "refresh-publish-race.json",
		Provider: "opaque-plugin",
		Metadata: map[string]any{"access_token": "old-token"},
	}
	if _, err := m.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register: %v", err)
	}

	m.mu.RLock()
	expectedCurrent := m.auths[auth.ID]
	m.mu.RUnlock()
	if expectedCurrent == nil {
		t.Fatal("registered auth missing")
	}
	refreshed := expectedCurrent.Clone()
	refreshed.Metadata["access_token"] = "refreshed-old-token"
	replacement := expectedCurrent.Clone()
	replacement.Metadata["access_token"] = "replacement-token"

	type updateResult struct {
		auth    *Auth
		applied bool
		err     error
	}
	store.blockNextSave()
	refreshDone := make(chan updateResult, 1)
	go func() {
		updated, applied, err := m.updateAuth(context.Background(), refreshed, expectedCurrent)
		refreshDone <- updateResult{auth: updated, applied: applied, err: err}
	}()
	select {
	case <-store.blockedSave:
	case <-time.After(time.Second):
		t.Fatal("refresh persistence did not block")
	}

	if _, err := m.Update(context.Background(), replacement); err != nil {
		t.Fatalf("Update replacement: %v", err)
	}
	close(store.releaseSave)

	var got updateResult
	select {
	case got = <-refreshDone:
	case <-time.After(time.Second):
		t.Fatal("conditional update did not finish")
	}
	if got.err != nil {
		t.Fatalf("conditional update: %v", got.err)
	}
	if got.applied {
		t.Fatal("conditional update reported stale owner as applied")
	}
	if token := authMetadataString(got.auth, "access_token"); token != "replacement-token" {
		t.Fatalf("conditional update returned token %q, want replacement token", token)
	}

	picked, errPick := m.scheduler.pickSingle(context.Background(), auth.Provider, "", cliproxyexecutor.Options{}, nil)
	if errPick != nil {
		t.Fatalf("scheduler pick: %v", errPick)
	}
	if token := authMetadataString(picked, "access_token"); token != "replacement-token" {
		t.Fatalf("scheduler published token %q, want replacement token", token)
	}
}

func TestConditionalUpdateRemovesStaleTargetAfterReplacementRename(t *testing.T) {
	store := &targetTrackingCredentialStore{}
	m := NewManager(store, nil, nil)
	auth := &Auth{
		ID:       "stable-runtime-id",
		FileName: "old-credential.json",
		Provider: "opaque-plugin",
		Metadata: map[string]any{"access_token": "old-token"},
	}
	if _, err := m.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register: %v", err)
	}

	m.mu.RLock()
	expectedCurrent := m.auths[auth.ID]
	m.mu.RUnlock()
	if expectedCurrent == nil {
		t.Fatal("registered auth missing")
	}

	refreshed := expectedCurrent.Clone()
	refreshed.Metadata["access_token"] = "refreshed-old-token"
	store.blockNextSaveCall()
	refreshDone := make(chan error, 1)
	go func() {
		_, _, err := m.updateAuth(context.Background(), refreshed, expectedCurrent)
		refreshDone <- err
	}()
	select {
	case <-store.blockedSave:
	case <-time.After(time.Second):
		t.Fatal("refresh persistence did not block")
	}

	replacement := expectedCurrent.Clone()
	replacement.FileName = "new-credential.json"
	replacement.Metadata["access_token"] = "replacement-token"
	if _, err := m.Update(context.Background(), replacement); err != nil {
		t.Fatalf("replacement update: %v", err)
	}
	close(store.releaseSave)

	select {
	case err := <-refreshDone:
		if err != nil {
			t.Fatalf("conditional update: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("conditional update did not finish")
	}

	if persisted := store.authAt(auth.FileName); persisted != nil {
		t.Fatalf("stale target %q remains persisted: %#v", auth.FileName, persisted)
	}
	persisted := store.authAt(replacement.FileName)
	if got := authMetadataString(persisted, "access_token"); got != "replacement-token" {
		t.Fatalf("replacement target token = %q, want replacement token", got)
	}
}

func TestConditionalUpdateDoesNotRecreateRemovedAuth(t *testing.T) {
	store := &orderedCredentialStore{}
	m := NewManager(store, nil, nil)
	auth := &Auth{
		ID:       "runtime-refresh-delete-id",
		FileName: "refresh-persist-delete-race.json",
		Provider: "opaque-plugin",
		Metadata: map[string]any{"access_token": "old-token"},
	}
	if _, err := m.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register: %v", err)
	}

	m.mu.RLock()
	expectedCurrent := m.auths[auth.ID]
	m.mu.RUnlock()
	if expectedCurrent == nil {
		t.Fatal("registered auth missing")
	}
	refreshed := expectedCurrent.Clone()
	refreshed.Metadata["access_token"] = "refreshed-token"

	store.blockNextSave()
	refreshDone := make(chan error, 1)
	go func() {
		_, _, err := m.updateAuth(context.Background(), refreshed, expectedCurrent)
		refreshDone <- err
	}()
	select {
	case <-store.blockedSave:
	case <-time.After(time.Second):
		t.Fatal("refresh persistence did not block")
	}

	if err := store.Delete(context.Background(), auth.ID); err != nil {
		t.Fatalf("external delete: %v", err)
	}
	m.Remove(context.Background(), auth.ID)
	close(store.releaseSave)

	select {
	case err := <-refreshDone:
		if err != nil {
			t.Fatalf("conditional update: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("conditional update did not finish")
	}
	if persisted := store.lastAuth(); persisted != nil {
		t.Fatalf("removed auth was recreated by stale refresh save: %#v", persisted)
	}
	if got := store.lastDeleteID(); got != auth.FileName {
		t.Fatalf("stale save cleanup deleted %q, want saved target %q", got, auth.FileName)
	}
	if _, ok := m.GetByID(auth.ID); ok {
		t.Fatal("removed auth returned to runtime state")
	}
}

func TestConditionalUpdatePersistenceDoesNotBlockOtherAuthAccess(t *testing.T) {
	store := &orderedCredentialStore{}
	m := NewManager(store, nil, nil)
	refreshAuth := &Auth{
		ID:       "refresh-persist-unrelated-access.json",
		Provider: "opaque-plugin",
		Metadata: map[string]any{"access_token": "old-token"},
	}
	otherAuth := &Auth{
		ID:       "other-auth.json",
		Provider: "other-provider",
		Metadata: map[string]any{"access_token": "other-token"},
	}
	if _, err := m.Register(context.Background(), refreshAuth); err != nil {
		t.Fatalf("Register refresh auth: %v", err)
	}
	if _, err := m.Register(context.Background(), otherAuth); err != nil {
		t.Fatalf("Register other auth: %v", err)
	}

	m.mu.RLock()
	expectedCurrent := m.auths[refreshAuth.ID]
	m.mu.RUnlock()
	if expectedCurrent == nil {
		t.Fatal("registered refresh auth missing")
	}
	refreshed := expectedCurrent.Clone()
	refreshed.Metadata["access_token"] = "refreshed-token"

	store.blockNextSave()
	refreshDone := make(chan error, 1)
	go func() {
		_, _, err := m.updateAuth(context.Background(), refreshed, expectedCurrent)
		refreshDone <- err
	}()
	select {
	case <-store.blockedSave:
	case <-time.After(time.Second):
		t.Fatal("refresh persistence did not block")
	}

	released := false
	defer func() {
		if !released {
			close(store.releaseSave)
		}
	}()
	lookupDone := make(chan *Auth, 1)
	go func() {
		auth, _ := m.GetByID(otherAuth.ID)
		lookupDone <- auth
	}()
	select {
	case got := <-lookupDone:
		if got == nil || got.ID != otherAuth.ID {
			t.Fatalf("other auth lookup = %#v, want %q", got, otherAuth.ID)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("blocked persistence held the global auth lock")
	}

	close(store.releaseSave)
	released = true
	select {
	case err := <-refreshDone:
		if err != nil {
			t.Fatalf("conditional update: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("conditional update did not finish")
	}
}

func TestSnapshotAuthCredentialFreezesOpaquePluginStorage(t *testing.T) {
	storage := &rawJSONCredentialStorage{payload: []byte(`{"token":"old-token"}`)}
	auth := &Auth{Provider: "opaque-plugin", Storage: storage}

	snapshot, revision := snapshotAuthCredential(auth)
	storage.payload = []byte(`{"token":"new-token"}`)

	source, ok := snapshot.Storage.(interface{ RawJSON() []byte })
	if !ok {
		t.Fatalf("snapshot storage = %T, want RawJSON source", snapshot.Storage)
	}
	if got := string(source.RawJSON()); got != `{"token":"old-token"}` {
		t.Fatalf("snapshot storage = %s, want old credential", got)
	}
	if revision != captureAuthCredentialRevision(snapshot) {
		t.Fatal("snapshot credential and revision diverged")
	}
	if revision == captureAuthCredentialRevision(auth) {
		t.Fatal("snapshot revision still tracks mutable storage")
	}
}

func TestSnapshotAuthCredentialUsesCoherentStorageGeneration(t *testing.T) {
	storage := &coherentCredentialSnapshotStorage{
		payload:  []byte(`{"token":"new-token"}`),
		metadata: map[string]any{"access_token": "new-token"},
	}
	auth := &Auth{
		Provider: "opaque-plugin",
		Metadata: map[string]any{"access_token": "old-token"},
		Storage:  storage,
	}

	snapshot, _ := snapshotAuthCredential(auth)

	if got := snapshot.Metadata["access_token"]; got != "new-token" {
		t.Fatalf("snapshot access token = %v, want metadata from storage generation", got)
	}
	source, ok := snapshot.Storage.(interface{ RawJSON() []byte })
	if !ok {
		t.Fatalf("snapshot storage = %T, want RawJSON source", snapshot.Storage)
	}
	if got := string(source.RawJSON()); got != `{"token":"new-token"}` {
		t.Fatalf("snapshot storage = %s, want matching storage generation", got)
	}
}

func TestCredentialFingerprintPrefersStorageMaterialOverRawSerialization(t *testing.T) {
	material := baseauth.CredentialFingerprintMaterial{APIKey: "provider-owned-secret"}
	first := &Auth{
		Provider: "opaque-plugin",
		Storage: &fingerprintedRawJSONCredentialStorage{
			payload:  []byte(`{"note":"first","token":"provider-owned-secret"}`),
			material: material,
		},
	}
	second := &Auth{
		Provider: "opaque-plugin",
		Storage: &fingerprintedRawJSONCredentialStorage{
			payload:  []byte(`{"note":"second","token":"provider-owned-secret"}`),
			material: material,
		},
	}

	firstFingerprint, firstOK := authCredentialFingerprint(first)
	secondFingerprint, secondOK := authCredentialFingerprint(second)
	if !firstOK || !secondOK {
		t.Fatal("credential fingerprint missing")
	}
	if firstFingerprint != secondFingerprint {
		t.Fatal("noncredential storage metadata changed credential fingerprint")
	}
}

func TestSnapshotAuthCredentialDeepCopiesServiceAccount(t *testing.T) {
	serviceAccount := map[string]any{
		"project_id":  "shared-project",
		"private_key": "old-private-key",
		"scopes":      []any{"scope-a", map[string]any{"audience": "old-audience"}},
	}
	auth := &Auth{
		Provider: "vertex",
		Metadata: map[string]any{"service_account": serviceAccount},
	}

	snapshot, revision := snapshotAuthCredential(auth)
	serviceAccount["private_key"] = "new-private-key"
	serviceAccount["scopes"].([]any)[1].(map[string]any)["audience"] = "new-audience"

	gotServiceAccount, ok := snapshot.Metadata["service_account"].(map[string]any)
	if !ok {
		t.Fatalf("snapshot service account = %T, want map[string]any", snapshot.Metadata["service_account"])
	}
	if got := gotServiceAccount["private_key"]; got != "old-private-key" {
		t.Fatalf("snapshot private key = %v, want old-private-key", got)
	}
	gotScopes := gotServiceAccount["scopes"].([]any)
	if got := gotScopes[1].(map[string]any)["audience"]; got != "old-audience" {
		t.Fatalf("snapshot nested audience = %v, want old-audience", got)
	}
	if revision != captureAuthCredentialRevision(snapshot) {
		t.Fatal("snapshot service account and revision diverged")
	}
	if revision == captureAuthCredentialRevision(auth) {
		t.Fatal("snapshot revision still tracks mutable service-account metadata")
	}
}

func TestExecuteStreamIgnoresLateFailureFromReplacedOpaqueCredential(t *testing.T) {
	const provider = "opaque-stream-plugin"
	executor := &lateUnauthorizedStreamCredentialExecutor{provider: provider, release: make(chan struct{})}
	m := NewManager(nil, nil, nil)
	m.RegisterExecutor(executor)
	m.SetRetryConfig(0, 0, 1)
	storage := &rawJSONCredentialStorage{payload: []byte(`{"token":"old-token"}`)}
	auth := &Auth{ID: "opaque-stream-inflight.json", Provider: provider, Status: StatusActive, Storage: storage}
	if _, err := m.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "plugin-model"}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
	m.RefreshSchedulerEntry(auth.ID)

	stream, err := m.ExecuteStream(context.Background(), []string{provider}, cliproxyexecutor.Request{Model: "plugin-model"}, cliproxyexecutor.Options{Stream: true})
	if err != nil {
		t.Fatalf("ExecuteStream: %v", err)
	}
	storage.payload = []byte(`{"token":"new-token"}`)
	current, _ := m.GetByID(auth.ID)
	if _, err := m.Update(context.Background(), current.Clone()); err != nil {
		t.Fatalf("Update replacement: %v", err)
	}
	close(executor.release)
	for range stream.Chunks {
	}

	current, _ = m.GetByID(auth.ID)
	if current == nil || current.Status != StatusActive || current.Unavailable || current.LastError != nil {
		t.Fatalf("late old stream failure changed replacement auth: %#v", current)
	}
	if len(current.ModelStates) != 0 {
		t.Fatalf("late old stream failure created model state: %#v", current.ModelStates)
	}
}

func TestRecordExecutionResultIgnoresStaleUnauthorizedAfterCredentialReplacement(t *testing.T) {
	m := NewManager(nil, nil, nil)
	oldAuth := &Auth{
		ID:       "codex-same-file.json",
		Provider: "codex",
		Status:   StatusActive,
		Metadata: map[string]any{
			"access_token":  "old-access-token",
			"refresh_token": "old-refresh-token",
		},
	}
	if _, err := m.Register(context.Background(), oldAuth); err != nil {
		t.Fatalf("Register: %v", err)
	}
	selected, ok := m.GetByID(oldAuth.ID)
	if !ok || selected == nil {
		t.Fatal("selected auth missing")
	}

	replacement := selected.Clone()
	replacement.Metadata["access_token"] = "new-access-token"
	replacement.Metadata["refresh_token"] = "new-refresh-token"
	if _, err := m.Update(context.Background(), replacement); err != nil {
		t.Fatalf("Update replacement: %v", err)
	}

	m.recordExecutionResult(context.Background(), Result{
		AuthID:   oldAuth.ID,
		Provider: oldAuth.Provider,
		Model:    "gpt-test",
		Error: &Error{
			HTTPStatus: http.StatusUnauthorized,
			Message:    "old token unauthorized",
		},
	}, selected, false)

	current, ok := m.GetByID(oldAuth.ID)
	if !ok || current == nil {
		t.Fatal("current auth missing")
	}
	if current.Status != StatusActive || current.Unavailable || current.LastError != nil || current.StatusMessage != "" {
		t.Fatalf("stale result changed replacement auth: %#v", current)
	}
	if len(current.ModelStates) != 0 {
		t.Fatalf("stale result created model states: %#v", current.ModelStates)
	}
	if current.Failed != 1 {
		t.Fatalf("failed count = %d, want request counted without changing availability", current.Failed)
	}
}

func TestRecordExecutionResultAppliesStaleQuotaFailureAfterTokenRotation(t *testing.T) {
	m := NewManager(nil, nil, nil)
	oldAuth := &Auth{
		ID:       "codex-quota.json",
		Provider: "codex",
		Status:   StatusActive,
		Metadata: map[string]any{"access_token": "old-access-token"},
	}
	if _, err := m.Register(context.Background(), oldAuth); err != nil {
		t.Fatalf("Register: %v", err)
	}
	selected, _ := m.GetByID(oldAuth.ID)
	replacement := selected.Clone()
	replacement.Metadata["access_token"] = "rotated-access-token"
	if _, err := m.Update(context.Background(), replacement); err != nil {
		t.Fatalf("Update replacement: %v", err)
	}

	m.recordExecutionResult(context.Background(), Result{
		AuthID:   oldAuth.ID,
		Provider: oldAuth.Provider,
		Model:    "gpt-test",
		Error:    &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota exhausted"},
	}, selected, false)

	current, _ := m.GetByID(oldAuth.ID)
	if current == nil || current.Status != StatusError || current.LastError == nil {
		t.Fatalf("quota result from pre-rotation request was not applied: %#v", current)
	}
	state := current.ModelStates["gpt-test"]
	if state == nil || !state.Quota.Exceeded {
		t.Fatalf("quota model state missing: %#v", state)
	}
}

func TestExecuteStaleSuccessDoesNotClearReplacementCooldown(t *testing.T) {
	previousDisableCooling := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previousDisableCooling) })

	const model = "gpt-stale-success-replacement"
	executor := &blockingSuccessfulCredentialExecutor{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	m := NewManager(nil, nil, nil)
	m.RegisterExecutor(executor)
	m.SetRetryConfig(0, 0, 1)
	oldAuth := &Auth{
		ID:       "codex-stale-success.json",
		Provider: "codex",
		Status:   StatusActive,
		Metadata: map[string]any{"access_token": "old-access-token"},
	}
	if _, err := m.Register(context.Background(), oldAuth); err != nil {
		t.Fatalf("Register: %v", err)
	}
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(oldAuth.ID, oldAuth.Provider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { reg.UnregisterClient(oldAuth.ID) })
	m.RefreshSchedulerEntry(oldAuth.ID)

	type executionResult struct {
		response cliproxyexecutor.Response
		err      error
	}
	done := make(chan executionResult, 1)
	go func() {
		response, err := m.Execute(
			context.Background(),
			[]string{oldAuth.Provider},
			cliproxyexecutor.Request{Model: model},
			cliproxyexecutor.Options{},
		)
		done <- executionResult{response: response, err: err}
	}()
	select {
	case <-executor.started:
	case <-time.After(time.Second):
		t.Fatal("old credential execution did not start")
	}

	selected, ok := m.GetByID(oldAuth.ID)
	if !ok || selected == nil {
		t.Fatal("selected old credential missing")
	}
	replacement := selected.Clone()
	replacement.Metadata["access_token"] = "replacement-access-token"
	if _, err := m.Update(context.Background(), replacement); err != nil {
		t.Fatalf("Update replacement: %v", err)
	}
	currentReplacement, ok := m.GetByID(oldAuth.ID)
	if !ok || currentReplacement == nil {
		t.Fatal("replacement credential missing")
	}
	m.recordExecutionResult(context.Background(), Result{
		AuthID:   oldAuth.ID,
		Provider: oldAuth.Provider,
		Model:    model,
		Error:    &Error{HTTPStatus: http.StatusUnauthorized, Message: "replacement token unauthorized"},
	}, currentReplacement, false)

	afterReplacementFailure, _ := m.GetByID(oldAuth.ID)
	state := afterReplacementFailure.ModelStates[model]
	if state == nil || !state.Unavailable || state.Status != StatusError || state.NextRetryAfter.IsZero() {
		t.Fatalf("replacement credential did not enter cooldown: %#v", state)
	}
	if count := reg.GetModelCount(model); count != 0 {
		t.Fatalf("registry model count after replacement 401 = %d, want 0", count)
	}

	close(executor.release)
	select {
	case result := <-done:
		if result.err != nil {
			t.Fatalf("old credential execution returned error: %v", result.err)
		}
		if got := string(result.response.Payload); got != "old credential succeeded" {
			t.Fatalf("old credential response = %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("old credential execution did not finish")
	}

	current, ok := m.GetByID(oldAuth.ID)
	if !ok || current == nil {
		t.Fatal("replacement credential missing after stale success")
	}
	if got := authAccessToken(current); got != "replacement-access-token" {
		t.Fatalf("current access token = %q, want replacement token", got)
	}
	if current.Status != StatusError || !current.Unavailable || current.LastError == nil {
		t.Fatalf("stale old success cleared replacement auth cooldown: %#v", current)
	}
	state = current.ModelStates[model]
	if state == nil || !state.Unavailable || state.Status != StatusError || state.LastError == nil || state.NextRetryAfter.IsZero() {
		t.Fatalf("stale old success cleared replacement model cooldown: %#v", state)
	}
	if state.LastError.HTTPStatus != http.StatusUnauthorized {
		t.Fatalf("replacement model error status = %d, want 401", state.LastError.HTTPStatus)
	}
	if count := reg.GetModelCount(model); count != 0 {
		t.Fatalf("stale old success resumed replacement registry model; count = %d", count)
	}
	if current.Success != 1 || current.Failed != 1 {
		t.Fatalf("request counters = success %d, failed %d; want 1 and 1", current.Success, current.Failed)
	}
}

func TestRecordExecutionResultSuppressesStaleUnauthorizedErrorEvent(t *testing.T) {
	withEnabledErrorQueue(t)
	subscriber, unsubscribe := redisqueue.SubscribeErrors()
	defer unsubscribe()

	m := NewManager(nil, nil, nil)
	oldAuth := &Auth{
		ID:       "codex-stale-event.json",
		Provider: "codex",
		Status:   StatusActive,
		Metadata: map[string]any{"access_token": "old-access-token"},
	}
	if _, err := m.Register(WithSkipPersist(context.Background()), oldAuth); err != nil {
		t.Fatalf("Register: %v", err)
	}
	selected, _ := m.GetByID(oldAuth.ID)
	replacement := selected.Clone()
	replacement.Metadata["access_token"] = "new-access-token"
	if _, err := m.Update(WithSkipPersist(context.Background()), replacement); err != nil {
		t.Fatalf("Update replacement: %v", err)
	}

	m.recordExecutionResult(context.Background(), Result{
		AuthID:   oldAuth.ID,
		Provider: oldAuth.Provider,
		Model:    "gpt-test",
		Error:    &Error{HTTPStatus: http.StatusUnauthorized, Message: "old token unauthorized"},
	}, selected, false)

	select {
	case payload := <-subscriber:
		t.Fatalf("stale unauthorized error event was published: %s", payload)
	default:
	}
}

func TestRecordExecutionResultStillAppliesToCurrentCredential(t *testing.T) {
	m := NewManager(nil, nil, nil)
	auth := &Auth{
		ID:       "codex-current.json",
		Provider: "codex",
		Status:   StatusActive,
		Metadata: map[string]any{"access_token": "current-access-token"},
	}
	if _, err := m.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register: %v", err)
	}
	selected, _ := m.GetByID(auth.ID)

	m.recordExecutionResult(context.Background(), Result{
		AuthID:   auth.ID,
		Provider: auth.Provider,
		Model:    "gpt-test",
		Error:    &Error{HTTPStatus: http.StatusUnauthorized, Message: "unauthorized"},
	}, selected, false)

	current, _ := m.GetByID(auth.ID)
	if current == nil || current.Status != StatusError || current.LastError == nil {
		t.Fatalf("current credential failure was not applied: %#v", current)
	}
}

func TestReconcileRegistryModelStatesPersistsClearedCooldown(t *testing.T) {
	store := &recordingCooldownStateStore{}
	m := NewManager(nil, nil, nil)
	m.SetCooldownStateStore(store)
	now := time.Now()
	auth := &Auth{
		ID:       "codex-relogin.json",
		Provider: "codex",
		Status:   StatusError,
		Metadata: map[string]any{"access_token": "new-token"},
		ModelStates: map[string]*ModelState{
			"gpt-test": {
				Unavailable:    true,
				Status:         StatusError,
				StatusMessage:  "unauthorized",
				NextRetryAfter: now.Add(30 * time.Minute),
				LastError:      &Error{HTTPStatus: http.StatusUnauthorized, Message: "unauthorized"},
				UpdatedAt:      now,
			},
		},
	}
	if _, err := m.Register(WithSkipPersist(context.Background()), auth); err != nil {
		t.Fatalf("Register: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "gpt-test"}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })

	m.persistCooldownStates(context.Background())
	store.mu.Lock()
	before := store.saveCount.Load()
	beforeRecords := cloneCooldownStateRecords(store.records)
	store.mu.Unlock()
	if len(beforeRecords) == 0 {
		t.Fatal("expected persisted unauthorized cooldown before reconciliation")
	}

	m.ReconcileRegistryModelStates(context.Background(), auth.ID)

	store.mu.Lock()
	after := store.saveCount.Load()
	afterRecords := cloneCooldownStateRecords(store.records)
	store.mu.Unlock()
	if after <= before {
		t.Fatalf("cooldown saves = %d, want greater than %d after reconciliation", after, before)
	}
	if len(afterRecords) != 0 {
		t.Fatalf("persisted cooldown records = %#v, want cleared", afterRecords)
	}
}

type blockingCredentialRefreshExecutor struct {
	identifier           string
	started              chan struct{}
	release              chan struct{}
	refreshedAccessToken string
	refreshErr           error
	mutate               func(*Auth)
}

func (e *blockingCredentialRefreshExecutor) Identifier() string {
	if e.identifier != "" {
		return e.identifier
	}
	return "codex"
}

func (e *blockingCredentialRefreshExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *blockingCredentialRefreshExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, &Error{HTTPStatus: http.StatusNotImplemented, Message: "not implemented"}
}

func (e *blockingCredentialRefreshExecutor) Refresh(ctx context.Context, auth *Auth) (*Auth, error) {
	close(e.started)
	select {
	case <-e.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if e.refreshErr != nil {
		return nil, e.refreshErr
	}
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	auth.Metadata["access_token"] = e.refreshedAccessToken
	if e.mutate != nil {
		e.mutate(auth)
	}
	return auth, nil
}

func (e *blockingCredentialRefreshExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *blockingCredentialRefreshExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, &Error{HTTPStatus: http.StatusNotImplemented, Message: "not implemented"}
}

func TestRefreshAuthForRequestDiscardsStaleResultAfterCredentialReplacement(t *testing.T) {
	tests := []struct {
		name           string
		refreshedToken string
		refreshErr     error
	}{
		{
			name:           "successful old refresh",
			refreshedToken: "refreshed-old-access-token",
		},
		{
			name:       "failed old refresh",
			refreshErr: &Error{HTTPStatus: http.StatusUnauthorized, Message: "old refresh token unauthorized"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			executor := &blockingCredentialRefreshExecutor{
				started:              make(chan struct{}),
				release:              make(chan struct{}),
				refreshedAccessToken: tt.refreshedToken,
				refreshErr:           tt.refreshErr,
			}
			m := NewManager(nil, nil, nil)
			m.RegisterExecutor(executor)
			auth := &Auth{
				ID:       "codex-refresh-race.json",
				Provider: "codex",
				Status:   StatusActive,
				Metadata: map[string]any{
					"access_token":  "old-access-token",
					"refresh_token": "old-refresh-token",
				},
			}
			if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
				t.Fatalf("Register: %v", errRegister)
			}

			type refreshResult struct {
				auth *Auth
				err  error
			}
			done := make(chan refreshResult, 1)
			go func() {
				refreshed, errRefresh := m.refreshAuthForRequest(context.Background(), auth.ID, "old-access-token")
				done <- refreshResult{auth: refreshed, err: errRefresh}
			}()

			select {
			case <-executor.started:
			case <-time.After(time.Second):
				t.Fatal("refresh did not start")
			}

			current, okCurrent := m.GetByID(auth.ID)
			if !okCurrent || current == nil {
				t.Fatal("current auth missing before replacement")
			}
			replacement := current.Clone()
			replacement.Metadata["access_token"] = "replacement-access-token"
			replacement.Metadata["refresh_token"] = "replacement-refresh-token"
			if _, errUpdate := m.Update(context.Background(), replacement); errUpdate != nil {
				t.Fatalf("Update replacement: %v", errUpdate)
			}
			close(executor.release)

			var got refreshResult
			select {
			case got = <-done:
			case <-time.After(time.Second):
				t.Fatal("refresh did not finish")
			}
			if got.err != nil {
				t.Fatalf("stale refresh returned error: %v", got.err)
			}
			if got.auth == nil || authAccessToken(got.auth) != "replacement-access-token" {
				t.Fatalf("refresh returned %#v, want replacement credential", got.auth)
			}

			current, okCurrent = m.GetByID(auth.ID)
			if !okCurrent || current == nil {
				t.Fatal("current auth missing after refresh")
			}
			if gotToken := authAccessToken(current); gotToken != "replacement-access-token" {
				t.Fatalf("current access token = %q, want replacement token", gotToken)
			}
			if gotRefresh := authMetadataString(current, "refresh_token"); gotRefresh != "replacement-refresh-token" {
				t.Fatalf("current refresh token = %q, want replacement token", gotRefresh)
			}
			if current.Status != StatusActive || current.Unavailable || current.LastError != nil || current.StatusMessage != "" {
				t.Fatalf("stale refresh changed replacement state: %#v", current)
			}
		})
	}
}

func TestRefreshAuthForRequestFreezesOpaqueCredentialRevision(t *testing.T) {
	const provider = "opaque-refresh-plugin"
	executor := &blockingCredentialRefreshExecutor{
		identifier: provider,
		started:    make(chan struct{}),
		release:    make(chan struct{}),
		refreshErr: &Error{HTTPStatus: http.StatusUnauthorized, Message: "old plugin token unauthorized"},
	}
	m := NewManager(nil, nil, nil)
	m.RegisterExecutor(executor)
	storage := &rawJSONCredentialStorage{payload: []byte(`{"token":"old-token"}`)}
	auth := &Auth{ID: "opaque-refresh-race.json", Provider: provider, Status: StatusActive, Storage: storage}
	if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("Register: %v", errRegister)
	}

	type refreshResult struct {
		auth *Auth
		err  error
	}
	done := make(chan refreshResult, 1)
	go func() {
		refreshed, errRefresh := m.refreshAuthForRequest(context.Background(), auth.ID, "")
		done <- refreshResult{auth: refreshed, err: errRefresh}
	}()
	select {
	case <-executor.started:
	case <-time.After(time.Second):
		t.Fatal("refresh did not start")
	}

	storage.payload = []byte(`{"token":"new-token"}`)
	current, okCurrent := m.GetByID(auth.ID)
	if !okCurrent || current == nil {
		t.Fatal("current auth missing before replacement")
	}
	if _, errUpdate := m.Update(context.Background(), current.Clone()); errUpdate != nil {
		t.Fatalf("Update replacement: %v", errUpdate)
	}
	close(executor.release)

	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("stale refresh returned error: %v", got.err)
		}
		if got.auth == nil {
			t.Fatal("stale refresh returned nil replacement auth")
		}
	case <-time.After(time.Second):
		t.Fatal("refresh did not finish")
	}
	current, _ = m.GetByID(auth.ID)
	if current == nil || current.Status != StatusActive || current.Unavailable || current.LastError != nil {
		t.Fatalf("stale refresh changed replacement auth: %#v", current)
	}
}

func TestRefreshAuthForRequestAppliesStorageOnlyRefresh(t *testing.T) {
	const provider = "opaque-refresh-plugin"
	release := make(chan struct{})
	close(release)
	executor := &blockingCredentialRefreshExecutor{
		identifier:           provider,
		started:              make(chan struct{}),
		release:              release,
		refreshedAccessToken: "unchanged-access-token",
		mutate: func(auth *Auth) {
			auth.Storage = &rawJSONCredentialStorage{payload: []byte(`{"token":"new-token"}`)}
		},
	}
	m := NewManager(nil, nil, nil)
	m.RegisterExecutor(executor)
	auth := &Auth{
		ID:       "opaque-refresh-storage.json",
		Provider: provider,
		Status:   StatusActive,
		Storage:  &rawJSONCredentialStorage{payload: []byte(`{"token":"old-token"}`)},
		Metadata: map[string]any{"access_token": "unchanged-access-token"},
	}
	if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("Register: %v", errRegister)
	}

	refreshed, errRefresh := m.refreshAuthForRequest(context.Background(), auth.ID, "")
	if errRefresh != nil {
		t.Fatalf("refreshAuthForRequest: %v", errRefresh)
	}
	if refreshed == nil {
		t.Fatal("refreshAuthForRequest returned nil auth")
	}
	source, okSource := refreshed.Storage.(interface{ RawJSON() []byte })
	if !okSource {
		t.Fatalf("refreshed storage = %T, want RawJSON source", refreshed.Storage)
	}
	if got := string(source.RawJSON()); got != `{"token":"new-token"}` {
		t.Fatalf("refreshed storage = %s, want new plugin credential", got)
	}
}

func TestRefreshAuthForRequestMergesAcrossUnrelatedAuthUpdate(t *testing.T) {
	executor := &blockingCredentialRefreshExecutor{
		started:              make(chan struct{}),
		release:              make(chan struct{}),
		refreshedAccessToken: "refreshed-access-token",
	}
	m := NewManager(nil, nil, nil)
	m.RegisterExecutor(executor)
	auth := &Auth{
		ID:       "codex-refresh-unrelated-update.json",
		Provider: "codex",
		Status:   StatusActive,
		Prefix:   "old-prefix",
		ProxyURL: "http://proxy-old.example:8080",
		Metadata: map[string]any{
			"access_token":  "old-access-token",
			"refresh_token": "refresh-token",
			"routing_hint":  "old-hint",
		},
	}
	if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("Register: %v", errRegister)
	}

	type refreshResult struct {
		auth *Auth
		err  error
	}
	done := make(chan refreshResult, 1)
	go func() {
		refreshed, errRefresh := m.refreshAuthForRequest(context.Background(), auth.ID, "old-access-token")
		done <- refreshResult{auth: refreshed, err: errRefresh}
	}()

	select {
	case <-executor.started:
	case <-time.After(time.Second):
		t.Fatal("refresh did not start")
	}

	current, okCurrent := m.GetByID(auth.ID)
	if !okCurrent || current == nil {
		t.Fatal("current auth missing before unrelated update")
	}
	unrelated := current.Clone()
	unrelated.Prefix = "new-prefix"
	unrelated.ProxyURL = "http://proxy-new.example:8080"
	unrelated.Metadata["routing_hint"] = "new-hint"
	if _, errUpdate := m.Update(context.Background(), unrelated); errUpdate != nil {
		t.Fatalf("Update unrelated fields: %v", errUpdate)
	}
	close(executor.release)

	var got refreshResult
	select {
	case got = <-done:
	case <-time.After(time.Second):
		t.Fatal("refresh did not finish")
	}
	if got.err != nil {
		t.Fatalf("refresh error = %v", got.err)
	}
	if got.auth == nil || authAccessToken(got.auth) != "refreshed-access-token" {
		t.Fatalf("refresh returned %#v, want refreshed credential", got.auth)
	}

	current, okCurrent = m.GetByID(auth.ID)
	if !okCurrent || current == nil {
		t.Fatal("current auth missing after refresh")
	}
	if gotToken := authAccessToken(current); gotToken != "refreshed-access-token" {
		t.Fatalf("current access token = %q, want refreshed token", gotToken)
	}
	if current.Prefix != "new-prefix" || current.ProxyURL != "http://proxy-new.example:8080" {
		t.Fatalf("unrelated fields were overwritten: prefix=%q proxy=%q", current.Prefix, current.ProxyURL)
	}
	if gotHint := authMetadataString(current, "routing_hint"); gotHint != "new-hint" {
		t.Fatalf("routing_hint = %q, want unrelated update preserved", gotHint)
	}
}

func TestRefreshAuthForRequestMergesStorageAcrossEquivalentReload(t *testing.T) {
	const provider = "opaque-refresh-plugin"
	executor := &blockingCredentialRefreshExecutor{
		identifier: provider,
		started:    make(chan struct{}),
		release:    make(chan struct{}),
		mutate: func(auth *Auth) {
			auth.Storage = &fingerprintedRawJSONCredentialStorage{
				payload:  []byte(`{"token":"refreshed-token","note":"refresh-result"}`),
				material: baseauth.CredentialFingerprintMaterial{Opaque: "refreshed-token"},
			}
		},
	}
	m := NewManager(nil, nil, nil)
	m.RegisterExecutor(executor)
	auth := &Auth{
		ID:       "opaque-refresh-equivalent-reload.json",
		Provider: provider,
		Status:   StatusActive,
		ProxyURL: "http://old-proxy.example:8080",
		Metadata: map[string]any{"note": "old-note"},
		Storage: &fingerprintedRawJSONCredentialStorage{
			payload:  []byte(`{"token":"old-token","note":"old-note"}`),
			material: baseauth.CredentialFingerprintMaterial{Opaque: "old-token"},
		},
	}
	if _, err := m.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register: %v", err)
	}

	type refreshResult struct {
		auth *Auth
		err  error
	}
	refreshDone := make(chan refreshResult, 1)
	go func() {
		refreshed, err := m.refreshAuthForRequest(context.Background(), auth.ID, "")
		refreshDone <- refreshResult{auth: refreshed, err: err}
	}()
	select {
	case <-executor.started:
	case <-time.After(time.Second):
		t.Fatal("refresh did not start")
	}

	current, _ := m.GetByID(auth.ID)
	reloaded := current.Clone()
	reloaded.ProxyURL = "http://new-proxy.example:8080"
	reloaded.Metadata["note"] = "reloaded-note"
	reloaded.Storage = &fingerprintedRawJSONCredentialStorage{
		payload:  []byte(`{"token":"old-token","note":"reloaded-note"}`),
		material: baseauth.CredentialFingerprintMaterial{Opaque: "old-token"},
	}
	if _, err := m.Update(context.Background(), reloaded); err != nil {
		t.Fatalf("equivalent hot reload: %v", err)
	}
	close(executor.release)

	var result refreshResult
	select {
	case result = <-refreshDone:
	case <-time.After(time.Second):
		t.Fatal("refresh did not finish")
	}
	if result.err != nil {
		t.Fatalf("refreshAuthForRequest: %v", result.err)
	}
	if result.auth == nil {
		t.Fatal("refreshAuthForRequest returned nil auth")
	}
	source, ok := result.auth.Storage.(interface{ RawJSON() []byte })
	if !ok {
		t.Fatalf("refreshed storage = %T, want RawJSON source", result.auth.Storage)
	}
	if got := string(source.RawJSON()); got != `{"token":"refreshed-token","note":"refresh-result"}` {
		t.Fatalf("refreshed storage = %s, want refresh-returned credential", got)
	}
	if result.auth.ProxyURL != "http://new-proxy.example:8080" {
		t.Fatalf("proxy URL = %q, want concurrent reload value", result.auth.ProxyURL)
	}
	if got := authMetadataString(result.auth, "note"); got != "reloaded-note" {
		t.Fatalf("note = %q, want concurrent reload value", got)
	}
}

func TestRefreshAuthForRequestPreservesConcurrentAvailabilityState(t *testing.T) {
	executor := &blockingCredentialRefreshExecutor{
		started:              make(chan struct{}),
		release:              make(chan struct{}),
		refreshedAccessToken: "refreshed-access-token",
	}
	m := NewManager(nil, nil, nil)
	m.RegisterExecutor(executor)
	auth := &Auth{
		ID:       "codex-refresh-concurrent-state.json",
		Provider: "codex",
		Status:   StatusActive,
		Metadata: map[string]any{
			"access_token":  "old-access-token",
			"refresh_token": "refresh-token",
		},
	}
	if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("Register: %v", errRegister)
	}

	type refreshResult struct {
		auth *Auth
		err  error
	}
	done := make(chan refreshResult, 1)
	go func() {
		refreshed, errRefresh := m.refreshAuthForRequest(context.Background(), auth.ID, "old-access-token")
		done <- refreshResult{auth: refreshed, err: errRefresh}
	}()
	select {
	case <-executor.started:
	case <-time.After(time.Second):
		t.Fatal("refresh did not start")
	}

	current, okCurrent := m.GetByID(auth.ID)
	if !okCurrent || current == nil {
		t.Fatal("current auth missing before state update")
	}
	now := time.Now()
	current.Status = StatusError
	current.Unavailable = true
	current.StatusMessage = "quota exhausted"
	current.LastError = &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota exhausted"}
	current.ModelStates = map[string]*ModelState{
		"gpt-test": {
			Status:         StatusError,
			Unavailable:    true,
			StatusMessage:  "quota exhausted",
			LastError:      &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota exhausted"},
			NextRetryAfter: now.Add(time.Minute),
			Quota:          QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: now.Add(time.Minute)},
		},
	}
	if _, errUpdate := m.Update(context.Background(), current); errUpdate != nil {
		t.Fatalf("Update concurrent state: %v", errUpdate)
	}
	close(executor.release)

	var got refreshResult
	select {
	case got = <-done:
	case <-time.After(time.Second):
		t.Fatal("refresh did not finish")
	}
	if got.err != nil {
		t.Fatalf("refresh error = %v", got.err)
	}
	current, okCurrent = m.GetByID(auth.ID)
	if !okCurrent || current == nil {
		t.Fatal("current auth missing after refresh")
	}
	if gotToken := authAccessToken(current); gotToken != "refreshed-access-token" {
		t.Fatalf("current access token = %q, want refreshed token", gotToken)
	}
	if current.Status != StatusError || !current.Unavailable || current.LastError == nil || current.LastError.StatusCode() != http.StatusTooManyRequests {
		t.Fatalf("concurrent availability state was cleared: %#v", current)
	}
	state := current.ModelStates["gpt-test"]
	if state == nil || !state.Quota.Exceeded || state.LastError == nil || state.LastError.StatusCode() != http.StatusTooManyRequests {
		t.Fatalf("concurrent model state was cleared: %#v", state)
	}
}

func TestRefreshAuthForRequestAppliesRefreshReturnedScalarFields(t *testing.T) {
	executor := &blockingCredentialRefreshExecutor{
		started:              make(chan struct{}),
		release:              make(chan struct{}),
		refreshedAccessToken: "refreshed-access-token",
		mutate: func(auth *Auth) {
			auth.Prefix = "refreshed-prefix"
			auth.ProxyURL = "http://refreshed-proxy.example:8080"
			auth.Label = "refreshed-label"
			auth.Disabled = true
			auth.Unavailable = true
			auth.Status = StatusDisabled
			auth.StatusMessage = "disabled by provider refresh"
		},
	}
	m := NewManager(nil, nil, nil)
	m.RegisterExecutor(executor)
	auth := &Auth{
		ID:       "codex-refresh-scalars.json",
		Provider: "codex",
		Status:   StatusActive,
		Prefix:   "old-prefix",
		ProxyURL: "http://old-proxy.example:8080",
		Label:    "old-label",
		Metadata: map[string]any{
			"access_token":  "old-access-token",
			"refresh_token": "refresh-token",
		},
	}
	if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("Register: %v", errRegister)
	}

	type refreshResult struct {
		auth *Auth
		err  error
	}
	done := make(chan refreshResult, 1)
	go func() {
		refreshed, errRefresh := m.refreshAuthForRequest(context.Background(), auth.ID, "old-access-token")
		done <- refreshResult{auth: refreshed, err: errRefresh}
	}()
	select {
	case <-executor.started:
	case <-time.After(time.Second):
		t.Fatal("refresh did not start")
	}
	close(executor.release)

	var got refreshResult
	select {
	case got = <-done:
	case <-time.After(time.Second):
		t.Fatal("refresh did not finish")
	}
	if got.err != nil {
		t.Fatalf("refresh error = %v", got.err)
	}
	current, okCurrent := m.GetByID(auth.ID)
	if !okCurrent || current == nil {
		t.Fatal("current auth missing after refresh")
	}
	if authAccessToken(current) != "refreshed-access-token" {
		t.Fatalf("access token = %q, want refreshed token", authAccessToken(current))
	}
	if current.Prefix != "refreshed-prefix" || current.ProxyURL != "http://refreshed-proxy.example:8080" || current.Label != "refreshed-label" {
		t.Fatalf("refresh scalar fields were dropped: prefix=%q proxy=%q label=%q", current.Prefix, current.ProxyURL, current.Label)
	}
	if !current.Disabled || current.Status != StatusDisabled || current.StatusMessage != "disabled by provider refresh" {
		t.Fatalf("refresh lifecycle fields were dropped: %#v", current)
	}
}

func TestRecordExecutionResultIgnoresStaleUnauthorizedAfterAPIKeyReplacement(t *testing.T) {
	m := NewManager(nil, nil, nil)
	oldAuth := &Auth{
		ID:       "openai-api-key.json",
		Provider: "openai-compatibility",
		Status:   StatusActive,
		Attributes: map[string]string{
			AttributeAPIKey: "old-api-key",
		},
	}
	if _, errRegister := m.Register(context.Background(), oldAuth); errRegister != nil {
		t.Fatalf("Register: %v", errRegister)
	}
	selected, okSelected := m.GetByID(oldAuth.ID)
	if !okSelected || selected == nil {
		t.Fatal("selected auth missing")
	}

	replacement := selected.Clone()
	replacement.Attributes[AttributeAPIKey] = "replacement-api-key"
	if _, errUpdate := m.Update(context.Background(), replacement); errUpdate != nil {
		t.Fatalf("Update replacement: %v", errUpdate)
	}

	m.recordExecutionResult(context.Background(), Result{
		AuthID:   oldAuth.ID,
		Provider: oldAuth.Provider,
		Model:    "gpt-test",
		Error:    &Error{HTTPStatus: http.StatusUnauthorized, Message: "old API key unauthorized"},
	}, selected, false)

	current, okCurrent := m.GetByID(oldAuth.ID)
	if !okCurrent || current == nil {
		t.Fatal("current auth missing")
	}
	if gotKey := authAttribute(current, AttributeAPIKey); gotKey != "replacement-api-key" {
		t.Fatalf("current API key = %q, want replacement key", gotKey)
	}
	if current.Status != StatusActive || current.Unavailable || current.LastError != nil || current.StatusMessage != "" {
		t.Fatalf("stale API-key result changed replacement auth: %#v", current)
	}
	if len(current.ModelStates) != 0 {
		t.Fatalf("stale API-key result created model states: %#v", current.ModelStates)
	}
}

func TestRecordExecutionResultIgnoresStaleUnauthorizedAfterOAuthKindAPIKeyReplacement(t *testing.T) {
	m := NewManager(nil, nil, nil)
	oldAuth := &Auth{
		ID:       "claude-oauth-api-key.json",
		Provider: "claude",
		Status:   StatusActive,
		Attributes: map[string]string{
			AttributeAuthKind: AuthKindOAuth,
			AttributeAPIKey:   "sk-ant-oat-old",
		},
	}
	if _, errRegister := m.Register(context.Background(), oldAuth); errRegister != nil {
		t.Fatalf("Register: %v", errRegister)
	}
	selected, okSelected := m.GetByID(oldAuth.ID)
	if !okSelected || selected == nil {
		t.Fatal("selected auth missing")
	}

	replacement := selected.Clone()
	replacement.Attributes[AttributeAPIKey] = "sk-ant-oat-replacement"
	if _, errUpdate := m.Update(context.Background(), replacement); errUpdate != nil {
		t.Fatalf("Update replacement: %v", errUpdate)
	}

	m.recordExecutionResult(context.Background(), Result{
		AuthID:   oldAuth.ID,
		Provider: oldAuth.Provider,
		Model:    "claude-test",
		Error:    &Error{HTTPStatus: http.StatusUnauthorized, Message: "old OAuth-shaped API key unauthorized"},
	}, selected, false)

	current, okCurrent := m.GetByID(oldAuth.ID)
	if !okCurrent || current == nil {
		t.Fatal("current auth missing")
	}
	if gotKey := authAttribute(current, AttributeAPIKey); gotKey != "sk-ant-oat-replacement" {
		t.Fatalf("current API key = %q, want replacement key", gotKey)
	}
	if current.Status != StatusActive || current.Unavailable || current.LastError != nil || current.StatusMessage != "" {
		t.Fatalf("stale OAuth-shaped API-key result changed replacement auth: %#v", current)
	}
	if len(current.ModelStates) != 0 {
		t.Fatalf("stale OAuth-shaped API-key result created model states: %#v", current.ModelStates)
	}
}

func TestRecordExecutionResultIgnoresStaleUnauthorizedAfterCustomAuthorizationHeaderReplacement(t *testing.T) {
	m := NewManager(nil, nil, nil)
	const authID = "openai-custom-authorization.json"
	oldAuth := &Auth{
		ID:       authID,
		Provider: "openai-compatibility",
		Status:   StatusActive,
		Attributes: map[string]string{
			AttributeAPIKey:        "stable-config-key",
			"header:Authorization": "Bearer old-upstream-token",
			"header:X-Trace-ID":    "stable-trace",
		},
	}
	if _, errRegister := m.Register(context.Background(), oldAuth); errRegister != nil {
		t.Fatalf("Register: %v", errRegister)
	}
	selected, okSelected := m.GetByID(authID)
	if !okSelected || selected == nil {
		t.Fatal("selected auth missing")
	}

	replacement := selected.Clone()
	replacement.Attributes["header:Authorization"] = "Bearer replacement-upstream-token"
	if _, errUpdate := m.Update(context.Background(), replacement); errUpdate != nil {
		t.Fatalf("Update replacement: %v", errUpdate)
	}

	m.recordExecutionResult(context.Background(), Result{
		AuthID:   authID,
		Provider: oldAuth.Provider,
		Model:    "gpt-test",
		Error:    &Error{HTTPStatus: http.StatusUnauthorized, Message: "old custom header unauthorized"},
	}, selected, false)

	current, okCurrent := m.GetByID(authID)
	if !okCurrent || current == nil {
		t.Fatal("current auth missing")
	}
	if got := current.Attributes["header:Authorization"]; got != "Bearer replacement-upstream-token" {
		t.Fatalf("current Authorization header = %q, want replacement token", got)
	}
	if current.Status != StatusActive || current.Unavailable || current.LastError != nil || current.StatusMessage != "" {
		t.Fatalf("stale custom-header result changed replacement auth: %#v", current)
	}
	if len(current.ModelStates) != 0 {
		t.Fatalf("stale custom-header result created model states: %#v", current.ModelStates)
	}
}

func TestCredentialFingerprintTracksAuthenticationCustomHeaders(t *testing.T) {
	for _, header := range []string{"Authorization", "x-api-key", "X-Goog-Api-Key", "X-Auth-Token"} {
		t.Run(header, func(t *testing.T) {
			left := &Auth{
				Provider: "openai-compatibility",
				Attributes: map[string]string{
					AttributeAPIKey:    "stable-config-key",
					"header:" + header: "credential-a",
				},
			}
			right := left.Clone()
			right.Attributes["header:"+header] = "credential-b"

			leftFingerprint, leftOK := authCredentialFingerprint(left)
			rightFingerprint, rightOK := authCredentialFingerprint(right)
			if !leftOK || !rightOK {
				t.Fatalf("credential fingerprint availability = left:%v right:%v", leftOK, rightOK)
			}
			if leftFingerprint == rightFingerprint {
				t.Fatalf("%s change did not change credential fingerprint", header)
			}
		})
	}

	left := &Auth{
		Provider: "openai-compatibility",
		Attributes: map[string]string{
			AttributeAPIKey:        "stable-config-key",
			"header:Authorization": "Bearer same-token",
			"header:X-API-Key":     "same-api-key",
		},
	}
	right := &Auth{
		Provider: "openai-compatibility",
		Attributes: map[string]string{
			AttributeAPIKey:        "stable-config-key",
			"header:authorization": "Bearer same-token",
			"header:x-api-key":     "same-api-key",
		},
	}
	leftFingerprint, leftOK := authCredentialFingerprint(left)
	rightFingerprint, rightOK := authCredentialFingerprint(right)
	if !leftOK || !rightOK {
		t.Fatalf("credential fingerprint availability = left:%v right:%v", leftOK, rightOK)
	}
	if leftFingerprint != rightFingerprint {
		t.Fatal("header-name casing changed credential fingerprint")
	}
}

func TestCredentialFingerprintIgnoresNonCredentialCustomHeaders(t *testing.T) {
	left := &Auth{
		Provider: "openai-compatibility",
		Attributes: map[string]string{
			AttributeAPIKey:     "stable-config-key",
			"header:X-Trace-ID": "trace-a",
		},
	}
	right := left.Clone()
	right.Attributes["header:X-Trace-ID"] = "trace-b"

	leftFingerprint, leftOK := authCredentialFingerprint(left)
	rightFingerprint, rightOK := authCredentialFingerprint(right)
	if !leftOK || !rightOK {
		t.Fatalf("credential fingerprint availability = left:%v right:%v", leftOK, rightOK)
	}
	if leftFingerprint != rightFingerprint {
		t.Fatal("noncredential custom header changed credential fingerprint")
	}
}

func TestCredentialFingerprintCanonicalizesRawStorageJSON(t *testing.T) {
	left := &Auth{
		Provider: "plugin-provider",
		Storage:  &rawJSONCredentialStorage{payload: []byte(`{"token":"same-token","nested":{"b":2,"a":1}}`)},
	}
	right := &Auth{
		Provider: "plugin-provider",
		Storage:  &rawJSONCredentialStorage{payload: []byte(" { \n  \"nested\": {\"a\": 1, \"b\": 2}, \n  \"token\": \"same-token\" \n} ")},
	}

	leftFingerprint, leftOK := authCredentialFingerprint(left)
	rightFingerprint, rightOK := authCredentialFingerprint(right)
	if !leftOK || !rightOK {
		t.Fatalf("raw storage fingerprint availability = left:%v right:%v", leftOK, rightOK)
	}
	if leftFingerprint != rightFingerprint {
		t.Fatalf("equivalent raw JSON fingerprints differ: %x != %x", leftFingerprint, rightFingerprint)
	}
}

func TestRecordExecutionResultIgnoresStaleUnauthorizedAfterOpaquePluginCredentialReplacement(t *testing.T) {
	m := NewManager(nil, nil, nil)
	const authID = "plugin-opaque-credential.json"
	oldAuth := &Auth{
		ID:       authID,
		Provider: "plugin-provider",
		Status:   StatusActive,
		Metadata: map[string]any{"type": "plugin-provider", "token": "old-token"},
		Storage: &rawJSONCredentialStorage{payload: []byte(
			`{"type":"plugin-provider","token":"old-token","scope":"shared"}`,
		)},
	}
	if _, errRegister := m.Register(context.Background(), oldAuth); errRegister != nil {
		t.Fatalf("Register: %v", errRegister)
	}
	selected, okSelected := m.GetByID(authID)
	if !okSelected || selected == nil {
		t.Fatal("selected plugin auth missing")
	}

	replacement := &Auth{
		ID:       authID,
		Provider: "plugin-provider",
		Status:   StatusActive,
		Metadata: map[string]any{"type": "plugin-provider", "token": "new-token"},
		Storage: &rawJSONCredentialStorage{payload: []byte(
			`{"scope":"shared","token":"new-token","type":"plugin-provider"}`,
		)},
	}
	if _, errUpdate := m.Update(context.Background(), replacement); errUpdate != nil {
		t.Fatalf("Update replacement: %v", errUpdate)
	}

	m.recordExecutionResult(context.Background(), Result{
		AuthID:   authID,
		Provider: "plugin-provider",
		Model:    "plugin-model",
		Error:    &Error{HTTPStatus: http.StatusUnauthorized, Message: "old plugin token unauthorized"},
	}, selected, false)

	current, okCurrent := m.GetByID(authID)
	if !okCurrent || current == nil {
		t.Fatal("current plugin auth missing")
	}
	if gotToken, _ := current.Metadata["token"].(string); gotToken != "new-token" {
		t.Fatalf("current plugin token = %q, want new-token", gotToken)
	}
	if current.Status != StatusActive || current.Unavailable || current.LastError != nil || current.StatusMessage != "" {
		t.Fatalf("stale plugin unauthorized result changed replacement auth: %#v", current)
	}
	if len(current.ModelStates) != 0 {
		t.Fatalf("stale plugin unauthorized result created model states: %#v", current.ModelStates)
	}
}

func TestRecordExecutionResultIgnoresStaleUnauthorizedAfterVertexServiceAccountReplacement(t *testing.T) {
	m := NewManager(nil, nil, nil)
	const authID = "vertex-shared-project.json"
	oldAuth := &Auth{
		ID:       authID,
		Provider: "vertex",
		Status:   StatusActive,
		Metadata: map[string]any{
			"type":       "vertex",
			"project_id": "shared-project",
			"service_account": map[string]any{
				"type":           "service_account",
				"project_id":     "shared-project",
				"client_email":   "vertex@example.com",
				"private_key_id": "old-key-id",
				"private_key":    "old-private-key",
			},
		},
	}
	if _, errRegister := m.Register(context.Background(), oldAuth); errRegister != nil {
		t.Fatalf("Register: %v", errRegister)
	}
	selected, okSelected := m.GetByID(authID)
	if !okSelected || selected == nil {
		t.Fatal("selected Vertex auth missing")
	}

	replacement := oldAuth.Clone()
	replacement.Metadata["service_account"] = map[string]any{
		"type":           "service_account",
		"project_id":     "shared-project",
		"client_email":   "vertex@example.com",
		"private_key_id": "new-key-id",
		"private_key":    "new-private-key",
	}
	if _, errUpdate := m.Update(context.Background(), replacement); errUpdate != nil {
		t.Fatalf("Update replacement: %v", errUpdate)
	}

	m.recordExecutionResult(context.Background(), Result{
		AuthID:   authID,
		Provider: "vertex",
		Model:    "gemini-test",
		Error:    &Error{HTTPStatus: http.StatusUnauthorized, Message: "old Vertex key unauthorized"},
	}, selected, false)

	current, okCurrent := m.GetByID(authID)
	if !okCurrent || current == nil {
		t.Fatal("current Vertex auth missing")
	}
	if current.Status != StatusActive || current.Unavailable || current.LastError != nil || current.StatusMessage != "" {
		t.Fatalf("stale Vertex unauthorized result changed replacement auth: %#v", current)
	}
	if len(current.ModelStates) != 0 {
		t.Fatalf("stale Vertex unauthorized result created model states: %#v", current.ModelStates)
	}
}

func TestRecordExecutionResultIgnoresStaleUnauthorizedWhenReplacementFingerprintMissing(t *testing.T) {
	m := NewManager(nil, nil, nil)
	oldAuth := &Auth{
		ID:       "opaque-replacement.json",
		Provider: "codex",
		Status:   StatusActive,
		Metadata: map[string]any{
			"type":         "codex",
			"access_token": "old-access-token",
		},
	}
	if _, errRegister := m.Register(context.Background(), oldAuth); errRegister != nil {
		t.Fatalf("Register: %v", errRegister)
	}
	selected, okSelected := m.GetByID(oldAuth.ID)
	if !okSelected || selected == nil {
		t.Fatal("selected auth missing")
	}
	if _, okFingerprint := authCredentialFingerprint(selected); !okFingerprint {
		t.Fatal("selected auth lacks credential fingerprint")
	}

	replacement := &Auth{
		ID:       oldAuth.ID,
		Provider: oldAuth.Provider,
		Status:   StatusActive,
		Metadata: map[string]any{"email": "replacement@example.com"},
		Storage:  &opaqueCredentialStorage{},
	}
	if _, okFingerprint := authCredentialFingerprint(replacement); okFingerprint {
		t.Fatal("replacement unexpectedly has a credential fingerprint")
	}
	if _, errUpdate := m.Update(context.Background(), replacement); errUpdate != nil {
		t.Fatalf("Update replacement: %v", errUpdate)
	}

	m.recordExecutionResult(context.Background(), Result{
		AuthID:   oldAuth.ID,
		Provider: oldAuth.Provider,
		Model:    "gpt-test",
		Error:    &Error{HTTPStatus: http.StatusUnauthorized, Message: "old credential unauthorized"},
	}, selected, false)

	current, okCurrent := m.GetByID(oldAuth.ID)
	if !okCurrent || current == nil {
		t.Fatal("current auth missing")
	}
	if current.Status != StatusActive || current.Unavailable || current.LastError != nil || current.StatusMessage != "" {
		t.Fatalf("stale unauthorized result changed replacement auth: %#v", current)
	}
	if len(current.ModelStates) != 0 {
		t.Fatalf("stale unauthorized result created model states: %#v", current.ModelStates)
	}
}

func TestCredentialFingerprintMatchesStorageAndMetadataOAuth(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		storage  any
	}{
		{
			name:     "Claude",
			provider: "claude",
			storage: &claudeauth.ClaudeTokenStorage{
				AccessToken:  "access-token",
				RefreshToken: "refresh-token",
				IDToken:      "id-token",
			},
		},
		{
			name:     "Codex",
			provider: "codex",
			storage: &codexauth.CodexTokenStorage{
				AccessToken:  "access-token",
				RefreshToken: "refresh-token",
				IDToken:      "id-token",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			storageAuth := &Auth{
				Provider: tt.provider,
				Metadata: map[string]any{"email": "user@example.com"},
			}
			switch storage := tt.storage.(type) {
			case *claudeauth.ClaudeTokenStorage:
				storageAuth.Storage = storage
			case *codexauth.CodexTokenStorage:
				storageAuth.Storage = storage
			default:
				t.Fatalf("unsupported test storage %T", storage)
			}
			metadataAuth := &Auth{
				Provider: tt.provider,
				Metadata: map[string]any{
					"type":          tt.provider,
					"access_token":  "access-token",
					"refresh_token": "refresh-token",
					"id_token":      "id-token",
				},
			}

			storageFingerprint, okStorage := authCredentialFingerprint(storageAuth)
			metadataFingerprint, okMetadata := authCredentialFingerprint(metadataAuth)
			if !okStorage || !okMetadata {
				t.Fatalf("fingerprint availability = storage:%v metadata:%v", okStorage, okMetadata)
			}
			if storageFingerprint != metadataFingerprint {
				t.Fatalf("storage and metadata fingerprints differ: %x != %x", storageFingerprint, metadataFingerprint)
			}
		})
	}
}

func TestRecordExecutionResultIgnoresStaleUnauthorizedAfterStorageBackedOAuthReplacement(t *testing.T) {
	tests := []struct {
		name       string
		provider   string
		newStorage any
	}{
		{
			name:     "Claude",
			provider: "claude",
			newStorage: &claudeauth.ClaudeTokenStorage{
				AccessToken:  "new-access-token",
				RefreshToken: "new-refresh-token",
				IDToken:      "new-id-token",
			},
		},
		{
			name:     "Codex",
			provider: "codex",
			newStorage: &codexauth.CodexTokenStorage{
				AccessToken:  "new-access-token",
				RefreshToken: "new-refresh-token",
				IDToken:      "new-id-token",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := NewManager(nil, nil, nil)
			oldAuth := &Auth{
				ID:       tt.provider + "-storage-relogin.json",
				Provider: tt.provider,
				Status:   StatusActive,
				Metadata: map[string]any{
					"type":          tt.provider,
					"access_token":  "old-access-token",
					"refresh_token": "old-refresh-token",
					"id_token":      "old-id-token",
				},
			}
			if _, errRegister := m.Register(context.Background(), oldAuth); errRegister != nil {
				t.Fatalf("Register: %v", errRegister)
			}
			selected, okSelected := m.GetByID(oldAuth.ID)
			if !okSelected || selected == nil {
				t.Fatal("selected auth missing")
			}

			replacement := &Auth{
				ID:       oldAuth.ID,
				Provider: tt.provider,
				Status:   StatusActive,
				Metadata: map[string]any{"email": "user@example.com"},
			}
			switch storage := tt.newStorage.(type) {
			case *claudeauth.ClaudeTokenStorage:
				replacement.Storage = storage
			case *codexauth.CodexTokenStorage:
				replacement.Storage = storage
			default:
				t.Fatalf("unsupported test storage %T", storage)
			}
			if _, errUpdate := m.Update(context.Background(), replacement); errUpdate != nil {
				t.Fatalf("Update replacement: %v", errUpdate)
			}

			m.recordExecutionResult(context.Background(), Result{
				AuthID:   oldAuth.ID,
				Provider: tt.provider,
				Model:    "model-test",
				Error:    &Error{HTTPStatus: http.StatusUnauthorized, Message: "old credential unauthorized"},
			}, selected, false)

			current, okCurrent := m.GetByID(oldAuth.ID)
			if !okCurrent || current == nil {
				t.Fatal("current auth missing")
			}
			if current.Status != StatusActive || current.Unavailable || current.LastError != nil || current.StatusMessage != "" {
				t.Fatalf("stale storage-backed result changed replacement auth: %#v", current)
			}
			if len(current.ModelStates) != 0 {
				t.Fatalf("stale storage-backed result created model states: %#v", current.ModelStates)
			}
		})
	}
}
