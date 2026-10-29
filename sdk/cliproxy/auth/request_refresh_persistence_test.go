package auth

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func requestRefreshReleaseBackoff(m *Manager, id string) {
	state := m.authRefreshState(id)
	state.stateMu.Lock()
	if state.outcome != nil {
		state.outcome.retryAt = time.Unix(1, 0)
	}
	state.stateMu.Unlock()
}

func requestRefreshRotate(a *Auth) *Auth {
	a.Metadata["access_token"] = "request-refresh-rotated"
	a.Metadata["refresh_token"] = "request-refresh-rotated-refresh"
	a.Metadata["expired"] = "2099-01-01T00:00:00Z"
	return a
}

func TestRequestRefreshSaveFailureWave(t *testing.T) {
	m, a, store, e := demandManager(t, time.Now().Add(-time.Hour))
	failure := errors.New("exact save failure")
	store.mu.Lock()
	store.fail = failure
	store.mu.Unlock()
	started, release := make(chan struct{}), make(chan struct{})
	e.refresh = func(_ context.Context, next *Auth) (*Auth, error) {
		close(started)
		<-release
		return requestRefreshRotate(next), nil
	}
	const n = 12
	results := make(chan error, n)
	waiters := make([]*demandWaitContext, n)
	for i := range waiters {
		waiters[i] = &demandWaitContext{Context: context.Background(), waiting: make(chan struct{})}
		go func(ctx context.Context) {
			_, err := m.Execute(ctx, []string{a.Provider}, cliproxyexecutor.Request{Model: "demand-model"}, cliproxyexecutor.Options{})
			results <- err
		}(waiters[i])
	}
	<-started
	for _, waiter := range waiters {
		<-waiter.waiting
	}
	close(release)
	for range n {
		if err := <-results; !errors.Is(err, failure) {
			t.Errorf("cohort lost the actual save error: %v", err)
		}
	}
	current, _ := m.GetByID(a.ID)
	store.mu.Lock()
	saves := store.saves
	store.fail = nil
	store.mu.Unlock()
	if current.Metadata["access_token"] != "old" || e.refreshes.Load() != 1 || e.calls.Load() != 0 || saves != 2 {
		t.Fatalf("unsaved cohort executed or rotated twice: refresh=%d inference=%d saves=%d current=%v", e.refreshes.Load(), e.calls.Load(), saves, current.Metadata)
	}
	requestRefreshReleaseBackoff(m, a.ID)
	fresh, err := m.EnsureRequestAuth(context.Background(), current, "demand-model")
	if err != nil || fresh.Metadata["refresh_token"] != "request-refresh-rotated-refresh" {
		t.Fatalf("save-only recovery failed: %v", err)
	}
	store.mu.Lock()
	saves = store.saves
	store.mu.Unlock()
	if e.refreshes.Load() != 1 || e.calls.Load() != 0 || saves != 3 {
		t.Fatalf("save-only recovery reused old refresh: refresh=%d inference=%d saves=%d", e.refreshes.Load(), e.calls.Load(), saves)
	}
	if _, err := m.Execute(context.Background(), []string{a.Provider}, cliproxyexecutor.Request{Model: "demand-model"}, cliproxyexecutor.Options{}); err != nil || e.calls.Load() != 1 || e.refreshes.Load() != 1 {
		t.Fatalf("saved pair was not executable: %v", err)
	}
	t.Logf("cohort=%d actual-error preserved; rotation=1; inference before save=0; save-only recovery succeeded", n)
}

