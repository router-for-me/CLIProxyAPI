package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"time"
)

// ModelAvailability reports why selection skips a credential for a model.
type ModelAvailability struct {
	Blocked       bool
	QuotaCooldown bool
	RecoverAt     time.Time
}

// AvailabilityForModel returns the selection availability of auth for model at now.
func AvailabilityForModel(auth *Auth, model string, now time.Time) ModelAvailability {
	blocked, reason, next := isAuthBlockedForModel(auth, model, now)
	return ModelAvailability{Blocked: blocked, QuotaCooldown: blocked && reason == blockReasonCooldown, RecoverAt: next}
}

// CredentialReferenceID returns the stable secret-free identifier used by the
// management inventory and model-routing allowlists.
func CredentialReferenceID(rawID string) string {
	digest := sha256.Sum256([]byte(rawID))
	return "sha256:" + hex.EncodeToString(digest[:])
}

// RoutingQuotaDomain returns the declared quota pool. Providers without a
// finer declaration share their provider pool; distinct pools are never inferred.
func (a *Auth) RoutingQuotaDomain() string {
	if a == nil {
		return ""
	}
	if a.QuotaDomain != "" {
		return a.QuotaDomain
	}
	return a.Provider
}

// RoutingCredentialRevision identifies a registration and version without secrets.
func (a *Auth) RoutingCredentialRevision() string {
	return strconv.FormatUint(a.RegistrationEpoch, 10) + ":" + strconv.FormatUint(a.CredentialVersion, 10)
}
