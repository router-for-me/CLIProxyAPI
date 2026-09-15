package config

import "testing"

func TestNormalizeAliasPool(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{in: "", want: ""},
		{in: "round-robin", want: ""},
		{in: "RR", want: ""},
		{in: "prefer", want: AliasPoolPrefer},
		{in: "PRIMARY", want: AliasPoolPrefer},
		{in: "unknown", want: ""},
	}
	for _, tt := range tests {
		if got := NormalizeAliasPool(tt.in); got != tt.want {
			t.Fatalf("NormalizeAliasPool(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
	if !AliasPoolIsPrefer("prefer") || AliasPoolIsPrefer("") {
		t.Fatal("AliasPoolIsPrefer mismatch")
	}
}
