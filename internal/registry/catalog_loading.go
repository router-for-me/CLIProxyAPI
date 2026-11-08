package registry

import "sync/atomic"

// catalogLoading is true from service start until the first full client load
// has registered its models. Until then a request for a model the registry does
// not know yet is a startup condition, not a bad request.
var catalogLoading atomic.Bool

// SetCatalogLoading marks the model catalog as loading (true) or loaded (false).
func SetCatalogLoading(loading bool) {
	catalogLoading.Store(loading)
}

// CatalogLoading reports whether the first full client load is still running.
func CatalogLoading() bool {
	return catalogLoading.Load()
}
