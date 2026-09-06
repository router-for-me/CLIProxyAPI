package store

import (
	"context"
	"errors"
	"testing"
)

// newTestProxyPoolStore opens a PostgresStore against PGSTORE_TEST_DSN and
// returns the proxy-pool store. Skips without the DSN like the other PG tests.
func newTestProxyPoolStore(t *testing.T) ProxyPoolStore {
	t.Helper()
	pg := newTestPostgresStore(t, "test_proxy_pools_store")
	t.Cleanup(func() { pg.Close() })
	ctx := context.Background()
	if err := pg.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	st := NewProxyPoolStore(pg)
	if st == nil {
		t.Fatal("nil proxy pool store")
	}
	return st
}

func TestProxyPoolCRUDRoundTrip(t *testing.T) {
	st := newTestProxyPoolStore(t)
	ctx := context.Background()

	created, err := st.Create(ctx, ProxyPool{
		Name:        "pool-a",
		ProxyURL:    "socks5://u:p@10.0.0.1:1080",
		NoProxy:     ".corp",
		Type:        "http",
		IsActive:    true, // store writes what it is given; the handler defaults new pools to active
		StrictProxy: true,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.ID == 0 || created.TestStatus != "unknown" || !created.IsActive {
		t.Fatalf("created = %+v", created)
	}
	if created.CreatedAt.IsZero() || created.UpdatedAt.IsZero() {
		t.Fatalf("timestamps not set: %+v", created)
	}

	got, err := st.Get(ctx, created.ID)
	if err != nil || got.Name != "pool-a" || got.ProxyURL != "socks5://u:p@10.0.0.1:1080" || got.NoProxy != ".corp" {
		t.Fatalf("get: %+v %v", got, err)
	}

	got.NoProxy = ""
	got.IsActive = false
	updated, err := st.Update(ctx, *got)
	if err != nil || updated.IsActive {
		t.Fatalf("update: %+v %v", updated, err)
	}

	list, err := st.List(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("list: %d pools, %v", len(list), err)
	}

	if err := st.Delete(ctx, created.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := st.Get(ctx, created.ID); !errors.Is(err, ErrProxyPoolNotFound) {
		t.Fatalf("post-delete err = %v, want ErrProxyPoolNotFound", err)
	}
}

func TestProxyPoolGetMissingReturnsNotFound(t *testing.T) {
	st := newTestProxyPoolStore(t)
	if _, err := st.Get(context.Background(), 987654); !errors.Is(err, ErrProxyPoolNotFound) {
		t.Fatalf("err = %v, want ErrProxyPoolNotFound", err)
	}
}

func TestProxyPoolDuplicateNameRejected(t *testing.T) {
	st := newTestProxyPoolStore(t)
	ctx := context.Background()
	if _, err := st.Create(ctx, ProxyPool{Name: "dup", ProxyURL: "http://1.2.3.4:8080"}); err != nil {
		t.Fatalf("first create: %v", err)
	}
	// Case-insensitive uniqueness (the index is on lower(name)).
	if _, err := st.Create(ctx, ProxyPool{Name: "DUP", ProxyURL: "http://5.6.7.8:8080"}); !errors.Is(err, ErrProxyPoolDuplicateName) {
		t.Fatalf("err = %v, want ErrProxyPoolDuplicateName", err)
	}
}

func TestProxyPoolUpdateMissingReturnsNotFound(t *testing.T) {
	st := newTestProxyPoolStore(t)
	if _, err := st.Update(context.Background(), ProxyPool{ID: 987654, Name: "x", ProxyURL: "http://1:1"}); !errors.Is(err, ErrProxyPoolNotFound) {
		t.Fatalf("err = %v, want ErrProxyPoolNotFound", err)
	}
}

func TestProxyPoolDeleteMissingReturnsNotFound(t *testing.T) {
	st := newTestProxyPoolStore(t)
	if err := st.Delete(context.Background(), 987654); !errors.Is(err, ErrProxyPoolNotFound) {
		t.Fatalf("err = %v, want ErrProxyPoolNotFound", err)
	}
}
