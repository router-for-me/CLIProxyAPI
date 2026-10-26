package cliproxy

import "time"

const (
	homeSubscriberRetryInitial = time.Second
	homeSubscriberRetryMaximum = 30 * time.Second
	homeSubscriberStablePeriod = 30 * time.Second
)

// homeSubscriberRetryBackoff belongs to the supervisor, not a replaceable client.
// Short-lived successful subscriptions and Home switches must not reset failures.
type homeSubscriberRetryBackoff struct {
	delay time.Duration
}

func (b *homeSubscriberRetryBackoff) next(healthyFor time.Duration, plannedRebalance bool, random float64) time.Duration {
	if healthyFor >= homeSubscriberStablePeriod {
		b.delay = 0
	}
	if plannedRebalance {
		return 0
	}
	if b.delay == 0 {
		b.delay = homeSubscriberRetryInitial
	} else {
		b.delay = min(b.delay*2, homeSubscriberRetryMaximum)
	}
	// Equal jitter keeps a nonzero quiet period even when failures are immediate.
	return b.delay/2 + time.Duration(random*float64(b.delay/2))
}
