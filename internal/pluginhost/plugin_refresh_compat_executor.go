package pluginhost

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

// pluginRefreshCompatExecutor keeps native OpenAI-compat inference while
// routing credential refresh to a plugin AuthProvider.
//
// Plugins often set Attributes["base_url"] so host routing uses the built-in
// OpenAI-compat executor. That binding previously swallowed refresh because
// OpenAICompatExecutor.Refresh is a no-op for non-Home providers. This wrapper
// preserves native Execute* paths and delegates Refresh to Host.RefreshAuth.
type pluginRefreshCompatExecutor struct {
	inner    coreauth.ProviderExecutor
	host     *Host
	cfg      *config.Config
	provider string
}

// NewPluginRefreshCompatExecutor wraps a native provider executor so Refresh is
// handled by the plugin AuthProvider for the same provider key.
func NewPluginRefreshCompatExecutor(inner coreauth.ProviderExecutor, host *Host, cfg *config.Config) coreauth.ProviderExecutor {
	if inner == nil {
		return nil
	}
	provider := strings.ToLower(strings.TrimSpace(inner.Identifier()))
	return &pluginRefreshCompatExecutor{
		inner:    inner,
		host:     host,
		cfg:      cfg,
		provider: provider,
	}
}

// IsPluginRefreshCompatExecutor reports whether executor is a plugin-refresh wrapper.
func IsPluginRefreshCompatExecutor(executor coreauth.ProviderExecutor) bool {
	_, ok := executor.(*pluginRefreshCompatExecutor)
	return ok
}

// UnwrapPluginRefreshCompatExecutor returns the inner native executor when executor
// is a plugin-refresh wrapper.
func UnwrapPluginRefreshCompatExecutor(executor coreauth.ProviderExecutor) (coreauth.ProviderExecutor, bool) {
	wrapper, ok := executor.(*pluginRefreshCompatExecutor)
	if !ok || wrapper == nil || wrapper.inner == nil {
		return nil, false
	}
	return wrapper.inner, true
}

func (e *pluginRefreshCompatExecutor) Identifier() string {
	if e == nil {
		return ""
	}
	if e.provider != "" {
		return e.provider
	}
	if e.inner != nil {
		return e.inner.Identifier()
	}
	return ""
}

func (e *pluginRefreshCompatExecutor) Execute(ctx context.Context, auth *coreauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	if e == nil || e.inner == nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("plugin refresh compat executor is unavailable")
	}
	return e.inner.Execute(ctx, auth, req, opts)
}

func (e *pluginRefreshCompatExecutor) ExecuteStream(ctx context.Context, auth *coreauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	if e == nil || e.inner == nil {
		return nil, fmt.Errorf("plugin refresh compat executor is unavailable")
	}
	return e.inner.ExecuteStream(ctx, auth, req, opts)
}

func (e *pluginRefreshCompatExecutor) CountTokens(ctx context.Context, auth *coreauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	if e == nil || e.inner == nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("plugin refresh compat executor is unavailable")
	}
	return e.inner.CountTokens(ctx, auth, req, opts)
}

func (e *pluginRefreshCompatExecutor) HttpRequest(ctx context.Context, auth *coreauth.Auth, req *http.Request) (*http.Response, error) {
	if e == nil || e.inner == nil {
		return nil, fmt.Errorf("plugin refresh compat executor is unavailable")
	}
	return e.inner.HttpRequest(ctx, auth, req)
}

// PrepareRequest forwards credential injection to the inner executor when supported.
func (e *pluginRefreshCompatExecutor) PrepareRequest(req *http.Request, auth *coreauth.Auth) error {
	if e == nil || e.inner == nil {
		return fmt.Errorf("plugin refresh compat executor is unavailable")
	}
	preparer, ok := e.inner.(interface {
		PrepareRequest(*http.Request, *coreauth.Auth) error
	})
	if !ok || preparer == nil {
		return nil
	}
	return preparer.PrepareRequest(req, auth)
}

func (e *pluginRefreshCompatExecutor) Refresh(ctx context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	if e == nil {
		return nil, fmt.Errorf("plugin refresh compat executor is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if refreshed, handled, errHome := helps.RefreshAuthViaHome(ctx, e.cfg, auth); handled {
		return refreshed, errHome
	}
	if e.host != nil {
		if refreshed, handled, errRefresh := e.host.RefreshAuth(ctx, auth); handled {
			return refreshed, errRefresh
		}
	}
	if authHasRefreshToken(auth) {
		provider := e.Identifier()
		if provider == "" && auth != nil {
			provider = strings.TrimSpace(auth.Provider)
		}
		return nil, fmt.Errorf("plugin auth provider refresh is unavailable for provider %s", provider)
	}
	if auth == nil {
		return nil, nil
	}
	return auth.Clone(), nil
}

func authHasRefreshToken(auth *coreauth.Auth) bool {
	if auth == nil || auth.Metadata == nil {
		return false
	}
	if token, _ := auth.Metadata["refresh_token"].(string); strings.TrimSpace(token) != "" {
		return true
	}
	if token, _ := auth.Metadata["refreshToken"].(string); strings.TrimSpace(token) != "" {
		return true
	}
	return false
}
