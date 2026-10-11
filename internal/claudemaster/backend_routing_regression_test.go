package claudemaster

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestBackendAPIBackupHistoricalFailureDoesNotVetoNewRequest(t *testing.T) {
	selector, auths, reset := backendAPIBackupTestSelector(t)
	oldCtx := withBackendAttempt(t.Context())
	result := coreauth.Result{AuthID: "subscription-a", Model: "claude-test", CredentialScope: true, Error: &coreauth.Error{HTTPStatus: http.StatusServiceUnavailable}}
	recordBackendAttempt(oldCtx, result)
	selector.OnResult(result)
	for _, id := range selector.authIDs {
		revision := selector.quotaPollRevision(id)
		if !selector.observePolledQuota(id, backendWeeklyQuota{known: true, used: 1, resetsAt: reset}, revision) {
			t.Fatal("failed to apply authoritative exhaustion")
		}
	}
	opts := backendAPIBackupTestOptions("new-request", false)
	if got, err := selector.Pick(oldCtx, "claude", backendAuthSelectionModel, opts, auths); err == nil || got != nil {
		t.Fatal("unrelated failure was retried on paid backup in the same request")
	}
	ctx := withBackendAttempt(t.Context())
	got, err := selector.Pick(ctx, "claude", backendAuthSelectionModel, opts, auths)
	if err != nil || got == nil || got.ID != "api-backup" {
		t.Fatalf("historical failure vetoed new request: got=%v error=%v", got, err)
	}
}

func TestBackendSeriesAllReserveDrainsEarliestResetWithoutPingPong(t *testing.T) {
	selector, auths, reset := backendAPIBackupTestSelector(t)
	selector.observeQuota("subscription-a", backendWeeklyQuota{known: true, used: .95, resetsAt: reset})
	selector.observeQuota("subscription-b", backendWeeklyQuota{known: true, used: .95, resetsAt: reset.Add(time.Hour)})
	opts := backendAPIBackupTestOptions("all-reserve", false)
	for turn := 0; turn < 8; turn++ {
		requireBackendAPIBackupPick(t, selector, opts, auths, "subscription-a")
	}
	// A session already on the later-reset reserve moves once to the earliest.
	identity, _ := backendSeriesSessionIDs(opts)
	selector.sessions.Set(identity, "subscription-b")
	for turn := 0; turn < 8; turn++ {
		requireBackendAPIBackupPick(t, selector, opts, auths, "subscription-a")
	}
}

func TestBackendSeriesReserveKeepsUsableBindingWhenAlternativeModelCools(t *testing.T) {
	selector, auths, reset := backendAPIBackupTestSelector(t)
	selector.observeQuota("subscription-a", backendWeeklyQuota{known: true, used: .2, resetsAt: reset})
	selector.observeQuota("subscription-b", backendWeeklyQuota{known: true, used: .2, resetsAt: reset.Add(time.Hour)})
	for _, auth := range auths {
		if auth.ID == "subscription-b" {
			auth.ModelStates = map[string]*coreauth.ModelState{"claude-test": {Status: coreauth.StatusError, Unavailable: true, NextRetryAfter: time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)}}
		}
	}
	manager := coreauth.NewManager(nil, selector, nil)
	manager.SetRetryConfig(0, 0, len(auths))
	setBackendResultPolicy(manager, selector)
	capture := &backendSeriesCapture{}
	manager.RegisterExecutor(capture)
	for _, auth := range auths {
		registry.GetGlobalRegistry().RegisterClient(auth.ID, "claude", []*registry.ModelInfo{{ID: backendAuthSelectionModel}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
		if _, err := manager.Register(coreauth.WithSkipPersist(t.Context()), auth); err != nil {
			t.Fatal(err)
		}
	}
	handler := newBackendHandler(t.Context(), BackendOptions{Provider: "claude", UseRequestModel: true}, handlers.NewBaseAPIHandlers(&config.SDKConfig{}, manager))
	request := func() {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"claude-test","messages":[{"role":"user","content":"hello"}]}`))
		r.Header.Set("X-Claude-Code-Session-Id", "reserve-model-cooldown")
		handler.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("usable bound subscription failed: status=%d body=%s", w.Code, w.Body.String())
		}
	}
	request()
	selector.observeQuota("subscription-a", backendWeeklyQuota{known: true, used: .9, resetsAt: reset})
	request()
	capture.mu.Lock()
	defer capture.mu.Unlock()
	if got := strings.Join(capture.calls, ","); got != "subscription-a,subscription-a" {
		t.Fatalf("reserve moved to a model-cooling alternative: %s", got)
	}
}

func TestBackendSeriesUnknownOpaqueOrMalformedOriginNeverGuessesAccount(t *testing.T) {
	selector, auths, _ := backendAPIBackupTestSelector(t)
	for _, opts := range []struct{ session, body string }{
		{"unknown-opaque", `{"model":"claude-test","messages":[{"role":"assistant","content":[{"type":"compaction","content":"summary","signature":"opaque"}]}]}`},
		{"unknown-malformed", `{`},
	} {
		request := backendAPIBackupTestOptions(opts.session, false)
		request.OriginalRequest = []byte(opts.body)
		if picked, err := selector.Pick(t.Context(), "claude", backendAuthSelectionModel, request, auths); err == nil || picked != nil {
			t.Fatalf("unbound continuation guessed an account: session=%s picked=%v error=%v", opts.session, picked, err)
		}
	}
}
