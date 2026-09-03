package cachestats

import (
	"testing"
	"time"
)

func newTestStore(t *testing.T, maxSessions, perSession int, idleTTL time.Duration) *Store {
	t.Helper()
	return NewStore(Config{
		Enabled:            true,
		MaxSessions:        maxSessions,
		PerSessionRequests: perSession,
		IdleTTL:            idleTTL,
	})
}

func observation(session string, cacheRead, cacheCreation int64, at time.Time) Observation {
	return Observation{
		SessionID:           session,
		KeyedBy:             KeyedBySession,
		Provider:            "claude",
		Model:               "claude-sonnet-5",
		AuthID:              "auth-1",
		At:                  at,
		Signal:              SignalFull,
		InputTokens:         2,
		PromptTokens:        2 + cacheRead + cacheCreation,
		OutputTokens:        4,
		MaxTokens:           1024,
		CacheReadTokens:     cacheRead,
		CacheCreationTokens: cacheCreation,
	}
}

func TestRecordClassifiesTiers(t *testing.T) {
	base := time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC)
	store := newTestStore(t, 10, 10, time.Hour)

	store.Record(observation("s1", 0, 1000, base))
	store.Record(observation("s1", 1000, 500, base.Add(time.Second)))
	store.Record(observation("s1", 1500, 200, base.Add(2*time.Second)))
	store.Record(observation("s1", 900, 800, base.Add(3*time.Second)))

	detail, ok := store.Session("s1")
	if !ok {
		t.Fatal("Session(s1) not found")
	}
	wantTiers := []Tier{TierT0, TierHit, TierHit, TierMiss}
	if len(detail.Requests) != len(wantTiers) {
		t.Fatalf("Requests length = %d, want %d", len(detail.Requests), len(wantTiers))
	}
	for i, want := range wantTiers {
		if got := detail.Requests[i].Tier; got != want {
			t.Errorf("request %d tier = %q, want %q", i+1, got, want)
		}
		if got := detail.Requests[i].Seq; got != i+1 {
			t.Errorf("request %d seq = %d, want %d", i, got, i+1)
		}
	}
	if got := detail.Requests[1].DeltaRead; got != 1000 {
		t.Errorf("hit delta_read = %d, want 1000", got)
	}
	if got := detail.Requests[3].DeltaRead; got != -600 {
		t.Errorf("miss delta_read = %d, want -600", got)
	}
	if detail.Summary.Requests != 4 || detail.Summary.Hits != 2 || detail.Summary.Misses != 1 || detail.Summary.T0s != 1 {
		t.Errorf("summary = %+v, want 4 requests / 2 hits / 1 miss / 1 T0", detail.Summary)
	}
}

func TestLostTokenMath(t *testing.T) {
	base := time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC)
	store := newTestStore(t, 10, 10, time.Hour)

	store.Record(observation("s1", 0, 1000, base))
	store.Record(observation("s1", 5000, 0, base.Add(time.Second)))
	// prior max read is 5000; a drop to 1200 loses 3800.
	store.Record(observation("s1", 1200, 4000, base.Add(2*time.Second)))
	// prior max read is still 5000; a drop to 4000 loses 1000.
	store.Record(observation("s1", 4000, 100, base.Add(3*time.Second)))

	detail, ok := store.Session("s1")
	if !ok {
		t.Fatal("Session(s1) not found")
	}
	if got := detail.Summary.LostTokens; got != 4800 {
		t.Fatalf("LostTokens = %d, want 4800", got)
	}
	if got := store.Global().LostTokens; got != 4800 {
		t.Fatalf("global LostTokens = %d, want 4800", got)
	}
}

func TestRingBufferBound(t *testing.T) {
	base := time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC)
	store := newTestStore(t, 10, 3, time.Hour)

	for i := 0; i < 7; i++ {
		store.Record(observation("s1", int64(100*i), 10, base.Add(time.Duration(i)*time.Second)))
	}

	detail, ok := store.Session("s1")
	if !ok {
		t.Fatal("Session(s1) not found")
	}
	if len(detail.Requests) != 3 {
		t.Fatalf("Requests length = %d, want 3 (ring bound)", len(detail.Requests))
	}
	if got := detail.Requests[0].Seq; got != 5 {
		t.Errorf("oldest retained seq = %d, want 5", got)
	}
	if got := detail.Requests[2].Seq; got != 7 {
		t.Errorf("newest retained seq = %d, want 7", got)
	}
	// Counters must survive eviction of the underlying request rows.
	if got := detail.Summary.Requests; got != 7 {
		t.Errorf("summary requests = %d, want 7", got)
	}
	if got := detail.Summary.T0s; got != 1 {
		t.Errorf("summary T0s = %d, want 1", got)
	}
}

