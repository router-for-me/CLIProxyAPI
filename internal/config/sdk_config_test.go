package config

import "testing"

func TestIsModelAllowedForKey(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		cfg     SDKConfig
		key     string
		model   string
		want    bool
	}{
		{
			name:  "no policies allow-all default permits any model",
			cfg:   SDKConfig{},
			key:   "sk-anything",
			model: "gpt-5",
			want:  true,
		},
		{
			name: "no policies deny-all default rejects any model",
			cfg: SDKConfig{
				APIKeyDefaultPolicy: APIKeyDefaultPolicyDenyAll,
			},
			key:   "sk-anything",
			model: "gpt-5",
			want:  false,
		},
		{
			name: "matching key with empty allowed list defers to default allow",
			cfg: SDKConfig{
				APIKeyPolicies: []APIKeyPolicy{
					{Key: "sk-empty", AllowedModels: nil},
				},
			},
			key:   "sk-empty",
			model: "gpt-4o-mini",
			want:  true,
		},
		{
			name: "matching key with empty allowed list and deny-all default rejects",
			cfg: SDKConfig{
				APIKeyDefaultPolicy: APIKeyDefaultPolicyDenyAll,
				APIKeyPolicies: []APIKeyPolicy{
					{Key: "sk-empty", AllowedModels: nil},
				},
			},
			key:   "sk-empty",
			model: "gpt-4o-mini",
			want:  false,
		},
		{
			name: "exact model match wins",
			cfg: SDKConfig{
				APIKeyPolicies: []APIKeyPolicy{
					{Key: "sk-strict", AllowedModels: []string{"claude-3-5-sonnet-20241022"}},
				},
			},
			key:   "sk-strict",
			model: "claude-3-5-sonnet-20241022",
			want:  true,
		},
		{
			name: "non-matching exact model rejects when policy is non-empty",
			cfg: SDKConfig{
				APIKeyPolicies: []APIKeyPolicy{
					{Key: "sk-strict", AllowedModels: []string{"claude-3-5-sonnet-20241022"}},
				},
			},
			key:   "sk-strict",
			model: "gpt-5",
			want:  false,
		},
		{
			name: "glob star matches family",
			cfg: SDKConfig{
				APIKeyPolicies: []APIKeyPolicy{
					{Key: "sk-glob", AllowedModels: []string{"gpt-4o*"}},
				},
			},
			key:   "sk-glob",
			model: "gpt-4o-mini",
			want:  true,
		},
		{
			name: "glob does not match unrelated family",
			cfg: SDKConfig{
				APIKeyPolicies: []APIKeyPolicy{
					{Key: "sk-glob", AllowedModels: []string{"gpt-4o*"}},
				},
			},
			key:   "sk-glob",
			model: "claude-3-5-sonnet-20241022",
			want:  false,
		},
		{
			name: "prefix-stripped model matches glob",
			cfg: SDKConfig{
				APIKeyPolicies: []APIKeyPolicy{
					{Key: "sk-prefix", AllowedModels: []string{"gemini-3-pro*"}},
				},
			},
			key:   "sk-prefix",
			model: "teamA/gemini-3-pro-preview",
			want:  true,
		},
		{
			name: "policy authored against the prefixed form still matches",
			cfg: SDKConfig{
				APIKeyPolicies: []APIKeyPolicy{
					{Key: "sk-prefix-literal", AllowedModels: []string{"teamA/gemini-3-pro*"}},
				},
			},
			key:   "sk-prefix-literal",
			model: "teamA/gemini-3-pro-preview",
			want:  true,
		},
		{
			name: "unknown key falls back to allow-all default",
			cfg: SDKConfig{
				APIKeyPolicies: []APIKeyPolicy{
					{Key: "sk-known", AllowedModels: []string{"gpt-4o*"}},
				},
			},
			key:   "sk-unknown",
			model: "gpt-4o",
			want:  true,
		},
		{
			name: "unknown key with deny-all default rejects",
			cfg: SDKConfig{
				APIKeyDefaultPolicy: APIKeyDefaultPolicyDenyAll,
				APIKeyPolicies: []APIKeyPolicy{
					{Key: "sk-known", AllowedModels: []string{"gpt-4o*"}},
				},
			},
			key:   "sk-unknown",
			model: "gpt-4o",
			want:  false,
		},
		{
			name: "blank key falls back to default",
			cfg: SDKConfig{
				APIKeyDefaultPolicy: APIKeyDefaultPolicyDenyAll,
				APIKeyPolicies: []APIKeyPolicy{
					{Key: "sk-known", AllowedModels: []string{"gpt-4o*"}},
				},
			},
			key:   "   ",
			model: "gpt-4o",
			want:  false,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := tc.cfg.IsModelAllowedForKey(tc.key, tc.model)
			if got != tc.want {
				t.Fatalf("IsModelAllowedForKey(%q, %q) = %v, want %v", tc.key, tc.model, got, tc.want)
			}
		})
	}
}
