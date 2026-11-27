package router

import (
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/prox/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/prox/quota"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/prox/session"
)

var t0 = time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)

type harness struct {
	t     *testing.T
	now   time.Time
	r     *Router
	ids   map[string]string // account → provider
	sizes map[string]int64  // credential file size, bumped by relogin
}

func newHarness(t *testing.T, mods ...func(*config.Config)) *harness {
	t.Helper()
	cfg := config.Default()
	cfg.Mode = config.ModeActive
	for _, m := range mods {
		m(&cfg)
	}
	h := &harness{t: t, now: t0, ids: map[string]string{}, sizes: map[string]int64{}}
	h.r = New(cfg, func() time.Time { return h.now })
	h.r.SetJitter(func() float64 { return 0.5 })
	return h
}

func (h *harness) advance(d time.Duration) { h.now = h.now.Add(d) }

func (h *harness) roster(provider string, ids ...string) {
	for _, id := range ids {
		h.ids[id] = provider
	}
	var entries []RosterEntry
	for id, p := range h.ids {
		entries = append(entries, RosterEntry{ID: id, AuthIndex: "idx-" + id, Provider: p, Name: id, Size: h.sizes[id]})
	}
	h.r.SyncRoster(entries)
}

// relogin simulates the credential file being rewritten.
func (h *harness) relogin(id string) {
	h.sizes[id]++
	h.roster(h.ids[id])
}

func (h *harness) remove(id string) {
	delete(h.ids, id)
	var entries []RosterEntry
	for id, p := range h.ids {
		entries = append(entries, RosterEntry{ID: id, AuthIndex: "idx-" + id, Provider: p, Name: id})
	}
	h.r.SyncRoster(entries)
}

type win struct {
	kind   quota.Kind
	family string
	used   float64
	reset  time.Duration
}

func (h *harness) poll(id string, ws ...win) {
	h.t.Helper()
	snap := quota.Snapshot{Provider: h.ids[id], PolledAt: h.now, Identity: "identity-" + id}
	for _, w := range ws {
		at := h.now.Add(w.reset)
		snap.Windows = append(snap.Windows, quota.Window{Kind: w.kind, Family: w.family, UsedPercent: w.used, ResetsAt: &at, Source: quota.SourcePoll, ObservedAt: h.now})
	}
	h.r.ApplyPoll(id, snap, "", nil)
}

func weekly(used float64, reset time.Duration) win { return win{quota.Weekly, "", used, reset} }
func fiveHour(used float64, reset time.Duration) win {
	return win{quota.FiveHour, "", used, reset}
}

func claudeSession(id string) http.Header {
	return http.Header{"X-Claude-Code-Session-Id": {id}}
}

func claudeAgent(id, agent string) http.Header {
	return http.Header{"X-Claude-Code-Session-Id": {id}, "X-Claude-Code-Agent-Id": {agent}}
}

func codexSession(id string) http.Header { return http.Header{"Session-Id": {id}} }

func codexChild(parent, thread string) http.Header {
	return http.Header{"Session-Id": {parent}, "Thread-Id": {thread}}
}

func (h *harness) pick(provider, model string, hdr http.Header, offered ...string) Decision {
	h.t.Helper()
	req := PickRequest{Provider: provider, Model: model}
	if hdr != nil {
		req.Session, req.HasSession = session.FromRequest(hdr, nil)
	}
	for _, id := range offered {
		req.Candidates = append(req.Candidates, Candidate{ID: id, Provider: provider})
	}
	return h.r.Pick(req)
}

func (h *harness) observe(provider, model, authID string, hdr http.Header, status int, headers http.Header) {
	o := Outcome{At: h.now, AuthID: authID, Provider: provider, Model: model, Status: status, Failed: status >= 400 || status == -1, Headers: headers}
	if status == -1 {
		o.Status = 0
	}
	if hdr != nil {
		o.Session, o.HasSession = session.FromRequest(hdr, nil)
	}
	h.r.Observe(o)
}

func expect(t *testing.T, d Decision, outcome, auth string) {
	t.Helper()
	if d.Outcome != outcome || d.AuthID != auth {
		t.Fatalf("got outcome=%s auth=%s cause=%s ranked=%+v skipped=%+v; want %s on %q", d.Outcome, d.AuthID, d.Cause, d.Ranked, d.Skipped, outcome, auth)
	}
}

