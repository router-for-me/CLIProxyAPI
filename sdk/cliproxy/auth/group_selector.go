package auth

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/routing"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

// GroupSelector extends RoundRobinSelector with health-aware provider group selection.
type GroupSelector struct {
	RoundRobinSelector
	healthTracker *HealthTracker
	groupCursors  map[string]int
	groupMu       sync.Mutex
}

// NewGroupSelector creates a selector with health tracking for provider groups.
func NewGroupSelector() *GroupSelector {
	return &GroupSelector{
		healthTracker: NewHealthTracker(),
		groupCursors:  make(map[string]int),
	}
}

// GetHealthTracker returns the underlying health tracker.
func (gs *GroupSelector) GetHealthTracker() *HealthTracker {
	return gs.healthTracker
}

// PickFromGroup selects an auth from a provider group, filtering by health.
func (gs *GroupSelector) PickFromGroup(
	ctx context.Context,
	group *routing.ProviderGroup,
	model string,
	opts cliproxyexecutor.Options,
	authsByID map[string]*Auth,
) (*Auth, error) {
	if group == nil || len(group.AccountIDs) == 0 {
		return nil, &Error{Code: "no_group", Message: "no provider group specified"}
	}

	available := gs.healthTracker.GetAvailableAccounts(group.AccountIDs)

	if len(available) == 0 {
		fallbackID := gs.healthTracker.GetLeastRecentlyLimited(group.AccountIDs)
		if fallbackID == "" {
			return nil, &Error{Code: "all_unavailable", Message: "all accounts in group are unavailable"}
		}
		if auth, ok := authsByID[fallbackID]; ok {
			return auth, nil
		}
		return nil, &Error{Code: "auth_not_found", Message: "fallback account not found"}
	}

	auths := make([]*Auth, 0, len(available))
	for _, id := range available {
		if auth, ok := authsByID[id]; ok {
			auths = append(auths, auth)
		}
	}

	if len(auths) == 0 {
		return nil, &Error{Code: "auth_not_found", Message: "no auth candidates in group"}
	}

	switch group.SelectionStrategy {
	case routing.SelectionRandom:
		return gs.pickRandom(auths)
	case routing.SelectionPriority:
		return gs.pickPriority(auths, group.AccountIDs)
	default:
		return gs.pickRoundRobinFromGroup(group.ID, model, auths)
	}
}

// pickRoundRobinFromGroup performs round-robin selection within a group.
func (gs *GroupSelector) pickRoundRobinFromGroup(groupID, model string, auths []*Auth) (*Auth, error) {
	if len(auths) == 0 {
		return nil, &Error{Code: "auth_not_found", Message: "no auth candidates"}
	}

	if len(auths) > 1 {
		sort.Slice(auths, func(i, j int) bool { return auths[i].ID < auths[j].ID })
	}

	key := groupID + ":" + model
	gs.groupMu.Lock()
	index := gs.groupCursors[key]
	if index >= 2_147_483_640 {
		index = 0
	}
	gs.groupCursors[key] = index + 1
	gs.groupMu.Unlock()

	return auths[index%len(auths)], nil
}

// pickRandom selects a random auth (simple implementation using time as seed).
func (gs *GroupSelector) pickRandom(auths []*Auth) (*Auth, error) {
	if len(auths) == 0 {
		return nil, &Error{Code: "auth_not_found", Message: "no auth candidates"}
	}
	index := int(time.Now().UnixNano()) % len(auths)
	return auths[index], nil
}

// pickPriority selects the first available auth in the original order.
func (gs *GroupSelector) pickPriority(auths []*Auth, orderedIDs []string) (*Auth, error) {
	authMap := make(map[string]*Auth, len(auths))
	for _, auth := range auths {
		authMap[auth.ID] = auth
	}

	for _, id := range orderedIDs {
		if auth, ok := authMap[id]; ok {
			return auth, nil
		}
	}

	if len(auths) > 0 {
		return auths[0], nil
	}
	return nil, &Error{Code: "auth_not_found", Message: "no auth candidates"}
}

// RecordResult updates health state based on request result.
func (gs *GroupSelector) RecordResult(result Result) {
	if result.AuthID == "" {
		return
	}

	if result.Success {
		gs.healthTracker.MarkSuccess(result.AuthID)
		return
	}

	if result.Error != nil {
		statusCode := result.Error.HTTPStatus
		switch statusCode {
		case 429:
			gs.healthTracker.MarkRateLimited(result.AuthID, result.RetryAfter)
		case 500, 502, 503, 504:
			gs.healthTracker.MarkError(result.AuthID, result.Error)
		default:
			gs.healthTracker.MarkError(result.AuthID, result.Error)
		}
	}
}

// ResetAccountHealth resets health state for an account.
func (gs *GroupSelector) ResetAccountHealth(accountID string) {
	gs.healthTracker.Reset(accountID)
}

// ResetAllHealth resets health state for all accounts.
func (gs *GroupSelector) ResetAllHealth() {
	gs.healthTracker.ResetAll()
}
