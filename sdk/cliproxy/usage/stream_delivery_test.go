package usage

import (
	"context"
	"errors"
	"io"
	"testing"
)

func TestStreamDeliverySurvivesCancellation(t *testing.T) {
	for _, deliveryErr := range []error{nil, io.ErrClosedPipe} {
		parent, cancel := context.WithCancel(context.Background())
		ctx, finish := WithStreamDelivery(parent)
		if !StreamDeliveryTracked(ctx) {
			t.Fatal("missing delivery tracking")
		}
		cancel()
		result := make(chan error)
		go func() {
			err, tracked := WaitStreamDelivery(ctx)
			if !tracked {
				result <- errors.New("missing delivery tracking")
				return
			}
			result <- err
		}()
		finish(deliveryErr)
		finish(context.Canceled)
		if err := <-result; !errors.Is(err, deliveryErr) {
			t.Fatalf("delivery error = %v, want %v", err, deliveryErr)
		}
	}
}

func TestStreamDeliveryUntracked(t *testing.T) {
	if StreamDeliveryTracked(context.Background()) {
		t.Fatal("unexpected delivery tracking")
	}
	if err, tracked := WaitStreamDelivery(context.Background()); err != nil || tracked {
		t.Fatalf("untracked outcome = %v, %v", err, tracked)
	}
}

func TestStreamDeliveryRequiresProducerSupport(t *testing.T) {
	ctx, finish := WithStreamDelivery(context.Background())
	defer finish(nil)
	if StreamDeliverySupported(ctx) {
		t.Fatal("consumer registration opted in an unsupported producer")
	}
	stop := SupportStreamDelivery(ctx)
	if !StreamDeliverySupported(ctx) {
		t.Fatal("producer support not visible")
	}
	stop()
	stop()
	if StreamDeliverySupported(ctx) {
		t.Fatal("failed attempt leaked support to fallback producer")
	}
	SupportStreamDelivery(context.Background())()
}

func TestWithoutStreamDeliveryPreservesContext(t *testing.T) {
	type valueKey struct{}
	parent, cancel := context.WithCancel(context.WithValue(context.Background(), valueKey{}, "metadata"))
	defer cancel()
	outer, finishOuter := WithStreamDelivery(parent)
	defer finishOuter(context.Canceled)
	stopOuter := SupportStreamDelivery(outer)
	defer stopOuter()
	inner := WithoutStreamDelivery(outer)
	if StreamDeliveryTracked(inner) || StreamDeliverySupported(inner) {
		t.Fatal("internal execution inherited delivery ownership")
	}
	if err, tracked := WaitStreamDelivery(inner); err != nil || tracked {
		t.Fatalf("internal delivery = %v, %v", err, tracked)
	}
	if inner.Value(valueKey{}) != "metadata" {
		t.Error("unrelated context value lost")
	}
	nested, finishNested := WithStreamDelivery(inner)
	finishNested(io.ErrClosedPipe)
	if err, tracked := WaitStreamDelivery(nested); !errors.Is(err, io.ErrClosedPipe) || !tracked {
		t.Fatalf("independent consumer outcome = %v, %v", err, tracked)
	}
	if !StreamDeliverySupported(outer) {
		t.Error("outer producer support lost")
	}
	cancel()
	if inner.Err() != context.Canceled {
		t.Error("caller cancellation lost")
	}
	if WithoutStreamDelivery(nil) != nil {
		t.Error("nil context changed")
	}
}
