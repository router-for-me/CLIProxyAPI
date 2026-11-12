package cliproxy

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/gin-gonic/gin"
	management "github.com/router-for-me/CLIProxyAPI/v8/internal/api/handlers/management"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/watcher"
	sdkauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

const deletedSnapshotCaseEnv = "CLIPROXY_DELETED_SNAPSHOT_CASE"

func TestWatcherCapturedSnapshotDoesNotRestoreDeletedCredential(t *testing.T) {
	which := os.Getenv(deletedSnapshotCaseEnv)
	if which == "" {
		// The stock watcher Stop method has no worker join, and the management
		// handler owns a cleanup ticker. Fresh processes isolate both cases.
		for _, child := range []string{"file_present", "deleted"} {
			t.Run(child, func(t *testing.T) { runDeletedSnapshotChild(t, child) })
		}
		return
	}
	if which != "file_present" && which != "deleted" {
		t.Fatal("snapshot probe: unknown child case")
	}

	var outbound atomic.Int32
	http.DefaultTransport = deletedSnapshotRejectTransport{calls: &outbound}
	root := t.TempDir()
	authDir := filepath.Join(root, "auth")
	if os.MkdirAll(authDir, 0o700) != nil {
		t.Fatal("snapshot probe: fixture directory unavailable")
	}
	configPath := filepath.Join(root, "config.yaml")
	if os.WriteFile(configPath, []byte("auth-dir: "+authDir+"\n"), 0o600) != nil {
		t.Fatal("snapshot probe: fixture config unavailable")
	}
	path := filepath.Join(authDir, "captured.json")
	writeDeletedSnapshotCredential(t, path, "before")
	store := sdkauth.NewFileTokenStore()
	store.SetBaseDir(authDir)
	sdkauth.RegisterTokenStore(store)
	cfg := &config.Config{AuthDir: authDir, CommercialMode: true}
	manager := coreauth.NewManager(store, nil, nil)
	service := &Service{cfg: cfg, coreManager: manager}
	w, errWatcher := watcher.NewWatcher(configPath, authDir, nil)
	if errWatcher != nil {
		t.Fatal("snapshot probe: watcher unavailable")
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		if w.Stop() != nil {
			t.Error("snapshot probe: watcher close failed")
		}
		manager.Remove(context.Background(), "captured.json")
		GlobalModelRegistry().UnregisterClient("captured.json")
		if outbound.Load() != 0 {
			t.Error("snapshot probe: unexpected network attempt")
		}
	})
	w.SetConfig(cfg)
	updates := make(chan watcher.AuthUpdate, 8)
	w.SetAuthUpdateQueue(updates)
	service.watcher = &WatcherWrapper{rereadDiscardedFileSnapshot: w.RereadDiscardedFileSnapshot}
	if w.Start(ctx) != nil {
		t.Fatal("snapshot probe: watcher start failed")
	}
	initial := awaitDeletedSnapshotUpdate(t, updates, watcher.AuthUpdateActionAdd, "before")
	applyCtx := coreauth.WithSkipPersist(ctx)
	service.handleAuthUpdates(applyCtx, []watcher.AuthUpdate{initial})
	before, present := manager.GetByID("captured.json")
	if !present || before == nil || before.Prefix != "before" {
		t.Fatal("snapshot probe: initial registration failed")
	}
	writeDeletedSnapshotCredential(t, path, "after")
	held := awaitDeletedSnapshotUpdate(t, updates, watcher.AuthUpdateActionModify, "after")
	if held.Revision() <= initial.Revision() || !held.Auth.HasFileSnapshot() || !held.Auth.FileSnapshotMatchesDisk() {
		t.Fatal("snapshot probe: captured watcher snapshot invalid")
	}

	// This is the stock handler and real FileTokenStore. No server, OAuth flow,
	// provider execution or manually constructed watcher update is involved.
	handler := management.NewHandlerWithoutConfigFilePath(cfg, manager)
	deleteStatus := 0
	if which == "deleted" {
		recorder := httptest.NewRecorder()
		requestContext, _ := gin.CreateTestContext(recorder)
		requestContext.Request = httptest.NewRequest(http.MethodDelete, "/v8/management/credentials?name=captured.json", nil)
		handler.DeleteAuthFileV8(requestContext)
		deleteStatus = recorder.Code
		_, statErr := os.Stat(path)
		_, stillPresent := manager.GetByID("captured.json")
		if deleteStatus != http.StatusOK || !os.IsNotExist(statErr) || stillPresent {
			t.Fatal("snapshot probe: management deletion did not complete")
		}
	}

	// Release the actual already-consumed update before delivering any later
	// fsnotify delete. Its revision and bound snapshot remain byte-for-byte the
	// watcher output. This controls queue order, not the duration of any residue.
	service.handleAuthUpdates(applyCtx, []watcher.AuthUpdate{held})
	current, runtimePresent := manager.GetByID("captured.json")
	prefixMatches := current != nil && current.Prefix == "after"
	epochAdvanced := current != nil && current.RegistrationEpoch > before.RegistrationEpoch
	_, statErr := os.Stat(path)
	fileMissing := os.IsNotExist(statErr)
	listRecorder := httptest.NewRecorder()
	listContext, _ := gin.CreateTestContext(listRecorder)
	listContext.Request = httptest.NewRequest(http.MethodGet, "/v8/management/credentials", nil)
	handler.ListAuthFiles(listContext)
	if listRecorder.Code != http.StatusOK || !ojson.ValidBytes(listRecorder.Body.Bytes()) {
		t.Fatal("snapshot probe: management inventory unavailable")
	}
	var inventory, memory int
	for _, entry := range ojson.GetBytes(listRecorder.Body.Bytes(), "files").Array() {
		if entry.Get("name").String() == "captured.json" {
			inventory++
			if entry.Get("source").String() == "memory" {
				memory++
			}
		}
	}
	t.Logf("snapshot observation: delete_status=%d snapshot_bound=%t matches_disk=%t file_missing=%t runtime_present=%t prefix_matches=%t epoch_advanced=%t inventory=%d memory=%d", deleteStatus, held.Auth.HasFileSnapshot(), held.Auth.FileSnapshotMatchesDisk(), fileMissing, runtimePresent, prefixMatches, epochAdvanced, inventory, memory)
	if which == "file_present" {
		if fileMissing || !runtimePresent || !prefixMatches || inventory != 1 || memory != 0 {
			t.Error("snapshot probe: file present control failed")
		}
	} else {
		if !fileMissing {
			t.Error("snapshot probe: applying update recreated credential file")
		}
		if runtimePresent || inventory != 0 {
			t.Error("snapshot probe: deleted credential was restored")
		}
	}
}

