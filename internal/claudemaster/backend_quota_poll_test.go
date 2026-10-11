package claudemaster

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func backendQuotaPollFixture(t *testing.T) (*coreauth.Manager, *backendSeriesSelector, time.Time) {
	t.Helper()
	now := time.Date(2026, time.October, 1, 12, 0, 0, 0, time.UTC)
	selector := &backendSeriesSelector{
		authIDs: []string{"poll-a", "poll-b"}, backupAuthID: "poll-api", provider: "claude",
		now: func() time.Time { return now },
	}
	t.Cleanup(selector.Stop)
	manager := coreauth.NewManager(nil, selector, nil)
	for _, id := range []string{"poll-a", "poll-b", "poll-api"} {
		auth := &coreauth.Auth{
			ID: id, Provider: "claude", Status: coreauth.StatusActive,
			Attributes: map[string]string{coreauth.AttributeAuthKind: coreauth.AuthKindOAuth, coreauth.AttributeRuntimeOnly: "true"},
			Metadata:   map[string]any{"access_token": "test-access-token", "expired": "2099-01-01T00:00:00Z"},
		}
		if id == selector.backupAuthID {
			auth.Attributes[coreauth.AttributeAuthKind] = coreauth.AuthKindAPIKey
			auth.Attributes[coreauth.AttributeAPIKey] = "test-api-key"
		}
		if _, err := manager.Register(t.Context(), auth); err != nil {
			t.Fatal(err)
		}
	}
	return manager, selector, now
}

func backendQuotaPollResponse(usedPercent int, reset time.Time) *http.Response {
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(fmt.Sprintf(
		`{"seven_day":{"utilization":%d,"resets_at":%q}}`, usedPercent, reset.Format(time.RFC3339),
	)))}
}

func backendQuotaPollSnapshot(selector *backendSeriesSelector, authID string) backendWeeklyQuota {
	selector.mu.Lock()
	defer selector.mu.Unlock()
	return selector.quota[authID]
}

