package codex

import "testing"

func TestCredentialFileName(t *testing.T) {
	tests := []struct {
		name                  string
		email                 string
		planType              string
		hashAccountID         string
		includeProviderPrefix bool
		want                  string
	}{
		{
			name:                  "no prefix no plan",
			email:                 "user@example.com",
			includeProviderPrefix: false,
			want:                  "user@example.com.json",
		},
		{
			name:                  "with prefix no plan",
			email:                 "user@example.com",
			includeProviderPrefix: true,
			want:                  "codex-user@example.com.json",
		},
		{
			name:                  "no prefix normalized plan",
			email:                 "user@example.com",
			planType:              "Plus Preview",
			includeProviderPrefix: false,
			want:                  "user@example.com-plus-preview.json",
		},
		{
			name:                  "team plan includes provider prefix and hashed account id",
			email:                 "user@example.com",
			planType:              "Team",
			hashAccountID:         "acct123",
			includeProviderPrefix: true,
			want:                  "codex-acct123-user@example.com-team.json",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := CredentialFileName(tc.email, tc.planType, tc.hashAccountID, tc.includeProviderPrefix)
			if got != tc.want {
				t.Fatalf("CredentialFileName() = %q, want %q", got, tc.want)
			}
		})
	}
}
