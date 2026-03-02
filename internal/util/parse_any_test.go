package util

import (
	"encoding/json"
	"testing"
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

	tests := []struct {
		name string
		in   any
		ok   bool
	}{
		{name: "rfc3339", in: "2026-03-01T00:00:00Z", ok: true},
		{name: "unix string", in: "1700000000", ok: true},
		{name: "float unix", in: 1700000000.0, ok: true},
		{name: "json number unix", in: json.Number("1700000000"), ok: true},
		{name: "empty", in: "", ok: false},
		{name: "invalid", in: "not-time", ok: false},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, ok := ParseTimeAny(tt.in)
			if ok != tt.ok {
				t.Fatalf("ParseTimeAny(%v) ok=%v, want %v", tt.in, ok, tt.ok)
			}
		})
	}
}
