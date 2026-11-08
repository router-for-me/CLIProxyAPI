package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	executor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

type issue6653Executor struct {
	issue6653BaselineExecutor
	refreshFn func(context.Context, *Auth) (*Auth, error)
	executeFn func(*Auth) error
}

func (e *issue6653Executor) Refresh(ctx context.Context, a *Auth) (*Auth, error) {
	if e.refreshFn != nil {
		e.refreshes.Add(1)
		return e.refreshFn(ctx, a)
	}
	return e.issue6653BaselineExecutor.Refresh(ctx, a)
}
func (e *issue6653Executor) Execute(ctx context.Context, a *Auth, r executor.Request, o executor.Options) (executor.Response, error) {
	if e.executeFn != nil {
		if err := e.executeFn(a); err != nil {
			return executor.Response{}, err
		}
	}
	return e.issue6653BaselineExecutor.Execute(ctx, a, r, o)
}
func (e *issue6653Executor) CountTokens(ctx context.Context, a *Auth, r executor.Request, o executor.Options) (executor.Response, error) {
	return e.Execute(ctx, a, r, o)
}
func (e *issue6653Executor) ExecuteStream(ctx context.Context, a *Auth, r executor.Request, o executor.Options) (*executor.StreamResult, error) {
	resp, err := e.Execute(ctx, a, r, o)
	if err != nil {
		return nil, err
	}
	chunks := make(chan executor.StreamChunk, 1)
	chunks <- executor.StreamChunk{Payload: resp.Payload}
	close(chunks)
	return &executor.StreamResult{Chunks: chunks}, nil
}
func issue6653Rotated(a *Auth) *Auth {
	u := a.Clone()
	u.Metadata["access_token"] = "qa-rotated-access"
	u.Metadata["refresh_token"] = "qa-rotated-refresh"
	u.Metadata["expires_at"] = time.Now().Add(2 * time.Hour).Format(time.RFC3339Nano)
	return u
}
func issue6653Manager(t *testing.T, e *issue6653Executor, store Store) (*Manager, *Auth) {
	t.Helper()
	m := NewManager(store, &FillFirstSelector{}, nil)
	m.RegisterExecutor(e)
	a := &Auth{ID: "qa-6653-" + t.Name(), Provider: e.Identifier(), Status: StatusActive, RefreshPolicy: &RefreshPolicy{Mode: pluginapi.AuthRefreshLazy, ExpiryMarginSeconds: 60}, Metadata: map[string]any{
		"access_token": "qa-disposable-access", "refresh_token": "qa-disposable-refresh", "expires_at": time.Now().Add(-time.Hour).Format(time.RFC3339Nano),
	}}
	registered, err := m.Register(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(a.ID, e.Identifier(), []*registry.ModelInfo{{ID: "qa-model-6653"}})
	t.Cleanup(func() { reg.UnregisterClient(a.ID) })
	return m, registered
}
func issue6653Execute(m *Manager) (executor.Response, error) {
	return m.Execute(context.Background(), []string{"qa-native-6653"}, executor.Request{Model: "qa-model-6653"}, executor.Options{})
}

func TestIssue6653LazyMarginBoundary(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, margin := range []int{0, 60, 3600} {
		for _, delta := range []time.Duration{-time.Nanosecond, 0, time.Nanosecond} {
			a := &Auth{RefreshPolicy: &RefreshPolicy{Mode: "lazy", ExpiryMarginSeconds: int64(margin)}, Metadata: map[string]any{"access_token": "qa-access", "expires_at": now.Add(time.Duration(margin)*time.Second + delta).Format(time.RFC3339Nano)}}
			if got := lazyRefreshDue(a, now); got != (delta <= 0) {
				t.Errorf("margin=%d delta=%v due=%v", margin, delta, got)
			}
		}
	}
	a := &Auth{RefreshPolicy: &RefreshPolicy{Mode: "lazy"}, Metadata: map[string]any{"refresh_token": "qa-refresh"}}
	if !lazyRefreshDue(a, now) {
		t.Fatal("missing access token must refresh")
	}
	a.Metadata["access_token"] = "opaque-qa-access"
	if lazyRefreshDue(a, now) {
		t.Fatal("opaque token with unknown expiry unexpectedly due")
	}
}

func TestIssue6653LazyOperations(t *testing.T) {
	for _, operation := range []string{"buffered", "stream", "image", "count", "ensure"} {
		t.Run(operation, func(t *testing.T) {
			e := &issue6653Executor{}
			m, a := issue6653Manager(t, e, nil)
			e.executeFn = func(fresh *Auth) error {
				if authAccessToken(fresh) != "qa-rotated-access" || fresh.Metadata["refresh_token"] != "qa-rotated-refresh" {
					return errors.New("stale credentials executed")
				}
				return nil
			}
			req := executor.Request{Model: "qa-model-6653"}
			opts := executor.Options{}
			var err error
			switch operation {
			case "stream":
				var s *executor.StreamResult
				s, err = m.ExecuteStream(context.Background(), []string{e.Identifier()}, req, opts)
				if s != nil {
					for c := range s.Chunks {
						if c.Err != nil {
							t.Fatal(c.Err)
						}
					}
				}
			case "count":
				_, err = m.ExecuteCount(context.Background(), []string{e.Identifier()}, req, opts)
			case "ensure":
				_, err = m.EnsureAuthFresh(context.Background(), a)
			default:
				if operation == "image" {
					opts.Metadata = map[string]any{executor.RequestPathMetadataKey: "/v1/images/generations"}
				}
				_, err = m.Execute(context.Background(), []string{e.Identifier()}, req, opts)
			}
			if err != nil {
				t.Fatal(err)
			}
			if e.refreshes.Load() != 1 {
				t.Fatalf("refreshes=%d", e.refreshes.Load())
			}
			if _, err = m.EnsureAuthFresh(context.Background(), a); err != nil {
				t.Fatal(err)
			}
			if e.refreshes.Load() != 1 {
				t.Fatal("fresh latest snapshot refreshed twice")
			}
		})
	}
}

func TestIssue6653LazyEligibility(t *testing.T) {
	for _, name := range []string{"disabled", "disabled-status", "requires-sign-in", "rejected-grant", "credential-cooldown", "model-cooldown", "missing-grant"} {
		t.Run(name, func(t *testing.T) {
			e := &issue6653Executor{}
			m, a := issue6653Manager(t, e, nil)
			future := time.Now().Add(time.Hour)
			switch name {
			case "disabled":
				a.Disabled = true
			case "disabled-status":
				a.Status = StatusDisabled
			case "requires-sign-in":
				a.RequiresSignIn = true
			case "rejected-grant":
				a.RejectedRefreshToken = "qa-disposable-refresh"
			case "credential-cooldown":
				a.Quota = QuotaState{Exceeded: true, Reason: "credential_quota", NextRecoverAt: future}
			case "model-cooldown":
				a.ModelStates = map[string]*ModelState{"qa-model-6653": {Unavailable: true, NextRetryAfter: future}}
			case "missing-grant":
				delete(a.Metadata, "refresh_token")
			}
			if name == "requires-sign-in" || name == "rejected-grant" {
				m.mu.Lock()
				m.auths[a.ID] = a.Clone()
				m.scheduler.upsertAuth(a.Clone())
				m.mu.Unlock()
			} else if _, err := m.Update(context.Background(), a); err != nil {
				t.Fatal(err)
			}
			if _, err := issue6653Execute(m); err == nil {
				t.Fatal("blocked credential executed")
			}
			if e.calls.Load() != 0 || e.refreshes.Load() != 0 {
				t.Fatalf("blocked path: inference=%d refresh=%d", e.calls.Load(), e.refreshes.Load())
			}
		})
	}
}

func TestIssue6653LazyIdleAndQueuedBackground(t *testing.T) {
	e := &issue6653Executor{}
	m, a := issue6653Manager(t, e, nil)
	now := time.Now()
	loop := newAuthAutoRefreshLoop(m, refreshCheckInterval, 2)
	m.refreshLoop = loop
	loop.rebuild(now)
	loop.handleDue(context.Background(), now)
	if len(loop.jobs) != 0 || m.shouldRefresh(a, now) {
		t.Fatal("lazy auth scheduled idle work")
	}
	if _, scheduled := m.nextRefreshCheckAt(now, a, refreshCheckInterval); scheduled {
		t.Fatal("lazy auth kept background deadline")
	}
	if _, err := m.refreshAuthWithTrigger(context.Background(), a.ID, "", a.RegistrationEpoch, refreshTriggerBackground); err != nil {
		t.Fatal(err)
	}
	if e.refreshes.Load() != 0 {
		t.Fatal("previously queued background job rotated lazy auth")
	}
	if _, err := m.refreshAuthForRequest(context.Background(), a.ID, ""); err != nil {
		t.Fatal(err)
	}
	if e.refreshes.Load() != 1 {
		t.Fatal("explicit refresh no longer available")
	}
}

func TestIssue6653LazyFailureBackoffAndRecovery(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		marker := &Error{Code: "qa_refresh_503", Message: "qa refresh unavailable", HTTPStatus: http.StatusServiceUnavailable}
		e := &issue6653Executor{}
		m, a := issue6653Manager(t, e, nil)
		e.refreshFn = func(_ context.Context, a *Auth) (*Auth, error) {
			if e.refreshes.Load() == 1 {
				return nil, marker
			}
			return issue6653Rotated(a), nil
		}
		for range 3 {
			_, err := issue6653Execute(m)
			if !errors.Is(err, marker) {
				t.Fatalf("actual refresh error lost: %v", err)
			}
		}
		if e.calls.Load() != 0 || e.refreshes.Load() != 1 {
			t.Fatal("transient failure sent inference or reused grant during backoff")
		}
		current, _ := m.GetByID(a.ID)
		if current.Unavailable || len(current.ModelStates) != 0 {
			t.Fatal("refresh failure poisoned inference eligibility")
		}
		time.Sleep(refreshFailureBackoff + time.Second)
		if _, err := issue6653Execute(m); err != nil {
			t.Fatal(err)
		}
		if e.refreshes.Load() != 2 || e.calls.Load() != 1 {
			t.Fatalf("recovery refresh=%d inference=%d", e.refreshes.Load(), e.calls.Load())
		}
	})
}

