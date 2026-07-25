package auth

import (
	"context"
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

// TestManagerExecute_AuthSupportsRouteModelForkFalseUpstreamRoute reproduces the
// 503 "auth_unavailable: no auth available" regression where:
//
//   - an OAuth provider exposes model "minimax-m3" (upstream name) with a
//     request-facing alias "opencode-mm3" and fork=false;
//   - applyOAuthModelAliasEntries therefore registers ONLY the alias id
//     "opencode-mm3" for the client (the upstream id "minimax-m3" is dropped
//     to avoid exposing it);
//   - a per-API-key model_route pins the model "minimax-m3" (the upstream
//     name) to the provider, so the request arrives naming "minimax-m3".
//
// Before the fix, authSupportsRouteModel rejected every candidate because
// neither the route key "minimax-m3" nor selectionModelForAuth's alias
// resolution (which only looks up alias -> upstream, not upstream -> alias)
// matched the registered alias id "opencode-mm3", producing the 503. The
// runtime alias resolver (executionModelCandidatesWithAlias) WOULD have
// mapped "minimax-m3" correctly, but the selection gate disqualified the
// auth first.
func TestManagerExecute_AuthSupportsRouteModelForkFalseUpstreamRoute(t *testing.T) {
	const (
		provider     = "openai-compatible-opencode"
		aliasName    = "minimax-m3"   // upstream name the request names
		requestAlias = "opencode-mm3" // request-facing alias (registered id)
	)

	manager := NewManager(nil, nil, nil)
	executor := &aliasRoutingExecutor{id: provider}
	manager.RegisterExecutor(executor)
	manager.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{
		provider: {{
			Name:  aliasName,
			Alias: requestAlias,
			Fork:  false,
		}},
	})

	// Per-auth attribute carries the alias so authSupportsRouteModelViaAlias
	// can match the upstream name to the registered alias id. The attribute
	// is set before registration so the scheduler's supportedModelSet
	// snapshot (built in buildScheduledAuthMeta) includes both sides of the
	// alias pair.
	auth := &Auth{
		ID:       "oauth-forkfalse-auth",
		Provider: provider,
		Status:   StatusActive,
	}
	SetOAuthModelAliasesAttribute(auth, []internalconfig.OAuthModelAlias{{
		Name:  aliasName,
		Alias: requestAlias,
		Fork:  false,
	}})
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	// Mirror the fork=false registration: only the alias id is registered.
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, provider, []*registry.ModelInfo{{ID: requestAlias}})
	t.Cleanup(func() {
		reg.UnregisterClient(auth.ID)
	})
	manager.RefreshSchedulerEntry(auth.ID)

	// The selection gate must accept the auth even though only the alias id
	// is registered, because the route key "minimax-m3" is the upstream name
	// that the alias maps FROM.
	if !manager.authSupportsRouteModel(reg, auth, aliasName) {
		t.Fatalf("authSupportsRouteModel(registry, auth, %q) = false, want true (fork=false upstream-name route)", aliasName)
	}

	resp, errExecute := manager.Execute(context.Background(), []string{provider}, cliproxyexecutor.Request{Model: aliasName}, cliproxyexecutor.Options{})
	if errExecute != nil {
		t.Fatalf("execute error = %v, want success", errExecute)
	}
	if string(resp.Payload) != aliasName {
		t.Fatalf("execute payload = %q, want %q", string(resp.Payload), aliasName)
	}
}

// TestManagerExecute_AuthSupportsRouteModelForkFalseAliasRoute covers the
// inverse fork=false configuration: the request names the request-facing
// alias WHILE only the upstream id is registered (e.g. when the alias id is
// dropped). The gate must still accept the auth.
func TestManagerExecute_AuthSupportsRouteModelForkFalseAliasRoute(t *testing.T) {
	const (
		provider     = "openai-compatible-opencode"
		aliasName    = "MiniMax-M3"   // upstream (vendor-style, differs from alias)
		requestAlias = "opencode-mm3" // request-facing alias; only upstream id is registered here
	)

	manager := NewManager(nil, nil, nil)
	executor := &aliasRoutingExecutor{id: provider}
	manager.RegisterExecutor(executor)
	manager.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{
		provider: {{
			Name:  aliasName,
			Alias: requestAlias,
			Fork:  false,
		}},
	})

	auth := &Auth{
		ID:       "oauth-forkfalse-alias-auth",
		Provider: provider,
		Status:   StatusActive,
	}
	SetOAuthModelAliasesAttribute(auth, []internalconfig.OAuthModelAlias{{
		Name:  aliasName,
		Alias: requestAlias,
		Fork:  false,
	}})
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	reg := registry.GetGlobalRegistry()
	// Only the upstream id is registered for this case.
	reg.RegisterClient(auth.ID, provider, []*registry.ModelInfo{{ID: aliasName}})
	t.Cleanup(func() {
		reg.UnregisterClient(auth.ID)
	})
	manager.RefreshSchedulerEntry(auth.ID)

	if !manager.authSupportsRouteModel(reg, auth, requestAlias) {
		t.Fatalf("authSupportsRouteModel(registry, auth, %q) = false, want true (fork=false alias-name route)", requestAlias)
	}

	resp, errExecute := manager.Execute(context.Background(), []string{provider}, cliproxyexecutor.Request{Model: requestAlias}, cliproxyexecutor.Options{})
	if errExecute != nil {
		t.Fatalf("execute error = %v, want success", errExecute)
	}
	if string(resp.Payload) != aliasName {
		t.Fatalf("execute payload = %q, want %q", string(resp.Payload), aliasName)
	}
}
