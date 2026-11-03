package cliproxy

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/pluginhost"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func TestAntigravityStalledAccountDoesNotBlockOtherRetries(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		resetAntigravityCapabilityCache()
		defer resetAntigravityCapabilityCache()
		svc := &Service{cfg: &config.Config{}, coreManager: coreauth.NewManager(nil, nil, nil)}
		for _, id := range []string{"refresh-stall", "refresh-retry"} {
			auth, errRegister := svc.coreManager.Register(t.Context(), antigravityTestAuth(id, "https://refresh.invalid"))
			if errRegister != nil {
				t.Fatal(errRegister)
			}
			defer GlobalModelRegistry().UnregisterClient(auth.ID)
		}
		var calls, stalledCalls atomic.Int32
		transport := http.DefaultTransport
		http.DefaultTransport = antigravityReviewTransport(func(r *http.Request) (*http.Response, error) {
			if strings.Contains(r.Header.Get("Authorization"), "refresh-stall") {
				stalledCalls.Add(1)
				<-r.Context().Done()
				return nil, r.Context().Err()
			}
			calls.Add(1)
			return &http.Response{StatusCode: http.StatusServiceUnavailable, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
		})
		defer func() { http.DefaultTransport = transport }()
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan struct{})
		go func() { defer close(done); svc.runAntigravityModelRefresh(ctx) }()
		// Synthetic time: every possible first retry deadline is within this window.
		time.Sleep(10 * time.Minute)
		synctest.Wait()
		attempts, stalledAttempts := calls.Load(), stalledCalls.Load()
		svc.antigravityProbeMu.Lock()
		pending := len(svc.antigravityRefreshPending)
		svc.antigravityProbeMu.Unlock()
		cancel()
		<-done
		svc.WaitAntigravityProbes()
		synctest.Wait()
		if attempts < 2 {
			t.Fatalf("other account never retried: calls=%d", attempts)
		}
		if stalledAttempts != 1 || pending != 1 {
			t.Fatalf("stalled account calls=%d pending=%d, want one task", stalledAttempts, pending)
		}
	})
}

func TestAntigravityPluginRefreshDoesNotWaitForProbe(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		resetAntigravityCapabilityCache()
		defer resetAntigravityCapabilityCache()
		svc := &Service{cfg: &config.Config{}, coreManager: coreauth.NewManager(nil, nil, nil), pluginHost: pluginhost.New()}
		auth, errRegister := svc.coreManager.Register(t.Context(), antigravityTestAuth("plugin-refresh-stall", "https://refresh.invalid"))
		if errRegister != nil {
			t.Fatal(errRegister)
		}
		defer GlobalModelRegistry().UnregisterClient(auth.ID)
		started := make(chan struct{})
		transport := http.DefaultTransport
		http.DefaultTransport = antigravityReviewTransport(func(r *http.Request) (*http.Response, error) {
			close(started)
			<-r.Context().Done()
			return nil, r.Context().Err()
		})
		defer func() { http.DefaultTransport = transport }()
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan struct{})
		go func() { defer close(done); svc.refreshPluginModelRegistrations(ctx) }()
		<-started
		synctest.Wait()
		completed := false
		select {
		case <-done:
			completed = true
		default:
		}
		cancel()
		<-done
		svc.WaitAntigravityProbes()
		synctest.Wait()
		if !completed {
			t.Fatal("plugin refresh waits for an unfinished background probe")
		}
	})
}

func TestAntigravityRefreshBoundsQueuedWorkAndCancels(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		resetAntigravityCapabilityCache()
		defer resetAntigravityCapabilityCache()
		// Blocked semaphore operations must belong to this synthetic-time bubble.
		slots := antigravityProbeSlots
		antigravityProbeSlots = make(chan struct{}, cap(slots))
		defer func() { antigravityProbeSlots = slots }()
		svc := &Service{cfg: &config.Config{}, coreManager: coreauth.NewManager(nil, nil, nil), pluginHost: pluginhost.New()}
		for range cap(antigravityProbeSlots) {
			antigravityProbeSlots <- struct{}{}
		}
		const accounts = modelRegistrationMaxWorkersPerCategory + 3
		for i := range accounts {
			auth, errRegister := svc.coreManager.Register(t.Context(), antigravityTestAuth(fmt.Sprintf("queued-refresh-%d", i), "https://refresh.invalid"))
			if errRegister != nil {
				t.Fatal(errRegister)
			}
			defer GlobalModelRegistry().UnregisterClient(auth.ID)
		}
		var calls atomic.Int32
		transport := http.DefaultTransport
		http.DefaultTransport = antigravityReviewTransport(func(r *http.Request) (*http.Response, error) {
			calls.Add(1)
			<-r.Context().Done()
			return nil, r.Context().Err()
		})
		defer func() { http.DefaultTransport = transport }()
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan struct{})
		go func() { defer close(done); svc.runAntigravityModelRefresh(ctx) }()
		time.Sleep(2 * time.Minute) // Synthetic time: native probes are waiting for admission.
		synctest.Wait()
		beforeAdmission := calls.Load()
		for range cap(antigravityProbeSlots) {
			<-antigravityProbeSlots
		}
		time.Sleep(8 * time.Minute) // Repeated scans must not accumulate followers.
		synctest.Wait()
		connected := calls.Load()
		svc.antigravityProbeMu.Lock()
		pending := len(svc.antigravityRefreshPending)
		svc.antigravityProbeMu.Unlock()
		cancel()
		<-done
		svc.WaitAntigravityProbes()
		synctest.Wait()
		svc.antigravityProbeMu.Lock()
		remaining := len(svc.antigravityRefreshPending) + len(svc.antigravityAccountProbes)
		svc.antigravityProbeMu.Unlock()
		if connected != modelRegistrationMaxWorkersPerCategory || pending != accounts {
			t.Fatalf("connected=%d pending=%d, want %d and %d", connected, pending, modelRegistrationMaxWorkersPerCategory, accounts)
		}
		if beforeAdmission != 0 {
			t.Fatal("native network admission was bypassed")
		}
		if remaining != 0 || len(antigravityProbeSlots) != 0 {
			t.Fatal("canceled refresh retained tasks or network slots")
		}
	})
}

func TestAntigravityShutdownCancelsServiceProbes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		resetAntigravityCapabilityCache()
		defer resetAntigravityCapabilityCache()
		lifetime, cancel := context.WithCancel(t.Context())
		defer cancel()
		svc := &Service{cfg: &config.Config{}, coreManager: coreauth.NewManager(nil, nil, nil), antigravityContext: lifetime, runCancel: cancel}
		auth, errRegister := svc.coreManager.Register(t.Context(), antigravityTestAuth("shutdown-probe", "https://refresh.invalid"))
		if errRegister != nil {
			t.Fatal(errRegister)
		}
		defer GlobalModelRegistry().UnregisterClient(auth.ID)
		started := make(chan struct{})
		transport := http.DefaultTransport
		http.DefaultTransport = antigravityReviewTransport(func(r *http.Request) (*http.Response, error) {
			close(started)
			<-r.Context().Done()
			return nil, r.Context().Err()
		})
		defer func() { http.DefaultTransport = transport }()
		svc.registerModelsForAuth(t.Context(), auth)
		<-started
		errShutdown := svc.Shutdown(t.Context())
		cancelled := lifetime.Err() != nil
		cancel()
		svc.WaitAntigravityProbes()
		synctest.Wait()
		if errShutdown != nil {
			t.Fatal(errShutdown)
		}
		if !cancelled {
			t.Fatal("shutdown did not cancel the service probe lifetime")
		}
	})
}
