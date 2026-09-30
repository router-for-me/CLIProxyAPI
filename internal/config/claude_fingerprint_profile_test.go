package config

import "testing"

func TestNormalizeClaudeFingerprintProfile(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		raw   string
		want  string
		wantK bool
	}{
		{name: "empty", raw: "", want: ClaudeFingerprintProfileDefault, wantK: true},
		{name: "blank", raw: "   ", want: ClaudeFingerprintProfileDefault, wantK: true},
		{name: "canonical", raw: "claude-code-cli", want: ClaudeFingerprintProfileClaudeCodeCLI, wantK: true},
		{name: "mixed case and padding", raw: "  Claude-Code-CLI ", want: ClaudeFingerprintProfileClaudeCodeCLI, wantK: true},
		{name: "legacy alias", raw: "oauth-cli", want: ClaudeFingerprintProfileClaudeCodeCLI, wantK: true},
		{name: "typo", raw: "claude-code", want: ClaudeFingerprintProfileDefault, wantK: false},
		{name: "unrelated", raw: "chrome", want: ClaudeFingerprintProfileDefault, wantK: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, ok := NormalizeClaudeFingerprintProfile(tt.raw)
			if got != tt.want || ok != tt.wantK {
				t.Fatalf("NormalizeClaudeFingerprintProfile(%q) = (%q, %t), want (%q, %t)", tt.raw, got, ok, tt.want, tt.wantK)
			}
			errValidate := ValidateClaudeFingerprintProfile(tt.raw)
			if (errValidate == nil) != tt.wantK {
				t.Fatalf("ValidateClaudeFingerprintProfile(%q) error = %v, want error = %t", tt.raw, errValidate, !tt.wantK)
			}
		})
	}
}

// An unrecognized value must survive sanitization: rewriting a config file is not
// the place to discard operator input, and the request path already falls back to
// the default profile.
func TestSanitizeClaudeKeysFingerprintProfile(t *testing.T) {
	cfg := &Config{ClaudeKey: []ClaudeKey{
		{APIKey: "a", FingerprintProfile: "  OAuth-CLI  "},
		{APIKey: "b", FingerprintProfile: " claude-code "},
		{APIKey: "c"},
	}}
	cfg.SanitizeClaudeKeys()

	if got := cfg.ClaudeKey[0].FingerprintProfile; got != ClaudeFingerprintProfileClaudeCodeCLI {
		t.Fatalf("recognized alias = %q, want %q", got, ClaudeFingerprintProfileClaudeCodeCLI)
	}
	if got := cfg.ClaudeKey[1].FingerprintProfile; got != "claude-code" {
		t.Fatalf("unrecognized value = %q, want it preserved as written", got)
	}
	if got := cfg.ClaudeKey[2].FingerprintProfile; got != "" {
		t.Fatalf("absent value = %q, want empty", got)
	}
}