// TestAcceptance is the scenario from the brief, for both providers.
func TestAcceptance(t *testing.T) {
	for _, p := range []struct {
		provider, model string
		thread          func(string) http.Header
		child           func(string) http.Header
	}{
		{"claude", "claude-opus-4-6", claudeSession, func(id string) http.Header { return claudeAgent(id, "agent-7") }},
		{"codex", "gpt-5.5-codex", codexSession, func(id string) http.Header { return codexChild(id, "child-thread") }},
	} {
		t.Run(p.provider, func(t *testing.T) {
			h := newHarness(t)
			h.roster(p.provider, "A", "B", "C")
			h.poll("A", weekly(30, 20*time.Hour))
			h.poll("B", weekly(30, 96*time.Hour))
			h.poll("C", weekly(30, 120*time.Hour))
			t1 := p.thread("thread-1")

			// A new thread selects A by quota-aware scheduling.
			expect(t, h.pick(p.provider, p.model, t1, "A", "B", "C"), OutcomeNewSession, "A")
			// Subsequent requests stay on A.
			for i := 0; i < 3; i++ {
				h.advance(time.Minute)
				h.observe(p.provider, p.model, "A", t1, 200, nil)
				expect(t, h.pick(p.provider, p.model, t1, "A", "B", "C"), OutcomeAffinity, "A")
			}
			// Rankings change so B becomes more attractive; the thread stays on A.
			h.poll("A", weekly(40, 72*time.Hour))
			h.poll("B", weekly(40, 10*time.Hour))
			d := h.pick(p.provider, p.model, t1, "A", "B", "C")
			expect(t, d, OutcomeAffinity, "A")
			if !d.AffinityOverrode || d.Ranked[0].AuthID != "B" {
				t.Fatalf("decision must show affinity overrode ranking: %+v", d)
			}
			// A new thread may select B.
			expect(t, h.pick(p.provider, p.model, p.thread("thread-2"), "A", "B", "C"), OutcomeNewSession, "B")
			// A becomes genuinely exhausted: the thread fails over to B and is rebound.
			h.poll("A", weekly(100, 72*time.Hour))
			d = h.pick(p.provider, p.model, t1, "A", "B", "C")
			expect(t, d, OutcomeRebind, "B")
			if d.Cause != "exhausted:weekly_exhausted" {
				t.Fatalf("rebind cause: %q", d.Cause)
			}
			// The rebinding survives a restart.
			state := h.r.Export()
			h2 := newHarness(t)
			h2.now = h.now.Add(time.Minute)
			h2.ids = h.ids
			h2.r.Import(state)
			h2.roster(p.provider)
			expect(t, h2.pick(p.provider, p.model, t1, "A", "B", "C"), OutcomeAffinity, "B")
			// A recovering does not steal the thread back.
			h2.poll("A", weekly(0, 1*time.Hour))
			expect(t, h2.pick(p.provider, p.model, t1, "A", "B", "C"), OutcomeAffinity, "B")
			// A child agent inherits its parent's account even though C or A ranks higher.
			h2.poll("C", weekly(0, 2*time.Hour))
			d = h2.pick(p.provider, p.model, p.child("thread-1"), "A", "B", "C")
			expect(t, d, OutcomeInherited, "B")
			expect(t, h2.pick(p.provider, p.model, p.child("thread-1"), "A", "B", "C"), OutcomeAffinity, "B")
		})
	}
}

func TestInheritanceCanBeDisabled(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { c.AffinityInheritParent = false })
	h.roster("claude", "A", "B")
	h.poll("A", weekly(0, 10*time.Hour))
	h.poll("B", weekly(0, 20*time.Hour))
	expect(t, h.pick("claude", "m", claudeSession("p"), "B"), OutcomeNewSession, "B")
	expect(t, h.pick("claude", "m", claudeAgent("p", "x"), "A", "B"), OutcomeNewSession, "A")
}

func TestInheritanceAcrossModels(t *testing.T) {
	h := newHarness(t)
	h.roster("claude", "A", "B")
	h.poll("A", weekly(0, 10*time.Hour))
	h.poll("B", weekly(0, 20*time.Hour))
	expect(t, h.pick("claude", "claude-opus-4-6", claudeSession("p"), "B"), OutcomeNewSession, "B")
	expect(t, h.pick("claude", "claude-haiku-4-5", claudeAgent("p", "x"), "A", "B"), OutcomeInherited, "B")
}

func TestTransientFailureKeepsBinding(t *testing.T) {
	h := newHarness(t)
	h.roster("claude", "A", "B")
	h.poll("A", weekly(0, 10*time.Hour))
	h.poll("B", weekly(0, 20*time.Hour))
	s := claudeSession("s")
	expect(t, h.pick("claude", "m", s, "A", "B"), OutcomeNewSession, "A")
	h.observe("claude", "m", "A", s, 503, nil)
	// CPA retries without A (already tried this request).
	d := h.pick("claude", "m", s, "B")
	expect(t, d, OutcomeFailover, "B")
	if d.Cause != "not_offered" {
		t.Fatalf("cause: %s", d.Cause)
	}
	h.observe("claude", "m", "B", s, 200, nil)
	expect(t, h.pick("claude", "m", s, "A", "B"), OutcomeAffinity, "A")
}

func TestCircuitBreakerAndRecovery(t *testing.T) {
	h := newHarness(t)
	h.roster("claude", "A", "B")
	h.poll("A", weekly(0, 10*time.Hour))
	h.poll("B", weekly(0, 20*time.Hour))
	s := claudeSession("s")
	expect(t, h.pick("claude", "m", s, "A", "B"), OutcomeNewSession, "A")
	for i := 0; i < 3; i++ {
		h.observe("claude", "m", "A", nil, 502, nil)
	}
	d := h.pick("claude", "m", s, "A", "B")
	expect(t, d, OutcomeFailover, "B")
	if d.Cause != "circuit_open" {
		t.Fatalf("cause %s", d.Cause)
	}
	h.advance(61 * time.Second)
	h.poll("A", weekly(0, 10*time.Hour))
	expect(t, h.pick("claude", "m", s, "A", "B"), OutcomeAffinity, "A")
	// A network error with no status is transient too.
	for i := 0; i < 3; i++ {
		h.observe("claude", "m", "A", nil, -1, nil)
	}
	expect(t, h.pick("claude", "m", s, "A", "B"), OutcomeFailover, "B")
}

func TestAuthFailureRebindsAndRecoversOnRelogin(t *testing.T) {
	h := newHarness(t)
	h.roster("claude", "A", "B")
	h.poll("A", weekly(0, 10*time.Hour))
	h.poll("B", weekly(0, 20*time.Hour))
	s := claudeSession("s")
	expect(t, h.pick("claude", "m", s, "A", "B"), OutcomeNewSession, "A")
	h.observe("claude", "m", "A", s, 401, nil)
	d := h.pick("claude", "m", s, "A", "B")
	expect(t, d, OutcomeRebind, "B")
	if d.Cause != "auth_failed_401" {
		t.Fatalf("cause %s", d.Cause)
	}
	// A successful poll with the same credential file does not clear a rejection
	// (e.g. a disabled organization whose usage endpoint still answers).
	h.poll("A", weekly(0, 10*time.Hour))
	expect(t, h.pick("claude", "m", claudeSession("other"), "A", "B"), OutcomeNewSession, "B")
	// Re-login rewrites the file; the verification poll succeeds and A is usable for new sessions.
	h.relogin("A")
	h.poll("A", weekly(0, 10*time.Hour))
	expect(t, h.pick("claude", "m", claudeSession("new"), "A", "B"), OutcomeNewSession, "A")
	expect(t, h.pick("claude", "m", s, "A", "B"), OutcomeAffinity, "B")
}

