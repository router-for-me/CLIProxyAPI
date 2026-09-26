package keepalive

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/tidwall/gjson"
)

var errProbeTest = errors.New("upstream refused the probe")

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
		BeforeExpiry5m:       45 * time.Second,
		Probe5m:              Probe5mAuto,
		OnlyWhenAgentsActive: true,
		MaxProbes:            6,
		MaxProbes5m:          30,
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

func observeFiveMinutes(scheduler *Scheduler, session, model string) {
	scheduler.Observe(ObserveInput{
		SessionID:        session,
		BindingSessionID: "claude:" + session + ":agent:main",
		AuthID:           "auth-a",
		Provider:         "claude",
		Model:            model,
		Body:             []byte(fiveMinuteBody),
		Headers:          http.Header{"Anthropic-Beta": []string{"claude-code-20250219"}},
		TTL:              5 * time.Minute,
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

func TestRequestCacheTTLResolvesTheWireDefault(t *testing.T) {
	tests := []struct {
		name string
		body string
		want time.Duration
	}{
		{name: "explicit one hour", body: oneHourBody, want: time.Hour},
		{name: "explicit five minutes", body: fiveMinuteBody, want: 5 * time.Minute},
		{name: "bare ephemeral is the 5m default", body: `{"system":[{"type":"text","cache_control":{"type":"ephemeral"}}]}`, want: 5 * time.Minute},
		{name: "bare ephemeral nested in messages", body: `{"messages":[{"content":[{"cache_control":{"type":"ephemeral"}}]}]}`, want: 5 * time.Minute},
		{name: "longest marker wins over a bare one", body: `{"tools":[{"cache_control":{"type":"ephemeral"}}],"system":[{"cache_control":{"type":"ephemeral","ttl":"1h"}}]}`, want: time.Hour},
		{name: "no cache control caches nothing", body: `{"messages":[{"role":"user","content":"hi"}]}`, want: 0},
		{name: "invalid json", body: `not json`, want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := RequestCacheTTL([]byte(tt.body)); got != tt.want {
				t.Fatalf("RequestCacheTTL() = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestTTLTier(t *testing.T) {
	tests := []struct {
		ttl  time.Duration
		want string
	}{
		{ttl: 5 * time.Minute, want: TTLTier5m},
		{ttl: 59 * time.Minute, want: TTLTier5m},
		{ttl: time.Hour, want: TTLTier1h},
		{ttl: 2 * time.Hour, want: TTLTier1h},
	}
	for _, tt := range tests {
		if got := TTLTier(tt.ttl); got != tt.want {
			t.Fatalf("TTLTier(%s) = %q, want %q", tt.ttl, got, tt.want)
		}
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

func TestSnapshotTracksSessionAndCounters(t *testing.T) {
	clock := &fakeClock{}
	prober := &recordingProber{result: ProbeResult{CacheReadInputTokens: 12468, CacheCreationInputTokens: 3}}
	scheduler := testScheduler(t, clock, prober, staticLiveness{live: true}, staticBinding{authID: "auth-a", state: BindingBound}, nil)

	observeOneHour(scheduler, "sess-1")
	snapshot := scheduler.Snapshot()
	if !snapshot.Enabled || len(snapshot.Sessions) != 1 {
		t.Fatalf("snapshot after Observe = %+v", snapshot)
	}
	session := snapshot.Sessions[0]
	if session.SessionID != "sess-1" || session.AuthID != "auth-a" || !session.Active {
		t.Fatalf("session snapshot = %+v", session)
	}
	if session.LastRequestAt == nil || session.NextProbeAt == nil {
		t.Fatalf("session snapshot is missing timestamps: %+v", session)
	}
	if session.TTL != "1h0m0s" || session.ProbesSent != 0 {
		t.Fatalf("session snapshot = %+v", session)
	}
	if snapshot.Counters.Scheduled != 1 || snapshot.Counters.Fired != 0 {
		t.Fatalf("counters after Observe = %+v", snapshot.Counters)
	}

	clock.fireLatest(t)
	snapshot = scheduler.Snapshot()
	if snapshot.Counters.Fired != 1 || snapshot.Counters.Hits != 1 || snapshot.Counters.Misses != 0 {
		t.Fatalf("counters after a hit = %+v", snapshot.Counters)
	}
	session = snapshot.Sessions[0]
	if session.ProbesSent != 1 || session.ConsecutiveProbes != 1 {
		t.Fatalf("probe counts = %+v", session)
	}
	if session.LastProbe == nil || session.LastProbe.Status != ProbeStatusHit {
		t.Fatalf("last probe = %+v", session.LastProbe)
	}
	if session.LastProbe.CacheReadInputTokens != 12468 || session.LastProbe.CacheCreationInputTokens != 3 {
		t.Fatalf("last probe tokens = %+v", session.LastProbe)
	}
}

func TestSnapshotRecordsAProbeThatMissed(t *testing.T) {
	clock := &fakeClock{}
	prober := &recordingProber{result: ProbeResult{CacheReadInputTokens: 0, Diagnosis: "messages_changed"}}
	scheduler := testScheduler(t, clock, prober, staticLiveness{live: true}, staticBinding{authID: "auth-a", state: BindingBound}, nil)

	observeOneHour(scheduler, "sess-1")
	clock.fireLatest(t)

	snapshot := scheduler.Snapshot()
	if snapshot.Counters.Misses != 1 || snapshot.Counters.Hits != 0 {
		t.Fatalf("counters after a miss = %+v", snapshot.Counters)
	}
	probe := snapshot.Sessions[0].LastProbe
	if probe == nil || probe.Status != ProbeStatusMiss || probe.Diagnosis != "messages_changed" {
		t.Fatalf("last probe = %+v", probe)
	}
}

func TestSnapshotRecordsProbeErrors(t *testing.T) {
	clock := &fakeClock{}
	prober := &recordingProber{err: errProbeTest}
	scheduler := testScheduler(t, clock, prober, staticLiveness{live: true}, staticBinding{authID: "auth-a", state: BindingBound}, nil)

	observeOneHour(scheduler, "sess-1")
	clock.fireLatest(t)

	snapshot := scheduler.Snapshot()
	if snapshot.Counters.Errors != 1 {
		t.Fatalf("counters after an error = %+v", snapshot.Counters)
	}
	if snapshot.Counters.SkippedByReason["probe-error"] != 1 {
		t.Fatalf("skipped_by_reason = %+v", snapshot.Counters.SkippedByReason)
	}
}

func TestSnapshotKeepsRetiredSessionsWithoutTheirBodies(t *testing.T) {
	clock := &fakeClock{}
	prober := &recordingProber{}
	scheduler := testScheduler(t, clock, prober, staticLiveness{live: false}, staticBinding{authID: "auth-a", state: BindingBound}, nil)

	observeOneHour(scheduler, "sess-1")
	clock.fireLatest(t)

	snapshot := scheduler.Snapshot()
	if len(snapshot.Sessions) != 1 {
		t.Fatalf("retired session was dropped from the snapshot: %+v", snapshot.Sessions)
	}
	session := snapshot.Sessions[0]
	if session.Active {
		t.Fatalf("retired session still reports active")
	}
	if session.RetiredAt == nil || session.NextProbeAt != nil {
		t.Fatalf("retired session = %+v", session)
	}
	if session.LastProbe == nil || session.LastProbe.SkippedReason != "no-live-agents" {
		t.Fatalf("retired session last probe = %+v", session.LastProbe)
	}
	if session.RetiredReason != "no-live-agents" {
		t.Fatalf("retired reason = %q", session.RetiredReason)
	}
	if snapshot.Counters.SkippedByReason["no-live-agents"] != 1 {
		t.Fatalf("skipped_by_reason = %+v", snapshot.Counters.SkippedByReason)
	}

	scheduler.mu.Lock()
	body := scheduler.sessions["sess-1"].body
	headers := scheduler.sessions["sess-1"].headers
	scheduler.mu.Unlock()
	if body != nil || headers != nil {
		t.Fatalf("retired session kept its request body in memory")
	}
}

func TestRetiringDoesNotEraseTheLastProbeResult(t *testing.T) {
	clock := &fakeClock{}
	prober := &recordingProber{result: ProbeResult{CacheReadInputTokens: 4242}}
	scheduler := testScheduler(t, clock, prober, staticLiveness{live: true}, staticBinding{authID: "auth-a", state: BindingBound}, func(cfg *Config) {
		cfg.MaxProbes = 1
	})

	observeOneHour(scheduler, "sess-1")
	clock.fireLatest(t)

	session := scheduler.Snapshot().Sessions[0]
	if session.Active {
		t.Fatalf("session should be retired after reaching max-probes")
	}
	if session.RetiredReason != "max-probes" {
		t.Fatalf("retired reason = %q, want max-probes", session.RetiredReason)
	}
	if session.LastProbe == nil || session.LastProbe.Status != ProbeStatusHit || session.LastProbe.CacheReadInputTokens != 4242 {
		t.Fatalf("retiring erased the probe result: %+v", session.LastProbe)
	}
}

func TestSnapshotOnNilScheduler(t *testing.T) {
	var scheduler *Scheduler
	snapshot := scheduler.Snapshot()
	if snapshot.Enabled || snapshot.Sessions == nil || snapshot.Counters.SkippedByReason == nil {
		t.Fatalf("nil scheduler snapshot must be empty but well formed: %+v", snapshot)
	}
}

func TestRetiredSessionHistoryIsBounded(t *testing.T) {
	clock := &fakeClock{}
	prober := &recordingProber{}
	scheduler := testScheduler(t, clock, prober, staticLiveness{live: false}, staticBinding{authID: "auth-a", state: BindingBound}, nil)

	for index := range maxRetiredSessions + 20 {
		session := "sess-" + strconv.Itoa(index)
		observeOneHour(scheduler, session)
		clock.fireLatest(t)
	}

	if got := len(scheduler.Snapshot().Sessions); got > maxRetiredSessions {
		t.Fatalf("retained %d retired sessions, want at most %d", got, maxRetiredSessions)
	}
}

func TestProbeMissed(t *testing.T) {
	tests := []struct {
		name     string
		read     int64
		baseline int64
		want     bool
	}{
		{name: "read nothing is a miss", read: 0, baseline: 100000, want: true},
		{name: "read nothing with no baseline is still a miss", read: 0, baseline: 0, want: true},
		{name: "full read is a hit", read: 100000, baseline: 100000, want: false},
		{name: "just above half is a hit", read: 50001, baseline: 100000, want: false},
		// The rule is "below half", so exactly half still counts as a hit.
		{name: "exactly half is a hit", read: 50000, baseline: 100000, want: false},
		{name: "one token below half is a miss", read: 49999, baseline: 100000, want: true},
		{name: "far below half is a miss", read: 12000, baseline: 100000, want: true},
		{name: "unknown baseline cannot judge proportion", read: 12000, baseline: 0, want: false},
		{name: "reading more than the baseline is a hit", read: 120000, baseline: 100000, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := probeMissed(tt.read, tt.baseline); got != tt.want {
				t.Fatalf("probeMissed(%d, %d) = %t, want %t", tt.read, tt.baseline, got, tt.want)
			}
		})
	}
}

func TestPartialReadCountsAsAMiss(t *testing.T) {
	clock := &fakeClock{}
	prober := &recordingProber{result: ProbeResult{CacheReadInputTokens: 4000, Diagnosis: "messages_changed"}}
	scheduler := testScheduler(t, clock, prober, staticLiveness{live: true}, staticBinding{authID: "auth-a", state: BindingBound}, nil)

	scheduler.Observe(ObserveInput{
		SessionID:            "sess-1",
		BindingSessionID:     "claude:sess-1:agent:main",
		AuthID:               "auth-a",
		Provider:             "claude",
		Model:                "claude-haiku-4-5-20251001",
		Body:                 []byte(oneHourBody),
		Headers:              http.Header{},
		TTL:                  time.Hour,
		StartedAt:            time.Now(),
		CacheReadInputTokens: 100000,
	})
	clock.fireLatest(t)

	snapshot := scheduler.Snapshot()
	if snapshot.Counters.Misses != 1 || snapshot.Counters.Hits != 0 {
		t.Fatalf("a probe reading 4%% of the baseline must count as a miss: %+v", snapshot.Counters)
	}
	probe := snapshot.Sessions[0].LastProbe
	if probe == nil || probe.Status != ProbeStatusMiss || probe.BaselineReadInputTokens != 100000 {
		t.Fatalf("last probe = %+v", probe)
	}
}

func TestFullReadAgainstABaselineCountsAsAHit(t *testing.T) {
	clock := &fakeClock{}
	prober := &recordingProber{result: ProbeResult{CacheReadInputTokens: 99000}}
	scheduler := testScheduler(t, clock, prober, staticLiveness{live: true}, staticBinding{authID: "auth-a", state: BindingBound}, nil)

	scheduler.Observe(ObserveInput{
		SessionID:            "sess-1",
		BindingSessionID:     "claude:sess-1:agent:main",
		AuthID:               "auth-a",
		Provider:             "claude",
		Model:                "claude-haiku-4-5-20251001",
		Body:                 []byte(oneHourBody),
		Headers:              http.Header{},
		TTL:                  time.Hour,
		StartedAt:            time.Now(),
		CacheReadInputTokens: 100000,
	})
	clock.fireLatest(t)

	if counters := scheduler.Snapshot().Counters; counters.Hits != 1 || counters.Misses != 0 {
		t.Fatalf("counters = %+v", counters)
	}
}

func TestProbeBodyDropsThinkingDependentContextManagement(t *testing.T) {
	body := []byte(`{"model":"m","max_tokens":32000,"thinking":{"type":"adaptive"},` +
		`"context_management":{"edits":[{"type":"clear_thinking_20251015","keep":"all"}]},` +
		`"messages":[{"role":"user","content":"hi"}]}`)

	probe, err := ProbeBody(body, 1)
	if err != nil {
		t.Fatalf("ProbeBody() error = %v", err)
	}
	if gjson.GetBytes(probe, "thinking").Exists() {
		t.Fatalf("thinking survived into the probe body")
	}
	// The upstream rejects clear_thinking_20251015 outright when thinking is
	// absent, so the strategy must go with it.
	if gjson.GetBytes(probe, "context_management").Exists() {
		t.Fatalf("context_management survived with only a thinking-dependent edit: %s", probe)
	}
}

func TestProbeBodyKeepsOtherContextManagementEdits(t *testing.T) {
	body := []byte(`{"model":"m","max_tokens":32000,"thinking":{"type":"adaptive"},` +
		`"context_management":{"edits":[{"type":"clear_thinking_20251015","keep":"all"},{"type":"clear_tool_uses_20250919","keep":3}]},` +
		`"messages":[{"role":"user","content":"hi"}]}`)

	probe, err := ProbeBody(body, 1)
	if err != nil {
		t.Fatalf("ProbeBody() error = %v", err)
	}
	edits := gjson.GetBytes(probe, "context_management.edits")
	if !edits.IsArray() || len(edits.Array()) != 1 {
		t.Fatalf("edits = %s, want only the non-thinking edit", edits.Raw)
	}
	if got := edits.Get("0.type").String(); got != "clear_tool_uses_20250919" {
		t.Fatalf("kept edit = %q", got)
	}
}

func TestProbeBodyLeavesContextManagementAloneWhenNoThinkingEdit(t *testing.T) {
	body := []byte(`{"model":"m","max_tokens":32000,` +
		`"context_management":{"edits":[{"type":"clear_tool_uses_20250919","keep":3}]},` +
		`"messages":[{"role":"user","content":"hi"}]}`)

	probe, err := ProbeBody(body, 1)
	if err != nil {
		t.Fatalf("ProbeBody() error = %v", err)
	}
	if got := gjson.GetBytes(probe, "context_management.edits.0.type").String(); got != "clear_tool_uses_20250919" {
		t.Fatalf("context_management was rewritten: %s", probe)
	}
}

func TestProbe5mDecision(t *testing.T) {
	tests := []struct {
		name    string
		mode    string
		models  []string
		model   string
		wantOK  bool
		wantWhy string
	}{
		{name: "auto matches fable", mode: Probe5mAuto, model: "claude-fable-5-1", wantOK: true, wantWhy: Probe5mDecisionModelAuto},
		{name: "auto matches mythos", mode: Probe5mAuto, model: "claude-mythos-5-1", wantOK: true, wantWhy: Probe5mDecisionModelAuto},
		{name: "auto matches a suffixed spelling", mode: Probe5mAuto, model: "claude-fable-5-1[1m]", wantOK: true, wantWhy: Probe5mDecisionModelAuto},
		{name: "auto matches a provider-prefixed spelling", mode: Probe5mAuto, model: "us.anthropic.CLAUDE-FABLE-5-1-v1:0", wantOK: true, wantWhy: Probe5mDecisionModelAuto},
		{name: "auto skips opus", mode: Probe5mAuto, model: "claude-opus-5", wantOK: false, wantWhy: Probe5mDecisionSkippedModel},
		{name: "auto skips an empty model", mode: Probe5mAuto, model: "", wantOK: false, wantWhy: Probe5mDecisionSkippedModel},
		{name: "an empty mode behaves as auto", mode: "", model: "claude-fable-5-1", wantOK: true, wantWhy: Probe5mDecisionModelAuto},
		{name: "always takes any model", mode: Probe5mAlways, model: "claude-opus-5", wantOK: true, wantWhy: Probe5mDecisionAlways},
		{name: "never refuses fable too", mode: Probe5mNever, model: "claude-fable-5-1", wantOK: false, wantWhy: Probe5mDecisionSkippedNever},
		{name: "an override list replaces the built-in one", mode: Probe5mAuto, models: []string{"claude-opus-5"}, model: "claude-opus-5", wantOK: true, wantWhy: Probe5mDecisionModelAuto},
		{name: "an override list excludes the built-in entries", mode: Probe5mAuto, models: []string{"claude-opus-5"}, model: "claude-fable-5-1", wantOK: false, wantWhy: Probe5mDecisionSkippedModel},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ok, why := probe5mDecision(tt.mode, tt.models, tt.model)
			if ok != tt.wantOK || why != tt.wantWhy {
				t.Fatalf("probe5mDecision(%q, %v, %q) = (%t, %q), want (%t, %q)", tt.mode, tt.models, tt.model, ok, why, tt.wantOK, tt.wantWhy)
			}
		})
	}
}

func TestObserveSchedulesA5mSessionAtTTLMinusBeforeExpiry5m(t *testing.T) {
	clock := &fakeClock{}
	prober := &recordingProber{}
	scheduler := testScheduler(t, clock, prober, staticLiveness{live: true}, staticBinding{authID: "auth-a", state: BindingBound}, nil)

	observeFiveMinutes(scheduler, "sess-5m", "claude-fable-5-1")

	if clock.count() != 1 {
		t.Fatalf("scheduled %d timers, want 1", clock.count())
	}
	// 5m minus the 45s lead time, measured from the observed request's start.
	if got := clock.timers[0].delay; got > 4*time.Minute+15*time.Second || got < 4*time.Minute+10*time.Second {
		t.Fatalf("delay = %s, want ~4m15s", got)
	}
}

func TestObserveAutoSkipsA5mSessionOnAnExpensiveCacheReadModel(t *testing.T) {
	clock := &fakeClock{}
	prober := &recordingProber{}
	scheduler := testScheduler(t, clock, prober, staticLiveness{live: true}, staticBinding{authID: "auth-a", state: BindingBound}, nil)

	observeFiveMinutes(scheduler, "sess-5m", "claude-opus-5")

	if clock.count() != 0 {
		t.Fatalf("scheduled %d timers for an expensive-cache-read model, want 0", clock.count())
	}
	if got := scheduler.Snapshot().Counters.SkippedByReason[Probe5mDecisionSkippedModel]; got != 1 {
		t.Fatalf("skipped_by_reason[%s] = %d, want 1", Probe5mDecisionSkippedModel, got)
	}
}

func TestObserveAutoStillSchedulesA1hSessionOnAnyModel(t *testing.T) {
	clock := &fakeClock{}
	prober := &recordingProber{}
	scheduler := testScheduler(t, clock, prober, staticLiveness{live: true}, staticBinding{authID: "auth-a", state: BindingBound}, nil)

	// oneHourBody's model is a haiku, which is not on the cheap-cache-read list.
	observeOneHour(scheduler, "sess-1h")

	if clock.count() != 1 {
		t.Fatalf("scheduled %d timers for a 1h session, want 1: probe-5m must not gate the 1h pool", clock.count())
	}
}

func TestObserveAlwaysProbesA5mSessionOnAnyModel(t *testing.T) {
	clock := &fakeClock{}
	prober := &recordingProber{}
	scheduler := testScheduler(t, clock, prober, staticLiveness{live: true}, staticBinding{authID: "auth-a", state: BindingBound},
		func(cfg *Config) { cfg.Probe5m = Probe5mAlways })

	observeFiveMinutes(scheduler, "sess-5m", "claude-opus-5")

	if clock.count() != 1 {
		t.Fatalf("scheduled %d timers under probe-5m=always, want 1", clock.count())
	}
	if got := scheduler.Snapshot().Sessions[0].Probe5mDecision; got != Probe5mDecisionAlways {
		t.Fatalf("probe_5m_decision = %q, want %q", got, Probe5mDecisionAlways)
	}
}

func TestObserveNeverSkipsEvery5mSessionButKeepsThe1hPool(t *testing.T) {
	clock := &fakeClock{}
	prober := &recordingProber{}
	scheduler := testScheduler(t, clock, prober, staticLiveness{live: true}, staticBinding{authID: "auth-a", state: BindingBound},
		func(cfg *Config) { cfg.Probe5m = Probe5mNever })

	observeFiveMinutes(scheduler, "sess-5m", "claude-fable-5-1")
	if clock.count() != 0 {
		t.Fatalf("scheduled %d timers under probe-5m=never, want 0", clock.count())
	}
	if got := scheduler.Snapshot().Counters.SkippedByReason[Probe5mDecisionSkippedNever]; got != 1 {
		t.Fatalf("skipped_by_reason[%s] = %d, want 1", Probe5mDecisionSkippedNever, got)
	}

	observeOneHour(scheduler, "sess-1h")
	if clock.count() != 1 {
		t.Fatalf("probe-5m=never must leave the 1h pool alone; timers = %d, want 1", clock.count())
	}
}

func TestFiveMinuteSessionsUseTheirOwnProbeBudget(t *testing.T) {
	clock := &fakeClock{}
	prober := &recordingProber{result: ProbeResult{CacheReadInputTokens: 4096}}
	scheduler := testScheduler(t, clock, prober, staticLiveness{live: true}, staticBinding{authID: "auth-a", state: BindingBound},
		func(cfg *Config) {
			cfg.MaxProbes = 6
			cfg.MaxProbes5m = 2
		})

	observeFiveMinutes(scheduler, "sess-5m", "claude-fable-5-1")
	clock.fireLatest(t)
	clock.fireLatest(t)
	if calls := prober.calls(); len(calls) != 2 {
		t.Fatalf("probed %d times, want 2 from max-probes-5m", len(calls))
	}
	// The second probe exhausts the 5m budget, so nothing is rearmed.
	if clock.count() != 2 {
		t.Fatalf("timers = %d, want 2: the budget must stop the rescheduling", clock.count())
	}
	session := scheduler.Snapshot().Sessions[0]
	if session.Active || session.RetiredReason != "max-probes" {
		t.Fatalf("session = %+v, want retired for max-probes", session)
	}
}

func TestFiveMinuteRescheduleUsesThe5mLeadTime(t *testing.T) {
	clock := &fakeClock{}
	prober := &recordingProber{result: ProbeResult{CacheReadInputTokens: 4096}}
	scheduler := testScheduler(t, clock, prober, staticLiveness{live: true}, staticBinding{authID: "auth-a", state: BindingBound}, nil)

	observeFiveMinutes(scheduler, "sess-5m", "claude-fable-5-1")
	clock.fireLatest(t)

	if clock.count() != 2 {
		t.Fatalf("timers = %d, want 2: the probe must rearm", clock.count())
	}
	if got := clock.timers[1].delay; got > 4*time.Minute+15*time.Second || got < 4*time.Minute+10*time.Second {
		t.Fatalf("reschedule delay = %s, want ~4m15s", got)
	}
}

func TestSnapshotReportsTheTierAndTheProbe5mSettings(t *testing.T) {
	clock := &fakeClock{}
	prober := &recordingProber{}
	scheduler := testScheduler(t, clock, prober, staticLiveness{live: true}, staticBinding{authID: "auth-a", state: BindingBound}, nil)

	observeFiveMinutes(scheduler, "sess-5m", "claude-fable-5-1")
	observeOneHour(scheduler, "sess-1h")

	snapshot := scheduler.Snapshot()
	if snapshot.BeforeExpiry5m != "45s" {
		t.Fatalf("before_expiry_5m = %q, want 45s", snapshot.BeforeExpiry5m)
	}
	if snapshot.Probe5m != Probe5mAuto {
		t.Fatalf("probe_5m = %q, want %q", snapshot.Probe5m, Probe5mAuto)
	}
	if snapshot.MaxProbes5m != 30 {
		t.Fatalf("max_probes_5m = %d, want 30", snapshot.MaxProbes5m)
	}
	if len(snapshot.Probe5mModels) != len(CheapCacheReadModels) {
		t.Fatalf("probe_5m_models = %v, want the built-in list %v", snapshot.Probe5mModels, CheapCacheReadModels)
	}

	byID := map[string]SessionSnapshot{}
	for _, session := range snapshot.Sessions {
		byID[session.SessionID] = session
	}
	if got := byID["sess-5m"]; got.TTLTier != TTLTier5m || got.Probe5mDecision != Probe5mDecisionModelAuto {
		t.Fatalf("5m session = (tier %q, decision %q), want (%q, %q)", got.TTLTier, got.Probe5mDecision, TTLTier5m, Probe5mDecisionModelAuto)
	}
	if got := byID["sess-1h"]; got.TTLTier != TTLTier1h || got.Probe5mDecision != Probe5mDecisionNotApplicable {
		t.Fatalf("1h session = (tier %q, decision %q), want (%q, %q)", got.TTLTier, got.Probe5mDecision, TTLTier1h, Probe5mDecisionNotApplicable)
	}
}
