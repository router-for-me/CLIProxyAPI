package usage

import (
	"context"
	"testing"
)

func TestPublishRecordMarksTrackedRequestSynchronously(t *testing.T) {
	ctx, tracker := WithRequestTracking(context.Background())
	if tracker.Published() {
		t.Fatal("new request tracker is already marked")
	}

	child := context.WithValue(ctx, struct{}{}, "child")
	PublishRecord(child, Record{Provider: "test", Model: "test-model"})

	if !tracker.Published() {
		t.Fatal("request tracker was not marked before PublishRecord returned")
	}
}

func TestRequestTrackerNilSafety(t *testing.T) {
	var tracker *RequestTracker
	if tracker.Published() {
		t.Fatal("nil request tracker reports published")
	}

	ctx, tracker := WithRequestTracking(nil)
	if ctx == nil || tracker == nil {
		t.Fatal("WithRequestTracking(nil) did not provide a context and tracker")
	}
}

func TestInheritRequestTrackingPreservesTrackerAcrossContextParents(t *testing.T) {
	requestCtx, tracker := WithRequestTracking(context.Background())
	executionCtx := InheritRequestTracking(context.WithValue(context.Background(), struct{}{}, "execution"), requestCtx)

	PublishRecord(executionCtx, Record{Provider: "test", Model: "test-model"})
	if !tracker.Published() {
		t.Fatal("inherited execution context did not mark the request tracker")
	}
}

func TestWithRequestTrackingIsIdempotent(t *testing.T) {
	ctx, first := WithRequestTracking(context.Background())
	ctx, second := WithRequestTracking(ctx)
	if first != second {
		t.Fatal("nested request tracking replaced the existing tracker")
	}
	PublishRecord(ctx, Record{Provider: "test", Model: "test-model"})
	if !first.Published() {
		t.Fatal("idempotent request tracker was not marked")
	}
}
