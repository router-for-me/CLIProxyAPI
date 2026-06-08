package otelplugin

import (
	"context"
	"net/url"
	"strings"
)

// W3C Baggage parser. Spec: https://www.w3.org/TR/baggage/
//
// We implement this in-package (instead of pulling go.opentelemetry.io/otel's
// baggage package) for two reasons:
//   - keep the package's exposed dependency surface narrow during the initial
//     review (only the OTLP exporter + trace SDK are required imports);
//   - the parser doubles as the baggage middleware's storage shape, which is
//     just a map[string]string — no Member/Property objects needed.

// Baggage is the parsed identity envelope from the request's `baggage:` HTTP
// header. Keys are lowercased; values are URL-decoded. Per-entry metadata
// after `;` in a single entry is intentionally dropped — consumers that need
// it should adopt the OTel SDK's baggage package.
type Baggage map[string]string

// ParseBaggageHeader parses a single `baggage:` header value into a Baggage
// map. Returns nil when the header is missing or no entries are well-formed.
//
// Per W3C Baggage: entries are comma-separated; within an entry, optional
// metadata follows the value after a `;` and is opaque to consumers. The
// parser is intentionally permissive — malformed entries are skipped, not
// errored, so a single bad entry does not poison the whole header.
func ParseBaggageHeader(headerValue string) Baggage {
	headerValue = strings.TrimSpace(headerValue)
	if headerValue == "" {
		return nil
	}
	out := make(Baggage)
	for _, rawEntry := range strings.Split(headerValue, ",") {
		entry := strings.TrimSpace(rawEntry)
		if entry == "" {
			continue
		}
		if semi := strings.IndexByte(entry, ';'); semi >= 0 {
			entry = entry[:semi]
		}
		eq := strings.IndexByte(entry, '=')
		if eq <= 0 {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(entry[:eq]))
		rawValue := strings.TrimSpace(entry[eq+1:])
		if key == "" || rawValue == "" {
			continue
		}
		value, err := url.QueryUnescape(rawValue)
		if err != nil {
			value = rawValue
		}
		out[key] = value
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// FormatBaggageHeader renders a Baggage map back into a W3C `baggage:` header
// value. Used when re-emitting allowlisted keys upstream. Output keys are
// emitted in the input map's iteration order — operators concerned about
// canonical ordering can sort the keys before calling.
func FormatBaggageHeader(b Baggage) string {
	if len(b) == 0 {
		return ""
	}
	var parts []string
	for k, v := range b {
		if strings.TrimSpace(k) == "" || strings.TrimSpace(v) == "" {
			continue
		}
		parts = append(parts, k+"="+url.QueryEscape(v))
	}
	return strings.Join(parts, ",")
}

// FilterAllowed returns a Baggage subset containing only keys listed in
// `allowed`. Used for the BaggageAllowlist propagation mode. nil `allowed`
// returns the original map (callers should branch on propagation mode before
// calling).
func FilterAllowed(b Baggage, allowed []string) Baggage {
	if len(b) == 0 {
		return nil
	}
	if len(allowed) == 0 {
		return nil
	}
	out := make(Baggage, len(allowed))
	for _, k := range allowed {
		k = strings.ToLower(strings.TrimSpace(k))
		if k == "" {
			continue
		}
		if v, ok := b[k]; ok {
			out[k] = v
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// ---- Context plumbing -------------------------------------------------------
//
// The plugin's HandleUsage receives a context.Context per record. We stash
// the parsed baggage on the context from the middleware so HandleUsage can
// pull it without re-parsing the header (or knowing about the Gin handler
// chain at all).

type baggageContextKey struct{}

// WithBaggage returns a context with the parsed baggage attached. Safe to
// call with a nil parent context.
func WithBaggage(ctx context.Context, b Baggage) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if len(b) == 0 {
		return ctx
	}
	return context.WithValue(ctx, baggageContextKey{}, b)
}

// BaggageFromContext returns the baggage stored on ctx, or nil when none.
func BaggageFromContext(ctx context.Context) Baggage {
	if ctx == nil {
		return nil
	}
	raw := ctx.Value(baggageContextKey{})
	if raw == nil {
		return nil
	}
	b, _ := raw.(Baggage)
	return b
}
