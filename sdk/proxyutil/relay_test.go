package proxyutil

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestRelayTransportContract stands up a fake relay worker and asserts the
// x-relay-target/x-relay-path header contract end to end.
func TestRelayTransportContract(t *testing.T) {
	t.Parallel()

	var gotTarget, gotPath, gotAuth, hostSeen string
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotTarget = r.Header.Get("x-relay-target")
		gotPath = r.Header.Get("x-relay-path")
		gotAuth = r.Header.Get("Authorization")
		hostSeen = r.Host
		w.WriteHeader(http.StatusTeapot)
	}))
	defer relay.Close()

	rt, errRelay := NewRelayTransportRelaxed(relay.URL, http.DefaultTransport)
	if errRelay != nil {
		t.Fatalf("relay transport: %v", errRelay)
	}
	req, _ := http.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages?beta=1", nil)
	req.Header.Set("Authorization", "Bearer sk-test")
	resp, errTrip := rt.RoundTrip(req)
	if errTrip != nil {
		t.Fatalf("round trip: %v", errTrip)
	}
	defer resp.Body.Close()
	if gotTarget != "https://api.anthropic.com" {
		t.Fatalf("x-relay-target = %q", gotTarget)
	}
	if gotPath != "/v1/messages?beta=1" {
		t.Fatalf("x-relay-path = %q", gotPath)
	}
	if gotAuth != "Bearer sk-test" {
		t.Fatalf("Authorization must pass through, got %q", gotAuth)
	}
	if hostSeen == "api.anthropic.com" {
		t.Fatal("Host header must be rewritten to the relay base")
	}
	if resp.StatusCode != http.StatusTeapot {
		t.Fatalf("relay response must pass through, got %d", resp.StatusCode)
	}
}

// TestNewRelayTransportEnforcesHTTPSBase pins the production constructor's
// https-only base requirement.
func TestNewRelayTransportEnforcesHTTPSBase(t *testing.T) {
	t.Parallel()

	if _, err := NewRelayTransport("http://relay.example"); err == nil {
		t.Fatal("http base must be rejected")
	}
	if _, err := NewRelayTransport("not a url"); err == nil {
		t.Fatal("malformed base must be rejected")
	}
	if _, err := NewRelayTransport(""); err == nil {
		t.Fatal("empty base must be rejected")
	}
	// A valid https base must construct cleanly (no network I/O at build time).
	if _, err := NewRelayTransport("https://relay.example"); err != nil {
		t.Fatalf("https base rejected: %v", err)
	}
}

// TestRelayTransportRejectsNonHTTPTarget pins the target-scheme guard.
func TestRelayTransportRejectsNonHTTPTarget(t *testing.T) {
	t.Parallel()

	rt, errRelay := NewRelayTransportRelaxed("https://relay.example", http.DefaultTransport)
	if errRelay != nil {
		t.Fatalf("relay transport: %v", errRelay)
	}
	req, _ := http.NewRequest(http.MethodGet, "ftp://api.example/x", nil)
	if _, errTrip := rt.RoundTrip(req); errTrip == nil {
		t.Fatal("non-http(s) target must be rejected")
	}
}

// TestRelayTransportKeepsHeaderOrderSafe ensures the relay headers are set
// even when the caller pre-set something at those keys.
func TestRelayTransportOverwritesCallerRelayHeaders(t *testing.T) {
	t.Parallel()

	var gotTarget string
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotTarget = r.Header.Get("x-relay-target")
		w.WriteHeader(http.StatusOK)
	}))
	defer relay.Close()

	rt, errRelay := NewRelayTransportRelaxed(relay.URL, http.DefaultTransport)
	if errRelay != nil {
		t.Fatalf("relay transport: %v", errRelay)
	}
	req, _ := http.NewRequest(http.MethodGet, "https://api.example.com/", nil)
	req.Header.Set("x-relay-target", "https://spoofed.example")
	resp, errTrip := rt.RoundTrip(req)
	if errTrip != nil {
		t.Fatalf("round trip: %v", errTrip)
	}
	resp.Body.Close()
	if gotTarget != "https://api.example.com" {
		t.Fatalf("caller-supplied relay header must be overwritten, got %q", gotTarget)
	}
	if !strings.HasPrefix(relay.URL, "http") {
		t.Fatal("test relay sanity")
	}
}