func TestClaude429Classification(t *testing.T) {
	reset := strconv.FormatInt(t0.Add(2*time.Hour).Unix(), 10)
	t.Run("shared window rejected rebinds", func(t *testing.T) {
		h := newHarness(t)
		h.roster("claude", "A", "B")
		h.poll("A", weekly(10, 10*time.Hour))
		h.poll("B", weekly(10, 20*time.Hour))
		s := claudeSession("s")
		expect(t, h.pick("claude", "m", s, "A", "B"), OutcomeNewSession, "A")
		h.observe("claude", "m", "A", s, 429, http.Header{
			"Anthropic-Ratelimit-Unified-5h-Status":      {"rejected"},
			"Anthropic-Ratelimit-Unified-5h-Utilization": {"1.0"},
			"Anthropic-Ratelimit-Unified-5h-Reset":       {reset},
		})
		d := h.pick("claude", "m", s, "A", "B")
		expect(t, d, OutcomeRebind, "B")
		if d.Cause != "exhausted:five_hour_exhausted" {
			t.Fatalf("cause %s", d.Cause)
		}
	})
	t.Run("model-only rejection blocks only that family", func(t *testing.T) {
		h := newHarness(t)
		h.roster("claude", "A", "B")
		h.poll("A", weekly(10, 10*time.Hour))
		h.poll("B", weekly(10, 20*time.Hour))
		h.observe("claude", "claude-fable-5", "A", nil, 429, http.Header{
			"Anthropic-Ratelimit-Unified-Status":    {"rejected"},
			"Anthropic-Ratelimit-Unified-5h-Status": {"allowed"},
			"Anthropic-Ratelimit-Unified-Reset":     {reset},
		})
		expect(t, h.pick("claude", "claude-fable-5", claudeSession("f"), "A", "B"), OutcomeNewSession, "B")
		expect(t, h.pick("claude", "claude-sonnet-4", claudeSession("g"), "A", "B"), OutcomeNewSession, "A")
	})
	t.Run("plain rate limit is temporary", func(t *testing.T) {
		h := newHarness(t)
		h.roster("claude", "A", "B")
		h.poll("A", weekly(10, 10*time.Hour))
		h.poll("B", weekly(10, 20*time.Hour))
		s := claudeSession("s")
		expect(t, h.pick("claude", "m", s, "A", "B"), OutcomeNewSession, "A")
		h.observe("claude", "m", "A", s, 429, http.Header{"Retry-After": {"30"}})
		expect(t, h.pick("claude", "m", s, "A", "B"), OutcomeFailover, "B")
		h.advance(31 * time.Second)
		expect(t, h.pick("claude", "m", s, "A", "B"), OutcomeAffinity, "A")
	})
}

func TestCodexExhaustionFromHeaders(t *testing.T) {
	h := newHarness(t)
	h.roster("codex", "A", "B")
	h.poll("A", weekly(10, 10*time.Hour))
	h.poll("B", weekly(10, 20*time.Hour))
	s := codexSession("c")
	expect(t, h.pick("codex", "gpt-5.5", s, "A", "B"), OutcomeNewSession, "A")
	h.observe("codex", "gpt-5.5", "A", s, 429, http.Header{
		"X-Codex-Primary-Used-Percent":        {"100"},
		"X-Codex-Primary-Window-Minutes":      {"300"},
		"X-Codex-Primary-Reset-After-Seconds": {"3600"},
	})
	expect(t, h.pick("codex", "gpt-5.5", s, "A", "B"), OutcomeRebind, "B")
}

func TestModelSpecificQuota(t *testing.T) {
	h := newHarness(t)
	h.roster("claude", "A", "B")
	h.poll("A", weekly(10, 10*time.Hour), win{quota.ModelWeekly, "opus", 100, 30 * time.Hour})
	h.poll("B", weekly(10, 20*time.Hour))
	expect(t, h.pick("claude", "claude-opus-4-6", nil, "A", "B"), OutcomeNoSession, "B")
	expect(t, h.pick("claude", "claude-sonnet-4-5", nil, "A", "B"), OutcomeNoSession, "A")
}

func TestPoliciesAndPriorities(t *testing.T) {
	setup := func(mods ...func(*config.Config)) *harness {
		h := newHarness(t, mods...)
		h.roster("claude", "early", "pressured", "late")
		h.poll("early", weekly(95, 6*time.Hour), fiveHour(90, time.Hour)) // 5% left in 6h
		h.poll("pressured", weekly(20, 30*time.Hour))                     // 80% left in 30h
		h.poll("late", weekly(0, 150*time.Hour))
		return h
	}
	expect(t, setup().pick("claude", "m", nil, "early", "pressured", "late"), OutcomeNoSession, "early")
	qp := setup(func(c *config.Config) { c.Strategy = config.StrategyQuotaPressure })
	expect(t, qp.pick("claude", "m", nil, "early", "pressured", "late"), OutcomeNoSession, "pressured")
	bal := setup(func(c *config.Config) { c.Strategy = config.StrategyBalanced })
	expect(t, bal.pick("claude", "m", nil, "early", "pressured", "late"), OutcomeNoSession, "pressured")
	manual := setup(func(c *config.Config) {
		c.Strategy = config.StrategyManualPriority
		c.AccountPriority = map[string]int{"late": 5}
	})
	expect(t, manual.pick("claude", "m", nil, "early", "pressured", "late"), OutcomeNoSession, "late")

	// CPA priority tiers outrank every plugin policy.
	h := setup()
	req := PickRequest{Provider: "claude", Model: "m", Candidates: []Candidate{{ID: "early", Provider: "claude"}, {ID: "late", Provider: "claude", Priority: 10}}}
	expect(t, h.r.Pick(req), OutcomeNoSession, "late")
}

