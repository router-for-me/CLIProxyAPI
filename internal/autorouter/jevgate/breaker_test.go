package jevgate

import (
	"testing"
	"time"
)

func TestBreakerOpensOnTripAndClosesAfterCooldown(t *testing.T) {
	now := time.Unix(1000, 0)
	b := NewBreaker(5 * time.Minute)
	b.now = func() time.Time { return now }

	if b.Open("r1") {
		t.Fatal("breaker should start closed")
	}
	b.Trip("r1")
	if !b.Open("r1") {
		t.Fatal("breaker should be open after Trip")
	}
	if b.Open("r2") {
		t.Fatal("trip must be per-router")
	}
	now = now.Add(5*time.Minute + time.Second)
	if b.Open("r1") {
		t.Fatal("breaker should close after cooldown")
	}
}

func TestBreakerNilIsSafe(t *testing.T) {
	var b *Breaker
	if b.Open("r") {
		t.Fatal("nil breaker must report closed")
	}
	b.Trip("r") // must not panic
}

func TestNewBreakerDefaultsCooldown(t *testing.T) {
	b := NewBreaker(0)
	if b.cooldown != DefaultBreakerCooldown {
		t.Errorf("cooldown = %v, want %v", b.cooldown, DefaultBreakerCooldown)
	}
}
