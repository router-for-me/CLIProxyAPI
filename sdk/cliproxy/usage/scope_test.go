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
