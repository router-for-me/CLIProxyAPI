package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/events"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

// withEventRing installs a fresh events.Ring as the global recorder for the
// duration of a test, returning the ring and a cleanup that restores the
// previous recorder. Tests use a real ring rather than a fake Recorder so
// the events flow through the same Record/Snapshot path production uses.
// The "previous ring" is events.Global()'s current value, which is a
// zero-value Ring (cap==0, Record no-op) when SetGlobal has never been
// called in this process — perfectly safe to re-install on cleanup.
func withEventRing(t *testing.T) *events.Ring {
	t.Helper()
	ring := events.NewRing(200)
	prev := events.Global()
	events.SetGlobal(ring)
	t.Cleanup(func() {
		events.SetGlobal(prev)
	})
	return ring
}

// routingDecisionRecorder captures the auth/model the conductor picked so
// the assertion side can read the routing.decision event.
type routingDecisionRecorder struct {
	id string
}

// Identifier returns the provider key handled by this executor.
func (r *routingDecisionRecorder) Identifier() string { return r.id }

// Execute returns a successful response carrying a tiny payload so the
// conductor's success path fires.
func (r *routingDecisionRecorder) Execute(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{Payload: []byte("ok")}, nil
}

func (r *routingDecisionRecorder) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, &Error{HTTPStatus: http.StatusNotImplemented, Message: "ExecuteStream not implemented"}
}

func (r *routingDecisionRecorder) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (r *routingDecisionRecorder) CountTokens(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return r.Execute(ctx, auth, req, opts)
}

func (r *routingDecisionRecorder) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, &Error{HTTPStatus: http.StatusNotImplemented, Message: "HttpRequest not implemented"}
}

// findRoutingDecision locates the first routing.decision event in the ring
// matching model. Returns nil if none is present.
func findRoutingDecision(snap []events.Event, model string) *events.Event {
	for i := range snap {
		if snap[i].Type == "routing.decision" && snap[i].Model == model {
			return &snap[i]
		}
	}
	return nil
}

// TestEmitRoutingDecisionFiresOnSuccess verifies that a successful dispatch
// records exactly one routing.decision event with the picked auth's ID,
// channel, and a Decision payload.
func TestEmitRoutingDecisionFiresOnSuccess(t *testing.T) {
	ring := withEventRing(t)

	// Fix the recorded-at clock so Snapshot timestamps are deterministic.
	fixed := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	restoreClock := setRecordedAtForTest(func() time.Time { return fixed })
	t.Cleanup(restoreClock)

	const (
		providerName = "routing-decision-test-provider"
		authID       = "routing-decision-auth"
		model        = "routing-decision-model"
	)
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(authID, providerName, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { reg.UnregisterClient(authID) })

	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	rec := &routingDecisionRecorder{id: providerName}
	manager.RegisterExecutor(rec)
	auth := &Auth{
		ID:       authID,
		Provider: providerName,
		Status:   StatusActive,
	}
	if _, errReg := manager.Register(context.Background(), auth); errReg != nil {
		t.Fatalf("Register() error = %v", errReg)
	}

	resp, errExec := manager.Execute(context.Background(), []string{providerName},
		cliproxyexecutor.Request{Model: model},
		cliproxyexecutor.Options{
			Metadata: map[string]any{
				cliproxyexecutor.PinnedAuthMetadataKey: authID,
			},
		})
	if errExec != nil {
		t.Fatalf("Execute() error = %v", errExec)
	}
	if string(resp.Payload) != "ok" {
		t.Fatalf("Execute() payload = %q, want %q", string(resp.Payload), "ok")
	}

	snap := ring.Snapshot()
	ev := findRoutingDecision(snap, model)
	if ev == nil {
		t.Fatalf("routing.decision event not recorded; got events=%+v", snap)
	}
	if ev.AuthID != authID {
		t.Errorf("routing.decision AuthID = %q, want %q", ev.AuthID, authID)
	}
	if ev.Channel != providerName {
		t.Errorf("routing.decision Channel = %q, want %q", ev.Channel, providerName)
	}
	if len(ev.Payload) == 0 {
		t.Fatal("routing.decision payload is empty; want Decision JSON")
	}
	var d Decision
	if err := json.Unmarshal(ev.Payload, &d); err != nil {
		t.Fatalf("routing.decision payload unmarshal: %v; payload=%s", err, ev.Payload)
	}
	if d.AuthID != authID {
		t.Errorf("Decision.AuthID = %q, want %q", d.AuthID, authID)
	}
	if d.Model != model {
		t.Errorf("Decision.Model = %q, want %q", d.Model, model)
	}
	if !ev.Ts.Equal(fixed) {
		t.Errorf("Event.Ts = %v, want %v", ev.Ts, fixed)
	}
}

