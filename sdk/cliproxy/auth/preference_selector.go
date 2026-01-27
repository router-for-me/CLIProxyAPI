package auth

import (
	"context"
	"sort"
	"sync"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

// PreferenceSelector applies a group preference (provider-first / credential-first)
// and uses a same-level strategy (round-robin / fill-first) within the chosen group.
type PreferenceSelector struct {
	preference string
	strategy   string

	mu      sync.Mutex
	cursors map[string]int
}

func (s *PreferenceSelector) pickRoundRobin(keyPrefix, provider, model string, now time.Time, auths []*Auth) (*Auth, error) {
	available, err := getAvailableAuths(auths, provider, model, now)
	if err != nil {
		return nil, err
	}
	key := keyPrefix + ":" + provider + ":" + model
	s.mu.Lock()
	if s.cursors == nil {
		s.cursors = make(map[string]int)
	}
	index := s.cursors[key]
	if index >= 2_147_483_640 {
		index = 0
	}
	s.cursors[key] = index + 1
	s.mu.Unlock()
	return available[index%len(available)], nil
}

func (s *PreferenceSelector) pickFillFirst(provider, model string, now time.Time, auths []*Auth) (*Auth, error) {
	available, err := getAvailableAuths(auths, provider, model, now)
	if err != nil {
		return nil, err
	}
	if len(available) > 1 {
		sort.Slice(available, func(i, j int) bool { return available[i].ID < available[j].ID })
	}
	return available[0], nil
}

func (s *PreferenceSelector) pickSameLevel(keyPrefix, provider, model string, now time.Time, auths []*Auth) (*Auth, error) {
	switch s.strategy {
	case RoutingStrategyFillFirst:
		return s.pickFillFirst(provider, model, now, auths)
	default:
		return s.pickRoundRobin(keyPrefix, provider, model, now, auths)
	}
}

func (s *PreferenceSelector) Pick(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	_ = ctx
	_ = opts
	now := time.Now()
	providers, credentials := splitAuthsByProviderType(auths)

	var primary []*Auth
	var secondary []*Auth
	if s.preference == RoutingStrategyCredentialFirst {
		primary = credentials
		secondary = providers
	} else {
		primary = providers
		secondary = credentials
	}

	var primaryErr error
	if len(primary) > 0 {
		keyPrefix := s.preference + ":primary:" + s.strategy
		picked, err := s.pickSameLevel(keyPrefix, provider, model, now, primary)
		if err == nil {
			return picked, nil
		}
		primaryErr = err
	}
	if len(secondary) > 0 {
		keyPrefix := s.preference + ":secondary:" + s.strategy
		picked, err := s.pickSameLevel(keyPrefix, provider, model, now, secondary)
		if err == nil {
			return picked, nil
		}
		if primaryErr == nil {
			return nil, err
		}
		return nil, primaryErr
	}
	if primaryErr != nil {
		return nil, primaryErr
	}
	return nil, &Error{Code: "auth_not_found", Message: "no auth candidates"}
}

