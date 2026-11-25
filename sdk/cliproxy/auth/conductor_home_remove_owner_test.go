package auth

import (
	"context"
	"testing"
)

func TestRemoveIfCleansHomeRuntimeAuthOwners(t *testing.T) {
	ctx := context.Background()
	m := NewManager(nil, nil, nil)

	auth1 := &Auth{ID: "auth-1", Provider: "codex", Metadata: map[string]any{"access_token": "tok-1"}}
	auth2 := &Auth{ID: "auth-2", Provider: "codex", Metadata: map[string]any{"access_token": "tok-2"}}
	if _, err := m.Register(ctx, auth1); err != nil {
		t.Fatalf("Register(auth1) error = %v", err)
	}
	if _, err := m.Register(ctx, auth2); err != nil {
		t.Fatalf("Register(auth2) error = %v", err)
	}

	sel1 := &HomeDispatchSelection{Auth: auth1.Clone(), Provider: "codex"}
	sel2 := &HomeDispatchSelection{Auth: auth2.Clone(), Provider: "codex"}

	// Shared session holds two auths; removing auth-1 must clean its owner entry
	// without disturbing auth-2, and replaceHomeSelectionAuth must not resurrect auth-1.
	m.rememberHomeSelectionRuntimeAuth("session-shared", sel1)
	m.rememberHomeSelectionRuntimeAuth("session-shared", sel2)
	// Single-auth session holds only auth-2; removing auth-2 must delete both outer session maps.
	m.rememberHomeSelectionRuntimeAuth("session-only", sel2)

	if removed := m.RemoveIf(ctx, "auth-1", nil); !removed {
		t.Fatal("RemoveIf(auth-1) = false, want true")
	}

	m.mu.RLock()
	_, auth1InRuntime := m.homeRuntimeAuths["session-shared"]["auth-1"]
	_, auth2InRuntime := m.homeRuntimeAuths["session-shared"]["auth-2"]
	_, auth1InOwners := m.homeRuntimeAuthOwners["session-shared"]["auth-1"]
	_, auth2InOwners := m.homeRuntimeAuthOwners["session-shared"]["auth-2"]
	m.mu.RUnlock()

	if auth1InRuntime {
		t.Fatal("auth-1 still present in homeRuntimeAuths[session-shared] after RemoveIf")
	}
	if !auth2InRuntime {
		t.Fatal("auth-2 unexpectedly removed from homeRuntimeAuths[session-shared]")
	}
	if auth1InOwners {
		t.Fatal("auth-1 still present in homeRuntimeAuthOwners[session-shared] after RemoveIf")
	}
	if !auth2InOwners {
		t.Fatal("auth-2 unexpectedly removed from homeRuntimeAuthOwners[session-shared]")
	}

	refreshedAuth1 := &Auth{ID: "auth-1", Provider: "codex", Metadata: map[string]any{"access_token": "tok-1-refreshed"}}
	m.replaceHomeSelectionAuth(sel1, refreshedAuth1)

	m.mu.RLock()
	_, auth1Resurrected := m.homeRuntimeAuths["session-shared"]["auth-1"]
	m.mu.RUnlock()
	if auth1Resurrected {
		t.Fatal("replaceHomeSelectionAuth resurrected removed auth-1 in homeRuntimeAuths[session-shared]")
	}

	if removed := m.RemoveIf(ctx, "auth-2", nil); !removed {
		t.Fatal("RemoveIf(auth-2) = false, want true")
	}

	m.mu.RLock()
	_, sharedRuntimeExists := m.homeRuntimeAuths["session-shared"]
	_, sharedOwnersExists := m.homeRuntimeAuthOwners["session-shared"]
	_, onlyRuntimeExists := m.homeRuntimeAuths["session-only"]
	_, onlyOwnersExists := m.homeRuntimeAuthOwners["session-only"]
	m.mu.RUnlock()

	if sharedRuntimeExists || sharedOwnersExists {
		t.Fatalf("session-shared maps not cleaned after last auth removed: runtime=%v owners=%v", sharedRuntimeExists, sharedOwnersExists)
	}
	if onlyRuntimeExists || onlyOwnersExists {
		t.Fatalf("session-only maps not cleaned after last auth removed: runtime=%v owners=%v", onlyRuntimeExists, onlyOwnersExists)
	}

	// Defense-in-depth: even if a stale owner entry exists while the auth is absent
	// from homeRuntimeAuths[sessionID], replaceHomeSelectionAuth must not re-insert it.
	liveAuth := &Auth{ID: "auth-live", Provider: "codex", Metadata: map[string]any{"access_token": "tok-live"}}
	staleAuth := &Auth{ID: "auth-stale", Provider: "codex", Metadata: map[string]any{"access_token": "tok-stale"}}
	liveSel := &HomeDispatchSelection{Auth: liveAuth.Clone(), Provider: "codex"}
	staleSel := &HomeDispatchSelection{Auth: staleAuth.Clone(), Provider: "codex"}

	m.rememberHomeSelectionRuntimeAuth("session-stale", liveSel)
	m.mu.Lock()
	m.homeRuntimeAuthOwners["session-stale"]["auth-stale"] = staleSel
	m.mu.Unlock()

	m.replaceHomeSelectionAuth(staleSel, &Auth{ID: "auth-stale", Provider: "codex", Metadata: map[string]any{"access_token": "tok-stale-new"}})

	m.mu.RLock()
	_, staleResurrected := m.homeRuntimeAuths["session-stale"]["auth-stale"]
	m.mu.RUnlock()
	if staleResurrected {
		t.Fatal("replaceHomeSelectionAuth inserted auth-stale into homeRuntimeAuths despite missing runtime entry")
	}
}
