package cliproxy

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/watcher"
	sdkauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

const registerRereadCaseEnv = "CLIPROXY_REGISTER_REREAD_CASE"

func TestFirstRegistrationSnapshotReread(t *testing.T) {
	which := os.Getenv(registerRereadCaseEnv)
	if which == "" {
		for _, child := range []string{"ordinary", "identical_rewrite", "unrelated_error"} {
			t.Run(child, func(t *testing.T) { runRegisterRereadChild(t, child) })
		}
		return
	}
	if which != "ordinary" && which != "identical_rewrite" && which != "unrelated_error" {
		t.Fatal("register reread: unknown case")
	}
	var outbound, rereads atomic.Int32
	http.DefaultTransport = deletedSnapshotRejectTransport{calls: &outbound}
	root := t.TempDir()
	authDir := filepath.Join(root, "auth")
	if os.Mkdir(authDir, 0700) != nil {
		t.Fatal("register reread: directory setup failed")
	}
	configPath := filepath.Join(root, "config.yaml")
	if os.WriteFile(configPath, []byte("auth-dir: "+authDir+"\n"), 0600) != nil {
		t.Fatal("register reread: config setup failed")
	}
	path := filepath.Join(authDir, "captured.json")
	writeDeletedSnapshotCredential(t, path, "before")
	original, errRead := os.ReadFile(path)
	if errRead != nil {
		t.Fatal("register reread: initial bytes unavailable")
	}
	store := sdkauth.NewFileTokenStore()
	store.SetBaseDir(authDir)
	sdkauth.RegisterTokenStore(store)
	cfg := &config.Config{AuthDir: authDir, CommercialMode: true}
	manager := coreauth.NewManager(store, nil, nil)
	service := &Service{cfg: cfg, coreManager: manager}
	w, errWatcher := watcher.NewWatcher(configPath, authDir, nil)
	if errWatcher != nil {
		t.Fatal("register reread: watcher unavailable")
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		if w.Stop() != nil {
			t.Error("register reread: watcher close failed")
		}
		manager.Remove(context.Background(), "captured.json")
		GlobalModelRegistry().UnregisterClient("captured.json")
		if outbound.Load() != 0 {
			t.Error("register reread: unexpected network attempt")
		}
	})
	w.SetConfig(cfg)
	updates := make(chan watcher.AuthUpdate, 8)
	w.SetAuthUpdateQueue(updates)
	service.watcher = &WatcherWrapper{rereadDiscardedFileSnapshot: func(a *coreauth.Auth) {
		rereads.Add(1)
		w.RereadDiscardedFileSnapshot(a)
	}}
	if w.Start(ctx) != nil {
		t.Fatal("register reread: watcher start failed")
	}
	initial := awaitDeletedSnapshotUpdate(t, updates, watcher.AuthUpdateActionAdd, "before")
	if !initial.Auth.FileSnapshotMatchesDisk() {
		t.Fatal("register reread: initial snapshot invalid")
	}
	applyCtx := coreauth.WithSkipPersist(ctx)
	if which == "unrelated_error" {
		// Validation rejects this independently of file freshness. Rereading a
		// still-valid snapshot here could continually requeue the same error.
		initial.Auth.Attributes[coreauth.AttributeWeight] = "invalid"
		service.handleAuthUpdates(applyCtx, []watcher.AuthUpdate{initial})
		if _, present := manager.GetByID(initial.ID); present || rereads.Load() != 0 {
			t.Fatal("register reread: unrelated error triggered publication or reread")
		}
		return
	}
	if which == "identical_rewrite" {
		file, errOpen := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0600)
		if errOpen != nil {
			t.Fatal("register reread: truncate failed")
		}
		t.Cleanup(func() { _ = file.Close() })
		if initial.Auth.FileSnapshotMatchesDisk() {
			t.Fatal("register reread: truncated snapshot still matches")
		}
		service.handleAuthUpdates(applyCtx, []watcher.AuthUpdate{initial})
		if _, present := manager.GetByID(initial.ID); present {
			t.Fatal("register reread: truncated first snapshot registered")
		}
		if rereads.Load() != 1 {
			t.Error("register reread: discarded registration did not request reread")
		}
		if _, errWrite := file.Write(original); errWrite != nil || file.Close() != nil {
			t.Fatal("register reread: completing rewrite failed")
		}
		completed, errRead := os.ReadFile(path)
		if errRead != nil || !bytes.Equal(completed, original) {
			t.Fatal("register reread: completing bytes changed")
		}
		// No forced reload and no further content edit: the real completing
		// write must produce a fresh observation despite identical bytes.
		resent := awaitDeletedSnapshotUpdate(t, updates, watcher.AuthUpdateActionModify, "before")
		if resent.Revision() <= initial.Revision() || !resent.Auth.FileSnapshotMatchesDisk() {
			t.Fatal("register reread: resent snapshot invalid")
		}
		service.handleAuthUpdates(applyCtx, []watcher.AuthUpdate{resent})
	} else {
		service.handleAuthUpdates(applyCtx, []watcher.AuthUpdate{initial})
		if rereads.Load() != 0 {
			t.Fatal("register reread: ordinary registration requested reread")
		}
	}
	current, present := manager.GetByID(initial.ID)
	if !present || current == nil || current.Prefix != "before" || current.RegistrationEpoch != 1 || current.HasFileSnapshot() {
		t.Fatal("register reread: completed credential unavailable")
	}
}

func runRegisterRereadChild(t *testing.T, which string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestFirstRegistrationSnapshotReread$", "-test.count=1", "-test.timeout=20s", "-test.v")
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, registerRereadCaseEnv+"=") {
			command.Env = append(command.Env, entry)
		}
	}
	command.Env = append(command.Env, registerRereadCaseEnv+"="+which)
	output, errRun := command.CombinedOutput()
	if errRun == nil {
		return
	}
	// The watcher has no worker join. Child exit isolates its lifetime, while
	// only fixed categories leave this captured output boundary.
	for _, reason := range []string{"unknown case", "directory setup failed", "config setup failed", "initial bytes unavailable", "watcher unavailable", "watcher close failed", "unexpected network attempt", "watcher start failed", "initial snapshot invalid", "unrelated error triggered publication or reread", "truncate failed", "truncated snapshot still matches", "truncated first snapshot registered", "discarded registration did not request reread", "completing rewrite failed", "completing bytes changed", "resent snapshot invalid", "ordinary registration requested reread", "completed credential unavailable"} {
		if bytes.Contains(output, []byte("register reread: "+reason)) {
			t.Log("register reread: " + reason)
		}
	}
	for _, reason := range []string{"fixture encoding failed", "fixture file replacement failed", "watcher update contract changed", "watcher update did not arrive"} {
		if bytes.Contains(output, []byte("snapshot probe: "+reason)) {
			t.Log("snapshot probe: " + reason)
		}
	}
	t.Logf("register reread child result: race=%t deadline=%t", bytes.Contains(output, []byte("DATA RACE")), ctx.Err() != nil || bytes.Contains(output, []byte("test timed out")))
	t.Fatal("register reread: isolated child failed")
}
