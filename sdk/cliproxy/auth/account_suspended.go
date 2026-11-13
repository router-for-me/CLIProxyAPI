package auth

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/MoeForge/moego/ojson"
)

const accountSuspendedMessage = "account suspended for a Terms of Service violation"

func isAccountSuspendedResultError(err *Error) bool {
	return err != nil && err.Code == ErrorCodeAccountSuspended
}

// accountSuspendedStatusMessage describes the suspension for the status
// message, with the retry the executor attached when the credential cools. The
// appeal link stays out of it: the management API redacts URLs in status
// messages, so the link goes to the log instead.
func accountSuspendedStatusMessage(retryAfter *time.Duration, cooling bool) string {
	if !cooling || retryAfter == nil || *retryAfter <= 0 {
		return accountSuspendedMessage
	}
	wait := retryAfter.Round(time.Second).String()
	if *retryAfter%time.Hour == 0 {
		wait = fmt.Sprintf("%dh", int(*retryAfter/time.Hour))
	}
	return accountSuspendedMessage + "; retrying after " + wait
}

// accountSuspendedLogMessage is the one warning a new suspension logs. The log
// formatter prints only known fields, so the appeal link goes in the message.
func accountSuspendedLogMessage(retryAfter *time.Duration, cooling bool, upstreamMessage string) string {
	message := accountSuspendedStatusMessage(retryAfter, cooling)
	if appealURL := accountSuspendedAppealURL(upstreamMessage); appealURL != "" {
		message += "; appeal: " + appealURL
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
