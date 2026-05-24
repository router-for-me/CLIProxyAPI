package oauthflight

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
)

// TestDo_SingleCall verifies that a single call to Do invokes fn exactly once
// and returns the value correctly.
func TestDo_SingleCall(t *testing.T) {
	reset()
	calls := atomic.Int64{}
	got, err := Do("single-acct", func() (string, error) {
		calls.Add(1)
		return "token-abc", nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "token-abc" {
		t.Fatalf("expected token-abc, got %s", got)
	}
	if calls.Load() != 1 {
		t.Fatalf("expected fn called 1 time, got %d", calls.Load())
	}
}

// concurrentDo is the shared harness for the 50-goroutine collapse tests.
// It launches n goroutines, all racing to call Do(authID, fn).
//
// Three barriers guarantee genuine single-flight collapse:
//  1. start (closed by caller): all goroutines wait here before calling Do.
//  2. testAfterCommit hook: each goroutine signals committed after it has
//     acquired mu and committed to either the leader or waiter path. fn
//     blocks until all n signals arrive, so the map entry is definitely
//     visible to every goroutine before fn completes.
//  3. fn returns only after receiving from allCommitted.
func runConcurrentDo[T any](n int, authID string, fn func() (T, error)) ([]T, []error, int64) {
	var committed atomic.Int64
	allCommitted := make(chan struct{})
	start := make(chan struct{})

	// Install the test hook before launching goroutines.
	testAfterCommit = func() {
		if committed.Add(1) == int64(n) {
			close(allCommitted)
		}
	}

	results := make([]T, n)
	errs := make([]error, n)
	calls := atomic.Int64{}
	var wg sync.WaitGroup
	wg.Add(n)

	wrappedFn := func() (T, error) {
		calls.Add(1)
		<-allCommitted // block until every goroutine has committed to its path
		return fn()
	}

	for i := 0; i < n; i++ {
		i := i
		go func() {
			defer wg.Done()
			<-start
			v, err := Do(authID, wrappedFn)
			results[i] = v
			errs[i] = err
		}()
	}

	close(start)
	wg.Wait()
	testAfterCommit = nil
	return results, errs, calls.Load()
}

// TestDo_FiftyGoroutineConcurrency verifies that 50 concurrent callers for the
// same authID collapse into exactly one fn invocation and all receive the same
// result.
func TestDo_FiftyGoroutineConcurrency(t *testing.T) {
	reset()
	results, errs, calls := runConcurrentDo(50, "acct-A-50", func() (string, error) {
		return "shared-token", nil
	})
	if calls != 1 {
		t.Fatalf("expected fn called exactly 1 time, got %d", calls)
	}
	for i, v := range results {
		if v != "shared-token" {
			t.Errorf("goroutine %d: expected shared-token, got %s", i, v)
		}
		if errs[i] != nil {
			t.Errorf("goroutine %d: unexpected error: %v", i, errs[i])
		}
	}
}

// TestDo_DifferentAuthIDsNotCollapsed verifies that two different authIDs each
// get their own fn invocation and their own distinct result.
func TestDo_DifferentAuthIDsNotCollapsed(t *testing.T) {
	reset()
	calls := atomic.Int64{}
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)

	var valA, valB string
	var errA, errB error

	go func() {
		defer wg.Done()
		<-start
		valA, errA = Do("acct-X", func() (string, error) {
			calls.Add(1)
			return "token-X", nil
		})
	}()
	go func() {
		defer wg.Done()
		<-start
		valB, errB = Do("acct-Y", func() (string, error) {
			calls.Add(1)
			return "token-Y", nil
		})
	}()

	close(start)
	wg.Wait()

	if calls.Load() != 2 {
		t.Fatalf("expected fn called 2 times (once per authID), got %d", calls.Load())
	}
	if errA != nil || errB != nil {
		t.Fatalf("unexpected errors: %v, %v", errA, errB)
	}
	if valA != "token-X" {
		t.Errorf("acct-X: expected token-X, got %s", valA)
	}
	if valB != "token-Y" {
		t.Errorf("acct-Y: expected token-Y, got %s", valB)
	}
}

// TestDo_SequentialCallsRunIndependently verifies that after the in-flight
// entry is cleaned up, a subsequent call for the same authID invokes fn again.
func TestDo_SequentialCallsRunIndependently(t *testing.T) {
	reset()
	calls := atomic.Int64{}
	authID := "acct-seq"

	for i := 0; i < 3; i++ {
		_, err := Do(authID, func() (string, error) {
			calls.Add(1)
			return "tok", nil
		})
		if err != nil {
			t.Fatalf("call %d: unexpected error: %v", i, err)
		}
	}

	if calls.Load() != 3 {
		t.Fatalf("expected fn called 3 times for 3 sequential calls, got %d", calls.Load())
	}
}

// TestDo_ErrorPropagatedToAllWaiters verifies that when fn returns an error,
// all 50 concurrent waiters receive that same error.
func TestDo_ErrorPropagatedToAllWaiters(t *testing.T) {
	reset()
	sentinel := errors.New("refresh_token_reused")
	_, errs, calls := runConcurrentDo(50, "acct-err", func() (string, error) {
		return "", sentinel
	})
	if calls != 1 {
		t.Fatalf("expected fn called exactly 1 time, got %d", calls)
	}
	for i, err := range errs {
		if !errors.Is(err, sentinel) {
			t.Errorf("goroutine %d: expected sentinel error, got %v", i, err)
		}
	}
}
