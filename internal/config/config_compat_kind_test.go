package config

import "testing"

func TestInferCompatKindFromBaseURL(t *testing.T) {
	tests := []struct {
		name    string
		baseURL string
		want    string
	}{
		{
			name:    "minimax cn anthropic",
			baseURL: "https://api.minimaxi.com/anthropic",
			want:    "minimax",
		},
		{
			name:    "minimax global anthropic",
			baseURL: "https://api.minimaxi.io/anthropic",
			want:    "minimax",
		},
		{
			name:    "minimax trailing slash",
			baseURL: "https://api.minimaxi.com/anthropic/",
			want:    "minimax",
		},
		{
			name:    "other provider",
			baseURL: "https://api.anthropic.com",
			want:    "",
		},
		{
			name:    "minimax non anthropic path",
			baseURL: "https://api.minimaxi.com/v1",
			want:    "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := InferCompatKindFromBaseURL(tt.baseURL); got != tt.want {
				t.Fatalf("InferCompatKindFromBaseURL(%q) = %q, want %q", tt.baseURL, got, tt.want)
			}
		})
	}
}
