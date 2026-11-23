package cliproxy

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	sdkauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestAntigravitySummarySupersedesNewerCatalogSnapshot(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	svc := &Service{coreManager: manager}
	auth, err := manager.Register(t.Context(), antigravityTestAuth("summary-publication", "https://quota.invalid"))
	if err != nil {
		t.Fatal(err)
	}
	key := svc.antigravityCapabilityKey(auth)
	summaryAt := time.Date(2026, 11, 22, 18, 0, 0, 0, time.UTC)
	catalogAt := summaryAt.Add(time.Second)
	catalog := AntigravityQuotaWindows(map[string]sdkauth.AntigravityModelQuota{
		"gemini-pro": {RemainingFraction: 0.25},
	})
	summary := antigravitySummaryWindows([]sdkauth.AntigravityQuotaBucket{
		{ID: "gemini-5h", Window: sdkauth.AntigravityQuotaWindowFiveHour, RemainingFraction: 0.5},
	}, summaryAt)
	if err := svc.recordAntigravityQuota(t.Context(), auth, key, catalog, catalogAt, 2, true); err != nil {
		t.Fatal(err)
	}
	if err := svc.recordAntigravityQuota(t.Context(), auth, key, summary, summaryAt, 1, false); err != nil {
		t.Fatal(err)
	}
	if err := svc.recordAntigravityQuota(t.Context(), auth, key, catalog, catalogAt.Add(time.Second), 3, true); err != nil {
		t.Fatal(err)
	}
	staleAuth := auth.Clone()
	staleAuth.Prefix = "stale"
	if err := svc.recordAntigravityQuota(t.Context(), staleAuth, key, catalog, summaryAt, 1, true); !errors.Is(err, coreauth.ErrQuotaLookupCredentialChanged) {
		t.Fatalf("stale credential error = %v, want %v", err, coreauth.ErrQuotaLookupCredentialChanged)
	}
	current, _ := manager.GetByID(auth.ID)
	if !reflect.DeepEqual(current.Quota.LookupWindows, summary) || !current.Quota.LookedUpAt.Equal(catalogAt) {
		t.Fatalf("stored quota = %+v, want summary windows with timestamp %s", current.Quota, catalogAt)
	}
}

