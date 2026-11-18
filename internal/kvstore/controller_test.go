package kvstore

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func resetControllerForTest(t *testing.T) {
	t.Helper()
	_ = ShutdownLocal()
	RegisterHomeProvider(nil)
	t.Cleanup(func() {
		_ = ShutdownLocal()
		RegisterHomeProvider(nil)
	})
}

func TestControllerDefaultDisabledReturnsNilClient(t *testing.T) {
	resetControllerForTest(t)

	client, kvMode, errClient := CurrentKVClient()
	if errClient != nil {
		t.Fatalf("CurrentKVClient() error = %v, want nil", errClient)
	}
	if kvMode {
		t.Fatalf("CurrentKVClient() kvMode = true, want false")
	}
	if client != nil {
		t.Fatalf("CurrentKVClient() client = %#v, want nil", client)
	}
}

func TestControllerConfigureLocalLifecycleAndReuse(t *testing.T) {
	resetControllerForTest(t)

	dir := t.TempDir()
	dbPath1 := filepath.Join(dir, "kv1.db")
	dbPath2 := filepath.Join(dir, "kv2.db")

	if errCfg := ConfigureLocal(true, dbPath1); errCfg != nil {
		t.Fatalf("ConfigureLocal(true, dbPath1) error = %v", errCfg)
	}

	client1, kvMode1, errClient1 := CurrentKVClient()
	if errClient1 != nil || !kvMode1 || client1 == nil {
		t.Fatalf("CurrentKVClient() = (%v, %v, %v), want (*BoltStore, true, nil)", client1, kvMode1, errClient1)
	}
	bolt1, ok := client1.(*BoltStore)
	if !ok {
		t.Fatalf("CurrentKVClient() type = %T, want *BoltStore", client1)
	}

	// Reconfiguring with the same resolved path must reuse the same *BoltStore instance.
	if errCfgAgain := ConfigureLocal(true, dbPath1); errCfgAgain != nil {
		t.Fatalf("ConfigureLocal(true, dbPath1) second call error = %v", errCfgAgain)
	}
	client1Again, kvMode1Again, errClient1Again := CurrentKVClient()
	if errClient1Again != nil || !kvMode1Again {
		t.Fatalf("CurrentKVClient() after idempotent ConfigureLocal = (%v, %v, %v)", client1Again, kvMode1Again, errClient1Again)
	}
	if client1Again != bolt1 {
		t.Fatalf("ConfigureLocal with identical path did not reuse *BoltStore instance: got %p, want %p", client1Again, bolt1)
	}

	// Switching to a new path must close the old store and open the new one.
	if errCfg2 := ConfigureLocal(true, dbPath2); errCfg2 != nil {
		t.Fatalf("ConfigureLocal(true, dbPath2) error = %v", errCfg2)
	}
	client2, kvMode2, errClient2 := CurrentKVClient()
	if errClient2 != nil || !kvMode2 || client2 == nil {
		t.Fatalf("CurrentKVClient() after path switch = (%v, %v, %v)", client2, kvMode2, errClient2)
	}
	bolt2, ok := client2.(*BoltStore)
	if !ok {
		t.Fatalf("CurrentKVClient() type = %T, want *BoltStore", client2)
	}
	if bolt2 == bolt1 {
		t.Fatalf("ConfigureLocal with new path returned old *BoltStore instance")
	}
	if bolt2.Path() != dbPath2 {
		t.Fatalf("bolt2.Path() = %q, want %q", bolt2.Path(), dbPath2)
	}
	// Old store should be closed; operations on bolt1 must fail.
	if _, _, errOld := bolt1.KVGet(context.Background(), "key"); errOld == nil {
		t.Fatalf("expected old BoltStore to be closed after switching path")
	}

	// Disabling local KV closes the store and resets state to memory mode.
	if errDisable := ConfigureLocal(false, ""); errDisable != nil {
		t.Fatalf("ConfigureLocal(false, \"\") error = %v", errDisable)
	}
	clientDisabled, kvModeDisabled, errDisabled := CurrentKVClient()
	if errDisabled != nil || kvModeDisabled || clientDisabled != nil {
		t.Fatalf("CurrentKVClient() after disable = (%v, %v, %v), want (nil, false, nil)", clientDisabled, kvModeDisabled, errDisabled)
	}
}

func TestControllerConfigureLocalInvalidPathReportsUnavailableError(t *testing.T) {
	resetControllerForTest(t)

	// Passing an existing directory as the database file path causes bbolt.Open to fail.
	invalidPath := t.TempDir()
	errCfg := ConfigureLocal(true, invalidPath)
	if errCfg == nil {
		t.Fatalf("ConfigureLocal(true, directory) error = nil, want error")
	}

	client, kvMode, errClient := CurrentKVClient()
	if !kvMode {
		t.Fatalf("CurrentKVClient() kvMode = false, want true when enabled but open failed")
	}
	if client != nil {
		t.Fatalf("CurrentKVClient() client = %#v, want nil on open failure", client)
	}
	if errClient == nil {
		t.Fatalf("CurrentKVClient() error = nil, want unavailable error")
	}
}

