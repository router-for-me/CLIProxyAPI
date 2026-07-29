package executor

import (
	"context"
	"errors"
	"net/http"
)

// StatusClientClosedRequest is the de facto HTTP status used when the client
// closes a request before the server can produce a response.
const StatusClientClosedRequest = 499

// StatusCodeFromError returns an explicit StatusError code when present, then
// classifies context cancellation and deadline errors that may be wrapped by
// HTTP transports. Explicit status errors take precedence over context causes.
func StatusCodeFromError(err error) int {
	if err == nil {
		return 0
	}
	var statusErr StatusError
	if errors.As(err, &statusErr) && statusErr != nil {
		if status := statusErr.StatusCode(); status > 0 {
			return status
		}
	}
	status, _ := ContextErrorStatus(err)
	return status
}

// ContextErrorStatus classifies context termination errors for downstream HTTP
// responses. The boolean reports whether err contains a recognized context
// cause.
func ContextErrorStatus(err error) (int, bool) {
	switch {
	case errors.Is(err, context.Canceled):
		return StatusClientClosedRequest, true
	case errors.Is(err, context.DeadlineExceeded):
		return http.StatusGatewayTimeout, true
	default:
		return 0, false
	}
}
