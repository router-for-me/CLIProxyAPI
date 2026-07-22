package auth

import "strings"

const (
	AuthKindAPIKey         = "apikey"
	AuthKindOAuth          = "oauth"
	AuthKindAgentIdentity  = "agent_identity"

	AuthSourceConfig      = "config"
	AuthSourceFile        = "file"
	AuthSourceGit         = "git"
	AuthSourceMemory      = "memory"
	AuthSourceObjectStore = "objectstore"
	AuthSourcePostgres    = "postgres"

	AttributeAPIKey        = "api_key"
	AttributeAuthKind      = "auth_kind"
	AttributePath          = "path"
	AttributeRuntimeOnly   = "runtime_only"
	AttributeSource        = "source"
	AttributeSourceBackend = "source_backend"
)

// AuthKind returns the credential kind using explicit metadata first and legacy
// field-shape fallbacks second.
func (a *Auth) AuthKind() string {
	if a == nil {
		return ""
	}
	if kind := normalizeAuthKind(authAttribute(a, AttributeAuthKind)); kind != "" {
		return kind
	}
	if kind := normalizeAuthKind(authMetadataString(a, AttributeAuthKind)); kind != "" {
		return kind
	}
	// Agent Identity must be classified before OAuth fallbacks: files often retain
	// email / stale refresh_token fields that would otherwise look like OAuth.
	if IsAgentIdentityAuth(a) {
		return AuthKindAgentIdentity
	}
	if authAttribute(a, AttributeAPIKey) != "" {
		return AuthKindAPIKey
	}
	if authHasOAuthMetadata(a) {
		return AuthKindOAuth
	}
	return ""
}

// AuthSourceKind returns where the Auth entry came from at runtime.
func (a *Auth) AuthSourceKind() string {
	if a == nil {
		return ""
	}
	if strings.EqualFold(authAttribute(a, AttributeRuntimeOnly), "true") {
		return AuthSourceMemory
	}
	if source := normalizeAuthSourceKind(authAttribute(a, AttributeSourceBackend)); source != "" {
		return source
	}
	source := authAttribute(a, AttributeSource)
	if source != "" {
		sourceLower := strings.ToLower(source)
		if strings.HasPrefix(sourceLower, AuthSourceConfig+":") {
			return AuthSourceConfig
		}
		if normalized := normalizeAuthSourceKind(source); normalized != "" {
			return normalized
		}
		return AuthSourceFile
	}
	if authAttribute(a, AttributePath) != "" {
		return AuthSourceFile
	}
	if strings.TrimSpace(a.FileName) != "" {
		return AuthSourceFile
	}
	return ""
}

func normalizeAuthKind(kind string) string {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case AuthKindAPIKey, "api_key", "api-key":
		return AuthKindAPIKey
	case AuthKindOAuth, "oauth2":
		return AuthKindOAuth
	case AuthKindAgentIdentity, "agent-identity":
		return AuthKindAgentIdentity
	default:
		return ""
	}
}

func normalizeAuthSourceKind(source string) string {
	switch strings.ToLower(strings.TrimSpace(source)) {
	case AuthSourceConfig:
		return AuthSourceConfig
	case AuthSourceFile, "filesystem":
		return AuthSourceFile
	case AuthSourceGit:
		return AuthSourceGit
	case AuthSourceMemory, "runtime", "runtime_only":
		return AuthSourceMemory
	case AuthSourceObjectStore, "object-store":
		return AuthSourceObjectStore
	case AuthSourcePostgres, "postgresql", "database", "db":
		return AuthSourcePostgres
	default:
		return ""
	}
}


// IsAgentIdentityAuth reports whether the auth carries Codex Agent Identity material.
// Detection prefers explicit auth_kind/type, then required signing fields.
func IsAgentIdentityAuth(auth *Auth) bool {
	if auth == nil {
		return false
	}
	if kind := normalizeAuthKind(authAttribute(auth, AttributeAuthKind)); kind == AuthKindAgentIdentity {
		return true
	}
	if kind := normalizeAuthKind(authMetadataString(auth, AttributeAuthKind)); kind == AuthKindAgentIdentity {
		return true
	}
	if strings.EqualFold(authMetadataString(auth, "type"), AuthKindAgentIdentity) {
		return true
	}
	runtimeID := authMetadataString(auth, "agent_runtime_id")
	taskID := authMetadataString(auth, "task_id")
	privateKey := authMetadataString(auth, "agent_private_key")
	if privateKey == "" {
		privateKey = authMetadataString(auth, "private_key_pkcs8_base64")
	}
	if privateKey == "" {
		privateKey = authMetadataString(auth, "private_key")
	}
	return runtimeID != "" && taskID != "" && privateKey != ""
}

func authHasOAuthMetadata(auth *Auth) bool {
	if auth == nil || len(auth.Metadata) == 0 {
		return false
	}
	for _, key := range []string{"access_token", "refresh_token", "id_token", "email", "token_type", "expires_at", "expired"} {
		if authMetadataString(auth, key) != "" {
			return true
		}
	}
	if token, ok := auth.Metadata["token"].(map[string]any); ok && len(token) > 0 {
		return true
	}
	return false
}

func authAttribute(auth *Auth, key string) string {
	if auth == nil || auth.Attributes == nil {
		return ""
	}
	return strings.TrimSpace(auth.Attributes[key])
}

func authMetadataString(auth *Auth, key string) string {
	if auth == nil || auth.Metadata == nil {
		return ""
	}
	switch value := auth.Metadata[key].(type) {
	case string:
		return strings.TrimSpace(value)
	default:
		return ""
	}
}