// TestEmitRoutingDecisionSkippedOnNilAuth is a defensive guard: the helper
// must not panic if the dispatch site accidentally passes a nil auth (a
// regression guard, not a production scenario).
func TestEmitRoutingDecisionSkippedOnNilAuth(t *testing.T) {
	ring := withEventRing(t)
	emitRoutingDecision("", "model", nil, Decision{})
	snap := ring.Snapshot()
	for _, ev := range snap {
		if ev.Type == "routing.decision" {
			t.Fatalf("routing.decision should not be recorded for nil auth, got %+v", ev)
		}
	}
}

// TestEmitCooldownWaitSkippedOnZeroMs ensures the helper does not record an
// event for a zero-wait cooldown path (the conductor returns immediately
// when wait <= 0 and never logs an event in that case).
func TestEmitCooldownWaitSkippedOnZeroMs(t *testing.T) {
	ring := withEventRing(t)
	emitCooldownWait("req-1", "model", "auth", 0)
	emitCooldownWait("req-2", "model", "auth", -5)
	snap := ring.Snapshot()
	if len(snap) != 0 {
		t.Fatalf("zero-wait emissions must be a no-op; got %d events", len(snap))
	}
}

// TestEmitCooldownWaitEmitsPositiveWait ensures the helper records one
// event with the supplied wait duration.
func TestEmitCooldownWaitEmitsPositiveWait(t *testing.T) {
	ring := withEventRing(t)
	emitCooldownWait("req-1", "model", "auth-1", 1500)
	snap := ring.Snapshot()
	if len(snap) != 1 {
		t.Fatalf("want 1 event, got %d", len(snap))
	}
	ev := snap[0]
	if ev.Type != "routing.cooldown_wait" {
		t.Errorf("type = %q, want routing.cooldown_wait", ev.Type)
	}
	if ev.RequestID != "req-1" || ev.Model != "model" || ev.AuthID != "auth-1" {
		t.Errorf("event metadata mismatch: %+v", ev)
	}
	var payload map[string]any
	if err := json.Unmarshal(ev.Payload, &payload); err != nil {
		t.Fatalf("payload unmarshal: %v", err)
	}
	if got := int64(payload["waited_ms"].(float64)); got != 1500 {
		t.Errorf("payload.waited_ms = %v, want 1500", payload["waited_ms"])
	}
}

// TestEmitAttemptsExhaustedEmits ensures the helper emits the expected
// payload shape for the attempts-exhausted path.
func TestEmitAttemptsExhaustedEmits(t *testing.T) {
	ring := withEventRing(t)
	emitAttemptsExhausted("model-1", "auth-last", 3)
	snap := ring.Snapshot()
	if len(snap) != 1 {
		t.Fatalf("want 1 event, got %d", len(snap))
	}
	ev := snap[0]
	if ev.Type != "routing.attempts_exhausted" {
		t.Errorf("type = %q, want routing.attempts_exhausted", ev.Type)
	}
	if ev.Model != "model-1" || ev.AuthID != "auth-last" {
		t.Errorf("event metadata mismatch: %+v", ev)
	}
	var payload map[string]any
	if err := json.Unmarshal(ev.Payload, &payload); err != nil {
		t.Fatalf("payload unmarshal: %v", err)
	}
	if got := int(payload["attempts"].(float64)); got != 3 {
		t.Errorf("payload.attempts = %v, want 3", payload["attempts"])
	}
}

