package cliproxy

import (
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	sdkauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestAntigravityUsageSummaryWindows(t *testing.T) {
	resetAntigravityCapabilityCache()
	t.Cleanup(resetAntigravityCapabilityCache)
	now := time.Date(2026, 11, 22, 18, 0, 0, 0, time.UTC)
	oldClock := antigravityNowFunc
	antigravityNowFunc = func() time.Time { return now }
	t.Cleanup(func() { antigravityNowFunc = oldClock })
	var mu sync.Mutex
	var paths []string
	probePaths := func() []string {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(paths)
	}
	summary := `{"groups":[{"displayName":"Gemini group","buckets":[
		{"bucketId":"gemini-5h","window":"5h","remainingFraction":1,"resetTime":"2026-11-22T23:01:00Z"},
		{"bucketId":"gemini-weekly","window":"weekly","remainingFraction":1,"resetTime":"2026-11-29T18:00:00Z"},
		{"bucketId":"gemini-argon-5h","window":"5h","remainingFraction":1,"description":"started","resetTime":"2026-11-22T23:00:00Z"},
		{"bucketId":"gemini-flash-5h","window":"5h","remainingFraction":1,"resetTime":"2026-11-22T22:58:59Z"},
		{"bucketId":"3p-weekly","window":"weekly","resetTime":"2026-11-29T18:00:00Z"},
		{"bucketId":"custom-weekly","window":"weekly","remainingFraction":-0.1}
	]},{"buckets":[
		{"bucketId":"gemini-carbon-5h","window":"5h","remainingFraction":0.8,"resetTime":"2026-11-22T23:00:00Z"},
		{"bucketId":"gemini-argon-weekly","window":"weekly","remainingFraction":0.2},
		{"bucketId":"custom-5h","window":"5h","remainingFraction":1e-20}
	]}]}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		response := summary
		mu.Unlock()
		switch r.URL.Path {
		case sdkauth.AntigravityQuotaSummaryPath:
			_, _ = io.WriteString(w, response)
		case antigravityModelsPath:
			_, _ = io.WriteString(w, `{"models":{"gemini-pro":{"quotaInfo":{"remainingFraction":0.2},"weeklyQuotaInfo":{"resetTime":"2026-11-29T18:00:00Z"}}}}`)
		default:
			t.Errorf("unexpected probe path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	manager := coreauth.NewManager(nil, nil, nil)
	svc := &Service{coreManager: manager}
	auth, err := manager.Register(t.Context(), antigravityTestAuth("summary", server.URL))
	if err != nil {
		t.Fatal(err)
	}
	lookup := svc.NewAntigravityUsageLookup()
	if err := lookup.Refresh(t.Context(), auth); err != nil {
		t.Fatal(err)
	}
	current, _ := manager.GetByID(auth.ID)
	windows := coreauth.QuotaWindows(current, now)
	if paths := probePaths(); !slices.Equal(paths, []string{sdkauth.AntigravityQuotaSummaryPath}) || len(windows) != 9 ||
		windows[0].Label != "5-hour" || windows[1].Label != "Weekly" {
		t.Fatalf("summary did not supply the leading windows, paths=%v windows=%+v", paths, windows)
	}
	byLabel := make(map[string]coreauth.QuotaWindow)
	for _, window := range windows {
		byLabel[window.Label] = window
	}
	for _, tc := range []struct {
		label, status, resetTime string
		seconds                  int
		scoped                   bool
		used                     float64
	}{
		{"5-hour", "allowed", "", 18000, false, 0},
		{"Weekly", "allowed", "2026-11-29T18:00:00Z", 604800, false, 0},
		{"Gemini group · 5-hour", "allowed", "2026-11-22T23:00:00Z", 18000, true, 0},
		{"Gemini · 5-hour", "allowed", "2026-11-22T22:58:59Z", 18000, true, 0},
		{"Claude & GPT · Weekly", "rejected", "2026-11-29T18:00:00Z", 604800, true, 100},
		{"Gemini group · Weekly", "rejected", "", 604800, true, 100},
		{"Gemini Carbon · 5-hour", "allowed", "2026-11-22T23:00:00Z", 18000, true, 20},
		{"Gemini Argon · Weekly", "allowed", "", 604800, true, 80},
		{"custom-5h · 5-hour", "allowed", "", 18000, true, math.Nextafter(100, 0)},
	} {
		wantReset, _ := time.Parse(time.RFC3339, tc.resetTime)
		cadence := coreauth.ResetCadenceOnUse
		if tc.seconds == 604800 {
			cadence = coreauth.ResetCadenceFixed
		}
		window := byLabel[tc.label]
		if window.Status != tc.status || window.Duration != time.Duration(tc.seconds)*time.Second ||
			window.Scoped != tc.scoped || window.Cadence != cadence || !window.ResetsAt.Equal(wantReset) ||
			window.UsedPercent == nil || math.Abs(*window.UsedPercent-tc.used) > 1e-10 ||
			(window.Status == "allowed" && *window.UsedPercent >= 100) {
			t.Errorf("window %q = %+v, want %+v", tc.label, window, tc)
		}
	}

	now = now.Add(time.Second)
	hints := svc.fetchAntigravityModelCapabilityHintsForAuth(t.Context(), auth)
	afterCatalog, _ := manager.GetByID(auth.ID)
	if _, ok := hints.ModelIDs["gemini-pro"]; !ok || !reflect.DeepEqual(current.Quota, afterCatalog.Quota) {
		t.Fatalf("catalog discovery failed or replaced summary: hints=%+v quota=%+v", hints, afterCatalog.Quota)
	}

	now = now.Add(time.Second)
	mu.Lock()
	summary = `{"groups":[{"buckets":[{"bucketId":"gemini-5h","window":"5h","disabled":true,"remainingFraction":0}]}]}`
	mu.Unlock()
	if err := lookup.Refresh(t.Context(), auth); err != nil {
		t.Fatal(err)
	}
	fallback, _ := manager.GetByID(auth.ID)
	windows = coreauth.QuotaWindows(fallback, now)
	wantPaths := []string{sdkauth.AntigravityQuotaSummaryPath, antigravityModelsPath, sdkauth.AntigravityQuotaSummaryPath, antigravityModelsPath}
	if paths := probePaths(); !slices.Equal(paths, wantPaths) || !fallback.Quota.LookedUpAt.Equal(now) || len(windows) != 2 {
		t.Fatalf("expected fresh fallback snapshot, paths=%v quota=%+v", paths, fallback.Quota)
	}
	if !windows[0].Scoped || !windows[1].Scoped ||
		windows[0].Key != "ag:gemini-pro" || windows[0].Label != "gemini-pro" ||
		windows[1].Key != "weekly:ag:gemini-pro" || windows[1].Label != "gemini-pro · Weekly" ||
		windows[0].Duration != 0 || windows[1].Duration != 604800*time.Second ||
		windows[1].Status != coreauth.QuotaWindowStatusRejected {
		t.Fatalf("unexpected model fallback windows %+v", windows)
	}
}

func TestAntigravityUsageSummaryKeepsQueuedWindowUnstarted(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		resetAntigravityCapabilityCache()
		defer resetAntigravityCapabilityCache()
		queuedAt := time.Now()
		now := queuedAt
		oldClock := antigravityNowFunc
		antigravityNowFunc = func() time.Time { return now }
		defer func() { antigravityNowFunc = oldClock }()
		oldSlots := antigravityProbeSlots
		antigravityProbeSlots = make(chan struct{}, 1)
		antigravityProbeSlots <- struct{}{}
		defer func() { antigravityProbeSlots = oldSlots }()
		oldTransport := http.DefaultTransport
		http.DefaultTransport = antigravityReviewTransport(func(req *http.Request) (*http.Response, error) {
			if req.URL.Path != sdkauth.AntigravityQuotaSummaryPath {
				return nil, fmt.Errorf("unexpected probe path %s", req.URL.Path)
			}
			reset := now.Add(antigravityFiveHourWindow).UTC().Format(time.RFC3339)
			body := fmt.Sprintf(`{"buckets":[{"bucketId":"gemini-5h","window":"5h","remainingFraction":1,"resetTime":%q}]}`, reset)
			return antigravityVersionTestResponse(req, http.StatusOK, body), nil
		})
		defer func() { http.DefaultTransport = oldTransport }()

		manager := coreauth.NewManager(nil, nil, nil)
		svc := &Service{coreManager: manager}
		auth, err := manager.Register(t.Context(), antigravityTestAuth("summary-queued", "https://quota.invalid"))
		if err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- svc.NewAntigravityUsageLookup().Refresh(t.Context(), auth) }()
		synctest.Wait()
		now = queuedAt.Add(antigravityUnstartedTolerance + time.Second)
		<-antigravityProbeSlots
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		stored, _ := manager.GetByID(auth.ID)
		if !stored.Quota.LookedUpAt.Equal(queuedAt) {
			t.Fatalf("published lookup time = %v, want %v", stored.Quota.LookedUpAt, queuedAt)
		}
		windows := stored.Quota.LookupWindows
		if len(windows) != 1 || !windows[0].ResetsAt.IsZero() {
			t.Fatalf("queued summary should publish one unstarted window, got %+v", windows)
		}
	})
}

func TestAntigravityUsageDoesNotShareCatalogOnlyResult(t *testing.T) {
	resetAntigravityCapabilityCache()
	t.Cleanup(resetAntigravityCapabilityCache)
	now := time.Date(2026, 11, 22, 18, 0, 0, 0, time.UTC)
	oldClock := antigravityNowFunc
	antigravityNowFunc = func() time.Time { return now }
	t.Cleanup(func() { antigravityNowFunc = oldClock })
	catalogStarted, releaseCatalog := make(chan struct{}), make(chan struct{})
	catalogDone := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case antigravityModelsPath:
			close(catalogStarted)
			<-releaseCatalog
			_, _ = io.WriteString(w, `{"models":{"gemini-pro":{"quotaInfo":{"remainingFraction":0.25}}}}`)
		case sdkauth.AntigravityQuotaSummaryPath:
			<-catalogDone
			_, _ = io.WriteString(w, `{"buckets":[{"bucketId":"gemini-5h","window":"5h","remainingFraction":0.5}]}`)
		default:
			t.Errorf("unexpected probe path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	manager := coreauth.NewManager(nil, nil, nil)
	svc := &Service{coreManager: manager}
	auth, err := manager.Register(t.Context(), antigravityTestAuth("usage-after-catalog", server.URL))
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		defer close(catalogDone)
		svc.fetchAntigravityModelCapabilityHintsForAuth(t.Context(), auth)
	}()
	<-catalogStarted
	followerCtx := &antigravityObservedContext{Context: t.Context(), waiting: make(chan struct{})}
	usageDone := make(chan error, 1)
	go func() { usageDone <- svc.NewAntigravityUsageLookup().Refresh(followerCtx, auth) }()
	<-followerCtx.waiting
	close(releaseCatalog)
	<-catalogDone
	if err := <-usageDone; err != nil {
		t.Fatal(err)
	}
	current, _ := manager.GetByID(auth.ID)
	windows := current.Quota.LookupWindows
	if len(windows) != 1 || !strings.HasPrefix(windows[0].Key, antigravitySummaryWindowPrefix) || windows[0].Scoped {
		t.Fatalf("usage lookup stored %+v, want account-wide summary", windows)
	}
}

func TestAntigravityScheduledRefreshUpdatesUsage(t *testing.T) {
	for _, summaryAvailable := range []bool{true, false} {
		name := "summary"
		if !summaryAvailable {
			name = "catalog fallback"
		}
		t.Run(name, func(t *testing.T) {
			resetAntigravityCapabilityCache()
			t.Cleanup(resetAntigravityCapabilityCache)
			now := time.Date(2026, 11, 22, 18, 0, 0, 0, time.UTC)
			oldClock := antigravityNowFunc
			antigravityNowFunc = func() time.Time { return now }
			t.Cleanup(func() { antigravityNowFunc = oldClock })
			var mu sync.Mutex
			var paths []string
			remaining := "0.75"
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				paths = append(paths, r.URL.Path)
				switch r.URL.Path {
				case sdkauth.AntigravityQuotaSummaryPath:
					if !summaryAvailable {
						w.WriteHeader(http.StatusNotFound)
						return
					}
					_, _ = io.WriteString(w, `{"buckets":[{"bucketId":"gemini-5h","window":"5h","remainingFraction":`+remaining+`}]}`)
				case antigravityModelsPath:
					_, _ = io.WriteString(w, `{"models":{"gemini-3.1-flash-lite":{"quotaInfo":{"remainingFraction":`+remaining+`}}}}`)
				default:
					t.Errorf("unexpected probe path %s", r.URL.Path)
				}
			}))
			defer server.Close()
			manager := coreauth.NewManager(nil, nil, nil)
			svc := &Service{coreManager: manager}
			auth, err := manager.Register(t.Context(), antigravityTestAuth("scheduled-usage", server.URL))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { GlobalModelRegistry().UnregisterClient(auth.ID) })
			refresh := func() {
				svc.refreshAntigravityModels(t.Context())
				svc.WaitAntigravityProbes()
			}
			check := func(wantUsed float64, wantPaths []string) {
				t.Helper()
				current, _ := manager.GetByID(auth.ID)
				windows := current.Quota.LookupWindows
				if len(windows) != 1 || windows[0].UsedPercent == nil || *windows[0].UsedPercent != wantUsed ||
					hasAntigravitySummaryWindows(windows) != summaryAvailable || !current.Quota.LookedUpAt.Equal(now) {
					t.Fatalf("unexpected scheduled quota: %+v", current.Quota)
				}
				mu.Lock()
				defer mu.Unlock()
				if !slices.Equal(paths, wantPaths) {
					t.Fatalf("requests = %v, want %v", paths, wantPaths)
				}
			}
			refresh()
			wantPaths := []string{sdkauth.AntigravityQuotaSummaryPath, antigravityModelsPath}
			check(25, wantPaths)
			refresh()
			check(25, wantPaths)
			mu.Lock()
			remaining = "0.5"
			mu.Unlock()
			now = now.Add(antigravityCapabilityCacheTTL)
			refresh()
			check(50, append(slices.Clone(wantPaths), wantPaths...))
			if !GlobalModelRegistry().ClientSupportsModel(auth.ID, "gemini-3.1-flash-lite") {
				t.Fatal("scheduled refresh did not register the catalog")
			}
		})
	}
}

func TestAntigravityProbeBackoffScopes(t *testing.T) {
	for _, tc := range []struct {
		name          string
		failingPath   string
		status        int
		sharedBackoff bool
	}{
		{"catalog transient", antigravityModelsPath, http.StatusServiceUnavailable, false},
		{"summary transient", sdkauth.AntigravityQuotaSummaryPath, http.StatusServiceUnavailable, false},
		{"summary unauthorized", sdkauth.AntigravityQuotaSummaryPath, http.StatusUnauthorized, true},
		{"catalog forbidden", antigravityModelsPath, http.StatusForbidden, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resetAntigravityCapabilityCache()
			t.Cleanup(resetAntigravityCapabilityCache)
			now := time.Date(2026, 11, 22, 18, 0, 0, 0, time.UTC)
			oldClock := antigravityNowFunc
			antigravityNowFunc = func() time.Time { return now }
			t.Cleanup(func() { antigravityNowFunc = oldClock })
			var mu sync.Mutex
			var paths []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				paths = append(paths, r.URL.Path)
				mu.Unlock()
				switch r.URL.Path {
				case tc.failingPath:
					w.WriteHeader(tc.status)
				case sdkauth.AntigravityQuotaSummaryPath:
					_, _ = io.WriteString(w, `{"buckets":[{"bucketId":"gemini-5h","window":"5h","remainingFraction":0.5}]}`)
				case antigravityModelsPath:
					_, _ = io.WriteString(w, `{"models":{"gemini-3.1-flash-lite":{}}}`)
				default:
					t.Errorf("unexpected probe path %s", r.URL.Path)
				}
			}))
			defer server.Close()
			manager := coreauth.NewManager(nil, nil, nil)
			svc := &Service{coreManager: manager}
			auth, err := manager.Register(t.Context(), antigravityTestAuth("probe-backoff", server.URL))
			if err != nil {
				t.Fatal(err)
			}
			failingUsage := tc.failingPath == sdkauth.AntigravityQuotaSummaryPath
			if _, _, err := svc.fetchAntigravityModelHints(t.Context(), auth, failingUsage); err == nil {
				t.Fatal("expected first probe to fail")
			}
			if _, _, err := svc.fetchAntigravityModelHints(t.Context(), auth, !failingUsage); (err != nil) != tc.sharedBackoff {
				t.Fatalf("other endpoint error = %v, want shared backoff %t", err, tc.sharedBackoff)
			}
			if _, _, err := svc.fetchAntigravityModelHints(t.Context(), auth, failingUsage); err == nil || !strings.Contains(err.Error(), "in backoff") {
				t.Fatalf("failing endpoint retry = %v, want backoff", err)
			}
			want := []string{tc.failingPath}
			if !tc.sharedBackoff {
				otherPath := antigravityModelsPath
				if !failingUsage {
					otherPath = sdkauth.AntigravityQuotaSummaryPath
				}
				want = append(want, otherPath)
			}
			mu.Lock()
			defer mu.Unlock()
			if !slices.Equal(paths, want) {
				t.Fatalf("requests = %v, want %v", paths, want)
			}
		})
	}
}