func TestLRUEviction(t *testing.T) {
	base := time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC)
	store := newTestStore(t, 2, 10, time.Hour)

	store.Record(observation("s1", 0, 10, base))
	store.Record(observation("s2", 0, 10, base.Add(time.Second)))
	// Touch s1 so s2 becomes the least recently seen session.
	store.Record(observation("s1", 100, 10, base.Add(2*time.Second)))
	store.Record(observation("s3", 0, 10, base.Add(3*time.Second)))

	if _, ok := store.Session("s2"); ok {
		t.Error("s2 should have been evicted as least recently seen")
	}
	if _, ok := store.Session("s1"); !ok {
		t.Error("s1 should have been retained")
	}
	if _, ok := store.Session("s3"); !ok {
		t.Error("s3 should have been retained")
	}
	if got := store.Global().Sessions; got != 2 {
		t.Errorf("session count = %d, want 2", got)
	}
}

func TestIdleTTLEviction(t *testing.T) {
	base := time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC)
	store := newTestStore(t, 10, 10, 30*time.Minute)

	store.Record(observation("stale", 0, 10, base))
	store.Record(observation("fresh", 0, 10, base.Add(time.Hour)))

	if _, ok := store.Session("stale"); ok {
		t.Error("session idle beyond the TTL should have been dropped")
	}
	if _, ok := store.Session("fresh"); !ok {
		t.Error("fresh session should have been retained")
	}
}

func TestProbeFlagAndRegime(t *testing.T) {
	base := time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC)
	store := newTestStore(t, 10, 10, time.Hour)

	first := observation("s1", 0, 35314, base)
	first.CacheCreation1hTokens = 35314
	store.Record(first)

	probe := observation("s1", 35314, 0, base.Add(time.Minute))
	probe.IsProbe = true
	probe.MaxTokens = 1
	store.Record(probe)

	detail, ok := store.Session("s1")
	if !ok {
		t.Fatal("Session(s1) not found")
	}
	if detail.Requests[0].IsProbe {
		t.Error("first request must not be flagged as a probe")
	}
	if !detail.Requests[1].IsProbe {
		t.Error("keepalive probe must be flagged")
	}
	if got := detail.Summary.Probes; got != 1 {
		t.Errorf("probe count = %d, want 1", got)
	}
	if got := detail.Summary.Regime; got != Regime1h {
		t.Errorf("regime = %q, want %q", got, Regime1h)
	}
	if got := detail.Summary.CacheCreation1hTokens; got != 35314 {
		t.Errorf("1h creation tokens = %d, want 35314", got)
	}
}

func TestMissReasonRetained(t *testing.T) {
	base := time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC)
	store := newTestStore(t, 10, 10, time.Hour)

	store.Record(observation("s1", 0, 1000, base))
	miss := observation("s1", 1000, 500, base.Add(time.Second))
	store.Record(miss)
	regress := observation("s1", 200, 900, base.Add(2*time.Second))
	regress.CacheMissReason = "messages_changed"
	regress.CacheMissedTokens = 25202
	store.Record(regress)

	detail, _ := store.Session("s1")
	last := detail.Requests[2]
	if last.MissReason != "messages_changed" || last.MissedTokens != 25202 {
		t.Fatalf("miss reason = %q/%d, want messages_changed/25202", last.MissReason, last.MissedTokens)
	}
}

func TestDisabledStoreRecordsNothing(t *testing.T) {
	store := NewStore(Config{Enabled: false, MaxSessions: 10, PerSessionRequests: 10, IdleTTL: time.Hour})
	store.Record(observation("s1", 0, 10, time.Now()))
	if got := store.Global().Requests; got != 0 {
		t.Fatalf("disabled store recorded %d requests, want 0", got)
	}
}

func TestRecordIgnoresEmptySession(t *testing.T) {
	store := newTestStore(t, 10, 10, time.Hour)
	store.Record(observation("", 0, 10, time.Now()))
	if got := store.Global().Sessions; got != 0 {
		t.Fatalf("store kept %d sessions for a blank session id, want 0", got)
	}
}

