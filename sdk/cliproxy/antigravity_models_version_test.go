package cliproxy

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

const (
	antigravityOldTestUA = "antigravity/hub/2.23.0 darwin/arm64"
	antigravityNewTestUA = "antigravity/hub/2.24.0 darwin/arm64"
)

func overrideAntigravityModelUserAgent(t *testing.T) *atomic.Value {
	t.Helper()
	var current atomic.Value
	current.Store(antigravityOldTestUA)
	previous := antigravityModelUserAgent
	antigravityModelUserAgent = func() string { return current.Load().(string) }
	t.Cleanup(func() { antigravityModelUserAgent = previous })
	return &current
}

func antigravityVersionTestResponse(req *http.Request, status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}
}

func TestAntigravityCapabilitiesRefreshOnClientVersionChange(t *testing.T) {
	for _, initialStatus := range []int{http.StatusOK, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(initialStatus), func(t *testing.T) {
			resetAntigravityCapabilityCache()
			t.Cleanup(resetAntigravityCapabilityCache)
			currentUA := overrideAntigravityModelUserAgent(t)
			var calls atomic.Int32
			previous := http.DefaultTransport
			http.DefaultTransport = antigravityReviewTransport(func(req *http.Request) (*http.Response, error) {
				calls.Add(1)
				if req.Header.Get("User-Agent") == antigravityOldTestUA {
					return antigravityVersionTestResponse(req, initialStatus, `{"models":{"old":{}}}`), nil
				}
				return antigravityVersionTestResponse(req, http.StatusOK, `{"models":{"old":{},"new":{}}}`), nil
			})
			t.Cleanup(func() { http.DefaultTransport = previous })
			svc := &Service{cfg: &config.Config{}}
			auth := antigravityTestAuth("version-change", "https://version.invalid")
			svc.fetchAntigravityModelCapabilityHintsForAuth(t.Context(), auth)
			svc.fetchAntigravityModelCapabilityHintsForAuth(t.Context(), auth)
			if calls.Load() != 1 {
				t.Fatalf("same version did not reuse its cache/backoff: requests=%d", calls.Load())
			}
			oldKey := svc.antigravityCapabilityKey(auth)
			currentUA.Store(antigravityNewTestUA)
			hints := svc.fetchAntigravityModelCapabilityHintsForAuth(t.Context(), auth)
			if oldKey == svc.antigravityCapabilityKey(auth) {
				t.Error("client version change did not change the capability key")
			}
			if _, ok := hints.ModelIDs["new"]; !ok || calls.Load() != 2 {
				t.Fatalf("new version reused old cache/backoff: models=%v requests=%d", hints.ModelIDs, calls.Load())
			}
		})
	}
}

func TestAntigravityVersionChangeKeepsPersistedHintsAsStaleFallback(t *testing.T) {
	resetAntigravityCapabilityCache()
	t.Cleanup(resetAntigravityCapabilityCache)
	currentUA := overrideAntigravityModelUserAgent(t)
	svc := &Service{cfg: &config.Config{}}
	auth := antigravityTestAuth("version-persisted", "https://version.invalid")
	auth.Metadata[antigravityPersistedHintsMetaKey] = buildPersistedAntigravityCapabilityHintsValue(
		"project-version-persisted", "https://version.invalid",
		antigravityModelCapabilityHints{ModelIDs: map[string]struct{}{"old": {}}},
	)
	var calls atomic.Int32
	previous := http.DefaultTransport
	http.DefaultTransport = antigravityReviewTransport(func(req *http.Request) (*http.Response, error) {
		calls.Add(1)
		status := http.StatusOK
		if req.Header.Get("User-Agent") == antigravityNewTestUA {
			status = http.StatusServiceUnavailable
		}
		return antigravityVersionTestResponse(req, status, `{"models":{"old":{}}}`), nil
	})
	t.Cleanup(func() { http.DefaultTransport = previous })
	svc.fetchAntigravityModelCapabilityHintsForAuth(t.Context(), auth)
	currentUA.Store(antigravityNewTestUA)
	hints := svc.fetchAntigravityModelCapabilityHintsForAuth(t.Context(), auth)
	if _, ok := hints.ModelIDs["old"]; !ok {
		t.Fatal("failed version refresh discarded the persisted fallback")
	}
	if calls.Load() != 2 {
		t.Fatalf("persisted hints suppressed the new version probe: requests=%d", calls.Load())
	}
	antigravityCapabilityMu.RLock()
	entry := antigravityCapabilityCache[svc.antigravityCapabilityKey(auth)]
	antigravityCapabilityMu.RUnlock()
	if !entry.expiresAt.IsZero() {
		t.Fatal("old persisted hints were marked fresh for the new version")
	}
}

