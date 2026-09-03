package keepalive

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/tidwall/gjson"
)

// fakeTimer records the requested delay and fires only when the test says so.
type fakeTimer struct {
	clock   *fakeClock
	delay   time.Duration
	fire    func()
	stopped bool
}

func (t *fakeTimer) Stop() bool {
	t.clock.mu.Lock()
	defer t.clock.mu.Unlock()
	already := t.stopped
	t.stopped = true
	return !already
}

type fakeClock struct {
	mu     sync.Mutex
	timers []*fakeTimer
}

func (c *fakeClock) after(delay time.Duration, fire func()) Timer {
	c.mu.Lock()
	defer c.mu.Unlock()
	timer := &fakeTimer{clock: c, delay: delay, fire: fire}
	c.timers = append(c.timers, timer)
	return timer
}

// fireLatest runs the most recently scheduled timer that is still armed.
func (c *fakeClock) fireLatest(t *testing.T) {
	t.Helper()
	c.mu.Lock()
	var target *fakeTimer
	for index := len(c.timers) - 1; index >= 0; index-- {
		if !c.timers[index].stopped {
			target = c.timers[index]
			break
		}
	}
	c.mu.Unlock()
	if target == nil {
		t.Fatalf("no armed timer to fire")
	}
	target.fire()
}

// fireAt runs the timer scheduled at the given index regardless of supersession.
func (c *fakeClock) fireAt(t *testing.T, index int) {
	t.Helper()
	c.mu.Lock()
	if index >= len(c.timers) {
		c.mu.Unlock()
		t.Fatalf("timer %d not scheduled (have %d)", index, len(c.timers))
	}
	target := c.timers[index]
	c.mu.Unlock()
	target.fire()
}

func (c *fakeClock) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.timers)
}

type recordingProber struct {
	mu       sync.Mutex
	requests []ProbeRequest
	result   ProbeResult
	err      error
}

func (p *recordingProber) Probe(_ context.Context, req ProbeRequest) (ProbeResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.requests = append(p.requests, req)
	return p.result, p.err
}

func (p *recordingProber) calls() []ProbeRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]ProbeRequest, len(p.requests))
	copy(out, p.requests)
	return out
}

type staticLiveness struct{ live bool }

func (l staticLiveness) Live(string, time.Duration) bool { return l.live }

type staticBinding struct {
	authID string
	state  BindingState
}

func (b staticBinding) SessionBinding(string, string, string) (string, BindingState) {
	return b.authID, b.state
}

const oneHourBody = `{
  "model": "claude-haiku-4-5-20251001",
  "max_tokens": 32000,
  "stream": true,
  "thinking": {"type": "enabled", "budget_tokens": 4000},
  "system": [{"type": "text", "text": "sys", "cache_control": {"type": "ephemeral", "ttl": "1h"}}],
  "tools": [{"name": "Bash", "cache_control": {"type": "ephemeral", "ttl": "1h"}}],
  "messages": [{"role": "user", "content": [{"type": "text", "text": "hi", "cache_control": {"type": "ephemeral", "ttl": "1h"}}]}],
  "metadata": {"user_id": "{\"session_id\":\"sess-1\"}"}
}`

const fiveMinuteBody = `{
  "model": "claude-haiku-4-5-20251001",
  "max_tokens": 100,
  "system": [{"type": "text", "text": "sys", "cache_control": {"type": "ephemeral", "ttl": "5m"}}],
  "messages": [{"role": "user", "content": "hi"}]
}`

func testScheduler(t *testing.T, clock *fakeClock, prober Prober, liveness Liveness, binding Binding, mutate func(*Config)) *Scheduler {
	t.Helper()
	cfg := Config{
		Enabled:              true,
		BeforeExpiry:         5 * time.Minute,
		OnlyWhenAgentsActive: true,
		MaxProbes:            6,
		MaxTokens:            1,
	}
	if mutate != nil {
		mutate(&cfg)
	}
	scheduler := New(cfg)
	scheduler.SetProber(prober)
	scheduler.SetLiveness(liveness)
	scheduler.SetBinding(binding)
	scheduler.SetTimerFactory(clock.after)
	return scheduler
}

func observeOneHour(scheduler *Scheduler, session string) {
	scheduler.Observe(ObserveInput{
		SessionID:        session,
		BindingSessionID: "claude:" + session + ":agent:main",
		AuthID:           "auth-a",
		Provider:         "claude",
		Model:            "claude-haiku-4-5-20251001",
		Body:             []byte(oneHourBody),
		Headers:          http.Header{"Anthropic-Beta": []string{"claude-code-20250219,extended-cache-ttl-2025-04-11"}},
		TTL:              time.Hour,
		StartedAt:        time.Now(),
	})
}

