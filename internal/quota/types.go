// Package quota provides quota fetching functionality for various AI providers.
// It allows clients to check remaining usage quota for connected accounts.
package quota

import (
	"time"
)

// ModelQuota represents quota information for a single model or quota category.
type ModelQuota struct {
	// Name is the model or category identifier (e.g., "gemini-3-pro-high", "codex-session")
	Name string `json:"name"`
	// Percentage is the remaining quota as a percentage (0-100). -1 means unavailable.
	Percentage float64 `json:"percentage"`
	// ResetTime is when the quota resets, in RFC3339 format.
	ResetTime string `json:"reset_time,omitempty"`
	// Used is the amount of quota used (optional, provider-specific).
	Used *int64 `json:"used,omitempty"`
	// Limit is the total quota limit (optional, provider-specific).
	Limit *int64 `json:"limit,omitempty"`
	// Remaining is the remaining quota (optional, provider-specific).
	Remaining *int64 `json:"remaining,omitempty"`
}

// ProviderQuotaData represents quota data for one account of a provider.
type ProviderQuotaData struct {
	// Models contains quota info for each model/category.
	Models []ModelQuota `json:"models"`
	// LastUpdated is when the quota was last fetched.
	LastUpdated time.Time `json:"last_updated"`
	// IsForbidden indicates if the account has been blocked/forbidden.
	IsForbidden bool `json:"is_forbidden"`
	// PlanType is the subscription plan type (e.g., "g1-pro-tier", "plus").
	PlanType string `json:"plan_type,omitempty"`
	// Error contains any error message from fetching quota.
	Error string `json:"error,omitempty"`
	// Extra contains provider-specific additional data.
	Extra map[string]any `json:"extra,omitempty"`
}

// QuotaResponse is the full API response for quota requests.
type QuotaResponse struct {
	// Quotas maps provider -> account -> quota data.
	Quotas map[string]map[string]*ProviderQuotaData `json:"quotas"`
	// LastUpdated is when the overall response was generated.
	LastUpdated time.Time `json:"last_updated"`
}

// ProviderQuotaResponse is the response for a specific provider's quota.
type ProviderQuotaResponse struct {
	// Provider is the provider name.
	Provider string `json:"provider"`
	// Accounts maps account ID -> quota data.
	Accounts map[string]*ProviderQuotaData `json:"accounts"`
	// LastUpdated is when the response was generated.
	LastUpdated time.Time `json:"last_updated"`
}

// AccountQuotaResponse is the response for a specific account's quota.
type AccountQuotaResponse struct {
	// Provider is the provider name.
	Provider string `json:"provider"`
	// Account is the account identifier (email or ID).
	Account string `json:"account"`
	// Quota is the quota data for this account.
	Quota *ProviderQuotaData `json:"quota"`
}

// SubscriptionTier represents subscription tier information.
type SubscriptionTier struct {
	// ID is the tier identifier (e.g., "g1-pro-tier").
	ID string `json:"id"`
	// Name is the human-readable tier name.
	Name string `json:"name"`
	// Description provides details about the tier.
	Description string `json:"description,omitempty"`
	// IsDefault indicates if this is the default tier.
	IsDefault bool `json:"is_default,omitempty"`
	// UpgradeURL is the URL to upgrade the subscription.
	UpgradeURL string `json:"upgrade_url,omitempty"`
}

// SubscriptionInfo represents subscription info for an account.
type SubscriptionInfo struct {
	// CurrentTier is the current subscription tier.
	CurrentTier *SubscriptionTier `json:"current_tier,omitempty"`
	// ProjectID is the GCP project ID (for Antigravity/Gemini-CLI).
	ProjectID string `json:"project_id,omitempty"`
	// UpgradeURL is the URL to upgrade the subscription.
	UpgradeURL string `json:"upgrade_url,omitempty"`
}

// SubscriptionInfoResponse is the response for subscription info.
type SubscriptionInfoResponse struct {
	// Subscriptions maps account ID -> subscription info.
	Subscriptions map[string]*SubscriptionInfo `json:"subscriptions"`
}

// RefreshRequest is the request body for forcing quota refresh.
type RefreshRequest struct {
	// Providers limits refresh to specific providers. If empty, refresh all.
	Providers []string `json:"providers,omitempty"`
}

// UnavailableQuota returns a ProviderQuotaData indicating quota is not available.
func UnavailableQuota(reason string) *ProviderQuotaData {
	return &ProviderQuotaData{
		Models: []ModelQuota{
			{
				Name:       "quota",
				Percentage: -1,
			},
		},
		LastUpdated: time.Now(),
		IsForbidden: false,
		PlanType:    "unavailable",
		Error:       reason,
	}
}