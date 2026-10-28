package auth

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

type ClaudeQuotaSelector struct {
	SnapshotPath string
	Fallback     Selector
}

type claudeQuotaAccount struct {
	WeeklyReset          time.Time `json:"weekly_reset"`
	FiveHourReset        time.Time `json:"five_hour_reset"`
	Eligible             *bool     `json:"eligible"`
	BlockedModelFamilies []string  `json:"blocked_model_families"`
}

type claudeQuotaSnapshot struct {
	ObservedAt time.Time                     `json:"observed_at"`
	Accounts   map[string]claudeQuotaAccount `json:"accounts"`
}

type claudeQuotaModelsKey struct{}

func sessionAffinitySelector(selector Selector) (*SessionAffinitySelector, bool) {
	if quota, ok := selector.(*ClaudeQuotaSelector); ok {
		selector = quota.Fallback
	}
	affinity, ok := selector.(*SessionAffinitySelector)
	return affinity, ok
}

func (selector *ClaudeQuotaSelector) OnResult(result Result) {
	if observer, ok := selector.Fallback.(interface{ OnResult(Result) }); ok {
		observer.OnResult(result)
	}
}

func (selector *ClaudeQuotaSelector) InvalidateAuth(authID string) {
	if invalidator, ok := selector.Fallback.(interface{ InvalidateAuth(string) }); ok {
		invalidator.InvalidateAuth(authID)
	}
}

func (selector *ClaudeQuotaSelector) LookupAffinity(provider, model, sessionID string, filters ...func(string) bool) (string, string) {
	if affinity, ok := sessionAffinitySelector(selector); ok {
		return affinity.LookupAffinity(provider, model, sessionID, filters...)
	}
	return "", "unsupported"
}

func (selector *ClaudeQuotaSelector) Stop() {
	if stoppable, ok := selector.Fallback.(StoppableSelector); ok {
		stoppable.Stop()
	}
}

func isClaudeQuotaAuth(candidate *Auth) bool {
	return candidate != nil && strings.EqualFold(candidate.Provider, "claude") && candidate.AuthKind() != AuthKindAPIKey
}

func hasClaudeQuotaAuth(auths []*Auth) bool {
	for _, candidate := range auths {
		if isClaudeQuotaAuth(candidate) {
			return true
		}
	}
	return false
}

func (manager *Manager) claudeQuotaContext(ctx context.Context, selector Selector, model string, auths []*Auth) context.Context {
	if _, ok := selector.(*ClaudeQuotaSelector); !ok {
		return ctx
	}
	models := make(map[string]string, len(auths))
	for _, candidate := range auths {
		if isClaudeQuotaAuth(candidate) {
			models[candidate.ID] = manager.selectionModelForAuth(candidate, model)
		}
	}
	return context.WithValue(ctx, claudeQuotaModelsKey{}, models)
}

func (selector *ClaudeQuotaSelector) load(now time.Time) (claudeQuotaSnapshot, error) {
	invalid := &Error{Code: "claude_quota_unavailable", Message: "Claude quota snapshot is missing, invalid, or stale", HTTPStatus: 503}
	file, errOpen := os.Open(selector.SnapshotPath)
	if errOpen != nil {
		return claudeQuotaSnapshot{}, invalid
	}
	raw, errRead := io.ReadAll(io.LimitReader(file, (1<<20)+1))
	errClose := file.Close()
	var snap claudeQuotaSnapshot
	if errRead != nil || errClose != nil || len(raw) > 1<<20 || json.Unmarshal(raw, &snap) != nil || snap.Accounts == nil || snap.ObservedAt.IsZero() || snap.ObservedAt.After(now) || now.Sub(snap.ObservedAt) > 2*time.Minute {
		return claudeQuotaSnapshot{}, invalid
	}
	return snap, nil
}

func (selector *ClaudeQuotaSelector) Pick(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	return selector.pick(ctx, provider, model, opts, auths, time.Now())
}

func (selector *ClaudeQuotaSelector) pick(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, auths []*Auth, now time.Time) (*Auth, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	fallback := selector.Fallback
	if fallback == nil {
		fallback = &RoundRobinSelector{}
	}
	if !hasClaudeQuotaAuth(auths) {
		return fallback.Pick(ctx, provider, model, opts, auths)
	}
	snap, quotaErr := selector.load(now)
	var accountErr error
	weightSelector := fallback
	if affinity, ok := sessionAffinitySelector(fallback); ok {
		weightSelector = affinity.fallback
	}
	_, weighted := weightSelector.(*WeightedRoundRobinSelector)
	models, _ := ctx.Value(claudeQuotaModelsKey{}).(map[string]string)
	var best *Auth
	var bestData claudeQuotaAccount
	for _, candidate := range auths {
		if !isClaudeQuotaAuth(candidate) {
			continue
		}
		if weighted && authWeight(candidate) <= 0 {
			continue
		}
		data, known := snap.Accounts[candidate.ID]
		if !known || data.Eligible == nil {
			accountErr = &Error{Code: "claude_quota_unavailable", Message: "Claude candidate quota is unknown", HTTPStatus: 503}
			continue
		}
		if !*data.Eligible {
			continue
		}
		if data.WeeklyReset.IsZero() || !data.WeeklyReset.After(now) || data.FiveHourReset.IsZero() || !data.FiveHourReset.After(now) {
			accountErr = &Error{Code: "claude_quota_unavailable", Message: "Claude quota windows need refreshing", HTTPStatus: 503}
			continue
		}
		resolved := models[candidate.ID]
		if resolved == "" {
			resolved = model
		}
		resolved = strings.ToLower(resolved)
		blocked := false
		knownFamily := false
		for _, family := range []string{"opus", "sonnet", "haiku", "fable", "mythos"} {
			knownFamily = knownFamily || strings.Contains(resolved, family)
		}
		for _, family := range data.BlockedModelFamilies {
			if strings.TrimSpace(family) == "" || !knownFamily || strings.Contains(resolved, strings.ToLower(family)) {
				blocked = true
			}
		}
		if blocked {
			continue
		}
		if best == nil || data.WeeklyReset.Before(bestData.WeeklyReset) || data.WeeklyReset.Equal(bestData.WeeklyReset) && (data.FiveHourReset.Before(bestData.FiveHourReset) || data.FiveHourReset.Equal(bestData.FiveHourReset) && candidate.ID < best.ID) {
			best, bestData = candidate, data
		}
	}
	pool := make([]*Auth, 0, len(auths))
	for _, candidate := range auths {
		if !isClaudeQuotaAuth(candidate) {
			pool = append(pool, candidate)
		}
	}
	if quotaErr == nil && best != nil {
		pool = append(pool, best)
	}
	if len(pool) == 0 {
		if quotaErr != nil {
			return nil, quotaErr
		}
		if accountErr != nil {
			return nil, accountErr
		}
		return nil, &Error{Code: "claude_quota_exhausted", Message: "No Claude account has quota for this model", HTTPStatus: 503}
	}
	sort.Slice(pool, func(first, second int) bool { return pool[first].ID < pool[second].ID })
	selected, errPick := fallback.Pick(ctx, provider, model, opts, pool)
	if errPick != nil {
		return nil, errPick
	}
	if isClaudeQuotaAuth(selected) {
		return best, nil
	}
	return selected, nil
}