func TestIssue6653LazyNoOpAndTerminalRejection(t *testing.T) {
	for _, kind := range []string{"no-op", "expired-rotated", "invalid-grant", "cancelled"} {
		t.Run(kind, func(t *testing.T) {
			e := &issue6653Executor{}
			m, a := issue6653Manager(t, e, nil)
			e.refreshFn = func(ctx context.Context, a *Auth) (*Auth, error) {
				switch kind {
				case "invalid-grant":
					return nil, &Error{Code: "invalid_grant", Message: "invalid_grant", HTTPStatus: 400}
				case "cancelled":
					return nil, context.Canceled
				case "expired-rotated":
					u := issue6653Rotated(a)
					u.Metadata["expires_at"] = time.Now().Add(-time.Minute).Format(time.RFC3339Nano)
					return u, nil
				default:
					return a.Clone(), nil
				}
			}
			if _, err := issue6653Execute(m); err == nil {
				t.Fatal("ineffective refresh executed")
			}
			if e.calls.Load() != 0 {
				t.Fatal("inference ran after ineffective refresh")
			}
			if kind == "cancelled" {
				e.refreshFn = func(_ context.Context, a *Auth) (*Auth, error) { return issue6653Rotated(a), nil }
				if _, err := issue6653Execute(m); err != nil {
					t.Fatal("cancel failure cached", err)
				}
			} else {
				if _, err := m.EnsureAuthFresh(context.Background(), a); err == nil {
					t.Fatal("ineffective result became fresh")
				}
				if e.refreshes.Load() != 1 {
					t.Fatal("backoff/terminal grant reused")
				}
			}
		})
	}
}

