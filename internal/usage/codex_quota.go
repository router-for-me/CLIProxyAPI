package usage

import (
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// CodexQuotaSnapshot captures Codex team quota information emitted via response headers.
// This is a best-effort in-memory snapshot for observability (it is not persisted).
type CodexQuotaSnapshot struct {
	PlanType string `json:"plan_type,omitempty"`

	PrimaryUsedPercent          *float64 `json:"primary_used_percent,omitempty"`
	PrimaryResetAfterSeconds    *int     `json:"primary_reset_after_seconds,omitempty"`
	PrimaryWindowMinutes        *int     `json:"primary_window_minutes,omitempty"`
	SecondaryUsedPercent        *float64 `json:"secondary_used_percent,omitempty"`
	SecondaryResetAfterSeconds  *int     `json:"secondary_reset_after_seconds,omitempty"`
	SecondaryWindowMinutes      *int     `json:"secondary_window_minutes,omitempty"`
	PrimaryOverSecondaryPercent *float64 `json:"primary_over_secondary_percent,omitempty"`

	PrimaryResetAtSeconds   *int64  `json:"primary_reset_at_seconds,omitempty"`
	SecondaryResetAtSeconds *int64  `json:"secondary_reset_at_seconds,omitempty"`
	CreditsHasCredits       *bool   `json:"credits_has_credits,omitempty"`
	CreditsBalance          *string `json:"credits_balance,omitempty"`
	CreditsUnlimited        *bool   `json:"credits_unlimited,omitempty"`

	UpdatedAt                  time.Time `json:"updated_at"`
}

var codexQuotaByAuth sync.Map // authID -> CodexQuotaSnapshot

// ParseCodexQuotaSnapshot parses Codex quota headers (x-codex-*) into a snapshot.
// Returns nil when no relevant headers are present.
func ParseCodexQuotaSnapshot(headers http.Header) *CodexQuotaSnapshot {
	if headers == nil {
		return nil
	}
	snapshot := &CodexQuotaSnapshot{}
	hasData := false

	parseFloat := func(key string) *float64 {
		if v := headers.Get(key); v != "" {
			if f, err := strconv.ParseFloat(v, 64); err == nil {
				return &f
			}
		}
		return nil
	}
	parseInt := func(key string) *int {
		if v := headers.Get(key); v != "" {
			if i, err := strconv.Atoi(v); err == nil {
				return &i
			}
		}
		return nil
	}
	parseInt64 := func(key string) *int64 {
		if v := headers.Get(key); v != "" {
			if i, err := strconv.ParseInt(v, 10, 64); err == nil {
				return &i
			}
		}
		return nil
	}
	parseBool := func(key string) *bool {
		if v := strings.TrimSpace(headers.Get(key)); v != "" {
			switch strings.ToLower(v) {
			case "true", "1", "yes":
				b := true
				return &b
			case "false", "0", "no":
				b := false
				return &b
			}
		}
		return nil
	}
	parseString := func(key string) *string {
		if v := headers.Get(key); v != "" {
			out := v
			return &out
		}
		return nil
	}

	if v := strings.TrimSpace(headers.Get("x-codex-plan-type")); v != "" {
		snapshot.PlanType = v
		hasData = true
	}

	if v := parseFloat("x-codex-primary-used-percent"); v != nil {
		snapshot.PrimaryUsedPercent = v
		hasData = true
	}
	if v := parseInt("x-codex-primary-reset-after-seconds"); v != nil {
		snapshot.PrimaryResetAfterSeconds = v
		hasData = true
	}
	if v := parseInt("x-codex-primary-window-minutes"); v != nil {
		snapshot.PrimaryWindowMinutes = v
		hasData = true
	}
	if v := parseFloat("x-codex-secondary-used-percent"); v != nil {
		snapshot.SecondaryUsedPercent = v
		hasData = true
	}
	if v := parseInt("x-codex-secondary-reset-after-seconds"); v != nil {
		snapshot.SecondaryResetAfterSeconds = v
		hasData = true
	}
	if v := parseInt("x-codex-secondary-window-minutes"); v != nil {
		snapshot.SecondaryWindowMinutes = v
		hasData = true
	}
	if v := parseFloat("x-codex-primary-over-secondary-limit-percent"); v != nil {
		snapshot.PrimaryOverSecondaryPercent = v
		hasData = true
	}

	if v := parseInt64("x-codex-primary-reset-at"); v != nil {
		snapshot.PrimaryResetAtSeconds = v
		hasData = true
	}
	if v := parseInt64("x-codex-secondary-reset-at"); v != nil {
		snapshot.SecondaryResetAtSeconds = v
		hasData = true
	}
	if v := parseBool("x-codex-credits-has-credits"); v != nil {
		snapshot.CreditsHasCredits = v
		hasData = true
	}
	if v := parseString("x-codex-credits-balance"); v != nil {
		snapshot.CreditsBalance = v
		hasData = true
	}
	if v := parseBool("x-codex-credits-unlimited"); v != nil {
		snapshot.CreditsUnlimited = v
		hasData = true
	}

	if !hasData {
		return nil
	}
	snapshot.UpdatedAt = time.Now()
	return snapshot
}

// UpdateCodexQuotaSnapshot stores the latest snapshot for an authID (in-memory).
func UpdateCodexQuotaSnapshot(authID string, snapshot *CodexQuotaSnapshot) {
	if authID == "" || snapshot == nil {
		return
	}
	codexQuotaByAuth.Store(authID, *snapshot)
}

// GetCodexQuotaSnapshot returns the most recent snapshot for an authID, if any.
func GetCodexQuotaSnapshot(authID string) *CodexQuotaSnapshot {
	if authID == "" {
		return nil
	}
	if v, ok := codexQuotaByAuth.Load(authID); ok {
		if snap, ok2 := v.(CodexQuotaSnapshot); ok2 {
			out := snap
			return &out
		}
	}
	return nil
}
