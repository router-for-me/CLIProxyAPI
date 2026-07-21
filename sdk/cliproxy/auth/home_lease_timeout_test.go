package auth

import (
	"context"
	"testing"
)

type leaseReleaseContextDispatcher struct {
	hasDeadline chan bool
}

func (d *leaseReleaseContextDispatcher) RenewInFlightLease(context.Context, string) (bool, error) {
	return true, nil
}

func (d *leaseReleaseContextDispatcher) ReleaseInFlightLease(ctx context.Context, _ string, _ string) (bool, error) {
	_, hasDeadline := ctx.Deadline()
	d.hasDeadline <- hasDeadline
	return true, nil
}

func TestHomeLeaseReleaseDoesNotSetAttemptDeadline(t *testing.T) {
	dispatcher := &leaseReleaseContextDispatcher{hasDeadline: make(chan bool, 1)}
	releases := newHomeLeaseReleaseQueue()
	releases.releaseWithRetry(homeLeaseReleaseRequest{client: dispatcher, leaseID: "lease-no-timeout"})

	if hasDeadline := <-dispatcher.hasDeadline; hasDeadline {
		t.Fatal("Home lease release context must not impose a per-attempt deadline")
	}
}