func TestGroupingAndReset(t *testing.T) {
	base := time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC)
	store := newTestStore(t, 10, 10, time.Hour)

	a := observation("s1", 0, 100, base)
	store.Record(a)
	b := observation("s2", 0, 200, base.Add(time.Second))
	b.Model = "claude-opus-5"
	b.AuthID = "auth-2"
	store.Record(b)

	byModel := store.ByModel()
	if len(byModel) != 2 {
		t.Fatalf("model groups = %d, want 2", len(byModel))
	}
	byAuth := store.ByAuth()
	if len(byAuth) != 2 {
		t.Fatalf("auth groups = %d, want 2", len(byAuth))
	}

	sessions := store.Sessions()
	if len(sessions) != 2 {
		t.Fatalf("sessions = %d, want 2", len(sessions))
	}
	// Sorted by last seen, newest first.
	if sessions[0].ID != "s2" {
		t.Errorf("sessions[0].ID = %q, want s2", sessions[0].ID)
	}
	if sessions[0].ShortID == "" {
		t.Error("ShortID must be populated")
	}

	if cleared := store.Reset(); cleared != 2 {
		t.Fatalf("Reset cleared %d sessions, want 2", cleared)
	}
	if got := store.Global().Requests; got != 0 {
		t.Fatalf("global requests after reset = %d, want 0", got)
	}
}

func TestApplyConfigShrinksBounds(t *testing.T) {
	base := time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC)
	store := newTestStore(t, 10, 10, time.Hour)
	for i := 0; i < 6; i++ {
		store.Record(observation("s1", int64(100*i), 10, base.Add(time.Duration(i)*time.Second)))
	}
	store.ApplyConfig(Config{Enabled: true, MaxSessions: 10, PerSessionRequests: 2, IdleTTL: time.Hour})

	detail, ok := store.Session("s1")
	if !ok {
		t.Fatal("Session(s1) not found")
	}
	if len(detail.Requests) != 2 {
		t.Fatalf("Requests length after shrink = %d, want 2", len(detail.Requests))
	}
}

func TestProviderWithoutCacheSignalIsNeverClassified(t *testing.T) {
	base := time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC)
	store := newTestStore(t, 10, 10, time.Hour)

	for i := 0; i < 3; i++ {
		store.Record(Observation{
			SessionID: "s1", KeyedBy: KeyedByAPIKeyModel, Provider: "xai", Model: "grok-4",
			AuthID: "auth-1", At: base.Add(time.Duration(i) * time.Second), Signal: SignalNone,
			InputTokens: 500, PromptTokens: 500, OutputTokens: 20,
		})
	}

	detail, ok := store.Session("s1")
	if !ok {
		t.Fatal("Session(s1) not found")
	}
	for _, request := range detail.Requests {
		if request.Tier != TierNA {
			t.Errorf("tier = %q, want %q for a provider with no cache signal", request.Tier, TierNA)
		}
	}
	summary := detail.Summary
	if summary.Requests != 3 {
		t.Errorf("requests = %d, want 3", summary.Requests)
	}
	if summary.Classified != 0 {
		t.Errorf("classified = %d, want 0", summary.Classified)
	}
	if summary.Hits != 0 || summary.Misses != 0 || summary.T0s != 0 {
		t.Errorf("tier counters must stay zero: %+v", summary.Aggregate)
	}
	if summary.HitRate != 0 {
		t.Errorf("hit rate = %v, want 0 with a zero Classified count", summary.HitRate)
	}
	if summary.Signal != SignalNone {
		t.Errorf("signal = %q, want none", summary.Signal)
	}
	if summary.KeyedBy != KeyedByAPIKeyModel {
		t.Errorf("keyed_by = %q, want apikey-model", summary.KeyedBy)
	}
}

