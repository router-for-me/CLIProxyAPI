package handlers

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/buildinfo"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// poolStatusResponse is the top-level payload returned by /v1/pool/status.
type poolStatusResponse struct {
	Timestamp string                         `json:"timestamp"`
	Version   string                         `json:"version"`
	Providers map[string]*poolStatusProvider `json:"providers"`
}

// poolStatusProvider aggregates one provider's credential pool health.
type poolStatusProvider struct {
	Total               int                  `json:"total"`
	PrimaryTotal        int                  `json:"primary_total"`
	PrimaryOpen         int                  `json:"primary_open"`
	PrimaryCooling      int                  `json:"primary_cooling"`
	LastResortOpen      int                  `json:"last_resort_open"`
	CoolingRatio        float64              `json:"cooling_ratio"`
	PrimaryCoolingRatio float64              `json:"primary_cooling_ratio"`
	Status              string               `json:"status"`
	EarliestRecovery    *string              `json:"earliest_recovery"`
	ActiveSessionsTotal int                  `json:"active_sessions_total"`
	Accounts            []*poolStatusAccount `json:"accounts"`
	coolingCount        int
	unavailableCount    int
	hasModelRestriction bool
}

// poolStatusAccount describes a single credential without leaking secrets or PII.
type poolStatusAccount struct {
	AuthID           string            `json:"auth_id"`
	Priority         int               `json:"priority"`
	Status           string            `json:"status"`
	Reason           *string           `json:"reason"`
	ResetsAt         *string           `json:"resets_at"`
	RemainingSeconds int64             `json:"remaining_seconds"`
	ActiveSessions   int               `json:"active_sessions"`
	ModelsCooling    map[string]string `json:"models_cooling"`
	ModelsBlocked    []string          `json:"models_blocked,omitempty"`
}

var poolStatusEmailPattern = regexp.MustCompile(`([a-zA-Z0-9._%+-]{1,10})[a-zA-Z0-9._%+-]*@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}`)

// poolStatusAtTokenPattern is a defensive fallback for email-like tokens the
// strict ASCII pattern misses (for example non-ASCII local parts or domains):
// any remaining whitespace-delimited token containing '@' is masked.
var poolStatusAtTokenPattern = regexp.MustCompile(`([a-zA-Z0-9._%+-]{0,10})[^\s@]*@\S*`)

// sanitizePoolAuthID strips path and extension noise and masks any embedded
// email local part so the unauthenticated endpoint never leaks PII.
func sanitizePoolAuthID(raw string) string {
	raw = filepath.Base(strings.TrimSuffix(strings.TrimSuffix(raw, ".json"), ".cds"))
	masked := poolStatusEmailPattern.ReplaceAllString(raw, "${1}***")
	return poolStatusAtTokenPattern.ReplaceAllString(masked, "${1}***")
}

// poolAccountID picks the best operator-facing identifier for an auth.
func poolAccountID(auth *coreauth.Auth) string {
	raw := strings.TrimSpace(auth.Label)
	if raw == "" {
		raw = strings.TrimSpace(auth.ID)
	}
	if raw == "" {
		raw = strings.TrimSpace(auth.FileName)
	}
	return sanitizePoolAuthID(raw)
}

// disambiguatePoolAccountID rewrites an account identifier shared by sibling
// credentials so every account stays individually identifiable. The sanitized
// auth ID is already unique per registered credential; a short stable hash of
// the full auth ID covers the rare cases where even that repeats — different
// storage paths ending in the same file name, for example — or where another
// credential's preferred identifier already claims the same string.
func disambiguatePoolAccountID(auth *coreauth.Auth, fallback string, prefCount, uniqCount map[string]int) string {
	want := sanitizePoolAuthID(strings.TrimSpace(auth.ID))
	usable := want != "" && want != "."
	if !usable {
		want = fallback
	}
	if !usable || uniqCount[want] > 1 || (want != fallback && prefCount[want] > 0) {
		sum := sha256.Sum256([]byte(auth.ID))
		want = fmt.Sprintf("%s#%s", want, hex.EncodeToString(sum[:])[:8])
	}
	return want
}

