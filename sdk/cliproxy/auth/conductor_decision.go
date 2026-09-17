package auth

import (
	"net/http"
	"strings"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	log "github.com/sirupsen/logrus"
)

// breakerStateForAuth returns the auth's pool breaker state as a stable
// string suitable for the X-NixLLM-Decision header. Empty/nil auth or a
// pool that did not opt into circuit breaking returns "n/a" so the field
// is always present. A pool that opted in but has no recorded breaker
// state yet (no failures observed) reports "closed" so operators see the
// field change when the breaker later trips.
func breakerStateForAuth(auth *Auth) string {
	if auth == nil {
		return "n/a"
	}
	key := poolBreakerSelectionKey(auth)
	if key == "" {
		return "n/a"
	}
	for _, record := range PoolBreakerSnapshot() {
		if record.PoolKey == key {
			return record.State
		}
	}
	return "closed"
}

// poolStrategyAttrForAuth returns the auth's pool routing strategy in the
// canonical header vocabulary. Auths whose pool opted into pool-level
// routing get their strategy verbatim (e.g. "attr", "fallback",
// "compound"); everything else returns "n/a" so the field is always
// present.
func poolStrategyAttrForAuth(auth *Auth) string {
	if auth == nil {
		return "n/a"
	}
	strategy := strings.TrimSpace(poolStrategyFromAuth(auth))
	if strategy == "" {
		return "n/a"
	}
	return strategy
}

// decisionFor builds a Decision from the dispatch context. Populates every
// field that can be read synchronously from the Manager's auth/scheduler
// state at the moment of a successful dispatch; fields the dispatch site
// cannot observe (e.g. accumulated cooldown waits across retry cycles, the
// per-attempt strategy override) fall back to "n/a" so the header stays
// self-describing. The Manager receiver tolerates nil for tests and
// defensive callers — empty Manager yields an all-"n/a" Decision.
func (m *Manager) decisionFor(auth *Auth, provider, model string, attempts int) Decision {
	d := Decision{
		Model:        model,
		AuthID:       strings.TrimSpace(authIDOf(auth)),
		Channel:      strings.TrimSpace(provider),
		Attempts:     attempts,
		PoolStrategy: poolStrategyAttrForAuth(auth),
		Breaker:      breakerStateForAuth(auth),
	}
	if auth == nil {
		return d
	}
	if m != nil && m.scheduler != nil {
		d.Strategy = m.scheduler.StrategyName()
		d.HeadroomExhausted = m.scheduler.HeadroomExhausted()
		if pct := m.scheduler.HeadroomPercent(strings.TrimSpace(auth.ID)); pct != "" {
			d.QuotaHeadroom = pct
		}
	}
	return d
}

// authIDOf returns the auth's ID, tolerating nil.
func authIDOf(auth *Auth) string {
	if auth == nil {
		return ""
	}
	return auth.ID
}

// setResponseDecisionHeader writes the decision header onto a non-streaming
// Response.Headers map. Lazy-allocates the map when nil so callers do not
// have to pre-allocate. Failures (oversized value, etc.) log a warning with
// the proposed value truncated to 200 chars per the round-2 design doc; the
// response is never broken by header-set failures.
func setResponseDecisionHeader(resp *cliproxyexecutor.Response, d Decision) {
	if resp == nil {
		return
	}
	if resp.Headers == nil {
		resp.Headers = http.Header{}
	}
	value := d.Header()
	resp.Headers.Set(DecisionHeader, value)
	if len(resp.Headers.Get(DecisionHeader)) != len(value) {
		log.Warnf("failed to set %s header: oversized value (truncated to 200): %.200s", DecisionHeader, value)
	}
}

// setStreamDecisionHeader writes the decision header onto a streaming
// response's Headers map before the first chunk is forwarded. Same
// defensive semantics as setResponseDecisionHeader. The API layer
// (handlers_stream.go) reads StreamResult.Headers and propagates them
// into the downstream response, so writing here surfaces the header to
// streaming clients as soon as the first byte is ready.
func setStreamDecisionHeader(headers http.Header, d Decision) {
	if headers == nil {
		return
	}
	value := d.Header()
	headers.Set(DecisionHeader, value)
	if len(headers.Get(DecisionHeader)) != len(value) {
		log.Warnf("failed to set %s header: oversized value (truncated to 200): %.200s", DecisionHeader, value)
	}
}
