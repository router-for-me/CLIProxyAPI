package quota

import (
	"context"

	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

// Fetcher defines the interface for quota fetchers.
// Each provider implements this interface to fetch quota data.
type Fetcher interface {
	// Provider returns the provider name (e.g., "antigravity", "codex").
	Provider() string

	// SupportedProviders returns all provider names this fetcher supports.
	// For example, antigravity fetcher may support both "antigravity" and "gemini-cli".
	SupportedProviders() []string

	// FetchQuota fetches quota for a single auth credential.
	// Returns nil ProviderQuotaData if the provider doesn't support quota fetching.
	FetchQuota(ctx context.Context, auth *coreauth.Auth) (*ProviderQuotaData, error)

	// CanFetch returns true if this fetcher can handle the given provider.
	CanFetch(provider string) bool
}