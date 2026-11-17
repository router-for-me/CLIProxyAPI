package auth

import (
	"strings"
	"time"
)

// ResetTriggerState describes whether a credential's short (e.g. five-hour)
// quota window is idle and should be started with a cheap request.
type ResetTriggerState struct {
	// Due reports whether a trigger request should be sent.
	Due bool
	// Window is the short window name ("5h", "primary"), empty when the
	// credential has no quota snapshot yet.
	Window string
	// IdleSince is the reset time of the last observed short window. It is
	// zero when no snapshot exists yet; together with Window it identifies
	// the idle period a trigger belongs to.
	IdleSince time.Time
}

// ResetTriggerStateForAuth decides whether the short quota window of a
// Claude or Codex OAuth credential has lapsed without a new window being
// started. Credentials that are disabled, blocked for model, using API keys,
// or whose long (weekly-style) window is exhausted are never due. A
// credential without any quota snapshot is due so its window starts and its
// reset times become known.
func ResetTriggerStateForAuth(auth *Auth, model string, now time.Time) ResetTriggerState {
	if auth == nil || auth.AuthKind() != AuthKindOAuth {
		return ResetTriggerState{}
	}
	switch strings.ToLower(strings.TrimSpace(auth.Provider)) {
	case "claude", "codex":
	default:
		return ResetTriggerState{}
	}
	if blocked, _, _ := isAuthBlockedForModel(auth, model, now); blocked {
		return ResetTriggerState{}
	}
	windows := QuotaWindowsForAuth(auth)
	if len(windows) == 0 {
		return ResetTriggerState{Due: true}
	}
	var short *QuotaWindow
	for i := range windows {
		window := &windows[i]
		if window.Duration >= longQuotaWindowMinDuration {
			if longWindowExhausted(*window, now) {
				return ResetTriggerState{}
			}
			continue
		}
		// Windows are sorted shortest first; keep the first short one.
		if short == nil && window.Duration > 0 {
			short = window
		}
	}
	if short == nil || short.ResetAt.IsZero() || short.ResetAt.After(now) {
		return ResetTriggerState{}
	}
	return ResetTriggerState{Due: true, Window: short.Name, IdleSince: short.ResetAt}
}

func longWindowExhausted(window QuotaWindow, now time.Time) bool {
	if window.ResetAt.IsZero() || !window.ResetAt.After(now) {
		return false
	}
	if strings.EqualFold(window.Status, "rejected") {
		return true
	}
	return window.UsedPercent != nil && *window.UsedPercent >= 100
}
