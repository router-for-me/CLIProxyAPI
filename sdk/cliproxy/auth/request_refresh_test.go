package auth

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	baseauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

type demandTestStorage struct{}

type demandWaitContext struct {
	context.Context
	waiting chan struct{}
	once    sync.Once
}

func (c *demandWaitContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.waiting) })
	return c.Context.Done()
}

func (*demandTestStorage) SaveTokenToFile(string) error { return nil }

var _ baseauth.TokenStorage = (*demandTestStorage)(nil)

type demandTestStore struct {
	mu    sync.Mutex
	fail  error
	saves int
	items []*Auth
}

func (s *demandTestStore) List(context.Context) ([]*Auth, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*Auth, len(s.items))
	for i, auth := range s.items {
		out[i] = auth.Clone()
	}
	return out, nil
}
func (s *demandTestStore) Save(_ context.Context, auth *Auth) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.saves++
	if s.fail != nil {
		return "", s.fail
	}
	s.items = []*Auth{auth.Clone()}
	return "synthetic.json", nil
}
func (*demandTestStore) Delete(context.Context, string) error      { return nil }
func (*demandTestStore) OnDemandRefreshPath(*Auth) (string, error) { return "synthetic.json", nil }

type demandTestExecutor struct {
	refreshes atomic.Int32
	calls     atomic.Int32
	refresh   func(context.Context, *Auth) (*Auth, error)
}

func (*demandTestExecutor) Identifier() string { return "demand-test" }
func (e *demandTestExecutor) Refresh(ctx context.Context, auth *Auth) (*Auth, error) {
	e.refreshes.Add(1)
	if e.refresh != nil {
		return e.refresh(ctx, auth)
	}
	auth.Metadata["access_token"] = "new"
	auth.Metadata["refresh_token"] = "new-refresh"
	auth.Metadata["expired"] = time.Now().Add(time.Hour).Format(time.RFC3339Nano)
	return auth, nil
}
func (e *demandTestExecutor) Execute(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.calls.Add(1)
	if !auth.HasValidAccessToken(time.Now()) {
		return cliproxyexecutor.Response{}, errors.New("expired bearer reached inference")
	}
	return cliproxyexecutor.Response{Payload: []byte(`{}`)}, nil
}
func (e *demandTestExecutor) CountTokens(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return e.Execute(ctx, auth, req, opts)
}
func (e *demandTestExecutor) ExecuteStream(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	_, err := e.Execute(ctx, auth, req, opts)
	if err != nil {
		return nil, err
	}
	chunks := make(chan cliproxyexecutor.StreamChunk, 1)
	chunks <- cliproxyexecutor.StreamChunk{Payload: []byte(`{}`)}
	close(chunks)
	return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
}
func (e *demandTestExecutor) HttpRequest(_ context.Context, auth *Auth, _ *http.Request) (*http.Response, error) {
	e.calls.Add(1)
	if !auth.HasValidAccessToken(time.Now()) {
		return nil, errors.New("expired bearer reached HTTP")
	}
	return &http.Response{StatusCode: 200, Body: http.NoBody}, nil
}

