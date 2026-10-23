package auth_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/watcher/synthesizer"
	sdkauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

const trafficAuthID = "traffic.json"

// afterSaveStore is a FileTokenStore that runs afterSave once, right after the next
// store write; failNext fails the next write instead.
type afterSaveStore struct {
	*sdkauth.FileTokenStore
	afterSave func()
	failNext  error
}

func (s *afterSaveStore) Save(ctx context.Context, auth *coreauth.Auth) (string, error) {
	if errFail := s.failNext; errFail != nil {
		s.failNext = nil
		return "", errFail
	}
	path, errSave := s.FileTokenStore.Save(ctx, auth)
	if hook := s.afterSave; hook != nil {
		s.afterSave = nil
		hook()
	}
	return path, errSave
}

// newTrafficManager returns a manager on a real file store with one registered auth file.
func newTrafficManager(t *testing.T) (*coreauth.Manager, *afterSaveStore, string) {
	t.Helper()
	dir := t.TempDir()
	store := &afterSaveStore{FileTokenStore: sdkauth.NewFileTokenStore()}
	store.SetBaseDir(dir)
	manager := coreauth.NewManager(store, nil, nil)
	path := filepath.Join(dir, trafficAuthID)
	if errWrite := os.WriteFile(path, []byte(`{"type":"claude","email":"a@b.c","prefix":"pfx-1"}`), 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	if _, errRegister := manager.Register(context.Background(), readTrafficSnapshot(t, path, "old-model")); errRegister != nil {
		t.Fatalf("register: %v", errRegister)
	}
	return manager, store, path
}

// readTrafficSnapshot synthesizes the auth file on disk as the watcher does, bound to the
// bytes read. excluded stands for an attribute the file does not hold.
func readTrafficSnapshot(t *testing.T, path, excluded string) *coreauth.Auth {
	t.Helper()
	data, errRead := os.ReadFile(path)
	if errRead != nil {
		t.Fatal(errRead)
	}
	auths, errSynthesize := synthesizer.SynthesizeAuthFile(&synthesizer.SynthesisContext{
		Config:      &config.Config{},
		AuthDir:     filepath.Dir(path),
		Now:         time.Now(),
		IDGenerator: synthesizer.NewStableIDGenerator(),
	}, path, data)
	if errSynthesize != nil || len(auths) != 1 || auths[0].ID != trafficAuthID || !auths[0].HasFileSnapshot() {
		t.Fatalf("synthesize %s: err=%v auths=%v", path, errSynthesize, auths)
	}
	auths[0].Attributes["excluded_models"] = excluded
	return auths[0]
}

// requireDiscarded applies snapshot and requires the manager to discard it: the current
// auth comes back, no error, nothing committed.
func requireDiscarded(t *testing.T, manager *coreauth.Manager, snapshot *coreauth.Auth) *coreauth.Auth {
	t.Helper()
	before, _ := manager.GetByID(snapshot.ID)
	current, errUpdate := manager.Update(coreauth.WithSkipPersist(context.Background()), snapshot)
	after, _ := manager.GetByID(snapshot.ID)
	if errUpdate != nil || current == nil || current.Generation != before.Generation || after.Generation != before.Generation {
		t.Fatalf("snapshot read before the write: err=%v current=%v, generation %d -> %d, want it discarded", errUpdate, current, before.Generation, after.Generation)
	}
	return current
}

// requireApplied applies snapshot and requires the manager to commit it.
func requireApplied(t *testing.T, manager *coreauth.Manager, snapshot *coreauth.Auth) {
	t.Helper()
	before, _ := manager.GetByID(snapshot.ID)
	if _, errUpdate := manager.Update(coreauth.WithSkipPersist(context.Background()), snapshot); errUpdate != nil {
		t.Fatalf("snapshot rejected: %v", errUpdate)
	}
	if after, _ := manager.GetByID(snapshot.ID); after.Generation <= before.Generation {
		t.Fatalf("snapshot discarded: generation %d -> %d", before.Generation, after.Generation)
	}
}

func trafficResult(success bool) coreauth.Result {
	result := coreauth.Result{AuthID: trafficAuthID, Provider: "claude", Model: "claude-sonnet-4-5", Success: success}
	if !success {
		retryAfter := time.Minute
		result.RetryAfter = &retryAfter
		result.Error = &coreauth.Error{Message: "rate limited", HTTPStatus: 429}
	}
	return result
}

func diskField(t *testing.T, path, key string) any {
	t.Helper()
	data, errRead := os.ReadFile(path)
	if errRead != nil {
		t.Fatal(errRead)
	}
	var content map[string]any
	if errUnmarshal := json.Unmarshal(data, &content); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	return content[key]
}

// assertResultRuntimeState checks that got still holds the runtime state a failed request left in want.
func assertResultRuntimeState(t *testing.T, got, want *coreauth.Auth) {
	t.Helper()
	if want.Status != coreauth.StatusError || want.LastError == nil || !want.Unavailable || !want.Quota.Exceeded || len(want.ModelStates) != 1 {
		t.Fatalf("request left no cooldown: status=%q lastError=%v unavailable=%v quota=%+v models=%d", want.Status, want.LastError, want.Unavailable, want.Quota, len(want.ModelStates))
	}
	if got.Status != want.Status || got.StatusMessage != want.StatusMessage || got.Unavailable != want.Unavailable || !got.NextRetryAfter.Equal(want.NextRetryAfter) {
		t.Fatalf("auth state = %q %q unavailable=%v retry=%v, want %q %q unavailable=%v retry=%v",
			got.Status, got.StatusMessage, got.Unavailable, got.NextRetryAfter, want.Status, want.StatusMessage, want.Unavailable, want.NextRetryAfter)
	}
	if got.LastError == nil || *got.LastError != *want.LastError {
		t.Fatalf("last error = %v, want %v", got.LastError, want.LastError)
	}
	if got.Quota.Exceeded != want.Quota.Exceeded || got.Quota.Reason != want.Quota.Reason || !got.Quota.NextRecoverAt.Equal(want.Quota.NextRecoverAt) || got.Quota.BackoffLevel != want.Quota.BackoffLevel {
		t.Fatalf("quota = %+v, want %+v", got.Quota, want.Quota)
	}
	if got.Failed != want.Failed {
		t.Fatalf("failed = %d, want %d", got.Failed, want.Failed)
	}
	for model, wantState := range want.ModelStates {
		gotState := got.ModelStates[model]
		if gotState == nil || gotState.Status != wantState.Status || gotState.Unavailable != wantState.Unavailable ||
			!gotState.NextRetryAfter.Equal(wantState.NextRetryAfter) || gotState.LastError == nil || *gotState.LastError != *wantState.LastError ||
			gotState.Quota.Exceeded != wantState.Quota.Exceeded || !gotState.Quota.NextRecoverAt.Equal(wantState.Quota.NextRecoverAt) {
			t.Fatalf("model state %s = %+v, want %+v", model, gotState, wantState)
		}
	}
}

// A request completes between a scan's read of the auth file and the apply of its
// snapshot; the request's save leaves the file untouched, so the snapshot applies.
func TestFileSnapshotAppliedAfterNoOpResultSave(t *testing.T) {
	ctx := context.Background()
	manager, _, path := newTrafficManager(t)
	before, _ := os.ReadFile(path)
	statBefore, _ := os.Stat(path)

	snapshot := readTrafficSnapshot(t, path, "new-model")
	manager.MarkResult(ctx, trafficResult(true))

	after, _ := os.ReadFile(path)
	statAfter, _ := os.Stat(path)
	if string(after) != string(before) || !statAfter.ModTime().Equal(statBefore.ModTime()) {
		t.Fatalf("the request result rewrote the auth file: %s -> %s", before, after)
	}
	requireApplied(t, manager, snapshot)
	got, _ := manager.GetByID(trafficAuthID)
	if got.Attributes["excluded_models"] != "new-model" {
		t.Fatalf("runtime excluded_models = %q, want new-model", got.Attributes["excluded_models"])
	}
	if got.Success != 1 {
		t.Fatalf("success count = %d, want the request's 1", got.Success)
	}
}

// A failed request leaves status, last error, cooldown, quota and model state on the
// auth after a scan read the file; applying the scan's snapshot keeps all of it and
// applies the snapshot's attributes in the same step.
func TestFileSnapshotKeepsRequestRuntimeState(t *testing.T) {
	ctx := context.Background()
	manager, _, path := newTrafficManager(t)

	snapshot := readTrafficSnapshot(t, path, "new-model")
	manager.MarkResult(ctx, trafficResult(false))
	want, _ := manager.GetByID(trafficAuthID)

	requireApplied(t, manager, snapshot)
	got, _ := manager.GetByID(trafficAuthID)
	if got.Attributes["excluded_models"] != "new-model" {
		t.Fatalf("runtime excluded_models = %q, want new-model", got.Attributes["excluded_models"])
	}
	assertResultRuntimeState(t, got, want)
}

// The operator edits the auth file after a request re-saved it; the scan reading the
// edit applies it and the next request's save keeps it.
func TestFileSnapshotAppliesExternalEditAfterResultSave(t *testing.T) {
	ctx := context.Background()
	manager, _, path := newTrafficManager(t)

	manager.MarkResult(ctx, trafficResult(true))
	if errWrite := os.WriteFile(path, []byte(`{"type":"claude","email":"a@b.c","prefix":"pfx-ext","disabled":false}`), 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	requireApplied(t, manager, readTrafficSnapshot(t, path, "old-model"))
	got, _ := manager.GetByID(trafficAuthID)
	if got.Prefix != "pfx-ext" || got.Metadata["prefix"] != "pfx-ext" {
		t.Fatalf("runtime prefix = %q (metadata %v), want the edit pfx-ext", got.Prefix, got.Metadata["prefix"])
	}
	manager.MarkResult(ctx, trafficResult(true))
	if prefix := diskField(t, path, "prefix"); prefix != "pfx-ext" {
		t.Fatalf("disk prefix after the next request = %v, want pfx-ext", prefix)
	}
}

// A scan reads the file, then the auth-files PATCH persists a new prefix, with request
// results before and after it. The scan's snapshot is discarded and the PATCH stays, on
// disk too; a snapshot that read the written file applies.
func TestFileSnapshotFencedByContentWriteAmidResults(t *testing.T) {
	ctx := context.Background()
	manager, store, path := newTrafficManager(t)

	stale := readTrafficSnapshot(t, path, "new-model")
	manager.MarkResult(ctx, trafficResult(true))
	var readAfterWrite *coreauth.Auth
	store.afterSave = func() {
		readAfterWrite = readTrafficSnapshot(t, path, "new-model")
	}
	patched, _ := manager.GetByID(trafficAuthID)
	patched.Metadata["prefix"] = "pfx-2"
	patched.Prefix = "pfx-2"
	if _, errUpdate := manager.Update(ctx, patched); errUpdate != nil {
		t.Fatalf("persisted update: %v", errUpdate)
	}
	manager.MarkResult(ctx, trafficResult(true))

	if current := requireDiscarded(t, manager, stale); current.Prefix != "pfx-2" {
		t.Fatalf("current prefix = %q, want pfx-2", current.Prefix)
	}
	manager.MarkResult(ctx, trafficResult(true))
	if got, _ := manager.GetByID(trafficAuthID); got.Prefix != "pfx-2" || diskField(t, path, "prefix") != "pfx-2" {
		t.Fatalf("runtime prefix = %q, disk %v, want pfx-2", got.Prefix, diskField(t, path, "prefix"))
	}

	if readAfterWrite == nil || readAfterWrite.Prefix != "pfx-2" {
		t.Fatalf("snapshot read after the write = %v, want prefix pfx-2", readAfterWrite)
	}
	requireApplied(t, manager, readAfterWrite)
	if got, _ := manager.GetByID(trafficAuthID); got.Prefix != "pfx-2" || got.Attributes["excluded_models"] != "new-model" {
		t.Fatalf("runtime prefix = %q excluded_models = %q, want pfx-2 and new-model", got.Prefix, got.Attributes["excluded_models"])
	}
}

// The auth-files PATCH disables the auth after a scan read the file: the scan's snapshot,
// read while the auth was enabled, must not enable it again.
func TestFileSnapshotFencedByDisableToggle(t *testing.T) {
	ctx := context.Background()
	manager, _, path := newTrafficManager(t)

	stale := readTrafficSnapshot(t, path, "new-model")
	patched, _ := manager.GetByID(trafficAuthID)
	patched.Disabled = true
	patched.Status = coreauth.StatusDisabled
	if _, errUpdate := manager.Update(ctx, patched); errUpdate != nil {
		t.Fatalf("disable: %v", errUpdate)
	}
	if disabled := diskField(t, path, "disabled"); disabled != true {
		t.Fatalf("disk disabled = %v, want true", disabled)
	}
	if current := requireDiscarded(t, manager, stale); !current.Disabled {
		t.Fatal("snapshot read before the disable enabled the auth again")
	}
}

// The auth-files PATCH's save fails after a scan read the file: nothing changed on disk,
// so the scan's snapshot still applies and brings the runtime back to the file's content.
func TestFileSnapshotAppliedAfterFailedSave(t *testing.T) {
	ctx := context.Background()
	manager, store, path := newTrafficManager(t)

	snapshot := readTrafficSnapshot(t, path, "new-model")
	store.failNext = errors.New("disk full")
	patched, _ := manager.GetByID(trafficAuthID)
	patched.Metadata["prefix"] = "pfx-2"
	patched.Prefix = "pfx-2"
	if _, errUpdate := manager.Update(ctx, patched); errUpdate != nil {
		t.Fatalf("update whose save fails: %v", errUpdate)
	}
	if store.failNext != nil || diskField(t, path, "prefix") != "pfx-1" {
		t.Fatalf("the save did not fail: disk prefix %v", diskField(t, path, "prefix"))
	}
	requireApplied(t, manager, snapshot)
	if got, _ := manager.GetByID(trafficAuthID); got.Prefix != "pfx-1" || got.Attributes["excluded_models"] != "new-model" {
		t.Fatalf("runtime prefix = %q excluded_models = %q, want the file's pfx-1 and new-model", got.Prefix, got.Attributes["excluded_models"])
	}
}

// A login or upload writes the auth file through the store itself, then hands the new auth
// to the manager without persisting. A scan snapshot read before that write must not bring
// the old content back, neither into the runtime nor, through the next request's save, to disk.
func TestFileSnapshotFencedByWriteOutsideManager(t *testing.T) {
	ctx := context.Background()
	manager, store, path := newTrafficManager(t)

	stale := readTrafficSnapshot(t, path, "new-model")
	loggedIn := readTrafficSnapshot(t, path, "old-model")
	loggedIn.ClearFileSnapshot()
	loggedIn.Metadata["prefix"] = "pfx-login"
	loggedIn.Prefix = "pfx-login"
	if _, errSave := store.Save(ctx, loggedIn.Clone()); errSave != nil {
		t.Fatalf("store write: %v", errSave)
	}
	if _, errUpdate := manager.Update(coreauth.WithSkipPersist(ctx), loggedIn); errUpdate != nil {
		t.Fatalf("hand-over without persisting: %v", errUpdate)
	}
	manager.MarkResult(ctx, trafficResult(true))

	requireDiscarded(t, manager, stale)
	manager.MarkResult(ctx, trafficResult(true))
	if got, _ := manager.GetByID(trafficAuthID); got.Prefix != "pfx-login" || diskField(t, path, "prefix") != "pfx-login" {
		t.Fatalf("runtime prefix = %q, disk %v, want pfx-login", got.Prefix, diskField(t, path, "prefix"))
	}
}
