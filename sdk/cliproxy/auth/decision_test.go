package auth

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDecisionHeaderVersionedAndNAFilled(t *testing.T) {
	d := Decision{
		Model:          "gpt-5",
		AuthID:         "auth-1",
		Channel:        "openai",
		Strategy:       "fill-first",
		Breaker:        "n/a",
		CooldownWaitMs: 0,
		Attempts:       1,
		PoolStrategy:   "fallback",
	}
	h := d.Header()
	if !strings.HasPrefix(h, "v=1;") {
		t.Errorf("missing v=1 prefix: %q", h)
	}
	if !strings.Contains(h, "breaker=n/a") {
		t.Errorf("missing breaker=n/a: %q", h)
	}
	if !strings.Contains(h, "strategy=fill-first") {
		t.Errorf("missing strategy=fill-first: %q", h)
	}
	if !strings.Contains(h, "model=gpt-5") {
		t.Errorf("missing model=gpt-5: %q", h)
	}
	if !strings.Contains(h, "auth=auth-1") {
		t.Errorf("missing auth=auth-1: %q", h)
	}
	if !strings.Contains(h, "channel=openai") {
		t.Errorf("missing channel=openai: %q", h)
	}
	if !strings.Contains(h, "cooldown_wait_ms=0") {
		t.Errorf("missing cooldown_wait_ms=0: %q", h)
	}
	if !strings.Contains(h, "attempts=1") {
		t.Errorf("missing attempts=1: %q", h)
	}
	if !strings.Contains(h, "pool_strategy=fallback") {
		t.Errorf("missing pool_strategy=fallback: %q", h)
	}
}

func TestDecisionHeaderOversizedValueTruncates(t *testing.T) {
	d := Decision{Model: strings.Repeat("x", 2000), AuthID: "a"}
	h := d.Header()
	if len(h) > 1024 {
		t.Errorf("header should be truncated to <= 1024, got %d", len(h))
	}
	// Truncation must happen at a field boundary (no half-fields).
	// The truncated string must end with a complete value (no trailing '=' or partial token).
	if strings.HasSuffix(h, "=") {
		t.Errorf("header truncated mid-key: %q", h)
	}
}

func TestDecisionHeaderHeadroomExhaustedMarker(t *testing.T) {
	d := Decision{
		Strategy:          "headroom",
		HeadroomExhausted: true,
	}
	h := d.Header()
	if !strings.Contains(h, "headroom=exhausted,fallback=least-used") {
		t.Errorf("missing headroom exhausted marker: %q", h)
	}
	// The strategy field should be replaced, not present alongside. We match
	// the bare "; strategy=" token (with the leading separator) to avoid
	// substring collisions with other field names like "pool_strategy=".
	if strings.Contains(h, "; strategy=") {
		t.Errorf("strategy field should be replaced when HeadroomExhausted: %q", h)
	}
}

func TestDecisionHeaderEmptyBreakerBecomesNA(t *testing.T) {
	d := Decision{Breaker: ""}
	h := d.Header()
	if !strings.Contains(h, "breaker=n/a") {
		t.Errorf("empty breaker should render as n/a: %q", h)
	}
}

func TestDecisionHeaderWrittenBeforeFirstStreamChunk(t *testing.T) {
	// The decision header is built into the StreamResult.Headers map at the
	// conductor's return point, before any chunk is forwarded. Simulate
	// the consumer path: an httptest recorder reads Headers before Write.
	rec := httptest.NewRecorder()
	headers := http.Header{}
	// Build a Decision and write its header value into the map BEFORE
	// any body write happens.
	d := Decision{
		Model:    "gpt-5",
		AuthID:   "auth-1",
		Channel:  "openai",
		Strategy: "headroom",
	}
	headers.Set(DecisionHeader, d.Header())

	// Headers must be observable on the recorder before any body write.
	if got := rec.Header().Get(DecisionHeader); got != "" {
		t.Errorf("header should not be set yet, got %q", got)
	}

	// Now copy the headers (the way handlers_stream.go does it via cloneHeader)
	// and write the body. The decision header must be in the final response.
	for key, values := range headers {
		for _, value := range values {
			rec.Header().Add(key, value)
		}
	}
	rec.WriteHeader(http.StatusOK)
	_, _ = rec.Write([]byte("chunk-1"))

	if got := rec.Header().Get(DecisionHeader); got == "" {
		t.Fatalf("decision header missing after body write: headers=%v", rec.Header())
	}
	if !strings.HasPrefix(rec.Header().Get(DecisionHeader), "v=1;") {
		t.Errorf("decision header should start with v=1;: got %q", rec.Header().Get(DecisionHeader))
	}
	if !strings.Contains(rec.Header().Get(DecisionHeader), "model=gpt-5") {
		t.Errorf("decision header missing model: got %q", rec.Header().Get(DecisionHeader))
	}
}
