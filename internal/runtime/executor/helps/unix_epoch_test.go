package helps

import (
	"testing"
	"time"
)

func TestUnixSecondsOrMilli(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)

	t.Run("seconds epoch", func(t *testing.T) {
		got := UnixSecondsOrMilli(now.Unix())
		if !got.Equal(now) {
			t.Fatalf("got %v, want %v", got, now)
		}
	})

	t.Run("millisecond epoch", func(t *testing.T) {
		want := now.Add(5 * time.Minute)
		got := UnixSecondsOrMilli(want.UnixMilli())
		if got.Sub(want).Abs() > time.Millisecond {
			t.Fatalf("got %v, want %v", got, want)
		}
	})

	t.Run("non-positive", func(t *testing.T) {
		if !UnixSecondsOrMilli(0).IsZero() {
			t.Fatal("expected zero time for 0")
		}
		if !UnixSecondsOrMilli(-1).IsZero() {
			t.Fatal("expected zero time for -1")
		}
	})
}
