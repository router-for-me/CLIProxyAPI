package cliproxy

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	sdkauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestAntigravityQuotaWindows(t *testing.T) {
	reset := time.Date(2026, 11, 22, 18, 0, 0, 0, time.UTC)
	quotas := map[string]sdkauth.AntigravityModelQuota{
		"gemini-pro":   {ResetTime: reset.Add(time.Second), RemainingFraction: 0.12501},
		"gemini-flash": {ResetTime: reset.Add(30 * time.Second).In(time.FixedZone("PST", -8*60*60)), RemainingFraction: 0.12504},
		"gemini-lite":  {ResetTime: reset.Add(time.Minute), RemainingFraction: 0.12501},
		"claude":       {ResetTime: reset.Add(time.Minute)},
		"claude-lite":  {ResetTime: reset.Add(2 * time.Minute), RemainingFraction: 0.00001},
		"gpt-a":        {ResetTime: reset, RemainingFraction: 0.5, DisplayName: "Short"},
		"other-a":      {ResetTime: reset, RemainingFraction: 0.5, DisplayName: "Longer Name"},
		"gpt-b":        {ResetTime: reset, RemainingFraction: 0.25005},
		"gpt-c":        {ResetTime: reset, RemainingFraction: 0.25006},
		"other":        {RemainingFraction: 1},
	}
	windows := AntigravityQuotaWindows(quotas)
	want := []struct {
		key, label, status string
		used               float64
		reset              time.Time
	}{
		{"ag_claude", "Claude", "rejected", 100, reset.Add(time.Minute)},
		{"ag_claude_2", "Claude", "allowed", 100, reset.Add(2 * time.Minute)},
		{"ag_gemini", "Gemini", "allowed", 87.5, reset.Add(30 * time.Second)},
		{"ag_gemini_2", "Gemini", "allowed", 87.5, reset.Add(time.Minute)},
		{"ag_gpt", "GPT", "allowed", 75, reset},
		{"ag_short", "Short", "allowed", 50, reset},
		{"ag_quota", "Quota", "allowed", 0, time.Time{}},
	}
	if len(windows) != len(want) {
		t.Fatalf("got %d windows, want %d", len(windows), len(want))
	}
	for i, w := range windows {
		expected := want[i]
		if w.Key != expected.key || w.Label != expected.label || w.Status != expected.status ||
			w.UsedPercent == nil || *w.UsedPercent != expected.used || w.ResetsAt != expected.reset || !w.Scoped {
			t.Errorf("window %d = %+v, want %+v with Scoped=true", i, w, expected)
		}
	}
}

func TestAntigravityQuotaWindowsKeepKeysWhenUsageOrderChanges(t *testing.T) {
	reset := time.Date(2026, 11, 22, 18, 0, 0, 0, time.UTC)
	flash := sdkauth.AntigravityModelQuota{ResetTime: reset, RemainingFraction: 0.2}
	pro := sdkauth.AntigravityModelQuota{ResetTime: reset.Add(time.Minute), RemainingFraction: 0.8}
	windows := func() []coreauth.QuotaWindow {
		return AntigravityQuotaWindows(map[string]sdkauth.AntigravityModelQuota{"gemini-flash": flash, "gemini-pro": pro})
	}
	before := windows()
	flash.RemainingFraction, pro.RemainingFraction = pro.RemainingFraction, flash.RemainingFraction
	after := windows()
	if len(before) != 2 || len(after) != 2 {
		t.Fatalf("expected two buckets before and after swap, got %v and %v", before, after)
	}
	for i, first := range before {
		second := after[len(after)-1-i]
		if first.Label != "Gemini" || first.Label != second.Label || first.ResetsAt != second.ResetsAt || first.Key != second.Key {
			t.Errorf("same bucket moved from %+v to %+v", first, second)
		}
	}
}

