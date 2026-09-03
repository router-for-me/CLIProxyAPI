package usage

import (
	"testing"
	"time"
)

func TestTokensPerSecondExcludesTTFT(t *testing.T) {
	got := TokensPerSecond(200, 5*time.Second, 3*time.Second)
	if got != 100 {
		t.Fatalf("TokensPerSecond() = %v, want 100", got)
	}
}

func TestTokensPerSecondOmittedWithoutTTFT(t *testing.T) {
	if TokensPerSecond(200, 5*time.Second, 0) != 0 {
		t.Fatal("unknown TTFT must not invent tok/s from tokens/total_latency")
	}
	if TokensPerSecond(0, 5*time.Second, 1*time.Second) != 0 {
		t.Fatal("zero output tokens must omit tok/s")
	}
	if TokensPerSecond(200, 3*time.Second, 3*time.Second) != 0 {
		t.Fatal("non-positive generation window must omit tok/s")
	}
}
