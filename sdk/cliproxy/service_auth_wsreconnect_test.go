package cliproxy

import (
	"context"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/watcher"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// A reconnecting websocket channel must always (re)emit its auth Add: the
// previous session's queued Delete can leave the stored auth looking active,
// and skipping the Add on that stale state leaves the live connection without
// an auth entry once the Delete lands (#5392, Race A).
func TestService_WsOnConnected_AlwaysEmitsAuthAdd(t *testing.T) {
	service := &Service{
		coreManager: coreauth.NewManager(nil, nil, nil),
		authUpdates: make(chan watcher.AuthUpdate, 8),
	}
	auth := &coreauth.Auth{
		ID:       "aistudio-reconnect-race",
		Provider: "aistudio",
		Status:   coreauth.StatusActive,
	}
	if _, errRegister := service.coreManager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	service.wsOnConnected(auth.ID)

	select {
	case update := <-service.authUpdates:
		if update.Action != watcher.AuthUpdateActionAdd {
			t.Fatalf("update action = %q, want add", update.Action)
		}
		if update.ID != auth.ID {
			t.Fatalf("update id = %q, want %q", update.ID, auth.ID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no auth update emitted for an already-active channel auth; the stale-state guard dropped the reconnect's Add")
	}
}
