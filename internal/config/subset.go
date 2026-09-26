package config

// SubsetConfig configures optional credential subset routing driven by the
// X-Auth-Subset request header. The feature is disabled by default; when
// disabled the header is ignored and selection behavior is unchanged.
type SubsetConfig struct {
	// Enabled toggles X-Auth-Subset subset routing. Default: false.
	Enabled bool `yaml:"enabled" json:"enabled"`

	// EmptyPolicy controls behavior when every entry of a request's subset is
	// unknown to the credential pool: "fallback" (default) selects from the
	// full pool, "reject" fails the request with an HTTP 429 JSON error.
	EmptyPolicy string `yaml:"empty-policy" json:"empty-policy"`

	// RequireSignature reserves signature verification of the X-Auth-Subset
	// header. Configuration only; verification is not implemented yet.
	RequireSignature bool `yaml:"require-signature" json:"require-signature"`

	// SignatureKey reserves the shared key for the future header signature
	// verification. Configuration only; not used yet.
	SignatureKey string `yaml:"signature-key" json:"signature-key"`
}
