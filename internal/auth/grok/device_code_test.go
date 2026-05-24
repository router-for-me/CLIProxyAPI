package grok

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// newTestGrokAuth returns a GrokAuth that uses the provided http.Client (or
// http.DefaultClient if nil). Used by all device-code tests to avoid real network calls.
func newTestGrokAuth(client *http.Client) *GrokAuth {
	if client == nil {
		client = http.DefaultClient
	}
	return &GrokAuth{httpClient: client}
}

// --------------------------------------------------------------------------
// RequestDeviceCode tests
// --------------------------------------------------------------------------

func TestRequestDeviceCode_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(DeviceCodeResponse{
			DeviceCode:      "dev-code-abc",
			UserCode:        "ABCD-1234",
			VerificationURI: "https://auth.x.ai/device",
			ExpiresIn:       300,
			Interval:        5,
		})
	}))
	defer srv.Close()

	// Point DeviceAuthURL at the test server by temporarily patching the request
	// via a transport that rewrites the host.
	g := newTestGrokAuth(srv.Client())

	// We need to call a version that hits our fake server. Override by crafting
	// the request manually via an inner helper that accepts a base URL.
	dc, err := requestDeviceCodeAt(context.Background(), g, srv.URL+"/device/code")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dc.DeviceCode != "dev-code-abc" {
		t.Errorf("DeviceCode = %q, want %q", dc.DeviceCode, "dev-code-abc")
	}
	if dc.UserCode != "ABCD-1234" {
		t.Errorf("UserCode = %q, want %q", dc.UserCode, "ABCD-1234")
	}
	if dc.VerificationURI != "https://auth.x.ai/device" {
		t.Errorf("VerificationURI = %q, want %q", dc.VerificationURI, "https://auth.x.ai/device")
	}
	if dc.ExpiresIn != 300 {
		t.Errorf("ExpiresIn = %d, want 300", dc.ExpiresIn)
	}
}

func TestRequestDeviceCode_MissingFields(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		// Return empty device_code to trigger validation error.
		_ = json.NewEncoder(w).Encode(DeviceCodeResponse{
			UserCode:        "ABCD-1234",
			VerificationURI: "https://auth.x.ai/device",
		})
	}))
	defer srv.Close()

	g := newTestGrokAuth(srv.Client())
	_, err := requestDeviceCodeAt(context.Background(), g, srv.URL+"/device/code")
	if err == nil {
		t.Fatal("expected error for missing device_code field, got nil")
	}
}

// --------------------------------------------------------------------------
// PollDeviceCodeToken tests
// --------------------------------------------------------------------------

// fakePollServer returns a handler that serves responses from the provided
// sequence. Each call to the handler pops one response from the front of the
// slice. After all responses are consumed, it returns 500.
func fakePollServer(responses []struct {
	status int
	body   interface{}
}) http.Handler {
	var idx int32
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		i := int(atomic.AddInt32(&idx, 1)) - 1
		if i >= len(responses) {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(responses[i].status)
		_ = json.NewEncoder(w).Encode(responses[i].body)
	})
}

// makeSyncClock returns a Now func and an advanceClock func that are safe to
// use from a single goroutine. The clock starts at a fixed time.
func makeSyncClock(start time.Time) (now func() time.Time, advance func(time.Duration)) {
	current := start
	return func() time.Time { return current },
		func(d time.Duration) { current = current.Add(d) }
}

// noSleep is a sleep func that returns immediately without actually waiting.
func noSleep(_ context.Context, _ time.Duration) error { return nil }

// recordingSleep records each duration it is called with.
type recordingSleep struct {
	durations []time.Duration
}

func (s *recordingSleep) Sleep(_ context.Context, d time.Duration) error {
	s.durations = append(s.durations, d)
	return nil
}

func TestPollDeviceCodeToken_HandlesAuthorizationPending(t *testing.T) {
	type errBody struct {
		Error string `json:"error"`
	}
	responses := []struct {
		status int
		body   interface{}
	}{
		{http.StatusBadRequest, errBody{"authorization_pending"}},
		{http.StatusBadRequest, errBody{"authorization_pending"}},
		{http.StatusOK, TokenResponse{AccessToken: "tok-xyz", TokenType: "Bearer"}},
	}

	srv := httptest.NewServer(fakePollServer(responses))
	defer srv.Close()

	g := newTestGrokAuth(srv.Client())

	device := &DeviceCodeResponse{
		DeviceCode:      "dev-code-123",
		UserCode:        "USER-CODE",
		VerificationURI: "https://auth.x.ai/device",
		ExpiresIn:       300,
		Interval:        5,
	}

	rs := &recordingSleep{}
	start := time.Now()
	now, _ := makeSyncClock(start)

	tok, err := pollDeviceCodeTokenAt(context.Background(), g, device, DeviceCodePollOptions{
		Sleep: rs.Sleep,
		Now:   now,
	}, srv.URL+"/token")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tok.AccessToken != "tok-xyz" {
		t.Errorf("AccessToken = %q, want %q", tok.AccessToken, "tok-xyz")
	}

	// Sleep should have been called exactly twice (once per authorization_pending).
	if len(rs.durations) != 2 {
		t.Errorf("sleep called %d times, want 2", len(rs.durations))
	}
	// Each sleep should be interval(5s) + safety_margin(3s) = 8s.
	expected := 5*time.Second + DeviceCodePollSafetyMargin
	for i, d := range rs.durations {
		if d != expected {
			t.Errorf("sleep[%d] = %v, want %v", i, d, expected)
		}
	}
}

