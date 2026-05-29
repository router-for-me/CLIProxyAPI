package auth

import (
	"testing"
	"time"
)

func TestRateWindowSumsAndExpiry(t *testing.T) {
	var w rateWindow
	base := time.Unix(1_700_000_000, 0)

	w.addRequest(base)
	w.addRequest(base.Add(2 * time.Second))
	w.addTokens(base.Add(2*time.Second), 50)

	reqs, toks := w.sums(base.Add(2 * time.Second))
	if reqs != 2 || toks != 50 {
		t.Fatalf("within window: got reqs=%d toks=%d, want 2/50", reqs, toks)
	}

	// At base+61s the base+2s event is still within the trailing 60s window.
	if reqs, toks := w.sums(base.Add(61 * time.Second)); reqs != 1 || toks != 50 {
		t.Fatalf("at +61s: got reqs=%d toks=%d, want 1/50 (base+2s still in window)", reqs, toks)
	}
	// At base+62s every recorded event has aged out of the window.
	if reqs, toks := w.sums(base.Add(62 * time.Second)); reqs != 0 || toks != 0 {
		t.Fatalf("after expiry: got reqs=%d toks=%d, want 0/0", reqs, toks)
	}
}

func TestRateWindowInFlight(t *testing.T) {
	var w rateWindow
	w.acquire()
	w.acquire()
	if w.inFlight != 2 {
		t.Fatalf("inFlight=%d, want 2", w.inFlight)
	}
	w.release()
	if w.inFlight != 1 {
		t.Fatalf("inFlight=%d, want 1", w.inFlight)
	}
	// release floors at zero and never goes negative.
	w.release()
	w.release()
	if w.inFlight != 0 {
		t.Fatalf("inFlight=%d, want 0 (floored)", w.inFlight)
	}
}

func TestRateLimitedRPMTPMConcurrency(t *testing.T) {
	defer SetRateLimitDefaults(0, 0, 0)
	m := NewManager(nil, nil, nil)
	a := &Auth{ID: "a1", Provider: "claude"}
	m.auths["a1"] = a
	now := time.Unix(1_700_000_000, 0)

	// RPM: limit 2 -> blocked once two requests are recorded in the window.
	SetRateLimitDefaults(2, 0, 0)
	a.rate.addRequest(now)
	if _, limited := m.rateLimited("a1", now); limited {
		t.Fatalf("rpm: should not be limited at 1/2")
	}
	a.rate.addRequest(now)
	if _, limited := m.rateLimited("a1", now); !limited {
		t.Fatalf("rpm: should be limited at 2/2")
	}
	if _, limited := m.rateLimited("a1", now.Add(61*time.Second)); limited {
		t.Fatalf("rpm: should recover after the window expires")
	}

	// TPM: independent window keyed on tokens.
	SetRateLimitDefaults(0, 100, 0)
	a2 := &Auth{ID: "a2", Provider: "claude"}
	m.auths["a2"] = a2
	a2.rate.addTokens(now, 100)
	if _, limited := m.rateLimited("a2", now); !limited {
		t.Fatalf("tpm: should be limited at 100/100")
	}

	// Concurrency: limit 1 -> blocked while one request is in flight.
	SetRateLimitDefaults(0, 0, 1)
	a3 := &Auth{ID: "a3", Provider: "claude"}
	m.auths["a3"] = a3
	a3.rate.acquire()
	if _, limited := m.rateLimited("a3", now); !limited {
		t.Fatalf("concurrency: should be limited at 1 in-flight/1")
	}
	a3.rate.release()
	if _, limited := m.rateLimited("a3", now); limited {
		t.Fatalf("concurrency: should be free after release")
	}
}

func TestRateLimitPerAccountOverride(t *testing.T) {
	defer SetRateLimitDefaults(0, 0, 0)
	// Global RPM disabled; the per-account override must still apply.
	SetRateLimitDefaults(0, 0, 0)
	m := NewManager(nil, nil, nil)
	a := &Auth{ID: "a1", Provider: "claude", Metadata: map[string]any{"rpm_limit": 1}}
	m.auths["a1"] = a
	now := time.Unix(1_700_000_000, 0)

	a.rate.addRequest(now)
	if _, limited := m.rateLimited("a1", now); !limited {
		t.Fatalf("override: should be limited at 1/1 via metadata rpm_limit")
	}
}
