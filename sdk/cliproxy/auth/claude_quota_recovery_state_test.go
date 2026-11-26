package auth

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

func newModelBlockedClaudeAuth(t *testing.T, m *Manager, id string) *Auth {
	t.Helper()
	a := &Auth{ID: id, Provider: "claude", Status: StatusActive, Metadata: map[string]any{"access_token": "test-oauth-" + id}}
	if _, err := m.Register(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	delay := 5 * 24 * time.Hour
	m.MarkResult(context.Background(), Result{AuthID: id, Provider: "claude", Model: "claude-fable-5-2", RetryAfter: &delay, Error: &Error{HTTPStatus: 429, Message: "weekly quota"}})
	got, _ := m.GetByID(id)
	if blocked, _, _ := isAuthBlockedForModel(got, "claude-fable-5-2", time.Now()); !blocked {
		t.Fatal("fixture is not blocked")
	}
	return got
}

func TestClaudeQuotaRecoveryPreservesDisabledModels(t *testing.T) {
	withQuotaCooldownEnabled(t)
	m := NewManager(nil, nil, nil)
	newModelBlockedClaudeAuth(t, m, t.Name())
	m.mu.Lock()
	m.auths[t.Name()].ModelStates["claude-retired"] = &ModelState{Status: StatusDisabled, StatusMessage: "disabled by operator"}
	m.mu.Unlock()
	snapshot, _ := m.GetByID(t.Name())
	if err := m.restoreClaudeQuota(context.Background(), snapshot); err != nil {
		t.Fatal(err)
	}
	after, _ := m.GetByID(t.Name())
	if blocked, _, _ := isAuthBlockedForModel(after, "claude-fable-5-2", time.Now()); blocked {
		t.Fatal("recovery left the reset model blocked")
	}
	if s := after.ModelStates["claude-retired"]; s == nil || s.Status != StatusDisabled {
		t.Fatalf("recovery re-enabled a disabled model: %+v", s)
	}
}

func TestClaudeQuotaRecoveryIgnoresUnrelatedSuccess(t *testing.T) {
	withQuotaCooldownEnabled(t)
	m := NewManager(nil, nil, nil)
	snapshot := newModelBlockedClaudeAuth(t, m, t.Name())
	m.MarkResult(context.Background(), Result{AuthID: t.Name(), Provider: "claude", Model: "claude-other", Success: true})
	if err := m.restoreClaudeQuota(context.Background(), snapshot); err != nil {
		t.Fatal(err)
	}
	after, _ := m.GetByID(t.Name())
	if blocked, _, _ := isAuthBlockedForModel(after, "claude-fable-5-2", time.Now()); blocked {
		t.Fatal("a success on another model discarded a valid recovery")
	}
}

func TestClaudeQuotaRecoveryKeepsNewerModelFailure(t *testing.T) {
	withQuotaCooldownEnabled(t)
	m := NewManager(nil, nil, nil)
	snapshot := newModelBlockedClaudeAuth(t, m, t.Name())
	delay := 5 * 24 * time.Hour
	m.MarkResult(context.Background(), Result{AuthID: t.Name(), Provider: "claude", Model: "claude-other", RetryAfter: &delay, Error: &Error{HTTPStatus: 429, Message: "weekly quota"}})
	if err := m.restoreClaudeQuota(context.Background(), snapshot); err != nil {
		t.Fatal(err)
	}
	after, _ := m.GetByID(t.Name())
	if blocked, _, _ := isAuthBlockedForModel(after, "claude-other", time.Now()); !blocked {
		t.Fatal("an old usage response cleared a newer model failure")
	}
}

func TestClaudeQuotaStalledProbeDoesNotBlockOtherAccounts(t *testing.T) {
	withQuotaCooldownEnabled(t)
	m := NewManager(nil, nil, nil)
	stalled, healthy := t.Name()+"-stalled", t.Name()+"-healthy"
	newModelBlockedClaudeAuth(t, m, stalled)
	newModelBlockedClaudeAuth(t, m, healthy)
	saved := make(chan struct{}, 1)
	m.SetStore(&claudeQuotaRecoveryStore{saved: saved})
	release := make(chan struct{})
	var mu sync.Mutex
	calls := map[string]int{}
	m.RegisterExecutor(&claudeQuotaExecutor{request: func(r *http.Request) (*http.Response, error) {
		mu.Lock()
		calls[r.Header.Get("Authorization")]++
		mu.Unlock()
		if r.Header.Get("Authorization") == "Bearer test-oauth-"+stalled {
			select {
			case <-release:
			case <-r.Context().Done():
			}
			return nil, context.Canceled
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"five_hour":null,"seven_day":{"utilization":10}}`)), Request: r}, nil
	}})
	done := make(chan struct{})
	go func() {
		m.reconcileClaudeQuota(context.Background())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		close(release)
		t.Fatal("a stalled probe blocked the quota check of other accounts")
	}
	select {
	case <-saved:
	case <-time.After(3 * time.Second):
		close(release)
		t.Fatal("a stalled probe prevented recovery of another account")
	}
	m.reconcileClaudeQuota(context.Background())
	close(release)
	m.claudeQuotaProbes.wait()
	mu.Lock()
	defer mu.Unlock()
	if n := calls["Bearer test-oauth-"+stalled]; n != 1 {
		t.Fatalf("in-flight probe started again: calls=%d", n)
	}
}

func TestClaudeQuotaCheckProbesEveryCandidate(t *testing.T) {
	withQuotaCooldownEnabled(t)
	m := NewManager(nil, nil, nil)
	ids := make([]string, claudeQuotaProbeConcurrency+2)
	for i := range ids {
		ids[i] = fmt.Sprintf("%s-%d", t.Name(), i)
		newModelBlockedClaudeAuth(t, m, ids[i])
	}
	m.RegisterExecutor(&claudeQuotaExecutor{request: func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"five_hour":null,"seven_day":{"utilization":10}}`)), Request: r}, nil
	}})
	m.reconcileClaudeQuota(context.Background())
	m.claudeQuotaProbes.wait()
	for _, id := range ids {
		a, _ := m.GetByID(id)
		if blocked, _, _ := isAuthBlockedForModel(a, "claude-fable-5-2", time.Now()); blocked {
			t.Errorf("quota check skipped candidate %s", id)
		}
	}
}

