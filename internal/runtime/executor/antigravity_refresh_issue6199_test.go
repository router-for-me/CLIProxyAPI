package executor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

type issue6199AntigravityRefreshResult struct {
	auth *cliproxyauth.Auth
	err  error
}

func issue6199AntigravityAuth(id, refreshToken string) *cliproxyauth.Auth {
	return &cliproxyauth.Auth{
		ID:       id,
		Provider: "antigravity",
		Status:   cliproxyauth.StatusActive,
		Metadata: map[string]any{
			"access_token":  "expired-access",
			"refresh_token": refreshToken,
			"project_id":    "existing-project",
			"expired":       time.Now().Add(-time.Hour).Format(time.RFC3339),
		},
	}
}

func issue6199AntigravityJSONResponse(req *http.Request, body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": {"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}
}

func TestIssue6199AntigravitySharedOAuthHasIndependentAcquisitionDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		started := make(chan context.Context, 1)
		release := make(chan struct{})
		defer close(release)
		tokenCalls := 0
		transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			switch req.URL.String() {
			case "https://oauth2.googleapis.com/token":
				tokenCalls++
				started <- req.Context()
				select {
				case <-req.Context().Done():
					return nil, req.Context().Err()
				case <-release:
					return issue6199AntigravityJSONResponse(req, `{"access_token":"new-access","expires_in":3600}`), nil
				}
			case "https://cloudcode-pa.googleapis.com/v1internal:loadCodeAssist":
				return issue6199AntigravityJSONResponse(req, `{"paidTier":{"id":"tier","availableCredits":[]}}`), nil
			default:
				return nil, fmt.Errorf("unexpected intercepted request: %s", req.URL)
			}
		})
		callerDeadline := time.Now().Add(time.Hour)
		callerCtx, cancelCaller := context.WithDeadline(context.Background(), callerDeadline)
		defer cancelCaller()
		callerCtx = context.WithValue(callerCtx, "cliproxy.roundtripper", transport)
		followerCtx := context.WithValue(context.Background(), "cliproxy.roundtripper", transport)
		executor := &AntigravityExecutor{}
		results := make(chan issue6199AntigravityRefreshResult, 2)
		refreshToken := t.Name() + "-shared-refresh"
		runRefresh := func(ctx context.Context, id string) {
			updated, errRefresh := executor.Refresh(ctx, issue6199AntigravityAuth(id, refreshToken))
			results <- issue6199AntigravityRefreshResult{auth: updated, err: errRefresh}
		}
		go runRefresh(callerCtx, "issue6199-oauth-leader")
		var requestCtx context.Context
		select {
		case requestCtx = <-started:
		case <-time.After(3 * time.Second):
			t.Fatal("OAuth request did not reach the intercepted transport")
		}
		go runRefresh(followerCtx, "issue6199-oauth-follower")
		// Wait until the follower is blocked inside singleflight, without wall-clock sleeps.
		synctest.Wait()
		if tokenCalls != 1 {
			t.Fatalf("shared OAuth requests = %d, want 1", tokenCalls)
		}

		cancelCaller()
		synctest.Wait()
		if errRequestContext := requestCtx.Err(); errRequestContext != nil {
			t.Fatalf("single caller cancellation canceled shared OAuth: %v", errRequestContext)
		}
		deadline, hasDeadline := requestCtx.Deadline()
		if !hasDeadline {
			t.Fatal("shared OAuth request has no credential-acquisition deadline after WithoutCancel")
		}
		if !deadline.After(time.Now()) || !deadline.Before(callerDeadline) {
			t.Fatalf("shared OAuth deadline = %s, want an independent acquisition budget before caller deadline %s", deadline, callerDeadline)
		}

		// Advance only the bubble clock to the actual acquisition deadline.
		timer := time.NewTimer(time.Until(deadline))
		defer timer.Stop()
		<-timer.C
		synctest.Wait()
		for i := 0; i < 2; i++ {
			select {
			case result := <-results:
				if !errors.Is(result.err, context.DeadlineExceeded) {
					t.Errorf("shared refresh error = %v, want context.DeadlineExceeded", result.err)
				}
			case <-time.After(time.Second):
				t.Fatal("shared refresh did not finish at its credential-acquisition deadline")
			}
		}
		if tokenCalls != 1 {
			t.Fatalf("OAuth calls after acquisition deadline = %d, want 1", tokenCalls)
		}
	})
}

