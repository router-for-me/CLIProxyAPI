package auth

import (
	"fmt"
	"math"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

const (
	// MetadataHoldUntil holds a credential out of every selection path until the
	// RFC3339 instant it stores. It lives in the auth JSON, so it can be set or
	// cleared without a restart (file edit or the credentials/fields endpoint).
	MetadataHoldUntil = "hold_until"
	// MetadataGroup names the credential groups an account pin can select.
	// The value is a comma-separated string, for example "personal,pro".
	MetadataGroup = "group"
	// MetadataPinOnly keeps a credential out of unpinned routing: only a request
	// whose account pin matches it can use it. Holds and disabled still apply.
	MetadataPinOnly = "pin_only"
)

// AuthPinOnly reports whether the credential serves pinned requests only.
func AuthPinOnly(auth *Auth) bool {
	if auth == nil {
		return false
	}
	switch value := auth.Metadata[MetadataPinOnly].(type) {
	case bool:
		return value
	case string:
		return strings.EqualFold(strings.TrimSpace(value), "true")
	}
	return false
}

func accountPinFromMetadata(meta map[string]any) string {
	value, _ := meta[cliproxyexecutor.AccountPinMetadataKey].(string)
	return strings.TrimSpace(value)
}

// authHoldUntil reports the hold deadline when the credential is held at now.
func authHoldUntil(auth *Auth, now time.Time) (time.Time, bool) {
	if auth == nil {
		return time.Time{}, false
	}
	raw, _ := auth.Metadata[MetadataHoldUntil].(string)
	if strings.TrimSpace(raw) == "" {
		return time.Time{}, false
	}
	until, errParse := time.Parse(time.RFC3339, strings.TrimSpace(raw))
	if errParse != nil || !until.After(now) {
		return time.Time{}, false
	}
	return until, true
}

// authMatchesAccountPin matches a pin case-insensitively against the credential ID,
// file name (with or without .json), label, email, or any entry of its group field.
func authMatchesAccountPin(auth *Auth, pin string) bool {
	if auth == nil {
		return false
	}
	pin = strings.TrimSpace(pin)
	names := []string{auth.ID, auth.Label}
	if auth.FileName != "" {
		base := filepath.Base(auth.FileName)
		names = append(names, base, strings.TrimSuffix(base, ".json"))
	}
	if email, _ := auth.Metadata["email"].(string); email != "" {
		names = append(names, email)
	}
	if groups, _ := auth.Metadata[MetadataGroup].(string); groups != "" {
		names = append(names, strings.Split(groups, ",")...)
	}
	for _, name := range names {
		if name = strings.TrimSpace(name); name != "" && strings.EqualFold(name, pin) {
			return true
		}
	}
	return false
}

// accountPinError refuses a pinned request outright instead of rotating it to
// another account. It carries Retry-After when the pinned account has a known
// return time.
type accountPinError struct {
	err        *Error
	retryAfter time.Duration
}

func (e *accountPinError) Error() string   { return e.err.Error() }
func (e *accountPinError) StatusCode() int { return e.err.StatusCode() }
func (e *accountPinError) Unwrap() error   { return e.err }

func (e *accountPinError) Headers() http.Header {
	if e == nil || e.retryAfter <= 0 {
		return nil
	}
	headers := make(http.Header)
	headers.Set("Retry-After", strconv.Itoa(int(math.Ceil(e.retryAfter.Seconds()))))
	return headers
}

// accountPinRefusal returns nil when at least one credential matching the request's
// account pin can serve the model now. Otherwise it explains why the pinned account
// cannot: unknown, disabled, held, or at its limit (cooling down).
func (m *Manager) accountPinRefusal(providers []string, model string, opts cliproxyexecutor.Options) error {
	pin := accountPinFromMetadata(opts.Metadata)
	if m == nil || pin == "" {
		return nil
	}
	providerSet := make(map[string]struct{}, len(providers))
	for _, provider := range providers {
		providerSet[canonicalSchedulingProvider(provider)] = struct{}{}
	}
	now := time.Now()
	registryRef := registry.GetGlobalRegistry()
	matched, disabled := 0, 0
	var heldUntil, coolingUntil time.Time

	m.mu.RLock()
	for _, candidate := range m.auths {
		if !authMatchesAccountPin(candidate, pin) {
			continue
		}
		if _, ok := providerSet[canonicalSchedulingProvider(executorKeyFromAuth(candidate))]; !ok {
			continue
		}
		if model != "" && !m.authSupportsRouteModel(registryRef, candidate, model) {
			continue
		}
		matched++
		if candidate.Disabled || candidate.Status == StatusDisabled {
			disabled++
			continue
		}
		if until, held := authHoldUntil(candidate, now); held {
			if heldUntil.IsZero() || until.Before(heldUntil) {
				heldUntil = until
			}
			continue
		}
		blocked, _, next := isAuthBlockedForModel(candidate, m.selectionModelForAuth(candidate, model), now)
		if !blocked {
			m.mu.RUnlock()
			return nil
		}
		if coolingUntil.IsZero() || (!next.IsZero() && next.Before(coolingUntil)) {
			coolingUntil = next
		}
	}
	m.mu.RUnlock()

	if matched == 0 {
		return &accountPinError{err: &Error{
			Code:       "account_not_found",
			Message:    fmt.Sprintf("no credential matches account %q for model %q", pin, model),
			HTTPStatus: http.StatusBadRequest,
		}}
	}
	var reasons []string
	soonest := time.Time{}
	if !coolingUntil.IsZero() {
		reasons = append(reasons, fmt.Sprintf("at its limit until %s", coolingUntil.UTC().Format(time.RFC3339)))
		soonest = coolingUntil
	}
	if !heldUntil.IsZero() {
		reasons = append(reasons, fmt.Sprintf("held until %s", heldUntil.UTC().Format(time.RFC3339)))
		if soonest.IsZero() || heldUntil.Before(soonest) {
			soonest = heldUntil
		}
	}
	if disabled > 0 {
		reasons = append(reasons, "disabled")
	}
	if len(reasons) == 0 {
		reasons = append(reasons, "unavailable")
	}
	refusal := &accountPinError{err: &Error{
		Code:       "account_unavailable",
		Message:    fmt.Sprintf("account %q cannot serve model %q: %s", pin, model, strings.Join(reasons, "; ")),
		HTTPStatus: http.StatusServiceUnavailable,
	}}
	if !soonest.IsZero() {
		refusal.err.HTTPStatus = http.StatusTooManyRequests
		refusal.retryAfter = soonest.Sub(now)
	}
	return refusal
}
