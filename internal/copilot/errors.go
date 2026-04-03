package copilot

import "errors"

var (
	ErrNoToken               = errors.New("no GitHub token stored for this account")
	ErrTokenRevoked          = errors.New("GitHub token has been revoked (401)")
	ErrNoCopilotSubscription = errors.New("account does not have a Copilot subscription")
	ErrRateLimited           = errors.New("GitHub API rate limit exceeded")
	ErrAPIUnavailable        = errors.New("GitHub API is unavailable")
)