func TestRequestRefreshWatcherPendingCollision(t *testing.T) {
	for _, observed := range []string{"pending", "old", "distinct-login"} {
		t.Run(observed, func(t *testing.T) {
			m, a, store, e := demandManager(t, time.Now().Add(30*time.Second))
			path := filepath.Join(t.TempDir(), "synthetic.json")
			a.Attributes = map[string]string{AttributePath: path}
			var err error
			a, err = m.Update(context.Background(), a)
			if err != nil {
				t.Fatal(err)
			}
			failure := errors.New("watcher unsaved rotation")
			store.mu.Lock()
			store.fail = failure
			store.mu.Unlock()
			e.refresh = func(_ context.Context, next *Auth) (*Auth, error) { return requestRefreshRotate(next), nil }
			if _, err := m.EnsureRequestAuth(context.Background(), a, "demand-model"); !errors.Is(err, failure) {
				t.Fatal(err)
			}
			snapshot := a.Clone()
			if observed == "pending" {
				requestRefreshRotate(snapshot)
			} else if observed == "distinct-login" {
				snapshot.Metadata["access_token"] = "request-refresh-login"
				snapshot.Metadata["refresh_token"] = "request-refresh-login-refresh"
				snapshot.Metadata["expired"] = "2099-01-01T00:00:00Z"
			}
			snapshot.Generation, snapshot.RegistrationEpoch, snapshot.CredentialVersion = 0, 0, 0
			raw, err := json.Marshal(snapshot.Metadata)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			snapshot.BindFileSnapshot(path, raw)
			if !snapshot.HasFileSnapshot() || !snapshot.FileSnapshotMatchesDisk() {
				t.Fatal("fixture never reached the real file-snapshot update guard")
			}
			if _, err := m.Update(WithSkipPersist(context.Background()), snapshot); err != nil {
				t.Fatal(err)
			}
			current, _ := m.GetByID(a.ID)
			if observed == "distinct-login" {
				if _, err := m.Execute(context.Background(), []string{a.Provider}, cliproxyexecutor.Request{Model: "demand-model"}, cliproxyexecutor.Options{}); err != nil {
					t.Fatal(err)
				}
				if current.Metadata["access_token"] != "request-refresh-login" || e.refreshes.Load() != 1 || e.calls.Load() != 1 {
					t.Fatal("distinct login did not retire pending state")
				}
				return
			}
			_, err = m.Execute(context.Background(), []string{a.Provider}, cliproxyexecutor.Request{Model: "demand-model"}, cliproxyexecutor.Options{})
			if !errors.Is(err, failure) || current.Metadata["access_token"] != "old" || e.refreshes.Load() != 1 || e.calls.Load() != 0 {
				t.Fatalf("watcher adopted a pair without a save receipt: err=%v refresh=%d inference=%d current=%v", err, e.refreshes.Load(), e.calls.Load(), current.Metadata)
			}
			store.mu.Lock()
			store.fail = nil
			store.mu.Unlock()
			requestRefreshReleaseBackoff(m, a.ID)
			if _, err := m.Execute(context.Background(), []string{a.Provider}, cliproxyexecutor.Request{Model: "demand-model"}, cliproxyexecutor.Options{}); err != nil {
				t.Fatal(err)
			}
			current, _ = m.GetByID(a.ID)
			if e.refreshes.Load() != 1 || e.calls.Load() != 1 || current.Metadata["refresh_token"] != "request-refresh-rotated-refresh" {
				t.Fatal("watcher recovery re-consumed the original refresh token")
			}
		})
	}
}

func TestRequestRefreshStaleFailureReplacement(t *testing.T) {
	for _, mutation := range []string{"update-distinct", "register-distinct", "register-identical"} {
		for _, rejection := range []bool{false, true} {
			t.Run(mutation+map[bool]string{false: "/transient", true: "/invalid-grant"}[rejection], func(t *testing.T) {
				m, a, _, e := demandManager(t, time.Now().Add(-time.Hour))
				var failure error = errors.New("obsolete transient refresh")
				if rejection {
					failure = &Error{HTTPStatus: 401, Message: "obsolete invalid_grant"}
				}
				started, release := make(chan struct{}), make(chan struct{})
				e.refresh = func(context.Context, *Auth) (*Auth, error) {
					close(started)
					<-release
					return nil, failure
				}
				result := make(chan error, 1)
				go func() { _, err := m.EnsureRequestAuth(context.Background(), a, "demand-model"); result <- err }()
				<-started
				next, _ := m.GetByID(a.ID)
				if mutation != "register-identical" {
					next.Metadata["access_token"] = "request-refresh-login"
					next.Metadata["refresh_token"] = "request-refresh-login-refresh"
					next.Metadata["expired"] = "2099-01-01T00:00:00Z"
				}
				var err error
				if mutation == "update-distinct" {
					_, err = m.Update(context.Background(), next)
				} else {
					_, err = m.Register(context.Background(), next)
				}
				if err != nil {
					t.Fatal(err)
				}
				close(release)
				if err := <-result; !errors.Is(err, failure) {
					t.Fatalf("original waiter lost its actual failure: %v", err)
				}
				current, _ := m.GetByID(a.ID)
				if current.Unavailable || current.Status != StatusActive || current.LastError != nil || current.Metadata["access_token"] != next.Metadata["access_token"] {
					t.Fatalf("obsolete refresh failure poisoned replacement: %+v", current)
				}
				if mutation != "register-identical" {
					if _, err := m.Execute(context.Background(), []string{a.Provider}, cliproxyexecutor.Request{Model: "demand-model"}, cliproxyexecutor.Options{}); err != nil || e.refreshes.Load() != 1 || e.calls.Load() != 1 {
						t.Fatalf("obsolete failure blocked newer login: err=%v refresh=%d inference=%d", err, e.refreshes.Load(), e.calls.Load())
					}
				}
			})
		}
	}
}

