package policy

import "testing"

func TestIPMatches(t *testing.T) {
	cases := []struct {
		name     string
		clientIP string
		patterns []string
		want     bool
	}{
		{"empty client", "", []string{"10.0.0.1"}, false},
		{"invalid client", "not-an-ip", []string{"10.0.0.1"}, false},
		{"empty patterns", "10.0.0.1", nil, false},
		{"single exact match ipv4", "10.0.0.5", []string{"10.0.0.5"}, true},
		{"single exact no match ipv4", "10.0.0.5", []string{"10.0.0.6"}, false},
		{"cidr match ipv4", "10.0.0.5", []string{"10.0.0.0/8"}, true},
		{"cidr no match ipv4", "11.0.0.5", []string{"10.0.0.0/8"}, false},
		{"single exact match ipv6", "::1", []string{"::1"}, true},
		{"cidr match ipv6", "2001:db8::1", []string{"2001:db8::/32"}, true},
		{"skip malformed then match", "10.0.0.5", []string{"garbage", "10.0.0.0/8"}, true},
		{"malformed cidr skipped", "10.0.0.5", []string{"10.0.0.0/33"}, false},
		{"trailing whitespace trimmed", "10.0.0.5", []string{"  10.0.0.5  "}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, _ := IPMatches(tc.clientIP, tc.patterns)
			if got != tc.want {
				t.Fatalf("IPMatches(%q, %v) = %v, want %v", tc.clientIP, tc.patterns, got, tc.want)
			}
		})
	}
}

func TestValidateIPPatterns(t *testing.T) {
	cases := []struct {
		name     string
		patterns []string
		wantOK   bool
	}{
		{"empty", nil, true},
		{"valid single ipv4", []string{"10.0.0.5"}, true},
		{"valid cidr ipv4", []string{"10.0.0.0/8"}, true},
		{"valid ipv6 single", []string{"::1"}, true},
		{"valid ipv6 cidr", []string{"2001:db8::/32"}, true},
		{"mix valid", []string{"10.0.0.5", "10.0.0.0/8", "::1"}, true},
		{"invalid single", []string{"not-an-ip"}, false},
		{"invalid cidr", []string{"10.0.0.0/33"}, false},
		{"whitespace blank ignored", []string{"  ", "10.0.0.5"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg := ValidateIPPatterns(tc.patterns)
			gotOK := msg == ""
			if gotOK != tc.wantOK {
				t.Fatalf("ValidateIPPatterns(%v) msg=%q, wantOK=%v", tc.patterns, msg, tc.wantOK)
			}
		})
	}
}
