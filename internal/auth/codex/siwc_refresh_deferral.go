package codex

import "errors"

// ErrSIWCRefreshDeferred indicates SIWC renewal should be deferred and retried later.
var ErrSIWCRefreshDeferred = errors.New("siwc refresh deferred")
