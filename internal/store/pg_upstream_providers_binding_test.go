package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestUpstreamProviderProxyPoolBindingRoundTrip verifies proxy_pool_id
// survives store Create/Get/Update on both the provider row and its entries.
func TestUpstreamProviderProxyPoolBindingRoundTrip(t *testing.T) {
	pg := newTestPostgresStore(t, "upstream_pool_binding")
	defer pg.Close()
	ensureMigrated(t, pg)

	pools := NewProxyPoolStore(pg)
	providers := NewUpstreamProviderStore(pg)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool, err := pools.Create(ctx, ProxyPool{Name: "bind-pool", ProxyURL: "http://1.2.3.4:8080", IsActive: true})
	if err != nil {
		t.Fatalf("pool: %v", err)
	}

	row, err := providers.Create(ctx, UpstreamProvider{
		ProviderType: "claude-api-key",
		Name:         "bind-row",
		APIKey:       "k-row",
		ProxyPoolID:  &pool.ID,
		APIKeyEntries: []UpstreamProviderAPIKey{
			{APIKey: "k-entry", ProxyPoolID: &pool.ID},
			{APIKey: "k-unbound"},
		},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	got, err := providers.Get(ctx, row.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.ProxyPoolID == nil || *got.ProxyPoolID != pool.ID {
		t.Fatalf("row binding = %v, want %d", got.ProxyPoolID, pool.ID)
	}
	if len(got.APIKeyEntries) != 2 {
		t.Fatalf("entries = %d, want 2", len(got.APIKeyEntries))
	}
	if got.APIKeyEntries[0].ProxyPoolID == nil || *got.APIKeyEntries[0].ProxyPoolID != pool.ID {
		t.Fatalf("entry binding = %v, want %d", got.APIKeyEntries[0].ProxyPoolID, pool.ID)
	}
	if got.APIKeyEntries[1].ProxyPoolID != nil {
		t.Fatalf("unbound entry must stay nil, got %v", *got.APIKeyEntries[1].ProxyPoolID)
	}

	// Clearing the row binding persists.
	got.ProxyPoolID = nil
	if _, err := providers.Update(ctx, *got); err != nil {
		t.Fatalf("update: %v", err)
	}
	reloaded, err := providers.Get(ctx, row.ID)
	if err != nil {
		t.Fatalf("re-get: %v", err)
	}
	if reloaded.ProxyPoolID != nil {
		t.Fatalf("row binding must clear, got %v", *reloaded.ProxyPoolID)
	}
	// Entry binding survives the row-level update (entry data untouched).
	if reloaded.APIKeyEntries[0].ProxyPoolID == nil || *reloaded.APIKeyEntries[0].ProxyPoolID != pool.ID {
		t.Fatalf("entry binding lost on row update: %v", reloaded.APIKeyEntries[0].ProxyPoolID)
	}
}

// TestDeletePoolBlockedWhileBound asserts the FK backstop prevents deleting a
// pool still referenced by a provider row.
func TestDeletePoolBlockedWhileBound(t *testing.T) {
	pg := newTestPostgresStore(t, "upstream_pool_binding_fk")
	defer pg.Close()
	ensureMigrated(t, pg)

	pools := NewProxyPoolStore(pg)
	providers := NewUpstreamProviderStore(pg)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool, err := pools.Create(ctx, ProxyPool{Name: "fk-pool", ProxyURL: "http://1.2.3.4:8080", IsActive: true})
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	if _, err := providers.Create(ctx, UpstreamProvider{
		ProviderType: "claude-api-key",
		Name:         "fk-row",
		APIKey:       "k",
		ProxyPoolID:  &pool.ID,
	}); err != nil {
		t.Fatalf("provider: %v", err)
	}

	if err := pools.Delete(ctx, pool.ID); err == nil {
		t.Fatal("FK must block deleting a pool still bound to a provider row")
	}

	// After unbinding, the delete succeeds.
	list, err := providers.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, p := range list {
		p.ProxyPoolID = nil
		if _, err := providers.Update(ctx, p); err != nil {
			t.Fatalf("unbind: %v", err)
		}
	}
	if err := pools.Delete(ctx, pool.ID); err != nil && !errors.Is(err, ErrProxyPoolNotFound) {
		t.Fatalf("delete after unbind: %v", err)
	}
}

// TestBoundEntryCountsRowAndEntryBindings pins the count contract: row-level
// and entry-level bindings sum.
func TestBoundEntryCountsRowAndEntryBindings(t *testing.T) {
	pg := newTestPostgresStore(t, "upstream_pool_bound_count")
	defer pg.Close()
	ensureMigrated(t, pg)

	pools := NewProxyPoolStore(pg)
	providers := NewUpstreamProviderStore(pg)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool, err := pools.Create(ctx, ProxyPool{Name: "count-pool", ProxyURL: "http://1.2.3.4:8080", IsActive: true})
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	if n, err := pools.BoundEntryCount(ctx, pool.ID); err != nil || n != 0 {
		t.Fatalf("fresh pool bound count = %d, %v; want 0", n, err)
	}

	if _, err := providers.Create(ctx, UpstreamProvider{
		ProviderType: "claude-api-key",
		Name:         "count-row",
		APIKey:       "k1",
		ProxyPoolID:  &pool.ID, // row-level binding: +1
		APIKeyEntries: []UpstreamProviderAPIKey{
			{APIKey: "k2", ProxyPoolID: &pool.ID}, // entry-level binding: +1
			{APIKey: "k3"},
		},
	}); err != nil {
		t.Fatalf("provider: %v", err)
	}

	n, err := pools.BoundEntryCount(ctx, pool.ID)
	if err != nil {
		t.Fatalf("bound count: %v", err)
	}
	if n != 2 {
		t.Fatalf("bound count = %d, want 2 (1 row + 1 entry)", n)
	}
}