type requestRefreshModelExecutor struct {
	*demandTestExecutor
	models chan string
}

func (e *requestRefreshModelExecutor) Execute(ctx context.Context, a *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.models <- req.Model
	return e.demandTestExecutor.Execute(ctx, a, req, opts)
}

func TestRequestRefreshEditsDuringWait(t *testing.T) {
	for _, change := range []string{"positive", "model-removed", "model-quota", "credential-quota", "alias-withdrawn", "alias-retargeted"} {
		t.Run(change, func(t *testing.T) {
			m, a, _, e := demandManager(t, time.Now().Add(-time.Hour))
			capture := &requestRefreshModelExecutor{demandTestExecutor: e, models: make(chan string, 1)}
			m.RegisterExecutor(capture)
			registry.GetGlobalRegistry().RegisterClient(a.ID, a.Provider, []*registry.ModelInfo{{ID: "demand-model"}, {ID: "request-refresh-other-model"}})
			m.SetOAuthModelAlias(map[string][]config.OAuthModelAlias{a.Provider: {{Name: "demand-model", Alias: "request-refresh-alias"}}})
			model := "demand-model"
			if change == "alias-withdrawn" || change == "alias-retargeted" {
				model = "request-refresh-alias"
			}
			started, release := make(chan struct{}), make(chan struct{})
			e.refresh = func(_ context.Context, next *Auth) (*Auth, error) {
				close(started)
				<-release
				return requestRefreshRotate(next), nil
			}
			result := make(chan error, 1)
			go func() {
				_, err := m.Execute(context.Background(), []string{a.Provider}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
				result <- err
			}()
			<-started
			current, _ := m.GetByID(a.ID)
			switch change {
			case "model-removed":
				registry.GetGlobalRegistry().RegisterClient(a.ID, a.Provider, []*registry.ModelInfo{{ID: "request-refresh-other-model"}})
			case "model-quota":
				current.ModelStates = map[string]*ModelState{"demand-model": {Unavailable: true, Quota: QuotaState{Exceeded: true, NextRecoverAt: time.Now().Add(time.Hour)}}}
				if _, err := m.Update(context.Background(), current); err != nil {
					t.Fatal(err)
				}
			case "credential-quota":
				current.Unavailable = true
				current.Quota = QuotaState{Exceeded: true, Reason: "credential_quota", NextRecoverAt: time.Now().Add(time.Hour)}
				if _, err := m.Update(context.Background(), current); err != nil {
					t.Fatal(err)
				}
			case "alias-withdrawn":
				m.SetOAuthModelAlias(nil)
			case "alias-retargeted":
				m.SetOAuthModelAlias(map[string][]config.OAuthModelAlias{a.Provider: {{Name: "request-refresh-other-model", Alias: "request-refresh-alias"}}})
			}
			close(release)
			err := <-result
			positive := change == "positive" || change == "alias-retargeted"
			if positive {
				if err != nil || e.calls.Load() != 1 || e.refreshes.Load() != 1 {
					t.Fatalf("positive control failed: err=%v refresh=%d inference=%d", err, e.refreshes.Load(), e.calls.Load())
				}
				gotModel := <-capture.models
				want := "demand-model"
				if change == "alias-retargeted" {
					want = "request-refresh-other-model"
				}
				if gotModel != want {
					t.Fatalf("executed stale alias target %q, want %q", gotModel, want)
				}
			} else if err == nil || e.calls.Load() != 0 || e.refreshes.Load() != 1 {
				t.Fatalf("post-wait eligibility edit waived: err=%v refresh=%d inference=%d", err, e.refreshes.Load(), e.calls.Load())
			}
		})
	}
}
