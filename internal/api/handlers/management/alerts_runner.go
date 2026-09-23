package management

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/policy"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// alertSweepRunning guards the alert runner against overlapping runs so a long
// sweep (many keys/users) plus a re-tick does not double up.
var alertSweepRunning sync.Mutex

// alertSubstitutionLookback bounds how far back the substitution detector
// scans. Bounded to 30 minutes: substitutions are actionable while the
// affected traffic is recent, and a wider window would re-alert on a
// condition the operator already acknowledged.
const alertSubstitutionLookback = 30 * time.Minute

// shouldWarnDrops reports whether the cumulative usage-flusher drop counter
// increased between two alert sweeps. Equal/steady counts must NOT warn
// (anti-spam: a quiescent drain stays silent), and a decrease (counter reset or
// overflow) must not warn either. Only a strictly-positive delta is worth
// surfacing.
func shouldWarnDrops(prev, cur int64) bool {
	return cur > prev
}

// alertFingerprintKey joins components into a stable dedup key. Component
// order matters — changing it would re-key existing alerts (harmless, but
// avoid unless needed).
func alertFingerprintKey(parts ...string) string {
	out := parts[0]
	for _, p := range parts[1:] {
		out += ":" + p
	}
	return out
}

// runAlertChecks runs one detection pass across every enabled detector,
// recording each fired condition to the alerts feed. Re-reads the operator
// settings at the start so toggles/thresholds are applied live. Safe to call
// concurrently; an in-flight pass blocks a second via alertSweepRunning.
func (h *Handler) runAlertChecks(ctx context.Context) {
	if !alertSweepRunning.TryLock() {
		log.Debug("alerts: sweep already in progress; skipping")
		return
	}
	defer alertSweepRunning.Unlock()
	if h == nil {
		return
	}

	h.mu.Lock()
	alerts := h.pgAlerts
	users := h.pgUsers
	apiKeys := h.pgAPIKeys
	usage := h.pgUsage
	authManager := h.authManager
	flusher := h.pgFlusher
	h.mu.Unlock()

	// Backpressure signal: if the PG usage flusher's flush queue filled up and
	// shed records since the last sweep, log a warning. The flush-side already
	// logs each per-record drop; this sweep turns the cumulative Drops() counter
	// into a periodic visibility/anomaly flag. Alarm on the *delta* (not the
	// cumulative total) so a steady-state drain does not re-warn every sweep.
	if flusher != nil {
		drops := flusher.Drops()
		h.mu.Lock()
		prev := h.pgFlusherLastDrops
		h.pgFlusherLastDrops = drops
		h.mu.Unlock()
		if shouldWarnDrops(prev, drops) {
			log.WithField("drops_cumulative", drops).
				WithField("drops_delta", drops-prev).
				Warn("postgres usage flusher: usage records dropped since last sweep (queue backpressure)")
		}
	}

	if alerts == nil {
		return
	}
	settings, err := alerts.GetAlertSettings(ctx)
	if err != nil {
		log.WithError(err).Warn("alerts: load settings failed; skipping sweep")
		return
	}
	if !settings.Enabled {
		log.Debug("alerts: sweep disabled by settings; skipping")
		return
	}
	suppression := settings.Suppression()

	if settings.CategoryEnabled(store.AlertTypeUserBudget) && users != nil {
		h.alertUserBudget(ctx, alerts, users, suppression)
	}
	if settings.CategoryEnabled(store.AlertTypeAPIKeyBudget) && apiKeys != nil && usage != nil {
		h.alertAPIKeyBudget(ctx, alerts, apiKeys, usage, suppression)
	}
	if settings.CategoryEnabled(store.AlertTypeErrorRate) && usage != nil {
		h.alertErrorRate(ctx, alerts, usage, settings, suppression)
	}
	if settings.CategoryEnabled(store.AlertTypeProviderCooldown) && authManager != nil {
		h.alertProviderCooldown(ctx, alerts, authManager, suppression)
	}
	if settings.CategoryEnabled(store.AlertTypeModelSubstitution) && usage != nil {
		h.alertModelSubstitution(ctx, alerts, usage, suppression)
	}
}

// alertUserBudget flags every internal user whose running spend has reached or
// exceeded their max_budget. Mirrors the enforcement predicate in
// policy.Check (spend >= max_budget, non-admin). The alert fires once and then
// bumps occurrences within the suppression window rather than spamming.
func (h *Handler) alertUserBudget(ctx context.Context, alerts *store.AlertStore, users *store.UserStore, suppression time.Duration) {
	exceeded, err := users.ListBudgetExceeded(ctx)
	if err != nil {
		log.WithError(err).Warn("alerts: user budget scan failed")
		return
	}
	for _, u := range exceeded {
		displayName := u.UserAlias
		if displayName == "" {
			displayName = u.UserEmail
		}
		if displayName == "" {
			displayName = u.ID
		}
		limit := 0.0
		if u.MaxBudget != nil {
			limit = *u.MaxBudget
		}
		_, _, err := alerts.RecordAlert(ctx, store.Alert{
			AlertType:  store.AlertTypeUserBudget,
			Severity:   store.AlertSeverityCritical,
			Title:      "Internal user budget exceeded",
			Message:    fmt.Sprintf("%s has spent $%.2f of a %.2f USD budget.", displayName, u.Spend, limit),
			EntityID:   u.ID,
			EntityName: displayName,
			Value:      u.Spend,
			LimitValue: limit,
			Data: map[string]any{
				"user_role":  u.UserRole,
				"user_email": u.UserEmail,
			},
			Fingerprint: alertFingerprintKey(store.AlertTypeUserBudget, u.ID),
		}, suppression)
		if err != nil {
			log.WithError(err).WithField("user_id", u.ID).Debug("alerts: record user budget alert failed")
		}
	}
}

