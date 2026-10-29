package config

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/proxyutil"
)

// ProxyPoolStrategySticky keeps an account bound to the same healthy node.
const ProxyPoolStrategySticky = "sticky"

// ProxyPoolStrategyRoundRobin rotates accounts across healthy nodes.
const ProxyPoolStrategyRoundRobin = "round-robin"

// NormalizeProxyPoolStrategy returns the canonical pool strategy name.
// An empty strategy defaults to sticky.
func NormalizeProxyPoolStrategy(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", ProxyPoolStrategySticky, "sticky-session":
		return ProxyPoolStrategySticky
	case ProxyPoolStrategyRoundRobin, "roundrobin", "rr":
		return ProxyPoolStrategyRoundRobin
	default:
		return strings.ToLower(strings.TrimSpace(raw))
	}
}

// ValidateProxyPools checks proxy pool declarations for structural errors.
// It rejects empty or duplicate IDs, empty node lists, unsupported strategies,
// unsupported proxy schemes, and malformed health-check targets.
func ValidateProxyPools(pools []ProxyPoolConfig) error {
	seen := make(map[string]struct{}, len(pools))
	for index, pool := range pools {
		id := strings.TrimSpace(pool.ID)
		if id == "" {
			return fmt.Errorf("proxy-pools[%d]: id is required", index)
		}
		if _, exists := seen[id]; exists {
			return fmt.Errorf("proxy-pools[%d]: duplicate id %q", index, id)
		}
		seen[id] = struct{}{}

		switch NormalizeProxyPoolStrategy(pool.Strategy) {
		case ProxyPoolStrategySticky, ProxyPoolStrategyRoundRobin:
		default:
			return fmt.Errorf("proxy-pools[%d] (%s): unsupported strategy %q", index, id, pool.Strategy)
		}

		nodeCount := 0
		for nodeIndex, node := range pool.Nodes {
			trimmed := strings.TrimSpace(node)
			if trimmed == "" {
				return fmt.Errorf("proxy-pools[%d] (%s): nodes[%d] is empty", index, id, nodeIndex)
			}
			setting, errParse := proxyutil.Parse(trimmed)
			if errParse != nil {
				return fmt.Errorf("proxy-pools[%d] (%s): nodes[%d] has invalid proxy URL: %w", index, id, nodeIndex, errParse)
			}
			if setting.Mode != proxyutil.ModeProxy || setting.URL == nil {
				return fmt.Errorf("proxy-pools[%d] (%s): nodes[%d] has unsupported proxy URL", index, id, nodeIndex)
			}
			if strings.TrimSpace(setting.URL.Hostname()) == "" {
				return fmt.Errorf("proxy-pools[%d] (%s): nodes[%d] is missing a proxy host", index, id, nodeIndex)
			}
			nodeCount++
		}
		if nodeCount == 0 {
			return fmt.Errorf("proxy-pools[%d] (%s): nodes must not be empty", index, id)
		}

		if testURL := strings.TrimSpace(pool.HealthCheck.TestURL); testURL != "" {
			parsed, errURL := url.Parse(testURL)
			if errURL != nil || parsed.Host == "" {
				return fmt.Errorf("proxy-pools[%d] (%s): invalid health-check test-url %q", index, id, testURL)
			}
			if scheme := strings.ToLower(parsed.Scheme); scheme != "http" && scheme != "https" {
				return fmt.Errorf("proxy-pools[%d] (%s): health-check test-url must use http or https", index, id)
			}
		}
	}
	return nil
}
