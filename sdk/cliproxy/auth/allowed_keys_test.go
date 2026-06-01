package auth

import "testing"

func TestAuthAllowedForClientKey(t *testing.T) {
	cases := []struct {
		name string
		auth *Auth
		key  string
		want bool
	}{
		{"nil-auth", nil, "k", false},
		{"no-attributes-public", &Auth{}, "anything", true},
		{"empty-allowed-keys-public", &Auth{Attributes: map[string]string{"allowed_keys": ""}}, "anything", true},
		{"listed-key-allowed", &Auth{Attributes: map[string]string{"allowed_keys": "keyA,keyB"}}, "keyA", true},
		{"unlisted-key-denied", &Auth{Attributes: map[string]string{"allowed_keys": "keyA,keyB"}}, "keyC", false},
		{"empty-key-denied-when-private", &Auth{Attributes: map[string]string{"allowed_keys": "keyA"}}, "", false},
		{"csv-whitespace-tolerated", &Auth{Attributes: map[string]string{"allowed_keys": " keyA , keyB "}}, "keyB", true},
		{"case-sensitive", &Auth{Attributes: map[string]string{"allowed_keys": "KeyA"}}, "keya", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := authAllowedForClientKey(tc.auth, tc.key); got != tc.want {
				t.Fatalf("authAllowedForClientKey(%v, %q) = %v, want %v", tc.auth, tc.key, got, tc.want)
			}
		})
	}
}
