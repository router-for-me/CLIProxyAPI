package usage

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
)

type deferredFailureRecorder struct {
	authPrefix string
	records    chan Record
	closed     atomic.Bool
}

func (r *deferredFailureRecorder) HandleUsage(_ context.Context, record Record) {
	if r.closed.Load() {
		return
	}
	if strings.HasPrefix(record.AuthID, r.authPrefix) {
		r.records <- record
	}
}

func newDeferredFailureRecorder(t *testing.T) *deferredFailureRecorder {
	t.Helper()
	recorder := &deferredFailureRecorder{
		authPrefix: "usage-deferred-" + uuid.NewString(),
		records:    make(chan Record, 8),
	}
	RegisterPlugin(recorder)
	t.Cleanup(func() { recorder.closed.Store(true) })
	return recorder
}

func readDeferredFailureRecord(t *testing.T, recorder *deferredFailureRecorder) Record {
	t.Helper()
	select {
	case record := <-recorder.records:
		return record
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for usage record")
		return Record{}
	}
}

func assertNoDeferredFailureRecord(t *testing.T, recorder *deferredFailureRecorder) {
	t.Helper()
	select {
	case record := <-recorder.records:
		t.Fatalf("unexpected usage record: auth=%s failed=%v", record.AuthID, record.Failed)
	case <-time.After(150 * time.Millisecond):
	}
}

func TestPublishRecordOrDeferDropsFailedRecordOnSuccess(t *testing.T) {
	recorder := newDeferredFailureRecorder(t)
	ctx, finish := WithDeferredFailures(context.Background())

	PublishRecordOrDefer(ctx, Record{AuthID: recorder.authPrefix + "-failed", Failed: true})
	finish(false)

	assertNoDeferredFailureRecord(t, recorder)
}

func TestPublishRecordOrDeferFlushesLatestFailedRecordOnFailure(t *testing.T) {
	recorder := newDeferredFailureRecorder(t)
	ctx, finish := WithDeferredFailures(context.Background())

	PublishRecordOrDefer(ctx, Record{AuthID: recorder.authPrefix + "-first", Failed: true})
	PublishRecordOrDefer(ctx, Record{AuthID: recorder.authPrefix + "-last", Failed: true})
	finish(true)
	finish(true)

	record := readDeferredFailureRecord(t, recorder)
	if record.AuthID != recorder.authPrefix+"-last" {
		t.Fatalf("flushed auth = %s, want latest failed record", record.AuthID)
	}
	if !record.Failed {
		t.Fatal("expected failed record")
	}
	assertNoDeferredFailureRecord(t, recorder)
}

func TestPublishRecordOrDeferPublishesSuccessImmediately(t *testing.T) {
	recorder := newDeferredFailureRecorder(t)
	ctx, finish := WithDeferredFailures(context.Background())

	PublishRecordOrDefer(ctx, Record{AuthID: recorder.authPrefix + "-success", Failed: false})

	record := readDeferredFailureRecord(t, recorder)
	if record.AuthID != recorder.authPrefix+"-success" {
		t.Fatalf("published auth = %s, want success record", record.AuthID)
	}
	if record.Failed {
		t.Fatal("expected success record")
	}
	finish(false)
	assertNoDeferredFailureRecord(t, recorder)
}