// alertAPIKeyBudget flags every active API key with a budget cap whose current
// window spend has reached or exceeded a cap. Window spend is read from
// usage_windows via the same windowStart the enforcement path uses
// (policy.WindowFor), so the alert matches the exact window a 402 denial would
// have checked. One alert is recorded per (key, window type) that is exceeded.
func (h *Handler) alertAPIKeyBudget(ctx context.Context, alerts *store.AlertStore, apiKeys *store.APIKeyStore, usage *store.UsageStore, suppression time.Duration) {
	keys, err := apiKeys.ListBudgetCaps(ctx)
	if err != nil {
		log.WithError(err).Warn("alerts: api key budget scan failed")
		return
	}
	now := time.Now()
	for _, k := range keys {
		displayName := k.Name
		if k.KeyAlias != "" {
			displayName = k.KeyAlias
		}
		type windowCap struct {
			windowType string
			label      string
			capValue   *float64
		}
		caps := []windowCap{
			{store.WindowTypeHourly, "hourly", k.BudgetHourlyUSD},
			{store.WindowTypeWeekly, "weekly", k.BudgetWeeklyUSD},
			{store.WindowTypeMonthly, "monthly", k.BudgetMonthlyUSD},
		}
		for _, c := range caps {
			if c.capValue == nil || *c.capValue <= 0 {
				continue
			}
			start, _ := policy.WindowFor(c.windowType, k.CreatedAt, now)
			spent, errGet := usage.GetWindow(ctx, k.APIKeyID, c.windowType, start)
			if errGet != nil {
				// No window row yet → zero spend, not exceeded.
				continue
			}
			if spent.CostUSD < *c.capValue {
				continue
			}
			_, _, err := alerts.RecordAlert(ctx, store.Alert{
				AlertType:  store.AlertTypeAPIKeyBudget,
				Severity:   store.AlertSeverityCritical,
				Title:      "API key budget exceeded",
				Message:    fmt.Sprintf("API key %s has spent $%.2f of a %.2f USD %s budget.", displayName, spent.CostUSD, *c.capValue, c.label),
				EntityID:   k.APIKeyID,
				EntityName: displayName,
				Value:      spent.CostUSD,
				LimitValue: *c.capValue,
				Data: map[string]any{
					"window_type": c.windowType,
					"user_id":     k.UserID,
					"user_alias":  k.UserAlias,
				},
				Fingerprint: alertFingerprintKey(store.AlertTypeAPIKeyBudget, k.APIKeyID, c.windowType),
			}, suppression)
			if err != nil {
				log.WithError(err).WithField("api_key_id", k.APIKeyID).Debug("alerts: record api key budget alert failed")
			}
		}
	}
}

// alertErrorRate fires a single error_rate alert when the failed-attempt rate
// over the configured window exceeds the configured threshold. The rate is the
// error-row count in the window divided by the window length in minutes. Using
// SelectErrorCount keeps the check a cheap COUNT(*) (index-backed on
// requested_at).
func (h *Handler) alertErrorRate(ctx context.Context, alerts *store.AlertStore, usage *store.UsageStore, settings store.AlertSettings, suppression time.Duration) {
	minutes := settings.ErrorWindowMinutes
	if minutes <= 0 {
		minutes = 5
	}
	window := time.Duration(minutes) * time.Minute
	from := time.Now().Add(-window)
	count, err := usage.SelectErrorCount(ctx, store.UsageFilter{From: from})
	if err != nil {
		log.WithError(err).Warn("alerts: error count failed")
		return
	}
	rate := float64(count) / float64(minutes)
	threshold := settings.ErrorRateThreshold
	if rate < threshold {
		return
	}
	_, _, err = alerts.RecordAlert(ctx, store.Alert{
		AlertType:  store.AlertTypeErrorRate,
		Severity:   store.AlertSeverityWarning,
		Title:      "Elevated error rate",
		Message:    fmt.Sprintf("%d failed attempts in the last %d minute(s) — %.2f/min exceeds the %.2f/min threshold.", count, minutes, rate, threshold),
		Value:      rate,
		LimitValue: threshold,
		Data: map[string]any{
			"error_count":    count,
			"window_minutes": minutes,
		},
		Fingerprint: alertFingerprintKey(store.AlertTypeErrorRate, "overall"),
	}, suppression)
	if err != nil {
		log.WithError(err).Debug("alerts: record error rate alert failed")
	}
}

