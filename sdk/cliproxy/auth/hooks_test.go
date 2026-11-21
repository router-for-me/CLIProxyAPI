package auth

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
)

type lifecycleTestHook struct {
	NoopHook
	registered func(*Auth)
	updated    func(*Auth)
	result     func()
}

func (h lifecycleTestHook) OnAuthRegistered(_ context.Context, a *Auth) {
	if h.registered != nil {
		h.registered(a)
	}
}
func (h lifecycleTestHook) OnAuthUpdated(_ context.Context, a *Auth) {
	if h.updated != nil {
		h.updated(a)
	}
}
func (h lifecycleTestHook) OnResult(context.Context, Result) {
	if h.result != nil {
		h.result()
	}
}

func TestAddHookPreservesOriginalHookAndIsolatesAuthSnapshots(t *testing.T) {
	ctx := context.Background()
	var primary, observer [3]int
	m := NewManager(nil, nil, lifecycleTestHook{
		registered: func(a *Auth) { primary[0]++; a.Provider = "mutated" },
		updated:    func(a *Auth) { primary[1]++; a.Provider = "mutated" },
		result:     func() { primary[2]++ },
	})
	check := func(a *Auth) {
		if a.Provider != "original" {
			t.Fatal("hook mutation leaked")
		}
	}
	remove := m.AddHook(lifecycleTestHook{
		registered: func(a *Auth) { observer[0]++; check(a) },
		updated:    func(a *Auth) { observer[1]++; check(a) },
		result:     func() { observer[2]++ },
	})
	a, err := m.Register(ctx, &Auth{ID: t.Name(), Provider: "original"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.UpdateRefreshedAuth(ctx, a, a.Clone()); err != nil {
		t.Fatal(err)
	}
	m.hook.OnResult(ctx, Result{})
	if primary != [3]int{1, 1, 1} || observer != primary {
		t.Fatalf("lost callbacks: primary=%v observer=%v", primary, observer)
	}
	remove()
	remove()
	if _, err = m.Update(ctx, a); err != nil {
		t.Fatal(err)
	}
	m.hook.OnResult(ctx, Result{})
	if primary != [3]int{1, 2, 2} || observer != [3]int{1, 1, 1} {
		t.Fatalf("unsubscribe replaced original hook: primary=%v observer=%v", primary, observer)
	}
}

func TestAddHookSupportsReentrantRemovalAndConcurrentDispatch(t *testing.T) {
	m := NewManager(nil, nil, nil)
	var remove func()
	calls := 0
	remove = m.AddHook(lifecycleTestHook{updated: func(*Auth) { calls++; remove() }})
	m.hook.OnAuthUpdated(context.Background(), &Auth{})
	m.hook.OnAuthUpdated(context.Background(), &Auth{})
	if calls != 1 {
		t.Fatal("reentrant unsubscribe failed")
	}
	var originalCalls atomic.Int64
	m = NewManager(nil, nil, lifecycleTestHook{result: func() { originalCalls.Add(1) }})
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 100 {
				stop := m.AddHook(NoopHook{})
				m.hook.OnResult(context.Background(), Result{})
				stop()
			}
		})
	}
	wg.Wait()
	if originalCalls.Load() != 800 {
		t.Fatal("concurrent subscriptions lost primary events")
	}
}