func TestIssue6199AntigravityRefreshPublishesTokenWithoutWaitingForCredits(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		releaseCredits := make(chan struct{})
		defer close(releaseCredits)
		tokenCalls, creditsCalls := 0, 0
		transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			switch req.URL.String() {
			case "https://oauth2.googleapis.com/token":
				tokenCalls++
				return issue6199AntigravityJSONResponse(req, `{"access_token":"new-access","expires_in":3600}`), nil
			case "https://cloudcode-pa.googleapis.com/v1internal:loadCodeAssist":
				creditsCalls++
				select {
				case <-releaseCredits:
					return issue6199AntigravityJSONResponse(req, `{"paidTier":{"id":"tier","availableCredits":[]}}`), nil
				case <-req.Context().Done():
					return nil, req.Context().Err()
				}
			default:
				return nil, fmt.Errorf("unexpected intercepted request: %s", req.URL)
			}
		})
		ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", transport)
		executor := &AntigravityExecutor{}
		manager := cliproxyauth.NewManager(nil, nil, nil)
		manager.RegisterExecutor(executor)
		auth := issue6199AntigravityAuth("issue6199-credits", t.Name()+"-refresh")
		if _, errRegister := manager.Register(ctx, auth); errRegister != nil {
			t.Fatalf("register auth: %v", errRegister)
		}
		t.Cleanup(func() {
			antigravityCreditsBalanceByAuth.Delete(auth.ID)
			antigravityCreditsHintRefreshByID.Delete(auth.ID)
		})
		results := make(chan issue6199AntigravityRefreshResult, 1)
		go func() {
			updated, errRefresh := manager.ForceRefreshAuth(ctx, auth.ID)
			results <- issue6199AntigravityRefreshResult{auth: updated, err: errRefresh}
		}()
		synctest.Wait()
		if tokenCalls != 1 {
			t.Fatalf("OAuth token calls = %d, want 1", tokenCalls)
		}

		select {
		case result := <-results:
			if result.err != nil || result.auth == nil {
				t.Fatalf("refresh result = %#v, %v, want successful token refresh", result.auth, result.err)
			}
		default:
			current, _ := manager.GetByID(auth.ID)
			t.Fatalf("OAuth succeeded but refresh is blocked by optional credits lookup (calls=%d); published access_token=%q, want new-access before credits completes", creditsCalls, metaStringValue(current.Metadata, "access_token"))
		}
		current, exists := manager.GetByID(auth.ID)
		if !exists || metaStringValue(current.Metadata, "access_token") != "new-access" || !current.HasValidAccessToken(time.Now()) {
			t.Fatalf("published auth = %#v, want refreshed usable access token while credits is blocked", current)
		}
	})
}

func TestIssue6199AntigravityProjectAcquisitionHasDeadline(t *testing.T) {
	auth := issue6199AntigravityAuth(t.Name(), "unused-refresh")
	delete(auth.Metadata, "project_id")
	auth.Metadata["expired"] = time.Now().Add(time.Hour).Format(time.RFC3339)
	transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() != "https://cloudcode-pa.googleapis.com/v1internal:loadCodeAssist" {
			return nil, fmt.Errorf("unexpected intercepted request: %s", req.URL)
		}
		if deadline, ok := req.Context().Deadline(); !ok || !deadline.After(time.Now()) {
			t.Error("required project acquisition has no finite credential-acquisition deadline")
		}
		return issue6199AntigravityJSONResponse(req, `{"cloudaicompanionProject":"acquired-project"}`), nil
	})
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", transport)
	executor := &AntigravityExecutor{}
	updated, errPrepare := executor.PrepareRequestAuth(ctx, auth)
	if errPrepare != nil || updated == nil || antigravityProjectIDFromAuth(updated) != "acquired-project" {
		t.Fatalf("project preparation = %#v, %v, want acquired project", updated, errPrepare)
	}
}

