package auth

import (
	"net/http"
	"testing"
	"time"
)

func TestModelCooldownError_ConfigurableStatusCode(t *testing.T) {
	t.Cleanup(func() { SetModelCooldownStatusCode(0) })

	if got := newModelCooldownError("m", "p", time.Minute).StatusCode(); got != http.StatusTooManyRequests {
		t.Fatalf("default status = %d, want 429", got)
	}

	SetModelCooldownStatusCode(http.StatusServiceUnavailable)
	if got := newModelCooldownError("m", "p", time.Minute).StatusCode(); got != http.StatusServiceUnavailable {
		t.Fatalf("configured status = %d, want 529", got)
	}

	SetModelCooldownStatusCode(599)
	if got := newModelCooldownError("m", "p", time.Minute).StatusCode(); got != 599 {
		t.Fatalf("status = %d, want 599", got)
	}

	SetModelCooldownStatusCode(399)
	if got := newModelCooldownError("m", "p", time.Minute).StatusCode(); got != http.StatusTooManyRequests {
		t.Fatalf("out-of-range status = %d, want 429 fallback", got)
	}

	SetModelCooldownStatusCode(600)
	if got := newModelCooldownError("m", "p", time.Minute).StatusCode(); got != http.StatusTooManyRequests {
		t.Fatalf("out-of-range status = %d, want 429 fallback", got)
	}
}