func TestClaudeQuotaRecoveryKeepsNonQuotaCooldown(t *testing.T) {
	withQuotaCooldownEnabled(t)
	m := NewManager(nil, nil, nil)
	a := &Auth{ID: t.Name(), Provider: "claude", Status: StatusActive, Metadata: map[string]any{"access_token": "test-oauth"}}
	if _, err := m.Register(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	outage := 2 * time.Hour
	m.MarkResult(context.Background(), Result{AuthID: a.ID, Provider: "claude", Model: "claude-fable-5-2", RetryAfter: &outage, Error: &Error{HTTPStatus: 503, Message: "overloaded"}})
	quota := 5 * 24 * time.Hour
	m.MarkResult(context.Background(), Result{AuthID: a.ID, Provider: "claude", Model: "claude-fable-5-2", RetryAfter: &quota, Error: &Error{HTTPStatus: 429, Message: "weekly quota"}})
	snapshot, _ := m.GetByID(a.ID)
	if err := m.restoreClaudeQuota(context.Background(), snapshot); err != nil {
		t.Fatal(err)
	}
	after, _ := m.GetByID(a.ID)
	if blocked, _, _ := isAuthBlockedForModel(after, "claude-fable-5-2", time.Now()); !blocked {
		t.Fatal("recovery cleared a live 503 cooldown")
	}
}

func TestClaudeQuotaRecoveryKeepsRepeatedFailure(t *testing.T) {
	withQuotaCooldownEnabled(t)
	m := NewManager(nil, nil, nil)
	a := &Auth{ID: t.Name(), Provider: "claude", Status: StatusActive, Metadata: map[string]any{"access_token": "test-oauth"}}
	if _, err := m.Register(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	fail := Result{AuthID: a.ID, Provider: "claude", Model: "claude-fable-5-2", Error: &Error{HTTPStatus: 429, Message: "rate limited"}}
	m.MarkResult(context.Background(), fail)
	snapshot, _ := m.GetByID(a.ID)
	// Without a retry hint, a second 429 reuses the live deadline and backoff.
	m.MarkResult(context.Background(), fail)
	if again, _ := m.GetByID(a.ID); !again.ModelStates["claude-fable-5-2"].NextRetryAfter.Equal(snapshot.ModelStates["claude-fable-5-2"].NextRetryAfter) {
		t.Fatal("fixture: the second 429 changed the deadline")
	}
	if err := m.restoreClaudeQuota(context.Background(), snapshot); err != nil {
		t.Fatal(err)
	}
	after, _ := m.GetByID(t.Name())
	if blocked, _, _ := isAuthBlockedForModel(after, "claude-fable-5-2", time.Now()); !blocked {
		t.Fatal("an old usage response cleared a repeated failure")
	}
}

func TestClaudeQuotaRecoveryIgnoresSuccessThatClearsQuota(t *testing.T) {
	withQuotaCooldownEnabled(t)
	m := NewManager(nil, nil, nil)
	newModelBlockedClaudeAuth(t, m, t.Name())
	now := time.Now()
	m.mu.Lock()
	cur := m.auths[t.Name()]
	cur.ModelStates["claude-haiku"] = &ModelState{Status: StatusActive, Quota: QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: now.Add(-time.Minute), BackoffLevel: 3}}
	updateAggregatedAvailability(cur, now)
	m.mu.Unlock()
	snapshot, _ := m.GetByID(t.Name())
	m.MarkResult(context.Background(), Result{AuthID: t.Name(), Provider: "claude", Model: "claude-haiku", Success: true})
	if err := m.restoreClaudeQuota(context.Background(), snapshot); err != nil {
		t.Fatal(err)
	}
	after, _ := m.GetByID(t.Name())
	if blocked, _, _ := isAuthBlockedForModel(after, "claude-fable-5-2", time.Now()); blocked {
		t.Fatal("a success that cleared an expired quota discarded a valid recovery")
	}
}

func TestClaudeQuotaRecoveryKeepsRestoredCooldown(t *testing.T) {
	withQuotaCooldownEnabled(t)
	m := NewManager(nil, nil, nil)
	snapshot := newModelBlockedClaudeAuth(t, m, t.Name())
	now := time.Now()
	later := now.Add(6 * 24 * time.Hour)
	m.mu.Lock()
	restored := m.restoreCooldownRecordLocked(CooldownStateRecord{AuthID: t.Name(), Provider: "claude", Model: "claude-other", NextRetryAfter: later, Reason: "quota", Quota: QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: later}, LastError: &Error{HTTPStatus: 429, Message: "weekly quota"}, UpdatedAt: now}, now)
	m.mu.Unlock()
	if !restored {
		t.Fatal("fixture: the store record was not restored")
	}
	if err := m.restoreClaudeQuota(context.Background(), snapshot); err != nil {
		t.Fatal(err)
	}
	after, _ := m.GetByID(t.Name())
	if blocked, _, _ := isAuthBlockedForModel(after, "claude-other", time.Now()); !blocked {
		t.Fatal("an old usage response cleared a cooldown restored from the store")
	}
}