func TestStableTieBreak(t *testing.T) {
	h := newHarness(t)
	h.roster("claude", "b", "a")
	h.poll("a", weekly(10, 10*time.Hour))
	h.poll("b", weekly(10, 10*time.Hour))
	for i := 0; i < 5; i++ {
		expect(t, h.pick("claude", "m", nil, "b", "a"), OutcomeNoSession, "a")
	}
}

func TestUntrustworthyQuotaDelegates(t *testing.T) {
	h := newHarness(t)
	h.roster("claude", "A", "B")
	d := h.pick("claude", "m", claudeSession("s"), "A", "B")
	expect(t, d, OutcomeDelegated, "")
	if d.Cause != "no_trustworthy_quota" || d.Applied {
		t.Fatalf("decision %+v", d)
	}
	// Stale data is not trusted for new sessions either.
	h.poll("A", weekly(10, 48*time.Hour))
	h.advance(11 * time.Minute)
	expect(t, h.pick("claude", "m", claudeSession("t"), "A", "B"), OutcomeDelegated, "")
	// When some candidates are known-bad, avoid them instead of delegating.
	h.poll("A", weekly(100, 48*time.Hour))
	d = h.pick("claude", "m", nil, "A", "B")
	expect(t, d, OutcomeNoSession, "B")
	if !d.Fallback {
		t.Fatal("fallback flag missing")
	}
	// Nothing usable at all: delegate.
	expect(t, h.pick("claude", "m", nil, "A"), OutcomeDelegated, "")
}

func TestStaleQuotaDoesNotBreakAffinity(t *testing.T) {
	h := newHarness(t)
	h.roster("claude", "A", "B")
	h.poll("A", weekly(10, 48*time.Hour))
	h.poll("B", weekly(10, 72*time.Hour))
	s := claudeSession("s")
	expect(t, h.pick("claude", "m", s, "A", "B"), OutcomeNewSession, "A")
	h.advance(30 * time.Minute)
	expect(t, h.pick("claude", "m", s, "A", "B"), OutcomeAffinity, "A")
}

func TestResetBoundaryNeedsEvidence(t *testing.T) {
	h := newHarness(t)
	h.roster("claude", "A", "B")
	h.poll("A", weekly(100, time.Hour))
	h.poll("B", weekly(50, 100*time.Hour))
	h.advance(61 * time.Minute)
	h.poll("B", weekly(50, 99*time.Hour))
	d := h.pick("claude", "m", nil, "A", "B")
	expect(t, d, OutcomeNoSession, "B")
	if len(d.Skipped) != 1 || d.Skipped[0].Reason != "awaiting_reset_verification" {
		t.Fatalf("passed reset must not re-enable A without evidence: %+v", d.Skipped)
	}
	h.poll("A", weekly(0, 7*24*time.Hour-time.Minute))
	h.poll("B", weekly(50, 10*time.Hour))
	expect(t, h.pick("claude", "m", nil, "A", "B"), OutcomeNoSession, "B")
	h.poll("B", weekly(100, 10*time.Hour))
	expect(t, h.pick("claude", "m", nil, "A", "B"), OutcomeNoSession, "A")
}

func TestIsolation(t *testing.T) {
	h := newHarness(t)
	h.roster("claude", "A", "B")
	h.roster("codex", "X", "Y")
	h.poll("A", weekly(0, 10*time.Hour))
	h.poll("B", weekly(0, 20*time.Hour))
	h.poll("X", weekly(0, 10*time.Hour))
	h.poll("Y", weekly(0, 20*time.Hour))
	s := claudeSession("same")
	expect(t, h.pick("claude", "opus", s, "B"), OutcomeNewSession, "B")
	// Different model scope: independent binding.
	expect(t, h.pick("claude", "haiku", s, "A", "B"), OutcomeNewSession, "A")
	// Different provider with a colliding raw ID: independent.
	expect(t, h.pick("codex", "gpt", codexSession("same"), "X", "Y"), OutcomeNewSession, "X")
	// Different caller scope: independent.
	ref, _ := session.FromRequest(s, map[string]any{"caller_scope": "other-caller"})
	d := h.r.Pick(PickRequest{Provider: "claude", Model: "opus", Session: ref, HasSession: true, Candidates: []Candidate{{ID: "A", Provider: "claude"}, {ID: "B", Provider: "claude"}}})
	expect(t, d, OutcomeNewSession, "A")
	// Mixed-provider requests are left to CPA.
	mixed := h.r.Pick(PickRequest{Provider: "claude", Candidates: []Candidate{{ID: "A", Provider: "claude"}, {ID: "X", Provider: "codex"}}})
	expect(t, mixed, OutcomeDelegated, "")
}

func TestProviderScopeSharesAccountAcrossModels(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { c.AffinityScope = config.ScopeProvider })
	h.roster("claude", "A", "B")
	h.poll("A", weekly(0, 10*time.Hour))
	h.poll("B", weekly(0, 20*time.Hour))
	s := claudeSession("s")
	expect(t, h.pick("claude", "opus", s, "B"), OutcomeNewSession, "B")
	expect(t, h.pick("claude", "haiku", s, "A", "B"), OutcomeAffinity, "B")
}

