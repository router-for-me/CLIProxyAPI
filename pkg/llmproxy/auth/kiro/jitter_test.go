package kiro

import (
	"testing"
	"time"
)

func TestRandomDelay(t *testing.T) {
	min := 100 * time.Millisecond
	max := 200 * time.Millisecond
	for i := 0; i < 100; i++ {
		d := RandomDelay(min, max)
		if d < min || d > max {
			t.Errorf("delay %v out of range [%v, %v]", d, min, max)
		}
	}

	if RandomDelay(max, min) != max {
		t.Error("expected min when min >= max")
	}
}

func TestJitterDelay(t *testing.T) {
	base := 1 * time.Second
	for i := 0; i < 100; i++ {
		d := JitterDelay(base, 0.3)
		if d < 700*time.Millisecond || d > 1300*time.Millisecond {
			t.Errorf("jitter delay %v out of range for base %v", d, base)
		}
	}

	d := JitterDelay(base, -1)
	if d < 0 {
		t.Errorf("jitterPercent -1 should use default, got %v", d)
	}
}

func TestJitterDelayDefault(t *testing.T) {
	d := JitterDelayDefault(1 * time.Second)
	if d < 700*time.Millisecond || d > 1300*time.Millisecond {
		t.Errorf("default jitter failed: %v", d)
	}
}

func TestHumanLikeDelay(t *testing.T) {
	ResetLastRequestTime()
	d1 := HumanLikeDelay()
	if d1 <= 0 {
		t.Error("expected positive delay")
	}

	// Rapid consecutive
	d2 := HumanLikeDelay()
	if d2 < ShortDelayMin || d2 > ShortDelayMax {
		t.Errorf("rapid consecutive delay %v out of range [%v, %v]", d2, ShortDelayMin, ShortDelayMax)
	}
}

func TestExponentialBackoffWithJitter(t *testing.T) {
	base := 1 * time.Second
	max := 10 * time.Second

	d := ExponentialBackoffWithJitter(0, base, max)
	if d < 700*time.Millisecond || d > 1300*time.Millisecond {
		t.Errorf("attempt 0 failed: %v", d)
	}

	d = ExponentialBackoffWithJitter(5, base, max) // 1s * 32 = 32s -> capped to 10s
	if d < 7*time.Second || d > 13*time.Second {
		t.Errorf("attempt 5 failed: %v", d)
	}
}

func TestShouldSkipDelay(t *testing.T) {
	if !ShouldSkipDelay(true) {
		t.Error("should skip for streaming")
	}
	if ShouldSkipDelay(false) {
		t.Error("should not skip for non-streaming")
	}
}
