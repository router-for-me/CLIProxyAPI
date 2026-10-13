package auth

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codex"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// Login-derived Codex auths keep CodexTokenStorage. An explicit Update must still
// persist configuration edits and deletions instead of the storage merge.
func TestManagerCodexStorageBackedUpdatePersistsConfiguration(t *testing.T) {
	for _, concurrentRotation := range []bool{false, true} {
		name := map[bool]string{false: "unchanged-disk", true: "concurrent-disk-rotation"}[concurrentRotation]
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			dir := t.TempDir()
			path := filepath.Join(dir, "codex.json")
			store := NewFileTokenStore()
			store.SetBaseDir(dir)
			manager := coreauth.NewManager(store, nil, nil)
			initial := codexRefreshFixture()
			initial.Status = coreauth.StatusActive
			initial.Storage = &codex.CodexTokenStorage{AccessToken: "outer-old", RefreshToken: "outer-ref-old"}
			if _, err := manager.Register(ctx, initial); err != nil {
				t.Fatal(err)
			}
			runtime, ok := manager.GetByID(initial.ID)
			if !ok || runtime.Storage == nil {
				t.Fatal("login storage unexpectedly absent")
			}
			if concurrentRotation {
				// Another process rotated both partitions after the runtime snapshot.
				disk := readCodexRefreshFixture(t, path)
				disk["access_token"] = "outer-disk-new"
				disk["refresh_token"] = "outer-ref-disk-new"
				disk["last_refresh"] = "2026-10-12T13:00:00Z"
				section := disk["siwc"].(map[string]any)
				section["access_token"] = "siwc-disk-new"
				section["refresh_token"] = "siwc-ref-disk-new"
				writeCodexRefreshFixture(t, path, disk)
			}
			update := runtime.Clone()
			update.Metadata["use_siwc"] = false
			update.Metadata["note"] = "operator-new"
			delete(update.Metadata, "proxy_url")
			updated, err := manager.Update(ctx, update)
			if err != nil {
				t.Fatal(err)
			}
			saved := readCodexRefreshFixture(t, path)
			reloaded, err := store.List(ctx)
			if err != nil || len(reloaded) != 1 {
				t.Fatalf("reload failed: count=%d err=%v", len(reloaded), err)
			}
			current, _ := manager.GetByID(initial.ID)
			for _, a := range []*coreauth.Auth{updated, current, reloaded[0]} {
				if a.Metadata["use_siwc"] != false || a.Metadata["note"] != "operator-new" {
					t.Fatal("operator configuration lost from runtime, disk or reload")
				}
				if _, exists := a.Metadata["proxy_url"]; exists {
					t.Fatal("deleted operator field restored")
				}
			}
			if saved["type"] != "codex" || reloaded[0].Provider != "codex" {
				t.Fatal("record type lost")
			}
			wantOuter, wantSIWC := "outer-ref-old", "siwc-ref-old"
			if concurrentRotation {
				wantOuter, wantSIWC = "outer-ref-disk-new", "siwc-ref-disk-new"
			}
			if saved["refresh_token"] != wantOuter || saved["siwc"].(map[string]any)["refresh_token"] != wantSIWC {
				t.Fatal("stale storage or runtime overwrote newer disk credentials")
			}
			if reloaded[0].Metadata["refresh_token"] != wantOuter {
				t.Fatal("reloaded credential version regressed")
			}
		})
	}
}