// poolSessionCounter is implemented by selectors that can report active
// session affinity bindings (currently *coreauth.SessionAffinitySelector).
type poolSessionCounter interface {
	ActiveSessionCounts(now time.Time) map[string]int
}

func poolActiveSessionCounts(manager *coreauth.Manager, now time.Time) map[string]int {
	if manager == nil {
		return nil
	}
	counter, ok := manager.Selector().(poolSessionCounter)
	if !ok || counter == nil {
		return nil
	}
	return counter.ActiveSessionCounts(now)
}

// PoolStatus reports per-provider credential pool health for local operators.
// The route is registered with loopback-only middleware; keep this response
// free of credentials, tokens, and raw email addresses. The loopback gate is
// not an access-control boundary: any local process, tunnel, or reverse proxy
// on the host reaches it, so the path must not be exposed through a proxy.
func (h *BaseAPIHandler) PoolStatus(c *gin.Context) {
	if h == nil || h.AuthManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "auth manager unavailable"})
		return
	}
	now := time.Now()
	sessionCounts := poolActiveSessionCounts(h.AuthManager, now)
	auths := h.AuthManager.List()
	byProvider := make(map[string][]*coreauth.Auth)
	for _, a := range auths {
		if a == nil {
			continue
		}
		provider := strings.ToLower(strings.TrimSpace(a.Provider))
		if provider == "" {
			provider = "unknown"
		}
		byProvider[provider] = append(byProvider[provider], a)
	}
	providers := make(map[string]*poolStatusProvider, len(byProvider))
	for provider, pAuths := range byProvider {
		entry := &poolStatusProvider{}
		providers[provider] = entry
		prefCount := make(map[string]int, len(pAuths))
		uniqCount := make(map[string]int, len(pAuths))
		for _, a := range pAuths {
			prefCount[poolAccountID(a)]++
			uniqCount[sanitizePoolAuthID(strings.TrimSpace(a.ID))]++
		}
		for _, a := range pAuths {
			account := buildPoolStatusAccount(a, now, sessionCounts)
			if prefCount[account.AuthID] > 1 {
				account.AuthID = disambiguatePoolAccountID(a, account.AuthID, prefCount, uniqCount)
			}
			entry.Accounts = append(entry.Accounts, account)
			entry.Total++
			entry.ActiveSessionsTotal += account.ActiveSessions
			if account.Priority >= 0 {
				entry.PrimaryTotal++
				switch account.Status {
				case "open":
					entry.PrimaryOpen++
				case "cooling":
					entry.PrimaryCooling++
				}
			} else if account.Status == "open" {
				entry.LastResortOpen++
			}
			if account.Status == "cooling" {
				entry.coolingCount++
			}
			if account.Status == "unavailable" {
				entry.unavailableCount++
			}
			if len(account.ModelsCooling) > 0 || len(account.ModelsBlocked) > 0 {
				entry.hasModelRestriction = true
			}
			if account.ResetsAt != nil && (entry.EarliestRecovery == nil || *account.ResetsAt < *entry.EarliestRecovery) {
				entry.EarliestRecovery = account.ResetsAt
			}
			for _, resetStr := range account.ModelsCooling {
				if resetStr == "" {
					continue
				}
				if entry.EarliestRecovery == nil || resetStr < *entry.EarliestRecovery {
					reset := resetStr
					entry.EarliestRecovery = &reset
				}
			}
		}
	}
	for _, entry := range providers {
		if entry.Total > 0 {
			entry.CoolingRatio = float64(entry.coolingCount) / float64(entry.Total)
		}
		if entry.PrimaryTotal > 0 {
			entry.PrimaryCoolingRatio = float64(entry.PrimaryCooling) / float64(entry.PrimaryTotal)
		}
		open := entry.PrimaryOpen + entry.LastResortOpen
		switch {
		case open == 0:
			entry.Status = "unavailable"
		case entry.coolingCount > 0 || entry.unavailableCount > 0 || entry.hasModelRestriction:
			entry.Status = "degraded"
		default:
			entry.Status = "healthy"
		}
		sort.Slice(entry.Accounts, func(i, j int) bool {
			return entry.Accounts[i].AuthID < entry.Accounts[j].AuthID
		})
	}
	c.JSON(http.StatusOK, poolStatusResponse{
		Timestamp: now.UTC().Format(time.RFC3339),
		Version:   buildinfo.Version,
		Providers: providers,
	})
}