func writeDeletedSnapshotCredential(t *testing.T, path, prefix string) {
	t.Helper()
	doc := ojson.NewDoc([]byte(`{"type":"codex","access_token":"synthetic-captured-grant"}`))
	if doc.Set("prefix", prefix) != nil {
		t.Fatal("snapshot probe: fixture encoding failed")
	}
	pending := path + ".pending"
	if os.WriteFile(pending, doc.Bytes(), 0o600) != nil || os.Rename(pending, path) != nil {
		t.Fatal("snapshot probe: fixture file replacement failed")
	}
}

func awaitDeletedSnapshotUpdate(t *testing.T, updates <-chan watcher.AuthUpdate, action watcher.AuthUpdateAction, prefix string) watcher.AuthUpdate {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case update := <-updates:
			if update.ID != "captured.json" || update.Auth == nil || update.Auth.Prefix != prefix {
				continue
			}
			if update.Action != action || update.Revision() == 0 || update.Auth.ID != update.ID || !update.Auth.HasFileSnapshot() {
				t.Fatal("snapshot probe: watcher update contract changed")
			}
			return update
		case <-deadline.C:
			t.Fatal("snapshot probe: watcher update did not arrive")
		}
	}
}

func runDeletedSnapshotChild(t *testing.T, which string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestWatcherCapturedSnapshotDoesNotRestoreDeletedCredential$", "-test.count=1", "-test.timeout=30s", "-test.v")
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, deletedSnapshotCaseEnv+"=") {
			command.Env = append(command.Env, entry)
		}
	}
	command.Env = append(command.Env, deletedSnapshotCaseEnv+"="+which)
	output, errRun := command.CombinedOutput()
	metric := regexp.MustCompile(`snapshot observation: delete_status=[0-9]+ snapshot_bound=(?:true|false) matches_disk=(?:true|false) file_missing=(?:true|false) runtime_present=(?:true|false) prefix_matches=(?:true|false) epoch_advanced=(?:true|false) inventory=[0-9]+ memory=[0-9]+`)
	for _, value := range metric.FindAll(output, -1) {
		t.Log(string(value))
	}
	if errRun != nil {
		for _, reason := range []string{"unknown child case", "fixture directory unavailable", "fixture config unavailable", "watcher unavailable", "watcher close failed", "unexpected network attempt", "watcher start failed", "initial registration failed", "captured watcher snapshot invalid", "management deletion did not complete", "management inventory unavailable", "file present control failed", "applying update recreated credential file", "deleted credential was restored", "fixture encoding failed", "fixture file replacement failed", "watcher update contract changed", "watcher update did not arrive"} {
			if bytes.Contains(output, []byte("snapshot probe: "+reason)) {
				t.Log("snapshot probe: " + reason)
			}
		}
		t.Logf("snapshot child result: race=%t deadline=%t", bytes.Contains(output, []byte("DATA RACE")), ctx.Err() != nil || bytes.Contains(output, []byte("test timed out")))
		t.Fatal("snapshot probe: isolated child failed")
	}
}

type deletedSnapshotRejectTransport struct{ calls *atomic.Int32 }

func (d deletedSnapshotRejectTransport) RoundTrip(*http.Request) (*http.Response, error) {
	d.calls.Add(1)
	return nil, errors.New("snapshot probe network disabled")
}
