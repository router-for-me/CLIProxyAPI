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

// AccountSuspensionRecord marks a credential whose account the upstream has
// suspended. Every suspension response sets it, other errors never touch it,
// and only a success or disabling the credential (or a manual quota reset)
// clears it. It is persisted with the auth-level cooldown record.
type AccountSuspensionRecord struct {
	// RecordedAt is when the first suspension response arrived.
	RecordedAt time.Time `json:"recorded_at"`
	// Until is when the credential may be tried again; zero with cooling off.
	Until time.Time `json:"until,omitempty"`
	// AppealURL is the checked appeal link from the response, if any.
	AppealURL string `json:"appeal_url,omitempty"`
	// seq is the request-order watermark: results of requests dispatched at
	// or before it predate the suspension. Runtime only, zero after a restart.
	seq uint64
}

func (r *AccountSuspensionRecord) clone() *AccountSuspensionRecord {
	if r == nil {
		return nil
	}
	copyRecord := *r
	return &copyRecord
}

// hasAccountSuspension reports whether the credential carries a suspension
// no success has cleared yet, whether or not it is cooling.
func hasAccountSuspension(auth *Auth) bool {
	return auth != nil && auth.accountSuspension != nil
}

// activeAccountSuspension returns when a suspended credential may be tried
// again. It reports false once that time has passed or cooling is off.
func activeAccountSuspension(auth *Auth, now time.Time) (time.Time, bool) {
	if !hasAccountSuspension(auth) || !auth.accountSuspension.Until.After(now) {
		return time.Time{}, false
	}
	return auth.accountSuspension.Until, true
}

// staleUnderAccountSuspension reports whether a result, other than another
// suspension, comes from a request dispatched before the suspension was
// recorded. Such a result proves nothing about the account.
func staleUnderAccountSuspension(auth *Auth, result Result) bool {
	if !hasAccountSuspension(auth) || isAccountSuspendedResultError(result.Error) {
		return false
	}
	seq := auth.accountSuspension.seq
	return result.ExecutionSeq > 0 && seq > 0 && result.ExecutionSeq <= seq
}

// recordAccountSuspension sets or refreshes the credential's suspension
// record. It reports whether the record is new, so the warning logs once.
func recordAccountSuspension(auth *Auth, resultErr *Error, seq uint64, now time.Time, disableCooling bool) bool {
	record := auth.accountSuspension
	created := record == nil
	if created {
		record = &AccountSuspensionRecord{RecordedAt: now}
		auth.accountSuspension = record
	}
	if seq > record.seq {
		record.seq = seq
	}
	if disableCooling {
		record.Until = time.Time{}
	} else if next := now.Add(accountSuspendedCooldown); next.After(record.Until) {
		record.Until = next
	}
	if appealURL := accountSuspendedAppealURL(resultErr.Message); appealURL != "" {
		record.AppealURL = appealURL
	}
	return created
}

// liftAccountSuspension drops the suspension record when the credential is
// disabled or reset by hand. It reports whether there was one.
func liftAccountSuspension(auth *Auth) bool {
	if !hasAccountSuspension(auth) {
		return false
	}
	auth.accountSuspension = nil
	return true
}

// dropAccountSuspensionCooldown keeps a recorded suspension once cooling is
// switched off, but drops its deadline and the retry time from the status.
func dropAccountSuspensionCooldown(auth *Auth) {
	if !hasAccountSuspension(auth) {
		return
	}
	auth.accountSuspension.Until = time.Time{}
	message := accountSuspendedStatusMessage(true)
	if isAccountSuspendedResultError(auth.LastError) {
		auth.StatusMessage = message
	}
	for _, state := range auth.ModelStates {
		if state != nil && isAccountSuspendedResultError(state.LastError) {
			state.StatusMessage = message
		}
	}
}

// applyAccountSuspension records a suspension on every model state of the
// credential, and on the credential itself when recordOnAuth is set, because
// the upstream refuses every model on the account alike. A model that was
// already cooling keeps that layer under the suspension, the way
// credentialQuotaShadow keeps one under a credential quota. With cooling off
// it records the status and leaves availability alone.
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
		if !isAccountSuspendedResultError(state.LastError) {
			state.suspensionShadow = nil
			if isModelStateActiveCooldown(state, now) {
				state.suspensionShadow = state.Clone()
			}
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

// clearAccountSuspension lifts the suspension from the credential and every
// model state once a success shows the account works again. A model whose own
// earlier cooldown is still running gets that exact stack back; the rest
// reset. It reports whether anything was cleared.
func clearAccountSuspension(auth *Auth, now time.Time) bool {
	if auth == nil {
		return false
	}
	cleared := liftAccountSuspension(auth)
	for _, state := range auth.ModelStates {
		if state == nil || state.Status == StatusDisabled {
			continue
		}
		if !isAccountSuspendedResultError(state.LastError) {
			// A newer failure already replaced the suspension on this model.
			if state.suspensionShadow != nil {
				state.suspensionShadow = nil
				cleared = true
			}
			continue
		}
		if shadow := state.suspensionShadow; shadow != nil && isModelStateActiveCooldown(shadow, now) {
			*state = *shadow.Clone()
			state.suspensionShadow = nil
			state.UpdatedAt = now
		} else {
			resetModelState(state, now)
		}
		cleared = true
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
	if cleared && len(auth.ModelStates) > 0 {
		updateAggregatedAvailability(auth, now)
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

// accountSuspendedLogMessage is the one warning a new suspension record logs.
// The log formatter prints only known fields, so the appeal link goes in the message.
func accountSuspendedLogMessage(record *AccountSuspensionRecord) string {
	message := accountSuspendedStatusMessage(record.Until.IsZero())
	if record.AppealURL != "" {
		message += "; appeal: " + record.AppealURL
	}
	return message
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
