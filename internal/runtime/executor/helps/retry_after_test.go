package helps

import (
	"net/http"
	"testing"
	"time"
)

func TestParseRetryAfterHeader(t *testing.T) {
	now := time.Unix(1700000000, 0).UTC()
	for _, tc := range []struct {
		value string
		want  time.Duration
		valid bool
	}{
		{"38", 38 * time.Second, true},
		{" 38 ", 38 * time.Second, true},
		{"0", 0, true},
		{"00038", 38 * time.Second, true},
		{now.Add(38 * time.Second).Format(http.TimeFormat), 38 * time.Second, true},
		{now.Add(-time.Second).Format(http.TimeFormat), 0, true},
		{"9223372036", 9223372036 * time.Second, true},
		{"9223372037", 0, false},
		{"9999999999", 0, false},
		{"999999999999999999999999", 0, false},
		{"-1", 0, false},
		{"+38", 0, false},
		{"1.5", 0, false},
		{"38, 40", 0, false},
		{"garbage", 0, false},
		{"", 0, false},
	} {
		t.Run(tc.value, func(t *testing.T) {
			got := ParseRetryAfterHeader(tc.value, now)
			if (got != nil) != tc.valid || (got != nil && *got != tc.want) {
				t.Fatalf("got %v, want valid=%v duration=%v", got, tc.valid, tc.want)
			}
		})
	}
}