func TestReadOnlySignalClassifiesAndReportsCachedShare(t *testing.T) {
	base := time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC)
	store := newTestStore(t, 10, 10, time.Hour)

	// OpenAI-style: prompt_tokens already includes cached_tokens.
	store.Record(Observation{
		SessionID: "s1", Provider: "openai-compatibility", Model: "gpt-x", AuthID: "auth-1",
		At: base, Signal: SignalRead, InputTokens: 1000, PromptTokens: 1000, OutputTokens: 10,
	})
	store.Record(Observation{
		SessionID: "s1", Provider: "openai-compatibility", Model: "gpt-x", AuthID: "auth-1",
		At: base.Add(time.Second), Signal: SignalRead, InputTokens: 1000, PromptTokens: 1000,
		OutputTokens: 10, CacheReadTokens: 800,
	})

	detail, _ := store.Session("s1")
	if detail.Requests[0].Tier != TierT0 {
		t.Errorf("first tier = %q, want T0", detail.Requests[0].Tier)
	}
	if detail.Requests[1].Tier != TierHit {
		t.Errorf("second tier = %q, want hit", detail.Requests[1].Tier)
	}
	if detail.Summary.Classified != 2 {
		t.Errorf("classified = %d, want 2", detail.Summary.Classified)
	}
	// 800 cached of 2000 prompt tokens.
	if got := detail.Summary.CachedShare; got < 0.399 || got > 0.401 {
		t.Errorf("cached share = %v, want 0.4", got)
	}
	// A read-only provider never reports a creation split, so no regime.
	if detail.Summary.Regime != RegimeNone {
		t.Errorf("regime = %q, want none", detail.Summary.Regime)
	}
}

func TestRebindAttributionOnT0(t *testing.T) {
	base := time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC)
	store := newTestStore(t, 10, 10, time.Hour)

	first := observation("s1", 0, 1000, base)
	store.Record(first)

	warm := observation("s1", 5000, 0, base.Add(time.Second))
	store.Record(warm)

	// Same credential, cold read: the prefix aged out.
	expired := observation("s1", 0, 4000, base.Add(2*time.Second))
	store.Record(expired)

	// Different credential, cold read: the prefix lives on the other account.
	rebound := observation("s1", 0, 4000, base.Add(3*time.Second))
	rebound.AuthID = "auth-2"
	store.Record(rebound)

	detail, _ := store.Session("s1")
	wantCauses := []T0Cause{T0CauseFirst, "", T0CauseExpiry, T0CauseRebind}
	for i, want := range wantCauses {
		if got := detail.Requests[i].T0Cause; got != want {
			t.Errorf("request %d t0_cause = %q, want %q", i+1, got, want)
		}
	}
	if detail.Requests[3].Rebind != true {
		t.Error("credential change must mark the request as a rebind")
	}
	if detail.Requests[2].Rebind {
		t.Error("same credential must not be marked as a rebind")
	}
	summary := detail.Summary
	if summary.Rebinds != 1 {
		t.Errorf("rebinds = %d, want 1", summary.Rebinds)
	}
	if summary.T0Rebinds != 1 || summary.T0Expiries != 1 {
		t.Errorf("t0 split = %d rebind / %d expiry, want 1/1", summary.T0Rebinds, summary.T0Expiries)
	}
	if got := store.Global().Rebinds; got != 1 {
		t.Errorf("global rebinds = %d, want 1", got)
	}
}

func alertingStore(t *testing.T, threshold int64) *Store {
	t.Helper()
	return NewStore(Config{
		Enabled: true, MaxSessions: 10, PerSessionRequests: 50, IdleTTL: 24 * time.Hour,
		Alert: AlertConfig{Enabled: true, LostTokensPerHour: threshold},
	})
}

func TestLossAlertRaisesAndRearms(t *testing.T) {
	base := time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC)
	store := alertingStore(t, 10000)

	// Build a high-water mark of 20000, then regress twice for 6000 each.
	store.Record(observation("s1", 20000, 0, base))
	store.Record(observation("s1", 14000, 0, base.Add(time.Minute)))

	if summary, _ := store.Session("s1"); summary.Summary.Alerting {
		t.Fatal("alert fired below the threshold")
	}

	store.Record(observation("s1", 14000, 0, base.Add(2*time.Minute)))
	detail, _ := store.Session("s1")
	if !detail.Summary.Alerting {
		t.Fatalf("alert did not fire: window loss = %d, threshold 10000", detail.Summary.LostTokensInWindow)
	}
	if detail.Summary.LostTokensInWindow != 12000 {
		t.Errorf("window loss = %d, want 12000", detail.Summary.LostTokensInWindow)
	}

	// Still above half the threshold an hour later minus a minute: one 6000
	// event has aged out, leaving 6000, which is not below half of 10000.
	store.Record(observation("s1", 20000, 0, base.Add(61*time.Minute)))
	detail, _ = store.Session("s1")
	if !detail.Summary.Alerting {
		t.Error("alert re-armed while the window still held half the threshold")
	}

	// Both events aged out: the window drains and the alert re-arms.
	store.Record(observation("s1", 20000, 0, base.Add(3*time.Hour)))
	detail, _ = store.Session("s1")
	if detail.Summary.Alerting {
		t.Errorf("alert did not re-arm: window loss = %d", detail.Summary.LostTokensInWindow)
	}
	if detail.Summary.LostTokensInWindow != 0 {
		t.Errorf("window loss = %d, want 0 after the window drained", detail.Summary.LostTokensInWindow)
	}
}

