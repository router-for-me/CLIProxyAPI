package cliproxy

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/watcher"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/watcher/synthesizer"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

type issue8556BlockingSaveStore struct {
	targetID    string
	saveEntered chan struct{}
	releaseSave chan struct{}
	enteredOnce sync.Once
}

func (s *issue8556BlockingSaveStore) List(context.Context) ([]*coreauth.Auth, error) {
	return nil, nil
}

func (s *issue8556BlockingSaveStore) Save(_ context.Context, auth *coreauth.Auth) (string, error) {
	if auth != nil && auth.ID == s.targetID {
		s.enteredOnce.Do(func() {
			close(s.saveEntered)
		})
		<-s.releaseSave
	}
	return "", nil
}

func (s *issue8556BlockingSaveStore) Delete(context.Context, string) error {
	return nil
}

func waitForGoroutineInStack(t *testing.T, mustContain ...string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	buf := make([]byte, 1<<20)
	for time.Now().Before(deadline) {
		n := runtime.Stack(buf, true)
		stacks := bytes.Split(buf[:n], []byte("\n\n"))
		for _, stack := range stacks {
			matched := true
			for _, token := range mustContain {
				if !bytes.Contains(stack, []byte(token)) {
					matched = false
					break
				}
			}
			if matched {
				return
			}
		}
		runtime.Gosched()
	}
	t.Fatalf("timed out waiting for goroutine stack containing %v", mustContain)
}

func synthesizeIssue8556Snapshot(t *testing.T, dir, path, raw string) *coreauth.Auth {
	t.Helper()
	if errWrite := os.WriteFile(path, []byte(raw), 0o600); errWrite != nil {
		t.Fatalf("write auth file %s: %v", path, errWrite)
	}
	auths, errSynth := synthesizer.SynthesizeAuthFile(&synthesizer.SynthesisContext{
		Config:      &internalconfig.Config{},
		AuthDir:     dir,
		Now:         time.Now(),
		IDGenerator: synthesizer.NewStableIDGenerator(),
	}, path, []byte(raw))
	if errSynth != nil || len(auths) != 1 {
		t.Fatalf("SynthesizeAuthFile(%s) = (%v, %v), want 1 auth", path, auths, errSynth)
	}
	return auths[0]
}

func newIssue8556Service(t *testing.T, dir string, store coreauth.Store) (*Service, *coreauth.Manager, func() *coreauth.Auth) {
	t.Helper()
	manager := coreauth.NewManager(store, nil, nil)
	service, errBuild := NewBuilder().
		WithConfig(&internalconfig.Config{AuthDir: dir}).
		WithConfigPath(filepath.Join(dir, "config.yaml")).
		WithCoreAuthManager(manager).
		Build()
	if errBuild != nil {
		t.Fatalf("build service: %v", errBuild)
	}
	var (
		rereadMu sync.Mutex
		reread   *coreauth.Auth
	)
	service.watcher = &WatcherWrapper{
		rereadDiscardedFileSnapshot: func(auth *coreauth.Auth) {
			rereadMu.Lock()
			reread = auth
			rereadMu.Unlock()
		},
	}
	getReread := func() *coreauth.Auth {
		rereadMu.Lock()
		defer rereadMu.Unlock()
		return reread
	}
	return service, manager, getReread
}