func TestAntigravityUsageLookupRequiresAccessToken(t *testing.T) {
	lookup := &AntigravityUsageLookup{}
	auth := &coreauth.Auth{Provider: "antigravity", Metadata: map[string]any{"email": "oauth@example.com"}}
	if lookup.Eligible(auth) {
		t.Error("OAuth credential without an access token is eligible")
	}
	auth.Metadata["access_token"] = "  "
	if lookup.Eligible(auth) {
		t.Error("OAuth credential with a blank access token is eligible")
	}
	auth.Metadata["access_token"] = " token "
	if !lookup.Eligible(auth) {
		t.Error("OAuth credential with an access token is ineligible")
	}
}

func TestAntigravityUsageLookup(t *testing.T) {
	for _, scenario := range []struct {
		status  int
		message string
	}{
		{http.StatusUnauthorized, "401"},
		{http.StatusForbidden, "403"},
		{http.StatusServiceUnavailable, "did not return a catalog"},
	} {
		t.Run(http.StatusText(scenario.status), func(t *testing.T) {
			resetAntigravityCapabilityCache()
			t.Cleanup(resetAntigravityCapabilityCache)
			now := time.Date(2026, 11, 22, 18, 0, 0, 0, time.UTC)
			oldClock := antigravityNowFunc
			antigravityNowFunc = func() time.Time { return now }
			t.Cleanup(func() { antigravityNowFunc = oldClock })
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != antigravityModelsPath || r.Header.Get("Authorization") != "Bearer token-usage" {
					t.Errorf("unexpected model probe %s %s", r.Method, r.URL.Path)
				}
				if calls.Add(1) > 2 {
					w.WriteHeader(scenario.status)
					return
				}
				_, _ = w.Write([]byte(`{"models":{"gemini-pro":{"quotaInfo":{"remainingFraction":0.2}}}}`))
			}))
			defer server.Close()
			manager := coreauth.NewManager(nil, nil, nil)
			svc := &Service{coreManager: manager}
			auth, err := manager.Register(t.Context(), antigravityTestAuth("usage", server.URL))
			if err != nil {
				t.Fatal(err)
			}
			lookup := svc.NewAntigravityUsageLookup()
			if !lookup.Eligible(auth) || lookup.Eligible(nil) {
				t.Fatal("unexpected OAuth eligibility")
			}
			apiKey := auth.Clone()
			apiKey.ID = "api-key"
			apiKey.Attributes[coreauth.AttributeAPIKey] = "key"
			if _, err := manager.Register(t.Context(), apiKey); err != nil {
				t.Fatal(err)
			}
			svc.fetchAntigravityModelCapabilityHintsForAuth(t.Context(), auth)
			if failures := lookup.RefreshAll(t.Context()); len(failures) != 0 || calls.Load() != 2 {
				t.Fatalf("expected usage to bypass fresh catalog only for OAuth, calls=%d failures=%v", calls.Load(), failures)
			}
			stored, _ := manager.GetByID(auth.ID)
			if !stored.Quota.LookedUpAt.Equal(now) {
				t.Fatalf("lookup timestamp = %v, want %v", stored.Quota.LookedUpAt, now)
			}
			now = now.Add(time.Second)
			if err := lookup.Refresh(t.Context(), auth); err == nil || !strings.Contains(err.Error(), scenario.message) {
				t.Fatalf("expected %s failure, got %v", scenario.message, err)
			}
			if err := lookup.Refresh(t.Context(), auth); err == nil || !strings.Contains(err.Error(), "backoff") || calls.Load() != 3 {
				t.Fatalf("expected backoff with no new request, calls=%d error=%v", calls.Load(), err)
			}
			latest, _ := manager.GetByID(auth.ID)
			if !latest.Quota.LookedUpAt.Equal(stored.Quota.LookedUpAt) {
				t.Fatal("failed lookup changed the last successful lookup timestamp")
			}
		})
	}
}

