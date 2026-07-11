package auth

// IsConfigAPIKeyAuth reports whether the auth entry is synthesized from a
// configured API-key provider, including keyless OpenAI-compatible entries.
func IsConfigAPIKeyAuth(auth *Auth) bool {
	if auth == nil {
		return false
	}
	if auth.AuthSourceKind() != AuthSourceConfig {
		return false
	}
	switch auth.AuthKind() {
	case AuthKindAPIKey:
		return authAttribute(auth, AttributeAPIKey) != ""
	case AuthKindOAuth:
		return false
	}
	return auth.Attributes != nil && authAttribute(auth, "compat_name") != ""
}
