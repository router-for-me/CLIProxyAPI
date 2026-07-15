package otelplugin

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// HeaderBaggage is the canonical inbound header name. Case-insensitive in Go's
// net/http, but operators querying logs occasionally look for the exact form.
const HeaderBaggage = "Baggage"

// Middleware returns a Gin middleware that parses the inbound `baggage:`
// header and stores it on the request context. The plugin's HandleUsage reads
// it back via BaggageFromContext when building span attributes.
//
// Wiring (server-side, optional opt-in):
//
//	engine.Use(otelplugin.Middleware())
//
// Always-on parsing — the propagation policy controls only what flows
// upstream from here on the *outbound* hop, not whether we capture the
// inbound envelope.
//
// Safe to register unconditionally even when the OTLP exporter is disabled:
// parsing a missing or empty header is cheap and produces nil baggage.
func Middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		header := c.GetHeader(HeaderBaggage)
		if header == "" {
			c.Next()
			return
		}
		b := ParseBaggageHeader(header)
		if len(b) == 0 {
			c.Next()
			return
		}
		ctx := WithBaggage(c.Request.Context(), b)
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	}
}

// PropagateBaggage applies the configured propagation policy to an outbound
// request. Call this from the upstream client construction site (where the
// proxy builds the request that will hit the LLM provider). Returns the
// header value (or empty string to omit the header entirely).
//
// Modes:
//   - off       : never propagate. Returns "" regardless of input. Safe default.
//   - propagate : forward the entire inbound baggage verbatim.
//   - allowlist : forward only keys in BaggageConfig.AllowedKeys.
func PropagateBaggage(inbound Baggage) string {
	cfg := loadConfig()
	switch cfg.Baggage.Propagation {
	case BaggagePropagate:
		return FormatBaggageHeader(inbound)
	case BaggageAllowlist:
		return FormatBaggageHeader(FilterAllowed(inbound, cfg.Baggage.AllowedKeys))
	case BaggageOff, "":
		return ""
	default:
		return ""
	}
}

// ApplyOutbound is a convenience helper that sets the `baggage:` header on an
// outbound *http.Request per the configured propagation policy. No-op when
// propagation is off or the inbound envelope is empty.
func ApplyOutbound(req *http.Request, inbound Baggage) {
	if req == nil {
		return
	}
	value := PropagateBaggage(inbound)
	if value == "" {
		return
	}
	req.Header.Set(HeaderBaggage, value)
}

// HeaderName re-exports HeaderBaggage with the lowercase form some operators
// prefer to log against. Use net/http header normalisation (Canonical-Case)
// when actually setting headers; this is a string constant only.
func HeaderName() string { return strings.ToLower(HeaderBaggage) }
