package auth

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
