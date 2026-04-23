package mongostate

import (
	"context"
	"errors"
	"testing"
)

func TestGetFailureCountsWithRetry_RetriesOnceAndReturnsSuccess(t *testing.T) {
	firstErr := errors.New("first read failed")
	attempts := 0

	counts, err := getFailureCountsWithRetry(context.Background(), "model-a", func(_ context.Context, model string) (map[string]int, error) {
		attempts++
		if model != "model-a" {
			t.Fatalf("model = %q, want model-a", model)
		}
		if attempts == 1 {
			return nil, firstErr
		}
		return map[string]int{"auth-b": 2}, nil
	})
	if err != nil {
		t.Fatalf("getFailureCountsWithRetry() error = %v, want nil", err)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
	if counts["auth-b"] != 2 {
		t.Fatalf("counts[auth-b] = %d, want 2", counts["auth-b"])
	}
}

func TestGetFailureCountsWithRetry_ReturnsSecondErrorAfterRetry(t *testing.T) {
	firstErr := errors.New("first read failed")
	secondErr := errors.New("second read failed")
	attempts := 0

	_, err := getFailureCountsWithRetry(context.Background(), "model-a", func(context.Context, string) (map[string]int, error) {
		attempts++
		if attempts == 1 {
			return nil, firstErr
		}
		return nil, secondErr
	})
	if !errors.Is(err, secondErr) {
		t.Fatalf("getFailureCountsWithRetry() error = %v, want %v", err, secondErr)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
}

func TestGetFailureCountsWithRetry_DoesNotRetryWhenContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	readErr := errors.New("read failed")
	attempts := 0

	_, err := getFailureCountsWithRetry(ctx, "model-a", func(context.Context, string) (map[string]int, error) {
		attempts++
		cancel()
		return nil, readErr
	})
	if !errors.Is(err, readErr) {
		t.Fatalf("getFailureCountsWithRetry() error = %v, want %v", err, readErr)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}
}
