package auth

import (
	"encoding/json"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/events"
)

// eventsPackage is a tiny adapter so the auth package's emission sites can
// call events.Global() without importing internal/events at every call site.
// Kept private — production code should keep using events.Global().Record
// directly when the import is already in scope; this helper exists so the
// emission sites introduced in round-2 Task 12 stay grep-friendly and the
// payload-marshal convention is centralized.
var eventsPackage = struct {
	Global func() *events.Ring
}{
	Global: events.Global,
}

// recordedAtTime is the time source used to stamp the events emitted from
// the auth package. Tests can override it via setRecordedAtForTest; default
// delegates to time.Now so production always sees a fresh wall-clock stamp.
var recordedAtTime = time.Now

// setRecordedAtForTest swaps the time source used to stamp emitted events.
// Restored by t.Cleanup via a returned function. Tests use a fixed clock so
// Snapshot() is deterministic.
func setRecordedAtForTest(now func() time.Time) func() {
	prev := recordedAtTime
	recordedAtTime = now
	return func() { recordedAtTime = prev }
}

// marshalEventPayload encodes v as a json.RawMessage suitable for the
// Event.Payload field. A nil v (or a value that fails to marshal) yields a
// nil RawMessage so the Event still records without a payload — the
// "routing.decision" emission always carries a Decision body, but the
// cooldown/breaker emission sites pass tiny literal maps that should always
// succeed; a panic-free fallback keeps a marshal bug from killing the call.
func marshalEventPayload(v any) json.RawMessage {
	if v == nil {
		return nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return b
}

// emitRoutingDecision records a routing.decision event for a successful
// dispatch. Called from the conductor's Execute/ExecuteStream success path,
// immediately after setResponseDecisionHeader / setStreamDecisionHeader.
// The Decision struct is serialized verbatim so the event payload and the
// X-NixLLM-Decision header always agree on field values.
func emitRoutingDecision(reqID, model string, auth *Auth, decision Decision) {
	if auth == nil {
		return
	}
	eventsPackage.Global().Record(events.Event{
		Type:      "routing.decision",
		Ts:        recordedAtTime(),
		RequestID: reqID,
		Model:     model,
		AuthID:    auth.ID,
		Channel:   auth.Provider,
		Payload:   marshalEventPayload(decision),
	})
}

// emitCooldownWait records a routing.cooldown_wait event when the conductor
// actually blocked on a cooldown (wait > 0). The model and provider list
// come from the retry context; the authID is optional because the wait can
// pre-date a pick attempt.
func emitCooldownWait(reqID, model, authID string, waitedMs int64) {
	if waitedMs <= 0 {
		return
	}
	eventsPackage.Global().Record(events.Event{
		Type:      "routing.cooldown_wait",
		Ts:        recordedAtTime(),
		RequestID: reqID,
		Model:     model,
		AuthID:    authID,
		Payload: marshalEventPayload(map[string]any{
			"waited_ms": waitedMs,
		}),
	})
}

// emitAttemptsExhausted records a routing.attempts_exhausted event when the
// retry loop gives up. attempts is the 1-based count of credentials tried
// before the loop gave up; lastAuthID is the final auth that failed.
func emitAttemptsExhausted(model, lastAuthID string, attempts int) {
	eventsPackage.Global().Record(events.Event{
		Type:   "routing.attempts_exhausted",
		Ts:     recordedAtTime(),
		Model:  model,
		AuthID: lastAuthID,
		Payload: marshalEventPayload(map[string]any{
			"attempts": attempts,
		}),
	})
}

// emitBreakerTripped records a breaker.tripped event when a pool breaker
// transitions CLOSED → OPEN. failures is the failure count that crossed
// the threshold.
func emitBreakerTripped(poolKey string, failures int) {
	eventsPackage.Global().Record(events.Event{
		Type:   "breaker.tripped",
		Ts:     recordedAtTime(),
		AuthID: poolKey,
		Payload: marshalEventPayload(map[string]any{
			"pool_key": poolKey,
			"failures": failures,
		}),
	})
}

// emitBreakerProbeVerdict records a breaker.probe_ok or breaker.probe_fail
// event when the pool breaker's HALF_OPEN probe gets a verdict from a real
// dispatch. ok=true → breaker.probe_ok, ok=false → breaker.probe_fail.
func emitBreakerProbeVerdict(poolKey string, ok bool) {
	kind := "breaker.probe_fail"
	if ok {
		kind = "breaker.probe_ok"
	}
	eventsPackage.Global().Record(events.Event{
		Type:   kind,
		Ts:     recordedAtTime(),
		AuthID: poolKey,
		Payload: marshalEventPayload(map[string]any{
			"pool_key": poolKey,
		}),
	})
}

// emitCooldownReclassified records a cooldown.reclassified event when the
// 403→429 reclassifier (G6) matched a quota-shaped forbidden error.
func emitCooldownReclassified(authID string) {
	eventsPackage.Global().Record(events.Event{
		Type:   "cooldown.reclassified",
		Ts:     recordedAtTime(),
		AuthID: authID,
		Payload: marshalEventPayload(map[string]any{
			"from": "403",
			"to":   "429",
		}),
	})
}
