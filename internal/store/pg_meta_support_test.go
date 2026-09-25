package store

import "testing"

// TestAuthIsOAuthProviderMeta pins that the meta channel is treated as an
// OAuth/file-backed auth (like xai) so its token metadata maps to an
// "oauth:meta" upstream_providers row during auth-dir seeding.
func TestAuthIsOAuthProviderMeta(t *testing.T) {
	if !authIsOAuthProvider("meta") {
		t.Fatal("authIsOAuthProvider(meta) = false; want true")
	}
}

// TestIsSyntheticProviderNameMeta pins that meta providers minted by the
// configsnapshot planner ("meta-<n>") are recognized as synthetic positional
// names so re-import identity matching falls back to the fingerprint rule
// instead of matching an operator-authored name.
func TestIsSyntheticProviderNameMeta(t *testing.T) {
	cases := []struct {
		name      string
		provider  string
		rowName   string
		wantKnown bool
	}{
		{name: "planner synthetic", provider: "meta-api-key", rowName: "meta-1", wantKnown: true},
		{name: "planner synthetic high ordinal", provider: "meta-api-key", rowName: "meta-17", wantKnown: true},
		{name: "operator named", provider: "meta-api-key", rowName: "primary", wantKnown: false},
		{name: "blank name", provider: "meta-api-key", rowName: "", wantKnown: false},
		{name: "other type prefix", provider: "xai-api-key", rowName: "meta-1", wantKnown: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isSyntheticProviderName(tc.provider, tc.rowName); got != tc.wantKnown {
				t.Fatalf("isSyntheticProviderName(%q, %q) = %t, want %t", tc.provider, tc.rowName, got, tc.wantKnown)
			}
		})
	}
}
