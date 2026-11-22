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
)

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

// NewAntigravityUsageLookup returns a usage lookup backed by the service's model probe.
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
	windows := AntigravityQuotaWindows(hints.ModelQuotas)
	if len(windows) == 0 {
		return errors.New("antigravity model probe returned no quota information")
	}
	return l.service.recordAntigravityQuota(ctx, auth, key, windows, hints.lookedUpAt)
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

func (s *Service) recordAntigravityQuota(ctx context.Context, auth *coreauth.Auth, key string, windows []coreauth.QuotaWindow, lookedUpAt time.Time) error {
	errCredentialChanged := errors.New("antigravity usage lookup credential is no longer current")
	updated := false
	_, recorded, err := s.coreManager.Modify(coreauth.WithSkipPersist(ctx), auth.ID, auth.RegistrationEpoch, func(current *coreauth.Auth) error {
		if !antigravityAuthModelSettingsEqual(current, auth) || s.antigravityCapabilityKey(current) != key {
			return errCredentialChanged
		}
		if lookedUpAt.After(current.Quota.LookedUpAt) {
			current.Quota.LookupWindows = windows
			current.Quota.LookupSignals = nil
			current.Quota.LookedUpAt = lookedUpAt
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
