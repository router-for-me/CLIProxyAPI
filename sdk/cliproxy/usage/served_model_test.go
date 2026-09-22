package usage

import (
	"strings"
	"testing"
)

func TestDetectSubstitution(t *testing.T) {
	cases := []struct {
		name        string
		served      string
		requested   string
		substituted bool
	}{
		{name: "identical", served: "claude-opus-5", requested: "claude-opus-5", substituted: false},
		{name: "case and space insensitive", served: " claude-opus-5 ", requested: "CLAUDE-OPUS-5", substituted: false},
		{name: "silent downgrade", served: "claude-haiku-4-5", requested: "claude-opus-5", substituted: true},
		{name: "served unknown is not substitution", served: "", requested: "claude-opus-5", substituted: false},
		{name: "requested unknown is not substitution", served: "claude-opus-5", requested: "", substituted: false},
		{name: "both unknown", served: "", requested: "", substituted: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			report := DetectSubstitution(tc.served, tc.requested)
			if report.Substituted != tc.substituted {
				t.Fatalf("DetectSubstitution(%q, %q).Substituted = %v, want %v",
					tc.served, tc.requested, report.Substituted, tc.substituted)
			}
			if report.Served != strings.TrimSpace(tc.served) {
				t.Fatalf("Served = %q, want %q", report.Served, strings.TrimSpace(tc.served))
			}
			if report.Requested != strings.TrimSpace(tc.requested) {
				t.Fatalf("Requested = %q, want %q", report.Requested, strings.TrimSpace(tc.requested))
			}
		})
	}
}