func TestAntigravityQuotaWindows(t *testing.T) {
	reset := time.Date(2026, 11, 22, 18, 0, 0, 0, time.UTC)
	quotas := map[string]sdkauth.AntigravityModelQuota{
		"gemini-pro":   {ResetTime: reset.Add(time.Second), RemainingFraction: 0.12501},
		"gemini-flash": {ResetTime: reset.Add(30 * time.Second).In(time.FixedZone("PST", -8*60*60)), RemainingFraction: 0.12504},
		"claude":       {ResetTime: reset.Add(time.Minute), RemainingFraction: -0.1},
		"claude-lite":  {ResetTime: reset.Add(2 * time.Minute), RemainingFraction: 0.00001},
		"a-b":          {ResetTime: reset, RemainingFraction: 0.5, DisplayName: " Shared "},
		"a_b":          {ResetTime: reset, RemainingFraction: 0.5, DisplayName: " Shared "},
		"other":        {RemainingFraction: 1.1},
	}
	windows := AntigravityQuotaWindows(quotas)
	want := []struct {
		key, label, status string
		used               float64
		reset              time.Time
	}{
		{"ag:claude", "claude", coreauth.QuotaWindowStatusRejected, 100, reset.Add(time.Minute)},
		{"ag:claude-lite", "claude-lite", coreauth.QuotaWindowStatusAllowed, 99.99, reset.Add(2 * time.Minute)},
		{"ag:gemini-pro", "gemini-pro", coreauth.QuotaWindowStatusAllowed, 87.5, reset.Add(time.Second)},
		{"ag:gemini-flash", "gemini-flash", coreauth.QuotaWindowStatusAllowed, 87.5, reset.Add(30 * time.Second)},
		{"ag:a-b", "Shared", coreauth.QuotaWindowStatusAllowed, 50, reset},
		{"ag:a_b", "Shared", coreauth.QuotaWindowStatusAllowed, 50, reset},
		{"ag:other", "other", coreauth.QuotaWindowStatusAllowed, 0, time.Time{}},
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

func TestAntigravityQuotaWindowsKeepKeysWhenUsageChanges(t *testing.T) {
	quotas := map[string]sdkauth.AntigravityModelQuota{
		"gemini-flash": {DisplayName: "Flash", RemainingFraction: 0.2},
		"gemini-pro":   {DisplayName: "Pro", RemainingFraction: 0.8},
	}
	before := AntigravityQuotaWindows(quotas)
	quotas["gemini-flash"] = sdkauth.AntigravityModelQuota{DisplayName: "Renamed Flash", RemainingFraction: 0.9}
	after := AntigravityQuotaWindows(quotas)
	if len(before) != 2 || len(after) != 2 {
		t.Fatalf("expected two windows before and after update, got %v and %v", before, after)
	}
	if before[0].Key != "ag:gemini-flash" || after[1].Key != before[0].Key || after[0].Key != before[1].Key {
		t.Fatalf("quota update changed model keys: %v to %v", before, after)
	}
}

func TestAntigravityUsageLookupFollowsHome(t *testing.T) {
	cfg := &config.Config{}
	lookup := (&Service{cfg: cfg}).NewAntigravityUsageLookup()
	auth := antigravityTestAuth("home-eligible", "")
	for _, home := range []bool{false, true, false} {
		cfg.Home.Enabled = home
		if got := lookup.Eligible(auth); got == home {
			t.Errorf("Home enabled = %t, Eligible = %t", home, got)
		}
	}
	if (*AntigravityUsageLookup)(nil).Eligible(auth) {
		t.Fatal("nil lookup is eligible")
	}
}

func TestAntigravityQuotaLookupPublicationOrder(t *testing.T) {
	now := time.Unix(2_000, 0)
	for _, tt := range []struct {
		name          string
		staleAt       time.Time
		staleRevision uint64
	}{
		{"older revision at equal timestamp", now, 1},
		{"older timestamp at higher revision", now.Add(-time.Second), 3},
	} {
		t.Run(tt.name, func(t *testing.T) {
			manager := coreauth.NewManager(nil, nil, nil)
			svc := &Service{coreManager: manager}
			auth, err := manager.Register(t.Context(), antigravityTestAuth("quota-order", ""))
			if err != nil {
				t.Fatal(err)
			}
			key := svc.antigravityCapabilityKey(auth)
			if err := svc.recordAntigravityQuota(t.Context(), auth, key, []coreauth.QuotaWindow{{Key: "current"}}, now, 2, false); err != nil {
				t.Fatal(err)
			}
			if err := svc.recordAntigravityQuota(t.Context(), auth, key, []coreauth.QuotaWindow{{Key: "stale"}}, tt.staleAt, tt.staleRevision, false); err != nil {
				t.Fatal(err)
			}
			stored, _ := manager.GetByID(auth.ID)
			if !stored.Quota.LookedUpAt.Equal(now) || len(stored.Quota.LookupWindows) != 1 || stored.Quota.LookupWindows[0].Key != "current" {
				t.Fatalf("stale publication replaced lookup: %+v", stored.Quota)
			}
			if err := svc.recordAntigravityQuota(t.Context(), auth, key, []coreauth.QuotaWindow{{Key: "newer"}}, now, 3, false); err != nil {
				t.Fatal(err)
			}
			stored, _ = manager.GetByID(auth.ID)
			if !stored.Quota.LookedUpAt.Equal(now) || len(stored.Quota.LookupWindows) != 1 || stored.Quota.LookupWindows[0].Key != "newer" {
				t.Fatalf("higher revision at the same timestamp did not replace lookup: %+v", stored.Quota)
			}
		})
	}
}

func TestAntigravityUsageLookupRequiresAccessToken(t *testing.T) {
	lookup := (&Service{}).NewAntigravityUsageLookup()
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
		{http.StatusServiceUnavailable, "quota summary probe failed"},
	} {
		t.Run(http.StatusText(scenario.status), func(t *testing.T) {
			resetAntigravityCapabilityCache()
			t.Cleanup(resetAntigravityCapabilityCache)
			now := time.Date(2026, 11, 22, 18, 0, 0, 0, time.UTC)
			oldClock := antigravityNowFunc
			antigravityNowFunc = func() time.Time { return now }
			t.Cleanup(func() { antigravityNowFunc = oldClock })
			var catalogCalls, summaryCalls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer token-usage" {
					t.Errorf("unexpected probe %s %s", r.Method, r.URL.Path)
				}
				switch r.URL.Path {
				case antigravityModelsPath:
					catalogCalls.Add(1)
					_, _ = w.Write([]byte(`{"models":{"gemini-pro":{"quotaInfo":{"remainingFraction":0.2}}}}`))
				case sdkauth.AntigravityQuotaSummaryPath:
					if summaryCalls.Add(1) == 1 {
						w.WriteHeader(http.StatusNotFound)
					} else {
						w.WriteHeader(scenario.status)
					}
				default:
					t.Errorf("unexpected probe path %s", r.URL.Path)
				}
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
			if failures := lookup.RefreshAll(t.Context()); len(failures) != 0 || catalogCalls.Load() != 2 || summaryCalls.Load() != 1 {
				t.Fatalf("expected OAuth usage to try summary then bypass fresh catalog, catalog=%d summary=%d failures=%v", catalogCalls.Load(), summaryCalls.Load(), failures)
			}
			stored, _ := manager.GetByID(auth.ID)
			if !stored.Quota.LookedUpAt.Equal(now) {
				t.Fatalf("lookup timestamp = %v, want %v", stored.Quota.LookedUpAt, now)
			}
			now = now.Add(time.Second)
			if err := lookup.Refresh(t.Context(), auth); err == nil || !strings.Contains(err.Error(), scenario.message) {
				t.Fatalf("expected %s failure, got %v", scenario.message, err)
			}
			if err := lookup.Refresh(t.Context(), auth); err == nil || !strings.Contains(err.Error(), "backoff") || catalogCalls.Load() != 2 || summaryCalls.Load() != 2 {
				t.Fatalf("expected backoff with no new request, catalog=%d summary=%d error=%v", catalogCalls.Load(), summaryCalls.Load(), err)
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
		if r.URL.Path != sdkauth.AntigravityQuotaSummaryPath {
			t.Errorf("unexpected probe path %s", r.URL.Path)
		}
		if calls.Add(1) == 1 {
			close(started)
			<-release
		}
		_, _ = w.Write([]byte(`{"buckets":[{"bucketId":"gemini-5h","window":"5h","remainingFraction":1,"resetTime":"2026-11-22T22:59:00Z"}]}`))
	}))
	defer server.Close()
	manager := coreauth.NewManager(nil, nil, nil)
	svc := &Service{coreManager: manager}
	auth, err := manager.Register(t.Context(), antigravityTestAuth("usage-shared-timestamp", server.URL))
	if err != nil {
		t.Fatal(err)
	}
	var publications atomic.Int32
	defer manager.SubscribeQuotaUpdates(func() { publications.Add(1) })()
	leaderDone := make(chan error, 1)
	go func() {
		leaderDone <- svc.NewAntigravityUsageLookup().Refresh(t.Context(), auth)
	}()
	<-started
	elapsed.Store(int64(time.Second))

	followerCtx := &antigravityObservedContext{Context: t.Context(), waiting: make(chan struct{})}
	followerDone := make(chan error, 1)
	go func() { followerDone <- svc.NewAntigravityUsageLookup().Refresh(followerCtx, auth) }()
	<-followerCtx.waiting
	close(release)
	if err := <-leaderDone; err != nil {
		t.Fatal(err)
	}
	if err := <-followerDone; err != nil {
		t.Fatal(err)
	}
	stored, _ := manager.GetByID(auth.ID)
	if got := stored.Quota.LookedUpAt; calls.Load() != 1 || publications.Load() != 1 || !got.Equal(start) {
		t.Fatalf("probe calls = %d, publications = %d, lookup timestamp = %v, want one probe and one publication at %v", calls.Load(), publications.Load(), got, start)
	}
	if len(stored.Quota.LookupWindows) != 1 || !stored.Quota.LookupWindows[0].ResetsAt.IsZero() {
		t.Fatalf("unstarted window = %+v", stored.Quota.LookupWindows)
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
		if r.URL.Path == sdkauth.AntigravityQuotaSummaryPath {
			if calls.Add(1) == 1 {
				_, _ = w.Write([]byte(`{"buckets":[{"bucketId":"gemini-5h","window":"5h","remainingFraction":0.2}]}`))
			}
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
