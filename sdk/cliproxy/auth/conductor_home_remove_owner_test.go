package auth

import (
	"context"
	"testing"
)

func TestRemoveIfCleansHomeRuntimeAuthOwners(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, nil, nil)

	auth1, errReg1 := manager.Register(ctx, &Auth{
		ID:       "auth-1",
		Provider: "codex",
		Metadata: map[string]any{"access_token": "token-1"},
	})
	if errReg1 != nil {
		t.Fatalf("Register(auth-1) error = %v", errReg1)
	}
	auth2, errReg2 := manager.Register(ctx, &Auth{
		ID:       "auth-2",
		Provider: "codex",
		Metadata: map[string]any{"access_token": "token-2"},
	})
	if errReg2 != nil {
		t.Fatalf("Register(auth-2) error = %v", errReg2)
	}

	sel1 := &HomeDispatchSelection{Auth: auth1.Clone()}
	sel2 := &HomeDispatchSelection{Auth: auth2.Clone()}

	manager.rememberHomeSelectionRuntimeAuth("session-solo", sel1)
	manager.rememberHomeSelectionRuntimeAuth("session-shared", sel1)
	manager.rememberHomeSelectionRuntimeAuth("session-shared", sel2)

	if removed := manager.RemoveIf(ctx, "auth-1", nil); !removed {
		t.Fatal("RemoveIf(auth-1) = false, want true")
	}

	manager.mu.RLock()
	_, soloAuthExists := manager.homeRuntimeAuths["session-solo"]
	_, soloOwnerExists := manager.homeRuntimeAuthOwners["session-solo"]
	_, sharedAuth1Exists := manager.homeRuntimeAuths["session-shared"]["auth-1"]
	_, sharedOwner1Exists := manager.homeRuntimeAuthOwners["session-shared"]["auth-1"]
	sharedAuth2 := manager.homeRuntimeAuths["session-shared"]["auth-2"]
	sharedOwner2 := manager.homeRuntimeAuthOwners["session-shared"]["auth-2"]
	manager.mu.RUnlock()

	if soloAuthExists {
		t.Fatal("session-solo still present in homeRuntimeAuths after RemoveIf")
	}
	if soloOwnerExists {
		t.Fatal("session-solo still present in homeRuntimeAuthOwners after RemoveIf")
	}
	if sharedAuth1Exists {
		t.Fatal("auth-1 still present in homeRuntimeAuths[session-shared] after RemoveIf")
	}
	if sharedOwner1Exists {
		t.Fatal("auth-1 still present in homeRuntimeAuthOwners[session-shared] after RemoveIf")
	}
	if sharedAuth2 == nil || sharedAuth2.ID != "auth-2" {
		t.Fatalf("homeRuntimeAuths[session-shared][auth-2] = %#v, want auth-2", sharedAuth2)
	}
	if sharedOwner2 != sel2 {
		t.Fatalf("homeRuntimeAuthOwners[session-shared][auth-2] = %p, want %p", sharedOwner2, sel2)
	}

	manager.replaceHomeSelectionAuth(sel1, &Auth{
		ID:       "auth-1",
		Provider: "codex",
		Metadata: map[string]any{"access_token": "refreshed-1"},
	})

	manager.mu.RLock()
	_, resurrectedShared := manager.homeRuntimeAuths["session-shared"]["auth-1"]
	_, resurrectedSolo := manager.homeRuntimeAuths["session-solo"]
	stillAuth2 := manager.homeRuntimeAuths["session-shared"]["auth-2"]
	manager.mu.RUnlock()

	if resurrectedShared {
		t.Fatal("replaceHomeSelectionAuth resurrected auth-1 in homeRuntimeAuths[session-shared]")
	}
	if resurrectedSolo {
		t.Fatal("replaceHomeSelectionAuth resurrected session-solo in homeRuntimeAuths")
	}
	if stillAuth2 == nil || stillAuth2.ID != "auth-2" {
		t.Fatalf("homeRuntimeAuths[session-shared][auth-2] after replace = %#v, want auth-2", stillAuth2)
	}
}

func TestReplaceHomeSelectionAuthDoesNotResurrectRemovedAuth(t *testing.T) {
	staleSel := &HomeDispatchSelection{Auth: &Auth{
		ID:       "auth-1",
		Provider: "codex",
		Metadata: map[string]any{"access_token": "old-1"},
	}}
	activeSel := &HomeDispatchSelection{Auth: &Auth{
		ID:       "auth-2",
		Provider: "codex",
		Metadata: map[string]any{"access_token": "old-2"},
	}}

	manager := &Manager{
		homeRuntimeAuths: map[string]map[string]*Auth{
			"session-shared": {
				"auth-2": activeSel.Auth.Clone(),
			},
		},
		homeRuntimeAuthOwners: map[string]map[string]*HomeDispatchSelection{
			"session-shared": {
				"auth-1": staleSel,
				"auth-2": activeSel,
			},
		},
	}

	manager.replaceHomeSelectionAuth(staleSel, &Auth{
		ID:       "auth-1",
		Provider: "codex",
		Metadata: map[string]any{"access_token": "refreshed-1"},
	})

	if _, resurrected := manager.homeRuntimeAuths["session-shared"]["auth-1"]; resurrected {
		t.Fatal("replaceHomeSelectionAuth resurrected missing auth-1 into homeRuntimeAuths[session-shared]")
	}

	manager.replaceHomeSelectionAuth(activeSel, &Auth{
		ID:       "auth-2",
		Provider: "codex",
		Metadata: map[string]any{"access_token": "refreshed-2"},
	})

	retained := manager.homeRuntimeAuths["session-shared"]["auth-2"]
	if retained == nil || retained.Metadata["access_token"] != "refreshed-2" {
		t.Fatalf("retained auth-2 = %#v, want refreshed-2", retained)
	}
}