func TestHandleAuthUpdates_AbsentWatcherSnapshotCannotResurrectDeletedAuth_Issue8556(t *testing.T) {
	dir := t.TempDir()
	service, manager, getReread := newIssue8556Service(t, dir, nil)

	id := "issue8556-deleted.json"
	reg := GlobalModelRegistry()
	reg.UnregisterClient(id)
	t.Cleanup(func() { reg.UnregisterClient(id) })

	path := filepath.Join(dir, id)
	oldRaw := `{"type":"claude","email":"old@example.com","access_token":"old-token"}`
	staleSnapshot := synthesizeIssue8556Snapshot(t, dir, path, oldRaw)

	// Simulate an upload registering the auth followed by a delete removing both
	// the backing file and the runtime entry after the watcher captured staleSnapshot.
	ctx := coreauth.WithSkipPersist(context.Background())
	uploaded := staleSnapshot.Clone()
	uploaded.ClearFileSnapshot()
	uploaded.Metadata["access_token"] = "uploaded-token"
	if _, errReg := manager.Register(ctx, uploaded); errReg != nil {
		t.Fatalf("register uploaded auth: %v", errReg)
	}
	if errRemove := os.Remove(path); errRemove != nil {
		t.Fatalf("remove auth file: %v", errRemove)
	}
	manager.Remove(ctx, id)
	reg.UnregisterClient(id)

	service.handleAuthUpdates(ctx, []watcher.AuthUpdate{{
		Action: watcher.AuthUpdateActionAdd,
		ID:     id,
		Auth:   staleSnapshot,
	}})

	if got, ok := manager.GetByID(id); ok || got != nil {
		t.Fatalf("deleted auth was resurrected in coreManager: %+v", got)
	}
	if models := reg.GetModelsForClient(id); len(models) != 0 {
		t.Fatalf("deleted auth registered %d model(s) in GlobalModelRegistry", len(models))
	}
	if reread := getReread(); reread == nil || !reread.SameFileSnapshot(staleSnapshot) {
		t.Fatalf("reread = %+v, want discarded snapshot for %s", reread, id)
	}
}

func TestHandleAuthUpdates_AbsentWatcherSnapshotDroppedWhenFileChanged_Issue8556(t *testing.T) {
	dir := t.TempDir()
	service, manager, getReread := newIssue8556Service(t, dir, nil)

	id := "issue8556-changed.json"
	reg := GlobalModelRegistry()
	reg.UnregisterClient(id)
	t.Cleanup(func() { reg.UnregisterClient(id) })

	path := filepath.Join(dir, id)
	oldRaw := `{"type":"claude","email":"user@example.com","access_token":"old-token"}`
	newRaw := `{"type":"claude","email":"user@example.com","access_token":"new-token"}`
	staleSnapshot := synthesizeIssue8556Snapshot(t, dir, path, oldRaw)
	if errWrite := os.WriteFile(path, []byte(newRaw), 0o600); errWrite != nil {
		t.Fatalf("overwrite auth file: %v", errWrite)
	}

	ctx := coreauth.WithSkipPersist(context.Background())
	service.handleAuthUpdates(ctx, []watcher.AuthUpdate{{
		Action: watcher.AuthUpdateActionAdd,
		ID:     id,
		Auth:   staleSnapshot,
	}})

	if got, ok := manager.GetByID(id); ok || got != nil {
		t.Fatalf("stale snapshot registered mismatched auth in coreManager: %+v", got)
	}
	if models := reg.GetModelsForClient(id); len(models) != 0 {
		t.Fatalf("stale snapshot registered %d model(s) in GlobalModelRegistry", len(models))
	}
	if reread := getReread(); reread == nil || !reread.SameFileSnapshot(staleSnapshot) {
		t.Fatalf("reread = %+v, want discarded snapshot for %s", reread, id)
	}
}

