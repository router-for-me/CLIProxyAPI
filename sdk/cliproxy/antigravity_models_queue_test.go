package cliproxy

import (
	"context"
	"io"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/pluginhost"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func TestAntigravityRefreshSkipsPluginOwnedModels(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		resetAntigravityCapabilityCache()
		defer resetAntigravityCapabilityCache()
		svc := &Service{cfg: &config.Config{}, coreManager: coreauth.NewManager(nil, nil, nil), pluginHost: pluginhost.New()}
		auth, errRegister := svc.coreManager.Register(t.Context(), antigravityTestAuth("plugin-owned-refresh", "https://unused.invalid"))
		if errRegister != nil {
			t.Fatal(errRegister)
		}
		reg := GlobalModelRegistry()
		defer reg.UnregisterClient(auth.ID)
		reg.RegisterClient(auth.ID, "antigravity", []*ModelInfo{{ID: "plugin-model"}})
		var owned atomic.Bool
		owned.Store(true)
		var lookups atomic.Int32
		oldOwnership := pluginHostHasAuthModelProvider
		pluginHostHasAuthModelProvider = func(host *pluginhost.Host, provider string) bool {
			lookups.Add(1)
			return host == svc.pluginHost && provider == "antigravity" && owned.Load()
		}
		defer func() { pluginHostHasAuthModelProvider = oldOwnership }()
		var calls atomic.Int32
		transport := http.DefaultTransport
		http.DefaultTransport = antigravityReviewTransport(func(req *http.Request) (*http.Response, error) {
			calls.Add(1)
			return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"models":{"gemini-3.1-flash-lite":{}}}`)), Request: req}, nil
		})
		defer func() { http.DefaultTransport = transport }()
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan struct{})
		go func() { defer close(done); svc.runAntigravityModelRefresh(ctx) }()
		svc.asyncProbeAntigravityCapabilities(ctx, auth, "antigravity")
		time.Sleep(10 * time.Minute) // Synthetic time: exercise multiple native scans.
		synctest.Wait()
		cancel()
		<-done
		svc.WaitAntigravityProbes()
		synctest.Wait()
		if lookups.Load() == 0 || calls.Load() != 0 || !reg.ClientSupportsModel(auth.ID, "plugin-model") {
			t.Fatal("native refresh ignored plugin model ownership")
		}
		// Plugin removal must immediately make native discovery eligible again.
		owned.Store(false)
		svc.completeModelRegistrationForAuth(t.Context(), auth)
		svc.WaitAntigravityProbes()
		if calls.Load() != 1 || !reg.ClientSupportsModel(auth.ID, "gemini-3.1-flash-lite") {
			t.Fatal("removed plugin continued to suppress native discovery")
		}
	})
}

func TestAntigravityRegistrationCoalescesLatestPublication(t *testing.T) {
	for _, name := range []string{"publish-latest", "concurrent-registration", "external-unregister"} {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				resetAntigravityCapabilityCache()
				defer resetAntigravityCapabilityCache()
				svc := &Service{cfg: &config.Config{}, coreManager: coreauth.NewManager(nil, nil, nil)}
				auth, errRegister := svc.coreManager.Register(t.Context(), antigravityTestAuth("coalesced-"+name, "https://stalled.invalid"))
				if errRegister != nil {
					t.Fatal(errRegister)
				}
				reg := GlobalModelRegistry()
				defer reg.UnregisterClient(auth.ID)
				var calls atomic.Int32
				release := make(chan struct{})
				transport := http.DefaultTransport
				http.DefaultTransport = antigravityReviewTransport(func(req *http.Request) (*http.Response, error) {
					calls.Add(1)
					select {
					case <-release:
						return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"models":{"gemini-3.1-flash-lite":{}}}`)), Request: req}, nil
					case <-req.Context().Done():
						return nil, req.Context().Err()
					}
				})
				defer func() { http.DefaultTransport = transport }()
				ctx, cancel := context.WithCancel(t.Context())
				defer func() { cancel(); svc.WaitAntigravityProbes(); synctest.Wait() }()
				svc.completeModelRegistrationForAuth(ctx, auth)
				synctest.Wait()
				before := runtime.NumGoroutine()
				register := func() {
					svc.completeModelRegistrationForAuth(ctx, auth)
					svc.refreshAntigravityModels(ctx)
				}
				var registrations sync.WaitGroup
				for range 100 {
					if name == "concurrent-registration" {
						registrations.Go(register)
					} else {
						register()
					}
				}
				registrations.Wait()
				synctest.Wait()
				growth := runtime.NumGoroutine() - before
				svc.antigravityProbeMu.Lock()
				pending := len(svc.antigravityRefreshPending)
				latest := svc.antigravityRefreshPending[auth.ID]
				svc.antigravityProbeMu.Unlock()
				if growth > 4 || calls.Load() != 1 || pending != 1 {
					t.Fatalf("unbounded work: goroutine growth=%d HTTP=%d workers=%d", growth, calls.Load(), pending)
				}
				if latest == nil || latest.registryEpoch != reg.ClientRegistrationEpoch(auth.ID) {
					t.Fatal("latest queued publication snapshot was not retained")
				}
				if name == "external-unregister" {
					reg.UnregisterClient(auth.ID)
				}
				close(release)
				svc.WaitAntigravityProbes()
				synctest.Wait()
				wantRegistered := name != "external-unregister"
				if reg.ClientSupportsModel(auth.ID, "gemini-3.1-flash-lite") != wantRegistered || calls.Load() != 1 {
					t.Fatal("queued publication lost the latest snapshot or adopted an external registry epoch")
				}
				svc.antigravityProbeMu.Lock()
				remaining := len(svc.antigravityRefreshPending) + len(svc.antigravityAccountProbes)
				svc.antigravityProbeMu.Unlock()
				if remaining != 0 {
					t.Fatal("completed coalesced refresh retained pending work")
				}
			})
		})
	}
}

