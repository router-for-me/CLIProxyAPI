package registry

import (
	"context"
	"reflect"
	"testing"
	"testing/synctest"
	"time"
)

func TestModelCatalogUpdaterRetryAndRecovery(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		start := time.Now()
		outcomes := []bool{false, false, true, true, false, true}
		var attempts []time.Duration
		runModelCatalogUpdater(ctx, "test refresh", func(_ context.Context, label string) bool {
			index := len(attempts)
			wantLabel := "periodic test refresh"
			if index == 0 {
				wantLabel = "startup test refresh"
			}
			if label != wantLabel {
				t.Errorf("attempt %d label = %q, want %q", index, label, wantLabel)
			}
			attempts = append(attempts, time.Since(start))
			if len(attempts) == len(outcomes) {
				cancel()
			}
			return outcomes[index]
		})
		want := []time.Duration{
			0,
			30 * time.Second,
			time.Minute,
			3*time.Hour + time.Minute,
			6*time.Hour + time.Minute,
			6*time.Hour + 90*time.Second,
		}
		if !reflect.DeepEqual(attempts, want) {
			t.Fatalf("refresh times = %v, want %v", attempts, want)
		}
	})
}

func TestModelCatalogUpdaterCancellation(t *testing.T) {
	for _, success := range []bool{false, true} {
		name := "retry wait"
		if success {
			name = "periodic wait"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				attempts := 0
				done := make(chan struct{})
				start := time.Now()
				go func() {
					defer close(done)
					runModelCatalogUpdater(ctx, "test refresh", func(context.Context, string) bool {
						attempts++
						return success
					})
				}()
				synctest.Wait()
				cancel()
				<-done
				if attempts != 1 {
					t.Fatalf("refresh attempts = %d, want 1", attempts)
				}
				if elapsed := time.Since(start); elapsed != 0 {
					t.Fatalf("cancellation waited %s for timer, want immediate return", elapsed)
				}
			})
		})
	}
}

func TestModelCatalogUpdaterAlreadyCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	runModelCatalogUpdater(ctx, "test refresh", func(context.Context, string) bool {
		t.Fatal("refresh called after cancellation")
		return false
	})
}