func TestAntigravityProbeKeepsVersionSnapshotWhileWaitingForSlot(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		resetAntigravityCapabilityCache()
		defer resetAntigravityCapabilityCache()
		currentUA := overrideAntigravityModelUserAgent(t)
		previousSlots := antigravityProbeSlots
		antigravityProbeSlots = make(chan struct{}, 1)
		antigravityProbeSlots <- struct{}{}
		defer func() { antigravityProbeSlots = previousSlots }()
		previousTransport := http.DefaultTransport
		var requestUA string
		http.DefaultTransport = antigravityReviewTransport(func(req *http.Request) (*http.Response, error) {
			requestUA = req.Header.Get("User-Agent")
			return antigravityVersionTestResponse(req, http.StatusOK, `{"models":{"gemini-3-flash":{}}}`), nil
		})
		defer func() { http.DefaultTransport = previousTransport }()
		svc := &Service{cfg: &config.Config{}, coreManager: coreauth.NewManager(nil, nil, nil)}
		auth, errRegister := svc.coreManager.Register(t.Context(), antigravityTestAuth("version-queued", "https://version.invalid"))
		if errRegister != nil {
			t.Fatal(errRegister)
		}
		defer GlobalModelRegistry().UnregisterClient(auth.ID)
		epoch := GlobalModelRegistry().ClientRegistrationEpoch(auth.ID)
		oldKey := svc.antigravityCapabilityKey(auth)
		done := make(chan struct{})
		var resultKey string
		go func() {
			defer close(done)
			resultKey, _ = svc.probeAndRegisterAntigravityModels(t.Context(), auth, "antigravity", auth.RegistrationEpoch, epoch)
		}()
		synctest.Wait()
		currentUA.Store(antigravityNewTestUA)
		<-antigravityProbeSlots
		<-done
		synctest.Wait()
		if resultKey != oldKey || requestUA != antigravityOldTestUA {
			t.Fatalf("cache/request version snapshots diverged: key=%q UA=%q", resultKey, requestUA)
		}
		if len(GlobalModelRegistry().GetModelsForClient(auth.ID)) != 0 {
			t.Fatal("obsolete version probe published its catalog")
		}
		if svc.cachedAntigravityHints(auth).ModelIDs != nil {
			t.Fatal("new version inherited the obsolete in-flight response")
		}
	})
}

func TestAntigravityVersionChangeUnblocksAccountRefresh(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		resetAntigravityCapabilityCache()
		defer resetAntigravityCapabilityCache()
		currentUA := overrideAntigravityModelUserAgent(t)
		previous := http.DefaultTransport
		var oldCancelled atomic.Bool
		var calls atomic.Int32
		http.DefaultTransport = antigravityReviewTransport(func(req *http.Request) (*http.Response, error) {
			calls.Add(1)
			if req.Header.Get("User-Agent") == antigravityOldTestUA {
				<-req.Context().Done()
				oldCancelled.Store(true)
				return nil, req.Context().Err()
			}
			return antigravityVersionTestResponse(req, http.StatusOK, `{"models":{"gemini-3-flash":{}}}`), nil
		})
		defer func() { http.DefaultTransport = previous }()
		svc := &Service{cfg: &config.Config{}, coreManager: coreauth.NewManager(nil, nil, nil)}
		auth, errRegister := svc.coreManager.Register(t.Context(), antigravityTestAuth("version-stalled", "https://version.invalid"))
		if errRegister != nil {
			t.Fatal(errRegister)
		}
		defer GlobalModelRegistry().UnregisterClient(auth.ID)
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan struct{})
		go func() { defer close(done); svc.runAntigravityModelRefresh(ctx) }()
		defer func() {
			cancel()
			<-done
			svc.WaitAntigravityProbes()
			synctest.Wait()
		}()
		svc.scheduleAntigravityModelRefresh(ctx, auth)
		synctest.Wait()
		if calls.Load() != 1 {
			t.Fatalf("initial probe requests=%d", calls.Load())
		}
		currentUA.Store(antigravityNewTestUA)
		// Only the synctest clock advances; no wall-clock wait is needed.
		time.Sleep(antigravityRefreshScanInterval)
		synctest.Wait()
		if !oldCancelled.Load() || calls.Load() != 2 {
			t.Fatalf("obsolete probe blocked its successor: cancelled=%v requests=%d", oldCancelled.Load(), calls.Load())
		}
		if !GlobalModelRegistry().ClientSupportsModel(auth.ID, "gemini-3-flash") {
			t.Fatal("periodic scan did not publish the new version catalog")
		}
	})
}
