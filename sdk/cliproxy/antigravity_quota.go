package cliproxy

import (
	"cmp"
	"context"
	"errors"
	"math"
	"slices"
	"strings"
	"time"

	sdkauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

const (
	antigravityQuotaPercentMax     = 100
	antigravityQuotaPercentScale   = 100
	antigravityQuotaPositiveMax    = 99.99
	antigravityQuotaRefreshSpacing = 2 * time.Second
	antigravityFiveHourWindow      = 5 * time.Hour
	antigravityWeeklyWindow        = 7 * 24 * time.Hour
	antigravityUnstartedTolerance  = time.Minute
	antigravitySummaryWindowPrefix = "ag_summary:"
)

func (h antigravityModelCapabilityHints) quotaWindows() []coreauth.QuotaWindow {
	if len(h.quotaBuckets) > 0 {
		return antigravitySummaryWindows(h.quotaBuckets, h.summaryRequestedAt)
	}
	windows := AntigravityQuotaWindows(h.ModelQuotas)
	weekly := AntigravityQuotaWindows(h.weeklyModelQuotas)
	for i := range weekly {
		weekly[i].Key = "weekly:" + weekly[i].Key
		weekly[i].Label += " · Weekly"
		weekly[i].Duration = antigravityWeeklyWindow
	}
	return append(windows, weekly...)
}

func antigravitySummaryWindows(buckets []sdkauth.AntigravityQuotaBucket, requestedAt time.Time) []coreauth.QuotaWindow {
	windows := make([]coreauth.QuotaWindow, 0, len(buckets))
	for _, bucket := range buckets {
		remaining := bucket.RemainingFraction
		used := min(antigravityQuotaPercentMax, max(0, (1-remaining)*antigravityQuotaPercentMax))
		window := coreauth.QuotaWindow{
			Key: antigravitySummaryWindowPrefix + bucket.ID, UsedPercent: &used, ResetsAt: bucket.ResetTime.UTC(),
			Status: coreauth.QuotaWindowStatusAllowed,
		}
		if remaining <= 0 {
			window.Status = coreauth.QuotaWindowStatusRejected
		} else {
			// Routing also treats 100% usage as exhausted. Keep positive quota below it.
			used = min(used, math.Nextafter(antigravityQuotaPercentMax, 0))
		}
		switch bucket.Window {
		case sdkauth.AntigravityQuotaWindowFiveHour:
			window.Label, window.Duration, window.Cadence = "5-hour", antigravityFiveHourWindow, coreauth.ResetCadenceOnUse
			window.Scoped = bucket.ID != "gemini-5h"
			if remaining == 1 && bucket.Description == "" &&
				window.ResetsAt.Sub(requestedAt.Add(window.Duration)).Abs() <= antigravityUnstartedTolerance {
				window.ResetsAt = time.Time{}
			}
		case sdkauth.AntigravityQuotaWindowWeekly:
			window.Label, window.Duration, window.Cadence = "Weekly", antigravityWeeklyWindow, coreauth.ResetCadenceFixed
			window.Scoped = bucket.ID != "gemini-weekly"
		default:
			continue
		}
		if window.Scoped {
			window.Label = antigravitySummaryFamily(bucket) + " · " + window.Label
		}
		windows = append(windows, window)
	}
	return windows
}

func antigravitySummaryFamily(bucket sdkauth.AntigravityQuotaBucket) string {
	switch {
	case strings.HasPrefix(bucket.ID, "gemini-argon-"):
		return cmp.Or(bucket.GroupDisplayName, "Gemini Argon")
	case strings.HasPrefix(bucket.ID, "gemini-carbon-"):
		return cmp.Or(bucket.GroupDisplayName, "Gemini Carbon")
	case strings.HasPrefix(bucket.ID, "gemini-"):
		return "Gemini"
	case strings.HasPrefix(bucket.ID, "3p-"):
		return "Claude & GPT"
	default:
		return cmp.Or(bucket.GroupDisplayName, bucket.ID)
	}
}

// AntigravityQuotaWindows converts model quotas to scoped windows.
func AntigravityQuotaWindows(quotas map[string]sdkauth.AntigravityModelQuota) []coreauth.QuotaWindow {
	if len(quotas) == 0 {
		return nil
	}
	windows := make([]coreauth.QuotaWindow, 0, len(quotas))
	for id, quota := range quotas {
		label := strings.TrimSpace(quota.DisplayName)
		if label == "" {
			label = id
		}
		used := (1 - quota.RemainingFraction) * antigravityQuotaPercentMax
		used = math.Round(used*antigravityQuotaPercentScale) / antigravityQuotaPercentScale
		used = max(0, min(used, antigravityQuotaPercentMax))
		status := coreauth.QuotaWindowStatusAllowed
		if quota.RemainingFraction <= 0 {
			status = coreauth.QuotaWindowStatusRejected
		} else {
			used = min(used, antigravityQuotaPositiveMax)
		}
		var resetsAt time.Time
		if !quota.ResetTime.IsZero() {
			resetsAt = quota.ResetTime.UTC()
		}
		windows = append(windows, coreauth.QuotaWindow{
			Key: "ag:" + id, Label: label, UsedPercent: &used, ResetsAt: resetsAt, Status: status, Scoped: true,
		})
	}
	slices.SortFunc(windows, func(a, b coreauth.QuotaWindow) int {
		if order := cmp.Compare(*b.UsedPercent, *a.UsedPercent); order != 0 {
			return order
		}
		if order := a.ResetsAt.Compare(b.ResetsAt); order != 0 {
			return order
		}
		return cmp.Compare(a.Key, b.Key)
	})
	return windows
}

// AntigravityUsageLookup fetches per-account Antigravity quotas on demand.
type AntigravityUsageLookup struct {
	service *Service
}

// NewAntigravityUsageLookup returns a usage lookup with model catalog fallback.
func (s *Service) NewAntigravityUsageLookup() *AntigravityUsageLookup {
	return &AntigravityUsageLookup{service: s}
}

// Eligible reports whether auth is an enabled Antigravity OAuth credential with an access token.
func (l *AntigravityUsageLookup) Eligible(auth *coreauth.Auth) bool {
	if l == nil || l.service == nil || l.service.antigravityHomeEnabled() || auth == nil || isAntigravityAuthDisabled(auth) || !strings.EqualFold(strings.TrimSpace(auth.Provider), "antigravity") ||
		auth.AuthKind() != coreauth.AuthKindOAuth || strings.TrimSpace(auth.Attributes[coreauth.AttributeAPIKey]) != "" {
		return false
	}
	accessToken, _ := auth.Metadata["access_token"].(string)
	return strings.TrimSpace(accessToken) != ""
}

// Refresh fetches usage for one credential, bypassing the catalog TTL.
func (l *AntigravityUsageLookup) Refresh(ctx context.Context, auth *coreauth.Auth) error {
	if !l.Eligible(auth) {
		return errors.New("usage lookup requires an enabled Antigravity OAuth credential with an access token")
	}
	if l.service.coreManager == nil {
		return errors.New("antigravity usage lookup requires a credential manager")
	}
	key, hints, err := l.service.fetchAntigravityModelHints(ctx, auth, true)
	if err != nil {
		return err
	}
	windows := hints.quotaWindows()
	if len(windows) == 0 {
		return errors.New("antigravity model probe returned no quota information")
	}
	return l.service.recordAntigravityQuota(ctx, auth, key, windows, hints.lookedUpAt, hints.revision, false)
}

// RefreshAll fetches usage for every eligible credential and returns failures by ID.
func (l *AntigravityUsageLookup) RefreshAll(ctx context.Context) map[string]error {
	failures := map[string]error{}
	if l == nil || l.service == nil || l.service.coreManager == nil {
		return failures
	}
	first := true
	for _, auth := range l.service.coreManager.List() {
		if ctx.Err() != nil {
			break
		}
		if !l.Eligible(auth) {
			continue
		}
		if !first {
			timer := time.NewTimer(antigravityQuotaRefreshSpacing)
			select {
			case <-ctx.Done():
				timer.Stop()
				return failures
			case <-timer.C:
			}
		}
		first = false
		if err := l.Refresh(ctx, auth); err != nil {
			failures[auth.ID] = err
		}
	}
	return failures
}

func (s *Service) recordAntigravityQuota(ctx context.Context, auth *coreauth.Auth, key string, windows []coreauth.QuotaWindow, lookedUpAt time.Time, revision uint64, catalogOnly bool) error {
	_, err := s.coreManager.ObserveQuotaLookupWindows(ctx, auth.ID, windows, lookedUpAt, func(current *coreauth.Auth) (bool, error) {
		if !antigravityAuthModelSettingsEqual(current, auth) || s.antigravityCapabilityKey(current) != key {
			return false, coreauth.ErrQuotaLookupCredentialChanged
		}
		currentSummary := hasAntigravitySummaryWindows(current.Quota.LookupWindows)
		if catalogOnly && currentSummary {
			return false, nil
		}
		summarySupersedesCatalog := !currentSummary && hasAntigravitySummaryWindows(windows)

		s.antigravityQuotaPublishMu.Lock()
		defer s.antigravityQuotaPublishMu.Unlock()
		storedAt := current.Quota.LookedUpAt
		newer := lookedUpAt.After(storedAt) ||
			(lookedUpAt.Equal(storedAt) && revision > s.antigravityQuotaPublished[auth.ID])
		if !summarySupersedesCatalog && !newer {
			return false, nil
		}
		if s.antigravityQuotaPublished == nil {
			s.antigravityQuotaPublished = make(map[string]uint64)
		}
		s.antigravityQuotaPublished[auth.ID] = revision
		return true, nil
	})
	return err
}

func hasAntigravitySummaryWindows(windows []coreauth.QuotaWindow) bool {
	return slices.ContainsFunc(windows, func(window coreauth.QuotaWindow) bool {
		return strings.HasPrefix(window.Key, antigravitySummaryWindowPrefix)
	})
}
