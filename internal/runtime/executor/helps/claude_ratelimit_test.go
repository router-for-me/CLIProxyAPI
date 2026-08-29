package helps

import (
	"net/http"
	"strconv"
	"testing"
	"time"
)

func TestParseClaudeRateLimitReset_AllCases(t *testing.T) {
	now := time.Now()

	t.Run("nil headers returns nil", func(t *testing.T) {
		if got := ParseClaudeRateLimitReset(nil, now); got != nil {
			t.Fatalf("expected nil, got %v", got)
		}
	})

	t.Run("empty headers returns nil", func(t *testing.T) {
		h := make(http.Header)
		if got := ParseClaudeRateLimitReset(h, now); got != nil {
			t.Fatalf("expected nil, got %v", got)
		}
	})

	t.Run("retry-after only seconds", func(t *testing.T) {
		h := make(http.Header)
		h.Set("Retry-After", "60")
		got := parseClaudeRateLimitResetWithFuzz(h, now, 0, 0)
		if got == nil {
			t.Fatal("expected non-nil RetryAfter")
		}
		if *got != 60*time.Second {
			t.Fatalf("expected 60s, got %v", *got)
		}
	})

	t.Run("retry-after HTTP date", func(t *testing.T) {
		h := make(http.Header)
		futureTime := now.Add(90 * time.Second).UTC().Truncate(time.Second)
		h.Set("Retry-After", futureTime.Format(http.TimeFormat))
		got := parseClaudeRateLimitResetWithFuzz(h, now, 0, 0)
		if got == nil {
			t.Fatal("expected non-nil RetryAfter")
		}
		if *got < 89*time.Second || *got > 91*time.Second {
			t.Fatalf("expected ~90s, got %v", *got)
		}
	})

	t.Run("5h rejected and 7d allowed with unified reset", func(t *testing.T) {
		h := make(http.Header)
		// Missing Anthropic-Ratelimit-Unified-Status, 5h is rejected, 7d is allowed
		h.Set("Anthropic-Ratelimit-Unified-5h-Status", "rejected")
		h.Set("Anthropic-Ratelimit-Unified-5h-Reset", strconv.FormatInt(now.Add(5*time.Hour).Unix(), 10))
		h.Set("Anthropic-Ratelimit-Unified-7d-Status", "allowed")
		h.Set("Anthropic-Ratelimit-Unified-7d-Reset", strconv.FormatInt(now.Add(7*24*time.Hour).Unix(), 10))
		h.Set("Anthropic-Ratelimit-Unified-Reset", strconv.FormatInt(now.Add(5*time.Hour).Unix(), 10))

		got := parseClaudeRateLimitResetWithFuzz(h, now, 0, 0)
		if got == nil {
			t.Fatal("expected non-nil RetryAfter")
		}
		if *got < 5*time.Hour-5*time.Second || *got > 5*time.Hour+5*time.Second {
			t.Fatalf("expected ~5h, got %v", *got)
		}
	})

	t.Run("7d rejected and 5h allowed", func(t *testing.T) {
		h := make(http.Header)
		h.Set("Anthropic-Ratelimit-Unified-Status", "rejected")
		h.Set("Anthropic-Ratelimit-Unified-5h-Status", "allowed")
		h.Set("Anthropic-Ratelimit-Unified-5h-Reset", strconv.FormatInt(now.Add(5*time.Hour).Unix(), 10))
		h.Set("Anthropic-Ratelimit-Unified-7d-Status", "rejected")
		h.Set("Anthropic-Ratelimit-Unified-7d-Reset", strconv.FormatInt(now.Add(7*24*time.Hour).Unix(), 10))

		got := parseClaudeRateLimitResetWithFuzz(h, now, 0, 0)
		if got == nil {
			t.Fatal("expected non-nil RetryAfter")
		}
		if *got < 7*24*time.Hour-5*time.Second || *got > 7*24*time.Hour+5*time.Second {
			t.Fatalf("expected ~7d, got %v", *got)
		}
	})

	t.Run("both 5h and 7d rejected chooses longest", func(t *testing.T) {
		h := make(http.Header)
		h.Set("Anthropic-Ratelimit-Unified-5h-Status", "rejected")
		h.Set("Anthropic-Ratelimit-Unified-5h-Reset", strconv.FormatInt(now.Add(5*time.Hour).Unix(), 10))
		h.Set("Anthropic-Ratelimit-Unified-7d-Status", "rejected")
		h.Set("Anthropic-Ratelimit-Unified-7d-Reset", strconv.FormatInt(now.Add(7*24*time.Hour).Unix(), 10))

		got := parseClaudeRateLimitResetWithFuzz(h, now, 0, 0)
		if got == nil {
			t.Fatal("expected non-nil RetryAfter")
		}
		if *got < 7*24*time.Hour-5*time.Second || *got > 7*24*time.Hour+5*time.Second {
			t.Fatalf("expected ~7d, got %v", *got)
		}
	})

	t.Run("all allowed returns nil", func(t *testing.T) {
		h := make(http.Header)
		h.Set("Anthropic-Ratelimit-Unified-Status", "allowed")
		h.Set("Anthropic-Ratelimit-Unified-5h-Status", "allowed")
		h.Set("Anthropic-Ratelimit-Unified-5h-Reset", strconv.FormatInt(now.Add(5*time.Hour).Unix(), 10))
		h.Set("Anthropic-Ratelimit-Unified-7d-Status", "allowed")
		h.Set("Anthropic-Ratelimit-Unified-7d-Reset", strconv.FormatInt(now.Add(7*24*time.Hour).Unix(), 10))

		got := ParseClaudeRateLimitReset(h, now)
		if got != nil {
			t.Fatalf("expected nil for allowed status, got %v", got)
		}
	})

	t.Run("fable-only rejection with 7d_oi reset and retry-after uses retry-after only", func(t *testing.T) {
		h := make(http.Header)
		h.Set("Anthropic-Ratelimit-Unified-Status", "rejected")
		h.Set("Anthropic-Ratelimit-Unified-5h-Status", "allowed")
		h.Set("Anthropic-Ratelimit-Unified-7d-Status", "allowed")
		h.Set("Anthropic-Ratelimit-Unified-7d_oi-Status", "rejected")
		h.Set("Anthropic-Ratelimit-Unified-7d_oi-Reset", strconv.FormatInt(now.Add(7*24*time.Hour).Unix(), 10))
		h.Set("Anthropic-Ratelimit-Unified-Reset", strconv.FormatInt(now.Add(7*24*time.Hour).Unix(), 10))
		h.Set("Retry-After", "60")

		got := parseClaudeRateLimitResetWithFuzz(h, now, 0, 0)
		if got == nil {
			t.Fatal("expected non-nil RetryAfter")
		}
		if *got != 60*time.Second {
			t.Fatalf("expected 60s from Retry-After, got %v", *got)
		}
	})

	t.Run("fable-only rejection with 7d_oi reset only returns nil for exponential backoff", func(t *testing.T) {
		h := make(http.Header)
		h.Set("Anthropic-Ratelimit-Unified-Status", "rejected")
		h.Set("Anthropic-Ratelimit-Unified-5h-Status", "allowed")
		h.Set("Anthropic-Ratelimit-Unified-7d-Status", "allowed")
		h.Set("Anthropic-Ratelimit-Unified-7d_oi-Status", "rejected")
		h.Set("Anthropic-Ratelimit-Unified-7d_oi-Reset", strconv.FormatInt(now.Add(7*24*time.Hour).Unix(), 10))
		h.Set("Anthropic-Ratelimit-Unified-Reset", strconv.FormatInt(now.Add(7*24*time.Hour).Unix(), 10))

		got := ParseClaudeRateLimitReset(h, now)
		if got != nil {
			t.Fatalf("expected nil for fable-only rejection without retry-after, got %v", *got)
		}
	})

	t.Run("fable-only rejection with allowed_warning on shared window uses retry-after only", func(t *testing.T) {
		h := make(http.Header)
		h.Set("Anthropic-Ratelimit-Unified-Status", "rejected")
		h.Set("Anthropic-Ratelimit-Unified-5h-Status", "allowed")
		h.Set("Anthropic-Ratelimit-Unified-7d-Status", "allowed_warning")
		h.Set("Anthropic-Ratelimit-Unified-7d_oi-Status", "rejected")
		h.Set("Anthropic-Ratelimit-Unified-7d_oi-Reset", strconv.FormatInt(now.Add(7*24*time.Hour).Unix(), 10))
		h.Set("Anthropic-Ratelimit-Unified-Reset", strconv.FormatInt(now.Add(7*24*time.Hour).Unix(), 10))
		h.Set("Retry-After", "60")

		got := parseClaudeRateLimitResetWithFuzz(h, now, 0, 0)
		if got == nil {
			t.Fatal("expected non-nil RetryAfter")
		}
		if *got != 60*time.Second {
			t.Fatalf("expected 60s from Retry-After, got %v", *got)
		}
	})

	t.Run("missing unified status with allowed_warning shared windows ignores unified reset and uses retry-after", func(t *testing.T) {
		h := make(http.Header)
		// Missing Anthropic-Ratelimit-Unified-Status, shared windows are allowed_warning
		h.Set("Anthropic-Ratelimit-Unified-5h-Status", "allowed_warning")
		h.Set("Anthropic-Ratelimit-Unified-7d-Status", "allowed_warning")
		h.Set("Anthropic-Ratelimit-Unified-Reset", strconv.FormatInt(now.Add(7*24*time.Hour).Unix(), 10))
		h.Set("Retry-After", "60")

		got := parseClaudeRateLimitResetWithFuzz(h, now, 0, 0)
		if got == nil {
			t.Fatal("expected non-nil RetryAfter")
		}
		if *got != 60*time.Second {
			t.Fatalf("expected 60s from Retry-After, got %v", *got)
		}
	})

	t.Run("non-fable combined rejection with 7d_oi reset keeps longer duration", func(t *testing.T) {
		h := make(http.Header)
		h.Set("Anthropic-Ratelimit-Unified-Status", "rejected")
		h.Set("Anthropic-Ratelimit-Unified-5h-Status", "rejected")
		h.Set("Anthropic-Ratelimit-Unified-5h-Reset", strconv.FormatInt(now.Add(5*time.Hour).Unix(), 10))
		h.Set("Anthropic-Ratelimit-Unified-7d-Status", "allowed")
		h.Set("Anthropic-Ratelimit-Unified-7d_oi-Status", "rejected")
		h.Set("Anthropic-Ratelimit-Unified-7d_oi-Reset", strconv.FormatInt(now.Add(7*24*time.Hour).Unix(), 10))

		got := parseClaudeRateLimitResetWithFuzz(h, now, 0, 0)
		if got == nil {
			t.Fatal("expected non-nil RetryAfter")
		}
		if *got < 7*24*time.Hour-5*time.Second || *got > 7*24*time.Hour+5*time.Second {
			t.Fatalf("expected ~7d, got %v", *got)
		}
	})

	t.Run("past timestamp returns nil", func(t *testing.T) {
		h := make(http.Header)
		h.Set("Anthropic-Ratelimit-Unified-5h-Status", "rejected")
		h.Set("Anthropic-Ratelimit-Unified-5h-Reset", strconv.FormatInt(now.Add(-5*time.Hour).Unix(), 10))

		got := ParseClaudeRateLimitReset(h, now)
		if got != nil {
			t.Fatalf("expected nil for past reset, got %v", got)
		}
	})

	t.Run("fuzz is bounded and non-negative", func(t *testing.T) {
		h := make(http.Header)
		h.Set("Retry-After", "100")
		for i := 0; i < 50; i++ {
			got := ParseClaudeRateLimitReset(h, now)
			if got == nil {
				t.Fatal("expected non-nil")
			}
			diff := *got - 100*time.Second
			if diff < 1*time.Second || diff > 30*time.Second {
				t.Fatalf("fuzz %v out of bounds [1s, 30s]", diff)
			}
		}
	})
}