type issue6653Store struct {
	mu    sync.Mutex
	fail  bool
	saves int
	last  *Auth
}

func (s *issue6653Store) Save(_ context.Context, a *Auth) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.saves++
	if s.fail {
		return "", errors.New("qa disk unavailable")
	}
	s.last = a.Clone()
	return a.ID, nil
}
func (s *issue6653Store) List(context.Context) ([]*Auth, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.last == nil {
		return nil, nil
	}
	return []*Auth{s.last.Clone()}, nil
}
func (s *issue6653Store) Delete(context.Context, string) error { return nil }

func TestIssue6653LazySaveRetryRetainsRotatedGrant(t *testing.T) {
	store := &issue6653Store{}
	e := &issue6653Executor{}
	m, a := issue6653Manager(t, e, store)
	store.fail = true
	if _, err := issue6653Execute(m); err == nil {
		t.Fatal("failed save did not block inference")
	}
	if e.refreshes.Load() != 1 || e.calls.Load() != 0 {
		t.Fatal("failed save executed or repeated rotation")
	}
	if _, err := m.EnsureAuthFresh(context.Background(), a); err == nil {
		t.Fatal("second failed save became successful")
	}
	if e.refreshes.Load() != 1 {
		t.Fatal("consumed old grant reused")
	}
	store.fail = false
	e.executeFn = func(a *Auth) error {
		store.mu.Lock()
		defer store.mu.Unlock()
		if store.last == nil || store.last.Metadata["refresh_token"] != "qa-rotated-refresh" {
			return errors.New("inference before persisted pair")
		}
		return nil
	}
	if _, err := issue6653Execute(m); err != nil {
		t.Fatal(err)
	}
	if e.refreshes.Load() != 1 || e.calls.Load() != 1 {
		t.Fatal("persistence retry did not use retained rotation")
	}
}

