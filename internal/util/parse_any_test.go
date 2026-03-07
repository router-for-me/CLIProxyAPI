package util

import (
	"encoding/json"
	"testing"
	"time"
)

func TestParseBoolAny(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   any
		want bool
		ok   bool
	}{
		{name: "bool true", in: true, want: true, ok: true},
		{name: "string false", in: "false", want: false, ok: true},
		{name: "float one", in: 1.0, want: true, ok: true},
		{name: "json number zero", in: json.Number("0"), want: false, ok: true},
		{name: "invalid string", in: "x", want: false, ok: false},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, ok := ParseBoolAny(tt.in)
			if ok != tt.ok || got != tt.want {
				t.Fatalf("ParseBoolAny(%v) = (%v,%v), want (%v,%v)", tt.in, got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestParseIntAny(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   any
		want int
		ok   bool
	}{
		{name: "int", in: 7, want: 7, ok: true},
		{name: "float", in: 7.9, want: 7, ok: true},
		{name: "json number", in: json.Number("12"), want: 12, ok: true},
		{name: "string", in: "42", want: 42, ok: true},
		{name: "invalid", in: "x", want: 0, ok: false},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, ok := ParseIntAny(tt.in)
			if ok != tt.ok || got != tt.want {
				t.Fatalf("ParseIntAny(%v) = (%v,%v), want (%v,%v)", tt.in, got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestParseTimeAny(t *testing.T) {
	t.Parallel()

	base := time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC)
	baseUnix := base.Unix()
	baseMillis := base.UnixMilli()

	tests := []struct {
		name string
		in   any
		want time.Time
		ok   bool
	}{
		{name: "rfc3339", in: "2023-01-01T00:00:00Z", want: base, ok: true},
		{name: "unix string seconds", in: "1672531200", want: base, ok: true},
		{name: "unix string millis", in: "1672531200000", want: base, ok: true},
		{name: "float unix", in: float64(baseUnix), want: base, ok: true},
		{name: "int unix", in: int(baseUnix), want: base, ok: true},
		{name: "int64 unix", in: baseUnix, want: base, ok: true},
		{name: "json number unix", in: json.Number("1672531200"), want: base, ok: true},
		{name: "json number millis", in: json.Number("1672531200000"), want: time.UnixMilli(baseMillis), ok: true},
		{name: "empty", in: "", want: time.Time{}, ok: false},
		{name: "invalid", in: "not-time", want: time.Time{}, ok: false},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, ok := ParseTimeAny(tt.in)
			if ok != tt.ok {
				t.Fatalf("ParseTimeAny(%v) ok=%v, want %v", tt.in, ok, tt.ok)
			}
			if tt.ok && !got.Equal(tt.want) {
				t.Fatalf("ParseTimeAny(%v) got=%v, want=%v", tt.in, got, tt.want)
			}
		})
	}
}
