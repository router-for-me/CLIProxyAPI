package codex

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRefreshRetryDelayWithJitter_UsesExponentialBackoff(t *testing.T) {
	t.Parallel()

	tests := []struct {
		attempt int
		want    time.Duration
	}{
		{attempt: 1, want: 200 * time.Millisecond},
		{attempt: 2, want: 400 * time.Millisecond},
		{attempt: 3, want: 800 * time.Millisecond},
	}

	for _, tt := range tests {
		if got := refreshRetryDelayWithJitter(tt.attempt, 0); got != tt.want {
			t.Fatalf("refreshRetryDelayWithJitter(%d, 0) = %s, want %s", tt.attempt, got, tt.want)
		}
	}
}

func TestRefreshTokensWithRetry_UsesComputedBackoffSequence(t *testing.T) {
	originalJitter := refreshRetryJitter
	originalWait := refreshRetryWait
	defer func() {
		refreshRetryJitter = originalJitter
		refreshRetryWait = originalWait
	}()

	refreshRetryJitter = func() time.Duration { return 0 }

	var delays []time.Duration
	refreshRetryWait = func(_ context.Context, delay time.Duration) error {
		delays = append(delays, delay)
		return nil
	}

	var calls int32
	auth := &CodexAuth{
		httpClient: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				attempt := atomic.AddInt32(&calls, 1)
				if attempt < 3 {
					return &http.Response{
						StatusCode: http.StatusBadGateway,
						Body:       io.NopCloser(strings.NewReader(`{"error":"temporary"}`)),
						Header:     make(http.Header),
						Request:    req,
					}, nil
				}
				return &http.Response{
					StatusCode: http.StatusOK,
					Body: io.NopCloser(strings.NewReader(
						`{"access_token":"access","refresh_token":"refresh","id_token":"invalid","token_type":"Bearer","expires_in":3600}`,
					)),
					Header:  make(http.Header),
					Request: req,
				}, nil
			}),
		},
	}

	tokenData, err := auth.RefreshTokensWithRetry(context.Background(), "dummy_refresh_token", 3)
	if err != nil {
		t.Fatalf("RefreshTokensWithRetry returned error: %v", err)
	}
	if tokenData == nil {
		t.Fatal("expected token data, got nil")
	}
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Fatalf("refresh calls = %d, want 3", got)
	}
	wantDelays := []time.Duration{200 * time.Millisecond, 400 * time.Millisecond}
	if len(delays) != len(wantDelays) {
		t.Fatalf("delay count = %d, want %d", len(delays), len(wantDelays))
	}
	for i := range wantDelays {
		if delays[i] != wantDelays[i] {
			t.Fatalf("delay[%d] = %s, want %s", i, delays[i], wantDelays[i])
		}
	}
}
