package executor

import "testing"

func TestIsKimiProvider(t *testing.T) {
	tests := []struct {
		name        string
		baseURL     string
		providerKey string
		want        bool
	}{
		{"kimi base url coding", "https://api.kimi.com/coding/", "openrouter", true},
		{"kimi base url v1", "https://api.kimi.com/coding/v1", "other", true},
		{"kimi base url no slash", "https://api.kimi.com/coding", "x", true},
		{"provider kimi", "https://example.com/v1", "kimi", true},
		{"provider kimi-for-coding", "https://example.com", "kimi-for-coding", true},
		{"provider kimi case", "https://example.com", "KIMI", true},
		{"provider kimi-for-coding case", "https://example.com", "Kimi-For-Coding", true},
		{"openrouter", "https://openrouter.ai/api/v1", "openrouter", false},
		{"other host", "https://api.openai.com/v1", "openai", false},
		{"empty", "", "", false},
		{"empty base kimi key", "", "kimi", true},
		{"empty key kimi base", "https://api.kimi.com/coding/", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsKimiProvider(tt.baseURL, tt.providerKey)
			if got != tt.want {
				t.Errorf("IsKimiProvider(%q, %q) = %v, want %v", tt.baseURL, tt.providerKey, got, tt.want)
			}
		})
	}
}

func TestKimiUserAgent(t *testing.T) {
	if KimiUserAgent != "claude-code/2.0" {
		t.Errorf("KimiUserAgent = %q, want %q", KimiUserAgent, "claude-code/2.0")
	}
}
