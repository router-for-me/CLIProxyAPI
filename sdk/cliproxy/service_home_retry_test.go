package cliproxy

import (
	"context"
	"testing"
	"testing/synctest"
	"time"
)

func TestHomeSubscriberRetryBackoffGrowthAndJitter(t *testing.T) {
	for _, random := range []float64{0, 0.5, 0.999999} {
		var backoff homeSubscriberRetryBackoff
		for index, ceiling := range []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 30 * time.Second, 30 * time.Second} {
			delay := backoff.next(0, false, random)
			want := ceiling/2 + time.Duration(random*float64(ceiling/2))
			if delay != want || delay < ceiling/2 || delay > homeSubscriberRetryMaximum {
				t.Fatalf("attempt %d, random %v: delay = %s, want %s", index+1, random, delay, want)
			}
		}
	}
}

func TestHomeSubscriberRetryBackoffResetsOnlyAfterStability(t *testing.T) {
	var backoff homeSubscriberRetryBackoff
	backoff.next(0, false, 0)
	if delay := backoff.next(homeSubscriberStablePeriod-time.Nanosecond, false, 0); delay != time.Second {
		t.Fatalf("short-lived published subscription reset backoff: %s", delay)
	}
	if delay := backoff.next(0, true, 0); delay != 0 {
		t.Fatalf("planned rebalance was delayed: %s", delay)
	}
	if delay := backoff.next(0, false, 0); delay != 2*time.Second {
		t.Fatalf("planned rebalance discarded prior failures: %s", delay)
	}
	if delay := backoff.next(homeSubscriberStablePeriod, false, 0); delay != homeSubscriberRetryInitial/2 {
		t.Fatalf("stable subscription did not reset backoff: %s", delay)
	}
	backoff.next(0, false, 0)
	backoff.next(homeSubscriberStablePeriod, true, 0)
	if delay := backoff.next(0, false, 0); delay != homeSubscriberRetryInitial/2 {
		t.Fatalf("stable planned rebalance did not reset backoff: %s", delay)
	}
}

func TestHomeSubscriberRetryWaitIsCancellable(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan bool, 1)
		go func() { done <- waitForHomeSubscriberRetry(ctx, homeSubscriberRetryMaximum) }()
		synctest.Wait()
		cancel()
		if <-done {
			t.Fatal("canceled wait requested another retry")
		}
	})
	synctest.Test(t, func(t *testing.T) {
		started := time.Now()
		if !waitForHomeSubscriberRetry(context.Background(), homeSubscriberRetryInitial) {
			t.Fatal("completed wait stopped retries")
		}
		if elapsed := time.Since(started); elapsed != homeSubscriberRetryInitial {
			t.Fatalf("wait = %s, want %s", elapsed, homeSubscriberRetryInitial)
		}
	})
}