func TestBackendQuotaPollingRetriesStartupFailureForAllSubscriptionsOnly(t *testing.T) {
	manager, selector, now := backendQuotaPollFixture(t)
	ids := append(append([]string(nil), selector.authIDs...), selector.backupAuthID, "missing")
	var mu sync.Mutex
	calls := make(map[string]int)
	startup := true
	request := func(ctx context.Context, auth *coreauth.Auth, req *http.Request) (*http.Response, error) {
		if _, hasDeadline := ctx.Deadline(); hasDeadline {
			return nil, errors.New("usage HTTP unexpectedly has a deadline")
		}
		if auth.AuthKind() != coreauth.AuthKindOAuth || req.URL.String() != ClaudeOAuthUsageEndpoint || req.Method != http.MethodGet {
			return nil, errors.New("unexpected quota account or request")
		}
		mu.Lock()
		calls[auth.ID]++
		mu.Unlock()
		if startup {
			return &http.Response{StatusCode: http.StatusServiceUnavailable, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
		}
		return backendQuotaPollResponse(20, now.Add(24*time.Hour)), nil
	}
	loadBackendWeeklyQuotas(t.Context(), manager, selector, ids, request)
	for _, id := range selector.authIDs {
		if quota := backendQuotaPollSnapshot(selector, id); quota.known {
			t.Fatalf("failed startup invented quota for %s: %#v", id, quota)
		}
	}
	startup = false
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	ticks := make(chan time.Time)
	done := startBackendQuotaPolling(ctx, manager, selector, ids, request, ticks)
	ticks <- now
	close(ticks)
	<-done
	for _, id := range selector.authIDs {
		if quota := backendQuotaPollSnapshot(selector, id); !quota.known || quota.used != 0.2 || !quota.resetsAt.Equal(now.Add(24*time.Hour)) {
			t.Fatalf("background retry did not refresh %s: %#v", id, quota)
		}
		if calls[id] != 2 {
			t.Fatalf("quota calls[%s] = %d, want startup and background retry", id, calls[id])
		}
	}
	if len(calls) != len(selector.authIDs) {
		t.Fatalf("quota requests included an API key or missing account: %#v", calls)
	}
	if backendQuotaPollInterval != 3*time.Minute {
		t.Fatalf("quota polling interval = %s, want three minutes", backendQuotaPollInterval)
	}
}

func TestBackendSeriesConstructorStartsAndClosesQuotaPolling(t *testing.T) {
	opts := writeSyntheticBackendCredential(t, t.TempDir(), "selected.json", map[string]any{
		"type": "claude", "account_uuid": integrationClaudeAccount,
		"claude_device_ids": []string{integrationClaudeDevice},
	})
	var calls int
	backend, err := NewBackendSeries(t.Context(), BackendSeriesOptions{
		Credentials:  []BackendCredential{{AuthDir: opts.AuthDir, Provider: "claude", AuthID: opts.AuthID}},
		BackupAPIKey: backendAPIBackupSyntheticKey,
		QuotaRequest: func(_ context.Context, auth *coreauth.Auth, _ *http.Request) (*http.Response, error) {
			if auth.AuthKind() != coreauth.AuthKindOAuth {
				return nil, errors.New("constructor queried non-subscription usage")
			}
			calls++
			return backendQuotaPollResponse(20, time.Date(2099, time.January, 1, 0, 0, 0, 0, time.UTC)), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	if backend.quotaPollDone == nil {
		t.Fatal("real backend constructor did not start subscription quota polling")
	}
	select {
	case <-backend.quotaPollDone:
		t.Fatal("constructor's quota polling stopped before shutdown")
	default:
	}
	if err := backend.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-backend.quotaPollDone:
	default:
		t.Fatal("real backend Close returned without joining its quota poller")
	}
	store := backend.store.(*backendStore)
	store.mu.Lock()
	sealed := store.closed
	store.mu.Unlock()
	if !sealed || calls != 1 {
		t.Fatalf("backend shutdown/startup wiring incorrect: sealed=%v startup quota calls=%d", sealed, calls)
	}
}

func TestBackendQuotaPollFailuresKeepLastKnownState(t *testing.T) {
	for _, tc := range []struct {
		name string
		do   ClaudeQuotaRequestFunc
	}{
		{name: "transport", do: func(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
			return nil, errors.New("offline")
		}},
		{name: "http", do: func(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusUnauthorized, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
		}},
		{name: "unknown_schema", do: func(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"future_meter":10}`))}, nil
		}},
		{name: "malformed", do: func(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{`))}, nil
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manager, selector, now := backendQuotaPollFixture(t)
			want := backendWeeklyQuota{known: true, used: 1, resetsAt: now.Add(24 * time.Hour)}
			selector.observeQuota("poll-a", want)
			revision := selector.quotaPollRevision("poll-a")
			loadBackendWeeklyQuotas(t.Context(), manager, selector, []string{"poll-a"}, tc.do)
			if got := backendQuotaPollSnapshot(selector, "poll-a"); got != want {
				t.Fatalf("failed poll replaced the last quota: got %#v, want %#v", got, want)
			}
			if got := selector.quotaPollRevision("poll-a"); got != revision {
				t.Fatalf("failed poll changed observation revision: got %d, want %d", got, revision)
			}
		})
	}
}

func TestBackendQuotaPollConfirmsLowerUsageAndCorrectsPredictedReset(t *testing.T) {
	manager, selector, now := backendQuotaPollFixture(t)
	reset := now.Add(time.Hour)
	selector.observeQuota("poll-a", backendWeeklyQuota{known: true, used: 1, resetsAt: reset})
	request := func(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
		return backendQuotaPollResponse(20, reset), nil
	}
	loadBackendWeeklyQuotas(t.Context(), manager, selector, []string{"poll-a"}, request)
	if got := backendQuotaPollSnapshot(selector, "poll-a"); got.used != 0.2 || !got.resetsAt.Equal(reset) {
		t.Fatalf("authoritative same-window decrease was ignored: %#v", got)
	}
	selector.mu.Lock()
	selector.now = func() time.Time { return now.Add(2 * time.Hour) }
	predicted := selector.currentQuotaLocked("poll-a")
	selector.mu.Unlock()
	if predicted.used != 0 || !predicted.resetsAt.Equal(reset.Add(7*24*time.Hour)) {
		t.Fatalf("unexpected lazy reset prediction: %#v", predicted)
	}
	actualReset := reset.Add(6 * 24 * time.Hour)
	loadBackendWeeklyQuotas(t.Context(), manager, selector, []string{"poll-a"}, func(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
		return backendQuotaPollResponse(40, actualReset), nil
	})
	if got := backendQuotaPollSnapshot(selector, "poll-a"); got.used != 0.4 || !got.resetsAt.Equal(actualReset) {
		t.Fatalf("actual usage snapshot did not correct the predicted reset: %#v", got)
	}
}

