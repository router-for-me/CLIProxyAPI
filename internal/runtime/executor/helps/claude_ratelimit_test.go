package helps

import (
	"net/http"
	"testing"
	"time"
)

func TestClaudeRatelimitParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)

	cases := []struct {
		name    string
		headers map[string][]string
		want    *time.Duration
		wantMin time.Duration // used when want is nil and a range is required
		wantMax time.Duration
		ranged  bool
	}{
		{
			name:    "seconds",
			headers: map[string][]string{"Retry-After": {"12"}},
			want:    durationPtr(12 * time.Second),
		},
		{
			name: "http-date",
			headers: map[string][]string{
				"Retry-After": {now.Add(45 * time.Second).UTC().Format(http.TimeFormat)},
			},
			ranged:  true,
			wantMin: 40 * time.Second,
			wantMax: 50 * time.Second,
		},
		{
			name:    "ms fallback",
			headers: map[string][]string{"Retry-After-Ms": {"1500"}},
			want:    durationPtr(1500 * time.Millisecond),
		},
		{
			name: "seconds wins over ms",
			headers: map[string][]string{
				"Retry-After":    {"12"},
				"Retry-After-Ms": {"1500"},
			},
			want: durationPtr(12 * time.Second),
		},
		{
			name:    "garbage value no fallback",
			headers: map[string][]string{"Retry-After": {"soon"}},
			want:    nil,
		},
		{
			name:    "empty headers",
			headers: map[string][]string{},
			want:    nil,
		},
		{
			name:    "zero seconds is no hint",
			headers: map[string][]string{"Retry-After": {"0"}},
			want:    nil,
		},
		{
			name:    "negative seconds is no hint",
			headers: map[string][]string{"Retry-After": {"-3"}},
			want:    nil,
		},
		{
			name:    "negative ms is no hint",
			headers: map[string][]string{"Retry-After-Ms": {"-100"}},
			want:    nil,
		},
		{
			name:    "raw lowercase map write still parses",
			headers: map[string][]string{"retry-after": {"12"}},
			want:    durationPtr(12 * time.Second),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := http.Header(tc.headers)
			got := ParseClaudeRetryAfterHeaders(h, now)
			switch {
			case tc.ranged:
				if got == nil {
					t.Fatalf("expected a duration between %v and %v, got nil", tc.wantMin, tc.wantMax)
				}
				if *got < tc.wantMin || *got > tc.wantMax {
					t.Fatalf("expected duration in [%v, %v], got %v", tc.wantMin, tc.wantMax, *got)
				}
			case tc.want == nil:
				if got != nil {
					t.Fatalf("expected nil, got %v", *got)
				}
			default:
				if got == nil {
					t.Fatalf("expected %v, got nil", *tc.want)
				}
				if *got != *tc.want {
					t.Fatalf("expected %v, got %v", *tc.want, *got)
				}
			}
		})
	}
}

func TestClaudeOverageOnlyRejection(t *testing.T) {
	unifiedHealthy := map[string][]string{
		"anthropic-ratelimit-unified-status":    {"allowed"},
		"anthropic-ratelimit-unified-5h-status": {"allowed"},
		"anthropic-ratelimit-unified-7d-status": {"allowed"},
	}

	withOverage := func(v string) map[string][]string {
		m := map[string][]string{}
		for k, vs := range unifiedHealthy {
			m[k] = vs
		}
		m["anthropic-ratelimit-unified-overage-status"] = []string{v}
		return m
	}

	cases := []struct {
		name    string
		headers map[string][]string
		want    bool
	}{
		{
			name:    "overage rejected, regular windows healthy",
			headers: withOverage("rejected"),
			want:    true,
		},
		{
			name: "overage rejected, 5h also rejected is shared rejection",
			headers: func() map[string][]string {
				m := withOverage("rejected")
				m["anthropic-ratelimit-unified-5h-status"] = []string{"rejected"}
				return m
			}(),
			want: false,
		},
		{
			name: "disabled-reason with healthy utilization",
			headers: map[string][]string{
				"anthropic-ratelimit-unified-overage-disabled-reason": {"spend_cap"},
				"anthropic-ratelimit-unified-5h-utilization":          {"0.0"},
				"anthropic-ratelimit-unified-7d-utilization":          {"0.0"},
			},
			want: true,
		},
		{
			name: "disabled-reason with saturated window is conservative false",
			headers: map[string][]string{
				"anthropic-ratelimit-unified-overage-disabled-reason": {"spend_cap"},
				"anthropic-ratelimit-unified-5h-utilization":          {"1.0"},
			},
			want: false,
		},
		{
			// The healthy-utilization condition applies only to utilization
			// headers that ARE present; with none present it is vacuously true.
			name: "disabled-reason without utilization headers",
			headers: map[string][]string{
				"anthropic-ratelimit-unified-overage-disabled-reason": {"spend_cap"},
			},
			want: true,
		},
		{
			name:    "no overage headers at all",
			headers: map[string][]string{},
			want:    false,
		},
		{
			name:    "overage allowed",
			headers: withOverage("allowed"),
			want:    false,
		},
		{
			name:    "overage allowed_warning",
			headers: withOverage("allowed_warning"),
			want:    false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := http.Header(tc.headers)
			if got := IsClaudeOverageOnlyRejection(h); got != tc.want {
				t.Fatalf("expected %v, got %v", tc.want, got)
			}
		})
	}
}

func TestClaudeSharedWindowRejected(t *testing.T) {
	cases := []struct {
		name    string
		headers map[string][]string
		want    bool
	}{
		{
			name:    "unified rejected",
			headers: map[string][]string{"anthropic-ratelimit-unified-status": {"rejected"}},
			want:    true,
		},
		{
			name:    "5h rejected",
			headers: map[string][]string{"anthropic-ratelimit-unified-5h-status": {"rejected"}},
			want:    true,
		},
		{
			name:    "7d rejected",
			headers: map[string][]string{"anthropic-ratelimit-unified-7d-status": {"rejected"}},
			want:    true,
		},
		{
			name: "all allowed or warning or absent",
			headers: map[string][]string{
				"anthropic-ratelimit-unified-status":    {"allowed"},
				"anthropic-ratelimit-unified-5h-status": {"allowed_warning"},
			},
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := http.Header(tc.headers)
			if got := ClaudeSharedWindowRejected(h); got != tc.want {
				t.Fatalf("expected %v, got %v", tc.want, got)
			}
		})
	}
}

func durationPtr(d time.Duration) *time.Duration {
	return &d
}