func TestHandleAuthUpdates_AbsentWatcherSnapshotCannotOverwriteConcurrentUpload_Issue8556(t *testing.T) {
	dir := t.TempDir()
	id := "issue8556-upload-race.json"
	reg := GlobalModelRegistry()
	reg.UnregisterClient(id)
	t.Cleanup(func() { reg.UnregisterClient(id) })

	store := &issue8556BlockingSaveStore{
		targetID:    id,
		saveEntered: make(chan struct{}),
		releaseSave: make(chan struct{}),
	}
	service, manager, getReread := newIssue8556Service(t, dir, store)

	path := filepath.Join(dir, id)
	oldRaw := `{"type":"claude","email":"user@example.com","access_token":"old-token","prefix":"old-pfx"}`
	newRaw := `{"type":"claude","email":"user@example.com","access_token":"new-token","prefix":"new-pfx"}`
	staleSnapshot := synthesizeIssue8556Snapshot(t, dir, path, oldRaw)
	uploadedAuth := synthesizeIssue8556Snapshot(t, dir, path, newRaw)
	uploadedAuth.ClearFileSnapshot()

	// Start the upload Register and pause inside Store.Save while holding the
	// per-auth mutation lock and before m.auths[id] is published.
	uploadDone := make(chan error, 1)
	go func() {
		_, errReg := manager.Register(context.Background(), uploadedAuth)
		uploadDone <- errReg
	}()
	select {
	case <-store.saveEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for upload Register to enter Store.Save")
	}

	// Start watcher handling while manager.GetByID(id) still returns false.
	watcherDone := make(chan struct{})
	go func() {
		defer close(watcherDone)
		service.handleAuthUpdates(coreauth.WithSkipPersist(context.Background()), []watcher.AuthUpdate{{
			Action: watcher.AuthUpdateActionAdd,
			ID:     id,
			Auth:   staleSnapshot,
		}})
	}()

	// Wait until the watcher goroutine has passed GetByID and is waiting on
	// the per-auth mutation lock held by the upload.
	waitForGoroutineInStack(t, "prepareCoreAuthForModelRegistration", "lockAuthMutationContext")

	// Let the upload finish Register and publish the newer auth.
	close(store.releaseSave)
	select {
	case errUpload := <-uploadDone:
		if errUpload != nil {
			t.Fatalf("upload Register failed: %v", errUpload)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for upload Register to complete")
	}
	uploadedEpoch := uploadedAuth.RegistrationEpoch
	if uploadedEpoch == 0 {
		t.Fatal("expected upload Register to assign a non-zero RegistrationEpoch")
	}

	select {
	case <-watcherDone:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for watcher handleAuthUpdates to complete")
	}

	gotAfterWatcher, ok := manager.GetByID(id)
	if !ok || gotAfterWatcher == nil {
		t.Fatal("uploaded auth missing after watcher update")
	}
	if gotAfterWatcher.RegistrationEpoch != uploadedEpoch {
		t.Fatalf("RegistrationEpoch = %d, want %d (stale snapshot must not bump epoch)", gotAfterWatcher.RegistrationEpoch, uploadedEpoch)
	}
	if token, _ := gotAfterWatcher.Metadata["access_token"].(string); token != "new-token" {
		t.Fatalf("access_token = %q, want %q (stale snapshot overwrote uploaded auth)", token, "new-token")
	}
	if gotAfterWatcher.Prefix != "new-pfx" {
		t.Fatalf("prefix = %q, want %q", gotAfterWatcher.Prefix, "new-pfx")
	}
	if reread := getReread(); reread == nil || !reread.SameFileSnapshot(staleSnapshot) {
		t.Fatalf("reread = %+v, want discarded snapshot for %s", reread, id)
	}

	// Verify the upload's own post-persist sync update is not dropped as stale.
	uploadedUpdate := uploadedAuth.Clone()
	uploadedUpdate.Prefix = "post-sync-pfx"
	service.handleAuthUpdate(coreauth.WithSkipPersist(context.Background()), watcher.AuthUpdate{
		Action: watcher.AuthUpdateActionModify,
		ID:     id,
		Auth:   uploadedUpdate,
	})
	gotAfterSync, ok := manager.GetByID(id)
	if !ok || gotAfterSync == nil {
		t.Fatal("auth missing after upload sync update")
	}
	if gotAfterSync.Prefix != "post-sync-pfx" {
		t.Fatalf("upload post-persist sync was dropped as stale: prefix = %q, want %q", gotAfterSync.Prefix, "post-sync-pfx")
	}
}

func TestHandleAuthUpdates_AbsentWatcherSnapshotMatchingConcurrentUploadDoesNotBumpEpoch_Issue8556(t *testing.T) {
	dir := t.TempDir()
	id := "issue8556-same-file-race.json"
	reg := GlobalModelRegistry()
	reg.UnregisterClient(id)
	t.Cleanup(func() { reg.UnregisterClient(id) })

	store := &issue8556BlockingSaveStore{
		targetID:    id,
		saveEntered: make(chan struct{}),
		releaseSave: make(chan struct{}),
	}
	service, manager, _ := newIssue8556Service(t, dir, store)

	path := filepath.Join(dir, id)
	raw := `{"type":"claude","email":"user@example.com","access_token":"same-token","prefix":"pfx-1"}`
	matchingSnapshot := synthesizeIssue8556Snapshot(t, dir, path, raw)
	uploadedAuth := matchingSnapshot.Clone()
	uploadedAuth.ClearFileSnapshot()

	uploadDone := make(chan error, 1)
	go func() {
		_, errReg := manager.Register(context.Background(), uploadedAuth)
		uploadDone <- errReg
	}()
	select {
	case <-store.saveEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for upload Register to enter Store.Save")
	}

	watcherDone := make(chan struct{})
	go func() {
		defer close(watcherDone)
		service.handleAuthUpdates(coreauth.WithSkipPersist(context.Background()), []watcher.AuthUpdate{{
			Action: watcher.AuthUpdateActionAdd,
			ID:     id,
			Auth:   matchingSnapshot,
		}})
	}()

	waitForGoroutineInStack(t, "prepareCoreAuthForModelRegistration", "lockAuthMutationContext")

	close(store.releaseSave)
	select {
	case errUpload := <-uploadDone:
		if errUpload != nil {
			t.Fatalf("upload Register failed: %v", errUpload)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for upload Register to complete")
	}
	uploadedEpoch := uploadedAuth.RegistrationEpoch

	select {
	case <-watcherDone:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for watcher handleAuthUpdates to complete")
	}

	got, ok := manager.GetByID(id)
	if !ok || got == nil {
		t.Fatal("auth missing after matching snapshot race")
	}
	if got.RegistrationEpoch != uploadedEpoch {
		t.Fatalf("RegistrationEpoch = %d, want %d (must apply as update, not re-register)", got.RegistrationEpoch, uploadedEpoch)
	}
}

func TestHandleAuthUpdates_AbsentWatcherSnapshotRegistersWhenStillAbsentAndMatchingDisk_Issue8556(t *testing.T) {
	dir := t.TempDir()
	service, manager, getReread := newIssue8556Service(t, dir, nil)

	id := "issue8556-fresh.json"
	reg := GlobalModelRegistry()
	reg.UnregisterClient(id)
	t.Cleanup(func() { reg.UnregisterClient(id) })

	path := filepath.Join(dir, id)
	raw := `{"type":"claude","email":"fresh@example.com","access_token":"fresh-token"}`
	snapshot := synthesizeIssue8556Snapshot(t, dir, path, raw)

	ctx := coreauth.WithSkipPersist(context.Background())
	service.handleAuthUpdates(ctx, []watcher.AuthUpdate{{
		Action: watcher.AuthUpdateActionAdd,
		ID:     id,
		Auth:   snapshot,
	}})

	got, ok := manager.GetByID(id)
	if !ok || got == nil {
		t.Fatal("fresh matching snapshot was not registered")
	}
	if token, _ := got.Metadata["access_token"].(string); token != "fresh-token" {
		t.Fatalf("access_token = %q, want %q", token, "fresh-token")
	}
	if models := reg.GetModelsForClient(id); len(models) == 0 {
		t.Fatal("expected models to be registered for fresh matching auth")
	}
	if reread := getReread(); reread != nil {
		t.Fatalf("unexpected reread for fresh matching snapshot: %+v", reread)
	}
}
