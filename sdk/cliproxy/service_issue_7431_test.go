package cliproxy

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

type noopServiceIssue7431Executor struct {
	provider string
}

func (e noopServiceIssue7431Executor) Identifier() string { return e.provider }

func (e noopServiceIssue7431Executor) Execute(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{Payload: []byte(`{"ok":true}`)}, nil
}

func (e noopServiceIssue7431Executor) ExecuteStream(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, nil
}

func (e noopServiceIssue7431Executor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e noopServiceIssue7431Executor) CountTokens(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e noopServiceIssue7431Executor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func TestIssue7431_ServiceApplyManagerConfigPreservesWeightedSelectorAcrossAffinityReload(t *testing.T) {
	ctx := coreauth.WithSkipPersist(t.Context())
	const (
		provider = "issue-7431-svc-wrr"
		model    = "issue-7431-svc-model"
	)
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(noopServiceIssue7431Executor{provider: provider})
	service := &Service{coreManager: manager}

	initialCfg := &internalconfig.Config{
		Routing: internalconfig.RoutingConfig{
			Strategy:        "weighted-round-robin",
			SessionAffinity: false,
		},
	}
	if !service.applyManagerConfig(ctx, configCommit{cfg: initialCfg, sequence: 1}) {
		t.Fatal("initial applyManagerConfig failed")
	}

	reg := registry.GetGlobalRegistry()
	var auths []*coreauth.Auth
	for i := 0; i < 4; i++ {
		id := fmt.Sprintf("svc-wrr-auth-%02d", i)
		a := &coreauth.Auth{ID: id, Provider: provider, Status: coreauth.StatusActive}
		auths = append(auths, a)
		reg.RegisterClient(id, provider, []*registry.ModelInfo{{ID: model}})
		if _, errReg := manager.Register(ctx, a); errReg != nil {
			t.Fatalf("register %s: %v", id, errReg)
		}
		manager.RefreshSchedulerEntry(id)
	}
	t.Cleanup(func() {
		for _, a := range auths {
			reg.UnregisterClient(a.ID)
		}
	})

	var lastPickedID string
	manager.RegisterExecutor(&funcIssue7431Executor{
		provider: provider,
		onExec: func(a *coreauth.Auth) {
			lastPickedID = a.ID
		},
	})

	var pickedBefore []string
	for i := 0; i < 2; i++ {
		if _, errExec := manager.Execute(ctx, []string{provider}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{}); errExec != nil {
			t.Fatalf("execute %d failed: %v", i, errExec)
		}
		pickedBefore = append(pickedBefore, lastPickedID)
	}
	if pickedBefore[0] != auths[0].ID || pickedBefore[1] != auths[1].ID {
		t.Fatalf("picks before reload = %v, want [%s %s]", pickedBefore, auths[0].ID, auths[1].ID)
	}

	// Reload routing config changing session-affinity while keeping strategy: weighted-round-robin.
	reloadedCfg := &internalconfig.Config{
		Routing: internalconfig.RoutingConfig{
			Strategy:           "weighted-round-robin",
			SessionAffinity:    true,
			SessionAffinityTTL: "30m",
		},
	}
	if !service.applyManagerConfig(ctx, configCommit{cfg: reloadedCfg, sequence: 2}) {
		t.Fatal("reload applyManagerConfig failed")
	}

	if _, errExec := manager.Execute(ctx, []string{provider}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{}); errExec != nil {
		t.Fatalf("execute after reload failed: %v", errExec)
	}
	if lastPickedID != auths[2].ID {
		t.Fatalf("pick after affinity config reload reset WRR to %q, want %q", lastPickedID, auths[2].ID)
	}
}

type funcIssue7431Executor struct {
	provider string
	onExec   func(*coreauth.Auth)
}

func (e funcIssue7431Executor) Identifier() string { return e.provider }

func (e funcIssue7431Executor) Execute(_ context.Context, auth *coreauth.Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	if e.onExec != nil {
		e.onExec(auth)
	}
	return cliproxyexecutor.Response{Payload: []byte(`{"ok":true}`)}, nil
}

func (e funcIssue7431Executor) ExecuteStream(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, nil
}

func (e funcIssue7431Executor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e funcIssue7431Executor) CountTokens(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e funcIssue7431Executor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}