// Removing a disk partition after a login snapshot must not let a later
// configuration update recreate it from stale runtime or storage credentials.
func TestManagerCodexStorageBackedUpdateKeepsRemovedDiskCredentialsAbsent(t *testing.T) {
	for _, removed := range []string{"siwc", "outer", "both"} {
		t.Run(removed, func(t *testing.T) {
			ctx := context.Background()
			dir := t.TempDir()
			path := filepath.Join(dir, "codex.json")
			store := NewFileTokenStore()
			store.SetBaseDir(dir)
			manager := coreauth.NewManager(store, nil, nil)
			initial := codexRefreshFixture()
			initial.Storage = &codex.CodexTokenStorage{AccessToken: "outer-old", RefreshToken: "outer-ref-old"}
			if _, err := manager.Register(ctx, initial); err != nil {
				t.Fatal(err)
			}
			runtime, ok := manager.GetByID(initial.ID)
			if !ok || runtime.Storage == nil {
				t.Fatal("login storage unexpectedly absent")
			}
			disk := readCodexRefreshFixture(t, path)
			if removed == "siwc" || removed == "both" {
				delete(disk, "siwc")
			}
			if removed == "outer" || removed == "both" {
				for key := range disk {
					if codex.IsCodexCredentialKey(key) && key != "siwc" && key != "use_siwc" {
						delete(disk, key)
					}
				}
			} else {
				// The surviving partition was independently rotated on disk.
				disk["access_token"] = "outer-disk-new"
				disk["refresh_token"] = "outer-ref-disk-new"
				disk["last_refresh"] = "2026-10-12T13:00:00Z"
			}
			if removed == "outer" {
				section := disk["siwc"].(map[string]any)
				section["access_token"] = "siwc-disk-new"
				section["refresh_token"] = "siwc-ref-disk-new"
			}
			disk["use_siwc"] = false
			writeCodexRefreshFixture(t, path, disk)

			update := runtime.Clone()
			update.Metadata["use_siwc"] = false
			update.Metadata["note"] = "operator-new"
			delete(update.Metadata, "proxy_url")
			updated, err := manager.Update(ctx, update)
			if err != nil {
				t.Fatal(err)
			}
			saved := readCodexRefreshFixture(t, path)
			reloaded, err := store.List(ctx)
			if err != nil || len(reloaded) != 1 {
				t.Fatalf("reload failed: count=%d err=%v", len(reloaded), err)
			}
			current, _ := manager.GetByID(initial.ID)
			for _, metadata := range []map[string]any{saved, updated.Metadata, current.Metadata, reloaded[0].Metadata} {
				if metadata["use_siwc"] != false || metadata["note"] != "operator-new" {
					t.Fatal("operator configuration lost from runtime, disk or reload")
				}
				if _, exists := metadata["proxy_url"]; exists {
					t.Fatal("deleted operator field restored")
				}
				for key := range runtime.Metadata {
					if !codex.IsCodexCredentialKey(key) || key == "use_siwc" {
						continue
					}
					want, hadDisk := disk[key]
					got, hasSaved := metadata[key]
					if hadDisk != hasSaved || !reflect.DeepEqual(got, want) {
						t.Fatalf("configuration update resurrected or regressed disk credential partition field %q", key)
					}
				}
			}
			if saved["type"] != "codex" || reloaded[0].Provider != "codex" {
				t.Fatal("record type lost")
			}
		})
	}
}

// Storage created by a real login lacks a metadata record type; a configuration
// update must keep the file loadable and the login credentials intact.
func TestManagerCodexLoginShapedStorageUpdateKeepsRecordLoadable(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "codex.json")
	store := NewFileTokenStore()
	store.SetBaseDir(dir)
	manager := coreauth.NewManager(store, nil, nil)
	login := &coreauth.Auth{
		ID: "codex.json", FileName: "codex.json", Provider: "codex",
		Storage:  &codex.CodexTokenStorage{AccessToken: "login-access", RefreshToken: "login-ref", LastRefresh: "2026-10-12T12:00:00Z", Email: "user@example.test"},
		Metadata: map[string]any{"email": "user@example.test", "plan_type": "free"},
	}
	if _, err := manager.Register(coreauth.WithAuthCreationIntent(ctx), login); err != nil {
		t.Fatal(err)
	}
	runtime, _ := manager.GetByID(login.ID)
	update := runtime.Clone()
	update.Metadata["proxy_url"] = "http://operator.invalid"
	if _, err := manager.Update(ctx, update); err != nil {
		t.Fatal(err)
	}
	saved := readCodexRefreshFixture(t, path)
	if saved["type"] != "codex" || saved["proxy_url"] != "http://operator.invalid" || saved["refresh_token"] != "login-ref" {
		t.Fatal("login-shaped update lost type, configuration or credentials")
	}
	reloaded, err := store.List(ctx)
	if err != nil || len(reloaded) != 1 || reloaded[0].Provider != "codex" {
		t.Fatalf("reload failed: count=%d err=%v", len(reloaded), err)
	}
}
