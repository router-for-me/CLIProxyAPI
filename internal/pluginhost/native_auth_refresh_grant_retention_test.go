//go:build cgo && (linux || darwin || freebsd)

package pluginhost

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sdkauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestNativeAuthRefreshRetainsConsumedGrant(t *testing.T) {
	native := nativeAuthRefreshBuildNativeHost(t)
	upstream := &nativeAuthRefreshLoopback{}
	provider := httptest.NewServer(upstream)
	defer provider.Close()
	credential := nativeAuthRefreshCredential("grant-retention.json", "expired", provider.URL)
	credential["test_refresh_policy"] = "on-demand"
	auth := nativeAuthRefreshParse(t, native.host, native.cfg.AuthDir, credential)
	manager := nativeAuthRefreshManager(t, native.cfg, native.host, auth)
	file := sdkauth.NewFileTokenStore()
	file.SetBaseDir(native.cfg.AuthDir)
	failure := errors.New("synthetic pending rotation save failure")
	store := &nativeFailingSave{FileTokenStore: file, err: failure}
	store.fail.Store(true)
	manager.SetStore(store)
	current, _ := manager.GetByID(auth.ID)
	if _, err := manager.EnsureRequestAuth(context.Background(), current, ""); !errors.Is(err, failure) {
		t.Fatalf("initial rotation lost save failure: %v", err)
	}
	if _, err := manager.EnsureRequestAuth(context.Background(), current, ""); !errors.Is(err, failure) {
		t.Fatalf("original demand cohort lost save failure: %v", err)
	}
	if counters := nativeAuthRefreshNativeCounters(t, native.client); counters.Refresh != 1 || store.saves.Load() != 1 {
		t.Fatalf("initial native rotation/save counts: refresh=%d save=%d", counters.Refresh, store.saves.Load())
	}
	path := filepath.Join(native.cfg.AuthDir, auth.FileName)
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	sdkauth.RegisterPluginAuthParser(native.host)
	defer sdkauth.RegisterPluginAuthParser(nil)
	if err := manager.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, exists := manager.GetByID(auth.ID); exists {
		t.Fatal("unparseable file unexpectedly retained a registration")
	}
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := manager.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	loaded, exists := manager.GetByID(auth.ID)
	if !exists || loaded.RegistrationEpoch == current.RegistrationEpoch {
		t.Fatal("restored file did not create a new registration epoch")
	}
	store.fail.Store(false)
	server := nativeAuthRefreshHandlerServer(t, native.cfg, manager, native.host)
	body := `{"model":"native-auth-refresh-chat","messages":[{"role":"user","content":"synthetic grant retention"}]}`
	attempts := make([]map[string]any, 0, 3)
	for attempt := 0; attempt < 3; attempt++ {
		forced, forceErr := manager.ForceRefreshAuth(context.Background(), auth.ID)
		if forceErr == nil || forced != nil {
			t.Errorf("force attempt %d admitted unsettled old grant: error=%v", attempt, forceErr)
		}
		loaded, _ = manager.GetByID(auth.ID)
		fresh, demandErr := manager.EnsureRequestAuth(context.Background(), loaded, "")
		if demandErr == nil || fresh != nil {
			t.Errorf("demand attempt %d admitted unsettled old grant: error=%v", attempt, demandErr)
		}
		resp, err := server.Client().Post(server.URL+"/v1/chat/completions", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		_, errRead := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if errRead != nil {
			t.Fatal(errRead)
		}
		if resp.StatusCode == http.StatusOK {
			t.Errorf("request attempt %d reached inference while the grant was unsettled", attempt)
		}
		counters := nativeAuthRefreshNativeCounters(t, native.client)
		if counters.Refresh != 1 || len(upstream.snapshot()) != 1 || store.saves.Load() != 1 {
			t.Errorf("attempt %d replayed or saved a source-fenced grant: refresh=%d requests=%d save=%d", attempt, counters.Refresh, len(upstream.snapshot()), store.saves.Load())
		}
		attempts = append(attempts, map[string]any{"attempt": attempt, "force_blocked": forceErr != nil, "demand_blocked": demandErr != nil, "http_status": resp.StatusCode, "native_refresh_calls": counters.Refresh, "upstream_requests": len(upstream.snapshot()), "save_calls": store.saves.Load()})
	}
	nativeAuthRefreshWriteJSON(t, filepath.Join(native.root, "grant-retention-receipt.json"), map[string]any{"native_library": native.library, "native_library_sha256": native.librarySHA, "restored_epoch": loaded.RegistrationEpoch, "attempts": attempts})
	t.Logf("native grant retention receipt=%s", filepath.Join(native.root, "grant-retention-receipt.json"))
	login := nativeAuthRefreshCredential(auth.ID, "valid", provider.URL)
	login["test_refresh_policy"] = "on-demand"
	replacement := nativeAuthRefreshParse(t, native.host, native.cfg.AuthDir, login)
	if _, err := manager.Register(coreauth.WithSkipPersist(context.Background()), replacement); err != nil {
		t.Fatal(err)
	}
	if store.saves.Load() != 1 {
		t.Fatal("replacement registration persisted a pending rotation")
	}
	resp, err := server.Client().Post(server.URL+"/v1/chat/completions", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	response, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil || resp.StatusCode != http.StatusOK || !strings.Contains(string(response), "synthetic-native-ok") {
		t.Fatalf("distinct native login did not recover: status=%d error=%v", resp.StatusCode, err)
	}
	counters := nativeAuthRefreshNativeCounters(t, native.client)
	seen := upstream.snapshot()
	if counters.Refresh != 1 || counters.ConsistencyErrors != 0 || len(seen) != 2 || seen[1].Authorization != "Bearer synthetic-access-valid" || store.saves.Load() != 2 {
		t.Fatalf("distinct login inherited the old rotation: refresh=%d requests=%d save=%d", counters.Refresh, len(seen), store.saves.Load())
	}
	current, _ = manager.GetByID(auth.ID)
	if current.Metadata["refresh_token"] != "synthetic-refresh-valid" {
		t.Fatal("successful inference replaced the distinct login with a pending grant")
	}
	nativeAuthRefreshWriteJSON(t, filepath.Join(native.root, "distinct-login-receipt.json"), map[string]any{"native_refresh_calls": counters.Refresh, "upstream_requests": len(seen), "save_calls": store.saves.Load(), "http_status": resp.StatusCode})
}