func TestObserveSchedulesAtTTLMinusBeforeExpiry(t *testing.T) {
	clock := &fakeClock{}
	prober := &recordingProber{}
	scheduler := testScheduler(t, clock, prober, staticLiveness{live: true}, staticBinding{authID: "auth-a", state: BindingBound}, nil)

	observeOneHour(scheduler, "sess-1")

	if clock.count() != 1 {
		t.Fatalf("scheduled %d timers, want 1", clock.count())
	}
	// The delay is measured from the observed request's start, so it lands just
	// under the nominal window.
	if got := clock.timers[0].delay; got > 55*time.Minute || got < 54*time.Minute {
		t.Fatalf("delay = %s, want ~55m", got)
	}
}

func TestObserveSupersedesEarlierTimer(t *testing.T) {
	clock := &fakeClock{}
	prober := &recordingProber{}
	scheduler := testScheduler(t, clock, prober, staticLiveness{live: true}, staticBinding{authID: "auth-a", state: BindingBound}, nil)

	observeOneHour(scheduler, "sess-1")
	observeOneHour(scheduler, "sess-1")

	if clock.count() != 2 {
		t.Fatalf("scheduled %d timers, want 2", clock.count())
	}
	if !clock.timers[0].stopped {
		t.Fatalf("first timer was not stopped by the superseding Observe")
	}

	// Firing the stale timer must not probe.
	clock.fireAt(t, 0)
	if calls := prober.calls(); len(calls) != 0 {
		t.Fatalf("superseded timer probed %d times, want 0", len(calls))
	}

	// The live timer still probes.
	clock.fireAt(t, 1)
	if calls := prober.calls(); len(calls) != 1 {
		t.Fatalf("live timer probed %d times, want 1", len(calls))
	}
}

func TestFireSkipsWhenNoAgentsLive(t *testing.T) {
	clock := &fakeClock{}
	prober := &recordingProber{}
	scheduler := testScheduler(t, clock, prober, staticLiveness{live: false}, staticBinding{authID: "auth-a", state: BindingBound}, nil)

	observeOneHour(scheduler, "sess-1")
	clock.fireLatest(t)

	if calls := prober.calls(); len(calls) != 0 {
		t.Fatalf("probed %d times with no live agents, want 0", len(calls))
	}
	if clock.count() != 1 {
		t.Fatalf("rescheduled after a liveness skip; timers = %d, want 1", clock.count())
	}
}

func TestFireProbesWhenLivenessIsAlwaysEvenWithNoAgents(t *testing.T) {
	clock := &fakeClock{}
	prober := &recordingProber{}
	scheduler := testScheduler(t, clock, prober, staticLiveness{live: false}, staticBinding{authID: "auth-a", state: BindingBound}, func(cfg *Config) {
		cfg.OnlyWhenAgentsActive = false
	})

	observeOneHour(scheduler, "sess-1")
	clock.fireLatest(t)

	if calls := prober.calls(); len(calls) != 1 {
		t.Fatalf("probed %d times, want 1", len(calls))
	}
}

func TestFireSkipsBindingCheckWithoutBindingSessionID(t *testing.T) {
	clock := &fakeClock{}
	prober := &recordingProber{}
	scheduler := testScheduler(t, clock, prober, staticLiveness{live: true}, staticBinding{state: BindingLost}, nil)

	scheduler.Observe(ObserveInput{
		SessionID: "sess-1",
		AuthID:    "auth-a",
		Provider:  "claude",
		Model:     "claude-haiku-4-5-20251001",
		Body:      []byte(oneHourBody),
		Headers:   http.Header{},
		TTL:       time.Hour,
		StartedAt: time.Now(),
	})
	clock.fireLatest(t)

	if calls := prober.calls(); len(calls) != 1 {
		t.Fatalf("probed %d times, want 1: with no binding identity there is nothing to check", len(calls))
	}
}

func TestFireProbesWhenBindingIsUnknown(t *testing.T) {
	clock := &fakeClock{}
	prober := &recordingProber{}
	scheduler := testScheduler(t, clock, prober, staticLiveness{live: true}, staticBinding{state: BindingUnknown}, nil)

	observeOneHour(scheduler, "sess-1")
	clock.fireLatest(t)

	if calls := prober.calls(); len(calls) != 1 {
		t.Fatalf("probed %d times, want 1: without session-sticky routing there is no binding to lose", len(calls))
	}
}