// TestEmitBreakerTrippedEmits ensures the helper emits the expected
// payload shape for the breaker-tripped path.
func TestEmitBreakerTrippedEmits(t *testing.T) {
	ring := withEventRing(t)
	emitBreakerTripped("pool-a", 8)
	snap := ring.Snapshot()
	if len(snap) != 1 {
		t.Fatalf("want 1 event, got %d", len(snap))
	}
	ev := snap[0]
	if ev.Type != "breaker.tripped" {
		t.Errorf("type = %q, want breaker.tripped", ev.Type)
	}
	if ev.AuthID != "pool-a" {
		t.Errorf("AuthID = %q, want pool-a", ev.AuthID)
	}
	var payload map[string]any
	if err := json.Unmarshal(ev.Payload, &payload); err != nil {
		t.Fatalf("payload unmarshal: %v", err)
	}
	if payload["pool_key"] != "pool-a" {
		t.Errorf("payload.pool_key = %v, want pool-a", payload["pool_key"])
	}
	if got := int(payload["failures"].(float64)); got != 8 {
		t.Errorf("payload.failures = %v, want 8", payload["failures"])
	}
}

// TestEmitBreakerProbeVerdictEmits covers both verdicts in one test to
// pin the type spelling (probe_ok vs probe_fail).
func TestEmitBreakerProbeVerdictEmits(t *testing.T) {
	cases := []struct {
		name string
		ok   bool
		want string
	}{
		{name: "ok", ok: true, want: "breaker.probe_ok"},
		{name: "fail", ok: false, want: "breaker.probe_fail"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ring := withEventRing(t)
			emitBreakerProbeVerdict("pool-b", tc.ok)
			snap := ring.Snapshot()
			if len(snap) != 1 {
				t.Fatalf("want 1 event, got %d", len(snap))
			}
			if snap[0].Type != tc.want {
				t.Errorf("type = %q, want %q", snap[0].Type, tc.want)
			}
		})
	}
}

// TestEmitCooldownReclassifiedEmits ensures the helper emits the expected
// payload shape for the 403→429 reclassification event.
func TestEmitCooldownReclassifiedEmits(t *testing.T) {
	ring := withEventRing(t)
	emitCooldownReclassified("auth-r")
	snap := ring.Snapshot()
	if len(snap) != 1 {
		t.Fatalf("want 1 event, got %d", len(snap))
	}
	ev := snap[0]
	if ev.Type != "cooldown.reclassified" {
		t.Errorf("type = %q, want cooldown.reclassified", ev.Type)
	}
	if ev.AuthID != "auth-r" {
		t.Errorf("AuthID = %q, want auth-r", ev.AuthID)
	}
	var payload map[string]any
	if err := json.Unmarshal(ev.Payload, &payload); err != nil {
		t.Fatalf("payload unmarshal: %v", err)
	}
	if payload["from"] != "403" || payload["to"] != "429" {
		t.Errorf("payload from/to mismatch: %+v", payload)
	}
}

// TestMarshalEventPayloadNilSafe verifies the helper returns nil for a nil
// value and never panics on a non-marshalable type.
func TestMarshalEventPayloadNilSafe(t *testing.T) {
	if got := marshalEventPayload(nil); got != nil {
		t.Errorf("nil input should yield nil payload, got %s", got)
	}
	if got := marshalEventPayload(make(chan int)); got != nil {
		t.Errorf("non-marshalable input should yield nil payload, got %s", got)
	}
}