func TestControllerPrioritizesHomeProviderOverLocalBoltStore(t *testing.T) {
	resetControllerForTest(t)

	localPath := filepath.Join(t.TempDir(), "local.db")
	if errCfg := ConfigureLocal(true, localPath); errCfg != nil {
		t.Fatalf("ConfigureLocal(true, localPath) error = %v", errCfg)
	}

	homeStorePath := filepath.Join(t.TempDir(), "home-mock.db")
	homeStore, errOpen := OpenBoltStore(homeStorePath, WithSweepInterval(0))
	if errOpen != nil {
		t.Fatalf("OpenBoltStore(homeStorePath) error = %v", errOpen)
	}
	t.Cleanup(func() {
		_ = homeStore.Close()
	})

	RegisterHomeProvider(func() (KVClient, bool, error) {
		return homeStore, true, nil
	})

	client, kvMode, errClient := CurrentKVClient()
	if errClient != nil || !kvMode {
		t.Fatalf("CurrentKVClient() with active Home provider = (%v, %v, %v), want (homeStore, true, nil)", client, kvMode, errClient)
	}
	if client != homeStore {
		t.Fatalf("CurrentKVClient() returned %p, want Home provider client %p", client, homeStore)
	}

	// When Home provider reports homeMode=true with an error, CurrentKVClient must return that error instead of falling back to local BoltStore.
	expectedErr := errors.New("home kv store unavailable: not connected")
	RegisterHomeProvider(func() (KVClient, bool, error) {
		return nil, true, expectedErr
	})
	clientErr, kvModeErr, errHome := CurrentKVClient()
	if !kvModeErr || clientErr != nil || !errors.Is(errHome, expectedErr) {
		t.Fatalf("CurrentKVClient() with unavailable Home = (%v, %v, %v), want (nil, true, %v)", clientErr, kvModeErr, errHome, expectedErr)
	}

	// When Home provider reports homeMode=false, CurrentKVClient falls back to the enabled local BoltStore.
	RegisterHomeProvider(func() (KVClient, bool, error) {
		return nil, false, nil
	})
	clientLocal, kvModeLocal, errLocal := CurrentKVClient()
	if errLocal != nil || !kvModeLocal || clientLocal == nil {
		t.Fatalf("CurrentKVClient() with inactive Home = (%v, %v, %v), want local BoltStore", clientLocal, kvModeLocal, errLocal)
	}
	if clientLocal == homeStore {
		t.Fatalf("CurrentKVClient() unexpectedly returned homeStore when homeMode=false")
	}
}

func TestControllerHelpersWithLocalBoltStore(t *testing.T) {
	resetControllerForTest(t)

	dbPath := filepath.Join(t.TempDir(), "helpers.db")
	if errCfg := ConfigureLocal(true, dbPath); errCfg != nil {
		t.Fatalf("ConfigureLocal(true, dbPath) error = %v", errCfg)
	}

	ctx := context.Background()
	type payload struct {
		Name string `json:"name"`
	}

	kvMode, errSet := KVSetJSONRequired(ctx, "cpa:test:json", payload{Name: "alice"}, 10*time.Minute)
	if errSet != nil || !kvMode {
		t.Fatalf("KVSetJSONRequired() = (%v, %v), want (true, nil)", kvMode, errSet)
	}

	var got payload
	kvMode, found, errGet := KVGetJSONRequired(ctx, "cpa:test:json", &got)
	if errGet != nil || !kvMode || !found || got.Name != "alice" {
		t.Fatalf("KVGetJSONRequired() = (%v, %v, %v, %+v), want (true, true, nil, alice)", kvMode, found, errGet, got)
	}

	if !KVSetNXBestEffort(ctx, "cpa:test:nx", []byte("1"), 5*time.Minute) {
		t.Fatalf("first KVSetNXBestEffort() = false, want true")
	}
	if KVSetNXBestEffort(ctx, "cpa:test:nx", []byte("2"), 5*time.Minute) {
		t.Fatalf("second KVSetNXBestEffort() = true, want false")
	}

	if !KVExpireBestEffort(ctx, "cpa:test:json", 20*time.Minute) {
		t.Fatalf("KVExpireBestEffort() = false, want true")
	}
	if !KVDelBestEffort(ctx, "cpa:test:json", "cpa:test:nx") {
		t.Fatalf("KVDelBestEffort() = false, want true")
	}
	var afterDel payload
	if _, foundAfterDel := KVGetJSONBestEffort(ctx, "cpa:test:json", &afterDel); foundAfterDel {
		t.Fatalf("KVGetJSONBestEffort() found deleted key")
	}
}