func TestIssue6199AntigravityOptionalCreditsRefreshIsDeduplicatedWithoutDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		release := make(chan struct{})
		defer close(release)
		creditsCalls := 0
		transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			switch req.URL.String() {
			case "https://oauth2.googleapis.com/token":
				return issue6199AntigravityJSONResponse(req, `{"access_token":"new-access","expires_in":3600}`), nil
			case "https://cloudcode-pa.googleapis.com/v1internal:loadCodeAssist":
				creditsCalls++
				if _, ok := req.Context().Deadline(); ok {
					t.Error("optional credits lookup inherited a network deadline from token acquisition")
				}
				<-release
				return issue6199AntigravityJSONResponse(req, `{"paidTier":{"id":"tier","availableCredits":[]}}`), nil
			default:
				return nil, fmt.Errorf("unexpected intercepted request: %s", req.URL)
			}
		})
		ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", transport)
		executor := &AntigravityExecutor{}
		auth := issue6199AntigravityAuth(t.Name(), t.Name()+"-refresh")
		t.Cleanup(func() {
			antigravityCreditsBalanceByAuth.Delete(auth.ID)
			antigravityCreditsHintRefreshByID.Delete(auth.ID)
		})
		for i := 0; i < 3; i++ {
			results := make(chan issue6199AntigravityRefreshResult, 1)
			go func() {
				updated, errRefresh := executor.Refresh(ctx, auth.Clone())
				results <- issue6199AntigravityRefreshResult{auth: updated, err: errRefresh}
			}()
			synctest.Wait()
			select {
			case result := <-results:
				if result.err != nil || result.auth == nil {
					t.Fatalf("refresh = %#v, %v, want success", result.auth, result.err)
				}
			default:
				t.Fatal("token refresh waited for optional credits")
			}
		}
		if creditsCalls != 1 {
			t.Fatalf("hanging optional credits calls = %d, want 1 across repeated token refreshes", creditsCalls)
		}
	})
}

func issue6199RefreshForCreditsLifecycle(t *testing.T, executor *AntigravityExecutor, ctx context.Context, auth *cliproxyauth.Auth) *cliproxyauth.Auth {
	t.Helper()
	results := make(chan issue6199AntigravityRefreshResult, 1)
	go func() {
		updated, errRefresh := executor.Refresh(ctx, auth)
		results <- issue6199AntigravityRefreshResult{auth: updated, err: errRefresh}
	}()
	synctest.Wait()
	select {
	case result := <-results:
		if result.err != nil || result.auth == nil {
			t.Fatalf("token refresh = %#v, %v, want success", result.auth, result.err)
		}
		return result.auth
	default:
		t.Fatal("token refresh waited for optional credits lifecycle work")
		return nil
	}
}

func issue6199CreditsLifecycleFixture(t *testing.T, authID string) {
	t.Helper()
	reset := func() {
		antigravityCreditsBalanceByAuth.Delete(authID)
		antigravityCreditsHintRefreshByID.Delete(authID)
		cliproxyauth.SetAntigravityCreditsHint(authID, cliproxyauth.AntigravityCreditsHint{})
	}
	// Isolate repeated test runs without changing state between lifecycle transitions.
	reset()
	t.Cleanup(reset)
}

func issue6199CreditsBalanceResponse(req *http.Request, amount float64) *http.Response {
	body := fmt.Sprintf(`{"paidTier":{"id":"tier","availableCredits":[{"creditType":"GOOGLE_ONE_AI","creditAmount":"%g","minimumCreditAmountForUsage":"50"}]}}`, amount)
	return issue6199AntigravityJSONResponse(req, body)
}

func issue6199AssertCreditsBalance(t *testing.T, authID string, amount float64) {
	t.Helper()
	value, found := antigravityCreditsBalanceByAuth.Load(authID)
	balance, valid := value.(antigravityCreditsBalance)
	if !found || !valid || !balance.Known || balance.CreditAmount != amount {
		t.Errorf("published credits balance = %#v, want known balance %g", value, amount)
	}
	hint, known := cliproxyauth.GetAntigravityCreditsHint(authID)
	if !known || !hint.Known || !hint.Available || hint.CreditAmount != amount {
		t.Errorf("published credits hint = %#v (found=%t), want available balance %g", hint, known, amount)
	}
}