func TestConcurrentFirstRequestsBindOnce(t *testing.T) {
	h := newHarness(t)
	h.roster("claude", "A", "B", "C")
	h.poll("A", weekly(0, 10*time.Hour))
	h.poll("B", weekly(0, 10*time.Hour))
	h.poll("C", weekly(0, 10*time.Hour))
	s := claudeSession("race")
	var wg sync.WaitGroup
	results := make([]Decision, 64)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// Each request sees the candidates in a different order.
			ids := []string{"A", "B", "C"}
			ids[0], ids[i%3] = ids[i%3], ids[0]
			results[i] = h.pick("claude", "m", s, ids...)
		}(i)
	}
	wg.Wait()
	news := 0
	for _, d := range results {
		if d.AuthID != results[0].AuthID {
			t.Fatalf("session split across accounts: %s vs %s", d.AuthID, results[0].AuthID)
		}
		if d.Outcome == OutcomeNewSession {
			news++
		}
	}
	if news != 1 || len(h.r.Sessions()) != 1 {
		t.Fatalf("expected one binding, got %d new decisions and %d sessions", news, len(h.r.Sessions()))
	}
}

func TestSessionlessRequestsNeverBind(t *testing.T) {
	h := newHarness(t)
	h.roster("claude", "A")
	h.poll("A", weekly(0, 10*time.Hour))
	expect(t, h.pick("claude", "m", http.Header{"X-Client-Request-Id": {"r1"}}, "A"), OutcomeNoSession, "A")
	if n := len(h.r.Sessions()); n != 0 {
		t.Fatalf("sessionless request created %d bindings", n)
	}
}

func TestTTLExpiryAndRenewal(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { c.AffinityTTL = time.Hour })
	h.roster("claude", "A", "B")
	h.poll("A", weekly(0, 100*time.Hour))
	h.poll("B", weekly(0, 200*time.Hour))
	s := claudeSession("s")
	expect(t, h.pick("claude", "m", s, "B"), OutcomeNewSession, "B")
	for i := 0; i < 3; i++ {
		h.advance(50 * time.Minute)
		h.poll("A", weekly(0, 100*time.Hour))
		expect(t, h.pick("claude", "m", s, "A", "B"), OutcomeAffinity, "B")
	}
	h.advance(61 * time.Minute)
	h.poll("A", weekly(0, 100*time.Hour))
	h.poll("B", weekly(0, 200*time.Hour))
	expect(t, h.pick("claude", "m", s, "A", "B"), OutcomeNewSession, "A")
}

func TestAccountRemovalRebinds(t *testing.T) {
	h := newHarness(t)
	h.roster("claude", "A", "B")
	h.poll("A", weekly(0, 10*time.Hour))
	h.poll("B", weekly(0, 20*time.Hour))
	s := claudeSession("s")
	expect(t, h.pick("claude", "m", s, "A", "B"), OutcomeNewSession, "A")
	h.remove("A")
	d := h.pick("claude", "m", s, "B")
	expect(t, d, OutcomeRebind, "B")
	if d.Cause != "removed" {
		t.Fatalf("cause %s", d.Cause)
	}
}

func TestDisabledAccountRebinds(t *testing.T) {
	h := newHarness(t)
	h.roster("claude", "A", "B")
	h.poll("A", weekly(0, 10*time.Hour))
	h.poll("B", weekly(0, 20*time.Hour))
	s := claudeSession("s")
	expect(t, h.pick("claude", "m", s, "A", "B"), OutcomeNewSession, "A")
	req := PickRequest{Provider: "claude", Model: "m", HasSession: true, Candidates: []Candidate{{ID: "A", Provider: "claude", Status: "disabled"}, {ID: "B", Provider: "claude"}}}
	req.Session, _ = session.FromRequest(s, nil)
	expect(t, h.r.Pick(req), OutcomeRebind, "B")
}

func TestRebindPolicies(t *testing.T) {
	run := func(policy string, auth bool) Decision {
		h := newHarness(t, func(c *config.Config) { c.RebindPolicy = policy })
		h.roster("claude", "A", "B")
		h.poll("A", weekly(0, 10*time.Hour))
		h.poll("B", weekly(0, 20*time.Hour))
		s := claudeSession("s")
		h.pick("claude", "m", s, "A", "B")
		if auth {
			h.observe("claude", "m", "A", s, 401, nil)
			return h.pick("claude", "m", s, "A", "B")
		}
		return h.pick("claude", "m", s, "B")
	}
	expect(t, run(config.RebindEager, false), OutcomeFailover, "B")
	expect(t, run(config.RebindConservative, false), OutcomeFailover, "B")
	expect(t, run(config.RebindNever, true), OutcomeFailover, "B")
	expect(t, run(config.RebindConservative, true), OutcomeRebind, "B")
}

func TestEagerRebindsAfterFailoverSucceeds(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { c.RebindPolicy = config.RebindEager })
	h.roster("claude", "A", "B")
	h.poll("A", weekly(0, 10*time.Hour))
	h.poll("B", weekly(0, 20*time.Hour))
	s := claudeSession("s")
	expect(t, h.pick("claude", "m", s, "A", "B"), OutcomeNewSession, "A")
	expect(t, h.pick("claude", "m", s, "B"), OutcomeFailover, "B")
	h.observe("claude", "m", "B", s, 200, nil)
	expect(t, h.pick("claude", "m", s, "A", "B"), OutcomeAffinity, "B")
}

func TestHealthChecksApplyWithoutQuotaData(t *testing.T) {
	h := newHarness(t)
	h.roster("claude", "A", "B")
	h.poll("B", weekly(0, 20*time.Hour))
	s := claudeSession("s")
	// A has no snapshot yet; a traffic 401 must still park it.
	h.observe("claude", "m", "A", nil, 401, nil)
	d := h.pick("claude", "m", s, "A", "B")
	expect(t, d, OutcomeNewSession, "B")
	if len(d.Skipped) != 1 || d.Skipped[0].Reason != "auth_failed_401" {
		t.Fatalf("skipped: %+v", d.Skipped)
	}
}

func TestFullWindowWithoutResetIsExhausted(t *testing.T) {
	snap := quota.Snapshot{Windows: []quota.Window{{Kind: quota.Weekly, UsedPercent: 100, ObservedAt: t0, Source: quota.SourcePoll}}}
	if a := quota.Assess(snap, "m", t0, 10*time.Minute); a.State != quota.Exhausted || a.Until != nil {
		t.Fatalf("got %+v", a)
	}
}