func TestFireSkipsWhenAuthBindingLost(t *testing.T) {
	clock := &fakeClock{}
	prober := &recordingProber{}
	scheduler := testScheduler(t, clock, prober, staticLiveness{live: true}, staticBinding{state: BindingLost}, nil)

	observeOneHour(scheduler, "sess-1")
	clock.fireLatest(t)

	if calls := prober.calls(); len(calls) != 0 {
		t.Fatalf("probed %d times with no binding, want 0", len(calls))
	}
}

func TestFireSkipsWhenAuthBindingMovedToAnotherCredential(t *testing.T) {
	clock := &fakeClock{}
	prober := &recordingProber{}
	scheduler := testScheduler(t, clock, prober, staticLiveness{live: true}, staticBinding{authID: "auth-b", state: BindingBound}, nil)

	observeOneHour(scheduler, "sess-1")
	clock.fireLatest(t)

	if calls := prober.calls(); len(calls) != 0 {
		t.Fatalf("probed %d times after the session rebound to another auth, want 0", len(calls))
	}
}

func TestConsecutiveProbesStopAtMaxProbes(t *testing.T) {
	clock := &fakeClock{}
	prober := &recordingProber{}
	scheduler := testScheduler(t, clock, prober, staticLiveness{live: true}, staticBinding{authID: "auth-a", state: BindingBound}, func(cfg *Config) {
		cfg.MaxProbes = 3
	})

	observeOneHour(scheduler, "sess-1")
	for range 6 {
		if clock.count() == 0 {
			break
		}
		armed := false
		clock.mu.Lock()
		for index := len(clock.timers) - 1; index >= 0; index-- {
			if !clock.timers[index].stopped {
				armed = true
				break
			}
		}
		clock.mu.Unlock()
		if !armed {
			break
		}
		clock.fireLatest(t)
	}

	if calls := prober.calls(); len(calls) != 3 {
		t.Fatalf("probed %d times, want max-probes = 3", len(calls))
	}
}

func TestRealRequestResetsProbeBudget(t *testing.T) {
	clock := &fakeClock{}
	prober := &recordingProber{}
	scheduler := testScheduler(t, clock, prober, staticLiveness{live: true}, staticBinding{authID: "auth-a", state: BindingBound}, func(cfg *Config) {
		cfg.MaxProbes = 1
	})

	observeOneHour(scheduler, "sess-1")
	clock.fireLatest(t)
	observeOneHour(scheduler, "sess-1")
	clock.fireLatest(t)

	if calls := prober.calls(); len(calls) != 2 {
		t.Fatalf("probed %d times, want 2 (the real request resets the budget)", len(calls))
	}
}

func TestProbeBodyPreservesCacheControlBetasAndPrefix(t *testing.T) {
	clock := &fakeClock{}
	prober := &recordingProber{}
	scheduler := testScheduler(t, clock, prober, staticLiveness{live: true}, staticBinding{authID: "auth-a", state: BindingBound}, nil)

	observeOneHour(scheduler, "sess-1")
	clock.fireLatest(t)

	calls := prober.calls()
	if len(calls) != 1 {
		t.Fatalf("probed %d times, want 1", len(calls))
	}
	probe := calls[0]
	body := probe.Body

	if got := gjson.GetBytes(body, "max_tokens").Int(); got != 1 {
		t.Fatalf("max_tokens = %d, want 1", got)
	}
	if got := gjson.GetBytes(body, "stream"); !got.Exists() || got.Bool() {
		t.Fatalf("stream = %v, want false", got.Raw)
	}
	if gjson.GetBytes(body, "thinking").Exists() {
		t.Fatalf("thinking survived into the probe body")
	}
	for _, path := range []string{
		"system.0.cache_control.ttl",
		"tools.0.cache_control.ttl",
		"messages.0.content.0.cache_control.ttl",
	} {
		if got := gjson.GetBytes(body, path).String(); got != "1h" {
			t.Fatalf("%s = %q, want 1h", path, got)
		}
	}
	if got := gjson.GetBytes(body, "system.0.text").String(); got != "sys" {
		t.Fatalf("system text rewritten: %q", got)
	}
	if got := gjson.GetBytes(body, "messages.0.content.0.text").String(); got != "hi" {
		t.Fatalf("message text rewritten: %q", got)
	}
	if got := gjson.GetBytes(body, "metadata.user_id").String(); got == "" {
		t.Fatalf("metadata.user_id dropped from the probe body")
	}
	if got := probe.Headers.Get("Anthropic-Beta"); got != "claude-code-20250219,extended-cache-ttl-2025-04-11" {
		t.Fatalf("Anthropic-Beta = %q, want the observed betas verbatim", got)
	}
	if probe.AuthID != "auth-a" {
		t.Fatalf("AuthID = %q, want auth-a", probe.AuthID)
	}
	if probe.Model != "claude-haiku-4-5-20251001" {
		t.Fatalf("Model = %q", probe.Model)
	}
}

