package auth

import "strings"

const (
	AttrAuthKind                 = "auth_kind"
	AttrAuthSource               = "auth_source"
	AttrAuthSourceCommand        = "command"
	AttrAuthCommand              = "auth_command"
	AttrAuthArgsJSON             = "auth_args_json"
	AttrAuthCommandKey           = "auth_command_key"
	AttrAuthTimeoutMS            = "auth_timeout_ms"
	AttrAuthRefreshIntervalMS    = "auth_refresh_interval_ms"
	AttrAuthInvalidatesOnNext401 = "auth_invalidate_on_401"
	AttrAuthKindAPIKey           = "apikey"
	AttrAuthKindAPIKeyUnderscore = "api_key"
)

// IsConfigAPIKeyAuth reports whether the auth entry is synthesized from config *-api-key lists.
func IsConfigAPIKeyAuth(auth *Auth) bool {
	if auth == nil {
		return false
	}
	if auth.AuthKind() != AuthKindAPIKey {
		return false
	}
	if auth.AuthSourceKind() != AuthSourceConfig {
		return false
	}
	return authAttribute(auth, AttributeAPIKey) != ""
}

// IsConfigCredentialAuth reports whether the auth entry is synthesized from configuration
// credential sections. This includes static API keys and command-backed API-key-class auths.
func IsConfigCredentialAuth(auth *Auth) bool {
	if auth == nil || auth.Attributes == nil {
		return false
	}
	source := strings.ToLower(strings.TrimSpace(auth.Attributes["source"]))
	if !strings.HasPrefix(source, "config:") {
		return false
	}
	if strings.TrimSpace(auth.Attributes["api_key"]) != "" {
		return true
	}
	authKind := strings.ToLower(strings.TrimSpace(auth.Attributes[AttrAuthKind]))
	if authKind == AttrAuthKindAPIKey || authKind == AttrAuthKindAPIKeyUnderscore {
		return true
	}
	return IsCommandAuth(auth)
}

// IsCommandAuth reports whether the auth entry obtains its bearer token from a configured command.
func IsCommandAuth(auth *Auth) bool {
	if auth == nil || auth.Attributes == nil {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(auth.Attributes[AttrAuthSource]), AttrAuthSourceCommand) {
		return false
	}
	return strings.TrimSpace(auth.Attributes[AttrAuthCommand]) != ""
}
