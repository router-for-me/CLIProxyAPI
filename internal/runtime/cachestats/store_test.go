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
		Model:               "claude-sonnet-5",
		AuthID:              "auth-1",
		At:                  at,
		InputTokens:         2,
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
