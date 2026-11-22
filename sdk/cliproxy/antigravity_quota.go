package cliproxy

import (
	"cmp"
	"context"
	"errors"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	sdkauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

const (
	antigravityQuotaPercentMax     = 100
	antigravityQuotaPercentScale   = 100
	antigravityQuotaFractionScale  = antigravityQuotaPercentMax * antigravityQuotaPercentScale
	antigravityQuotaRefreshSpacing = 2 * time.Second
	antigravityFiveHourWindow      = 5 * time.Hour
	antigravityWeeklyWindow        = 7 * 24 * time.Hour
	antigravityUnstartedTolerance  = time.Minute
	antigravitySummaryWindowPrefix = "ag_summary:"
)

func (h antigravityModelCapabilityHints) quotaWindows() []coreauth.QuotaWindow {
	if len(h.quotaBuckets) > 0 {
		return antigravitySummaryWindows(h.quotaBuckets, h.lookedUpAt)
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
			Status: "allowed",
		}
		if remaining <= 0 {
			window.Status = "rejected"
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

// AntigravityQuotaWindows converts per-model quotas into distinct quota buckets.
func AntigravityQuotaWindows(quotas map[string]sdkauth.AntigravityModelQuota) []coreauth.QuotaWindow {
	if len(quotas) == 0 {
		return nil
	}
	type bucket struct {
		reset          time.Time
		remainingUnits float64
	}
	groups := make(map[bucket][]sdkauth.AntigravityModelQuota)
	// Disjoint groups are ordered by their first sorted model ID.
	var orderedBuckets []bucket
	for _, id := range slices.Sorted(maps.Keys(quotas)) {
		quota := quotas[id]
		if quota.ModelID == "" {
			quota.ModelID = id
		}
		key := bucket{
			reset:          quota.ResetTime.UTC().Truncate(time.Minute),
			remainingUnits: math.Round(quota.RemainingFraction * antigravityQuotaFractionScale),
		}
		if _, exists := groups[key]; !exists {
			orderedBuckets = append(orderedBuckets, key)
		}
		groups[key] = append(groups[key], quota)
	}
	windows := make([]coreauth.QuotaWindow, 0, len(groups))
	// Reserve every base key before assigning numbered keys.
	assignedKeys := make(map[string]bool, len(groups))
	for _, key := range orderedBuckets {
		models := groups[key]
		used := 0.0
		status := "allowed"
		var resetsAt time.Time
		// Members at a rounding boundary can differ by one hundredth of a percent.
		for _, model := range models {
			used = max(used, math.Round((1-model.RemainingFraction)*antigravityQuotaFractionScale)/antigravityQuotaPercentScale)
			if model.RemainingFraction <= 0 {
				status = "rejected"
			}
			if !model.ResetTime.IsZero() && (resetsAt.IsZero() || model.ResetTime.After(resetsAt)) {
				resetsAt = model.ResetTime.UTC()
			}
		}
		used = min(used, antigravityQuotaPercentMax)
		label := antigravityQuotaLabel(models)
		parts := strings.FieldsFunc(strings.ToLower(label), func(r rune) bool {
			return !unicode.IsLetter(r) && !unicode.IsNumber(r)
		})
		slug := strings.Join(parts, "_")
		if slug == "" {
			slug = "quota"
		}
		base := "ag_" + slug
		windows = append(windows, coreauth.QuotaWindow{
			Key: base, Label: label, UsedPercent: &used, ResetsAt: resetsAt, Status: status,
			Scoped: true,
		})
		assignedKeys[base] = false
	}
	for i := range windows {
		base := windows[i].Key
		if !assignedKeys[base] {
			assignedKeys[base] = true
			continue
		}
		for suffix := 2; ; suffix++ {
			key := base + "_" + strconv.Itoa(suffix)
			if _, exists := assignedKeys[key]; !exists {
				windows[i].Key = key
				assignedKeys[key] = true
				break
			}
		}
	}
	compare := func(a, b coreauth.QuotaWindow) int {
		if order := cmp.Compare(*b.UsedPercent, *a.UsedPercent); order != 0 {
			return order
		}
		if order := a.ResetsAt.Compare(b.ResetsAt); order != 0 {
			return order
		}
		if order := cmp.Compare(a.Key, b.Key); order != 0 {
			return order
		}
		return cmp.Compare(a.Label, b.Label)
	}
	slices.SortFunc(windows, compare)
	return windows
}

func antigravityQuotaLabel(models []sdkauth.AntigravityModelQuota) string {
	family, _, _ := strings.Cut(models[0].ModelID, "-")
	shortest := ""
	for _, model := range models {
		if modelFamily, _, _ := strings.Cut(model.ModelID, "-"); modelFamily != family {
			family = ""
		}
		if name := strings.TrimSpace(model.DisplayName); name != "" &&
			(shortest == "" || len(name) < len(shortest) || (len(name) == len(shortest) && name < shortest)) {
			shortest = name
		}
	}
	switch family {
	case "claude":
		return "Claude"
	case "gemini":
		return "Gemini"
	case "gpt":
		return "GPT"
	}
	if shortest != "" {
		return shortest
	}
	return "Quota"
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
func (*AntigravityUsageLookup) Eligible(auth *coreauth.Auth) bool {
	if auth == nil || isAntigravityAuthDisabled(auth) || !strings.EqualFold(strings.TrimSpace(auth.Provider), "antigravity") ||
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
	return l.service.recordAntigravityQuota(ctx, auth, key, windows, hints.lookedUpAt, false)
}

// RefreshAll fetches usage for every eligible credential and returns failures by ID.
func (l *AntigravityUsageLookup) RefreshAll(ctx context.Context) map[string]error {
	failures := map[string]error{}
	if l.service.coreManager == nil {
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

func (s *Service) recordAntigravityQuota(ctx context.Context, auth *coreauth.Auth, key string, windows []coreauth.QuotaWindow, lookedUpAt time.Time, catalogOnly bool) error {
	errCredentialChanged := errors.New("antigravity usage lookup credential is no longer current")
	updated := false
	_, recorded, err := s.coreManager.Modify(coreauth.WithSkipPersist(ctx), auth.ID, auth.RegistrationEpoch, func(current *coreauth.Auth) error {
		if !antigravityAuthModelSettingsEqual(current, auth) || s.antigravityCapabilityKey(current) != key {
			return errCredentialChanged
		}
		currentSummary := hasAntigravitySummaryWindows(current.Quota.LookupWindows)
		if catalogOnly && currentSummary {
			return nil
		}
		summarySupersedesCatalog := !currentSummary && hasAntigravitySummaryWindows(windows)
		newer := lookedUpAt.After(current.Quota.LookedUpAt)
		if newer || summarySupersedesCatalog {
			current.Quota.LookupWindows = windows
			current.Quota.LookupSignals = nil
			if newer {
				current.Quota.LookedUpAt = lookedUpAt
			}
			updated = true
		}
		return nil
	})
	if err != nil {
		return err
	}
	if !recorded {
		return errCredentialChanged
	}
	if updated {
		s.coreManager.NotifyQuotaUpdated()
	}
	return nil
}

func hasAntigravitySummaryWindows(windows []coreauth.QuotaWindow) bool {
	return slices.ContainsFunc(windows, func(window coreauth.QuotaWindow) bool {
		return strings.HasPrefix(window.Key, antigravitySummaryWindowPrefix)
	})
}
