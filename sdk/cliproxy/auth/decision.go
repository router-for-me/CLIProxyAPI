package auth

import (
	"strconv"
	"strings"
)

// DecisionHeader is the response header carrying routing decision metadata.
// Always on, no opt-in. v=1 prefix for forward compatibility.
const DecisionHeader = "X-NixLLM-Decision"

// decisionHeaderCapBytes is the maximum serialized size of the decision
// header value. The 1024 byte cap fits well below common reverse-proxy header
// limits while leaving room for upstream pass-through headers.
const decisionHeaderCapBytes = 1024

// decisionVersionPrefix is the canonical wire prefix emitted at byte 0 of
// every Decision.Header() output. Truncation logic must always preserve
// this prefix so downstream parsers can rely on its presence — an
// oversized field value must never leave the prefix half-stripped.
const decisionVersionPrefix = "v=1"

// Decision captures the routing decision for a single successful dispatch.
// The header surfaces metadata about which auth handled a request, which
// selector chose it, and any gating state (breaker, headroom, cooldown waits)
// observed at the moment of dispatch. Field values may be empty; the Header
// method substitutes "n/a" so clients can rely on field presence.
type Decision struct {
	// Model is the routing-model resolved for the request (after alias
	// rewriting). Empty falls back to "n/a" in the serialized header.
	Model string
	// AuthID is the stable identifier of the selected auth (auth.ID).
	AuthID string
	// Channel is the executor channel the auth runs on (openai, claude,
	// codex, etc.). Falls back to "n/a" when unset.
	Channel string
	// Strategy is the canonical global selector name used for the pick
	// ("fill-first", "weighted", "headroom", "round-robin", "p2c",
	// "least-used", "weighted-round-robin"). When HeadroomExhausted is true
	// this field is REPLACED in the serialized header with the marker
	// "headroom=exhausted,fallback=least-used".
	Strategy string
	// Breaker is the auth's pool breaker state at dispatch time:
	// "closed", "open", "half_open", or "n/a" when the auth did not opt
	// into pool-level circuit breaking.
	Breaker string
	// CooldownWaitMs is the total millisecond sum of cooldown waits
	// accumulated across retry attempts before a successful dispatch.
	CooldownWaitMs int
	// Attempts is the 1-based count of credential picks consumed before
	// the successful dispatch. A first-try success is 1.
	Attempts int
	// PoolStrategy is the in-pool routing strategy inherited from the
	// auth's upstream row ("attr", "fallback", "compound", or "n/a" when
	// the auth's pool did not opt into pool-level routing).
	PoolStrategy string
	// QuotaHeadroom is the remaining quota percent for the auth at
	// dispatch time (formatted as "%.1f", e.g. "98.8"), or "n/a" when the
	// headroom selector is not in use or no HeadroomLookup is wired.
	QuotaHeadroom string
	// HeadroomExhausted, when true, replaces the Strategy field with the
	// "headroom=exhausted,fallback=least-used" marker so clients see a
	// self-describing signal that the headroom selector fell back to
	// least-used ordering. Sourced from authScheduler.lastHeadroomExhausted
	// (set by the headroom pick path in scheduler.go).
	HeadroomExhausted bool
}

// Header serializes the Decision as a v=1 semicolon-delimited key=value
// string suitable for an HTTP response header. The output is capped at 1024
// bytes and truncated at the last complete field boundary when over the cap.
// All fields are always present; missing values render as "n/a" so clients
// can rely on field presence rather than re-checking key existence.
func (d Decision) Header() string {
	fields := []struct {
		k string
		v string
	}{
		{"v", "1"},
		{"model", naOrValue(d.Model)},
		{"auth", naOrValue(d.AuthID)},
		{"channel", naOrValue(d.Channel)},
		{"strategy", naOrValue(d.Strategy)},
		{"breaker", naOrValue(d.Breaker)},
		{"cooldown_wait_ms", strconv.Itoa(d.CooldownWaitMs)},
		{"attempts", strconv.Itoa(d.Attempts)},
		{"pool_strategy", naOrValue(d.PoolStrategy)},
		{"quota_headroom", naOrValue(d.QuotaHeadroom)},
	}
	if d.HeadroomExhausted {
		// Replace the strategy field with the headroom-exhausted marker so
		// clients see a single self-describing signal without having to
		// combine strategy=headroom with a separate exhausted flag. Index 4
		// is the strategy field above.
		fields[4] = struct {
			k string
			v string
		}{k: "headroom", v: "exhausted,fallback=least-used"}
	}
	var b strings.Builder
	for i, f := range fields {
		if i > 0 {
			b.WriteString("; ")
		}
		b.WriteString(f.k)
		b.WriteString("=")
		b.WriteString(f.v)
	}
	out := b.String()
	if len(out) > decisionHeaderCapBytes {
		// Truncate at the last complete field boundary so we never emit a
		// partial key=value pair, and ALWAYS preserve the "v=1" version
		// prefix so downstream parsers can rely on it. Without the
		// minIdx guard an oversized Model/AuthID field could truncate
		// the header to literally "v=1" (3 bytes, no payload), silently
		// breaking downstream parsers — see
		// TestDecisionHeaderOversizedModelPreservesVersionPrefix.
		out = out[:decisionHeaderCapBytes]
		minIdx := len(decisionVersionPrefix)
		if idx := strings.LastIndex(out, "; "); idx > minIdx {
			out = out[:idx]
		} else if idx >= 0 {
			// Cap fits "v=1" but not a full second field. Drop the partial
			// second field but keep the trailing "; " so the version prefix
			// still parses as "v=1;" (a known marker that no payload
			// fields fit). Clients that expect the prefix can rely on it
			// being emitted verbatim.
			out = out[:minIdx+2]
		} else {
			// No "; " at all — the cap is so tight even "v=1" doesn't fit;
			// fall back to the bare prefix so we never emit an empty
			// string in violation of the contract.
			out = decisionVersionPrefix
		}
	}
	return out
}

// naOrValue returns s when non-empty, otherwise the literal "n/a". Used by
// Decision.Header to keep all fields present in the serialized output even
// when the conductor has no value to emit for them. Whitespace-only inputs
// are treated as empty so callers can pass raw user/executor output without
// pre-trimming and still see a clean "n/a" marker in the wire payload.
func naOrValue(s string) string {
	if strings.TrimSpace(s) == "" {
		return "n/a"
	}
	return s
}
