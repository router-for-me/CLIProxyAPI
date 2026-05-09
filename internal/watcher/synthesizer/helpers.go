package synthesizer

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/watcher/diff"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

// StableIDGenerator generates stable, deterministic IDs for auth entries.
// It uses SHA256 hashing with collision handling via counters.
// It is not safe for concurrent use.
type StableIDGenerator struct {
	counters map[string]int
}

// NewStableIDGenerator creates a new StableIDGenerator instance.
func NewStableIDGenerator() *StableIDGenerator {
	return &StableIDGenerator{counters: make(map[string]int)}
}

// Next generates a stable ID based on the kind and parts.
// Returns the full ID (kind:hash) and the short hash portion.
func (g *StableIDGenerator) Next(kind string, parts ...string) (string, string) {
	if g == nil {
		return kind + ":000000000000", "000000000000"
	}
	hasher := sha256.New()
	hasher.Write([]byte(kind))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		hasher.Write([]byte{0})
		hasher.Write([]byte(trimmed))
	}
	digest := hex.EncodeToString(hasher.Sum(nil))
	if len(digest) < 12 {
		digest = fmt.Sprintf("%012s", digest)
	}
	short := digest[:12]
	key := kind + ":" + short
	index := g.counters[key]
	g.counters[key] = index + 1
	if index > 0 {
		short = fmt.Sprintf("%s-%d", short, index)
	}
	return fmt.Sprintf("%s:%s", kind, short), short
}

// ApplyAuthExcludedModelsMeta applies excluded models metadata to an auth entry.
// It computes a hash of excluded models and sets the auth_kind attribute.
// For OAuth entries, perKey (from the JSON file's excluded-models field) is merged
// with the global oauth-excluded-models config for the provider.
func ApplyAuthExcludedModelsMeta(auth *coreauth.Auth, cfg *config.Config, perKey []string, authKind string) {
	if auth == nil || cfg == nil {
		return
	}
	authKindKey := strings.ToLower(strings.TrimSpace(authKind))
	seen := make(map[string]struct{})
	add := func(list []string) {
		for _, entry := range list {
			if trimmed := strings.TrimSpace(entry); trimmed != "" {
				key := strings.ToLower(trimmed)
				if _, exists := seen[key]; exists {
					continue
				}
				seen[key] = struct{}{}
			}
		}
	}
	if authKindKey == "apikey" {
		add(perKey)
	} else {
		// For OAuth: merge per-account excluded models with global provider-level exclusions
		add(perKey)
		if cfg.OAuthExcludedModels != nil {
			providerKey := strings.ToLower(strings.TrimSpace(auth.Provider))
			add(cfg.OAuthExcludedModels[providerKey])
		}
	}
	combined := make([]string, 0, len(seen))
	for k := range seen {
		combined = append(combined, k)
	}
	sort.Strings(combined)
	hash := diff.ComputeExcludedModelsHash(combined)
	if auth.Attributes == nil {
		auth.Attributes = make(map[string]string)
	}
	if hash != "" {
		auth.Attributes["excluded_models_hash"] = hash
	}
	// Store the combined excluded models list so that routing can read it at runtime
	if len(combined) > 0 {
		auth.Attributes["excluded_models"] = strings.Join(combined, ",")
	}
	if authKind != "" {
		auth.Attributes["auth_kind"] = authKind
	}
}

// WarnAuthAliasExclusionConflicts returns warnings for any oauth-model-alias
// upstream that would be wildcard-blocked by this auth's per-account
// excluded_models. Counterpart to validateOAuthAliasExclusions in
// sdk/cliproxy (which handles provider-wide exclusions at config-load).
//
// Per-account exclusions live as auth attributes populated by
// ApplyAuthExcludedModelsMeta, so this check only fires meaningfully at
// synthesizer time when both the alias config and the auth's perKey list
// are visible together. Returns nil when there's nothing to check.
//
// Caller logs each returned string at WARN level. Cardinality: one call per
// synthesized auth file per synthesizer pass.
func WarnAuthAliasExclusionConflicts(auth *coreauth.Auth, cfg *config.Config, perKey []string, authKind string) []string {
	if auth == nil || cfg == nil || len(cfg.OAuthModelAlias) == 0 || len(perKey) == 0 {
		return nil
	}
	channel := coreauth.OAuthModelAliasChannel(strings.TrimSpace(auth.Provider), authKind)
	if channel == "" {
		return nil
	}
	aliases := cfg.OAuthModelAlias[channel]
	if len(aliases) == 0 {
		return nil
	}
	authID := strings.TrimSpace(auth.ID)
	if authID == "" {
		authID = "<unknown>"
	}
	var warnings []string
	for _, entry := range aliases {
		upstream := strings.TrimSpace(entry.Name)
		alias := strings.TrimSpace(entry.Alias)
		if upstream == "" || alias == "" || strings.EqualFold(upstream, alias) {
			continue
		}
		upstreamLower := strings.ToLower(upstream)
		for _, pattern := range perKey {
			p := strings.TrimSpace(pattern)
			if p == "" {
				continue
			}
			if matchExclusionWildcard(strings.ToLower(p), upstreamLower) {
				warnings = append(warnings, fmt.Sprintf(
					"oauth-model-alias: auth=%q channel=%q alias=%q upstream=%q matches per-account excluded-models pattern=%q — alias will not resolve at runtime",
					authID, channel, alias, upstream, p,
				))
			}
		}
	}
	return warnings
}

// matchExclusionWildcard performs case-insensitive wildcard matching where
// '*' matches any substring. Inputs are expected pre-lowercased by callers.
// Mirrors sdk/cliproxy.matchWildcard semantics; duplicated here to keep
// this package's import surface narrow (no synthesizer -> sdk/cliproxy edge).
func matchExclusionWildcard(pattern, value string) bool {
	if pattern == "" {
		return false
	}
	if !strings.Contains(pattern, "*") {
		return pattern == value
	}
	parts := strings.Split(pattern, "*")
	if prefix := parts[0]; prefix != "" {
		if !strings.HasPrefix(value, prefix) {
			return false
		}
		value = value[len(prefix):]
	}
	if suffix := parts[len(parts)-1]; suffix != "" {
		if !strings.HasSuffix(value, suffix) {
			return false
		}
		value = value[:len(value)-len(suffix)]
	}
	for _, segment := range parts[1 : len(parts)-1] {
		if segment == "" {
			continue
		}
		idx := strings.Index(value, segment)
		if idx < 0 {
			return false
		}
		value = value[idx+len(segment):]
	}
	return true
}

// addConfigHeadersToAttrs adds header configuration to auth attributes.
// Headers are prefixed with "header:" in the attributes map.
func addConfigHeadersToAttrs(headers map[string]string, attrs map[string]string) {
	if len(headers) == 0 || attrs == nil {
		return
	}
	for hk, hv := range headers {
		key := strings.TrimSpace(hk)
		val := strings.TrimSpace(hv)
		if key == "" || val == "" {
			continue
		}
		attrs["header:"+key] = val
	}
}