func TestIssue6199AntigravityCreditsRecoverAfterLifecycleCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		auth := issue6199AntigravityAuth(t.Name(), t.Name()+"-refresh")
		auth.RegistrationEpoch = 1
		issue6199CreditsLifecycleFixture(t, auth.ID)
		releaseOld := make(chan struct{})
		defer close(releaseOld)
		oldCanceled := make(chan struct{})
		var oldRequest context.Context
		oldTransport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			switch req.URL.String() {
			case "https://oauth2.googleapis.com/token":
				return issue6199AntigravityJSONResponse(req, `{"access_token":"old-access","expires_in":3600}`), nil
			case "https://cloudcode-pa.googleapis.com/v1internal:loadCodeAssist":
				oldRequest = req.Context()
				select {
				case <-req.Context().Done():
					close(oldCanceled)
					return nil, req.Context().Err()
				case <-releaseOld:
					return issue6199CreditsBalanceResponse(req, 10), nil
				}
			default:
				return nil, fmt.Errorf("unexpected intercepted request: %s", req.URL)
			}
		})
		ctx, cancelLifecycle := context.WithCancel(context.WithValue(context.Background(), "cliproxy.roundtripper", oldTransport))
		defer cancelLifecycle()
		executor := &AntigravityExecutor{}
		updated := issue6199RefreshForCreditsLifecycle(t, executor, ctx, auth)
		if oldRequest == nil {
			t.Fatal("old lifecycle credits request did not start")
		}
		if _, ok := oldRequest.Deadline(); ok {
			t.Error("optional credits request acquired a network deadline")
		}

		cancelLifecycle()
		synctest.Wait()
		if !errors.Is(oldRequest.Err(), context.Canceled) {
			t.Errorf("old credits context after lifecycle cancellation = %v, want context.Canceled", oldRequest.Err())
		}
		select {
		case <-oldCanceled:
		default:
			t.Error("old credits transport did not observe lifecycle cancellation")
		}
		// Isolate the held-lock regression from the existing retry interval policy.
		timer := time.NewTimer(antigravityCreditsHintRefreshInterval + time.Nanosecond)
		defer timer.Stop()
		<-timer.C
		healthyCalls := 0
		healthyTransport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			switch req.URL.String() {
			case "https://oauth2.googleapis.com/token":
				return issue6199AntigravityJSONResponse(req, `{"access_token":"healthy-access","expires_in":3600}`), nil
			case "https://cloudcode-pa.googleapis.com/v1internal:loadCodeAssist":
				healthyCalls++
				if _, ok := req.Context().Deadline(); ok {
					t.Error("healthy optional credits request acquired a network deadline")
				}
				return issue6199CreditsBalanceResponse(req, 900), nil
			default:
				return nil, fmt.Errorf("unexpected intercepted request: %s", req.URL)
			}
		})
		newCtx := context.WithValue(context.Background(), "cliproxy.roundtripper", healthyTransport)
		issue6199RefreshForCreditsLifecycle(t, executor, newCtx, updated.Clone())
		if healthyCalls != 1 {
			t.Errorf("healthy same-ID same-epoch credits requests = %d, want 1 after lifecycle cancellation", healthyCalls)
		}
		issue6199AssertCreditsBalance(t, auth.ID, 900)
	})
}

