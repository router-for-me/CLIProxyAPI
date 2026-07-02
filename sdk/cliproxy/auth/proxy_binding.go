package auth

import (
	"sort"
	"strings"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func proxyURLsFromConfig(cfg *internalconfig.Config) []string {
	if cfg == nil {
		return nil
	}
	return internalconfig.NormalizeProxyURLList(cfg.ProxyURLs)
}

// SetImplicitProxyOrder sets a runtime-only ordering hint for implicit proxy binding.
func (a *Auth) SetImplicitProxyOrder(order string) {
	if a == nil {
		return
	}
	a.implicitProxyOrder = strings.TrimSpace(order)
}

// SetImplicitProxyURL sets a runtime-only proxy URL bound from the global proxy_urls list.
func (a *Auth) SetImplicitProxyURL(proxyURL string) {
	if a == nil {
		return
	}
	a.implicitProxyURL = strings.TrimSpace(proxyURL)
}

// ImplicitProxyURL returns the runtime-only proxy URL bound from the global proxy_urls list.
func (a *Auth) ImplicitProxyURL() string {
	if a == nil {
		return ""
	}
	return strings.TrimSpace(a.implicitProxyURL)
}

// EffectiveProxyURL resolves the single proxy URL that should be used for an auth.
// Explicit auth proxy wins, then runtime proxy_urls binding, then global proxy-url fallback.
func EffectiveProxyURL(globalProxyURL string, auth *Auth) string {
	if auth != nil {
		if proxyURL := strings.TrimSpace(auth.ProxyURL); proxyURL != "" {
			return proxyURL
		}
		if proxyURL := auth.ImplicitProxyURL(); proxyURL != "" {
			return proxyURL
		}
	}
	return strings.TrimSpace(globalProxyURL)
}

func (m *Manager) refreshImplicitProxyBindings() {
	if m == nil {
		return
	}
	cfg, _ := m.runtimeConfig.Load().(*internalconfig.Config)
	var proxies []string
	if cfg != nil {
		proxies = proxyURLsFromConfig(cfg)
	}

	m.mu.Lock()
	snapshots := m.rebindImplicitProxyURLsLocked(proxies)
	m.mu.Unlock()

	if m.scheduler == nil {
		return
	}
	for _, snapshot := range snapshots {
		m.scheduler.upsertAuth(snapshot)
	}
}

func (m *Manager) rebindImplicitProxyURLsLocked(proxies []string) []*Auth {
	if m == nil || len(m.auths) == 0 {
		return nil
	}

	changed := make(map[string]*Auth)
	groups := make(map[string][]*Auth)
	for _, auth := range m.auths {
		if auth == nil || strings.TrimSpace(auth.ID) == "" {
			continue
		}
		if strings.TrimSpace(auth.ProxyURL) != "" || len(proxies) == 0 {
			if auth.ImplicitProxyURL() != "" {
				auth.SetImplicitProxyURL("")
				changed[auth.ID] = auth.Clone()
			}
			continue
		}
		category := implicitProxyCategory(auth)
		groups[category] = append(groups[category], auth)
	}

	for _, group := range groups {
		sort.SliceStable(group, func(i, j int) bool {
			left := implicitProxyOrderKey(group[i])
			right := implicitProxyOrderKey(group[j])
			if left == right {
				return group[i].ID < group[j].ID
			}
			return left < right
		})
		for i, auth := range group {
			desired := proxies[i%len(proxies)]
			if auth.ImplicitProxyURL() == desired {
				continue
			}
			auth.SetImplicitProxyURL(desired)
			changed[auth.ID] = auth.Clone()
		}
	}

	if len(changed) == 0 {
		return nil
	}
	ids := make([]string, 0, len(changed))
	for id := range changed {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	snapshots := make([]*Auth, 0, len(ids))
	for _, id := range ids {
		snapshots = append(snapshots, changed[id])
	}
	return snapshots
}

func implicitProxyCategory(auth *Auth) string {
	provider := executorKeyFromAuth(auth)
	if provider == "" && auth != nil {
		provider = strings.ToLower(strings.TrimSpace(auth.Provider))
	}
	if provider == "" {
		provider = "unknown"
	}
	kind := ""
	if auth != nil {
		kind = auth.AuthKind()
	}
	if kind == "" {
		kind = "unknown"
	}
	return provider + ":" + kind
}

func implicitProxyOrderKey(auth *Auth) string {
	if auth == nil {
		return ""
	}
	if auth.implicitProxyOrder != "" {
		return "0:" + auth.implicitProxyOrder
	}
	if auth.Attributes != nil {
		for _, key := range []string{AttributeAuthIndexSeed, AttributeVirtualSource, AttributeSource, AttributePath} {
			if value := strings.TrimSpace(auth.Attributes[key]); value != "" {
				return "1:" + strings.ToLower(value)
			}
		}
	}
	if value := strings.TrimSpace(auth.FileName); value != "" {
		return "2:" + strings.ToLower(value)
	}
	if value := strings.TrimSpace(auth.ID); value != "" {
		return "3:" + strings.ToLower(value)
	}
	return ""
}
