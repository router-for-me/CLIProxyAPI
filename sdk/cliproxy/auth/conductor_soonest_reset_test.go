package auth

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	internallogging "github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// soonestResetExecutor succeeds and reports the configured weekly reset for
// each auth as Codex response headers.
type soonestResetExecutor struct {
	mu      sync.Mutex
	resets  map[string]time.Duration
	pickedN []string
}

func (*soonestResetExecutor) Identifier() string { return "codex" }

func (e *soonestResetExecutor) Execute(ctx context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.mu.Lock()
	e.pickedN = append(e.pickedN, auth.ID)
	after, ok := e.resets[auth.ID]
	e.mu.Unlock()
	if ok {
		internallogging.SetResponseHeaders(ctx, http.Header{
			"X-Codex-Primary-Used-Percent":        []string{"10"},
			"X-Codex-Primary-Window-Minutes":      []string{"10080"},
			"X-Codex-Primary-Reset-After-Seconds": []string{strconv.Itoa(int(after.Seconds()))},
		})
	}
	return cliproxyexecutor.Response{Payload: []byte("{}")}, nil
}

func (e *soonestResetExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, &Error{HTTPStatus: http.StatusNotImplemented, Message: "not implemented"}
}

func (*soonestResetExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (*soonestResetExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusNotImplemented, Message: "not implemented"}
}

func (*soonestResetExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, &Error{HTTPStatus: http.StatusNotImplemented, Message: "not implemented"}
}

func (e *soonestResetExecutor) setReset(id string, after time.Duration) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.resets[id] = after
}

func (e *soonestResetExecutor) last() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.pickedN) == 0 {
		return ""
	}
	return e.pickedN[len(e.pickedN)-1]
}

