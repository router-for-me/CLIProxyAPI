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
