package auth

import "errors"

// ErrRefreshDeferred indicates token renewal should be deferred and retried later.
var ErrRefreshDeferred = errors.New("refresh deferred")
