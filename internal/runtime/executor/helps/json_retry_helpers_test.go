package helps

import (
	"testing"
	"time"
)

func TestParseRetryDelay_Formats(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		want      time.Duration
		expectErr bool
	}{
		{
			name: "google errorinfo metadata camelCase quotaResetDelay",
			body: `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","metadata":{"quotaResetDelay":"479.417207ms"}}]}}`,
			want: 479417207 * time.Nanosecond,
		},
		{
			name: "google errorinfo metadata snake_case quota_reset_delay",
			body: `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","metadata":{"quota_reset_delay":"166h8m44s"}}]}}`,
			want: 166*time.Hour + 8*time.Minute + 44*time.Second,
		},
		{
			name: "google retryinfo retryDelay",
			body: `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"0.5s"}]}}`,
			want: 500 * time.Millisecond,
		},
		{
			name: "message with resets in human duration",
			body: `{"error":{"code":429,"message":"Individual quota reached. Please upgrade your subscription to increase your limits. Resets in 166h27m18s.","status":"RESOURCE_EXHAUSTED"}}`,
			want: 166*time.Hour + 27*time.Minute + 18*time.Second,
		},
		{
			name: "message with resets in seconds",
			body: `{"error":{"code":429,"message":"Quota exceeded. Resets in 45s.","status":"RESOURCE_EXHAUSTED"}}`,
			want: 45 * time.Second,
		},
		{
			name: "message with after human duration",
			body: `{"error":{"code":429,"message":"You have exhausted your capacity on this model. Your quota will reset after 1h43m56s.","status":"RESOURCE_EXHAUSTED"}}`,
			want: 1*time.Hour + 43*time.Minute + 56*time.Second,
		},
		{
			name: "message with after seconds",
			body: `{"error":{"code":429,"message":"Your quota will reset after 30s.","status":"RESOURCE_EXHAUSTED"}}`,
			want: 30 * time.Second,
		},
		{
			name:      "unrelated error body returns error",
			body:      `{"error":{"code":500,"message":"Internal Server Error"}}`,
			expectErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseRetryDelay([]byte(tc.body))
			if tc.expectErr {
				if err == nil {
					t.Fatalf("expected error, got %v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got == nil || *got != tc.want {
				t.Fatalf("ParseRetryDelay() = %v, want %v", got, tc.want)
			}
		})
	}
}
