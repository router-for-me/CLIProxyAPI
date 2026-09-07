//go:build !linux

package auth

import "time"

func newRefreshDeadlineTimer(d time.Duration) refreshDeadlineTimer {
	return newGoRefreshDeadlineTimer(d)
}