func TestBackendQuotaPollCannotOverwriteNewerInferenceObservation(t *testing.T) {
	for _, observation := range []string{"weekly_header", "ignored_lower_weekly_header", "credential_429"} {
		t.Run(observation, func(t *testing.T) {
			manager, selector, now := backendQuotaPollFixture(t)
			reset := now.Add(24 * time.Hour)
			used, polledPercent := 0.2, 0
			if observation == "ignored_lower_weekly_header" {
				used, polledPercent = 0.9, 70
			}
			selector.observeQuota("poll-a", backendWeeklyQuota{known: true, used: used, resetsAt: reset})
			entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
			go func() {
				defer close(done)
				loadBackendWeeklyQuotas(t.Context(), manager, selector, []string{"poll-a"}, func(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
					close(entered)
					<-release
					return backendQuotaPollResponse(polledPercent, reset), nil
				})
			}()
			<-entered
			want := backendWeeklyQuota{known: true, used: 1, resetsAt: reset.Add(24 * time.Hour)}
			if observation == "weekly_header" {
				selector.observeQuota("poll-a", want)
			} else if observation == "ignored_lower_weekly_header" {
				want = backendQuotaPollSnapshot(selector, "poll-a")
				selector.observeQuota("poll-a", backendWeeklyQuota{known: true, used: 0.8, resetsAt: reset})
			} else {
				want = backendQuotaPollSnapshot(selector, "poll-a")
				retry := time.Hour
				selector.OnResult(coreauth.Result{AuthID: "poll-a", Model: "claude-test", CredentialScope: true, RetryAfter: &retry, Error: &coreauth.Error{HTTPStatus: http.StatusTooManyRequests}})
			}
			close(release)
			<-done
			if got := backendQuotaPollSnapshot(selector, "poll-a"); got != want {
				t.Fatalf("slow poll replaced newer %s state: got %#v, want %#v", observation, got, want)
			}
			if observation == "credential_429" {
				selector.mu.Lock()
				until := selector.quotaBlockedUntil["poll-a"]
				selector.mu.Unlock()
				if !until.Equal(now.Add(time.Hour)) {
					t.Fatalf("slow poll erased a newer short-window rejection: %s", until)
				}
			}
		})
	}
}

func TestBackendQuotaPollRechargeReturnsCleanAPIWorkToSubscription(t *testing.T) {
	manager, selector, now := backendQuotaPollFixture(t)
	auths := make([]*coreauth.Auth, 0, 3)
	for _, id := range append(append([]string(nil), selector.authIDs...), selector.backupAuthID) {
		auth, _ := manager.GetByID(id)
		auths = append(auths, auth)
		if id != selector.backupAuthID {
			selector.observeQuota(id, backendWeeklyQuota{known: true, used: 1, resetsAt: now.Add(24 * time.Hour)})
		}
	}
	plain := backendAPIBackupTestOptions("polled-recharge", false)
	requireBackendAPIBackupPick(t, selector, plain, auths, "poll-api")
	loadBackendWeeklyQuotas(t.Context(), manager, selector, []string{"poll-a"}, func(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
		return backendQuotaPollResponse(0, now.Add(7*24*time.Hour)), nil
	})
	requireBackendAPIBackupPick(t, selector, backendAPIBackupTestOptions("polled-recharge", true), auths, "poll-api")
	requireBackendAPIBackupPick(t, selector, plain, auths, "poll-a")
}

func TestBackendQuotaPollKeepsActiveCredentialCooldownAndOpaqueBinding(t *testing.T) {
	manager, selector, now := backendQuotaPollFixture(t)
	selector.mu.Lock()
	selector.initializeLocked()
	selector.sessions.Set("bound-session", "poll-a")
	selector.mu.Unlock()
	retry := 5 * time.Hour
	selector.OnResult(coreauth.Result{AuthID: "poll-a", Model: "claude-test", CredentialScope: true, RetryAfter: &retry, Error: &coreauth.Error{HTTPStatus: http.StatusTooManyRequests}})
	loadBackendWeeklyQuotas(t.Context(), manager, selector, []string{"poll-a"}, func(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
		return backendQuotaPollResponse(10, now.Add(24*time.Hour)), nil
	})
	selector.mu.Lock()
	until := selector.quotaBlockedUntil["poll-a"]
	bound, ok := selector.sessions.GetAndRefresh("bound-session")
	selector.mu.Unlock()
	if !until.Equal(now.Add(retry)) || !ok || bound != "poll-a" {
		t.Fatalf("weekly capacity cleared short-window state or binding: until=%s bound=%q ok=%v", until, bound, ok)
	}
	if got := backendQuotaPollSnapshot(selector, "poll-a"); !got.known || got.used != 0.1 {
		t.Fatalf("weekly quota itself was not refreshed: %#v", got)
	}
}

