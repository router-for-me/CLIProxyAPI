package auth

import (
	"context"
	"strings"
	"sync"
	"time"
)

type antigravityUseCreditsContextKey struct{}

// WithAntigravityCredits returns a child context that signals the executor to
// inject enabledCreditTypes into the request payload.
func WithAntigravityCredits(ctx context.Context) context.Context {
	return context.WithValue(ctx, antigravityUseCreditsContextKey{}, true)
}

// AntigravityCreditsRequested reports whether the context carries the credits flag.
func AntigravityCreditsRequested(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	v, _ := ctx.Value(antigravityUseCreditsContextKey{}).(bool)
	return v
}

// AntigravityCreditsHint stores the latest known AI credits state for one auth.
type AntigravityCreditsHint struct {
	Known           bool
	Available       bool
	CreditAmount    float64
	MinCreditAmount float64
	PaidTierID      string
	UpdatedAt       time.Time
}

var antigravityCreditsHintByAuth sync.Map

// SetAntigravityCreditsHint updates the latest known AI credits state for an auth.
func SetAntigravityCreditsHint(authID string, hint AntigravityCreditsHint) {
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return
	}
	if hint.UpdatedAt.IsZero() {
		hint.UpdatedAt = time.Now()
	}
	antigravityCreditsHintByAuth.Store(authID, hint)
}

// GetAntigravityCreditsHint returns the latest known AI credits state for an auth.
func GetAntigravityCreditsHint(authID string) (AntigravityCreditsHint, bool) {
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return AntigravityCreditsHint{}, false
	}
	value, ok := antigravityCreditsHintByAuth.Load(authID)
	if !ok {
		return AntigravityCreditsHint{}, false
	}
	hint, ok := value.(AntigravityCreditsHint)
	if !ok {
		antigravityCreditsHintByAuth.Delete(authID)
		return AntigravityCreditsHint{}, false
	}
	return hint, true
}

// HasKnownAntigravityCreditsHint reports whether credits state has been discovered for an auth.
func HasKnownAntigravityCreditsHint(authID string) bool {
	hint, ok := GetAntigravityCreditsHint(authID)
	return ok && hint.Known
}

func antigravityCreditsAvailableForModel(auth *Auth, model string) bool {
	if auth == nil {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(auth.Provider), "antigravity") {
		return false
	}
	if !strings.Contains(strings.ToLower(strings.TrimSpace(model)), "claude") {
		return false
	}
	hint, ok := GetAntigravityCreditsHint(auth.ID)
	if !ok || !hint.Known {
		return false
	}
	return hint.Available
}

// AntigravityModelQuota holds per-model quota details from the fetchAvailableModels API.
// Mirrors Rust: models::quota::ModelQuota
type AntigravityModelQuota struct {
	Name               string
	Percentage         int    // 0-100, derived from remainingFraction * 100
	ResetTime          string // ISO8601
	DisplayName        string
	SupportsImages     bool
	SupportsThinking   bool
	ThinkingBudget     int
	Recommended        bool
	MaxTokens          int
	MaxOutputTokens    int
	SupportedMimeTypes map[string]bool
}

// AntigravityQuotaData holds the full quota snapshot from fetchAvailableModels.
// Mirrors Rust: models::quota::QuotaData
type AntigravityQuotaData struct {
	Models               []AntigravityModelQuota
	LastUpdated          int64
	IsForbidden          bool
	ForbiddenReason      string
	SubscriptionTier     string
	ModelForwardingRules map[string]string // old_model_id -> new_model_id
}

var antigravityQuotaDataByAuth sync.Map

// SetAntigravityQuotaData stores the latest quota snapshot for an auth.
func SetAntigravityQuotaData(authID string, data AntigravityQuotaData) {
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return
	}
	antigravityQuotaDataByAuth.Store(authID, data)
}

// GetAntigravityQuotaData returns the latest quota snapshot for an auth.
func GetAntigravityQuotaData(authID string) (AntigravityQuotaData, bool) {
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return AntigravityQuotaData{}, false
	}
	value, ok := antigravityQuotaDataByAuth.Load(authID)
	if !ok {
		return AntigravityQuotaData{}, false
	}
	data, ok := value.(AntigravityQuotaData)
	if !ok {
		antigravityQuotaDataByAuth.Delete(authID)
		return AntigravityQuotaData{}, false
	}
	return data, true
}

// antigravityAllowedModelPrefixes defines which model names are relevant.
// Mirrors Rust filter in quota.rs: only models starting with these prefixes are recorded.
var antigravityAllowedModelPrefixes = []string{"gemini", "claude", "gpt", "image", "imagen"}

// IsAntigravityRelevantModel reports whether a model name should be tracked.
func IsAntigravityRelevantModel(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	for _, prefix := range antigravityAllowedModelPrefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}