func TestPollDeviceCodeToken_HandlesSlowDown(t *testing.T) {
	type errBody struct {
		Error string `json:"error"`
	}
	responses := []struct {
		status int
		body   interface{}
	}{
		{http.StatusBadRequest, errBody{"slow_down"}},
		{http.StatusOK, TokenResponse{AccessToken: "tok-slow", TokenType: "Bearer"}},
	}

	srv := httptest.NewServer(fakePollServer(responses))
	defer srv.Close()

	g := newTestGrokAuth(srv.Client())

	device := &DeviceCodeResponse{
		DeviceCode:      "dev-code-slow",
		UserCode:        "USER-CODE",
		VerificationURI: "https://auth.x.ai/device",
		ExpiresIn:       300,
		Interval:        5, // initial interval = 5s
	}

	rs := &recordingSleep{}
	start := time.Now()
	now, _ := makeSyncClock(start)

	tok, err := pollDeviceCodeTokenAt(context.Background(), g, device, DeviceCodePollOptions{
		Sleep: rs.Sleep,
		Now:   now,
	}, srv.URL+"/token")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tok.AccessToken != "tok-slow" {
		t.Errorf("AccessToken = %q, want %q", tok.AccessToken, "tok-slow")
	}

	// Sleep should have been called once (for the slow_down response).
	if len(rs.durations) != 1 {
		t.Errorf("sleep called %d times, want 1", len(rs.durations))
	}
	// After slow_down: interval bumped from 5s to 10s, sleep = 10s + 3s = 13s.
	expected := 10*time.Second + DeviceCodePollSafetyMargin
	if rs.durations[0] != expected {
		t.Errorf("sleep[0] = %v, want %v", rs.durations[0], expected)
	}
}

func TestPollDeviceCodeToken_AccessDenied(t *testing.T) {
	type errBody struct {
		Error string `json:"error"`
	}
	responses := []struct {
		status int
		body   interface{}
	}{
		{http.StatusBadRequest, errBody{"access_denied"}},
	}

	srv := httptest.NewServer(fakePollServer(responses))
	defer srv.Close()

	g := newTestGrokAuth(srv.Client())
	device := &DeviceCodeResponse{
		DeviceCode:      "dev-code",
		UserCode:        "USER",
		VerificationURI: "https://auth.x.ai/device",
		ExpiresIn:       300,
		Interval:        5,
	}

	start := time.Now()
	now, _ := makeSyncClock(start)

	_, err := pollDeviceCodeTokenAt(context.Background(), g, device, DeviceCodePollOptions{
		Sleep: noSleep,
		Now:   now,
	}, srv.URL+"/token")
	if !errors.Is(err, ErrDeviceAuthDenied) {
		t.Errorf("err = %v, want ErrDeviceAuthDenied", err)
	}
}

func TestPollDeviceCodeToken_ExpiredToken(t *testing.T) {
	type errBody struct {
		Error string `json:"error"`
	}
	responses := []struct {
		status int
		body   interface{}
	}{
		{http.StatusBadRequest, errBody{"expired_token"}},
	}

	srv := httptest.NewServer(fakePollServer(responses))
	defer srv.Close()

	g := newTestGrokAuth(srv.Client())
	device := &DeviceCodeResponse{
		DeviceCode:      "dev-code",
		UserCode:        "USER",
		VerificationURI: "https://auth.x.ai/device",
		ExpiresIn:       300,
		Interval:        5,
	}

	start := time.Now()
	now, _ := makeSyncClock(start)

	_, err := pollDeviceCodeTokenAt(context.Background(), g, device, DeviceCodePollOptions{
		Sleep: noSleep,
		Now:   now,
	}, srv.URL+"/token")
	if !errors.Is(err, ErrDeviceCodeExpired) {
		t.Errorf("err = %v, want ErrDeviceCodeExpired", err)
	}
}

func TestPollDeviceCodeToken_DeadlineReached(t *testing.T) {
	type errBody struct {
		Error string `json:"error"`
	}
	// Server always returns authorization_pending.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(errBody{"authorization_pending"})
	}))
	defer srv.Close()

	g := newTestGrokAuth(srv.Client())
	device := &DeviceCodeResponse{
		DeviceCode:      "dev-code",
		UserCode:        "USER",
		VerificationURI: "https://auth.x.ai/device",
		ExpiresIn:       10, // 10s deadline
		Interval:        5,
	}

	start := time.Now()
	now, advance := makeSyncClock(start)

	// Sleep func advances the clock past the deadline on first call.
	sleepAndAdvance := func(_ context.Context, d time.Duration) error {
		advance(15 * time.Second) // push past the 10s deadline
		return nil
	}

	_, err := pollDeviceCodeTokenAt(context.Background(), g, device, DeviceCodePollOptions{
		Sleep: sleepAndAdvance,
		Now:   now,
	}, srv.URL+"/token")
	if !errors.Is(err, ErrDeviceCodeTimeout) {
		t.Errorf("err = %v, want ErrDeviceCodeTimeout", err)
	}
}
