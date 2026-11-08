package auth

import "testing"

func TestMetaDCARefreshCredentialIsProviderScoped(t *testing.T) {
	for _, tc := range []struct {
		name string
		auth *Auth
		want bool
	}{
		{"meta", &Auth{Provider: "meta", Metadata: map[string]any{"dca_token": "dca:valid"}}, true},
		{"meta attributes", &Auth{Provider: "meta", Attributes: map[string]string{"dca_token": "dca:valid"}}, true},
		{"non-meta", &Auth{Provider: "codex", Metadata: map[string]any{"dca_token": "dca:valid"}}, false},
		{"empty", &Auth{Provider: "meta", Metadata: map[string]any{"dca_token": " "}}, false},
		{"existing oauth", &Auth{Provider: "codex", Metadata: map[string]any{"refresh_token": "refresh"}}, true},
		{"nil", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := authHasRefreshCredential(tc.auth); got != tc.want {
				t.Fatalf("refresh eligible = %v, want %v", got, tc.want)
			}
		})
	}
}
