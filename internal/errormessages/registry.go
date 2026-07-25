// Package errormessages lets operators customize the JSON error response
// bodies returned by the proxy server for each HTTP status code. Default
// values ship in-repo; overrides are persisted in the PostgreSQL
// error_messages table and served by the dashboard.
//
// Hot path: Respond(c, statusCode, details) is invoked from gin handlers
// (including the policy middleware). It reads from an in-memory cache that
// is refreshed from PG via RefreshFromDB on startup and via Invalidate on
// dashboard-configured edits. The fallback when no override exists is a
// curated default that ships in this package — so deployments without PG
// configured still produce stable messages.
package errormessages

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// Message describes one customizable error response.
type Message struct {
	StatusCode   int       `json:"status_code"`
	Title        string    `json:"title"`
	Message      string    `json:"message"`
	BodyTemplate string    `json:"body_template,omitempty"` // optional JSON template, see renderBody
	Enabled      bool      `json:"enabled"`
	CreatedAt    time.Time `json:"created_at,omitempty"`
	UpdatedAt    time.Time `json:"updated_at,omitempty"`
}

// Store is the data accessor contract the registry needs. The concrete
// implementation lives in internal/store and operates on the error_messages
// table.
type Store interface {
	ListAll(ctx context.Context) ([]Message, error)
	GetByStatusCode(ctx context.Context, statusCode int) (*Message, error)
	Upsert(ctx context.Context, m Message) error
	Delete(ctx context.Context, statusCode int) error
}

// Registry is the singleton cache consulted by Respond() on every error
// reply. It is safe for concurrent use. Refresh syncs from PG; Invalidate
// drops entries so the next Respond() falls back to defaults + re-queries.
type Registry struct {
	store Store

	mu     sync.RWMutex
	byCode map[int]Message
	loaded bool
}

var (
	registryMu sync.Mutex
	singleton  *Registry
)

// SetStore initializes (or replaces) the global registry bound to the
// supplied Store. Pass nil to disable PG overrides so Respond() always uses
// the curated defaults. Safe to call more than once (e.g. on hot-reload).
func SetStore(store Store) {
	registryMu.Lock()
	defer registryMu.Unlock()
	singleton = &Registry{store: store}
}

// registry returns the singleton, creating an empty one if SetStore was not
// called (e.g. tests without PG). Respond() will then always use defaults.
func registry() *Registry {
	registryMu.Lock()
	defer registryMu.Unlock()
	if singleton == nil {
		singleton = &Registry{}
	}
	return singleton
}

// RefreshFromDB pulls every enabled row from PG and replaces the in-memory
// cache. Idempotent; called on server startup and after every dashboard edit.
// Failures are swallowed (cache stays unchanged) so a transient DB outage
// does not degrade error responses.
func RefreshFromDB(ctx context.Context) {
	r := registry()
	if r == nil || r.store == nil {
		return
	}
	rows, err := r.store.ListAll(ctx)
	if err != nil {
		return
	}
	byCode := make(map[int]Message, len(rows))
	for _, m := range rows {
		if m.Enabled {
			byCode[m.StatusCode] = m
		}
	}
	r.mu.Lock()
	r.byCode = byCode
	r.loaded = true
	r.mu.Unlock()
}

// Invalidate drops the cache so the next Respond() call re-reads from PG
// (or falls back to defaults if the store is unavailable).
func Invalidate() {
	r := registry()
	r.mu.Lock()
	r.byCode = nil
	r.loaded = false
	r.mu.Unlock()
}

// Lookup returns the configured Message for a status code. WithPG=true means
// an attempt was made to consult the store; the bool returned is "found".
// The function never returns an error — failures fall back to defaults so
// the hot path stays non-blocking.
func Lookup(ctx context.Context, statusCode int) (Message, bool) {
	// Fast path: check the in-memory cache.
	r := registry()
	r.mu.RLock()
	if m, ok := r.byCode[statusCode]; ok {
		r.mu.RUnlock()
		return m, true
	}
	loaded := r.loaded
	r.mu.RUnlock()

	// Cache miss + not yet loaded: re-query PG. Future Respond() calls for
	// the same code hit the cache. The query is bounded by row count (one
	// row per configured status code, typically <100 rows).
	if !loaded && r.store != nil {
		RefreshFromDB(ctx)
		r.mu.RLock()
		defer r.mu.RUnlock()
		if m, ok := r.byCode[statusCode]; ok {
			return m, true
		}
	}
	return Default(statusCode), false
}

// Respond writes the customized error JSON to the supplied gin.Context.
// `details` is an optional short string merged into the response when the
// configured message contains the literal "{{details}}" — typical use is
// surfacing which budget cap was hit ("${{details}} USD") or which model
// was blocked ("model {{details}} is not permitted").
//
// When no override is configured for the status code, the curated default
// is used. When statusCode is 0, the function is a no-op (callers use 0 to
// signal "I already wrote a response").
func Respond(c *gin.Context, statusCode int, details string) {
	if c == nil || statusCode == 0 {
		return
	}
	msg, _ := Lookup(c.Request.Context(), statusCode)
	body := renderBody(msg, details)
	c.AbortWithStatusJSON(statusCode, body)
}

