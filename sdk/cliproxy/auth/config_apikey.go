package auth

import "github.com/router-for-me/CLIProxyAPI/v8/internal/config"

// ConfiguredKeyPayloadSource returns the policy owner for a locally configured key.
func (auth *Auth) ConfiguredKeyPayloadSource() config.PayloadFilterSource {
	if auth == nil || auth.AuthKind() == AuthKindOAuth || auth.AuthSourceKind() != AuthSourceConfig {
		return nil
	}
	return auth.ConfigKey
}

// IsConfigAPIKeyAuth reports whether the auth entry is synthesized from config *-api-key lists.
func IsConfigAPIKeyAuth(auth *Auth) bool {
	if auth == nil {
		return false
	}
	if auth.AuthKind() != AuthKindAPIKey {
		return false
	}
	return auth.AuthSourceKind() == AuthSourceConfig
}
