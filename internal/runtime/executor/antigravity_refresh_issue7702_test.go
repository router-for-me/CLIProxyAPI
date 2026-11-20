package executor

import (
	"context"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

// TestIssue7702AntigravityPostRefreshCreditsHasBoundedTimeout reproduces issue
// #7702: after a successful OAuth token refresh, refreshToken queued the
// credits warm with timeout=0, binding the background goroutine to the caller's
// context with no independent deadline. When loadCodeAssist stalls on an
// unresponsive upstream, the goroutine blocks forever, state.task stays
// non-nil, and every later credits refresh for that authID is permanently
// dropped. The post-refresh warm must run under a bounded context so a stalled
// upstream self-releases the task.
func TestIssue7702AntigravityPostRefreshCreditsHasBoundedTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		auth := issue6199AntigravityAuth(t.Name(), t.Name()+"-refresh")
		issue6199CreditsLifecycleFixture(t, auth.ID)

		var creditsRequest context.Context
		creditsCalls := 0
		var stalled atomic.Bool
		stalled.Store(true)
		transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			switch req.URL.String() {
			case "https://oauth2.googleapis.com/token":
				return issue6199AntigravityJSONResponse(req, `{"access_token":"new-access","expires_in":3600}`), nil
			case "https://daily-cloudcode-pa.googleapis.com/v1internal:loadCodeAssist":
				creditsCalls++
				if !stalled.Load() {
					return issue6199CreditsBalanceResponse(req, 900), nil
				}
				creditsRequest = req.Context()
				// Emulate an upstream that accepts the connection but never
				// sends headers: block until the request context is done.
				<-req.Context().Done()
				return nil, req.Context().Err()
			default:
				return nil, fmt.Errorf("unexpected intercepted request: %s", req.URL)
			}
		})
		// A long-lived caller context with no deadline, matching a background
		// refresh lifecycle. The stalled loadCodeAssist must still be bounded.
		ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", transport)
		executor := &AntigravityExecutor{}

		updated := issue6199RefreshForCreditsLifecycle(t, executor, ctx, auth)
		if updated == nil {
			t.Fatal("token refresh did not complete")
		}
		if creditsCalls != 1 {
			t.Fatalf("post-refresh credits calls = %d, want 1", creditsCalls)
		}
		if creditsRequest == nil {
			t.Fatal("post-refresh credits request did not start")
		}
		if _, hasDeadline := creditsRequest.Deadline(); !hasDeadline {
			t.Error("post-refresh credits request has no deadline; a stalled upstream leaks the goroutine and deadlocks state.task")
		}

		// Advancing the bubble clock past the bounded timeout must release the
		// stalled task so state.task clears and a subsequent refresh can run.
		timer := time.NewTimer(antigravityCreditsHintRefreshTimeout + time.Nanosecond)
		defer timer.Stop()
		<-timer.C
		synctest.Wait()

		stateVal, ok := antigravityCreditsHintRefreshByID.Load(auth.ID)
		if !ok {
			t.Fatal("expected credits refresh state to be registered for auth")
		}
		state, ok := stateVal.(*antigravityCreditsHintRefreshState)
		if !ok || state == nil {
			t.Fatalf("unexpected credits refresh state type %T", stateVal)
		}
		state.mu.Lock()
		remaining := state.task
		state.mu.Unlock()
		if remaining != nil {
			t.Fatal("stalled post-refresh credits task was not released after the bounded timeout; subsequent refreshes are deadlocked")
		}

		// Once the upstream recovers and the retry interval elapses, the same
		// authID must be able to refresh again and publish a balance, proving the
		// earlier stall did not permanently wedge the state.
		stalled.Store(false)
		intervalTimer := time.NewTimer(antigravityCreditsHintRefreshInterval + time.Nanosecond)
		defer intervalTimer.Stop()
		<-intervalTimer.C
		recovered := issue6199AntigravityAuth(t.Name(), t.Name()+"-refresh")
		issue6199RefreshForCreditsLifecycle(t, executor, ctx, recovered)
		synctest.Wait()
		issue6199AssertCreditsBalance(t, auth.ID, 900)
	})
}
