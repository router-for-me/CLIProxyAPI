package watcher

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/watcher/synthesizer"
	sdkauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

const trafficAuthFile = "claude.json"

// takeQueuedAuthUpdate removes the queued update for id and drops the rest; the test is the consumer.
func takeQueuedAuthUpdate(w *Watcher, id string) (AuthUpdate, bool) {
	w.dispatchMu.Lock()
	defer w.dispatchMu.Unlock()
	update, ok := w.pendingUpdates[id]
	w.pendingUpdates, w.pendingOrder = nil, nil
	return update, ok
}

func excludedModelsConfig(dir, model string) *config.Config {
	return &config.Config{AuthDir: dir, OAuthExcludedModels: map[string][]string{"claude": {model}}}
}

func setTrafficConfig(w *Watcher, cfg *config.Config) {
	w.clientsMutex.Lock()
	w.config = cfg
	w.clientsMutex.Unlock()
}

// reloadExcludedModels applies a config whose oauth-excluded-models for claude changed to
// model, as the config reload does (no auth-dir rescan), and returns the queued update.
func reloadExcludedModels(t *testing.T, w *Watcher, model string) AuthUpdate {
	t.Helper()
	setTrafficConfig(w, excludedModelsConfig(w.authDir, model))
	w.reloadClients(false, []string{"claude"}, false)
	update, ok := takeQueuedAuthUpdate(w, trafficAuthFile)
	if !ok || update.Auth == nil || !update.Auth.HasFileSnapshot() {
		t.Fatalf("config reload queued %#v, want a file snapshot of the auth", update.Auth)
	}
	return update
}

// newTrafficWatcher loads one Claude auth file through a scan into a manager on a real
// file store, with oauth-excluded-models old-model; persist registers it as a login does,
// skip-persist as the queue consumer does.
func newTrafficWatcher(t *testing.T, persist bool) (*Watcher, *coreauth.Manager, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, trafficAuthFile)
	if errWrite := os.WriteFile(path, []byte(`{"type":"claude","email":"a@b.c"}`), 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	store := sdkauth.NewFileTokenStore()
	store.SetBaseDir(dir)
	manager := coreauth.NewManager(store, nil, nil)
	w := &Watcher{authDir: dir, config: excludedModelsConfig(dir, "old-model"), authQueue: make(chan AuthUpdate, 1)}
	w.refreshAuthState(false)
	add, ok := takeQueuedAuthUpdate(w, trafficAuthFile)
	if !ok || add.Auth == nil {
		t.Fatal("initial scan queued no update")
	}
	ctx := context.Background()
	if !persist {
		ctx = coreauth.WithSkipPersist(ctx)
	}
	if _, errRegister := manager.Register(ctx, add.Auth); errRegister != nil {
		t.Fatalf("register: %v", errRegister)
	}
	return w, manager, path
}

func trafficRequestResult(success bool) coreauth.Result {
	result := coreauth.Result{AuthID: trafficAuthFile, Provider: "claude", Model: "claude-sonnet-4-5", Success: success}
	if !success {
		retryAfter := time.Minute
		result.RetryAfter = &retryAfter
		result.Error = &coreauth.Error{Message: "rate limited", HTTPStatus: 429}
	}
	return result
}

// applyQueuedSnapshot applies the queued update as the service's queue consumer does and
// reports whether the manager committed it (false: discarded, the file changed since the read).
func applyQueuedSnapshot(t *testing.T, manager *coreauth.Manager, update AuthUpdate) bool {
	t.Helper()
	if (update.Action != AuthUpdateActionModify && update.Action != AuthUpdateActionAdd) || update.Auth == nil {
		t.Fatalf("queued update = %v %#v, want an Add or Modify snapshot", update.Action, update.Auth)
	}
	before, _ := manager.GetByID(update.Auth.ID)
	if _, errUpdate := manager.Update(coreauth.WithSkipPersist(context.Background()), update.Auth); errUpdate != nil {
		t.Fatalf("snapshot rejected: %v", errUpdate)
	}
	after, _ := manager.GetByID(update.Auth.ID)
	return after.Generation > before.Generation
}

