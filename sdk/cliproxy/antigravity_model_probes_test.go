package cliproxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func TestAntigravityAccountChangesCancelConnectedProbe(t *testing.T) {
	for _, change := range []string{"disabled", "route", "token", "config-proxy", "native-refresh", "native-prepare", "native-update", "native-remove"} {
		t.Run(change, func(t *testing.T) {
			resetAntigravityCapabilityCache()
			t.Cleanup(resetAntigravityCapabilityCache)
			started, cancelled := make(chan struct{}), make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(200)
				w.(http.Flusher).Flush()
				close(started)
				<-r.Context().Done()
				close(cancelled)
			}))
			ctx, cancel := context.WithCancel(t.Context())
			svc := &Service{cfg: &config.Config{}, coreManager: coreauth.NewManager(nil, nil, nil)}
			t.Cleanup(func() { cancel(); svc.WaitAntigravityProbes(); server.Close() })
			auth, _ := svc.coreManager.Register(ctx, antigravityTestAuth("cancel-"+change, server.URL))
			t.Cleanup(func() { GlobalModelRegistry().UnregisterClient(auth.ID) })
			svc.registerModelsForAuth(ctx, auth)
			<-started
			updated := auth.Clone()
			switch change {
			case "disabled":
				updated.Disabled = true
			case "route":
				updated.Attributes["base_url"] = "https://new-route.invalid"
			case "token", "native-refresh", "native-prepare", "native-update":
				updated.Metadata["access_token"] = "new-token"
			}
			if change == "config-proxy" {
				next := &config.Config{}
				next.ProxyURL = "direct"
				svc.commitConfigUpdate(next)
			} else {
				switch change {
				case "native-refresh":
					_, _ = svc.coreManager.UpdateRefreshedAuth(ctx, auth, updated)
				case "native-prepare":
					_, _ = svc.coreManager.UpdatePreparedAuth(ctx, auth, updated)
				case "native-update":
					_, _ = svc.coreManager.Update(ctx, updated)
				case "native-remove":
					svc.coreManager.Remove(ctx, auth.ID)
				default:
					svc.prepareCoreAuthForModelRegistration(ctx, updated)
				}
			}
			select {
			case <-cancelled:
			case <-time.After(time.Second):
				t.Fatal("obsolete connected probe was not canceled")
			}
			svc.WaitAntigravityProbes()
			antigravityCapabilityMu.RLock()
			failures := len(antigravityAuthFailureCache)
			antigravityCapabilityMu.RUnlock()
			if failures != 0 {
				t.Fatal("account invalidation counted as upstream failure")
			}
			svc.antigravityProbeMu.Lock()
			active := len(svc.antigravityAccountProbes)
			svc.antigravityProbeMu.Unlock()
			if active != 0 {
				t.Fatal("completed account probes retained")
			}
		})
	}
}

func TestAntigravityDeletedAccountCannotStartQueuedProbe(t *testing.T) {
	svc := &Service{cfg: &config.Config{}, coreManager: coreauth.NewManager(nil, nil, nil)}
	auth, _ := svc.coreManager.Register(t.Context(), antigravityTestAuth("queued-removed", "http://127.0.0.1:1"))
	key := svc.antigravityCapabilityKey(auth)
	svc.applyCoreAuthRemoval(t.Context(), auth.ID)
	ctx, finish, active := svc.beginAntigravityAccountProbe(t.Context(), auth, key, "flight", "token-queued-removed")
	defer finish()
	if active || ctx.Err() == nil {
		t.Fatal("removed account started a late queued probe")
	}
}

type failingAntigravityRefreshExecutor struct {
	err error
}

func (e *failingAntigravityRefreshExecutor) Identifier() string { return "antigravity" }

func (e *failingAntigravityRefreshExecutor) Refresh(context.Context, *coreauth.Auth) (*coreauth.Auth, error) {
	return nil, e.err
}

func (e *failingAntigravityRefreshExecutor) Execute(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *failingAntigravityRefreshExecutor) ExecuteStream(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, nil
}

func (e *failingAntigravityRefreshExecutor) CountTokens(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *failingAntigravityRefreshExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func TestAntigravityTerminalRefreshFailureCancelsAndBlocksProbe(t *testing.T) {
	resetAntigravityCapabilityCache()
	t.Cleanup(resetAntigravityCapabilityCache)

	// Keep the probe clock before the access token expiry so the probe connects,
	// while ForceRefreshAuth sees an expired access token at wall-clock time.Now().
	wallNow := time.Now().UTC()
	antigravityNowFunc = func() time.Time { return wallNow.Add(-2 * time.Hour) }
	t.Cleanup(func() { antigravityNowFunc = time.Now })

	started, cancelled := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		close(started)
		<-r.Context().Done()
		close(cancelled)
	}))
	ctx, cancel := context.WithCancel(t.Context())
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(&failingAntigravityRefreshExecutor{
		err: &coreauth.Error{
			Code:       "token_revoked",
			Message:    "token_revoked",
			HTTPStatus: http.StatusUnauthorized,
		},
	})
	svc := &Service{cfg: &config.Config{}, coreManager: manager}
	t.Cleanup(func() { cancel(); svc.WaitAntigravityProbes(); server.Close() })

	auth := antigravityTestAuth("terminal-refresh-cancel", server.URL)
	auth.Metadata["refresh_token"] = "dead-rt"
	auth.Metadata["expired"] = wallNow.Add(-time.Hour).Format(time.RFC3339)
	registered, errRegister := manager.Register(ctx, auth)
	if errRegister != nil {
		t.Fatal(errRegister)
	}
	t.Cleanup(func() { GlobalModelRegistry().UnregisterClient(registered.ID) })

	svc.registerModelsForAuth(ctx, registered)
	<-started

	if _, errRefresh := manager.ForceRefreshAuth(ctx, registered.ID); errRefresh == nil {
		t.Fatal("expected terminal refresh failure, got nil")
	}
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("connected probe was not canceled after terminal refresh failure")
	}
	svc.WaitAntigravityProbes()

	// Late queued probe using the old snapshot must also be rejected at admission.
	key := svc.antigravityCapabilityKey(registered)
	probeCtx, finish, active := svc.beginAntigravityAccountProbe(ctx, registered, key, "flight", "token-terminal-refresh-cancel")
	defer finish()
	if active || probeCtx.Err() == nil {
		t.Fatal("terminally unauthenticated account started a late queued probe")
	}
}
