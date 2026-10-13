package auth

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codex"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestManagerCodexPendingPatchPreservesOperatorUpdate(t *testing.T) {
	for _, partial := range []bool{false, true} {
		for _, refreshOnly := range []bool{false, true} {
			name := map[bool]string{false: "operator-update", true: "pure-refresh"}[refreshOnly] + map[bool]string{false: "/outer", true: "/partial-both"}[partial]
			t.Run(name, func(t *testing.T) {
				ctx := context.Background()
				dir := t.TempDir()
				path := filepath.Join(dir, "codex.json")
				store := NewFileTokenStore()
				store.SetBaseDir(dir)
				manager := coreauth.NewManager(store, nil, nil)
				initial := codexRefreshFixture()
				initial.Status = coreauth.StatusActive
				if _, err := manager.Register(ctx, initial); err != nil {
					t.Fatal(err)
				}
				base, _ := manager.GetByID(initial.ID)
				rotated := base.Clone()
				rotated.Metadata["access_token"] = "outer-new"
				rotated.Metadata["refresh_token"] = "outer-ref-new"
				if partial {
					rotated.Metadata["siwc"].(map[string]any)["access_token"] = "siwc-new"
					rotated.Metadata["siwc"].(map[string]any)["refresh_token"] = "siwc-ref-new"
				}
				rotated.CodexRefreshPatch = codex.NewCredentialRefreshPatch(base.Metadata, rotated.Metadata, true, partial)
				// Reproduce the published pending state after a failed rotation save without
				// platform-dependent permission faults (covered by the executor regressions).
				if _, err := manager.Update(coreauth.WithSkipPersist(ctx), rotated); err != nil {
					t.Fatal(err)
				}
				pending, _ := manager.GetByID(initial.ID)
				if pending.CodexRefreshPatch == nil {
					t.Fatal("pending rotation fixture lost")
				}
				disk := base.Clone().Metadata
				if refreshOnly || partial {
					disk["note"] = "disk-operator"
					disk["disabled"] = true
					disk["use_siwc"] = false
					disk["disk-only"] = "keep"
					delete(disk, "proxy_url")
				}
				if partial {
					disk["access_token"] = "outer-concurrent"
					disk["refresh_token"] = "outer-ref-concurrent"
				} else {
					disk["siwc"].(map[string]any)["access_token"] = "siwc-concurrent"
					disk["siwc"].(map[string]any)["refresh_token"] = "siwc-ref-concurrent"
				}
				writeCodexRefreshFixture(t, path, disk)
				request := pending.Clone()
				wantNote := "disk-operator"
				if !refreshOnly {
					request.Disabled = true
					request.Status = coreauth.StatusDisabled
					request.Metadata["disabled"] = true
					request.Metadata["note"] = "operator-new"
					request.Metadata["use_siwc"] = false
					delete(request.Metadata, "proxy_url")
					wantNote = "operator-new"
				}
				var updated *coreauth.Auth
				var err error
				if refreshOnly {
					updated, err = manager.UpdateRefreshedAuth(ctx, pending, request)
				} else {
					updated, err = manager.Update(ctx, request)
				}
				if err != nil {
					t.Fatal(err)
				}
				saved := readCodexRefreshFixture(t, path)
				reloaded, err := store.List(ctx)
				if err != nil || len(reloaded) != 1 {
					t.Fatalf("reload failed: count=%d err=%v", len(reloaded), err)
				}
				runtime, ok := manager.GetByID(initial.ID)
				if !ok {
					t.Fatal("updated runtime auth missing")
				}
				for _, a := range []*coreauth.Auth{updated, runtime, reloaded[0]} {
					if !a.Disabled || a.Metadata["note"] != wantNote || a.Metadata["use_siwc"] != false {
						t.Fatal("operator configuration lost from runtime or reloaded auth")
					}
					if _, exists := a.Metadata["proxy_url"]; exists {
						t.Fatal("deleted operator field restored")
					}
				}
				if !refreshOnly && updated.Status != coreauth.StatusDisabled {
					t.Fatal("operator disabled status lost")
				}
				if saved["disabled"] != true || saved["note"] != wantNote {
					t.Fatal("operator update lost from disk")
				}
				if refreshOnly && saved["disk-only"] != "keep" {
					t.Fatal("pure refresh lost new disk property")
				}
				wantOuter, wantSIWC := "outer-new", "siwc-concurrent"
				if partial {
					wantOuter, wantSIWC = "outer-concurrent", "siwc-new"
				}
				section := saved["siwc"].(map[string]any)
				if saved["access_token"] != wantOuter || section["access_token"] != wantSIWC ||
					saved["refresh_token"] != strings.Replace(wantOuter, "outer-", "outer-ref-", 1) ||
					section["refresh_token"] != strings.Replace(wantSIWC, "siwc-", "siwc-ref-", 1) {
					t.Fatal("pending or independently rotated partition lost")
				}
				if updated.CodexRefreshPatch != nil {
					t.Fatal("successfully persisted pending patch not cleared")
				}
			})
		}
	}
}

// A pending rotation must not let an operator update recreate a SIWC partition
// that was removed on disk after the runtime snapshot.
func TestManagerCodexPendingPatchUpdateKeepsRemovedDiskSIWCAbsent(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "codex.json")
	store := NewFileTokenStore()
	store.SetBaseDir(dir)
	manager := coreauth.NewManager(store, nil, nil)
	initial := codexRefreshFixture()
	if _, err := manager.Register(ctx, initial); err != nil {
		t.Fatal(err)
	}
	base, _ := manager.GetByID(initial.ID)
	rotated := base.Clone()
	rotated.Metadata["access_token"] = "outer-new"
	rotated.Metadata["refresh_token"] = "outer-ref-new"
	rotated.CodexRefreshPatch = codex.NewCredentialRefreshPatch(base.Metadata, rotated.Metadata, true, false)
	if _, err := manager.Update(coreauth.WithSkipPersist(ctx), rotated); err != nil {
		t.Fatal(err)
	}
	pending, _ := manager.GetByID(initial.ID)
	disk := readCodexRefreshFixture(t, path)
	delete(disk, "siwc")
	disk["use_siwc"] = false
	writeCodexRefreshFixture(t, path, disk)
	request := pending.Clone()
	request.Metadata["use_siwc"] = false
	request.Metadata["note"] = "operator-new"
	delete(request.Metadata, "proxy_url")
	updated, err := manager.Update(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	saved := readCodexRefreshFixture(t, path)
	reloaded, err := store.List(ctx)
	if err != nil || len(reloaded) != 1 {
		t.Fatalf("reload failed: count=%d err=%v", len(reloaded), err)
	}
	for _, metadata := range []map[string]any{saved, updated.Metadata, reloaded[0].Metadata} {
		if _, exists := metadata["siwc"]; exists {
			t.Fatal("pending-patch configuration update resurrected SIWC removed on disk")
		}
		if metadata["refresh_token"] != "outer-ref-new" || metadata["note"] != "operator-new" || metadata["use_siwc"] != false {
			t.Fatal("pending rotation or operator configuration lost")
		}
		if _, exists := metadata["proxy_url"]; exists {
			t.Fatal("deleted operator field restored")
		}
	}
}
