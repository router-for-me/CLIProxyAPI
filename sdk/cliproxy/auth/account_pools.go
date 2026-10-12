package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

type accountPoolsContextKey struct{}

// AccountPoolPolicy is the account pools a client API key may use, resolved
// for one request. It is immutable: a config change resolves a new policy.
type AccountPoolPolicy struct {
	groups    []config.ClientAccountPool
	providers map[string]bool
	namespace string
}

// ResolveAccountPools returns the account pool policy of the client.key-scopes
// entry that applies to a client API key, or nil when that entry sets no pools.
// A reference to a missing or repeated pool, or an invalid pool definition, is
// an error, so the key fails closed instead of widening its access. An empty
// pool is valid, for example while a new login is being provisioned.
func ResolveAccountPools(cfg config.ClientConfig, clientKey string) (*AccountPoolPolicy, error) {
	scope, ok := cfg.KeyScope(clientKey)
	if !ok || scope.Pools == nil {
		return nil, nil
	}
	policy := &AccountPoolPolicy{providers: make(map[string]bool)}
	for _, provider := range scope.Providers {
		if provider = config.NormalizeKeyScopeProvider(provider); provider != "" {
			policy.providers[provider] = true
		}
	}
	if len(policy.providers) == 0 {
		return nil, fmt.Errorf("account-pool scope must name providers")
	}
	groups := make(map[string]config.ClientAccountPool)
	if cfg.AccountPools != nil {
		for _, group := range *cfg.AccountPools {
			if group.Name == "" || strings.TrimSpace(group.Name) != group.Name {
				return nil, fmt.Errorf("invalid account-pool name")
			}
			if _, exists := groups[group.Name]; exists {
				return nil, fmt.Errorf("duplicate account-pool name")
			}
			if group.AuthKind != "" && group.AuthKind != AuthKindOAuth && group.AuthKind != AuthKindAPIKey {
				return nil, fmt.Errorf("invalid account-pool auth-kind")
			}
			group.AuthIDs = slices.Clone(group.AuthIDs)
			group.AuthFiles = slices.Clone(group.AuthFiles)
			groups[group.Name] = group
		}
	}
	seen := make(map[string]bool)
	for _, name := range *scope.Pools {
		group, ok := groups[name]
		if !ok || seen[name] {
			return nil, fmt.Errorf("unknown or repeated account-pool reference")
		}
		seen[name] = true
		policy.groups = append(policy.groups, group)
	}
	// Session affinity is namespaced by key and policy, so two keys that share a
	// session ID, or a key whose pools changed, never reuse each other's binding.
	encoded, _ := json.Marshal(struct {
		Groups    []config.ClientAccountPool
		Providers map[string]bool
	}{policy.groups, policy.providers})
	digest := sha256.Sum256(append([]byte(clientKey+"\x00"), encoded...))
	policy.namespace = hex.EncodeToString(digest[:])
	return policy, nil
}

// WithAccountPools attaches a policy resolved by the server for the
// authenticated client key; clients cannot supply it through headers or model
// names. A nil policy clears an earlier one.
func WithAccountPools(ctx context.Context, policy *AccountPoolPolicy) context.Context {
	return context.WithValue(ctx, accountPoolsContextKey{}, policy)
}

func accountPoolsFromContext(ctx context.Context) *AccountPoolPolicy {
	if ctx == nil {
		return nil
	}
	policy, _ := ctx.Value(accountPoolsContextKey{}).(*AccountPoolPolicy)
	return policy
}

// HasAccountPools reports whether the request is limited to account pools.
func HasAccountPools(ctx context.Context) bool { return accountPoolsFromContext(ctx) != nil }

// Allows reports whether a credential belongs to one of the policy's pools,
// regardless of its current quota or cooldown. A nil policy allows every
// credential.
func (p *AccountPoolPolicy) Allows(a *Auth) bool { return p == nil || p.tier(a) >= 0 }

// tier returns the index of the first pool the credential belongs to, or -1.
func (p *AccountPoolPolicy) tier(a *Auth) int {
	if a == nil || !p.providers[strings.ToLower(a.Provider)] {
		return -1
	}
	for i, group := range p.groups {
		if group.AuthKind != "" && a.AuthKind() != group.AuthKind {
			continue
		}
		if slices.Contains(group.AuthIDs, a.ID) || (a.FileName != "" && slices.Contains(group.AuthFiles, filepath.Base(a.FileName))) {
			return i
		}
	}
	return -1
}

// availableAuthsForPoolSelector narrows the available credentials to the first
// pool that has one, then applies credential priority within that pool, so
// pool order takes precedence over priority. Quota, cooldown and concurrency
// stay on the credential itself, shared by every pool and key that uses it.
func (m *Manager) availableAuthsForPoolSelector(ctx context.Context, selector Selector, auths []*Auth, provider, model string, now time.Time) ([]*Auth, []*Auth, error) {
	policy := accountPoolsFromContext(ctx)
	if policy == nil {
		return m.availableAuthsForSelector(selector, auths, provider, model, now)
	}
	weightedSelector := selector
	if affinity, ok := selector.(*SessionAffinitySelector); ok {
		weightedSelector = affinity.fallback
	}
	if _, weighted := weightedSelector.(*WeightedRoundRobinSelector); weighted {
		auths = positiveWeightAuths(auths)
	}
	available, err := m.availableAuthsForRouteModelAcrossPriorities(auths, provider, model, now)
	if err != nil {
		return nil, nil, err
	}
	best := len(policy.groups)
	var tierAuths []*Auth
	for _, a := range available {
		tier := policy.tier(a)
		if tier < 0 || tier > best {
			continue
		}
		if tier < best {
			best, tierAuths = tier, nil
		}
		tierAuths = append(tierAuths, a)
	}
	return m.availableAuthsForSelector(selector, tierAuths, provider, model, now)
}

func accountPoolAffinityNamespace(provider string, metadata map[string]any) string {
	if namespace, _ := metadata[cliproxyexecutor.AccountPoolScopeMetadataKey].(string); namespace != "" {
		return provider + "::pool:" + namespace
	}
	return provider
}
