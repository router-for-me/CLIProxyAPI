package claudemaster

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

const creditRefusal = `{"type":"error","error":{"type":"invalid_request_error","message":"Your credit balance is too low to access the Anthropic API. Please go to Plans & Billing to upgrade or purchase credits."}}`

// An exhausted pool hands work to a backup that can pay, and never to one that cannot: the session gets the
// weekly-limit refusal (Anthropic's 429) and waits, instead of the backup's credit error.
func TestExhaustedPoolSkipsABackupThatCannotPay(t *testing.T) {
	selector, auths, reset := backendAPIBackupTestSelector(t)
	selector.observeQuota("subscription-a", backendWeeklyQuota{known: true, used: 1, resetsAt: reset})
	selector.observeQuota("subscription-b", backendWeeklyQuota{known: true, used: 1, resetsAt: reset.Add(time.Hour)})
	requireBackendAPIBackupPick(t, selector, backendAPIBackupTestOptions("funded", false), auths, "api-backup")

	selector.setBackupHealth(false, "credit balance too low")
	ctx := withBackendAttempt(t.Context())
	auth, err := selector.Pick(ctx, "mixed", backendAuthSelectionModel, backendAPIBackupTestOptions("unfunded", false), auths)
	var refusal *backendRefusalError
	if auth != nil || !errors.As(err, &refusal) || refusal.kind != backendRefusalQuota {
		t.Fatalf("Pick() = %#v, %v; want the weekly-limit refusal, not the backup", auth, err)
	}

	selector.setBackupHealth(true, "a token count was answered")
	requireBackendAPIBackupPick(t, selector, backendAPIBackupTestOptions("funded-again", false), auths, "api-backup")
}

// A real backup request refused for credit marks the backup unavailable at once; other errors, or the same text
// from a subscription, do not.
func TestCreditRefusalMarksTheBackupUnavailable(t *testing.T) {
	selector, _, _ := backendAPIBackupTestSelector(t)
	selector.noteBackupUpstream("subscription-a", &backendUpstreamError{status: 400, body: []byte(creditRefusal)})
	selector.noteBackupUpstream("api-backup", &backendUpstreamError{status: 529, body: []byte(`{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`)})
	if state := selector.backupStateLocked(); state != "unchecked" {
		t.Fatalf("unrelated errors changed the backup state to %q", state)
	}
	selector.noteBackupUpstream("api-backup", &backendUpstreamError{status: 400, body: []byte(creditRefusal)})
	if state := selector.backupStateLocked(); state != "unavailable" {
		t.Fatalf("backup state %q after a credit refusal", state)
	}
}

// The executor wrapper hands every upstream error to its observer, which is how the passive check sees them.
func TestRecordingExecutorReportsUpstreamErrorsToItsObserver(t *testing.T) {
	var seen []string
	inner := &scriptedExecutor{answers: []error{&bodilessUpstreamErr{status: 400, headers: http.Header{"Content-Type": {"application/json"}}, body: []byte(creditRefusal)}}}
	ex := withBackendUpstreamRecordingObserved(inner, func(authID string, record *backendUpstreamError) {
		if backendCreditExhausted(record) {
			seen = append(seen, authID)
		}
	})
	_, _ = ex.Execute(withBackendAttempt(context.Background()), backendSeriesTestAuth("api-backup", "claude"), coreexecutor.Request{Model: "claude-test-model"}, coreexecutor.Options{})
	if len(seen) != 1 || seen[0] != "api-backup" {
		t.Fatalf("observer saw %v", seen)
	}
}

// The check reads Anthropic's answer to a token count: answered or model-not-offered means it can pay; a credit
// refusal or a refused key means it cannot; anything else is inconclusive and changes nothing.
func TestBackupCreditCheckClassifiesAnthropicsAnswer(t *testing.T) {
	for name, tc := range map[string]struct {
		status                int
		body                  string
		available, conclusive bool
	}{
		"answered":      {200, `{"input_tokens":8}`, true, true},
		"no credit":     {400, creditRefusal, false, true},
		"key refused":   {401, `{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`, false, true},
		"model missing": {404, `{"type":"error","error":{"type":"not_found_error","message":"model: x"}}`, true, true},
		"overloaded":    {529, `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`, false, false},
		"other 400":     {400, `{"type":"error","error":{"type":"invalid_request_error","message":"bad"}}`, false, false},
	} {
		t.Run(name, func(t *testing.T) {
			var gotKey, gotPath string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotKey, gotPath = r.Header.Get("x-api-key"), r.URL.Path
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			available, conclusive, why := newBackupCreditCheck("test-key", srv.URL, srv.Client())(t.Context())
			if available != tc.available || conclusive != tc.conclusive || why == "" {
				t.Fatalf("got available=%v conclusive=%v why=%q", available, conclusive, why)
			}
			if gotKey != "test-key" || gotPath != "/v1/messages/count_tokens" {
				t.Fatalf("check sent key %q to %q", gotKey, gotPath)
			}
		})
	}
}

// Checks run at start and on every tick; an inconclusive check leaves the last conclusive state alone.
func TestBackupChecksRunAtStartAndOnTicks(t *testing.T) {
	selector, _, _ := backendAPIBackupTestSelector(t)
	var calls atomic.Int32
	answers := []struct{ available, conclusive bool }{{false, true}, {false, false}, {true, true}}
	ticks := make(chan time.Time)
	ctx, cancel := context.WithCancel(t.Context())
	done := startBackupCreditChecks(ctx, selector, func(context.Context) (bool, bool, string) {
		a := answers[min(int(calls.Load()), len(answers)-1)]
		calls.Add(1)
		return a.available, a.conclusive, "test"
	}, ticks)
	wait := func(n int32) {
		deadline := time.Now().Add(2 * time.Second)
		for calls.Load() < n && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		time.Sleep(5 * time.Millisecond)
	}
	wait(1)
	selector.mu.Lock()
	first := selector.backupStateLocked()
	selector.mu.Unlock()
	ticks <- time.Now()
	wait(2)
	selector.mu.Lock()
	second := selector.backupStateLocked()
	selector.mu.Unlock()
	ticks <- time.Now()
	wait(3)
	selector.mu.Lock()
	third := selector.backupStateLocked()
	selector.mu.Unlock()
	cancel()
	<-done
	if first != "unavailable" || second != "unavailable" || third != "available" {
		t.Fatalf("states %q, %q, %q; want unavailable, unavailable (inconclusive kept it), available", first, second, third)
	}
}

// The network check is opt-in: the launcher turns it on, and a backend built directly (every test) makes no call.
func TestTheLauncherTurnsTheBackupCheckOn(t *testing.T) {
	src, err := os.ReadFile("launcher.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), "BackupAPIKey: opts.BackupAPIKey, CheckBackupCredit: true") {
		t.Fatal("the launcher does not turn the backup credit check on")
	}
}
