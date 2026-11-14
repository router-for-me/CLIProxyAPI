package cliproxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
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