func TestHostPinnedAccount(t *testing.T) {
	h := newHarness(t)
	h.roster("codex", "A", "B")
	h.poll("A", weekly(0, 10*time.Hour))
	h.poll("B", weekly(0, 20*time.Hour))
	ref, _ := session.FromRequest(codexSession("ws"), nil)
	d := h.r.Pick(PickRequest{Provider: "codex", Model: "m", Session: ref, HasSession: true, PinnedAuthID: "B", Candidates: []Candidate{{ID: "B", Provider: "codex"}}})
	expect(t, d, OutcomeHostPinned, "B")
	expect(t, h.pick("codex", "m", codexSession("ws"), "A", "B"), OutcomeAffinity, "B")
}

func TestLearningFromDelegatedRequests(t *testing.T) {
	h := newHarness(t)
	h.roster("claude", "A", "B")
	s := claudeSession("s")
	expect(t, h.pick("claude", "m", s, "A", "B"), OutcomeDelegated, "")
	h.observe("claude", "m", "B", s, 200, nil)
	h.poll("A", weekly(0, 10*time.Hour))
	h.poll("B", weekly(0, 20*time.Hour))
	expect(t, h.pick("claude", "m", s, "A", "B"), OutcomeAffinity, "B")
}

func TestLateFailureEvidenceRepairsBinding(t *testing.T) {
	// CPA publishes usage asynchronously: the retry pick can run before the
	// 429 that exhausted A is observed. The later success on B repairs the binding.
	h := newHarness(t)
	h.roster("claude", "A", "B")
	h.poll("A", weekly(0, 10*time.Hour))
	h.poll("B", weekly(0, 20*time.Hour))
	s := claudeSession("s")
	expect(t, h.pick("claude", "m", s, "A", "B"), OutcomeNewSession, "A")
	expect(t, h.pick("claude", "m", s, "B"), OutcomeFailover, "B")
	h.observe("claude", "m", "A", s, 429, http.Header{
		"Anthropic-Ratelimit-Unified-7d-Status": {"rejected"},
		"Anthropic-Ratelimit-Unified-7d-Reset":  {strconv.FormatInt(t0.Add(5*time.Hour).Unix(), 10)},
	})
	h.observe("claude", "m", "B", s, 200, nil)
	sess := h.r.Sessions()
	if len(sess) != 1 || sess[0].AuthID != "B" || sess[0].LastRebindWhy != "learned:exhausted:weekly_exhausted" {
		t.Fatalf("binding not repaired: %+v", sess)
	}
}

func TestShadowModeProposesAndAdoptsActualOnActivation(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { c.Mode = config.ModeShadow })
	h.roster("claude", "A", "B")
	h.poll("A", weekly(0, 10*time.Hour))
	h.poll("B", weekly(0, 20*time.Hour))
	s := claudeSession("s")
	d := h.pick("claude", "m", s, "A", "B")
	if d.AuthID != "A" || d.Applied {
		t.Fatalf("shadow decision %+v", d)
	}
	// CPA actually served the session from B.
	h.observe("claude", "m", "B", s, 200, nil)
	if got := h.r.Decisions("", "", 1)[0]; got.Result == nil || got.Result.AuthID != "B" {
		t.Fatalf("actual routing not attributed: %+v", got)
	}
	cfg := h.r.Config()
	cfg.Mode = config.ModeActive
	h.r.Configure(cfg)
	expect(t, h.pick("claude", "m", s, "A", "B"), OutcomeAffinity, "B")
}

func TestEvictionCap(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { c.AffinityMaxSessions = 100 })
	h.roster("claude", "A")
	h.poll("A", weekly(0, 10*time.Hour))
	for i := 0; i < 150; i++ {
		h.advance(time.Second)
		h.pick("claude", "m", claudeSession(fmt.Sprint("s", i)), "A")
	}
	if n := len(h.r.Sessions()); n > 100 {
		t.Fatalf("cap exceeded: %d", n)
	}
	if d := h.pick("claude", "m", claudeSession("s149"), "A"); d.Outcome != OutcomeAffinity {
		t.Fatal("most recent session was evicted")
	}
}

func TestCredentialReplacementResetsHealth(t *testing.T) {
	h := newHarness(t)
	h.r.SyncRoster([]RosterEntry{{ID: "A", AuthIndex: "1", Provider: "claude", Email: "one@example.com"}})
	h.ids["A"] = "claude"
	h.poll("A", weekly(10, 10*time.Hour))
	h.observe("claude", "m", "A", nil, 401, nil)
	h.r.SyncRoster([]RosterEntry{{ID: "A", AuthIndex: "1", Provider: "claude", Email: "two@example.com"}})
	acc := h.r.Accounts()[0]
	if acc.Health.AuthFailed || acc.Polled {
		t.Fatalf("replaced credential kept old state: %+v", acc)
	}
}

