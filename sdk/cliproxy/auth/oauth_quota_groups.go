package auth

import (
	"path"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

type oauthQuotaRuntimeSnapshot struct {
	groups          []internalconfig.OAuthQuotaGroup
	oauthModelAlias map[string][]internalconfig.OAuthModelAlias
	states          map[string]map[string]internalconfig.OAuthAccountQuotaGroupState
}

var oauthQuotaRuntime atomic.Value

func init() {
	oauthQuotaRuntime.Store(buildOAuthQuotaRuntimeSnapshot(&internalconfig.Config{}))
}

func buildOAuthQuotaRuntimeSnapshot(cfg *internalconfig.Config) oauthQuotaRuntimeSnapshot {
	if cfg == nil {
		cfg = &internalconfig.Config{}
	}
	groups := internalconfig.NormalizeOAuthQuotaGroups(cfg.OAuthQuotaGroups)
	stateSlice := internalconfig.NormalizeOAuthAccountQuotaGroupState(cfg.OAuthAccountQuotaGroupState)

	groupCopy := make([]internalconfig.OAuthQuotaGroup, 0, len(groups))
	for _, group := range groups {
		groupCopy = append(groupCopy, internalconfig.OAuthQuotaGroup{
			ID:        strings.ToLower(strings.TrimSpace(group.ID)),
			Label:     strings.TrimSpace(group.Label),
			Providers: append([]string(nil), group.Providers...),
			Patterns:  append([]string(nil), group.Patterns...),
			Priority:  group.Priority,
			Enabled:   group.Enabled,
		})
	}
	sort.Slice(groupCopy, func(i, j int) bool {
		if groupCopy[i].Priority != groupCopy[j].Priority {
			return groupCopy[i].Priority > groupCopy[j].Priority
		}
		return groupCopy[i].ID < groupCopy[j].ID
	})

	aliasCopy := make(map[string][]internalconfig.OAuthModelAlias, len(cfg.OAuthModelAlias))
	for channel, entries := range cfg.OAuthModelAlias {
		key := strings.ToLower(strings.TrimSpace(channel))
		if key == "" || len(entries) == 0 {
			continue
		}
		copied := make([]internalconfig.OAuthModelAlias, 0, len(entries))
		for _, entry := range entries {
			copied = append(copied, internalconfig.OAuthModelAlias{
				Name:  strings.TrimSpace(entry.Name),
				Alias: strings.TrimSpace(entry.Alias),
				Fork:  entry.Fork,
			})
		}
		aliasCopy[key] = copied
	}

	stateCopy := make(map[string]map[string]internalconfig.OAuthAccountQuotaGroupState)
	for _, entry := range stateSlice {
		authID := strings.TrimSpace(entry.AuthID)
		groupID := strings.ToLower(strings.TrimSpace(entry.GroupID))
		if authID == "" || groupID == "" {
			continue
		}
		if stateCopy[authID] == nil {
			stateCopy[authID] = make(map[string]internalconfig.OAuthAccountQuotaGroupState)
		}
		stateCopy[authID][groupID] = entry
	}

	return oauthQuotaRuntimeSnapshot{
		groups:          groupCopy,
		oauthModelAlias: aliasCopy,
		states:          stateCopy,
	}
}

// SetOAuthQuotaRuntimeConfig updates the global quota-group routing snapshot.
func SetOAuthQuotaRuntimeConfig(cfg *internalconfig.Config) {
	oauthQuotaRuntime.Store(buildOAuthQuotaRuntimeSnapshot(cfg))
}

func currentOAuthQuotaRuntimeSnapshot() oauthQuotaRuntimeSnapshot {
	if snapshot, ok := oauthQuotaRuntime.Load().(oauthQuotaRuntimeSnapshot); ok {
		return snapshot
	}
	return buildOAuthQuotaRuntimeSnapshot(nil)
}

func oauthQuotaProviderSupported(provider string) bool {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "antigravity", "gemini", "gemini-cli":
		return true
	default:
		return false
	}
}

func wildcardMatchCI(pattern, value string) bool {
	pattern = strings.ToLower(strings.TrimSpace(pattern))
	value = strings.ToLower(strings.TrimSpace(value))
	if pattern == "" || value == "" {
		return false
	}
	matched, err := path.Match(pattern, value)
	if err == nil && matched {
		return true
	}
	return pattern == value
}