func TestClaudeHeadersIndicateUnifiedRateLimitRejection_AllowedWarning(t *testing.T) {
	tests := []struct {
		name     string
		headers  http.Header
		expected bool
	}{
		{
			name: "both shared windows allowed, 7d_oi rejected is fable-only",
			headers: http.Header{
				"Anthropic-Ratelimit-Unified-Status":       []string{"rejected"},
				"Anthropic-Ratelimit-Unified-5h-Status":    []string{"allowed"},
				"Anthropic-Ratelimit-Unified-7d-Status":    []string{"allowed"},
				"Anthropic-Ratelimit-Unified-7d_oi-Status": []string{"rejected"},
			},
			expected: false,
		},
		{
			name: "7d allowed_warning and 5h allowed with 7d_oi rejected is fable-only",
			headers: http.Header{
				"Anthropic-Ratelimit-Unified-Status":       []string{"rejected"},
				"Anthropic-Ratelimit-Unified-5h-Status":    []string{"allowed"},
				"Anthropic-Ratelimit-Unified-7d-Status":    []string{"allowed_warning"},
				"Anthropic-Ratelimit-Unified-7d_oi-Status": []string{"rejected"},
			},
			expected: false,
		},
		{
			name: "5h allowed_warning and 7d allowed with 7d_oi rejected is fable-only",
			headers: http.Header{
				"Anthropic-Ratelimit-Unified-Status":       []string{"rejected"},
				"Anthropic-Ratelimit-Unified-5h-Status":    []string{"allowed_warning"},
				"Anthropic-Ratelimit-Unified-7d-Status":    []string{"allowed"},
				"Anthropic-Ratelimit-Unified-7d_oi-Status": []string{"rejected"},
			},
			expected: false,
		},
		{
			name: "both shared windows allowed_warning with 7d_oi rejected is fable-only",
			headers: http.Header{
				"Anthropic-Ratelimit-Unified-Status":       []string{"rejected"},
				"Anthropic-Ratelimit-Unified-5h-Status":    []string{"allowed_warning"},
				"Anthropic-Ratelimit-Unified-7d-Status":    []string{"allowed_warning"},
				"Anthropic-Ratelimit-Unified-7d_oi-Status": []string{"rejected"},
			},
			expected: false,
		},
		{
			name: "5h rejected even if 7d allowed_warning is unified rejection",
			headers: http.Header{
				"Anthropic-Ratelimit-Unified-Status":       []string{"rejected"},
				"Anthropic-Ratelimit-Unified-5h-Status":    []string{"rejected"},
				"Anthropic-Ratelimit-Unified-7d-Status":    []string{"allowed_warning"},
				"Anthropic-Ratelimit-Unified-7d_oi-Status": []string{"rejected"},
			},
			expected: true,
		},
		{
			name: "7d rejected even if 5h allowed_warning is unified rejection",
			headers: http.Header{
				"Anthropic-Ratelimit-Unified-Status":       []string{"rejected"},
				"Anthropic-Ratelimit-Unified-5h-Status":    []string{"allowed_warning"},
				"Anthropic-Ratelimit-Unified-7d-Status":    []string{"rejected"},
				"Anthropic-Ratelimit-Unified-7d_oi-Status": []string{"rejected"},
			},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ClaudeHeadersIndicateUnifiedRateLimitRejection(tt.headers)
			if got != tt.expected {
				t.Fatalf("ClaudeHeadersIndicateUnifiedRateLimitRejection() = %v, want %v", got, tt.expected)
			}
		})
	}
}