func TestIssue6653LazyConcurrentFreshness(t *testing.T) {
	e := &issue6653Executor{}
	m, a := issue6653Manager(t, e, nil)
	started := make(chan struct{})
	release := make(chan struct{})
	e.refreshFn = func(_ context.Context, a *Auth) (*Auth, error) {
		close(started)
		<-release
		return issue6653Rotated(a), nil
	}
	const n = 32
	results := make(chan error, n)
	start := make(chan struct{})
	for range n {
		go func() { <-start; _, err := m.EnsureAuthFresh(context.Background(), a); results <- err }()
	}
	close(start)
	<-started
	close(release)
	for range n {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	if e.refreshes.Load() != 1 {
		t.Fatalf("concurrent refreshes=%d", e.refreshes.Load())
	}
}

func TestIssue6653LazyLatestSnapshotDuringRotation(t *testing.T) {
	for _, change := range []string{"disabled", "replace-token", "delete", "reregister", "model-cooldown"} {
		t.Run(change, func(t *testing.T) {
			e := &issue6653Executor{}
			m, a := issue6653Manager(t, e, nil)
			started := make(chan struct{})
			release := make(chan struct{})
			e.refreshFn = func(_ context.Context, a *Auth) (*Auth, error) {
				close(started)
				<-release
				return issue6653Rotated(a), nil
			}
			done := make(chan error, 1)
			go func() { _, err := issue6653Execute(m); done <- err }()
			<-started
			switch change {
			case "delete", "reregister":
				m.Remove(context.Background(), a.ID)
				if change == "reregister" {
					newer := issue6653Rotated(a)
					newer.RegistrationEpoch = 0
					if _, err := m.Register(context.Background(), newer); err != nil {
						t.Fatal(err)
					}
				}
			default:
				latest, _ := m.GetByID(a.ID)
				if change == "disabled" {
					latest.Disabled = true
				} else if change == "model-cooldown" {
					latest.ModelStates = map[string]*ModelState{"qa-model-6653": {Unavailable: true, NextRetryAfter: time.Now().Add(time.Hour)}}
				} else {
					latest.Metadata["access_token"] = "qa-newer-access"
					latest.Metadata["refresh_token"] = "qa-newer-refresh"
					latest.Metadata["expires_at"] = time.Now().Add(2 * time.Hour).Format(time.RFC3339Nano)
				}
				if _, err := m.Update(context.Background(), latest); err != nil {
					t.Fatal(err)
				}
			}
			close(release)
			err := <-done
			if change != "replace-token" && (err == nil || e.calls.Load() != 0) {
				t.Fatalf("lifecycle change executed: err=%v calls=%d", err, e.calls.Load())
			}
			if change == "replace-token" {
				latest, _ := m.GetByID(a.ID)
				if authAccessToken(latest) != "qa-newer-access" {
					t.Fatal("refresh overwrote newer credentials")
				}
			}
		})
	}
}

type issue6653HTTPExecutor struct{ *issue6653Executor }

func (e *issue6653HTTPExecutor) PrepareRequest(req *http.Request, a *Auth) error {
	req.Header.Set("Authorization", "Bearer "+authAccessToken(a))
	return nil
}
func (e *issue6653HTTPExecutor) HttpRequest(ctx context.Context, a *Auth, req *http.Request) (*http.Response, error) {
	if err := e.PrepareRequest(req, a); err != nil {
		return nil, err
	}
	return http.DefaultClient.Do(req.WithContext(ctx))
}

func TestIssue6653LazyAuthBoundHTTP(t *testing.T) {
	e := &issue6653Executor{}
	m, a := issue6653Manager(t, e, nil)
	m.RegisterExecutor(&issue6653HTTPExecutor{e})
	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") != "Bearer qa-rotated-access" {
			t.Error("billing HTTP request used expired token")
			w.WriteHeader(401)
		}
	}))
	defer upstream.Close()
	req, err := m.NewHttpRequest(context.Background(), a, http.MethodGet, upstream.URL+"/billing", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if req.Header.Get("Authorization") != "Bearer qa-rotated-access" {
		t.Fatal("HTTP preparation did not refresh")
	}
	resp, err := m.HttpRequest(context.Background(), a, req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 || calls != 1 || e.refreshes.Load() != 1 {
		t.Fatalf("HTTP status=%d calls=%d refresh=%d", resp.StatusCode, calls, e.refreshes.Load())
	}
}

func TestIssue6653LazyStructuredInvalidGrantIsTerminal(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := &issue6653Executor{}
		m, _ := issue6653Manager(t, e, nil)
		e.refreshFn = func(context.Context, *Auth) (*Auth, error) {
			return nil, &Error{Code: "invalid_grant", Message: "qa refresh denied", HTTPStatus: 400}
		}
		if _, err := issue6653Execute(m); err == nil {
			t.Fatal("grant rejection executed")
		}
		time.Sleep(refreshFailureBackoff + time.Second)
		if _, err := issue6653Execute(m); err == nil {
			t.Fatal("terminal rejection recovered without new credentials")
		}
		if e.refreshes.Load() != 1 {
			t.Fatalf("structured terminal rejection reused rejected grant: refreshes=%d want 1", e.refreshes.Load())
		}
	})
}

