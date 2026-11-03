package auth

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codex"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func codexRefreshFixture() *cliproxyauth.Auth {
	return &cliproxyauth.Auth{
		ID: "codex.json", FileName: "codex.json", Provider: "codex",
		Metadata: map[string]any{
			"type": "codex", "access_token": "outer-old", "refresh_token": "outer-ref-old",
			"last_refresh": "2026-10-12T12:00:00Z", "use_siwc": true,
			"siwc": map[string]any{
				"access_token": "siwc-old", "refresh_token": "siwc-ref-old", "client_id": "client",
				"last_refresh": "2026-10-12T12:00:00Z", "note": "siwc-original",
			},
			"note": "original", "proxy_url": "http://original", "disabled": false,
		},
	}
}

func writeCodexRefreshFixture(t *testing.T, path string, metadata map[string]any) {
	t.Helper()
	if errWrite := codex.WriteJSONFileAtomic(path, metadata); errWrite != nil {
		t.Fatal(errWrite)
	}
}

func readCodexRefreshFixture(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, errRead := os.ReadFile(path)
	if errRead != nil {
		t.Fatal(errRead)
	}
	var metadata map[string]any
	if errUnmarshal := json.Unmarshal(raw, &metadata); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	return metadata
}

func TestFileTokenStoreCodexRefreshPreservesDiskConfiguration(t *testing.T) {
	for _, withStorage := range []bool{false, true} {
		for _, partition := range []string{"outer", "siwc", "both"} {
			t.Run(partition+map[bool]string{false: "/metadata", true: "/storage"}[withStorage], func(t *testing.T) {
				baseDir := t.TempDir()
				path := filepath.Join(baseDir, "codex.json")
				base := codexRefreshFixture()
				updated := base.Clone()
				outer, siwc := partition != "siwc", partition != "outer"
				if outer {
					updated.Metadata["access_token"] = "outer-new"
					updated.Metadata["refresh_token"] = "outer-ref-new"
				}
				if siwc {
					section := updated.Metadata["siwc"].(map[string]any)
					section["access_token"] = "siwc-new"
					section["refresh_token"] = "siwc-ref-new"
				}
				updated.CodexRefreshPatch = codex.NewCredentialRefreshPatch(base.Metadata, updated.Metadata, outer, siwc)
				if withStorage {
					updated.Storage = &codex.CodexTokenStorage{AccessToken: "stale-storage"}
				}
				// Operator changes do not change either second-resolution refresh timestamp.
				disk := base.Clone().Metadata
				disk["use_siwc"] = false
				disk["note"] = "disk-note"
				disk["new_property"] = map[string]any{"keep": true}
				disk["disabled"] = true
				delete(disk, "proxy_url")
				disk["siwc"].(map[string]any)["note"] = "disk-siwc-note"
				writeCodexRefreshFixture(t, path, disk)

				store := NewFileTokenStore()
				store.SetBaseDir(baseDir)
				if _, errSave := store.Save(context.Background(), updated); errSave != nil {
					t.Fatal(errSave)
				}
				saved := readCodexRefreshFixture(t, path)
				if saved["use_siwc"] != false || saved["note"] != "disk-note" || saved["disabled"] != true || saved["new_property"] == nil {
					t.Fatalf("disk configuration overwritten: %#v", saved)
				}
				if _, exists := saved["proxy_url"]; exists {
					t.Fatal("deleted disk property was restored")
				}
				section := saved["siwc"].(map[string]any)
				if section["note"] != "disk-siwc-note" {
					t.Fatalf("SIWC custom property overwritten: %#v", section)
				}
				wantOuter, wantSIWC := "outer-old", "siwc-old"
				if outer {
					wantOuter = "outer-new"
				}
				if siwc {
					wantSIWC = "siwc-new"
				}
				if saved["access_token"] != wantOuter || section["access_token"] != wantSIWC {
					t.Fatalf("wrong credential partitions saved: %#v", saved)
				}
				if updated.Metadata["use_siwc"] != false || updated.CodexRefreshPatch != nil || !updated.Disabled {
					t.Fatalf("saved auth was not synchronized: %#v", updated)
				}
			})
		}
	}
}