func TestAntigravityUsageLookupSharesProbeTimestamp(t *testing.T) {
	resetAntigravityCapabilityCache()
	t.Cleanup(resetAntigravityCapabilityCache)
	start := time.Date(2026, 11, 22, 18, 0, 0, 0, time.UTC)
	oldClock := antigravityNowFunc
	var elapsed atomic.Int64
	antigravityNowFunc = func() time.Time { return start.Add(time.Duration(elapsed.Load())) }
	t.Cleanup(func() { antigravityNowFunc = oldClock })

	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			close(started)
			<-release
		}
		_, _ = w.Write([]byte(`{"models":{"gemini-pro":{"quotaInfo":{"remainingFraction":0.2}}}}`))
	}))
	defer server.Close()
	manager := coreauth.NewManager(nil, nil, nil)
	svc := &Service{coreManager: manager}
	auth, err := manager.Register(t.Context(), antigravityTestAuth("usage-shared-timestamp", server.URL))
	if err != nil {
		t.Fatal(err)
	}
	leaderDone := make(chan struct{})
	go func() {
		defer close(leaderDone)
		svc.fetchAntigravityModelCapabilityHintsForAuth(t.Context(), auth)
	}()
	<-started
	elapsed.Store(int64(time.Second))

	followerCtx := &antigravityObservedContext{Context: t.Context(), waiting: make(chan struct{})}
	followerDone := make(chan error, 1)
	go func() { followerDone <- svc.NewAntigravityUsageLookup().Refresh(followerCtx, auth) }()
	<-followerCtx.waiting
	close(release)
	<-leaderDone
	if err := <-followerDone; err != nil {
		t.Fatal(err)
	}
	stored, _ := manager.GetByID(auth.ID)
	if got := stored.Quota.LookedUpAt; calls.Load() != 1 || !got.Equal(start) {
		t.Fatalf("probe calls = %d, lookup timestamp = %v, want one probe at %v", calls.Load(), got, start)
	}
}

func TestAntigravityUsageLookupKeepsSnapshotWithoutQuota(t *testing.T) {
	resetAntigravityCapabilityCache()
	t.Cleanup(resetAntigravityCapabilityCache)
	now := time.Date(2026, 11, 22, 18, 0, 0, 0, time.UTC)
	oldClock := antigravityNowFunc
	antigravityNowFunc = func() time.Time { return now }
	t.Cleanup(func() { antigravityNowFunc = oldClock })
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			_, _ = w.Write([]byte(`{"models":{"gemini-pro":{"quotaInfo":{"remainingFraction":0.2}}}}`))
			return
		}
		_, _ = w.Write([]byte(`{"models":{"gemini-pro":{}}}`))
	}))
	defer server.Close()

	manager := coreauth.NewManager(nil, nil, nil)
	svc := &Service{coreManager: manager}
	auth, err := manager.Register(t.Context(), antigravityTestAuth("usage-no-quota", server.URL))
	if err != nil {
		t.Fatal(err)
	}
	var published []coreauth.QuotaState
	defer manager.SubscribeQuotaUpdates(func() {
		current, _ := manager.GetByID(auth.ID)
		published = append(published, current.Quota)
	})()

	lookup := svc.NewAntigravityUsageLookup()
	if err := lookup.Refresh(t.Context(), auth); err != nil {
		t.Fatal(err)
	}
	before, _ := manager.GetByID(auth.ID)
	if len(before.Quota.LookupWindows) != 1 || !before.Quota.LookedUpAt.Equal(now) {
		t.Fatalf("first lookup snapshot = %+v", before.Quota)
	}
	now = now.Add(time.Second)
	if err := lookup.Refresh(t.Context(), auth); err == nil || err.Error() != "antigravity model probe returned no quota information" {
		t.Fatalf("empty quota lookup error = %v", err)
	}
	after, _ := manager.GetByID(auth.ID)
	if !after.Quota.LookedUpAt.Equal(before.Quota.LookedUpAt) || !reflect.DeepEqual(after.Quota.LookupWindows, before.Quota.LookupWindows) {
		t.Fatalf("empty quota lookup changed the snapshot from %+v to %+v", before.Quota, after.Quota)
	}
	if len(published) != 1 || !reflect.DeepEqual(published[0], before.Quota) {
		t.Fatalf("published quota snapshots = %+v, want only the committed snapshot", published)
	}
}