func TestClaudeQuotaCandidateKeepsForcedCooldown(t *testing.T) {
	now := time.Now()
	for _, forced := range []bool{false, true} {
		a := &Auth{Provider: "claude", Metadata: map[string]any{"access_token": "test"}, ModelStates: map[string]*ModelState{"fable": {Status: StatusError, Unavailable: true, NextRetryAfter: now.Add(time.Hour), Quota: QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: now.Add(time.Hour)}}}}
		if forced {
			a.ModelStates["fable"].quotaForceCooldownUntil = now.Add(time.Hour)
		}
		if got := claudeQuotaCandidate(a); got == forced {
			t.Errorf("candidate=%v with forced cooldown=%v", got, forced)
		}
	}
}

func TestClaudeQuotaRecoveryIgnoresExpiredSiblingError(t *testing.T) {
	withQuotaCooldownEnabled(t)
	m := NewManager(nil, nil, nil)
	a := &Auth{ID: t.Name(), Provider: "claude", Status: StatusActive, Metadata: map[string]any{"access_token": "test-oauth"}}
	if _, err := m.Register(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	outage := time.Hour
	m.MarkResult(context.Background(), Result{AuthID: a.ID, Provider: "claude", Model: "claude-haiku", RetryAfter: &outage, Error: &Error{HTTPStatus: 503, Message: "overloaded"}})
	quota := 5 * 24 * time.Hour
	m.MarkResult(context.Background(), Result{AuthID: a.ID, Provider: "claude", Model: "claude-fable-5-2", CredentialScope: true, RetryAfter: &quota, Error: &Error{HTTPStatus: 429, Message: "weekly quota"}})
	// Move the 503 deadlines into the past, as if the hour has gone by.
	past := time.Now().Add(-time.Minute)
	m.mu.Lock()
	cur := m.auths[a.ID]
	for _, s := range []*ModelState{cur.ModelStates["claude-haiku"], cur.ModelStates["claude-haiku"].credentialQuotaShadow} {
		if s == nil {
			continue
		}
		s.NextRetryAfter = past
		if !s.nonQuotaCooldownUntil.IsZero() {
			s.nonQuotaCooldownUntil = past
		}
		if !s.nonCapacityCooldownUntil.IsZero() {
			s.nonCapacityCooldownUntil = past
		}
	}
	if !cur.nonQuotaCooldownUntil.IsZero() {
		cur.nonQuotaCooldownUntil = past
	}
	if !cur.credentialRetryAfter.IsZero() {
		cur.credentialRetryAfter = past
	}
	if !cur.nonAuthCredentialRetryAfter.IsZero() {
		cur.nonAuthCredentialRetryAfter = past
	}
	m.mu.Unlock()
	snapshot, _ := m.GetByID(a.ID)
	if got := snapshot.ModelStates["claude-haiku"].LastError; got == nil || got.HTTPStatus != 503 {
		t.Fatalf("fixture: haiku LastError=%+v, want the old 503", got)
	}
	if err := m.restoreClaudeQuota(context.Background(), snapshot); err != nil {
		t.Fatal(err)
	}
	after, _ := m.GetByID(a.ID)
	if after.Quota.Exceeded {
		t.Fatal("an expired 503 on another model kept the account blocked")
	}
}

func TestClaudeQuotaRecoveryKeepsFailureAcrossReplacement(t *testing.T) {
	withQuotaCooldownEnabled(t)
	m := NewManager(nil, nil, nil)
	a := &Auth{ID: t.Name(), Provider: "claude", Status: StatusActive, Metadata: map[string]any{"access_token": "test-oauth"}}
	if _, err := m.Register(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	reload := func() {
		if _, err := m.Update(context.Background(), &Auth{ID: a.ID, Provider: "claude", Status: StatusActive, Metadata: map[string]any{"access_token": "test-oauth"}}); err != nil {
			t.Fatal(err)
		}
	}
	fail := Result{AuthID: a.ID, Provider: "claude", Model: "claude-fable-5-2", Error: &Error{HTTPStatus: 429, Message: "rate limited"}}
	m.MarkResult(context.Background(), fail)
	reload()
	snapshot, _ := m.GetByID(a.ID)
	m.MarkResult(context.Background(), fail)
	reload()
	if err := m.restoreClaudeQuota(context.Background(), snapshot); err != nil {
		t.Fatal(err)
	}
	after, _ := m.GetByID(a.ID)
	if blocked, _, _ := isAuthBlockedForModel(after, "claude-fable-5-2", time.Now()); !blocked {
		t.Fatal("a reload reset the failure count and an old usage response cleared a newer failure")
	}
}
