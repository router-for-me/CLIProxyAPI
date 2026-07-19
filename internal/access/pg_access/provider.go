// Package pgaccess implements the PG-backed access provider. Unlike the
// config-inline provider, which validates a flat list of opaque strings, this
// provider looks up credentials in the api_keys table by their SHA-256 hash
// and only accepts keys whose status is 'active'. The plaintext key is
// returned as Result.Principal so downstream middleware (the policy service)
// can resolve the matching key+policy via the same hash.
package pgaccess

import (
	"context"
	"net/http"
	"strings"
	"sync"

	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
)

// Identifier is the name surfaced via Result.Provider when this provider
// authenticates a request. The trailing "-inline" line from the config
// provider is intentionally avoided so logs distinguish PG-managed from
// file-only keys.
const Identifier = "pg-store"

// Lookup is the minimal contract the provider needs from the API key store.
// Aliased as a local interface so the package can be unit-tested with a
// stub instead of a real Postgres connection.
type Lookup interface {
	LookupByHash(ctx context.Context, hash string) (*store.APIKey, *store.Policy, error)
}

// Provider validates credentials against the api_keys table. It mirrors the
// extraction order of the config-inline provider (Authorization Bearer →
// X-Goog-Api-Key → X-Api-Key → ?key → ?auth_token) so callers can mix the two
// providers without picking up inconsistent credential sources.
type Provider struct {
	name   string
	lookup Lookup

	// mu guards the read-only name field against hot-swap via SetName (used
	// during tests). Production callers do not mutate the name.
	mu sync.RWMutex
}

// New constructs a Provider bound to an APIKeyStore (the concrete Lookup
// implementation). Returns nil when lookup is nil so the caller can
// feature-detect PG backing via a single nil check.
func New(lookup Lookup) *Provider {
	if lookup == nil {
		return nil
	}
	return &Provider{name: Identifier, lookup: lookup}
}

// SetName overrides the provider identifier. Useful for testing; production
// code should leave the default.
func (p *Provider) SetName(name string) {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.name = strings.TrimSpace(name)
	if p.name == "" {
		p.name = Identifier
	}
	p.mu.Unlock()
}

// Identifier returns the provider name surfaced in Result.Provider.
func (p *Provider) Identifier() string {
	if p == nil {
		return ""
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.name == "" {
		return Identifier
	}
	return p.name
}

// Authenticate extracts a credential candidate from the request and looks it
// up by SHA-256 hash. On a hit the plaintext key is returned as Principal so
// the downstream policy service can re-resolve the same key+policy.
func (p *Provider) Authenticate(ctx context.Context, r *http.Request) (*sdkaccess.Result, *sdkaccess.AuthError) {
	if p == nil || p.lookup == nil {
		return nil, sdkaccess.NewNotHandledError()
	}
	authHeader := r.Header.Get("Authorization")
	authHeaderGoogle := r.Header.Get("X-Goog-Api-Key")
	authHeaderAnthropic := r.Header.Get("X-Api-Key")
	queryKey := ""
	queryAuthToken := ""
	if r.URL != nil {
		queryKey = r.URL.Query().Get("key")
		queryAuthToken = r.URL.Query().Get("auth_token")
	}
	if authHeader == "" && authHeaderGoogle == "" && authHeaderAnthropic == "" && queryKey == "" && queryAuthToken == "" {
		return nil, sdkaccess.NewNoCredentialsError()
	}

	apiKey := extractBearerToken(authHeader)

	candidates := []struct {
		value  string
		source string
	}{
		{apiKey, "authorization"},
		{authHeaderGoogle, "x-goog-api-key"},
		{authHeaderAnthropic, "x-api-key"},
		{queryKey, "query-key"},
		{queryAuthToken, "query-auth-token"},
	}

	for _, candidate := range candidates {
		if candidate.value == "" {
			continue
		}
		hash := store.HashSecret(candidate.value)
		key, _, err := p.lookup.LookupByHash(ctx, hash)
		if err != nil {
			// ErrAPIKeyNotFound is expected for file-only keys or unknown
			// principals: defer to the next provider rather than reject,
			// so config-inline and pg-store can coexist.
			if isNotFound(err) {
				continue
			}
			// Genuine infra failures bubble as internal errors so the
			// manager logs them; AuthMiddleware will translate to 500.
			return nil, sdkaccess.NewInternalAuthError("pg access provider lookup failed", err)
		}
		if key.Status != store.APIKeyStatusActive {
			return nil, sdkaccess.NewInvalidCredentialError()
		}
		return &sdkaccess.Result{
			Provider:  p.Identifier(),
			Principal: candidate.value,
			Metadata: map[string]string{
				"source": candidate.source,
				"key_id": key.ID,
			},
		}, nil
	}

	// No candidate matched a PG-stored key. Returning NotHandled lets the
	// manager fall through to the config-inline provider (which covers the
	// legacy global api-keys list) when both are registered.
	return nil, sdkaccess.NewNotHandledError()
}

// isNotFound reports whether err wraps store.ErrAPIKeyNotFound without
// forcing this package to import store errors directly at call sites. The
// check stays local to avoid an extra import alias churn.
func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "api key not found")
}

func extractBearerToken(header string) string {
	if header == "" {
		return ""
	}
	parts := strings.SplitN(header, " ", 2)
	if len(parts) != 2 {
		return header
	}
	if strings.ToLower(parts[0]) != "bearer" {
		return header
	}
	return strings.TrimSpace(parts[1])
}

// Compile-time assertion that *Provider satisfies sdkaccess.Provider.
var _ sdkaccess.Provider = (*Provider)(nil)