func demandManager(t *testing.T, expiry time.Time) (*Manager, *Auth, *demandTestStore, *demandTestExecutor) {
	t.Helper()
	store := &demandTestStore{}
	exec := &demandTestExecutor{}
	manager := NewManager(store, nil, nil)
	manager.RegisterExecutor(exec)
	auth, err := manager.Register(context.Background(), &Auth{ID: t.Name(), Provider: "demand-test", FileName: "synthetic.json", Status: StatusActive, RefreshPolicy: pluginapi.RefreshOnDemand, Storage: &demandTestStorage{}, Metadata: map[string]any{"access_token": "old", "refresh_token": "old-refresh", "expired": expiry.Format(time.RFC3339Nano)}})
	if err != nil {
		t.Fatal(err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "demand-model"}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
	return manager, auth, store, exec
}

func TestRequestRefreshMarginBoundaries(t *testing.T) {
	now := time.Unix(2000000000, 0)
	for _, tc := range []struct {
		name  string
		delta time.Duration
		due   bool
	}{{"beyond", 60*time.Second + time.Nanosecond, false}, {"equal", 60 * time.Second, true}, {"within", 59 * time.Second, true}, {"expiry", 0, true}, {"expired", -time.Second, true}} {
		t.Run(tc.name, func(t *testing.T) {
			auth := &Auth{Metadata: map[string]any{"access_token": "token", "expired": now.Add(tc.delta).Format(time.RFC3339Nano)}}
			if got := requestRefreshDue(auth, now); got != tc.due {
				t.Fatalf("due=%v want %v", got, tc.due)
			}
		})
	}
}

func TestRequestRefreshManagedConsumers(t *testing.T) {
	for _, consumer := range []string{"buffered", "count", "stream", "http"} {
		t.Run(consumer, func(t *testing.T) {
			m, a, _, e := demandManager(t, time.Now().Add(-time.Hour))
			req := cliproxyexecutor.Request{Model: "demand-model"}
			var err error
			switch consumer {
			case "buffered":
				_, err = m.Execute(context.Background(), []string{a.Provider}, req, cliproxyexecutor.Options{})
			case "count":
				_, err = m.ExecuteCount(context.Background(), []string{a.Provider}, req, cliproxyexecutor.Options{})
			case "stream":
				var result *cliproxyexecutor.StreamResult
				result, err = m.ExecuteStream(context.Background(), []string{a.Provider}, req, cliproxyexecutor.Options{})
				if result != nil {
					for range result.Chunks {
					}
				}
			case "http":
				httpReq, _ := http.NewRequest("GET", "http://127.0.0.1", nil)
				_, err = m.HttpRequest(context.Background(), a, httpReq)
			}
			if err != nil || e.refreshes.Load() != 1 || e.calls.Load() != 1 {
				t.Fatalf("err=%v refreshes=%d calls=%d", err, e.refreshes.Load(), e.calls.Load())
			}
		})
	}
	m, a, _, e := demandManager(t, time.Now().Add(time.Hour))
	if _, err := m.EnsureRequestAuth(context.Background(), a, ""); err != nil || e.refreshes.Load() != 0 {
		t.Fatalf("fresh credential rotated: %v", err)
	}
}

func TestRequestRefreshCoalescesSameTokenAndErrors(t *testing.T) {
	for _, mode := range []string{"rotated", "same-token", "transient", "rejected"} {
		t.Run(mode, func(t *testing.T) {
			m, a, store, e := demandManager(t, time.Now().Add(-time.Hour))
			started := make(chan struct{})
			release := make(chan struct{})
			failure := errors.New("synthetic transient")
			if mode == "rejected" {
				failure = &Error{HTTPStatus: 401, Message: "synthetic invalid_grant"}
			}
			e.refresh = func(_ context.Context, auth *Auth) (*Auth, error) {
				close(started)
				<-release
				if mode == "transient" || mode == "rejected" {
					return nil, failure
				}
				if mode != "same-token" {
					auth.Metadata["access_token"] = "new"
				}
				auth.Metadata["refresh_token"] = "new-refresh"
				auth.Metadata["expired"] = time.Now().Add(time.Hour).Format(time.RFC3339Nano)
				return auth, nil
			}
			const n = 24
			results := make(chan error, n)
			var ready sync.WaitGroup
			ready.Add(n)
			for i := 0; i < n; i++ {
				go func() { ready.Done(); _, err := m.EnsureRequestAuth(context.Background(), a, ""); results <- err }()
			}
			ready.Wait()
			<-started
			close(release)
			for i := 0; i < n; i++ {
				err := <-results
				if mode == "transient" || mode == "rejected" {
					if !errors.Is(err, failure) {
						t.Fatalf("lost actual error: %v", err)
					}
				} else if err != nil {
					t.Fatal(err)
				}
			}
			if e.refreshes.Load() != 1 {
				t.Fatalf("refreshes=%d", e.refreshes.Load())
			}
			store.mu.Lock()
			saves := store.saves
			store.mu.Unlock()
			if mode == "rotated" || mode == "same-token" {
				if saves != 2 {
					t.Fatalf("saves=%d want registration plus rotation", saves)
				}
			}
		})
	}
}

func TestRequestRefreshSaveOnlyRetryAndFreshOldHold(t *testing.T) {
	m, a, store, e := demandManager(t, time.Now().Add(30*time.Second))
	failed := errors.New("synthetic disk failure")
	store.mu.Lock()
	store.fail = failed
	store.mu.Unlock()
	_, err := m.EnsureRequestAuth(WithSkipPersist(context.Background()), a, "")
	if !errors.Is(err, failed) {
		t.Fatalf("save error=%v", err)
	}
	current, _ := m.GetByID(a.ID)
	if current.Metadata["access_token"] != "old" {
		t.Fatal("unsaved rotation was published")
	}
	_, err = m.EnsureRequestAuth(context.Background(), current, "")
	if !errors.Is(err, failed) || e.refreshes.Load() != 1 {
		t.Fatalf("pending state did not hold old fresh credential: %v", err)
	}
	store.mu.Lock()
	store.fail = nil
	store.mu.Unlock()
	state := m.authRefreshState(a.ID)
	state.stateMu.Lock()
	state.outcome.retryAt = time.Now().Add(-time.Second)
	state.stateMu.Unlock()
	fresh, err := m.EnsureRequestAuth(context.Background(), current, "")
	if err != nil || fresh.Metadata["access_token"] != "new" || e.refreshes.Load() != 1 {
		t.Fatalf("save-only retry failed: %v refreshes=%d", err, e.refreshes.Load())
	}
	store.mu.Lock()
	saves := store.saves
	store.mu.Unlock()
	if saves != 3 {
		t.Fatalf("saves=%d", saves)
	}
}

func TestRequestRefreshCancellationRetainsRotation(t *testing.T) {
	m, a, _, e := demandManager(t, time.Now().Add(-time.Hour))
	started := make(chan struct{})
	release := make(chan struct{})
	e.refresh = func(ctx context.Context, auth *Auth) (*Auth, error) {
		close(started)
		<-release
		if ctx.Err() != nil {
			t.Error("waiter canceled physical rotation")
		}
		auth.Metadata["access_token"] = "new"
		auth.Metadata["refresh_token"] = "new-refresh"
		auth.Metadata["expired"] = time.Now().Add(time.Hour).Format(time.RFC3339Nano)
		return auth, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { _, err := m.EnsureRequestAuth(ctx, a, ""); result <- err }()
	<-started
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("waiter=%v", err)
	}
	other := make(chan error, 1)
	go func() { _, err := m.EnsureRequestAuth(context.Background(), a, ""); other <- err }()
	close(release)
	if err := <-other; err != nil {
		t.Fatal(err)
	}
	current, _ := m.GetByID(a.ID)
	if current.Metadata["access_token"] != "new" || e.refreshes.Load() != 1 {
		t.Fatal("late rotation was discarded")
	}
}

func TestRequestRefreshStrictAdmissionAndOptOut(t *testing.T) {
	for _, mode := range []string{"opt-out", "disabled", "status-disabled", "rejected", "terminal", "no-refresh", "quota", "model-quota"} {
		t.Run(mode, func(t *testing.T) {
			m, a, _, e := demandManager(t, time.Now().Add(-time.Hour))
			switch mode {
			case "opt-out":
				a.RefreshPolicy = pluginapi.RefreshDefault
			case "disabled":
				a.Disabled = true
			case "status-disabled":
				a.Status = StatusDisabled
			case "rejected":
				a.RejectedAccessToken = "old"
			case "terminal":
				a.LastError = &Error{HTTPStatus: 401}
				a.Unavailable = true
				a.Status = StatusError
			case "no-refresh":
				delete(a.Metadata, "refresh_token")
			case "quota":
				a.Quota = QuotaState{Exceeded: true, Reason: "credential_quota", NextRecoverAt: time.Now().Add(time.Hour)}
			case "model-quota":
				a.ModelStates = map[string]*ModelState{"demand-model": {Unavailable: true, Quota: QuotaState{Exceeded: true, NextRecoverAt: time.Now().Add(time.Hour)}}}
			}
			if _, err := m.Update(context.Background(), a); err != nil {
				t.Fatal(err)
			}
			if mode == "rejected" {
				m.markRejectedAccessToken(a.ID, "old")
			}
			_, err := m.Execute(context.Background(), []string{a.Provider}, cliproxyexecutor.Request{Model: "demand-model"}, cliproxyexecutor.Options{})
			if err == nil || e.refreshes.Load() != 0 || e.calls.Load() != 0 {
				t.Fatalf("ineligible credential admitted: %v refreshes=%d calls=%d", err, e.refreshes.Load(), e.calls.Load())
			}
		})
	}
	m, a, _, e := demandManager(t, time.Now().Add(-time.Hour))
	if _, err := m.SelectAuth(context.Background(), a.Provider, "demand-model", cliproxyexecutor.Options{}); err == nil {
		t.Fatal("pure selection admitted expired token")
	}
	if e.refreshes.Load() != 0 {
		t.Fatal("pure selection refreshed")
	}
	if m.shouldRefresh(a, time.Now()) {
		t.Fatal("lazy auth scheduled")
	}
	if _, schedule := nextRefreshCheckAt(time.Now(), a, time.Second); schedule {
		t.Fatal("lazy auth in idle queue")
	}
}

func TestRequestRefreshConcurrentReplacementAndBlocks(t *testing.T) {
	for _, change := range []string{"login", "register", "disable", "quota", "policy"} {
		t.Run(change, func(t *testing.T) {
			m, a, store, e := demandManager(t, time.Now().Add(-time.Hour))
			started := make(chan struct{})
			release := make(chan struct{})
			e.refresh = func(_ context.Context, auth *Auth) (*Auth, error) {
				close(started)
				<-release
				auth.Metadata["access_token"] = "rotated"
				auth.Metadata["refresh_token"] = "rotated-refresh"
				auth.Metadata["expired"] = time.Now().Add(time.Hour).Format(time.RFC3339Nano)
				return auth, nil
			}
			result := make(chan error, 1)
			go func() { _, err := m.EnsureRequestAuth(context.Background(), a, ""); result <- err }()
			<-started
			current, _ := m.GetByID(a.ID)
			switch change {
			case "login", "register":
				current.Metadata["access_token"] = "login"
				current.Metadata["refresh_token"] = "login-refresh"
				current.Metadata["expired"] = time.Now().Add(time.Hour).Format(time.RFC3339Nano)
			case "disable":
				current.Disabled = true
			case "quota":
				current.Quota = QuotaState{Exceeded: true, Reason: "credential_quota", NextRecoverAt: time.Now().Add(time.Hour)}
				current.Unavailable = true
			case "policy":
				current.RefreshPolicy = pluginapi.RefreshDefault
			}
			if change == "register" {
				_, _ = m.Register(context.Background(), current)
			} else {
				_, _ = m.Update(context.Background(), current)
			}
			close(release)
			err := <-result
			after, _ := m.GetByID(a.ID)
			if change == "login" || change == "register" {
				if after.Metadata["access_token"] != "login" || err == nil {
					t.Fatalf("stale output won: %v", err)
				}
			} else {
				if after.Metadata["access_token"] != "rotated" {
					t.Fatal("consumed pair was not saved")
				}
				if change != "policy" && err == nil {
					t.Fatal("concurrent block was waived")
				}
				store.mu.Lock()
				saved := store.items[0].Clone()
				store.mu.Unlock()
				if change == "disable" && !saved.Disabled {
					t.Fatal("disable lost on save")
				}
				if change == "policy" && after.RefreshPolicy != pluginapi.RefreshDefault {
					t.Fatal("policy edit lost")
				}
			}
		})
	}
}

func TestRequestRefreshQueuedIdleRefreshVeto(t *testing.T) {
	m, a, _, e := demandManager(t, time.Now().Add(-time.Hour))
	a.RefreshPolicy = pluginapi.RefreshDefault
	_, _ = m.Update(context.Background(), a)
	job := m.markRefreshPending(nil, a.ID, a.RegistrationEpoch, time.Now())
	if job == nil {
		t.Fatal("legacy job not queued")
	}
	current, _ := m.GetByID(a.ID)
	current.RefreshPolicy = pluginapi.RefreshOnDemand
	_, _ = m.Update(context.Background(), current)
	if m.beginRefreshJob(context.Background(), job) {
		t.Fatal("queued background job survived opt-in")
	}
	_, err := m.refreshAuthForRequestAtEpoch(context.Background(), a.ID, "", a.RegistrationEpoch)
	if err != nil || e.refreshes.Load() != 0 {
		t.Fatalf("queued path rotated: %v", err)
	}
}

func TestRequestRefreshMissingDurabilityFailsBeforeRotation(t *testing.T) {
	for _, mode := range []string{"missing-store", "runtime-only", "virtual", "no-storage"} {
		t.Run(mode, func(t *testing.T) {
			m, a, _, e := demandManager(t, time.Now().Add(-time.Hour))
			switch mode {
			case "missing-store":
				m.SetStore(nil)
			case "runtime-only":
				a.Attributes = map[string]string{"runtime_only": "true"}
			case "virtual":
				MarkPluginVirtualAuth(a, "synthetic.json", 0)
			case "no-storage":
				a.Storage = nil
			}
			if mode != "missing-store" {
				_, _ = m.Update(context.Background(), a)
			}
			_, err := m.EnsureRequestAuth(context.Background(), a, "")
			if err == nil || e.refreshes.Load() != 0 {
				t.Fatalf("non-durable rotated: %v", err)
			}
		})
	}
}

func TestRequestRefreshFailureDoesNotReachInference(t *testing.T) {
	m, a, _, e := demandManager(t, time.Now().Add(-time.Hour))
	failure := errors.New("synthetic real refresh failure")
	e.refresh = func(context.Context, *Auth) (*Auth, error) { return nil, failure }
	_, err := m.Execute(context.Background(), []string{a.Provider}, cliproxyexecutor.Request{Model: "demand-model"}, cliproxyexecutor.Options{})
	if !errors.Is(err, failure) || e.calls.Load() != 0 {
		t.Fatalf("err=%v calls=%d", err, e.calls.Load())
	}
	current, _ := m.GetByID(a.ID)
	if current.Unavailable || strings.Contains(current.StatusMessage, "expired") {
		t.Fatal("transient lazy failure poisoned availability")
	}
}

func TestRequestRefreshReloadDoesNotAdoptUnsettledPair(t *testing.T) {
	for _, observed := range []string{"old", "pending", "login"} {
		t.Run(observed, func(t *testing.T) {
			m, a, store, e := demandManager(t, time.Now().Add(30*time.Second))
			failure := errors.New("synthetic failed save")
			store.mu.Lock()
			store.fail = failure
			store.mu.Unlock()
			if _, err := m.EnsureRequestAuth(context.Background(), a, ""); !errors.Is(err, failure) {
				t.Fatal(err)
			}
			state := m.authRefreshState(a.ID)
			state.stateMu.Lock()
			pending := state.pending.updated.Clone()
			state.stateMu.Unlock()
			snapshot := a.Clone()
			if observed == "pending" {
				snapshot = pending
			}
			if observed == "login" {
				snapshot.Metadata["access_token"] = "login"
				snapshot.Metadata["refresh_token"] = "login-refresh"
				snapshot.Metadata["expired"] = time.Now().Add(time.Hour).Format(time.RFC3339)
			}
			store.mu.Lock()
			store.items = []*Auth{snapshot}
			store.mu.Unlock()
			if err := m.Load(context.Background()); err != nil {
				t.Fatal(err)
			}
			current, _ := m.GetByID(a.ID)
			if observed == "login" {
				fresh, err := m.EnsureRequestAuth(context.Background(), current, "")
				if err != nil || fresh.Metadata["access_token"] != "login" || e.refreshes.Load() != 1 {
					t.Fatalf("new login did not retire hold: %v", err)
				}
				return
			}
			if current.Metadata["access_token"] != "old" {
				t.Fatal("reload adopted non-receipted pair")
			}
			if _, err := m.EnsureRequestAuth(context.Background(), current, ""); !errors.Is(err, failure) {
				t.Fatalf("reload waived pending failure: %v", err)
			}
			store.mu.Lock()
			store.fail = nil
			store.mu.Unlock()
			state.stateMu.Lock()
			state.outcome.retryAt = time.Now().Add(-time.Second)
			state.stateMu.Unlock()
			fresh, err := m.EnsureRequestAuth(context.Background(), current, "")
			if err != nil || fresh.Metadata["access_token"] != "new" || e.refreshes.Load() != 1 {
				t.Fatalf("reload retry reused consumed token: %v", err)
			}
		})
	}
}

func TestRequestRefreshForceAnd401UseExistingOwner(t *testing.T) {
	m, a, _, e := demandManager(t, time.Now().Add(-time.Hour))
	started := make(chan struct{})
	release := make(chan struct{})
	e.refresh = func(_ context.Context, auth *Auth) (*Auth, error) {
		close(started)
		<-release
		auth.Metadata["access_token"] = "new"
		auth.Metadata["refresh_token"] = "new-refresh"
		auth.Metadata["expired"] = time.Now().Add(time.Hour).Format(time.RFC3339)
		return auth, nil
	}
	demand := make(chan error, 1)
	go func() { _, err := m.EnsureRequestAuth(context.Background(), a, ""); demand <- err }()
	<-started
	force := make(chan error, 1)
	forceCtx := &demandWaitContext{Context: context.Background(), waiting: make(chan struct{})}
	go func() { _, err := m.ForceRefreshAuth(forceCtx, a.ID); force <- err }()
	recovery := make(chan error, 1)
	recoveryCtx := &demandWaitContext{Context: context.Background(), waiting: make(chan struct{})}
	go func() { _, err := m.refreshAuthForRequest(recoveryCtx, a.ID, "old"); recovery <- err }()
	<-forceCtx.waiting
	<-recoveryCtx.waiting
	close(release)
	for _, result := range []chan error{demand, force, recovery} {
		if err := <-result; err != nil {
			t.Fatal(err)
		}
	}
	if e.refreshes.Load() != 1 {
		t.Fatalf("shared refresh owner rotated %d times", e.refreshes.Load())
	}
}

func TestRequestRefreshPendingPolicyChangeCannotUseOldToken(t *testing.T) {
	m, a, store, e := demandManager(t, time.Now().Add(-time.Hour))
	failure := errors.New("synthetic failed save")
	store.mu.Lock()
	store.fail = failure
	store.mu.Unlock()
	_, _ = m.EnsureRequestAuth(context.Background(), a, "")
	current, _ := m.GetByID(a.ID)
	current.RefreshPolicy = pluginapi.RefreshDefault
	_, _ = m.Update(context.Background(), current)
	_, err := m.Execute(context.Background(), []string{a.Provider}, cliproxyexecutor.Request{Model: "demand-model"}, cliproxyexecutor.Options{})
	if !errors.Is(err, failure) || e.calls.Load() != 0 || e.refreshes.Load() != 1 {
		t.Fatalf("policy removal waived rotation hold: %v", err)
	}
}
