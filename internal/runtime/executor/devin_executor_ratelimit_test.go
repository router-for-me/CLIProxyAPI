package executor

import (
	"errors"
	"net/http"
	"testing"
	"time"
)

func TestDevinResetHintFromMessageRelative(t *testing.T) {
	now := time.Date(2026, 10, 8, 18, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		msg  string
		want time.Duration
	}{
		{
			name: "minutes",
			msg:  "Reached free model rate limit. Your limit will reset in 13 minutes (at 18:01 UTC).",
			want: 13 * time.Minute,
		},
		{
			name: "hours and minutes",
			msg:  "Reached free model rate limit. Your limit will reset in 5 hours 30 minutes (at 23:30 UTC).",
			want: 5*time.Hour + 30*time.Minute,
		},
		{
			name: "seconds",
			msg:  "Reached free model rate limit. Your limit will reset in 52 seconds (at 18:01 UTC).",
			want: 52 * time.Second,
		},
		{
			name: "single hour",
			msg:  "Your limit will reset in 1 hour (at 19:00 UTC).",
			want: time.Hour,
		},
		{
			name: "no hint",
			msg:  "Your weekly usage quota has been exhausted.",
			want: 0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := devinResetHintFromMessage(tc.msg, now)
			if tc.want == 0 {
				if got != nil {
					t.Fatalf("expected nil retry hint, got %v", *got)
				}
				return
			}
			if got == nil || *got != tc.want {
				t.Fatalf("expected %v, got %v", tc.want, got)
			}
		})
	}
}

func TestDevinResetHintFromMessageAbsolute(t *testing.T) {
	now := time.Date(2026, 10, 8, 17, 55, 0, 0, time.UTC)
	got := devinResetHintFromMessage("reset at unknown (at 18:01 UTC)", now)
	if got == nil || *got != 6*time.Minute {
		t.Fatalf("expected 6m from absolute hint, got %v", got)
	}

	// Past absolute time rolls over to the next day.
	later := time.Date(2026, 10, 8, 18, 30, 0, 0, time.UTC)
	got = devinResetHintFromMessage("(at 18:01 UTC)", later)
	if got == nil || *got <= 23*time.Hour || *got > 24*time.Hour {
		t.Fatalf("expected ~24h rollover, got %v", got)
	}
}

func TestNewDevinStatusErrorRetryAfter(t *testing.T) {
	body := []byte(`devin upstream error (resource_exhausted): Reached free model rate limit. Your limit will reset in 13 minutes (at 18:01 UTC).`)
	err := newDevinStatusError(http.StatusTooManyRequests, http.Header{}, body)
	if err.retryAfter == nil || *err.retryAfter != 13*time.Minute {
		t.Fatalf("expected 13m retryAfter, got %v", err.retryAfter)
	}
	if err.credentialScoped {
		t.Fatal("model rate limit must not be credential scoped")
	}
}

func TestNewDevinStatusErrorCredentialScoped(t *testing.T) {
	body := []byte(`devin upstream error (failed_precondition): Your weekly usage quota has been exhausted.`)
	err := newDevinStatusError(http.StatusTooManyRequests, http.Header{}, body)
	if !err.credentialScoped {
		t.Fatal("usage quota exhaustion must be credential scoped")
	}
}

func TestNewDevinTrailerStatusError(t *testing.T) {
	err := newDevinTrailerStatusError(http.StatusTooManyRequests,
		errors.New("devin upstream error (resource_exhausted): Reached free model rate limit. Your limit will reset in 9 minutes (at 18:01 UTC)."))
	if err.retryAfter == nil || *err.retryAfter != 9*time.Minute {
		t.Fatalf("expected 9m retryAfter, got %v", err.retryAfter)
	}
}
