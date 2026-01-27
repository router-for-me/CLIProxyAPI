package auth

import (
	"context"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

func isProviderTypeAuth(auth *Auth) bool {
	if auth == nil {
		return false
	}
	switch auth.Provider {
	case "openai-compatibility", "vertex":
		return true
	default:
		return false
	}
}

func splitAuthsByProviderType(auths []*Auth) (providers []*Auth, credentials []*Auth) {
	if len(auths) == 0 {
		return nil, nil
	}
	providers = make([]*Auth, 0, len(auths))
	credentials = make([]*Auth, 0, len(auths))
	for _, item := range auths {
		if isProviderTypeAuth(item) {
			providers = append(providers, item)
		} else {
			credentials = append(credentials, item)
		}
	}
	return providers, credentials
}

// ProviderFirstSelector preserves legacy behavior where routing.strategy could be "provider-first".
// It delegates to PreferenceSelector with a fixed provider-first preference.
type ProviderFirstSelector struct {
	PreferenceSelector
}

func (s *ProviderFirstSelector) Pick(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	if s.preference == "" {
		s.preference = RoutingStrategyProviderFirst
	}
	if s.strategy == "" {
		s.strategy = RoutingStrategyRoundRobin
	}
	return s.PreferenceSelector.Pick(ctx, provider, model, opts, auths)
}

// CredentialFirstSelector preserves legacy behavior where routing.strategy could be "credential-first".
// It delegates to PreferenceSelector with a fixed credential-first preference.
type CredentialFirstSelector struct {
	PreferenceSelector
}

func (s *CredentialFirstSelector) Pick(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	if s.preference == "" {
		s.preference = RoutingStrategyCredentialFirst
	}
	if s.strategy == "" {
		s.strategy = RoutingStrategyRoundRobin
	}
	return s.PreferenceSelector.Pick(ctx, provider, model, opts, auths)
}