func TestManagerSoonestResetRoutesByObservedResponseHeaders(t *testing.T) {
	executor := &soonestResetExecutor{resets: map[string]time.Duration{}}
	manager := NewManager(nil, &SoonestResetSelector{}, nil)
	manager.RegisterExecutor(executor)

	model := "gpt-soonest-reset-routing"
	ids := []string{"soonest-reset-a", "soonest-reset-b"}
	for _, id := range ids {
		if _, errRegister := manager.Register(context.Background(), &Auth{ID: id, Provider: "codex", Status: StatusActive}); errRegister != nil {
			t.Fatalf("Register(%s) error = %v", id, errRegister)
		}
		registry.GetGlobalRegistry().RegisterClient(id, "codex", []*registry.ModelInfo{{ID: model}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
	}

	execute := func() string {
		t.Helper()
		ctx := internallogging.WithResponseHeadersHolder(context.Background())
		if _, errExecute := manager.Execute(ctx, []string{"codex"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{}); errExecute != nil {
			t.Fatalf("Execute() error = %v", errExecute)
		}
		return executor.last()
	}

	// Nothing observed yet: fill-first order. The response reports a reset for a in 2 days.
	executor.setReset(ids[0], 2*24*time.Hour)
	if got := execute(); got != ids[0] {
		t.Fatalf("first pick = %q, want %q", got, ids[0])
	}

	// b becomes known with a later reset (for example a snapshot restored after a restart).
	if _, errUpdate := manager.Update(context.Background(), &Auth{
		ID:       ids[1],
		Provider: "codex",
		Status:   StatusActive,
		Quota: QuotaState{
			ObservedAt: time.Now(),
			Signals: map[string]string{
				"X-Codex-Primary-Window-Minutes":      "10080",
				"X-Codex-Primary-Reset-After-Seconds": strconv.Itoa(int((5 * 24 * time.Hour).Seconds())),
			},
		},
	}); errUpdate != nil {
		t.Fatalf("Update(%s) error = %v", ids[1], errUpdate)
	}
	// a's observed reset (2 days) is sooner than b's (5 days), so a stays selected.
	// a's window then resets upstream and the response reports a fresh 7-day window.
	executor.setReset(ids[0], 7*24*time.Hour)
	if got := execute(); got != ids[0] {
		t.Fatalf("second pick = %q, want %q (observed 2d reset beats 5d)", got, ids[0])
	}
	// The new observation moves a behind b.
	if got := execute(); got != ids[1] {
		t.Fatalf("third pick = %q, want %q (observed 7d reset loses to 5d)", got, ids[1])
	}
}

func TestMarkResultAnchorsQuotaObservationAtHeaderReceipt(t *testing.T) {
	manager := NewManager(nil, &SoonestResetSelector{}, nil)
	manager.RegisterExecutor(&soonestResetExecutor{resets: map[string]time.Duration{}})
	const id = "soonest-reset-anchor"
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: id, Provider: "codex", Status: StatusActive}); errRegister != nil {
		t.Fatalf("Register(%s) error = %v", id, errRegister)
	}

	// Headers arrived when the stream opened a minute ago; the result is recorded after it drains.
	ctx := internallogging.WithResponseHeadersHolder(context.Background())
	receivedAt := time.Now().Add(-time.Minute)
	internallogging.SetResponseHeadersAt(ctx, http.Header{
		"X-Codex-Primary-Window-Minutes":      []string{"10080"},
		"X-Codex-Primary-Reset-After-Seconds": []string{"3600"},
	}, receivedAt)
	manager.MarkResult(ctx, Result{AuthID: id, Provider: "codex", Success: true})

	auth, ok := manager.GetByID(id)
	if !ok || auth == nil {
		t.Fatalf("GetByID(%s) failed", id)
	}
	if !auth.Quota.ObservedAt.Equal(receivedAt) {
		t.Fatalf("ObservedAt = %v, want header receipt %v", auth.Quota.ObservedAt, receivedAt)
	}
	reset, okReset := quotaWindowResetAt(auth, receivedAt)
	if !okReset || !reset.Equal(receivedAt.Add(time.Hour)) {
		t.Fatalf("reset = %v, %v; want %v", reset, okReset, receivedAt.Add(time.Hour))
	}
}

func TestManagerSoonestResetLearnsEveryCredentialAfterRestart(t *testing.T) {
	executor := &soonestResetExecutor{resets: map[string]time.Duration{
		"soonest-reset-fresh-a": 6 * 24 * time.Hour,
		"soonest-reset-fresh-b": 3 * time.Hour,
	}}
	manager := NewManager(nil, &SoonestResetSelector{}, nil)
	manager.RegisterExecutor(executor)

	model := "gpt-soonest-reset-fresh"
	for _, id := range []string{"soonest-reset-fresh-a", "soonest-reset-fresh-b"} {
		if _, errRegister := manager.Register(context.Background(), &Auth{ID: id, Provider: "codex", Status: StatusActive}); errRegister != nil {
			t.Fatalf("Register(%s) error = %v", id, errRegister)
		}
		registry.GetGlobalRegistry().RegisterClient(id, "codex", []*registry.ModelInfo{{ID: model}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
	}

	var picks []string
	for i := 0; i < 4; i++ {
		ctx := internallogging.WithResponseHeadersHolder(context.Background())
		if _, errExecute := manager.Execute(ctx, []string{"codex"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{}); errExecute != nil {
			t.Fatalf("Execute() error = %v", errExecute)
		}
		picks = append(picks, executor.last())
	}
	// Both credentials are probed once, then the sooner reset (b) wins.
	want := []string{"soonest-reset-fresh-a", "soonest-reset-fresh-b", "soonest-reset-fresh-b", "soonest-reset-fresh-b"}
	for i := range want {
		if picks[i] != want[i] {
			t.Fatalf("picks = %v, want %v", picks, want)
		}
	}
}

func TestMarkResultRebasesRelativeResetAcrossPartialMerges(t *testing.T) {
	manager := NewManager(nil, &SoonestResetSelector{}, nil)
	manager.RegisterExecutor(&soonestResetExecutor{resets: map[string]time.Duration{}})
	const id = "soonest-reset-merge"
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: id, Provider: "codex", Status: StatusActive}); errRegister != nil {
		t.Fatalf("Register(%s) error = %v", id, errRegister)
	}

	ctx := internallogging.WithResponseHeadersHolder(context.Background())
	firstReceipt := time.Now().Add(-10 * time.Minute)
	internallogging.SetResponseHeadersAt(ctx, http.Header{
		"X-Codex-Primary-Window-Minutes":      []string{"10080"},
		"X-Codex-Primary-Reset-After-Seconds": []string{"3600"},
	}, firstReceipt)
	// A later websocket quota frame merges only the usage percentage.
	internallogging.MergeResponseHeadersAt(ctx, http.Header{"X-Codex-Primary-Used-Percent": []string{"20"}}, firstReceipt.Add(5*time.Minute))
	manager.MarkResult(ctx, Result{AuthID: id, Provider: "codex", Success: true})

	auth, ok := manager.GetByID(id)
	if !ok || auth == nil {
		t.Fatalf("GetByID(%s) failed", id)
	}
	reset, okReset := quotaWindowReset(auth)
	want := firstReceipt.Add(time.Hour)
	if !okReset || !reset.Equal(want) {
		t.Fatalf("reset = %v, %v; want %v (anchored at the first receipt)", reset, okReset, want)
	}
}

func TestMarkResultKeepsNewerResetOverLateUnrelatedMerge(t *testing.T) {
	for name, lateHeader := range map[string]string{
		"credits":          "X-Codex-Credits-Balance",
		"additional limit": "X-Codex-Additional-Spark-Primary-Reset-At",
	} {
		t.Run(name, func(t *testing.T) {
			manager := NewManager(nil, &SoonestResetSelector{}, nil)
			id := "soonest-reset-rollover-" + name
			if _, errRegister := manager.Register(context.Background(), &Auth{ID: id, Provider: "codex", Status: StatusActive}); errRegister != nil {
				t.Fatalf("Register(%s) error = %v", id, errRegister)
			}

			t1 := time.Now().Add(-30 * time.Minute)
			t2 := t1.Add(10 * time.Minute)
			t3 := t2.Add(10 * time.Minute)
			resetHeaders := func(after time.Duration) http.Header {
				return http.Header{
					"X-Codex-Primary-Window-Minutes":      []string{"10080"},
					"X-Codex-Primary-Reset-After-Seconds": []string{strconv.Itoa(int(after.Seconds()))},
				}
			}

			// The older attempt saw the pre-rollover reset at t1 and is still streaming.
			older := internallogging.WithResponseHeadersHolder(context.Background())
			internallogging.SetResponseHeadersAt(older, resetHeaders(time.Hour), t1)

			// A newer attempt saw the post-rollover window at t2 and finished first.
			newer := internallogging.WithResponseHeadersHolder(context.Background())
			internallogging.SetResponseHeadersAt(newer, resetHeaders(7*24*time.Hour), t2)
			manager.MarkResult(newer, Result{AuthID: id, Provider: "codex", Success: true})

			// The older attempt merges one unrelated header at t3, then finishes.
			internallogging.MergeResponseHeadersAt(older, http.Header{lateHeader: []string{"1793810327"}}, t3)
			manager.MarkResult(older, Result{AuthID: id, Provider: "codex", Success: true})

			auth, ok := manager.GetByID(id)
			if !ok || auth == nil {
				t.Fatalf("GetByID(%s) failed", id)
			}
			reset, okReset := quotaWindowReset(auth)
			if want := t2.Add(7 * 24 * time.Hour); !okReset || !reset.Equal(want) || !auth.Quota.ObservedAt.Equal(t2) {
				t.Fatalf("reset = %v (observed %v); want %v observed at %v", reset, auth.Quota.ObservedAt, want, t2)
			}
		})
	}
}

func TestUpdateClearsQuotaObservationWhenAccountChanges(t *testing.T) {
	const oldEmail, oldToken = "first@example.com", "old-token"
	resetAt := time.Now().Add(48 * time.Hour)
	reload := func(manager *Manager, id, email, token string) *Auth {
		t.Helper()
		metadata := map[string]any{"access_token": token}
		if email != "" {
			metadata["email"] = email
		}
		data := []byte(`{"type":"codex","email":"` + email + `","access_token":"` + token + `"}`)
		path := filepath.Join(t.TempDir(), id+".json")
		if errWrite := os.WriteFile(path, data, 0o600); errWrite != nil {
			t.Fatalf("write auth file: %v", errWrite)
		}
		snapshot := &Auth{ID: id, Provider: "codex", Status: StatusActive, Metadata: metadata}
		snapshot.BindFileSnapshot(path, data)
		if _, errUpdate := manager.Update(context.Background(), snapshot); errUpdate != nil {
			t.Fatalf("Update(%s) error = %v", id, errUpdate)
		}
		auth, ok := manager.GetByID(id)
		if !ok || auth == nil {
			t.Fatalf("GetByID(%s) failed", id)
		}
		return auth
	}

	for i, tc := range []struct {
		name      string
		newEmail  string
		newToken  string
		cooling   bool
		wantKnown bool
	}{
		{name: "same account keeps the snapshot", newEmail: oldEmail, newToken: oldToken, wantKnown: true},
		{name: "refreshed token on the same account keeps the snapshot", newEmail: oldEmail, newToken: "new-token", wantKnown: true},
		{name: "different account drops the snapshot", newEmail: "second@example.com", newToken: oldToken},
		{name: "different account drops the snapshot during a cooldown", newEmail: "second@example.com", newToken: oldToken, cooling: true},
		{name: "replaced token without an identity drops the snapshot", newToken: "new-token"},
		{name: "unchanged token without an identity keeps the snapshot", newToken: oldToken, wantKnown: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manager := NewManager(nil, &SoonestResetSelector{}, nil)
			id := "soonest-reset-account-" + strconv.Itoa(i)
			quota := codexWeeklySignals(resetAt)
			if tc.cooling {
				quota.Exceeded = true
				quota.Reason = "credential_quota"
				quota.NextRecoverAt = resetAt
			}
			if _, errRegister := manager.Register(context.Background(), &Auth{
				ID:       id,
				Provider: "codex",
				Status:   StatusActive,
				Metadata: map[string]any{"email": oldEmail, "access_token": oldToken},
				Quota:    quota,
			}); errRegister != nil {
				t.Fatalf("Register(%s) error = %v", id, errRegister)
			}

			auth := reload(manager, id, tc.newEmail, tc.newToken)
			if _, known := quotaWindowReset(auth); known != tc.wantKnown {
				t.Fatalf("reset known after reload = %v, want %v; quota=%#v", known, tc.wantKnown, auth.Quota)
			}
		})
	}
}

func TestMarkResultPrefersNewerRelativeResetOverRetainedAbsolute(t *testing.T) {
	manager := NewManager(nil, &SoonestResetSelector{}, nil)
	const id = "soonest-reset-sibling"
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: id, Provider: "codex", Status: StatusActive}); errRegister != nil {
		t.Fatalf("Register(%s) error = %v", id, errRegister)
	}

	t1 := time.Now().Add(-20 * time.Minute)
	t2 := t1.Add(10 * time.Minute)
	ctx := internallogging.WithResponseHeadersHolder(context.Background())
	// The first frame describes the old window with both representations.
	internallogging.SetResponseHeadersAt(ctx, http.Header{
		"X-Codex-Primary-Window-Minutes":      []string{"10080"},
		"X-Codex-Primary-Reset-At":            []string{strconv.FormatInt(t1.Add(time.Hour).Unix(), 10)},
		"X-Codex-Primary-Reset-After-Seconds": []string{"3600"},
	}, t1)
	// After the rollover a quota frame updates only the relative value.
	internallogging.MergeResponseHeadersAt(ctx, http.Header{
		"X-Codex-Primary-Reset-After-Seconds": []string{strconv.Itoa(int((7 * 24 * time.Hour).Seconds()))},
	}, t2)
	manager.MarkResult(ctx, Result{AuthID: id, Provider: "codex", Success: true})

	auth, ok := manager.GetByID(id)
	if !ok || auth == nil {
		t.Fatalf("GetByID(%s) failed", id)
	}
	if _, stale := auth.Quota.Signals["X-Codex-Primary-Reset-At"]; stale {
		t.Fatalf("superseded Reset-At kept: %#v", auth.Quota.Signals)
	}
	reset, okReset := quotaWindowReset(auth)
	if want := t2.Add(7 * 24 * time.Hour); !okReset || !reset.Equal(want) {
		t.Fatalf("reset = %v, %v; want %v", reset, okReset, want)
	}
}

