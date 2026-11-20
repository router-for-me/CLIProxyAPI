package cmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// refreshExecutor is the provider's own executor, whose Refresh holds its OAuth
// client; nil for a provider with nothing to refresh (API keys).
func refreshExecutor(cfg *config.Config, provider string) cliproxyauth.ProviderExecutor {
	switch strings.ToLower(provider) {
	case "antigravity":
		return executor.NewAntigravityExecutor(cfg)
	case "codex":
		return executor.NewCodexExecutor(cfg)
	case "claude":
		return executor.NewClaudeExecutor(cfg)
	case "gemini", "gemini-cli":
		return executor.NewGeminiExecutor(cfg)
	case "kimi", "kimi-ai", "kimi.ai", "kimi.com":
		return executor.NewKimiExecutor(cfg)
	case "xai":
		return executor.NewXAIExecutor(cfg)
	case "devin":
		return executor.NewDevinExecutor(cfg)
	case "meta":
		return executor.NewMetaExecutor(cfg)
	}
	return nil
}

// DoRefresh refreshes every OAuth sign-in in the auth dir that the proxy's own
// auto-refresh would refresh now, saves it and exits. It lets a tool that only
// reads tokens (a quota or usage reader) keep sign-ins alive with no proxy running.
func DoRefresh(cfg *config.Config) int {
	dir, err := util.ResolveAuthDir(cfg.AuthDir)
	if err != nil {
		fmt.Println(err)
		return 1
	}
	store := sdkAuth.NewFileTokenStore()
	store.SetBaseDir(dir)
	ctx := context.Background()
	auths, err := store.List(ctx)
	if err != nil {
		fmt.Println(err)
		return 1
	}
	manager := cliproxyauth.NewManager(store, nil, nil)
	manager.SetConfig(cfg)
	for _, a := range auths {
		if exec := refreshExecutor(cfg, a.Provider); exec != nil {
			manager.RegisterExecutor(exec)
		}
	}
	if err := manager.Load(ctx); err != nil {
		fmt.Println(err)
		return 1
	}
	code := 0
	for _, r := range manager.RefreshDue(ctx) {
		if r.Success {
			fmt.Printf("%s: refreshed\n", r.ID)
			continue
		}
		fmt.Printf("%s: refresh failed: %s\n", r.ID, r.Error)
		code = 1
	}
	return code
}
