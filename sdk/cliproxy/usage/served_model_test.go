package usage

import "testing"

func TestDetectSubstitution(t *testing.T) {
	cases := []struct {
		name          string
		served        string
		requested     string
		substituted   bool
		wantServed    string
		wantRequested string
	}{
		{name: "identical", served: "claude-opus-5", requested: "claude-opus-5", substituted: false,
			wantServed: "claude-opus-5", wantRequested: "claude-opus-5"},
		{name: "case and space insensitive", served: " claude-opus-5 ", requested: "CLAUDE-OPUS-5", substituted: false,
			wantServed: "claude-opus-5", wantRequested: "CLAUDE-OPUS-5"},
		{name: "silent downgrade", served: "claude-haiku-4-5", requested: "claude-opus-5", substituted: true,
			wantServed: "claude-haiku-4-5", wantRequested: "claude-opus-5"},
		{name: "served unknown is not substitution", served: "", requested: "claude-opus-5", substituted: false,
			wantServed: "", wantRequested: "claude-opus-5"},
		{name: "requested unknown is not substitution", served: "claude-opus-5", requested: "", substituted: false,
			wantServed: "claude-opus-5", wantRequested: ""},
		{name: "both unknown", served: "", requested: "", substituted: false,
			wantServed: "", wantRequested: ""},
		{name: "internal whitespace is significant", served: "claude-opus- 5", requested: "claude-opus-5", substituted: true,
			wantServed: "claude-opus- 5", wantRequested: "claude-opus-5"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			report := DetectSubstitution(tc.served, tc.requested)
			if report.Substituted != tc.substituted {
				t.Fatalf("DetectSubstitution(%q, %q).Substituted = %v, want %v",
					tc.served, tc.requested, report.Substituted, tc.substituted)
			}
			if report.Served != tc.wantServed {
				t.Fatalf("Served = %q, want %q", report.Served, tc.wantServed)
			}
			if report.Requested != tc.wantRequested {
				t.Fatalf("Requested = %q, want %q", report.Requested, tc.wantRequested)
			}
		})
	}
}