func TestIssue6199AntigravityCreditsReplaceOldRegistrationEpoch(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		auth := issue6199AntigravityAuth(t.Name(), t.Name()+"-refresh")
		auth.RegistrationEpoch = 1
		issue6199CreditsLifecycleFixture(t, auth.ID)
		releaseOld := make(chan struct{})
		defer close(releaseOld)
		oldCanceled := make(chan struct{})
		var oldRequest context.Context
		oldTransport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			switch req.URL.String() {
			case "https://oauth2.googleapis.com/token":
				return issue6199AntigravityJSONResponse(req, `{"access_token":"old-access","expires_in":3600}`), nil
			case "https://cloudcode-pa.googleapis.com/v1internal:loadCodeAssist":
				oldRequest = req.Context()
				select {
				case <-req.Context().Done():
					close(oldCanceled)
					return nil, req.Context().Err()
				case <-releaseOld:
					return issue6199CreditsBalanceResponse(req, 10), nil
				}
			default:
				return nil, fmt.Errorf("unexpected intercepted request: %s", req.URL)
			}
		})
		oldCtx, cancelLifecycle := context.WithCancel(context.WithValue(context.Background(), "cliproxy.roundtripper", oldTransport))
		defer cancelLifecycle()
		executor := &AntigravityExecutor{}
		updated := issue6199RefreshForCreditsLifecycle(t, executor, oldCtx, auth)
		if oldRequest == nil {
			t.Fatal("old registration credits request did not start")
		}
		healthyCalls := 0
		healthyTransport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			switch req.URL.String() {
			case "https://oauth2.googleapis.com/token":
				return issue6199AntigravityJSONResponse(req, `{"access_token":"replacement-access","expires_in":3600}`), nil
			case "https://cloudcode-pa.googleapis.com/v1internal:loadCodeAssist":
				healthyCalls++
				if _, ok := req.Context().Deadline(); ok {
					t.Error("replacement optional credits request acquired a network deadline")
				}
				return issue6199CreditsBalanceResponse(req, 1200), nil
			default:
				return nil, fmt.Errorf("unexpected intercepted request: %s", req.URL)
			}
		})
		replacement := updated.Clone()
		replacement.RegistrationEpoch++
		newCtx := context.WithValue(context.Background(), "cliproxy.roundtripper", healthyTransport)
		issue6199RefreshForCreditsLifecycle(t, executor, newCtx, replacement)
		if !errors.Is(oldRequest.Err(), context.Canceled) {
			t.Errorf("old credits context after registration replacement = %v, want context.Canceled", oldRequest.Err())
		}
		select {
		case <-oldCanceled:
		default:
			t.Error("registration replacement did not cancel the old credits transport")
		}
		if oldCtx.Err() != nil || newCtx.Err() != nil {
			t.Errorf("task replacement canceled a parent lifecycle: old=%v new=%v", oldCtx.Err(), newCtx.Err())
		}
		if healthyCalls != 1 {
			t.Errorf("new registration credits requests = %d, want 1 without the old epoch's throttle", healthyCalls)
		}
		issue6199AssertCreditsBalance(t, auth.ID, 1200)
	})
}

func TestIssue6199AntigravityCanceledCreditsCannotOverwriteReplacementBalance(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		auth := issue6199AntigravityAuth(t.Name(), t.Name()+"-refresh")
		auth.RegistrationEpoch = 1
		issue6199CreditsLifecycleFixture(t, auth.ID)
		releaseOld := make(chan struct{})
		var releaseOnce sync.Once
		defer releaseOnce.Do(func() { close(releaseOld) })
		var oldRequest context.Context
		oldTransport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			switch req.URL.String() {
			case "https://oauth2.googleapis.com/token":
				return issue6199AntigravityJSONResponse(req, `{"access_token":"old-access","expires_in":3600}`), nil
			case "https://cloudcode-pa.googleapis.com/v1internal:loadCodeAssist":
				oldRequest = req.Context()
				// Emulate a transport that returns a successful response after cancellation.
				<-releaseOld
				return issue6199CreditsBalanceResponse(req, 10), nil
			default:
				return nil, fmt.Errorf("unexpected intercepted request: %s", req.URL)
			}
		})
		oldCtx, cancelLifecycle := context.WithCancel(context.WithValue(context.Background(), "cliproxy.roundtripper", oldTransport))
		defer cancelLifecycle()
		executor := &AntigravityExecutor{}
		updated := issue6199RefreshForCreditsLifecycle(t, executor, oldCtx, auth)
		if oldRequest == nil {
			t.Fatal("old registration credits request did not start")
		}
		cancelLifecycle()
		synctest.Wait()
		healthyCalls := 0
		healthyTransport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			if req.URL.String() != "https://cloudcode-pa.googleapis.com/v1internal:loadCodeAssist" {
				return nil, fmt.Errorf("unexpected intercepted request: %s", req.URL)
			}
			healthyCalls++
			return issue6199CreditsBalanceResponse(req, 1500), nil
		})
		replacement := updated.Clone()
		replacement.RegistrationEpoch++
		newCtx := context.WithValue(context.Background(), "cliproxy.roundtripper", healthyTransport)
		// Commit through the real balance updater to isolate publication from admission.
		executor.updateAntigravityCreditsBalance(newCtx, replacement, "replacement-access")
		if healthyCalls != 1 {
			t.Fatalf("replacement balance queries = %d, want 1", healthyCalls)
		}
		issue6199AssertCreditsBalance(t, auth.ID, 1500)
		releaseOnce.Do(func() { close(releaseOld) })
		synctest.Wait()
		issue6199AssertCreditsBalance(t, auth.ID, 1500)
	})
}