// alertProviderCooldown records one alert per auth/model pair currently in
// cooldown (quota exhaustion, backoff, unauthorized, etc.). Auth-level
// cooldowns (empty Model) get their own row keyed by auth id.
func (h *Handler) alertProviderCooldown(ctx context.Context, alerts *store.AlertStore, authManager *coreauth.Manager, suppression time.Duration) {
	records := authManager.CooldownStateSnapshot()
	for _, r := range records {
		level := "model"
		entityID := r.AuthID
		displayName := r.AuthID
		if r.Provider != "" {
			displayName = r.Provider + " (" + r.AuthID + ")"
		}
		if r.Model == "" {
			level = "auth"
		}
		_, _, err := alerts.RecordAlert(ctx, store.Alert{
			AlertType:  store.AlertTypeProviderCooldown,
			Severity:   store.AlertSeverityWarning,
			Title:      "Provider in cooldown",
			Message:    fmt.Sprintf("%s is cooling down until %s (%s).", displayName, r.NextRetryAfter.UTC().Format(time.RFC3339), cooldownReasonOrFallback(r)),
			EntityID:   entityID,
			EntityName: displayName,
			Model:      r.Model,
			Provider:   r.Provider,
			Data: map[string]any{
				"next_retry_after": r.NextRetryAfter,
				"reason":           r.Reason,
				"level":            level,
				"status":           r.Status,
			},
			Fingerprint: alertFingerprintKey(store.AlertTypeProviderCooldown, r.AuthID, r.Model),
		}, suppression)
		if err != nil {
			log.WithError(err).WithField("auth_id", r.AuthID).Debug("alerts: record provider cooldown alert failed")
		}
	}
}

func cooldownReasonOrFallback(r coreauth.CooldownStateRecord) string {
	if r.Reason != "" {
		return r.Reason
	}
	if r.LastError != nil && r.LastError.Message != "" {
		return r.LastError.Message
	}
	return r.Status
}

// StartAlertSweep launches a background goroutine that periodically runs one
// detection pass across every enabled detector. The cadence is re-read from
// settings on each tick so operator changes take effect without a restart.
// No-op (returns immediately) when the alert store is nil so callers can
// unconditionally invoke it. Mirrors StartModelHealthSweep.
func (h *Handler) StartAlertSweep() {
	if h == nil {
		return
	}
	h.mu.Lock()
	s := h.pgAlerts
	h.mu.Unlock()
	if s == nil {
		return
	}
	go func() {
		// Run an initial pass shortly after startup so the feed is not empty
		// until the first interval elapses.
		initialDelay := 5 * time.Second
		timer := time.NewTimer(initialDelay)
		defer timer.Stop()
		for {
			select {
			case <-timer.C:
				h.runAlertChecks(context.Background())
			}
			interval := s.Interval(context.Background())
			if interval <= 0 {
				interval = 60 * time.Second
			}
			timer.Reset(interval)
		}
	}()
}

// formatSubstitutionMessage renders the operator-facing sentence for one
// provider/model/served triple.
func formatSubstitutionMessage(requested, served, provider string, count int64) string {
	return fmt.Sprintf(`%s served %q for %d request(s) that asked for %q.`, provider, served, count, requested)
}

// substitutionFingerprint keys the suppression window on the provider and the
// requested→served pair, so a recurring substitution bumps the existing row
// while a different served model raises a new alert.
func substitutionFingerprint(provider, requested, served string) string {
	return alertFingerprintKey(store.AlertTypeModelSubstitution,
		provider, requested+"->"+served)
}

// alertModelSubstitution records one alert per provider/model/served triple
// seen in the lookback window. Silent substitution is always at least a
// warning: the client asked for a specific model and received another.
func (h *Handler) alertModelSubstitution(ctx context.Context, alerts *store.AlertStore, usage *store.UsageStore, suppression time.Duration) {
	since := time.Now().UTC().Add(-alertSubstitutionLookback)
	rows, errList := usage.ListSubstitutions(ctx, since)
	if errList != nil {
		log.WithError(errList).Warn("alerts: list substitutions failed; skipping")
		return
	}
	for _, row := range rows {
		_, _, errRecord := alerts.RecordAlert(ctx, store.Alert{
			AlertType:  store.AlertTypeModelSubstitution,
			Severity:   store.AlertSeverityWarning,
			Title:      "Upstream served a different model",
			Message:    formatSubstitutionMessage(row.Model, row.ServedModel, row.Provider, row.Count),
			EntityID:   row.Provider,
			EntityName: row.Provider,
			Model:      row.Model,
			Provider:   row.Provider,
			Value:      float64(row.Count),
			Data: map[string]any{
				"requested_model":    row.Model,
				"served_model":       row.ServedModel,
				"substitution_count": row.Count,
				"lookback_minutes":   int(alertSubstitutionLookback.Minutes()),
			},
			Fingerprint: substitutionFingerprint(row.Provider, row.Model, row.ServedModel),
		}, suppression)
		if errRecord != nil {
			log.WithError(errRecord).WithField("provider", row.Provider).
				Debug("alerts: record model substitution alert failed")
		}
	}
}