func TestBackendQuotaPollingStalledAccountDoesNotStarveOtherAccountsOrOverlap(t *testing.T) {
	manager, selector, now := backendQuotaPollFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	ticks := make(chan time.Time)
	firstA, firstB, secondB := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var mu sync.Mutex
	calls := make(map[string]int)
	request := func(ctx context.Context, auth *coreauth.Auth, _ *http.Request) (*http.Response, error) {
		mu.Lock()
		calls[auth.ID]++
		n := calls[auth.ID]
		mu.Unlock()
		if auth.ID == "poll-a" {
			if n == 1 {
				close(firstA)
			}
			<-ctx.Done()
			return nil, ctx.Err()
		}
		if n == 1 {
			close(firstB)
			return backendQuotaPollResponse(20, now.Add(24*time.Hour)), nil
		}
		if n == 2 {
			close(secondB)
		}
		<-ctx.Done()
		return nil, ctx.Err()
	}
	done := startBackendQuotaPolling(ctx, manager, selector, selector.authIDs, request, ticks)
	ticks <- now
	<-firstA
	<-firstB
	// Drive subsequent rounds until B's completed request becomes eligible again.
	// Channels, rather than a wall-clock delay, synchronize worker completion.
	stopTicks, ticksDone := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(ticksDone)
		for {
			select {
			case ticks <- now:
			case <-stopTicks:
				return
			}
		}
	}()
	<-secondB
	close(stopTicks)
	<-ticksDone
	// Both accounts now have a blocked request. Extra ticks cannot duplicate them.
	ticks <- now
	ticks <- now
	cancel()
	<-done
	if calls["poll-a"] != 1 || calls["poll-b"] != 2 {
		t.Fatalf("polling overlapped per-account work or starved healthy B: %#v", calls)
	}
}

type backendQuotaPollSealStore struct{ sealed chan struct{} }

func (*backendQuotaPollSealStore) List(context.Context) ([]*coreauth.Auth, error) { return nil, nil }
func (*backendQuotaPollSealStore) Save(context.Context, *coreauth.Auth) (string, error) {
	return "", nil
}
func (*backendQuotaPollSealStore) Delete(context.Context, string) error { return nil }
func (s *backendQuotaPollSealStore) seal()                              { close(s.sealed) }

func TestBackendCloseCancelsAndJoinsQuotaPollingBeforeSealingCredentials(t *testing.T) {
	manager, selector, now := backendQuotaPollFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	ticks := make(chan time.Time)
	entered, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	store := &backendQuotaPollSealStore{sealed: make(chan struct{})}
	backend := &Backend{manager: manager, seriesSelector: selector, store: store, cancel: cancel}
	backend.quotaPollDone = startBackendQuotaPolling(ctx, manager, selector, []string{"poll-a"}, func(ctx context.Context, _ *coreauth.Auth, _ *http.Request) (*http.Response, error) {
		close(entered)
		<-ctx.Done()
		close(canceled)
		<-release
		return nil, ctx.Err()
	}, ticks)
	var releaseOnce sync.Once
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(release) })
		_ = backend.Close()
	})
	ticks <- now
	<-entered
	closed := make(chan struct{})
	go func() {
		_ = backend.Close()
		close(closed)
	}()
	<-canceled
	select {
	case <-closed:
		t.Fatal("Close returned before the canceled quota worker finished")
	case <-store.sealed:
		t.Fatal("credential store was sealed before the quota worker finished")
	default:
	}
	releaseOnce.Do(func() { close(release) })
	<-closed
	select {
	case <-store.sealed:
	default:
		t.Fatal("Close did not seal the credential store after joining polling")
	}
}
