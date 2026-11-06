package clawmaster

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// Retain the same account identity while simulating an OAuth login that did not refresh.
func backendUnavailableFallbackExpired(auth *coreauth.Auth) *coreauth.Auth {
	expired := auth.Clone()
	if expired.Metadata == nil {
		expired.Metadata = make(map[string]any)
	}
	expired.Metadata["access_token"] = "synthetic-expired-oauth-token"
	expired.Metadata["expired"] = "2000-01-01T00:00:00Z"
	return expired
}

func TestBackendUnavailableFallbackCommitsPortableHandoffAcrossRestart(t *testing.T) {
	for _, managerOmitsExpired := range []bool{false, true} {
		t.Run(fmt.Sprintf("manager_omits_expired=%t", managerOmitsExpired), func(t *testing.T) {
			credentials, auths := backendSessionStoreFixture(t)
			selector := backendPersistedSelector(t, credentials, auths, "")
			portable := backendAPIBackupTestOptions(t.Name(), false)
			opaque := backendAPIBackupTestOptions(t.Name(), true)
			requireBackendAPIBackupPick(t, selector, portable, auths, auths[0].ID)

			candidates := []*coreauth.Auth{backendUnavailableFallbackExpired(auths[0]), auths[1]}
			if managerOmitsExpired {
				candidates = candidates[1:]
			}
			if got, err := selector.Pick(t.Context(), "mixed", backendAuthSelectionModel, opaque, candidates); got != nil || err == nil {
				t.Fatalf("account-bound history moved after its login expired: %v, %v", got, err)
			}
			identity, _ := backendSeriesSessionIDs(portable)
			if route, found, err := selector.routes.lookup(identity); err != nil || !found || !route.Committed || route.Revision != 1 || route.Origin != selector.routes.origin(auths[0]) {
				t.Fatalf("opaque refusal changed the persisted origin: %v, %v, %v", route, found, err)
			}

			requireBackendAPIBackupPick(t, selector, portable, candidates, auths[1].ID)
			if route, found, err := selector.routes.lookup(identity); err != nil || !found || !route.Committed || route.Revision != 2 || route.Origin != selector.routes.origin(auths[1]) {
				t.Fatalf("accepted portable handoff was not committed: %v, %v, %v", route, found, err)
			}
			selector.Stop()

			// A repaired original login and new process IDs must not move the conversation back.
			restartedAuths := []*coreauth.Auth{auths[0].Clone(), auths[1].Clone()}
			restartedAuths[0].ID, restartedAuths[1].ID = "repaired-a", "restarted-b"
			restarted := backendPersistedSelector(t, credentials, restartedAuths, "")
			requireBackendAPIBackupPick(t, restarted, opaque, restartedAuths, "restarted-b")
		})
	}
}

func TestBackendUnavailableFallbackDoesNotReplayOrUsePaidBackup(t *testing.T) {
	selector, auths, _ := backendAPIBackupTestSelector(t)
	opts := backendAPIBackupTestOptions(t.Name(), false)
	requireBackendAPIBackupPick(t, selector, opts, auths, "subscription-a")
	ctx := withBackendAttempt(t.Context())
	recordBackendAttempt(ctx, coreauth.Result{
		AuthID: "subscription-a", Model: "claude-test",
		Error: &coreauth.Error{HTTPStatus: http.StatusServiceUnavailable},
	})
	if got, err := selector.Pick(ctx, "mixed", backendAuthSelectionModel, opts, auths[:2]); got != nil || err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatalf("attempt was replayed or lost its original failure: %v, %v", got, err)
	}
	if got, err := selector.Pick(t.Context(), "mixed", backendAuthSelectionModel, opts, auths[:1]); got != nil || err == nil {
		t.Fatalf("unavailable subscriptions caused paid fallback: %v, %v", got, err)
	}
	requireBackendAPIBackupPick(t, selector, opts, auths[:2], "subscription-b")
}

func TestBackendUnavailableFallbackDoesNotReplayUnboundRequestAfterRankChange(t *testing.T) {
	selector, auths, reset := backendAPIBackupTestSelector(t)
	opts := backendAPIBackupTestOptions(t.Name(), false)
	opts.Headers = nil
	ctx := withBackendAttempt(t.Context())
	backendSetCountRequest(ctx) // Counting tokens does not establish a session binding.
	first, err := selector.Pick(ctx, "mixed", backendAuthSelectionModel, opts, auths)
	if err != nil || first == nil || first.ID != "subscription-a" {
		t.Fatalf("initial selection = %v, %v; want subscription-a", first, err)
	}
	// A later quota observation promotes the other eligible subscription while
	// the manager is deciding whether to retry the already attempted request.
	selector.observeQuota("subscription-a", backendWeeklyQuota{known: true, used: .98, resetsAt: reset})
	selector.observeQuota("subscription-b", backendWeeklyQuota{known: true, used: .2, resetsAt: reset})
	recordBackendAttempt(ctx, coreauth.Result{
		AuthID: "subscription-a", Model: "claude-test",
		Error: &coreauth.Error{HTTPStatus: http.StatusServiceUnavailable},
	})
	if got, err := selector.Pick(ctx, "mixed", backendAuthSelectionModel, opts, auths[:2]); got != nil || err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatalf("unbound request was replayed or lost its original failure after the rank changed: %v, %v", got, err)
	}
}

func TestBackendUnavailableFallbackDispatchesThroughManager(t *testing.T) {
	first, second := t.Name()+"-first", t.Name()+"-second"
	model := t.Name() + "-model"
	selector := &backendSeriesSelector{authIDs: []string{first, second}, provider: "claude"}
	t.Cleanup(selector.Stop)
	capture := &backendSeriesCapture{}
	manager := coreauth.NewManager(nil, selector, nil)
	manager.SetRetryConfig(0, 0, 2)
	setBackendSeriesResultPolicy(manager, selector)
	manager.RegisterExecutor(withBackendUpstreamRecording(capture))
	for _, id := range []string{first, second} {
		registry.GetGlobalRegistry().RegisterClient(id, "claude", []*registry.ModelInfo{{ID: backendAuthSelectionModel}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
		if _, err := manager.Register(t.Context(), backendSeriesTestAuth(id, "claude")); err != nil {
			t.Fatal(err)
		}
	}
	handler := newBackendHandler(t.Context(), BackendOptions{Provider: "claude", UseRequestModel: true}, handlers.NewBaseAPIHandlers(&config.SDKConfig{}, manager))
	send := func(session string) {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hello"}]}`, model)))
		req.Header.Set("X-Claude-Code-Session-Id", session)
		writer := httptest.NewRecorder()
		handler.ServeHTTP(writer, req)
		if writer.Code != http.StatusOK {
			t.Fatalf("session %q response = %d: %s", session, writer.Code, writer.Body.String())
		}
	}
	send("existing")
	original, ok := manager.GetByID(first)
	if !ok {
		t.Fatal("original subscription was not registered")
	}
	if _, err := manager.Update(t.Context(), backendUnavailableFallbackExpired(original)); err != nil {
		t.Fatal(err)
	}
	send("existing")
	send("fresh")
	capture.mu.Lock()
	defer capture.mu.Unlock()
	if got, want := strings.Join(capture.calls, ","), first+","+second+","+second; got != want {
		t.Fatalf("upstream dispatch = %s, want %s", got, want)
	}
}
