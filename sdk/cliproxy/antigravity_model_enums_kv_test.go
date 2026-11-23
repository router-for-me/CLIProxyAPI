package cliproxy

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/kvstore"
	sdkauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func TestAntigravityModelEnumLocalKVRestart(t *testing.T) {
	resetAntigravityCapabilityCache()
	t.Cleanup(resetAntigravityCapabilityCache)
	const id = "enum-restart.json"
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = fmt.Fprint(w, `{"models":{"gemini-2.5-pro":{"model":"MODEL_GOOGLE_GEMINI_2_5_PRO"}}}`)
	}))
	t.Cleanup(srv.Close)
	authDir := t.TempDir()
	store := sdkauth.NewFileTokenStore()
	store.SetBaseDir(authDir)
	path := filepath.Join(t.TempDir(), "state.db")
	openKV := func() {
		t.Helper()
		if errConfigure := kvstore.ConfigureLocal(true, path); errConfigure != nil {
			t.Fatal(errConfigure)
		}
	}
	closeKV := func() {
		t.Helper()
		if errShutdown := kvstore.ShutdownLocal(); errShutdown != nil {
			t.Error(errShutdown)
		}
	}
	t.Cleanup(closeKV)
	openKV()
	first := coreauth.NewManager(store, nil, nil)
	auth := antigravityTestAuth(id, srv.URL)
	auth.Metadata["type"] = "antigravity"
	auth.Metadata["base_url"] = srv.URL
	auth.Metadata["refresh_token"] = "refresh-token"
	auth.CreatedAt = time.Unix(1, 0)
	auth, errRegister := first.Register(t.Context(), auth)
	if errRegister != nil {
		t.Fatal(errRegister)
	}
	svc := &Service{cfg: &config.Config{AuthDir: authDir}, coreManager: first}
	svc.registerModelsForAuth(t.Context(), auth)
	svc.WaitAntigravityProbes()
	t.Cleanup(func() { GlobalModelRegistry().UnregisterClient(id) })
	resetAntigravityCapabilityCache()
	closeKV()
	openKV()
	second := coreauth.NewManager(store, nil, nil)
	if errLoad := second.Load(t.Context()); errLoad != nil {
		t.Fatal(errLoad)
	}
	loaded, found := second.GetByID(id)
	if !found || loaded.CreatedAt.Equal(auth.CreatedAt) {
		t.Fatal("fixture did not reload a new file snapshot")
	}
	if got, ok := coreauth.LookupAntigravityModelEnum(t.Context(), loaded, "gemini-2.5-pro"); !ok || got != "MODEL_GOOGLE_GEMINI_2_5_PRO" {
		t.Fatalf("reloaded credential lookup = %q, %v", got, ok)
	}
	restarted := &Service{cfg: &config.Config{AuthDir: authDir}, coreManager: second}
	restarted.registerModelsForAuth(t.Context(), loaded)
	restarted.WaitAntigravityProbes()
	if calls.Load() != 1 {
		t.Fatalf("warm KV startup made %d upstream calls, want 1 total", calls.Load())
	}
	client, _, _ := kvstore.CurrentKVClient()
	if _, errDel := client.KVDel(t.Context(), "antigravity:model-enums:local:v1:"+kvstore.HashKeyPart(id)); errDel != nil {
		t.Fatal(errDel)
	}
	resetAntigravityCapabilityCache()
	restarted.registerModelsForAuth(t.Context(), loaded)
	restarted.WaitAntigravityProbes()
	if calls.Load() != 2 {
		t.Fatalf("missing KV under fresh persisted hints made %d upstream calls, want 2 total", calls.Load())
	}
}

func TestAntigravityModelEnumLocalKVRestartAfterReregistration(t *testing.T) {
	t.Run("register", func(t *testing.T) { testAntigravityModelEnumLocalKVReplacement(t, false) })
	t.Run("reload", func(t *testing.T) { testAntigravityModelEnumLocalKVReplacement(t, true) })
}