func TestIssue6199AntigravityCanceledCreditsRetryWithoutOldThrottle(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		auth := issue6199AntigravityAuth(t.Name(), t.Name()+"-refresh")
		auth.RegistrationEpoch = 1
		issue6199CreditsLifecycleFixture(t, auth.ID)
		release := make(chan struct{})
		defer close(release)
		var oldRequest context.Context
		healthyCalls := 0
		transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			switch req.URL.String() {
			case "https://oauth2.googleapis.com/token":
				return issue6199AntigravityJSONResponse(req, `{"access_token":"new-access","expires_in":3600}`), nil
			case "https://cloudcode-pa.googleapis.com/v1internal:loadCodeAssist":
				if oldRequest == nil {
					oldRequest = req.Context()
					select {
					case <-req.Context().Done():
						return nil, req.Context().Err()
					case <-release:
						return issue6199CreditsBalanceResponse(req, 10), nil
					}
				}
				healthyCalls++
				return issue6199CreditsBalanceResponse(req, 900), nil
			default:
				return nil, fmt.Errorf("unexpected intercepted request: %s", req.URL)
			}
		})
		oldCtx, cancelLifecycle := context.WithCancel(context.WithValue(context.Background(), "cliproxy.roundtripper", transport))
		defer cancelLifecycle()
		executor := &AntigravityExecutor{}
		updated := issue6199RefreshForCreditsLifecycle(t, executor, oldCtx, auth)
		if oldRequest == nil {
			t.Fatal("old credits request did not start")
		}
		cancelLifecycle()
		synctest.Wait()
		newCtx := context.WithValue(context.Background(), "cliproxy.roundtripper", transport)
		// No clock advancement: cancellation must not retain the old task's throttle.
		issue6199RefreshForCreditsLifecycle(t, executor, newCtx, updated.Clone())
		if healthyCalls != 1 {
			t.Errorf("immediate same-epoch credits recovery requests = %d, want 1", healthyCalls)
		}
		issue6199AssertCreditsBalance(t, auth.ID, 900)
	})
}

func TestIssue6199AntigravityOlderEpochCannotReplaceCompletedCredits(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		auth := issue6199AntigravityAuth(t.Name(), t.Name()+"-refresh")
		auth.RegistrationEpoch = 2
		issue6199CreditsLifecycleFixture(t, auth.ID)
		makeTransport := func(amount float64, calls *int) roundTripperFunc {
			return func(req *http.Request) (*http.Response, error) {
				switch req.URL.String() {
				case "https://oauth2.googleapis.com/token":
					return issue6199AntigravityJSONResponse(req, `{"access_token":"new-access","expires_in":3600}`), nil
				case "https://cloudcode-pa.googleapis.com/v1internal:loadCodeAssist":
					*calls++
					return issue6199CreditsBalanceResponse(req, amount), nil
				default:
					return nil, fmt.Errorf("unexpected intercepted request: %s", req.URL)
				}
			}
		}
		newCalls := 0
		newCtx := context.WithValue(context.Background(), "cliproxy.roundtripper", makeTransport(900, &newCalls))
		executor := &AntigravityExecutor{}
		updated := issue6199RefreshForCreditsLifecycle(t, executor, newCtx, auth)
		if newCalls != 1 {
			t.Fatalf("current epoch credits requests = %d, want 1", newCalls)
		}
		issue6199AssertCreditsBalance(t, auth.ID, 900)
		timer := time.NewTimer(antigravityCreditsHintRefreshInterval + time.Nanosecond)
		defer timer.Stop()
		<-timer.C
		stale := updated.Clone()
		stale.RegistrationEpoch--
		oldCalls := 0
		oldCtx := context.WithValue(context.Background(), "cliproxy.roundtripper", makeTransport(10, &oldCalls))
		issue6199RefreshForCreditsLifecycle(t, executor, oldCtx, stale)
		if oldCalls != 0 {
			t.Errorf("older epoch credits requests after throttle elapsed = %d, want 0", oldCalls)
		}
		issue6199AssertCreditsBalance(t, auth.ID, 900)
	})
}
