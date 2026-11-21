package registry

import "sync/atomic"

var liveModelListRefresh atomic.Pointer[func()]

// SetLiveModelListRefreshHook registers the function that refreshes the per-login live model
// lists (see CatalogSources.LiveLists). The service sets it; nil removes it.
func SetLiveModelListRefreshHook(fn func()) {
	if fn == nil {
		liveModelListRefresh.Store(nil)
		return
	}
	liveModelListRefresh.Store(&fn)
}

// RequestLiveModelListRefresh asks the service to fetch every login's live model list again,
// e.g. after an on-demand catalog refresh. It never blocks.
func RequestLiveModelListRefresh() {
	if fn := liveModelListRefresh.Load(); fn != nil {
		(*fn)()
	}
}