// BodyFor returns the rendered error-response body for a status code, in
// the same JSON shape the proxy emits at runtime. `details` is merged into
// the configured message when it contains the "{{details}}" placeholder.
//
// Disabled overrides are skipped: when an operator sets Enabled=false the
// row is not loaded into the registry cache, so Lookup falls back to the
// curated default (which is always enabled) — exactly mirroring runtime
// behavior. Callers that need the rendered body without a gin.Context
// (e.g. the preview endpoint, or handlers that write the body themselves)
// should use this instead of Respond.
func BodyFor(ctx context.Context, statusCode int, details string) gin.H {
	msg, _ := Lookup(ctx, statusCode)
	return renderBody(msg, details)
}

// RenderBody is the exported form of renderBody, kept so callers outside
// the package (the management preview endpoint) can render a candidate
// Message without consulting the registry — useful for testing edits
// before they are persisted.
func RenderBody(m Message, details string) gin.H {
	return renderBody(m, details)
}

// MessageText returns the configured message string for a status code with
// placeholders substituted, WITHOUT the surrounding JSON body. It is meant
// for protocols whose streaming error format is constrained (e.g. OpenAI
// Responses SSE chunks with a fixed {type,code,message} shape): callers
// keep their protocol-specific envelope but surface the operator's custom
// message text in the `message` field. As with Respond, an operator
// override (enabled) takes precedence; otherwise the curated default is
// used, and disabled overrides are skipped.
func MessageText(ctx context.Context, statusCode int, details string) string {
	msg, _ := Lookup(ctx, statusCode)
	return substitute(msg.Message, details, msg)
}

// renderBody builds the JSON response shape. The default shape is
//
//	{
//	  "error": {
//	    "type":    "<slug>",
//	    "code":    <status>,
//	    "title":   "<title>",
//	    "message": "<message>"
//	  }
//	}
//
// When BodyTemplate is set it is parsed as JSON; if it parses cleanly the
// rendered template replaces the default body (with `{{details}}`/`{{code}}`/
// `{{title}}`/`{{message}}` substitutions). If parsing fails we fall back to
// the default body so a misconfigured template cannot break error responses.
func renderBody(m Message, details string) gin.H {
	defaultBody := gin.H{
		"error": gin.H{
			"type":    slugFor(m.StatusCode),
			"code":    m.StatusCode,
			"title":   m.Title,
			"message": substitute(m.Message, details, m),
		},
	}
	templ := strings.TrimSpace(m.BodyTemplate)
	if templ == "" {
		return defaultBody
	}
	rendered := substitute(templ, details, m)
	// Verify the rendered template is still valid JSON; if not, return the
	// default body so we never emit malformed JSON to clients.
	var parsed map[string]any
	if err := json.Unmarshal([]byte(rendered), &parsed); err != nil {
		return defaultBody
	}
	return parsed
}

// substitute replaces placeholder tokens in s with their values. Tokens:
//
//	{{details}}  — the caller-supplied detail string
//	{{code}}     — the status code integer
//	{{title}}    — the configured title
//	{{message}}  — the configured message (already substituted recursively)
func substitute(s string, details string, m Message) string {
	if s == "" {
		return s
	}
	out := s
	out = strings.ReplaceAll(out, "{{details}}", details)
	out = strings.ReplaceAll(out, "{{code}}", fmt.Sprintf("%d", m.StatusCode))
	out = strings.ReplaceAll(out, "{{title}}", m.Title)
	out = strings.ReplaceAll(out, "{{message}}", m.Message)
	return out
}

// slugFor returns a stable snake_case type string for each status code,
// matching the conventions of OpenAI / Anthropic error responses so
// existing SDK error handlers keep working unchanged.
func slugFor(code int) string {
	switch code {
	case http.StatusBadRequest:
		return "invalid_request_error"
	case http.StatusUnauthorized:
		return "authentication_error"
	case http.StatusForbidden:
		return "permission_denied"
	case http.StatusNotFound:
		return "not_found_error"
	case http.StatusPaymentRequired:
		return "quota_exceeded"
	case http.StatusMethodNotAllowed:
		return "method_not_allowed"
	case http.StatusConflict:
		return "conflict_error"
	case http.StatusRequestTimeout:
		return "timeout_error"
	case http.StatusTooManyRequests:
		return "rate_limit_exceeded"
	case http.StatusInternalServerError:
		return "internal_error"
	case http.StatusBadGateway:
		return "upstream_error"
	case http.StatusServiceUnavailable:
		return "service_unavailable"
	case http.StatusGatewayTimeout:
		return "gateway_timeout"
	default:
		return "error"
	}
}