func TestMarkResultOrdersSnapshotsByTheScoredWindow(t *testing.T) {
	manager := NewManager(nil, &SoonestResetSelector{}, nil)
	const id = "soonest-reset-scored-window"
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: id, Provider: "codex", Status: StatusActive}); errRegister != nil {
		t.Fatalf("Register(%s) error = %v", id, errRegister)
	}

	t1 := time.Now().Add(-30 * time.Minute)
	t2 := t1.Add(10 * time.Minute)
	t3 := t2.Add(10 * time.Minute)
	weekly := func(after time.Duration) http.Header {
		return http.Header{
			"X-Codex-Secondary-Window-Minutes":      []string{"10080"},
			"X-Codex-Secondary-Reset-After-Seconds": []string{strconv.Itoa(int(after.Seconds()))},
		}
	}

	// The older attempt holds the previous weekly reset from t1.
	older := internallogging.WithResponseHeadersHolder(context.Background())
	internallogging.SetResponseHeadersAt(older, weekly(time.Hour), t1)

	// A newer attempt stores the new weekly reset at t2.
	newer := internallogging.WithResponseHeadersHolder(context.Background())
	internallogging.SetResponseHeadersAt(newer, weekly(7*24*time.Hour), t2)
	manager.MarkResult(newer, Result{AuthID: id, Provider: "codex", Success: true})

	// The older attempt then merges only a shorter primary window at t3.
	internallogging.MergeResponseHeadersAt(older, http.Header{
		"X-Codex-Primary-Window-Minutes":      []string{"300"},
		"X-Codex-Primary-Reset-After-Seconds": []string{"3600"},
	}, t3)
	manager.MarkResult(older, Result{AuthID: id, Provider: "codex", Success: true})

	auth, ok := manager.GetByID(id)
	if !ok || auth == nil {
		t.Fatalf("GetByID(%s) failed", id)
	}
	reset, okReset := quotaWindowReset(auth)
	if want := t2.Add(7 * 24 * time.Hour); !okReset || !reset.Equal(want) || !auth.Quota.ObservedAt.Equal(t2) {
		t.Fatalf("reset = %v (observed %v); want %v observed at %v", reset, auth.Quota.ObservedAt, want, t2)
	}
}