func testAntigravityModelEnumLocalKVReplacement(t *testing.T, reload bool) {
	t.Helper()
	authDir := t.TempDir()
	store := sdkauth.NewFileTokenStore()
	store.SetBaseDir(authDir)
	path := filepath.Join(t.TempDir(), "state.db")
	openKV := func() {
		t.Helper()
		if errConfigure := kvstore.ConfigureLocal(true, path); errConfigure != nil {
			t.Fatal(errConfigure)
		}
	}
	closeKV := func() {
		t.Helper()
		if errShutdown := kvstore.ShutdownLocal(); errShutdown != nil {
			t.Fatal(errShutdown)
		}
	}
	t.Cleanup(closeKV)
	openKV()
	manager := coreauth.NewManager(store, nil, nil)
	const id, model = "enum-reregister.json", "gemini-2.5-pro"
	credential := func(label string) *coreauth.Auth {
		auth := antigravityTestAuth(id, "https://example.test")
		auth.Metadata["type"] = "antigravity"
		auth.Metadata["base_url"] = "https://example.test"
		auth.Metadata["project_id"] = "project"
		auth.Metadata["refresh_token"] = "same-grant"
		auth.Metadata["email"] = label + "@example.test"
		return auth
	}
	first, errRegister := manager.Register(t.Context(), credential("first"))
	if errRegister != nil {
		t.Fatal(errRegister)
	}
	if !manager.StoreAntigravityModelEnums(t.Context(), first, map[string]string{model: "MODEL_FIRST"}) {
		t.Fatal("failed to persist the first snapshot")
	}
	if got, ok := coreauth.LookupAntigravityModelEnum(t.Context(), first, model); !ok || got != "MODEL_FIRST" {
		t.Fatalf("first registration lookup = %q, %v", got, ok)
	}
	var second *coreauth.Auth
	if reload {
		manager.Remove(t.Context(), id)
		if _, errSave := store.Save(t.Context(), credential("second")); errSave != nil {
			t.Fatal(errSave)
		}
		if errLoad := manager.Load(t.Context()); errLoad != nil {
			t.Fatal(errLoad)
		}
		second, _ = manager.GetByID(id)
	} else {
		var errReplace error
		second, errReplace = manager.Register(t.Context(), credential("second"))
		if errReplace != nil {
			t.Fatal(errReplace)
		}
	}
	if second == nil {
		t.Fatal("replacement was not admitted")
	}
	if got, ok := coreauth.LookupAntigravityModelEnum(t.Context(), second, model); ok {
		t.Fatalf("replacement read the first snapshot before restart: %q", got)
	}
	if manager.StoreAntigravityModelEnums(t.Context(), first, map[string]string{model: "MODEL_LATE_FIRST"}) {
		t.Fatal("late publication from the first registration was accepted")
	}
	closeKV()
	openKV()
	restarted := coreauth.NewManager(store, nil, nil)
	if errLoad := restarted.Load(t.Context()); errLoad != nil {
		t.Fatal(errLoad)
	}
	loaded, found := restarted.GetByID(id)
	if !found || loaded.Metadata["email"] != "second@example.test" {
		t.Fatal("fixture did not restore the replacement credential")
	}
	if got, ok := coreauth.LookupAntigravityModelEnum(t.Context(), loaded, model); ok {
		t.Fatalf("restored replacement read the first snapshot: %q", got)
	}
}

