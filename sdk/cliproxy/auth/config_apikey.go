package auth

import "strings"

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

// IsConfigADCAuth reports whether the auth entry is synthesized from config
// vertex-adc entries. Keyless by design, so IsConfigAPIKeyAuth does not
// cover it, but it is equally config-owned: the auth dir and credential
// stores must not retain a copy that outlives the config entry.
func IsConfigADCAuth(auth *Auth) bool {
	if auth == nil {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(auth.Provider), "vertex") {
		return false
	}
	if auth.AuthSourceKind() != AuthSourceConfig {
		return false
	}
	if auth.Metadata == nil {
		return false
	}
	adc, _ := auth.Metadata["adc"].(bool)
	return adc
}
