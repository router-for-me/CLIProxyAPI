package auth

import (
	"context"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/proxyutil"
	log "github.com/sirupsen/logrus"
)

const (
	// AttributeProxyPoolID binds an auth record to a configured proxy pool.
	AttributeProxyPoolID = "proxy_pool_id"
	// MetadataProxyPoolNode records the concrete pool node a sticky account is
	// bound to. It is persisted with the auth metadata and survives restarts.
	MetadataProxyPoolNode = "proxy_pool_node"
)

// ProxyPoolResolver resolves proxy pool bindings into concrete proxy URLs.
// Implementations must be safe for concurrent use.
type ProxyPoolResolver interface {
	// ResolveAuthProxy resolves the binding for an auth whose ProxyURL is empty
	// and whose proxy pool id is set. node is the concrete pool node URL.
	ResolveAuthProxy(ctx context.Context, auth *Auth) (proxyURL string, node string, err error)
	// ResolvePoolNode resolves an explicit pool id for a credential that is not
	// registered yet, such as an OAuth login session.
	ResolvePoolNode(ctx context.Context, provider, authID, poolID string) (proxyURL string, node string, err error)
}

type proxyPoolResolverHolder struct {
	resolver ProxyPoolResolver
}

// SetProxyPoolResolver installs the resolver used to expand proxy_pool_id into
// Auth.ProxyURL. Passing nil disables resolution.
func (m *Manager) SetProxyPoolResolver(resolver ProxyPoolResolver) {
	if m == nil {
		return
	}
	if resolver == nil {
		m.proxyPoolResolver.Store(nil)
		return
	}
	m.proxyPoolResolver.Store(&proxyPoolResolverHolder{resolver: resolver})
}

func (m *Manager) currentProxyPoolResolver() ProxyPoolResolver {
	if m == nil {
		return nil
	}
	holder := m.proxyPoolResolver.Load()
	if holder == nil {
		return nil
	}
	return holder.resolver
}

// ProxyPoolID returns the proxy pool id bound to an auth record. The attribute
// is authoritative; file-backed auths that only expose the JSON key through
// Metadata are supported as a fallback.
func ProxyPoolID(auth *Auth) string {
	if auth == nil {
		return ""
	}
	if auth.Attributes != nil {
		if value := strings.TrimSpace(auth.Attributes[AttributeProxyPoolID]); value != "" {
			return value
		}
	}
	if auth.Metadata != nil {
		if value, ok := auth.Metadata[AttributeProxyPoolID].(string); ok {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

// ApplyProxyPoolMetadata copies a file-level proxy_pool_id into the auth
// attributes, matching the convention used for priority and weight.
func ApplyProxyPoolMetadata(auth *Auth, metadata map[string]any) {
	if auth == nil || metadata == nil {
		return
	}
	raw, exists := metadata[AttributeProxyPoolID]
	if !exists {
		return
	}
	value, ok := raw.(string)
	if !ok {
		return
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return
	}
	if auth.Attributes == nil {
		auth.Attributes = make(map[string]string)
	}
	auth.Attributes[AttributeProxyPoolID] = value
}

// ProxyPoolNode returns the persisted concrete node binding of an auth record.
func ProxyPoolNode(auth *Auth) string {
	if auth == nil || auth.Metadata == nil {
		return ""
	}
	value, _ := auth.Metadata[MetadataProxyPoolNode].(string)
	return strings.TrimSpace(value)
}

// ResolveProxyPoolNode resolves a pool id for a credential that is not
// registered yet. It is used by OAuth login sessions.
func (m *Manager) ResolveProxyPoolNode(ctx context.Context, provider, authID, poolID string) (string, error) {
	resolver := m.currentProxyPoolResolver()
	if resolver == nil {
		return "", nil
	}
	proxyURL, _, errResolve := resolver.ResolvePoolNode(ctx, provider, authID, poolID)
	if errResolve != nil {
		return "", errResolve
	}
	return strings.TrimSpace(proxyURL), nil
}

// applyProxyPoolResolution fills ProxyURL and the sticky node binding from the
// configured pool. An explicit ProxyURL always wins.
func (m *Manager) applyProxyPoolResolution(ctx context.Context, auth *Auth) {
	if m == nil || auth == nil {
		return
	}
	if strings.TrimSpace(auth.ProxyURL) != "" {
		return
	}
	poolID := ProxyPoolID(auth)
	if poolID == "" {
		return
	}
	resolver := m.currentProxyPoolResolver()
	if resolver == nil {
		return
	}
	proxyURL, node, errResolve := resolver.ResolveAuthProxy(ctx, auth)
	if errResolve != nil {
		log.WithFields(log.Fields{
			"auth_id":    auth.ID,
			"provider":   auth.Provider,
			"proxy_pool": poolID,
			"bound_node": proxyutil.Redact(ProxyPoolNode(auth)),
		}).Warnf("failed to resolve proxy pool binding: %v", errResolve)
		return
	}
	if strings.TrimSpace(proxyURL) == "" {
		return
	}
	auth.ProxyURL = strings.TrimSpace(proxyURL)
	if strings.TrimSpace(node) == "" {
		return
	}
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	auth.Metadata[MetadataProxyPoolNode] = strings.TrimSpace(node)
}
