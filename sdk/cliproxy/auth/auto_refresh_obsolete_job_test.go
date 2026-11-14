package auth

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestBackgroundRefreshSkipsJobMadeObsoleteByForegroundRefresh(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newObsoleteRefreshFixture(t, false, true)
		defer f.stop(t)
		f.foregroundDone = make(chan struct{})
		foregroundResult := make(chan error, 1)
		go func() {
			defer close(f.foregroundDone)
			_, err := f.manager.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: f.model}, cliproxyexecutor.Options{})
			foregroundResult <- err
		}()
		awaitObsoleteRefresh(t, f.exec.entered)
		current := f.current(t)
		// The 401 makes A due even though the fixture's nominal expiry is +24h.
		// The foreground still owns the actual per-auth refresh mutex here.
		if current.RejectedAccessToken != "synthetic-obsolete-a" || !f.manager.shouldRefresh(current, time.Now()) {
			t.Fatal("obsolete refresh rejected token precondition")
		}
		f.queueDue(t)
		f.startWorker(t)
		f.manager.mu.RLock()
		job := f.manager.refreshJobs[f.id]
		running := job != nil && job.running
		f.manager.mu.RUnlock()
		if !running || f.exec.oldRefresh.Load() != 1 || f.exec.freshRefresh.Load() != 0 {
			t.Fatal("obsolete refresh worker ordering")
		}
		// beginRefreshJob has passed while foreground Refresh is held. The
		// worker cannot use the same refresh mutex until the foreground leaves.
		f.exec.release()
		awaitObsoleteRefresh(t, f.foregroundDone)
		if <-foregroundResult != nil {
			t.Fatal("obsolete refresh foreground result")
		}
		// Do not call Wait while a worker is blocked on the held sync.Mutex;
		// mutex waits are not durably blocked operations in synctest.
		synctest.Wait()
		f.requireFinished(t)
		if f.exec.oldExecute.Load() != 1 || f.exec.freshExecute.Load() != 1 || f.exec.oldRefresh.Load() != 1 || f.exec.freshRefresh.Load() != 0 {
			t.Fatal("obsolete refresh extra dispatch")
		}
	})
}

func TestBackgroundRefreshProcessesDueJobWithPendingMarker(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newObsoleteRefreshFixture(t, true, false)
		defer f.stop(t)
		f.queueDue(t)
		f.startWorker(t)
		synctest.Wait()
		f.requireFinished(t)
		if f.exec.oldRefresh.Load() != 1 || f.exec.freshRefresh.Load() != 0 {
			t.Fatal("obsolete refresh due job skipped")
		}
	})
}

func TestBackgroundRefreshKeepsDueJobAfterUnrelatedMetadataUpdate(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newObsoleteRefreshFixture(t, true, false)
		defer f.stop(t)
		f.queueDue(t)
		before := f.current(t)
		updated, committed, err := f.manager.Modify(WithSkipPersist(context.Background()), f.id, before.RegistrationEpoch, func(auth *Auth) error {
			auth.Metadata["display_note"] = "synthetic annotation"
			return nil
		})
		if err != nil || !committed || updated == nil || updated.RegistrationEpoch != before.RegistrationEpoch || updated.CredentialVersion != before.CredentialVersion || updated.Generation <= before.Generation {
			t.Fatal("obsolete refresh metadata precondition")
		}
		f.startWorker(t)
		synctest.Wait()
		f.requireFinished(t)
		if f.exec.oldRefresh.Load() != 1 || f.exec.freshRefresh.Load() != 0 || f.current(t).Metadata["display_note"] != "synthetic annotation" {
			t.Fatal("obsolete refresh metadata job skipped")
		}
	})
}

// Err remains exactly the underlying context's result. The first call in
// worker is beginRefreshJob's context check, under manager.mu. After receiving
// this signal the test reads job.running under the same mutex to establish
// that the real beginRefreshJob completed before releasing foreground work.
type obsoleteRefreshWorkerContext struct {
	context.Context
	entered chan struct{}
	once    sync.Once
}

func (c *obsoleteRefreshWorkerContext) Err() error {
	c.once.Do(func() { close(c.entered) })
	return c.Context.Err()
}

type obsoleteRefreshFixture struct {
	manager        *Manager
	loop           *authAutoRefreshLoop
	exec           *obsoleteRefreshExecutor
	id, model      string
	ctx            context.Context
	cancel         context.CancelFunc
	workerDone     chan struct{}
	foregroundDone chan struct{}
}

func newObsoleteRefreshFixture(t *testing.T, due, hold bool) *obsoleteRefreshFixture {
	t.Helper()
	executor := &obsoleteRefreshExecutor{hold: hold, entered: make(chan struct{}), unblock: make(chan struct{})}
	manager := NewManager(nil, &FillFirstSelector{}, nil)
	manager.RegisterExecutor(executor)
	ctx, cancel := context.WithCancel(context.Background())
	f := &obsoleteRefreshFixture{manager: manager, exec: executor, id: "obsolete-refresh-" + strings.ReplaceAll(t.Name(), "/", "-"),
		model: "claude-obsolete-refresh-model", ctx: ctx, cancel: cancel}
	f.loop = newAuthAutoRefreshLoop(manager, 5*time.Second, 1)
	expiry := time.Now().Add(24 * time.Hour)
	if due {
		expiry = time.Now().Add(-time.Hour)
	}
	auth := &Auth{ID: f.id, Provider: "claude", Status: StatusActive, Runtime: obsoleteRefreshLead{},
		Metadata: map[string]any{"type": "claude", "access_token": "synthetic-obsolete-a", "refresh_token": "synthetic-obsolete-grant-a", "expired": expiry.Format(time.RFC3339)}}
	if _, err := manager.Register(ctx, auth); err != nil {
		cancel()
		t.Fatal("obsolete refresh setup")
	}
	registry.GetGlobalRegistry().RegisterClient(f.id, "claude", []*registry.ModelInfo{{ID: f.model}})
	return f
}

