package handlers

import (
	"context"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// AccountPoolContext attaches the account pools of the request's client API key
// to ctx. It resolves the current config on every call, so each request on a
// persistent WebSocket follows pool changes. A pooled key fails in Home mode,
// which selects credentials without regard to pools.
func (h *BaseAPIHandler) AccountPoolContext(ctx context.Context, c *gin.Context) (context.Context, *interfaces.ErrorMessage) {
	if h == nil || h.Cfg == nil || c == nil {
		return ctx, nil
	}
	policy, err := coreauth.ResolveAccountPools(h.Cfg.Client, c.GetString("userApiKey"))
	if err != nil {
		return ctx, accountPoolError(err)
	}
	if policy != nil && h.AuthManager.HomeEnabled() {
		return ctx, accountPoolError(errors.New("client account pools are unavailable in Home mode"))
	}
	return coreauth.WithAccountPools(ctx, policy), nil
}

func (h *BaseAPIHandler) accountPoolExecutionContext(ctx context.Context) (context.Context, *interfaces.ErrorMessage) {
	if ctx == nil {
		return context.Background(), nil
	}
	c, _ := ctx.Value("gin").(*gin.Context)
	return h.AccountPoolContext(ctx, c)
}

func accountPoolError(err error) *interfaces.ErrorMessage {
	return &interfaces.ErrorMessage{StatusCode: http.StatusServiceUnavailable, Error: err}
}

// accountPoolPluginExecutorError refuses a pooled request that a model router
// hands to an executor plugin, which selects credentials without the pools.
func accountPoolPluginExecutorError() *interfaces.ErrorMessage {
	return accountPoolError(errors.New("plugin executor does not support account-pool keys"))
}

// accountPoolModelFilter returns a filter that reports whether a credential in
// the key's pools serves a model, so model lists only show what the key can
// use. It allows every model for a key without pools.
func (h *BaseAPIHandler) accountPoolModelFilter(c *gin.Context) func(model string) bool {
	policy, err := coreauth.ResolveAccountPools(h.Cfg.Client, c.GetString("userApiKey"))
	if err != nil {
		return func(string) bool { return false }
	}
	if policy == nil {
		return func(string) bool { return true }
	}
	if h.AuthManager == nil || h.AuthManager.HomeEnabled() {
		return func(string) bool { return false }
	}
	// Selection never picks a disabled credential, in either representation,
	// so its models are not offered either.
	var members []string
	for _, a := range h.AuthManager.List() {
		if !a.Disabled && a.Status != coreauth.StatusDisabled && policy.Allows(a) {
			members = append(members, a.ID)
		}
	}
	modelRegistry := registry.GetGlobalRegistry()
	return func(model string) bool {
		for _, id := range members {
			if modelRegistry.ClientSupportsModel(id, model) {
				return true
			}
		}
		return false
	}
}