func TestFileTokenStoreCodexConcurrentSameTimestampRefresh(t *testing.T) {
	baseDir := t.TempDir()
	path := filepath.Join(baseDir, "codex.json")
	base := codexRefreshFixture()
	writeCodexRefreshFixture(t, path, base.Metadata)
	outer, siwc := base.Clone(), base.Clone()
	outer.Metadata["access_token"] = "outer-new"
	outer.Metadata["refresh_token"] = "outer-ref-new"
	outer.CodexRefreshPatch = codex.NewCredentialRefreshPatch(base.Metadata, outer.Metadata, true, false)
	siwc.Metadata["siwc"].(map[string]any)["access_token"] = "siwc-new"
	siwc.Metadata["siwc"].(map[string]any)["refresh_token"] = "siwc-ref-new"
	siwc.CodexRefreshPatch = codex.NewCredentialRefreshPatch(base.Metadata, siwc.Metadata, false, true)

	start := make(chan struct{})
	var wg sync.WaitGroup
	for _, updated := range []*cliproxyauth.Auth{outer, siwc} {
		wg.Add(1)
		go func(auth *cliproxyauth.Auth) {
			defer wg.Done()
			<-start
			store := NewFileTokenStore()
			store.SetBaseDir(baseDir)
			if _, errSave := store.Save(context.Background(), auth); errSave != nil {
				t.Errorf("concurrent save: %v", errSave)
			}
		}(updated)
	}
	close(start)
	wg.Wait()
	// A refresh started from the same old version must not overwrite rotated tokens.
	stale := base.Clone()
	stale.Metadata["access_token"] = "outer-stale-result"
	stale.Metadata["refresh_token"] = "outer-ref-stale-result"
	stale.Metadata["siwc"].(map[string]any)["access_token"] = "siwc-stale-result"
	stale.Metadata["siwc"].(map[string]any)["refresh_token"] = "siwc-ref-stale-result"
	stale.CodexRefreshPatch = codex.NewCredentialRefreshPatch(base.Metadata, stale.Metadata, true, true)
	store := NewFileTokenStore()
	store.SetBaseDir(baseDir)
	if _, errSave := store.Save(context.Background(), stale); errSave != nil {
		t.Fatal(errSave)
	}
	// A stale configuration snapshot is not a credential update either.
	if _, errSave := store.Save(context.Background(), base.Clone()); errSave != nil {
		t.Fatal(errSave)
	}
	saved := readCodexRefreshFixture(t, path)
	if saved["refresh_token"] != "outer-ref-new" || saved["siwc"].(map[string]any)["refresh_token"] != "siwc-ref-new" {
		t.Fatalf("same-timestamp token rotation lost: %#v", saved)
	}
}

func TestFileTokenStoreCodexLoginReplacementIsExplicit(t *testing.T) {
	baseDir := t.TempDir()
	path := filepath.Join(baseDir, "codex.json")
	base := codexRefreshFixture()
	writeCodexRefreshFixture(t, path, base.Metadata)
	store := NewFileTokenStore()
	store.SetBaseDir(baseDir)
	login := &cliproxyauth.Auth{
		ID: "codex.json", FileName: "codex.json", Provider: "codex",
		Storage: &codex.CodexTokenStorage{AccessToken: "login-new", RefreshToken: "login-ref-new"},
	}
	if _, errSave := store.Save(cliproxyauth.WithAuthCreationIntent(context.Background()), login); errSave != nil {
		t.Fatal(errSave)
	}
	stale := base.Clone()
	stale.Storage = &codex.CodexTokenStorage{AccessToken: "outer-old", RefreshToken: "outer-ref-old"}
	if _, errSave := store.Save(context.Background(), stale); errSave != nil {
		t.Fatal(errSave)
	}
	saved := readCodexRefreshFixture(t, path)
	if saved["refresh_token"] != "login-ref-new" || saved["siwc"].(map[string]any)["refresh_token"] != "siwc-ref-old" {
		t.Fatalf("login or stale storage save overwrote the wrong partition: %#v", saved)
	}
}
