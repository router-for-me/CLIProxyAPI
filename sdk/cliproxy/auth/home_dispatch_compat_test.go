package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/home"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

type legacyHomeDispatchFallback struct {
	identityCalls int
	legacyCalls   int
}

func (d *legacyHomeDispatchFallback) HeartbeatOK() bool { return true }

func (d *legacyHomeDispatchFallback) SupportsLegacyAuthDispatchFallback() bool { return true }

func (d *legacyHomeDispatchFallback) RPopAuth(context.Context, string, string, http.Header, int) ([]byte, error) {
	d.legacyCalls++
	return json.Marshal(homeAuthDispatchResponse{
		Auth: Auth{ID: "legacy-home-auth", Provider: "test", Status: StatusActive},
	})
}

func (d *legacyHomeDispatchFallback) RPopAuthWithIdentity(context.Context, string, string, string, string, http.Header, int) ([]byte, error) {
	d.identityCalls++
	return nil, home.ErrAuthNotFound
}

func TestHomeDispatchFallsBackToLegacyKeyWhenIdentityKeyIsMissing(t *testing.T) {
	dispatcher := &legacyHomeDispatchFallback{}
	oldCurrentHomeDispatcher := currentHomeDispatcher
	currentHomeDispatcher = func() homeAuthDispatcher { return dispatcher }
	t.Cleanup(func() { currentHomeDispatcher = oldCurrentHomeDispatcher })

	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
	manager.RegisterExecutor(schedulerTestExecutor{})

	auth, _, provider, errPick := manager.pickNextViaHome(context.Background(), "model-a", cliproxyexecutor.Options{}, nil)
	if errPick != nil {
		t.Fatalf("pickNextViaHome() error = %v", errPick)
	}
	if auth == nil || auth.ID != "legacy-home-auth" || provider != "test" {
		t.Fatalf("pickNextViaHome() = auth:%#v provider:%q", auth, provider)
	}
	if dispatcher.identityCalls != 1 || dispatcher.legacyCalls != 1 {
		t.Fatalf("dispatch calls = identity:%d legacy:%d, want 1 each", dispatcher.identityCalls, dispatcher.legacyCalls)
	}
}
