package auth

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	execution "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// Done observes entry into the capacity select without timing or scheduler polls.
type launchWaitContext struct {
	context.Context
	once    sync.Once
	waiting chan struct{}
}

func (c *launchWaitContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.waiting) })
	return c.Context.Done()
}

// Two prepare probes and two established requests occupy the same four slots.
func saturatedLaunchFixture(t *testing.T) (*launchFixture, LaunchResult, func()) {
	t.Helper()
	f := newLaunchFixture(t)
	f.add(t, "a", "account")
	bindings := make([]LaunchResult, 3)
	for i := range bindings {
		bindings[i] = f.pool.Prepare(context.Background(), "client", launchRequest(fmt.Sprintf("bound-%d", i)))
		if err := f.execute("client", bindings[i], bindings[i].Model, bindings[i].SessionID, false); err != nil {
			t.Fatal(err)
		}
	}
	entered, release := make(chan struct{}, 4), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	f.handler = func(w http.ResponseWriter, r *http.Request) {
		entered <- struct{}{}
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		_, _ = w.Write(launchUsage("account", true, 12, f.now))
	}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i < 2 {
				r := bindings[i]
				if err := f.execute("client", r, r.Model, r.SessionID, false); err != nil {
					t.Errorf("occupying request: %v", err)
				}
			} else if r := f.pool.Prepare(context.Background(), "client", launchRequest(fmt.Sprintf("prepare-%d", i))); r.Status != "ready" {
				t.Errorf("occupying prepare: %+v", r)
			}
		}(i)
	}
	t.Cleanup(func() { unblock(); wg.Wait() })
	for i := 0; i < 4; i++ {
		<-entered
	}
	return f, bindings[2], unblock
}

func TestDelegatedLaunchProbeContentionWaits(t *testing.T) {
	f, r, unblock := saturatedLaunchFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	observed := &launchWaitContext{Context: ctx, waiting: make(chan struct{})}
	result := make(chan *LaunchRefusal, 1)
	go func() {
		_, _, refusal := f.pool.authorize(observed, cancel, "client", r.BindingID, r.SessionID, r.Model)
		result <- refusal
	}()
	select {
	case refusal := <-result:
		t.Fatalf("fifth request refused instead of waiting: %+v; state=%+v", refusal, f.pool.Inspect("client", r.BindingID, r.SessionID))
	case <-observed.waiting:
	}
	state := f.pool.Inspect("client", r.BindingID, r.SessionID)
	if !state.Active || state.Refusal != nil || state.RefusalVersion != 0 || state.ExecutorAttempts != 1 {
		t.Fatalf("queued ownership: %+v", state)
	}
	requireLaunchRefusal(t, f.execute("client", r, r.Model, r.SessionID, false), "binding_busy")
	if f.pool.Resume("client", r.BindingID, r.SessionID, 1) {
		t.Fatal("resume accepted while queued")
	}
	unblock()
	if refusal := <-result; refusal != nil {
		t.Fatal(refusal)
	}
	f.pool.finish(r.BindingID)
	if err := f.execute("client", r, r.Model, r.SessionID, false); err != nil {
		t.Fatal(err)
	}
	if state = f.pool.Inspect("client", r.BindingID, r.SessionID); state.Refusal != nil || state.RefusalVersion != 0 || state.ExecutorAttempts != 2 {
		t.Fatal(state)
	}
}

