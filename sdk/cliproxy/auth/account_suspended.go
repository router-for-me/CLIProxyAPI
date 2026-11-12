package auth

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/MoeForge/moego/ojson"
)

// accountSuspendedCooldown replaces the 30 minute 403 cooldown for a suspended
// account. The upstream refuses every call until the owner acts, so a shorter
// retry only adds failed calls and log noise.
const accountSuspendedCooldown = 24 * time.Hour

const accountSuspendedMessage = "account suspended for a Terms of Service violation"

func isAccountSuspendedResultError(err *Error) bool {
	return err != nil && err.Code == ErrorCodeAccountSuspended
}

// hasAccountSuspension reports whether the credential or any of its models
// still carries a suspension that no success has cleared.
func hasAccountSuspension(auth *Auth) bool {
	if auth == nil {
		return false
	}
	if isAccountSuspendedResultError(auth.LastError) {
		return true
	}
	for _, state := range auth.ModelStates {
		if state != nil && isAccountSuspendedResultError(state.LastError) {
			return true
		}
	}
	return false
}

// activeAccountSuspension returns when a suspended credential may be tried
// again. It reports false once that time has passed or cooling is off.
func activeAccountSuspension(auth *Auth, now time.Time) (time.Time, bool) {
	if auth == nil || !isAccountSuspendedResultError(auth.LastError) || !auth.NextRetryAfter.After(now) {
		return time.Time{}, false
	}
	return auth.NextRetryAfter, true
}

// applyAccountSuspension records a suspension on every model state of the
// credential, and on the credential itself when recordOnAuth is set, because
// the upstream refuses every model on the account alike. With cooling off it
// records the status and leaves availability alone.
func applyAccountSuspension(auth *Auth, resultErr *Error, now time.Time, disableCooling, recordOnAuth bool) {
	if auth == nil {
		return
	}
	message := accountSuspendedStatusMessage(disableCooling)
	var next time.Time
	if !disableCooling {
		next = now.Add(accountSuspendedCooldown)
	}
	for _, state := range auth.ModelStates {
		if state == nil || state.Status == StatusDisabled {
			continue
		}
		state.Status = StatusError
		state.LastError = cloneError(resultErr)
		state.StatusMessage = message
		state.UpdatedAt = now
		if !disableCooling {
			state.Unavailable = true
			if next.After(state.NextRetryAfter) {
				state.NextRetryAfter = next
			}
		}
	}
	if !recordOnAuth {
		return
	}
	auth.Status = StatusError
	auth.LastError = cloneError(resultErr)
	auth.StatusMessage = message
	auth.UpdatedAt = now
	if !disableCooling {
		auth.Unavailable = true
		if next.After(auth.NextRetryAfter) {
			auth.NextRetryAfter = next
		}
	}
}

// clearAccountSuspension resets every model state a suspension cooled, and
// the credential's suspension status, once a success shows the account works
// again. It reports whether anything was cleared.
func clearAccountSuspension(auth *Auth, now time.Time) bool {
	if auth == nil {
		return false
	}
	cleared := false
	for _, state := range auth.ModelStates {
		if state != nil && state.Status != StatusDisabled && isAccountSuspendedResultError(state.LastError) {
			resetModelState(state, now)
			cleared = true
		}
	}
	if isAccountSuspendedResultError(auth.LastError) {
		auth.LastError = nil
		auth.StatusMessage = ""
		auth.Status = StatusActive
		auth.Unavailable = false
		auth.NextRetryAfter = time.Time{}
		auth.UpdatedAt = now
		cleared = true
	}
	return cleared
}

// accountSuspendedStatusMessage describes the suspension for the status
// message. The appeal link stays out of it: the management API redacts URLs in
// status messages, so the link goes to the log instead.
func accountSuspendedStatusMessage(disableCooling bool) string {
	if disableCooling {
		return accountSuspendedMessage
	}
	return fmt.Sprintf("%s; retrying after %dh", accountSuspendedMessage, int(accountSuspendedCooldown/time.Hour))
}

// accountSuspendedAppealURL returns the first appeal link in the response's
// ErrorInfo details that is an https link on forms.gle or google.com.
func accountSuspendedAppealURL(message string) string {
	for _, detail := range ojson.Get(message, "error.details").Array() {
		raw := strings.TrimSpace(detail.Get("metadata.appeal_url").String())
		if raw == "" {
			continue
		}
		parsed, errParse := url.Parse(raw)
		if errParse != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.Port() != "" {
			continue
		}
		host := strings.ToLower(parsed.Hostname())
		if host != "forms.gle" && host != "google.com" && !strings.HasSuffix(host, ".google.com") {
			continue
		}
		return parsed.String()
	}
	return ""
}
