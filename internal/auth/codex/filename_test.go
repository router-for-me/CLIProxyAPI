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
			name:                  "team includes account hash",
			email:                 "user@example.com",
			planType:              "team",
			hashAccountID:         "abc12345",
			includeProviderPrefix: true,
			want:                  "codex-abc12345-user@example.com-team.json",
		},
		{
			name:                  "k12 includes account hash",
			email:                 "user@example.com",
			planType:              "k12",
			hashAccountID:         "def67890",
			includeProviderPrefix: true,
			want:                  "codex-def67890-user@example.com-k12.json",
		},
		{
			name:                  "k12 without account hash falls back to email and plan",
			email:                 "user@example.com",
			planType:              "k12",
			hashAccountID:         "",
			includeProviderPrefix: true,
			want:                  "codex-user@example.com-k12.json",
		},
		{
			name:                  "plus includes account hash",
			email:                 " user@example.com ",
			planType:              "Plus",
			hashAccountID:         " abc12345 ",
			includeProviderPrefix: true,
			want:                  "codex-abc12345-user@example.com-plus.json",
		},
		{
			name:                  "plus without account hash falls back to email and plan",
			email:                 "user@example.com",
			planType:              "plus",
			hashAccountID:         "",
			includeProviderPrefix: true,
			want:                  "codex-user@example.com-plus.json",
		},
		{
			name:                  "plan is normalized",
			email:                 "user@example.com",
			planType:              " Team Plan ",
			hashAccountID:         "abc12345",
			includeProviderPrefix: true,
			want:                  "codex-abc12345-user@example.com-team-plan.json",
		},
		{
			name:                  "account hash is used without plan",
			email:                 "user@example.com",
			planType:              "",
			hashAccountID:         "abc12345",
			includeProviderPrefix: true,
			want:                  "codex-abc12345-user@example.com.json",
		},
		{
			name:                  "missing plan and account hash falls back to email",
			email:                 "user@example.com",
			planType:              "",
			hashAccountID:         "",
			includeProviderPrefix: true,
			want:                  "codex-user@example.com.json",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CredentialFileName(tt.email, tt.planType, tt.hashAccountID, tt.includeProviderPrefix)
			if got != tt.want {
				t.Fatalf("CredentialFileName() = %q, want %q", got, tt.want)
			}
		})
	}
}
