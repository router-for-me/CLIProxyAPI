package helps

import (
	"net/http"
	"testing"
	"time"
)

func TestRetryAfter(t *testing.T) {
	now := time.Date(2026, time.August, 7, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name    string
		value   string
		want    time.Duration
		wantNil bool
	}{
		{name: "delta seconds", value: "2", want: 2 * time.Second},
		{name: "http date", value: now.Add(5 * time.Second).Format(http.TimeFormat), want: 5 * time.Second},
		{name: "zero", value: "0", wantNil: true},
		{name: "expired date", value: now.Add(-time.Second).Format(http.TimeFormat), wantNil: true},
		{name: "invalid", value: "soon", wantNil: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RetryAfter(http.Header{"Retry-After": {tt.value}}, now)
			if tt.wantNil {
				if got != nil {
					t.Fatalf("RetryAfter() = %v, want nil", *got)
				}
				return
			}
			if got == nil || *got != tt.want {
				t.Fatalf("RetryAfter() = %v, want %v", got, tt.want)
			}
		})
	}
}
