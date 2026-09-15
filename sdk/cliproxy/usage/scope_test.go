package usage

import (
	"context"
	"sync/atomic"
	"testing"
)

type synchronousTestSink struct{ calls atomic.Int32 }

func (s *synchronousTestSink) Synchronous() bool                   { return true }
func (s *synchronousTestSink) HandleUsage(context.Context, Record) { s.calls.Add(1) }

func TestSynchronousSinkCompletesBeforePublishReturns(t *testing.T) {
	m := NewManager(1)
	sink := &synchronousTestSink{}
	m.Register(sink)
	ctx, scope := WithAccountingScope(context.Background())
	m.Publish(WithNewGeneration(ctx), Record{})
	if sink.calls.Load() != 1 || !scope.Published() {
		t.Fatal("durable sink must complete synchronously")
	}
	m.Stop()
	if sink.calls.Load() != 1 {
		t.Fatal("asynchronous dispatch repeated synchronous sink")
	}
}
func TestGenerationIdentityChangesWithoutLosingCoverageScope(t *testing.T) {
	ctx, scope := WithAccountingScope(context.Background())
	first, second := WithNewGeneration(ctx), WithNewGeneration(ctx)
	if GenerationFromContext(first) == "" || GenerationFromContext(first) == GenerationFromContext(second) {
		t.Fatal("generations must be distinct")
	}
	markPublished(second)
	if !scope.Published() {
		t.Fatal("generation context lost request coverage")
	}
}

type identityReviewSink struct {
	synchronous bool
	records     chan Record
}

func (s *identityReviewSink) Synchronous() bool                       { return s.synchronous }
func (s *identityReviewSink) HandleUsage(_ context.Context, r Record) { s.records <- r }
func TestEventIdentityIsSharedBySynchronousAndAsyncPlugins(t *testing.T) {
	m := NewManager(1)
	defer m.Stop()
	a := &identityReviewSink{synchronous: true, records: make(chan Record, 2)}
	b := &identityReviewSink{records: make(chan Record, 2)}
	m.Register(a)
	m.Register(b)
	for _, provided := range []string{"", "caller-supplied"} {
		m.Publish(context.Background(), Record{EventID: provided})
		syncRecord := <-a.records
		if syncRecord.EventID == "" {
			t.Fatal("manager did not assign an event ID")
		}
		asyncRecord := <-b.records
		if syncRecord.EventID != asyncRecord.EventID {
			t.Fatal("plugins received different IDs")
		}
		if provided != "" && syncRecord.EventID != provided {
			t.Fatal("caller identity was replaced")
		}
	}
}
