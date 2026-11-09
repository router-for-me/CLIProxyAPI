package watcher

import (
	"context"
	"testing"
)

// Exercise the exact gap after dispatch removes a batch but before publication.
func TestCatalogPendingIncludesRemovedUnpublishedBatch(t *testing.T) {
	w := &Watcher{pendingOrder: []string{"synthetic"}, pendingUpdates: map[string]AuthUpdate{"synthetic": {ID: "synthetic"}}}
	batch, ok := w.nextPendingBatch(context.Background())
	if !ok || len(batch) != 1 {
		t.Fatal("missing synthetic batch")
	}
	if got := w.PendingAuthUpdates(); got != 1 {
		t.Fatalf("pending=%d after removal, before service publication; want 1", got)
	}
	channel := make(chan AuthUpdate, 1)
	channel <- batch[0]
	received := <-channel
	if got := w.PendingAuthUpdates(); got != 1 {
		t.Fatalf("pending=%d after consumer receive, before publication; want 1", got)
	}
	received.MarkApplied()
	batch[0].MarkApplied() // A copied or duplicate acknowledgement is harmless.
	if got := w.PendingAuthUpdates(); got != 0 {
		t.Fatalf("pending=%d after publication; want 0", got)
	}
}
