package registry

import "sync/atomic"

// The explicit override and service-owned publication windows are independent.
// A model missing while either is active is retryable startup, not a bad request.
var catalogLoading atomic.Bool
var catalogLoadingOwners atomic.Int64

// SetCatalogLoading sets an explicit loading override. Clearing the override
// does not release loading owned by active service startups.
func SetCatalogLoading(loading bool) {
	catalogLoading.Store(loading)
}

// BeginCatalogLoading owns one service's initial publication window. The
// returned release is idempotent and cannot finish another service's window.
func BeginCatalogLoading() func() {
	catalogLoadingOwners.Add(1)
	var released atomic.Bool
	return func() {
		if released.CompareAndSwap(false, true) {
			catalogLoadingOwners.Add(-1)
		}
	}
}

// CatalogLoading reports whether any initial load or explicit override remains.
func CatalogLoading() bool {
	return catalogLoading.Load() || catalogLoadingOwners.Load() > 0
}
