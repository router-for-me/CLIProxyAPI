package auth

import (
	"context"
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestOAuthModelAliasCaseOnlyExecution(t *testing.T) {
	const provider = "opencode-go"
	const upstream = "minimax-m3.1-flash"
	const alias = "MiniMax-M3.1-Flash"
	for _, scope := range []string{"global", "credential"} {
		t.Run(scope, func(t *testing.T) {
			manager := NewManager(nil, nil, nil)
			manager.SetConfig(&internalconfig.Config{})
			executor := &noForkAliasTestExecutor{id: provider}
			manager.RegisterExecutor(executor)
			mappings := []internalconfig.OAuthModelAlias{{Name: upstream, Alias: alias}}
			auth := &Auth{ID: t.Name(), Provider: provider, Status: StatusActive}
			if scope == "global" {
				manager.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{provider: mappings})
			} else {
				SetOAuthModelAliasesAttribute(auth, mappings)
			}
			if _, err := manager.Register(context.Background(), auth); err != nil {
				t.Fatal(err)
			}
			reg := registry.GetGlobalRegistry()
			reg.RegisterClient(auth.ID, provider, []*registry.ModelInfo{{ID: alias}})
			t.Cleanup(func() { reg.UnregisterClient(auth.ID) })
			manager.RefreshSchedulerEntry(auth.ID)
			for _, request := range []struct{ model, want string }{
				{alias, upstream},
				{"MINIMAX-M3.1-FLASH", upstream},
				{alias + "(high)", upstream + "(high)"},
				{upstream, upstream},
			} {
				response, err := manager.Execute(context.Background(), []string{provider}, cliproxyexecutor.Request{Model: request.model}, cliproxyexecutor.Options{})
				if err != nil || string(response.Payload) != request.want {
					t.Fatalf("execute %q = %q, %v; want upstream %q", request.model, response.Payload, err, request.want)
				}
			}
		})
	}
}

func TestOAuthModelAliasCaseOnlyForceMapping(t *testing.T) {
	const upstream = "minimax-m3.1-flash"
	const alias = "MiniMax-M3.1-Flash"
	for _, scope := range []string{"global", "credential"} {
		t.Run(scope, func(t *testing.T) {
			manager := NewManager(nil, nil, nil)
			manager.SetConfig(&internalconfig.Config{})
			mappings := []internalconfig.OAuthModelAlias{{Name: upstream, Alias: alias, ForceMapping: true}}
			auth := &Auth{ID: t.Name(), Provider: "opencode-go"}
			if scope == "global" {
				manager.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{auth.Provider: mappings})
			} else {
				SetOAuthModelAliasesAttribute(auth, mappings)
			}
			got := manager.resolveOAuthModelAliasWithResult(auth, alias+"(high)")
			if got.UpstreamModel != upstream+"(high)" || !got.ForceMapping || got.OriginalAlias != alias {
				t.Fatalf("case-only force mapping = %+v", got)
			}
		})
	}
}
