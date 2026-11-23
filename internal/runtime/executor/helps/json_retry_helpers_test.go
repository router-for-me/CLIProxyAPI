package helps

import (
	"math"
	"net/http"
	"testing"
	"time"
)

func TestParseRetryDelay_Issue7565Formats(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		want    time.Duration
		wantErr bool
	}{
		{
			name: "issue 7565 google rpc RetryInfo 2190s",
			body: `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"2190s"}]}}`,
			want: 2190 * time.Second,
		},
		{
			name: "issue 7565 message Please retry in fractional duration",
			body: `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","message":"Resource has been exhausted. Please retry in 36m41.110635822s."}}`,
			want: 36*time.Minute + 41110635822*time.Nanosecond,
		},
		{
			name: "message with earlier word after still finds retry in duration",
			body: `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","message":"Quota exceeded after multiple requests. Please retry in 36m41.110635822s."}}`,
			want: 36*time.Minute + 41110635822*time.Nanosecond,
		},
		{
			name: "uppercase duration units in message are accepted",
			body: `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","message":"Quota will reset after 1H2M3S."}}`,
			want: time.Hour + 2*time.Minute + 3*time.Second,
		},
		{
			name: "uppercase retry in duration units are accepted",
			body: `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","message":"Please retry in 36M41.5S."}}`,
			want: 36*time.Minute + 41*time.Second + 500*time.Millisecond,
		},
		{
			name: "array wrapped google rpc RetryInfo",
			body: `[{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"2190s"}]}}]`,
			want: 2190 * time.Second,
		},
		{
			name: "malformed RetryInfo falls back to ErrorInfo quotaResetDelay",
			body: `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"not-a-duration"},{"@type":"type.googleapis.com/google.rpc.ErrorInfo","metadata":{"quotaResetDelay":"120s"}}]}}`,
			want: 120 * time.Second,
		},
		{
			name: "zero RetryInfo preserved for legacy ParseRetryDelay callers",
			body: `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"0s"}]}}`,
			want: 0,
		},
		{
			name: "zero quotaResetDelay preserved for legacy ParseRetryDelay callers",
			body: `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","metadata":{"quotaResetDelay":"0s"}}]}}`,
			want: 0,
		},
		{
			name: "zero message duration preserved for legacy ParseRetryDelay callers",
			body: `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","message":"Your quota will reset after 0s."}}`,
			want: 0,
		},
		{
			name:    "negative RetryInfo without fallback fails",
			body:    `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"-5s"}]}}`,
			wantErr: true,
		},
		{
			name:    "word starting with unit letter after number does not false-match",
			body:    `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","message":"Failed after 3models or retry in 2steps."}}`,
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, errParse := ParseRetryDelay([]byte(tc.body))
			if tc.wantErr {
				if errParse == nil && got != nil {
					t.Fatalf("ParseRetryDelay() = %v, nil; want error", *got)
				}
				return
			}
			if errParse != nil {
				t.Fatalf("ParseRetryDelay() error = %v", errParse)
			}
			if got == nil || *got != tc.want {
				t.Fatalf("ParseRetryDelay() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestParseGoogleRetryAfter_PrecedenceAndHeaders(t *testing.T) {
	now := time.Date(2026, 11, 15, 15, 8, 53, 0, time.UTC)
	futureHTTPDate := now.Add(2190 * time.Second).UTC().Format(http.TimeFormat)
	pastHTTPDate := now.Add(-10 * time.Second).UTC().Format(http.TimeFormat)

	wantDuration := func(d time.Duration) *time.Duration {
		return &d
	}

	tests := []struct {
		name    string
		status  int
		headers http.Header
		body    string
		want    *time.Duration
	}{
		{
			name:    "non-429 ignores Retry-After and RetryInfo",
			status:  http.StatusServiceUnavailable,
			headers: http.Header{"Retry-After": []string{"2190"}},
			body:    `{"error":{"code":503,"details":[{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"2190s"}]}}`,
			want:    nil,
		},
		{
			name:    "structured RetryInfo wins over Retry-After header and message",
			status:  http.StatusTooManyRequests,
			headers: http.Header{"Retry-After": []string{"60"}},
			body:    `{"error":{"code":429,"message":"Please retry in 10s.","details":[{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"2190s"}]}}`,
			want:    wantDuration(2190 * time.Second),
		},
		{
			name:    "zero RetryInfo falls back to positive Retry-After header",
			status:  http.StatusTooManyRequests,
			headers: http.Header{"Retry-After": []string{"2190"}},
			body:    `{"error":{"code":429,"details":[{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"0s"}]}}`,
			want:    wantDuration(2190 * time.Second),
		},
		{
			name:    "zero RetryInfo alone yields nil for Gemini",
			status:  http.StatusTooManyRequests,
			headers: nil,
			body:    `{"error":{"code":429,"details":[{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"0s"}]}}`,
			want:    nil,
		},
		{
			name:    "Retry-After header wins over unstructured message text",
			status:  http.StatusTooManyRequests,
			headers: http.Header{"Retry-After": []string{"2190"}},
			body:    `{"error":{"code":429,"message":"Please retry in 36m41.110635822s."}}`,
			want:    wantDuration(2190 * time.Second),
		},
		{
			name:    "lowercase retry-after header key honored",
			status:  http.StatusTooManyRequests,
			headers: http.Header{"retry-after": []string{"2190"}},
			body:    `{"error":{"code":429}}`,
			want:    wantDuration(2190 * time.Second),
		},
		{
			name:    "future HTTP-date Retry-After honored",
			status:  http.StatusTooManyRequests,
			headers: http.Header{"Retry-After": []string{futureHTTPDate}},
			body:    `{"error":{"code":429}}`,
			want:    wantDuration(2190 * time.Second),
		},
		{
			name:    "zero Retry-After without body hint yields nil",
			status:  http.StatusTooManyRequests,
			headers: http.Header{"Retry-After": []string{"0"}},
			body:    `{"error":{"code":429}}`,
			want:    nil,
		},
		{
			name:    "past HTTP-date Retry-After falls back to uppercase message duration",
			status:  http.StatusTooManyRequests,
			headers: http.Header{"Retry-After": []string{pastHTTPDate}},
			body:    `{"error":{"code":429,"message":"Please retry in 1M30S."}}`,
			want:    wantDuration(90 * time.Second),
		},
		{
			name:    "invalid Retry-After yields nil when body has no hint",
			status:  http.StatusTooManyRequests,
			headers: http.Header{"Retry-After": []string{"soon"}},
			body:    `{"error":{"code":429,"message":"Rate limited."}}`,
			want:    nil,
		},
		{
			name:    "huge Retry-After seconds clamps to MaxInt64",
			status:  http.StatusTooManyRequests,
			headers: http.Header{"Retry-After": []string{"18446744074"}},
			body:    `{"error":{"code":429}}`,
			want:    wantDuration(time.Duration(math.MaxInt64)),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseGoogleRetryAfter(tc.status, tc.headers, []byte(tc.body), now)
			if tc.want == nil {
				if got != nil {
					t.Fatalf("ParseGoogleRetryAfter() = %v, want nil", *got)
				}
				return
			}
			if got == nil || *got != *tc.want {
				t.Fatalf("ParseGoogleRetryAfter() = %v, want %v", got, *tc.want)
			}
		})
	}
}

func TestParseRetryDelayWithHeaders_FallbackAndPrecedence(t *testing.T) {
	now := time.Date(2026, 11, 21, 10, 30, 0, 0, time.UTC)
	futureHTTPDate := now.Add(45 * time.Second).UTC().Format(http.TimeFormat)
	pastHTTPDate := now.Add(-15 * time.Second).UTC().Format(http.TimeFormat)

	tests := []struct {
		name    string
		headers http.Header
		body    string
		want    time.Duration
		wantErr bool
	}{
		{
			name:    "body RetryInfo wins over Retry-After header",
			headers: http.Header{"Retry-After": []string{"45"}},
			body:    `{"error":{"code":429,"details":[{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"12s"}]}}`,
			want:    12 * time.Second,
		},
		{
			name:    "body zero RetryInfo preserved over Retry-After header",
			headers: http.Header{"Retry-After": []string{"45"}},
			body:    `{"error":{"code":429,"details":[{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"0s"}]}}`,
			want:    0,
		},
		{
			name:    "body message duration wins over Retry-After header",
			headers: http.Header{"Retry-After": []string{"45"}},
			body:    `{"error":{"code":429,"message":"Your quota will reset after 1h43m56s."}}`,
			want:    time.Hour + 43*time.Minute + 56*time.Second,
		},
		{
			name:    "delta-seconds Retry-After fallback when body lacks delay",
			headers: http.Header{"Retry-After": []string{"45"}},
			body:    `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"RATE_LIMIT_EXCEEDED"}]}}`,
			want:    45 * time.Second,
		},
		{
			name:    "lowercase retry-after header key honored on empty body",
			headers: http.Header{"retry-after": []string{"30"}},
			body:    "",
			want:    30 * time.Second,
		},
		{
			name:    "future HTTP-date Retry-After fallback",
			headers: http.Header{"Retry-After": []string{futureHTTPDate}},
			body:    `{"error":{"code":429,"message":"Too many requests."}}`,
			want:    45 * time.Second,
		},
		{
			name:    "huge Retry-After seconds clamps to MaxInt64",
			headers: http.Header{"Retry-After": []string{"18446744074"}},
			body:    `{"error":{"code":429}}`,
			want:    time.Duration(math.MaxInt64),
		},
		{
			name:    "zero Retry-After preserved for Antigravity when body has no delay",
			headers: http.Header{"Retry-After": []string{"0"}},
			body:    `{"error":{"code":429}}`,
			want:    0,
		},
		{
			name:    "negative Retry-After yields error when body has no delay",
			headers: http.Header{"Retry-After": []string{"-10"}},
			body:    `{"error":{"code":429}}`,
			wantErr: true,
		},
		{
			name:    "past HTTP-date Retry-After yields error when body has no delay",
			headers: http.Header{"Retry-After": []string{pastHTTPDate}},
			body:    `{"error":{"code":429}}`,
			wantErr: true,
		},
		{
			name:    "malformed Retry-After yields error when body has no delay",
			headers: http.Header{"Retry-After": []string{"soon"}},
			body:    `{"error":{"code":429}}`,
			wantErr: true,
		},
		{
			name:    "nil headers and body without hint yields error",
			headers: nil,
			body:    `{"error":{"code":429}}`,
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, errParse := ParseRetryDelayWithHeaders(tc.headers, []byte(tc.body), now)
			if tc.wantErr {
				if errParse == nil {
					t.Fatalf("ParseRetryDelayWithHeaders() error = nil (got=%v), want error", got)
				}
				if got != nil {
					t.Fatalf("ParseRetryDelayWithHeaders() = %v, err=%v; want nil duration", *got, errParse)
				}
				return
			}
			if errParse != nil {
				t.Fatalf("ParseRetryDelayWithHeaders() error = %v", errParse)
			}
			if got == nil || *got != tc.want {
				t.Fatalf("ParseRetryDelayWithHeaders() = %v, want %v", got, tc.want)
			}
		})
	}
}
