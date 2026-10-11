package claudemaster

import (
	"context"
	"math/rand/v2"
	"time"
)

// backendPollJitterFraction spreads every periodic upstream poll by up to this fraction either side of
// its interval, so polls that start together (two colors during a rollout, five accounts on one box)
// drift apart instead of landing on the same instant every round.
const backendPollJitterFraction = 0.2

// backendPollJitter is d moved by a random amount of at most fraction*d either way.
func backendPollJitter(d time.Duration, fraction float64) time.Duration {
	if d <= 0 || fraction <= 0 {
		return d
	}
	return d + time.Duration((rand.Float64()*2-1)*fraction*float64(d))
}

// jitteredTicks sends on the returned channel once per interval, each wait drawn afresh by
// backendPollJitter, until ctx ends. Unlike time.Ticker it never settles into a fixed phase.
func jitteredTicks(ctx context.Context, interval time.Duration, fraction float64) <-chan time.Time {
	ticks := make(chan time.Time)
	go func() {
		timer := time.NewTimer(backendPollJitter(interval, fraction))
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-timer.C:
				select {
				case ticks <- now:
				case <-ctx.Done():
					return
				}
				timer.Reset(backendPollJitter(interval, fraction))
			}
		}
	}()
	return ticks
}

// backendQuotaPollPhase is account i's fixed delay after each round's tick: the n accounts are spaced
// evenly across the first half of the interval, so a round is n requests apart rather than one burst,
// and one account's polls stay a whole (jittered) interval apart, round after round.
func backendQuotaPollPhase(i, n int, interval time.Duration) time.Duration {
	if n <= 1 || i <= 0 {
		return 0
	}
	return time.Duration(i) * (interval / 2) / time.Duration(n)
}

// sleepContext waits d, or returns false at once when ctx ends first.
func sleepContext(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