func TestPollSchedulingBackoffAndResets(t *testing.T) {
	h := newHarness(t)
	h.roster("claude", "A")
	if due := h.r.DuePolls(); len(due) != 1 || due[0].AuthIndex != "idx-A" {
		t.Fatalf("new account not due: %+v", due)
	}
	h.poll("A", weekly(10, 2*time.Minute))
	if next := h.r.NextPollAt(); !next.Equal(t0.Add(2*time.Minute - probeOffset)) {
		t.Fatalf("poll must precede the reset: %s", next)
	}
	h.r.ApplyPoll("A", quota.Snapshot{}, "", &quota.Error{Code: "quota_http_500", Status: 500})
	first := h.r.NextPollAt().Sub(h.now)
	h.r.ApplyPoll("A", quota.Snapshot{}, "", &quota.Error{Code: "quota_http_500", Status: 500})
	second := h.r.NextPollAt().Sub(h.now)
	if first != time.Minute || second != 2*time.Minute {
		t.Fatalf("backoff %s then %s", first, second)
	}
	h.r.ApplyPoll("A", quota.Snapshot{}, "", &quota.Error{Code: "quota_http_429", Status: 429, RetryAfter: 20 * time.Minute})
	if d := h.r.NextPollAt().Sub(h.now); d != 20*time.Minute {
		t.Fatalf("Retry-After ignored: %s", d)
	}
	if n, wait := h.r.RequestRefresh("A"); n != 1 || wait != 0 {
		t.Fatal("manual refresh refused")
	}
	if _, wait := h.r.RequestRefresh("A"); wait == 0 {
		t.Fatal("manual refresh not rate limited")
	}
	if len(h.r.DuePolls()) != 1 {
		t.Fatal("manual refresh did not schedule a poll")
	}
}

func TestPollAuthRejectionDoesNotBreakAffinity(t *testing.T) {
	h := newHarness(t)
	h.roster("claude", "A", "B")
	h.poll("A", weekly(0, 10*time.Hour))
	h.poll("B", weekly(0, 20*time.Hour))
	s := claudeSession("s")
	expect(t, h.pick("claude", "m", s, "A", "B"), OutcomeNewSession, "A")
	h.r.ApplyPoll("A", quota.Snapshot{}, "", &quota.Error{Code: "quota_http_401", Status: 401})
	expect(t, h.pick("claude", "m", s, "A", "B"), OutcomeAffinity, "A")
	expect(t, h.pick("claude", "m", claudeSession("new"), "A", "B"), OutcomeNewSession, "B")
}

func TestViewsAreRedacted(t *testing.T) {
	h := newHarness(t)
	h.r.SyncRoster([]RosterEntry{{ID: "claude-kishan@example.com.json", AuthIndex: "1", Provider: "claude", Name: "claude-kishan@example.com.json"}})
	sessions := h.r.Sessions()
	acc := h.r.Accounts()
	if len(acc) != 1 || acc[0].Display != "claude-k•••@•••.com.json" {
		t.Fatalf("display not redacted: %+v", acc)
	}
	_ = sessions
	h.ids["claude-kishan@example.com.json"] = "claude"
	h.poll("claude-kishan@example.com.json", weekly(0, time.Hour))
	h.pick("claude", "m", claudeSession("raw-session-id"), "claude-kishan@example.com.json")
	for _, s := range h.r.Sessions() {
		if s.Session == "raw-session-id" || len(s.Session) != 10 {
			t.Fatalf("session not redacted: %+v", s)
		}
	}
	for _, d := range h.r.Decisions("", "", 0) {
		if d.SessionKey == "claude:raw-session-id" {
			t.Fatal("raw session in decision")
		}
	}
}

func TestLowHeadroomSkippedForNewSessionsButKeptForBound(t *testing.T) {
	h := newHarness(t)
	h.roster("codex", "nearly", "fresh")
	h.poll("nearly", weekly(80, 2*time.Hour))
	h.poll("fresh", weekly(10, 100*time.Hour))
	s := codexSession("s")
	expect(t, h.pick("codex", "m", s, "nearly", "fresh"), OutcomeNewSession, "nearly")
	h.poll("nearly", weekly(97, 2*time.Hour))
	expect(t, h.pick("codex", "m", s, "nearly", "fresh"), OutcomeAffinity, "nearly")
	d := h.pick("codex", "m", codexSession("new"), "nearly", "fresh")
	expect(t, d, OutcomeNewSession, "fresh")
	if len(d.Skipped) != 1 || d.Skipped[0].Reason != "low_headroom:3" {
		t.Fatalf("skip reason: %+v", d.Skipped)
	}
}

func TestLowHeadroomUsedWhenNothingElseIsReady(t *testing.T) {
	h := newHarness(t)
	h.roster("codex", "a", "b")
	h.poll("a", weekly(98, 2*time.Hour))
	h.poll("b", weekly(97, 50*time.Hour))
	d := h.pick("codex", "m", codexSession("s"), "a", "b")
	expect(t, d, OutcomeNewSession, "a")
	if len(d.Skipped) != 0 {
		t.Fatalf("skipped: %+v", d.Skipped)
	}
}

func TestHumanDurationsMatchTheDashboard(t *testing.T) {
	for d, want := range map[time.Duration]string{
		-time.Minute: "0m", 59 * time.Minute: "59m", 3 * time.Hour: "3h 0m", 4*time.Hour + 40*time.Minute: "4h 40m",
		39*time.Hour + 48*time.Minute: "1d 15h", 48 * time.Hour: "2d 0h", 6*24*time.Hour + 4*time.Hour + 10*time.Minute: "6d 4h",
	} {
		if got := human(d); got != want {
			t.Errorf("human(%v) = %q, want %q", d, got, want)
		}
	}
}