func TestIssue6653LazyRefreshCannotChangeIdentity(t *testing.T) {
	e := &issue6653Executor{}
	m, a := issue6653Manager(t, e, nil)
	other := issue6653Rotated(a)
	other.ID = "zzz-qa-other-6653"
	other.RegistrationEpoch = 0
	other, err := m.Register(context.Background(), other)
	if err != nil {
		t.Fatal(err)
	}
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(other.ID, e.Identifier(), []*registry.ModelInfo{{ID: "qa-model-6653"}})
	defer reg.UnregisterClient(other.ID)
	e.refreshFn = func(context.Context, *Auth) (*Auth, error) { return other.Clone(), nil }
	if fresh, err := m.EnsureAuthFresh(context.Background(), a); err == nil {
		t.Fatalf("refresh for %s accepted different identity %s", a.ID, fresh.ID)
	}
}

func TestIssue6653LazyRegisteredPolicyOverridesStaleCaller(t *testing.T) {
	e := &issue6653Executor{}
	m, a := issue6653Manager(t, e, nil)
	stale := a.Clone()
	stale.RefreshPolicy = nil
	fresh, err := m.EnsureAuthFresh(context.Background(), stale)
	if err != nil {
		t.Fatal(err)
	}
	if authAccessToken(fresh) != "qa-rotated-access" || e.refreshes.Load() != 1 {
		t.Fatal("stale caller policy bypassed registered lazy freshness")
	}
}

func TestIssue6653LazyPendingSaveDiscardedForNewCredential(t *testing.T) {
	store := &issue6653Store{}
	e := &issue6653Executor{}
	m, a := issue6653Manager(t, e, store)
	store.fail = true
	if _, err := issue6653Execute(m); err == nil {
		t.Fatal("failed save allowed inference")
	}
	store.fail = false
	newer := a.Clone()
	newer.Metadata["access_token"] = "qa-newer-access"
	newer.Metadata["refresh_token"] = "qa-newer-refresh"
	newer.Metadata["expires_at"] = time.Now().Add(2 * time.Hour).Format(time.RFC3339Nano)
	if _, err := m.Update(context.Background(), newer); err != nil {
		t.Fatal(err)
	}
	resp, err := issue6653Execute(m)
	if err != nil {
		t.Fatal(err)
	}
	if string(resp.Payload) != "qa-newer-access" || e.refreshes.Load() != 1 || store.last.Metadata["refresh_token"] != "qa-newer-refresh" {
		t.Fatal("pending consumed-grant rotation overwrote newer credential")
	}
}

func TestIssue6653LazyCancelledCallerDoesNotRotate(t *testing.T) {
	e := &issue6653Executor{}
	m, a := issue6653Manager(t, e, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := m.EnsureAuthFresh(ctx, a); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel err=%v", err)
	}
	if e.refreshes.Load() != 0 {
		t.Fatal("already cancelled request rotated tokens")
	}
}