func (f *obsoleteRefreshFixture) current(t *testing.T) *Auth {
	t.Helper()
	auth, found := f.manager.GetByID(f.id)
	if !found || auth == nil {
		t.Fatal("obsolete refresh current auth")
	}
	return auth
}

func (f *obsoleteRefreshFixture) queueDue(t *testing.T) {
	t.Helper()
	if !f.manager.shouldRefresh(f.current(t), time.Now()) {
		t.Fatal("obsolete refresh due precondition")
	}
	f.loop.handleDueAuth(context.Background(), time.Now(), f.id)
	f.manager.mu.RLock()
	job := f.manager.refreshJobs[f.id]
	current := f.manager.auths[f.id].Clone()
	queued := job != nil && !job.running && job.registrationEpoch == current.RegistrationEpoch && current.NextRefreshAfter.Equal(job.pendingUntil)
	f.manager.mu.RUnlock()
	// The scheduler's own marker makes shouldRefresh false. A fix must not
	// confuse that marker with evidence that a queued due job is obsolete.
	if !queued || f.manager.shouldRefresh(current, time.Now()) {
		t.Fatal("obsolete refresh pending marker")
	}
}

func (f *obsoleteRefreshFixture) startWorker(t *testing.T) {
	t.Helper()
	observed := &obsoleteRefreshWorkerContext{Context: f.ctx, entered: make(chan struct{})}
	f.workerDone = make(chan struct{})
	go func() { defer close(f.workerDone); f.loop.worker(observed) }()
	awaitObsoleteRefresh(t, observed.entered)
}

func (f *obsoleteRefreshFixture) requireFinished(t *testing.T) {
	t.Helper()
	f.manager.mu.RLock()
	remaining := f.manager.refreshJobs[f.id] != nil
	f.manager.mu.RUnlock()
	auth := f.current(t)
	if remaining || auth.Metadata["access_token"] != "synthetic-obsolete-c" || auth.Metadata["refresh_token"] != "synthetic-obsolete-grant-c" || !auth.HasValidAccessToken(time.Now()) || f.manager.shouldRefresh(auth, time.Now()) {
		t.Fatal("obsolete refresh final state")
	}
}

func (f *obsoleteRefreshFixture) stop(t *testing.T) {
	t.Helper()
	defer registry.GetGlobalRegistry().UnregisterClient(f.id)
	defer func() {
		t.Logf("obsolete refresh counts: old=%d fresh=%d execute_old=%d execute_fresh=%d", f.exec.oldRefresh.Load(), f.exec.freshRefresh.Load(), f.exec.oldExecute.Load(), f.exec.freshExecute.Load())
	}()
	f.exec.release()
	f.cancel()
	if f.foregroundDone != nil {
		awaitObsoleteRefresh(t, f.foregroundDone)
	}
	if f.workerDone != nil {
		awaitObsoleteRefresh(t, f.workerDone)
	}
}

func awaitObsoleteRefresh(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("obsolete refresh synchronization")
	}
}

type obsoleteRefreshLead struct{}

func (obsoleteRefreshLead) RefreshLead() *time.Duration { return new(4 * time.Hour) }

type obsoleteRefreshExecutor struct {
	hold                     bool
	entered, unblock         chan struct{}
	enterOnce, releaseOnce   sync.Once
	oldRefresh, freshRefresh atomic.Int32
	oldExecute, freshExecute atomic.Int32
}

func (*obsoleteRefreshExecutor) Identifier() string { return "claude" }
func (e *obsoleteRefreshExecutor) release()         { e.releaseOnce.Do(func() { close(e.unblock) }) }
func (e *obsoleteRefreshExecutor) Execute(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	if authAccessToken(auth) == "synthetic-obsolete-a" {
		e.oldExecute.Add(1)
		return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusUnauthorized, Message: "synthetic unauthorized"}
	}
	if authAccessToken(auth) == "synthetic-obsolete-c" {
		e.freshExecute.Add(1)
		return cliproxyexecutor.Response{Payload: []byte(`{"ok":true}`)}, nil
	}
	return cliproxyexecutor.Response{}, errors.New("synthetic credential refused")
}
func (e *obsoleteRefreshExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	switch authAccessToken(auth) {
	case "synthetic-obsolete-a":
		e.oldRefresh.Add(1)
		if e.hold {
			e.enterOnce.Do(func() { close(e.entered) })
			<-e.unblock
		}
	case "synthetic-obsolete-c":
		e.freshRefresh.Add(1)
	default:
		return nil, errors.New("synthetic credential refused")
	}
	updated := auth.Clone()
	updated.Metadata["access_token"] = "synthetic-obsolete-c"
	updated.Metadata["refresh_token"] = "synthetic-obsolete-grant-c"
	updated.Metadata["expired"] = time.Now().Add(24 * time.Hour).Format(time.RFC3339)
	return updated, nil
}
func (*obsoleteRefreshExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, errors.New("synthetic unused stream")
}
func (*obsoleteRefreshExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, errors.New("synthetic unused count")
}
func (*obsoleteRefreshExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("synthetic unused HTTP")
}