// buildPoolStatusAccount projects one auth snapshot into the response schema.
func buildPoolStatusAccount(auth *coreauth.Auth, now time.Time, sessionCounts map[string]int) *poolStatusAccount {
	account := &poolStatusAccount{
		AuthID:         poolAccountID(auth),
		Priority:       coreauth.Priority(auth),
		Status:         "open",
		ActiveSessions: sessionCounts[auth.ID],
		ModelsCooling:  map[string]string{},
	}
	if auth.Disabled || auth.Status == coreauth.StatusDisabled {
		account.Status = "disabled"
		reason := "disabled"
		account.Reason = &reason
		return account
	}
	if reason, blocked := poolUntimedBlockReason(auth, now); blocked {
		account.Status = "unavailable"
		account.Reason = &reason
		return account
	}
	if blockedModels := coreauth.ModelBlocksWithoutRetry(auth, now); len(blockedModels) > 0 {
		account.ModelsBlocked = blockedModels
		if coreauth.AllModelsBlockedWithoutRetry(auth, now) {
			account.Status = "unavailable"
			reason := "unavailable"
			account.Reason = &reason
			return account
		}
	}
	views := coreauth.CooldownSnapshotForAuth(auth, now)
	if len(views) == 0 {
		return account
	}
	var credential *coreauth.CooldownView
	earliest := -1
	for i := range views {
		view := &views[i]
		if view.Scope == "credential" {
			credential = view
		}
		if view.Scope == "model" && view.ModelKey != "" {
			account.ModelsCooling[view.ModelKey] = view.RetryAt.UTC().Format(time.RFC3339)
		}
		if earliest < 0 || view.RetryAt.Before(views[earliest].RetryAt) {
			earliest = i
		}
	}
	if credential != nil || coreauth.AllModelsBlocked(auth, now) {
		account.Status = "cooling"
		primary := credential
		if primary == nil && earliest >= 0 {
			primary = &views[earliest]
		}
		if primary != nil {
			reason := poolCooldownReason(primary.Reason)
			account.Reason = &reason
			resetsAt := primary.RetryAt.UTC().Format(time.RFC3339)
			account.ResetsAt = &resetsAt
			account.RemainingSeconds = primary.RemainingSeconds
		}
	}
	return account
}

// poolUntimedBlockReason reports a public reason when the credential is
// blocked by a restriction that carries no retry timer (terminal unauthorized
// failure, expired access token, or unavailability without a recovery
// deadline). An empty cooldown snapshot does not imply usability, so these
// gates are checked separately from CooldownSnapshotForAuth.
func poolUntimedBlockReason(auth *coreauth.Auth, now time.Time) (string, bool) {
	if coreauth.HasUnauthorizedAuthFailure(auth) {
		return "unauthorized", true
	}
	if exp, ok := auth.AccessTokenExpirationTime(); ok && !exp.IsZero() && !exp.After(now) {
		return "expired_token", true
	}
	if coreauth.AuthBlockedWithoutRetry(auth, now) {
		return "unavailable", true
	}
	return "", false
}

// poolCooldownReason normalizes internal cooldown reason codes for the public schema.
func poolCooldownReason(reason string) string {
	switch strings.TrimSpace(reason) {
	case "credential_quota":
		return "quota"
	case "cloudflare_challenge":
		return "cloudflare"
	default:
		return strings.TrimSpace(reason)
	}
}