func TestStaleCPACooldownRule(t *testing.T) {
	freshWindows := func() []quota.Window {
		return []quota.Window{
			{Kind: quota.FiveHour, UsedPercent: 50, ObservedAt: t0.Add(-time.Minute)},
			{Kind: quota.Weekly, UsedPercent: 40, ObservedAt: t0.Add(-time.Minute)},
		}
	}
	tests := []struct {
		name        string
		unavailable bool
		retryAfter  time.Duration
		windows     []quota.Window
		want        bool
	}{
		{name: "all conditions hold", unavailable: true, retryAfter: 11 * time.Minute, windows: freshWindows(), want: true},
		{name: "unavailable flag is required", retryAfter: 11 * time.Minute, windows: freshWindows()},
		{name: "retry must exceed ten minutes", unavailable: true, retryAfter: 10 * time.Minute, windows: freshWindows()},
		{name: "retry time is required", unavailable: true, windows: freshWindows()},
		{name: "five-hour window is required", unavailable: true, retryAfter: 11 * time.Minute, windows: []quota.Window{{Kind: quota.Weekly, UsedPercent: 40, ObservedAt: t0.Add(-time.Minute)}}},
		{name: "every observation must be younger than fifteen minutes", unavailable: true, retryAfter: 11 * time.Minute, windows: []quota.Window{
			{Kind: quota.FiveHour, UsedPercent: 50, ObservedAt: t0.Add(-time.Minute)},
			{Kind: quota.Weekly, UsedPercent: 40, ObservedAt: t0.Add(-15 * time.Minute)},
		}},
		{name: "fourteen-minute observations are fresh", unavailable: true, retryAfter: 11 * time.Minute, windows: []quota.Window{
			{Kind: quota.FiveHour, UsedPercent: 50, ObservedAt: t0.Add(-14 * time.Minute)},
			{Kind: quota.Weekly, UsedPercent: 40, ObservedAt: t0.Add(-14 * time.Minute)},
		}, want: true},
		{name: "every window must have more than 0.01 percent remaining", unavailable: true, retryAfter: 11 * time.Minute, windows: []quota.Window{
			{Kind: quota.FiveHour, UsedPercent: 50, ObservedAt: t0.Add(-time.Minute)},
			{Kind: quota.Weekly, UsedPercent: 99.99, ObservedAt: t0.Add(-time.Minute)},
		}},
		{name: "more than 0.01 percent remaining qualifies", unavailable: true, retryAfter: 11 * time.Minute, windows: []quota.Window{
			{Kind: quota.FiveHour, UsedPercent: 50, ObservedAt: t0.Add(-time.Minute)},
			{Kind: quota.Weekly, UsedPercent: 99.989, ObservedAt: t0.Add(-time.Minute)},
		}, want: true},
		{name: "a snapshot with no windows does not qualify", unavailable: true, retryAfter: 11 * time.Minute},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			h.r.SyncRoster([]RosterEntry{{ID: "A", Provider: "claude", Unavailable: tt.unavailable, NextRetry: t0.Add(tt.retryAfter)}})
			h.r.ApplyPoll("A", quota.Snapshot{Provider: "claude", PolledAt: t0, Identity: "identity-A", Windows: tt.windows}, "", nil)
			accounts := h.r.Accounts()
			if len(accounts) != 1 {
				t.Fatalf("account view count %d, want 1", len(accounts))
			}
			if accounts[0].CooldownStale != tt.want {
				t.Fatalf("cooldown_stale = %t, want %t", accounts[0].CooldownStale, tt.want)
			}
			if tt.want && (accounts[0].CooldownUntil == nil || !accounts[0].CooldownUntil.Equal(t0.Add(tt.retryAfter))) {
				t.Fatalf("cooldown_until = %v, want %s", accounts[0].CooldownUntil, t0.Add(tt.retryAfter))
			}
		})
	}
}

// CPA is authoritative about cooldowns: an offered account is usable even if
// the last roster sync still showed it cooling down.
func TestOfferedAccountIgnoresRosterCooldown(t *testing.T) {
	h := newHarness(t)
	h.ids["stale"] = "claude"
	h.ids["ready"] = "claude"
	h.r.SyncRoster([]RosterEntry{
		{ID: "stale", Provider: "claude", Unavailable: true, NextRetry: t0.Add(30 * time.Minute)},
		{ID: "ready", Provider: "claude"},
	})
	h.poll("stale", fiveHour(20, 4*time.Hour), weekly(30, 48*time.Hour))
	h.poll("ready", fiveHour(20, 4*time.Hour), weekly(30, 72*time.Hour))
	expect(t, h.pick("claude", "m", nil, "stale", "ready"), OutcomeNoSession, "stale")
}

func TestStaleCPACooldownAppearsInSkipReasons(t *testing.T) {
	for _, offered := range []struct {
		name       string
		candidates []string
	}{
		{name: "CPA omitted account", candidates: []string{"ready"}},
	} {
		t.Run(offered.name, func(t *testing.T) {
			h := newHarness(t)
			h.ids["stale"] = "claude"
			h.ids["ready"] = "claude"
			h.r.SyncRoster([]RosterEntry{
				{ID: "stale", Provider: "claude", Unavailable: true, NextRetry: t0.Add(30 * time.Minute)},
				{ID: "ready", Provider: "claude"},
			})
			h.poll("stale", fiveHour(20, 4*time.Hour), weekly(30, 48*time.Hour))
			h.poll("ready", fiveHour(20, 4*time.Hour), weekly(30, 72*time.Hour))

			d := h.pick("claude", "m", nil, offered.candidates...)
			expect(t, d, OutcomeNoSession, "ready")
			found := false
			for _, skipped := range d.Skipped {
				if skipped.AuthID == "stale" && skipped.Reason == "cooldown_stale" {
					found = true
				}
			}
			if !found {
				t.Fatalf("decision skipped list lacks stale CPA cooldown: %+v", d.Skipped)
			}

			nextUp := h.r.NextUp()
			foundNextUp := false
			for _, next := range nextUp {
				if next.Provider != "claude" {
					continue
				}
				foundNextUp = true
				if next.AuthID != "ready" || len(next.Skipped) != 1 || next.Skipped[0].AuthID != "stale" || next.Skipped[0].Reason != "cooldown_stale" {
					t.Fatalf("NextUp did not skip stale cooldown: %+v", next)
				}
			}
			if !foundNextUp {
				t.Fatal("NextUp did not return Claude")
			}

			var stale AccountView
			for _, account := range h.r.Accounts() {
				if account.ID == "stale" {
					stale = account
				}
			}
			if !stale.Unavailable || !stale.CooldownStale || stale.CooldownUntil == nil || !stale.CooldownUntil.Equal(t0.Add(30*time.Minute)) {
				t.Fatalf("router changed or lost CPA cooldown state: %+v", stale)
			}
		})
	}
}
