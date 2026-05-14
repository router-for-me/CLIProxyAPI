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

var antigravityCreditsStickByAuth sync.Map // auth ID → time.Time (first successful credits usage)

// MarkAntigravityCreditsStick records the first successful credits-based request
// for the given auth. Subsequent calls are no-ops — the window is never renewed.
func MarkAntigravityCreditsStick(authID string) {
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return
	}
	antigravityCreditsStickByAuth.LoadOrStore(authID, time.Now())
}

// ClearAntigravityCreditsStick removes the sticky credits state for the given auth.
func ClearAntigravityCreditsStick(authID string) {
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return
	}
	antigravityCreditsStickByAuth.Delete(authID)
}

// IsAntigravityCreditsSticky reports whether the given auth is within an active
// sticky credits window.
// stickSeconds == 0: disabled. stickSeconds < 0 (-1): permanently sticky.
func IsAntigravityCreditsSticky(authID string, stickSeconds int) bool {
	if stickSeconds == 0 {
		return false
	}
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return false
	}
	if stickSeconds < 0 {
		return true
	}
	val, ok := antigravityCreditsStickByAuth.Load(authID)
	if !ok {
		return false
	}
	t, ok := val.(time.Time)
	if !ok {
		antigravityCreditsStickByAuth.Delete(authID)
		return false
	}
	if time.Since(t) > time.Duration(stickSeconds)*time.Second {
		antigravityCreditsStickByAuth.Delete(authID)
		return false
	}
	return true
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