func oauthQuotaGroupCandidates(auth *Auth, model string, snapshot oauthQuotaRuntimeSnapshot) []string {
	model = strings.TrimSpace(model)
	if model == "" {
		return nil
	}
	seen := make(map[string]struct{}, 4)
	out := make([]string, 0, 4)
	appendCandidate := func(value string) {
		value = strings.ToLower(strings.TrimSpace(canonicalModelKey(value)))
		if value == "" {
			return
		}
		if _, ok := seen[value]; ok {
			return
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}

	channel := modelAliasChannel(auth)
	if channel != "" && len(snapshot.oauthModelAlias) > 0 {
		if aliases := snapshot.oauthModelAlias[channel]; len(aliases) > 0 {
			entries := make([]modelAliasEntry, 0, len(aliases))
			for i := range aliases {
				entries = append(entries, aliases[i])
			}
			if resolved := resolveModelAliasFromConfigModels(model, entries); resolved != "" {
				appendCandidate(resolved)
			}
		}
	}
	appendCandidate(model)
	return out
}

func resolveOAuthQuotaGroup(auth *Auth, model string) (internalconfig.OAuthQuotaGroup, bool) {
	if auth == nil || !oauthQuotaProviderSupported(auth.Provider) {
		return internalconfig.OAuthQuotaGroup{}, false
	}
	snapshot := currentOAuthQuotaRuntimeSnapshot()
	provider := strings.ToLower(strings.TrimSpace(auth.Provider))
	candidates := oauthQuotaGroupCandidates(auth, model, snapshot)
	if len(candidates) == 0 {
		return internalconfig.OAuthQuotaGroup{}, false
	}

	for _, group := range snapshot.groups {
		if !group.Enabled {
			continue
		}
		supported := false
		for _, candidateProvider := range group.Providers {
			if strings.EqualFold(strings.TrimSpace(candidateProvider), provider) {
				supported = true
				break
			}
		}
		if !supported {
			continue
		}
		for _, candidate := range candidates {
			for _, pattern := range group.Patterns {
				if wildcardMatchCI(pattern, candidate) {
					return group, true
				}
			}
		}
	}
	return internalconfig.OAuthQuotaGroup{}, false
}

func oauthQuotaGroupState(authID, groupID string) (internalconfig.OAuthAccountQuotaGroupState, bool) {
	authID = strings.TrimSpace(authID)
	groupID = strings.ToLower(strings.TrimSpace(groupID))
	if authID == "" || groupID == "" {
		return internalconfig.OAuthAccountQuotaGroupState{}, false
	}
	snapshot := currentOAuthQuotaRuntimeSnapshot()
	byAuth := snapshot.states[authID]
	if len(byAuth) == 0 {
		return internalconfig.OAuthAccountQuotaGroupState{}, false
	}
	state, ok := byAuth[groupID]
	return state, ok
}

func oauthQuotaGroupBlock(auth *Auth, model string, now time.Time) (bool, bool, time.Time) {
	if auth == nil {
		return false, false, time.Time{}
	}
	group, ok := resolveOAuthQuotaGroup(auth, model)
	if !ok {
		return false, false, time.Time{}
	}
	state, ok := oauthQuotaGroupState(auth.ID, group.ID)
	if !ok {
		return false, false, time.Time{}
	}
	if state.ManualSuspended {
		return true, false, time.Time{}
	}
	if !state.AutoSuspendedUntil.IsZero() && state.AutoSuspendedUntil.After(now) {
		return true, true, state.AutoSuspendedUntil
	}
	return false, false, time.Time{}
}

func collectOAuthQuotaGroupsForModels(auth *Auth, models []string) []internalconfig.OAuthQuotaGroup {
	if auth == nil || !oauthQuotaProviderSupported(auth.Provider) {
		return nil
	}
	snapshot := currentOAuthQuotaRuntimeSnapshot()
	seen := make(map[string]internalconfig.OAuthQuotaGroup)
	for _, model := range models {
		group, ok := resolveOAuthQuotaGroup(auth, model)
		if !ok {
			continue
		}
		seen[group.ID] = group
	}
	if len(seen) == 0 {
		provider := strings.ToLower(strings.TrimSpace(auth.Provider))
		for _, group := range snapshot.groups {
			if !group.Enabled {
				continue
			}
			for _, candidateProvider := range group.Providers {
				if strings.EqualFold(strings.TrimSpace(candidateProvider), provider) {
					seen[group.ID] = group
					break
				}
			}
		}
	}
	if len(seen) == 0 {
		return nil
	}
	out := make([]internalconfig.OAuthQuotaGroup, 0, len(seen))
	for _, group := range seen {
		out = append(out, group)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Priority != out[j].Priority {
			return out[i].Priority > out[j].Priority
		}
		return out[i].ID < out[j].ID
	})
	return out
}
