package usage

import (
	"context"
	"testing"
	"time"
)

type usagePluginFunc func(context.Context, Record)

func (f usagePluginFunc) HandleUsage(ctx context.Context, record Record) {
	f(ctx, record)
}

func TestGenerateEnabledDefaultsNilToTrue(t *testing.T) {
	if !GenerateEnabled(nil) {
		t.Fatalf("GenerateEnabled(nil) = false, want true")
	}
}

func TestGenerateEnabledHonorsExplicitFalse(t *testing.T) {
	if GenerateEnabled(GenerateFlag(false)) {
		t.Fatalf("GenerateEnabled(false) = true, want false")
	}
}

func TestGenerateEnabledHonorsExplicitTrue(t *testing.T) {
	if !GenerateEnabled(GenerateFlag(true)) {
		t.Fatalf("GenerateEnabled(true) = false, want true")
	}
}

func TestGenerateFromContextDefaultsMissingToTrue(t *testing.T) {
	if !GenerateFromContext(context.Background()) {
		t.Fatalf("GenerateFromContext(background) = false, want true")
	}
}

func TestGenerateFromContextHonorsExplicitFalse(t *testing.T) {
	ctx := WithGenerate(context.Background(), false)
	if GenerateFromContext(ctx) {
		t.Fatalf("GenerateFromContext(false) = true, want false")
	}
}

func TestRecordOmittedGenerateIsEnabled(t *testing.T) {
	// Existing callers construct Record without setting Generate.
	// Omission must remain distinguishable from explicit false and default to true.
	record := Record{
		Provider: "openai",
		Model:    "gpt-5.4",
	}
	if record.Generate != nil {
		t.Fatalf("Record.Generate = %v, want nil for omitted field", record.Generate)
	}
	if !GenerateEnabled(record.Generate) {
		t.Fatalf("GenerateEnabled(omitted) = false, want true")
	}
}

func TestManagerPublishPreservesValuesAfterRequestCancellation(t *testing.T) {
	manager := NewManager(1)
	t.Cleanup(manager.Stop)

	started := make(chan struct{})
	release := make(chan struct{})
	manager.Register(usagePluginFunc(func(context.Context, Record) {
		close(started)
		<-release
	}))

	type contextKey struct{}
	type delivery struct {
		value string
		err   error
	}
	delivered := make(chan delivery, 1)
	manager.Register(usagePluginFunc(func(ctx context.Context, _ Record) {
		delivered <- delivery{value: ctx.Value(contextKey{}).(string), err: ctx.Err()}
	}))

	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), contextKey{}, "request-value"))
	manager.Publish(ctx, Record{Model: "test-model"})
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first usage plugin was not invoked")
	}
	cancel()
	close(release)

	select {
	case got := <-delivered:
		if got.err != nil || got.value != "request-value" {
			t.Fatalf("delivery = %+v, want preserved value and active context", got)
		}
	case <-time.After(time.Second):
		t.Fatal("second usage plugin was not invoked")
	}
}