func TestLossAlertStaysOffWhenDisabled(t *testing.T) {
	base := time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC)
	store := newTestStore(t, 10, 50, 24*time.Hour)

	store.Record(observation("s1", 90000, 0, base))
	store.Record(observation("s1", 0, 0, base.Add(time.Minute)))

	detail, _ := store.Session("s1")
	if detail.Summary.Alerting {
		t.Fatal("alert fired with the alert disabled")
	}
}

func TestApplyConfigReevaluatesAlert(t *testing.T) {
	base := time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC)
	store := newTestStore(t, 10, 50, 24*time.Hour)
	store.Record(observation("s1", 20000, 0, base))
	store.Record(observation("s1", 5000, 0, base.Add(time.Minute)))

	store.ApplyConfig(Config{
		Enabled: true, MaxSessions: 10, PerSessionRequests: 50, IdleTTL: 24 * time.Hour,
		Alert: AlertConfig{Enabled: true, LostTokensPerHour: 1000},
	})
	detail, _ := store.Session("s1")
	if !detail.Summary.Alerting {
		t.Fatal("lowering the threshold did not raise the alert")
	}

	store.ApplyConfig(Config{
		Enabled: true, MaxSessions: 10, PerSessionRequests: 50, IdleTTL: 24 * time.Hour,
		Alert: AlertConfig{Enabled: false},
	})
	detail, _ = store.Session("s1")
	if detail.Summary.Alerting {
		t.Fatal("disabling the alert did not clear it")
	}
}

func TestSnapshotProviderFilter(t *testing.T) {
	base := time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC)
	store := newTestStore(t, 10, 10, time.Hour)

	store.Record(observation("claude-session", 0, 1000, base))
	store.Record(Observation{
		SessionID: "gemini-session", KeyedBy: KeyedByAPIKeyModel, Provider: "gemini",
		Model: "gemini-3-pro", AuthID: "auth-2", At: base.Add(time.Second), Signal: SignalRead,
		InputTokens: 900, PromptTokens: 900, OutputTokens: 5, CacheReadTokens: 400,
	})

	all := store.Snapshot(Filter{})
	if len(all.Sessions) != 2 {
		t.Fatalf("unfiltered sessions = %d, want 2", len(all.Sessions))
	}
	if len(all.Providers) != 2 {
		t.Fatalf("provider groups = %d, want 2", len(all.Providers))
	}

	filtered := store.Snapshot(Filter{Provider: "GEMINI"})
	if len(filtered.Sessions) != 1 || filtered.Sessions[0].ID != "gemini-session" {
		t.Fatalf("filtered sessions = %+v, want only gemini-session", filtered.Sessions)
	}
	if filtered.Global.Requests != 1 {
		t.Errorf("filtered global requests = %d, want 1", filtered.Global.Requests)
	}
	if len(filtered.Providers) != 1 || filtered.Providers[0].Key != "gemini" {
		t.Errorf("filtered provider groups = %+v, want only gemini", filtered.Providers)
	}

	empty := store.Snapshot(Filter{Provider: "nope"})
	if len(empty.Sessions) != 0 || empty.Global.Requests != 0 {
		t.Errorf("unknown provider filter returned data: %+v", empty)
	}
}

func TestShortIDHashesCompositeKeys(t *testing.T) {
	uuid := "7c9e4b21-0000-4aaa-bbbb-cccccccccccc"
	if got := shortID(uuid); got != "7c9e4b21" {
		t.Errorf("shortID(uuid) = %q, want the first block", got)
	}
	first := shortID("apikey:abcd1234|gpt-x|curl/8.7")
	second := shortID("apikey:abcd1234|gpt-y|curl/8.7")
	if len(first) != 8 || len(second) != 8 {
		t.Fatalf("composite short ids must be 8 chars: %q, %q", first, second)
	}
	if first == second {
		t.Error("composite keys that differ must not share a short id")
	}
	if shortID("apikey:abcd1234|gpt-x|curl/8.7") != first {
		t.Error("shortID must be stable for the same key")
	}
}