func TestDelegatedLaunchProbeContentionCancellationDoesNotLatch(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		t.Run(fmt.Sprint(deadline), func(t *testing.T) {
			f, r, unblock := saturatedLaunchFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			if deadline {
				cancel()
				ctx, cancel = context.WithTimeout(context.Background(), 25*time.Millisecond)
			} else {
				cancel()
			}
			defer cancel()
			_, _, err := f.pool.ExecuteBound(ctx, "client", r.BindingID, r.SessionID, execution.Request{Model: r.Model}, execution.Options{})
			if err == nil {
				t.Fatal("cancelled request executed")
			}
			state := f.pool.Inspect("client", r.BindingID, r.SessionID)
			if state.Refusal != nil || state.RefusalVersion != 0 || state.Active || state.ExecutorAttempts != 1 {
				t.Fatalf("local saturation poisoned binding: %+v", state)
			}
			unblock()
			if err := f.execute("client", r, r.Model, r.SessionID, false); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestDelegatedLaunchQueuedAdmissionControls(t *testing.T) {
	for _, kind := range []string{"cancel", "release", "auth-change", "quota", "identity"} {
		t.Run(kind, func(t *testing.T) {
			f := newLaunchFixture(t)
			f.add(t, "a", "account")
			r := f.pool.Prepare(context.Background(), "client", launchRequest("queued-controls"))
			if err := f.execute("client", r, r.Model, r.SessionID, false); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < cap(f.pool.probes); i++ {
				f.pool.probes <- struct{}{}
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			observed := &launchWaitContext{Context: ctx, waiting: make(chan struct{})}
			result := make(chan *LaunchRefusal, 1)
			go func() {
				_, _, refusal := f.pool.authorize(observed, cancel, "client", r.BindingID, r.SessionID, r.Model)
				result <- refusal
			}()
			select {
			case <-observed.waiting:
			case refusal := <-result:
				t.Fatalf("did not queue: %v", refusal)
			}
			wantCode, wantVersion := "capacity_unverified", uint64(1)
			switch kind {
			case "cancel":
				cancel()
				wantVersion = 0
			case "release":
				f.pool.Release("client", r.BindingID, r.SessionID)
				wantVersion = 0
			case "auth-change":
				a, _ := f.manager.GetByID(t.Name() + "-a")
				a.Metadata["access_token"] = "changed-synthetic-only"
				if _, err := f.manager.Update(context.Background(), a); err != nil {
					t.Fatal(err)
				}
				wantCode = "binding_changed"
			case "quota", "identity":
				f.handler = func(w http.ResponseWriter, q *http.Request) {
					account, allowed := "account", false
					if kind == "identity" {
						account, allowed = "foreign", true
					}
					_, _ = w.Write(launchUsage(account, allowed, 12, f.now))
				}
				if kind == "quota" {
					wantCode = "usage_limit"
				}
			}
			for i := 0; i < cap(f.pool.probes); i++ {
				<-f.pool.probes
			}
			refusal := <-result
			if refusal == nil || wantVersion != 0 && refusal.Code != wantCode {
				t.Fatalf("unexpected refusal: %+v", refusal)
			}
			state := f.pool.Inspect("client", r.BindingID, r.SessionID)
			if state.Active || state.RefusalVersion != wantVersion || state.ExecutorAttempts != 1 || f.executor.starts.Load() != 1 || len(f.pool.probes) != 0 {
				t.Fatalf("failed ownership cleanup: %+v", state)
			}
			if wantVersion == 0 {
				if state.Refusal != nil || f.pool.Resume("client", r.BindingID, r.SessionID, 1) {
					t.Fatal("local cancellation/release produced a resumable latch")
				}
				return
			}
			if state.Refusal == nil || state.Refusal.Code != wantCode || f.pool.Resume("client", r.BindingID, r.SessionID, 2) {
				t.Fatal(state)
			}
			if kind == "quota" && (state.Refusal.QuotaScope != "account" || len(state.Refusal.QuotaDomains) != 1 || state.Refusal.QuotaDomains[0] != quotaDomain("account")) {
				t.Fatal("lost authoritative exhaustion scope")
			}
			requireLaunchRefusal(t, f.execute("client", r, r.Model, r.SessionID, false), wantCode)
			if !f.pool.Resume("client", r.BindingID, r.SessionID, 1) || f.pool.Resume("client", r.BindingID, r.SessionID, 1) {
				t.Fatal("acknowledgement not consumed exactly once")
			}
		})
	}
}

func TestDelegatedLaunchProbeWaitCeiling(t *testing.T) {
	f := newLaunchFixture(t)
	f.add(t, "a", "account")
	r := f.pool.Prepare(context.Background(), "client", launchRequest("wait-ceiling"))
	for i := 0; i < cap(f.pool.probes); i++ {
		f.pool.probes <- struct{}{}
	}
	// Actual deadline expiration, without sleeps or a provider call. The test
	// timeout bounds a broken implementation that forgets the admission ceiling.
	requireLaunchRefusal(t, f.execute("client", r, r.Model, r.SessionID, false), "capacity_unverified")
	state := f.pool.Inspect("client", r.BindingID, r.SessionID)
	if state.Active || state.Refusal != nil || state.RefusalVersion != 0 || state.ExecutorAttempts != 0 || f.probes.Load() != 1 {
		t.Fatal(state)
	}
	for i := 0; i < cap(f.pool.probes); i++ {
		<-f.pool.probes
	}
	if err := f.execute("client", r, r.Model, r.SessionID, false); err != nil {
		t.Fatal(err)
	}
}

func TestDelegatedLaunchProbeOwnerRejectsDuplicate(t *testing.T) {
	f := newLaunchFixture(t)
	f.add(t, "a", "account")
	r := f.pool.Prepare(context.Background(), "client", launchRequest("probe-owner"))
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	f.handler = func(w http.ResponseWriter, q *http.Request) {
		close(entered)
		<-release
		_, _ = w.Write(launchUsage("account", true, 12, f.now))
	}
	result := make(chan error, 1)
	go func() { result <- f.execute("client", r, r.Model, r.SessionID, false) }()
	t.Cleanup(func() { unblock() })
	<-entered
	requireLaunchRefusal(t, f.execute("client", r, r.Model, r.SessionID, false), "binding_busy")
	state := f.pool.Inspect("client", r.BindingID, r.SessionID)
	if !state.Active || state.Refusal != nil || state.RefusalVersion != 0 || state.ExecutorAttempts != 0 || f.probes.Load() != 2 {
		t.Fatalf("duplicate escaped probe ownership: %+v", state)
	}
	unblock()
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	if f.executor.starts.Load() != 1 {
		t.Fatal("parallel authorization duplicated generation")
	}
}
