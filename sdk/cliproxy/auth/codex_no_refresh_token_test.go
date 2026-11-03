package auth

import (
	"context"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

type codexRenewalEvaluator struct{}

func (codexRenewalEvaluator) ShouldRefresh(time.Time, *Auth) bool { return true }

func TestCodexAutoRefreshRequiresRenewalCredential(t *testing.T) {
	now := time.Now()
	lead := CodexOuterRefreshLead
	setRefreshLeadFactory(t, "codex", func() *time.Duration { return &lead })
	for _, tc := range []struct {
		name    string
		token   any
		siwc    map[string]any
		home    bool
		runtime any
		want    bool
	}{
		{name: "missing RT"},
		{name: "empty RT", token: ""},
		{name: "whitespace RT", token: " \t"},
		{name: "non-string RT", token: 123},
		{name: "outer RT", token: "refresh", want: true},
		{name: "SIWC renewal", siwc: map[string]any{"refresh_token": "siwc-refresh", "client_id": "client"}, want: true},
		{name: "SIWC missing client", siwc: map[string]any{"refresh_token": "siwc-refresh"}},
		{name: "SIWC missing RT", siwc: map[string]any{"client_id": "client"}},
		{name: "Home renewal", home: true, want: true},
		{name: "runtime renewal", runtime: codexRenewalEvaluator{}, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, disabled := range []bool{false, true} {
				m := NewManager(nil, &RoundRobinSelector{}, nil)
				cfg := &internalconfig.Config{}
				cfg.Home.Enabled = tc.home
				m.SetConfig(cfg)
				a := &Auth{ID: "codex-no-rt", Provider: "codex", Disabled: disabled, Runtime: tc.runtime, Metadata: map[string]any{"access_token": "access", "expired": now.Add(-time.Hour).Format(time.RFC3339)}}
				if tc.token != nil {
					a.Metadata["refresh_token"] = tc.token
				}
				if tc.siwc != nil {
					a.Metadata["siwc"] = tc.siwc
				}
				if got := m.shouldRefresh(a, now); got != tc.want {
					t.Fatalf("disabled=%v shouldRefresh=%v, want %v", disabled, got, tc.want)
				}
				if _, got := m.nextRefreshCheckAt(now, a, time.Second); got != tc.want {
					t.Fatalf("disabled=%v scheduled=%v, want %v", disabled, got, tc.want)
				}
				a.NextRefreshAfter = now.Add(time.Minute)
				if _, got := m.nextRefreshCheckAt(now, a, time.Second); got != tc.want {
					t.Fatalf("disabled=%v pending scheduled=%v, want %v", disabled, got, tc.want)
				}
			}
		})
	}
}

func TestCodexNoRefreshTokenRemovedFromAutoRefreshQueue(t *testing.T) {
	now := time.Now()
	lead := CodexOuterRefreshLead
	setRefreshLeadFactory(t, "codex", func() *time.Duration { return &lead })
	m := NewManager(nil, &RoundRobinSelector{}, nil)
	m.RegisterExecutor(&countingRefreshExecutor{id: "codex"})
	a, err := m.Register(context.Background(), &Auth{ID: "access-only", Provider: "codex", Metadata: map[string]any{"access_token": "access", "expired": now.Add(-time.Hour).Format(time.RFC3339)}})
	if err != nil {
		t.Fatal(err)
	}
	loop := newAuthAutoRefreshLoop(m, time.Second, 1)
	loop.rebuild(now)
	if _, ok := loop.peek(); ok {
		t.Fatal("access-only credential queued by rebuild")
	}
	loop.upsert(a.ID, now)
	loop.queueReschedule(a.ID)
	loop.applyDirty(now)
	if _, ok := loop.peek(); ok {
		t.Fatal("access-only credential retained by dirty reschedule")
	}
	loop.upsert(a.ID, now)
	loop.handleDue(context.Background(), now)
	if _, ok := loop.peek(); ok {
		t.Fatal("access-only credential requeued by due handler")
	}
	if len(loop.jobs) != 0 {
		t.Fatal("access-only credential submitted for refresh")
	}

	// A later credential update must restore scheduling without restarting the loop.
	a.Metadata["refresh_token"] = "restored-refresh"
	if _, errUpdate := m.Update(context.Background(), a); errUpdate != nil {
		t.Fatal(errUpdate)
	}
	loop.queueReschedule(a.ID)
	loop.applyDirty(now)
	if next, ok := loop.peek(); !ok || next.After(now) {
		t.Fatal("renewable credential was not scheduled after update")
	}
	loop.handleDue(context.Background(), now)
	if len(loop.jobs) != 1 {
		t.Fatal("renewable credential was not submitted for refresh")
	}
}