func TestAntigravityQueuedAuthUpdateRejectsLateStaleCaller(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		resetAntigravityCapabilityCache()
		defer resetAntigravityCapabilityCache()
		svc := &Service{cfg: &config.Config{SDKConfig: config.SDKConfig{ForceModelPrefix: true}}, coreManager: coreauth.NewManager(nil, nil, nil)}
		auth, errRegister := svc.coreManager.Register(t.Context(), antigravityTestAuth("coalesced-auth-update", "https://stalled.invalid"))
		if errRegister != nil {
			t.Fatal(errRegister)
		}
		defer GlobalModelRegistry().UnregisterClient(auth.ID)
		var oldCalls, newCalls atomic.Int32
		transport := http.DefaultTransport
		http.DefaultTransport = antigravityReviewTransport(func(req *http.Request) (*http.Response, error) {
			if req.Header.Get("Authorization") != "Bearer updated-token" {
				oldCalls.Add(1)
				<-req.Context().Done()
				return nil, req.Context().Err()
			}
			newCalls.Add(1)
			return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"models":{"gemini-3.1-flash-lite":{}}}`)), Request: req}, nil
		})
		defer func() { http.DefaultTransport = transport }()
		ctx, cancel := context.WithCancel(t.Context())
		defer func() { cancel(); svc.WaitAntigravityProbes(); synctest.Wait() }()
		svc.completeModelRegistrationForAuth(ctx, auth)
		synctest.Wait()
		updated := auth.Clone()
		updated.Prefix = "latest"
		updated.Metadata["access_token"] = "updated-token"
		updated, errUpdate := svc.coreManager.Update(ctx, updated)
		if errUpdate != nil {
			t.Fatal(errUpdate)
		}
		svc.completeModelRegistrationForAuth(ctx, updated)
		svc.asyncProbeAntigravityCapabilities(ctx, auth, "antigravity")
		svc.WaitAntigravityProbes()
		synctest.Wait()
		if oldCalls.Load() != 1 || newCalls.Load() != 1 || !GlobalModelRegistry().ClientSupportsModel(auth.ID, "latest/gemini-3.1-flash-lite") {
			t.Fatal("stale caller replaced the updated queued auth snapshot")
		}
	})
}
