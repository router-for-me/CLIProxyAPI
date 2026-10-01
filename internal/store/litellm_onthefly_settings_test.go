package store

import (
	"context"
	"testing"
)

func TestLiteLLMSyncSettingsOnTheFlyRoundTrip(t *testing.T) {
	pg := newTestPostgresStore(t, "test_litellm_onthefly_settings")
	defer pg.Close()
	ensureMigrated(t, pg)
	ctx := context.Background()
	s := NewLiteLLMSyncStore(pg)

	set, err := s.Get(ctx)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if set.OnTheFlyEnabled {
		t.Fatalf("OnTheFlyEnabled default = true; want false")
	}
	if set.OnTheFlyCacheTTLSeconds != LiteLLMOnTheFlyDefaultCacheTTLSeconds {
		t.Fatalf("cache ttl default = %d; want %d", set.OnTheFlyCacheTTLSeconds, LiteLLMOnTheFlyDefaultCacheTTLSeconds)
	}
	if set.OnTheFlyTimeoutMs != LiteLLMOnTheFlyDefaultTimeoutMs {
		t.Fatalf("timeout default = %d; want %d", set.OnTheFlyTimeoutMs, LiteLLMOnTheFlyDefaultTimeoutMs)
	}

	set.OnTheFlyEnabled = true
	set.OnTheFlyCacheTTLSeconds = 120
	set.OnTheFlyTimeoutMs = 2500
	if _, err := s.Upsert(ctx, set, nil); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	got, err := s.Get(ctx)
	if err != nil {
		t.Fatalf("Get after Upsert: %v", err)
	}
	if !got.OnTheFlyEnabled || got.OnTheFlyCacheTTLSeconds != 120 || got.OnTheFlyTimeoutMs != 2500 {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
}
