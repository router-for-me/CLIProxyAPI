package auth

import (
	"encoding/json"
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

type routeExhaustionClonedError struct {
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
	return &routeExhaustionClonedError{
		cause:   cause,
		summary: summary,
	}
}

func (e *routeExhaustionClonedError) Error() string {
	if e == nil {
		return ""
	}
	if e.cause == nil {
		return e.summary
	}
	if e.summary == "" {
		return e.cause.Error()
	}
	causeStr := e.cause.Error()
	if isStructuredJSON(causeStr) {
		return causeStr
	}
	return causeStr + "; " + e.summary
}

func isStructuredJSON(s string) bool {
	trimmed := strings.TrimSpace(s)
	if len(trimmed) < 2 {
		return false
	}
	if (trimmed[0] == '{' && trimmed[len(trimmed)-1] == '}') || (trimmed[0] == '[' && trimmed[len(trimmed)-1] == ']') {
		return json.Valid([]byte(trimmed))
	}
	return false
}

func (e *routeExhaustionClonedError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

// Headers forwards the wrapped cause's error headers if it exposes them, so
// handlers that collect passthrough headers from the final routed error do not
// lose them when the cause is wrapped by route exhaustion. It returns a fresh
// copy of the cause's map and never mutates the caller's headers.
func (e *routeExhaustionClonedError) Headers() http.Header {
	if e == nil {
		return nil
	}
	var carrier interface{ Headers() http.Header }
	if errors.As(e.cause, &carrier) && carrier != nil {
		return cloneHTTPHeader(carrier.Headers())
	}
	return nil
}

// SafeResponseHeaders forwards trusted response headers from the wrapped cause
// if it exposes them, so handlers reading SafeResponseHeaders from the final
// routed error keep e.g. the Home busy error's Retry-After through route
// exhaustion. It returns a fresh copy and never mutates the caller's headers.
func (e *routeExhaustionClonedError) SafeResponseHeaders() http.Header {
	if e == nil {
		return nil
	}
	return SafeResponseHeaders(e.cause)
}
