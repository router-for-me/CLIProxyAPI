package auth

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func (m *Manager) resolveEgressProxy(ctx context.Context, provider, model, operation string, auth *Auth) (*Auth, error) {
	if m == nil || auth == nil {
		return auth, nil
	}
	m.mu.RLock()
	resolver := m.egressProxyResolver
	m.mu.RUnlock()
	if resolver == nil {
		return auth, nil
	}

	accountType, email := auth.AccountInfo()
	if accountType != "oauth" {
		email = ""
	}
	response, handled, errResolve := resolver.ResolveEgressProxy(ctx, pluginapi.EgressProxyRequest{
		Provider:  strings.ToLower(strings.TrimSpace(provider)),
		Model:     strings.TrimSpace(model),
		Operation: strings.ToLower(strings.TrimSpace(operation)),
		Auth: pluginapi.EgressProxyAuth{
			ID:       strings.TrimSpace(auth.ID),
			Index:    strings.TrimSpace(auth.EnsureIndex()),
			Provider: strings.ToLower(strings.TrimSpace(auth.Provider)),
			Label:    strings.TrimSpace(auth.Label),
			Email:    email,
		},
	})
	if errResolve != nil {
		return nil, fmt.Errorf("resolve request egress proxy: %w", errResolve)
	}
	if !handled || !response.Handled || response.Mode == pluginapi.EgressProxyModeInherit {
		return auth, nil
	}

	override := auth.Clone()
	switch response.Mode {
	case pluginapi.EgressProxyModeDirect:
		override.ProxyURL = "direct"
	case pluginapi.EgressProxyModeProxy:
		override.ProxyURL = response.ProxyURL
	default:
		return nil, fmt.Errorf("resolve request egress proxy: invalid proxy mode")
	}
	return override, nil
}

func applyEgressProxyOverride(auth, selected, resolved *Auth) *Auth {
	if auth == nil || selected == nil || resolved == nil || resolved.ProxyURL == selected.ProxyURL {
		return auth
	}
	override := auth.Clone()
	override.ProxyURL = resolved.ProxyURL
	return override
}

func (m *Manager) egressProxyContext(ctx context.Context, auth *Auth) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if rt := m.roundTripperFor(auth); rt != nil {
		ctx = context.WithValue(ctx, roundTripperContextKey{}, rt)
		ctx = context.WithValue(ctx, "cliproxy.roundtripper", rt)
	}
	return ctx
}

func egressProxyOperationForContext(ctx context.Context, stream bool) string {
	if stream && cliproxyexecutor.DownstreamWebsocket(ctx) {
		return "websocket"
	}
	if stream {
		return "stream"
	}
	return "execute"
}

func (m *Manager) resolveEgressProxyContext(ctx context.Context, provider, model, operation string, auth *Auth) (context.Context, *Auth, error) {
	resolvedAuth, errResolve := m.resolveEgressProxy(ctx, provider, model, operation, auth)
	if errResolve != nil {
		return ctx, auth, errResolve
	}
	return m.egressProxyContext(ctx, resolvedAuth), resolvedAuth, nil
}

func (m *Manager) resolveEgressProxyHTTPRequest(ctx context.Context, auth *Auth, req *http.Request) (context.Context, *Auth, error) {
	provider := ""
	if auth != nil {
		provider = auth.Provider
	}
	model := ""
	if req != nil && req.URL != nil {
		model = req.URL.Path
	}
	return m.resolveEgressProxyContext(ctx, provider, model, "http", auth)
}
