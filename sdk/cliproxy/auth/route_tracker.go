package auth

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

const maxRouteAttemptsRecorded = 16

type routeAttempt struct {
	providerClass string
	status        string
}

type routeAttemptTracker struct {
	attempts []routeAttempt
	seen     map[routeAttempt]bool
	omitted  int
}

func newRouteAttemptTracker() *routeAttemptTracker {
	return &routeAttemptTracker{
		attempts: make([]routeAttempt, 0, 8),
		seen:     make(map[routeAttempt]bool),
	}
}

func (t *routeAttemptTracker) Record(auth *Auth, err error) {
	if t == nil {
		return
	}
	pClass := sanitizeProviderClass(authProviderName(auth))
	statusStr := sanitizeStatus(err)
	entry := routeAttempt{
		providerClass: pClass,
		status:        statusStr,
	}
	if t.seen[entry] {
		return
	}
	t.seen[entry] = true
	if len(t.attempts) >= maxRouteAttemptsRecorded {
		t.omitted++
		return
	}
	t.attempts = append(t.attempts, entry)
}

func (t *routeAttemptTracker) Summary() string {
	if t == nil || len(t.attempts) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("attempted routes: [")
	for i, a := range t.attempts {
		if i > 0 {
			sb.WriteString(", ")
		}
		sb.WriteString(a.providerClass)
		sb.WriteString(":")
		sb.WriteString(a.status)
	}
	if t.omitted > 0 {
		sb.WriteString(fmt.Sprintf(", ... (+%d omitted)", t.omitted))
	}
	sb.WriteString("]")
	return sb.String()
}

func authProviderName(auth *Auth) string {
	if auth == nil {
		return ""
	}
	return auth.Provider
}

func sanitizeProviderClass(p string) string {
	switch strings.ToLower(strings.TrimSpace(p)) {
	case "gemini":
		return "gemini"
	case "claude", "anthropic":
		return "claude"
	case "openai":
		return "openai"
	case "codex":
		return "codex"
	case "antigravity":
		return "antigravity"
	case "aistudio":
		return "aistudio"
	case "vertex", "vertexai", "vertex_ai":
		return "vertex"
	default:
		return "other"
	}
}

func sanitizeStatus(err error) string {
	if err == nil {
		return "error"
	}
	var authErr *Error
	if errors.As(err, &authErr) && authErr != nil {
		if authErr.HTTPStatus > 0 && authErr.HTTPStatus < 1000 {
			return strconv.Itoa(authErr.HTTPStatus)
		}
	}
	if sc := statusCodeFromError(err); sc > 0 && sc < 1000 {
		return strconv.Itoa(sc)
	}
	return "error"
}

type routeExhaustionError struct {
	cause   error
	summary string
}

func wrapRouteExhaustion(cause error, tracker *routeAttemptTracker) error {
	if cause == nil {
		return nil
	}
	if tracker == nil {
		return cause
	}
	summary := tracker.Summary()
	if summary == "" {
		return cause
	}
	var authErr *Error
	if errors.As(cause, &authErr) && authErr != nil {
		cloned := *authErr
		if cloned.Message != "" {
			cloned.Message = cloned.Message + "; " + summary
		} else if cloned.Code != "" {
			cloned.Message = cloned.Code + "; " + summary
		} else {
			cloned.Message = summary
		}
		return &cloned
	}
	return &routeExhaustionError{
		cause:   cause,
		summary: summary,
	}
}

func (e *routeExhaustionError) Error() string {
	if e == nil {
		return ""
	}
	if e.cause == nil {
		return e.summary
	}
	if e.summary == "" {
		return e.cause.Error()
	}
	return e.cause.Error() + "; " + e.summary
}

func (e *routeExhaustionError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

// Headers forwards the wrapped cause's error headers if it exposes them, so
// handlers that collect passthrough headers from the final routed error do not
// lose them when the cause is wrapped by route exhaustion. It returns a fresh
// copy of the cause's map and never mutates the caller's headers.
func (e *routeExhaustionError) Headers() http.Header {
	if e == nil {
		return nil
	}
	var carrier interface{ Headers() http.Header }
	if errors.As(e.cause, &carrier) && carrier != nil {
		return cloneHTTPHeader(carrier.Headers())
	}
	return nil
}
