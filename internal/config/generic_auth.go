package config

// GenericAuth configures validation of bearer tokens against a generic OAuth2
// introspection or userinfo endpoint.
//
// This is used by internal/auth/generic helpers and is intentionally small; most
// deployments will configure it via the access-provider system.
type GenericAuth struct {
	// ProviderID is the provider key associated with this auth entry (e.g. "generic").
	ProviderID string `yaml:"provider-id,omitempty" json:"provider_id,omitempty"`

	// IntrospectionURL is the token introspection/userinfo endpoint URL.
	IntrospectionURL string `yaml:"introspection-url,omitempty" json:"introspection_url,omitempty"`

	// ClientID and ClientSecret are optional client credentials for RFC 7662 introspection endpoints.
	ClientID     string `yaml:"client-id,omitempty" json:"client_id,omitempty"`
	ClientSecret string `yaml:"client-secret,omitempty" json:"client_secret,omitempty"`

	// UserIDField is the JSON field to use as the stable user identifier (default: "sub").
	UserIDField string `yaml:"user-id-field,omitempty" json:"user_id_field,omitempty"`

	// EmailField is the JSON field to use as the user email (default: "email").
	EmailField string `yaml:"email-field,omitempty" json:"email_field,omitempty"`
}