// requireNextScanKeeps runs the next unrelated scan: it must queue nothing for the auth.
func requireNextScanKeeps(t *testing.T, w *Watcher) {
	t.Helper()
	w.refreshAuthState(false)
	if update, queued := takeQueuedAuthUpdate(w, trafficAuthFile); queued {
		t.Fatalf("next scan queued %v for the auth", update.Action)
	}
}

// A config reload changes oauth-excluded-models; a request on the auth completes between
// the reload's scan and the apply of the scan's snapshot (its save leaves the file as is):
// the snapshot applies and the next scan queues nothing.
func TestRefreshAuthStateExcludedModelsReloadSurvivesRequestResult(t *testing.T) {
	w, manager, _ := newTrafficWatcher(t, true)

	update := reloadExcludedModels(t, w, "new-model")
	manager.MarkResult(context.Background(), trafficRequestResult(true))
	if !applyQueuedSnapshot(t, manager, update) {
		t.Fatal("the reload's snapshot was discarded after a no-op result save")
	}
	if got, _ := manager.GetByID(trafficAuthFile); got.Success != 1 {
		t.Fatalf("success count = %d, want the request's 1", got.Success)
	}
	requireNextScanKeeps(t, w)
}

// A failed request lands between the config reload's scan and the apply: the snapshot
// applies and keeps status, last error, cooldown, quota and model state.
func TestRefreshAuthStateSnapshotKeepsRequestRuntimeState(t *testing.T) {
	w, manager, _ := newTrafficWatcher(t, true)

	update := reloadExcludedModels(t, w, "new-model")
	manager.MarkResult(context.Background(), trafficRequestResult(false))
	want, _ := manager.GetByID(trafficAuthFile)
	if want.Status != coreauth.StatusError || want.LastError == nil || !want.Unavailable || !want.Quota.Exceeded || len(want.ModelStates) != 1 {
		t.Fatalf("request left no cooldown: status=%q lastError=%v unavailable=%v quota=%+v models=%d", want.Status, want.LastError, want.Unavailable, want.Quota, len(want.ModelStates))
	}
	if !applyQueuedSnapshot(t, manager, update) {
		t.Fatal("the reload's snapshot was discarded after a failed request")
	}

	got, _ := manager.GetByID(trafficAuthFile)
	if got.Status != want.Status || got.StatusMessage != want.StatusMessage || got.Unavailable != want.Unavailable || !got.NextRetryAfter.Equal(want.NextRetryAfter) ||
		got.LastError == nil || *got.LastError != *want.LastError || got.Failed != want.Failed {
		t.Fatalf("auth state = %q %q unavailable=%v retry=%v lastError=%v failed=%d, want %q %q unavailable=%v retry=%v lastError=%v failed=%d",
			got.Status, got.StatusMessage, got.Unavailable, got.NextRetryAfter, got.LastError, got.Failed,
			want.Status, want.StatusMessage, want.Unavailable, want.NextRetryAfter, want.LastError, want.Failed)
	}
	if got.Quota.Exceeded != want.Quota.Exceeded || got.Quota.Reason != want.Quota.Reason || !got.Quota.NextRecoverAt.Equal(want.Quota.NextRecoverAt) {
		t.Fatalf("quota = %+v, want %+v", got.Quota, want.Quota)
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

// A request on the auth completes and the operator edits the auth file while a config
// reload's scan runs, before it reads the file. The scan's snapshot carries the edit and applies.
func TestRefreshAuthStateAppliesExternalEditAfterRequestResult(t *testing.T) {
	w, manager, path := newTrafficWatcher(t, true)

	setTrafficConfig(w, excludedModelsConfig(w.authDir, "new-model"))
	original := snapshotCoreAuthsFunc
	t.Cleanup(func() { snapshotCoreAuthsFunc = original })
	snapshotCoreAuthsFunc = func(cfg *config.Config, authDir string, parser synthesizer.PluginAuthParser) ([]*coreauth.Auth, map[string]struct{}) {
		manager.MarkResult(context.Background(), trafficRequestResult(true))
		if errWrite := os.WriteFile(path, []byte(`{"type":"claude","email":"a@b.c","disabled":false,"note":"edited"}`), 0o600); errWrite != nil {
			t.Error(errWrite)
		}
		return original(cfg, authDir, parser)
	}
	w.reloadClients(false, []string{"claude"}, false)
	snapshotCoreAuthsFunc = original
	update, _ := takeQueuedAuthUpdate(w, trafficAuthFile)
	if !applyQueuedSnapshot(t, manager, update) {
		t.Fatal("the snapshot carrying the edit was discarded")
	}

	got, _ := manager.GetByID(trafficAuthFile)
	if got.Attributes["note"] != "edited" || got.Metadata["note"] != "edited" {
		t.Fatalf("runtime note = %q (metadata %v), want the edit", got.Attributes["note"], got.Metadata["note"])
	}
	requireNextScanKeeps(t, w)
	manager.MarkResult(context.Background(), trafficRequestResult(true))
	data, _ := os.ReadFile(path)
	var onDisk map[string]any
	if errUnmarshal := json.Unmarshal(data, &onDisk); errUnmarshal != nil || onDisk["note"] != "edited" {
		t.Fatalf("auth file after the next request = %s, want the edit kept", data)
	}
}

// The fsnotify variant: the auth-files PATCH writes the file and the watcher reads it under
// the new config; a request completes before the queue applies the snapshot.
func TestAddOrUpdateClientConfigChangeSurvivesRequestResult(t *testing.T) {
	w, manager, path := newTrafficWatcher(t, true)

	setTrafficConfig(w, excludedModelsConfig(w.authDir, "new-model"))
	patched, _ := manager.GetByID(trafficAuthFile)
	patched.Metadata["prefix"] = "pfx-9"
	patched.Prefix = "pfx-9"
	if _, errUpdate := manager.Update(context.Background(), patched); errUpdate != nil {
		t.Fatalf("persisted update: %v", errUpdate)
	}
	w.addOrUpdateClient(path)
	update, _ := takeQueuedAuthUpdate(w, trafficAuthFile)
	manager.MarkResult(context.Background(), trafficRequestResult(true))
	if !applyQueuedSnapshot(t, manager, update) {
		t.Fatal("the file event's snapshot was discarded after a no-op result save")
	}

	if got, _ := manager.GetByID(trafficAuthFile); got.Prefix != "pfx-9" {
		t.Fatalf("runtime prefix = %q, want pfx-9", got.Prefix)
	}
	requireNextScanKeeps(t, w)
}

// The queue consumer registered the auth without persisting, so the first request result's
// save adds "disabled": false and changes the file after the reload's scan read it: the
// reload's snapshot is discarded, and the file event of that write queues a snapshot that
// applies; the next scan queues nothing.
func TestRefreshAuthStateFirstResultSaveRecoveredByFileEvent(t *testing.T) {
	w, manager, path := newTrafficWatcher(t, false)

	update := reloadExcludedModels(t, w, "new-model")
	manager.MarkResult(context.Background(), trafficRequestResult(true))
	if disabled, ok := readJSONField(t, path, "disabled").(bool); !ok || disabled {
		t.Fatalf("first result save wrote disabled = %v, want false", readJSONField(t, path, "disabled"))
	}
	if applyQueuedSnapshot(t, manager, update) {
		t.Fatal("the reload's snapshot, read before the first save changed the file, was applied")
	}

	w.addOrUpdateClient(path)
	event, ok := takeQueuedAuthUpdate(w, trafficAuthFile)
	if !ok || !applyQueuedSnapshot(t, manager, event) {
		t.Fatalf("file event queued %v (ok=%v), want a snapshot that applies", event.Action, ok)
	}
	if got, _ := manager.GetByID(trafficAuthFile); got.Success != 1 || got.Disabled {
		t.Fatalf("runtime success = %d disabled = %v, want the request's 1 and enabled", got.Success, got.Disabled)
	}
	requireNextScanKeeps(t, w)
}

func readJSONField(t *testing.T, path, key string) any {
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

// hostConfigParser parses claude auth files as an auth plugin does; its output depends on the
// host config, which the plugin host sends with every parse request (here: *host).
type hostConfigParser struct{ host *string }

func (p hostConfigParser) ParseAuth(_ context.Context, req pluginapi.AuthParseRequest) (*coreauth.Auth, bool, error) {
	var metadata map[string]any
	if errUnmarshal := json.Unmarshal(req.RawJSON, &metadata); errUnmarshal != nil {
		return nil, false, nil
	}
	return &coreauth.Auth{ID: req.FileName, Provider: "claude", Metadata: metadata, Attributes: map[string]string{"host_config": *p.host}}, true, nil
}

// An auth-files PATCH writes the file and the watcher reads that write; its persisted update
// reaches the watcher only while a config reload's scan runs. The scan keeps the persisted
// auth over its own snapshot and reads the file again afterwards, as a file event would, so
// what the plugin parser derives from the new host config reaches the runtime.
func TestRefreshAuthStateRereadsAuthPersistedDuringScan(t *testing.T) {
	ctx := context.Background()
	w, manager, path := newTrafficWatcher(t, true)
	host := "old"
	w.SetPluginAuthParser(hostConfigParser{host: &host})

	patched, _ := manager.GetByID(trafficAuthFile)
	patched.Metadata["note"] = "patched"
	persisted, errUpdate := manager.Update(ctx, patched)
	if errUpdate != nil {
		t.Fatalf("persisted update: %v", errUpdate)
	}
	w.addOrUpdateClient(path)
	written, _ := takeQueuedAuthUpdate(w, trafficAuthFile)
	if !applyQueuedSnapshot(t, manager, written) {
		t.Fatal("the PATCH's file event was discarded")
	}
	if got, _ := manager.GetByID(trafficAuthFile); got.Attributes["host_config"] != "old" {
		t.Fatalf("runtime host_config = %q, want old", got.Attributes["host_config"])
	}

	setTrafficConfig(w, excludedModelsConfig(w.authDir, "new-model"))
	host = "new"
	original := snapshotCoreAuthsFunc
	t.Cleanup(func() { snapshotCoreAuthsFunc = original })
	snapshotCoreAuthsFunc = func(cfg *config.Config, authDir string, parser synthesizer.PluginAuthParser) ([]*coreauth.Auth, map[string]struct{}) {
		w.dispatchPersistedAuthUpdate(AuthUpdate{Action: AuthUpdateActionModify, ID: trafficAuthFile, Auth: persisted})
		return original(cfg, authDir, parser)
	}
	w.reloadClients(false, []string{"claude"}, false)
	snapshotCoreAuthsFunc = original
	update, ok := takeQueuedAuthUpdate(w, trafficAuthFile)
	if !ok {
		t.Fatal("nothing queued for the auth after the scan")
	}
	manager.MarkResult(ctx, trafficRequestResult(true))
	applyQueuedSnapshot(t, manager, update)

	if got, _ := manager.GetByID(trafficAuthFile); got.Attributes["host_config"] != "new" || got.Metadata["note"] != "patched" {
		t.Fatalf("runtime host_config = %q note = %v, want the new host config and the PATCH's note", got.Attributes["host_config"], got.Metadata["note"])
	}
	requireNextScanKeeps(t, w)
}