func TestDisabledSchedulerNeverSchedules(t *testing.T) {
	clock := &fakeClock{}
	prober := &recordingProber{}
	scheduler := testScheduler(t, clock, prober, staticLiveness{live: true}, staticBinding{authID: "auth-a", state: BindingBound}, func(cfg *Config) {
		cfg.Enabled = false
	})

	observeOneHour(scheduler, "sess-1")

	if clock.count() != 0 {
		t.Fatalf("scheduled %d timers while disabled, want 0", clock.count())
	}
}

func TestNilSchedulerObserveIsSafe(t *testing.T) {
	var scheduler *Scheduler
	scheduler.Observe(ObserveInput{SessionID: "sess-1", TTL: time.Hour})
}

func TestObserveIgnoredWithoutSessionOrTTL(t *testing.T) {
	clock := &fakeClock{}
	prober := &recordingProber{}
	scheduler := testScheduler(t, clock, prober, staticLiveness{live: true}, staticBinding{authID: "auth-a", state: BindingBound}, nil)

	scheduler.Observe(ObserveInput{AuthID: "auth-a", Body: []byte(oneHourBody), TTL: time.Hour})
	scheduler.Observe(ObserveInput{SessionID: "sess-1", AuthID: "auth-a", Body: []byte(oneHourBody)})
	scheduler.Observe(ObserveInput{SessionID: "sess-1", Body: []byte(oneHourBody), TTL: time.Hour})

	if clock.count() != 0 {
		t.Fatalf("scheduled %d timers for incomplete observations, want 0", clock.count())
	}
}

func TestObserveIgnoredWhenBeforeExpiryExceedsTTL(t *testing.T) {
	clock := &fakeClock{}
	prober := &recordingProber{}
	scheduler := testScheduler(t, clock, prober, staticLiveness{live: true}, staticBinding{authID: "auth-a", state: BindingBound}, func(cfg *Config) {
		cfg.BeforeExpiry = 2 * time.Hour
	})

	observeOneHour(scheduler, "sess-1")

	if clock.count() != 0 {
		t.Fatalf("scheduled %d timers with before-expiry > ttl, want 0", clock.count())
	}
}

func TestCacheControlTTL(t *testing.T) {
	tests := []struct {
		name string
		body string
		want time.Duration
	}{
		{name: "one hour", body: oneHourBody, want: time.Hour},
		{name: "five minutes only", body: fiveMinuteBody, want: 5 * time.Minute},
		{name: "bare ephemeral has no explicit ttl", body: `{"system":[{"type":"text","cache_control":{"type":"ephemeral"}}]}`, want: 0},
		{name: "no cache control", body: `{"messages":[{"role":"user","content":"hi"}]}`, want: 0},
		{name: "nested one hour", body: `{"messages":[{"content":[{"cache_control":{"ttl":"1h"}}]}]}`, want: time.Hour},
		{name: "invalid json", body: `not json`, want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ExtendedCacheTTL([]byte(tt.body)); got != tt.want {
				t.Fatalf("ExtendedCacheTTL() = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestIsExtendedCacheTTL(t *testing.T) {
	if IsExtendedCacheTTL(ExtendedCacheTTL([]byte(fiveMinuteBody))) {
		t.Fatalf("a 5m-only body must not qualify for keepalive")
	}
	if !IsExtendedCacheTTL(ExtendedCacheTTL([]byte(oneHourBody))) {
		t.Fatalf("a 1h body must qualify for keepalive")
	}
	if IsExtendedCacheTTL(ExtendedCacheTTL([]byte(`{"messages":[]}`))) {
		t.Fatalf("a body with no cache_control must not qualify for keepalive")
	}
}

func TestStopClearsScheduledTimers(t *testing.T) {
	clock := &fakeClock{}
	prober := &recordingProber{}
	scheduler := testScheduler(t, clock, prober, staticLiveness{live: true}, staticBinding{authID: "auth-a", state: BindingBound}, nil)

	observeOneHour(scheduler, "sess-1")
	scheduler.Stop()

	if !clock.timers[0].stopped {
		t.Fatalf("Stop() left a timer armed")
	}
	clock.fireAt(t, 0)
	if calls := prober.calls(); len(calls) != 0 {
		t.Fatalf("probed after Stop(), want 0 probes")
	}
}