func TestAntigravityModelEnumRetriesFailedLocalKVPublication(t *testing.T) {
	resetAntigravityCapabilityCache()
	t.Cleanup(resetAntigravityCapabilityCache)
	path := filepath.Join(t.TempDir(), "models.db")
	if errConfigure := kvstore.ConfigureLocal(true, path); errConfigure != nil {
		t.Fatal(errConfigure)
	}
	t.Cleanup(func() { _ = kvstore.ShutdownLocal() })
	client, _, _ := kvstore.CurrentKVClient()
	if errClose := client.(*kvstore.BoltStore).Close(); errClose != nil {
		t.Fatal(errClose)
	}
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = fmt.Fprint(w, `{"models":{"gemini-2.5-pro":{"model":"MODEL_GOOGLE_GEMINI_2_5_PRO"}}}`)
	}))
	t.Cleanup(srv.Close)
	manager := coreauth.NewManager(nil, nil, nil)
	svc := &Service{cfg: &config.Config{}, coreManager: manager}
	auth, errRegister := manager.Register(t.Context(), antigravityTestAuth("enum-kv-retry", srv.URL))
	if errRegister != nil {
		t.Fatal(errRegister)
	}
	t.Cleanup(func() { GlobalModelRegistry().UnregisterClient(auth.ID) })
	svc.registerModelsForAuth(t.Context(), auth)
	svc.WaitAntigravityProbes()
	if errShutdown := kvstore.ShutdownLocal(); errShutdown != nil {
		t.Fatal(errShutdown)
	}
	if errConfigure := kvstore.ConfigureLocal(true, path); errConfigure != nil {
		t.Fatal(errConfigure)
	}
	svc.refreshAntigravityModels(t.Context())
	svc.WaitAntigravityProbes()
	if got, ok := coreauth.LookupAntigravityModelEnum(t.Context(), auth, "gemini-2.5-pro"); !ok || got != "MODEL_GOOGLE_GEMINI_2_5_PRO" || calls.Load() != 1 {
		t.Fatalf("publication retry = %q, %v, %d upstream calls", got, ok, calls.Load())
	}
}

// blockEnumKVWrite stalls just one account's publication.
type blockEnumKVWrite struct {
	kvstore.KVClient
	key     string
	started chan struct{}
	release chan struct{}
}

func (b *blockEnumKVWrite) KVSet(ctx context.Context, key string, raw []byte, opts kvstore.KVSetOptions) (bool, error) {
	if key == b.key {
		close(b.started)
		select {
		case <-b.release:
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}
	return b.KVClient.KVSet(ctx, key, raw, opts)
}

func TestAntigravityModelEnumBlockedKVDoesNotHoldCapabilityLock(t *testing.T) {
	resetAntigravityCapabilityCache()
	t.Cleanup(resetAntigravityCapabilityCache)
	const slowID = "enum-slow-kv"
	backend, errOpen := kvstore.OpenBoltStore(filepath.Join(t.TempDir(), "state.db"))
	if errOpen != nil {
		t.Fatal(errOpen)
	}
	t.Cleanup(func() {
		if errClose := backend.Close(); errClose != nil {
			t.Error(errClose)
		}
	})
	blocked := &blockEnumKVWrite{
		KVClient: backend, key: "antigravity:model-enums:local:v1:" + kvstore.HashKeyPart(slowID),
		started: make(chan struct{}), release: make(chan struct{}),
	}
	kvstore.RegisterHomeProvider(func() (kvstore.KVClient, bool, error) { return blocked, true, nil })
	t.Cleanup(func() { kvstore.RegisterHomeProvider(nil) })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"models":{"gemini-2.5-pro":{"model":"MODEL_GOOGLE_GEMINI_2_5_PRO"}}}`)
	}))
	t.Cleanup(srv.Close)
	manager := coreauth.NewManager(nil, nil, nil)
	svc := &Service{cfg: &config.Config{}, coreManager: manager}
	t.Cleanup(func() {
		close(blocked.release)
		svc.WaitAntigravityProbes()
	})
	var auths []*coreauth.Auth
	for _, id := range []string{slowID, "enum-other-kv"} {
		auth, errRegister := manager.Register(t.Context(), antigravityTestAuth(id, srv.URL))
		if errRegister != nil {
			t.Fatal(errRegister)
		}
		t.Cleanup(func() { GlobalModelRegistry().UnregisterClient(id) })
		auths = append(auths, auth)
	}
	svc.registerModelsForAuth(t.Context(), auths[0])
	select {
	case <-blocked.started:
	case <-time.After(3 * time.Second):
		t.Fatal("the first publication never reached KV")
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		svc.fetchAntigravityModelCapabilityHints(t.Context(), auths[1])
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("blocked KV held the capability mutex while refreshing another credential")
	}
}
