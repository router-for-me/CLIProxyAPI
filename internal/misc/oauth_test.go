package misc

import "testing"

func TestShouldPromptForOAuthCallback(t *testing.T) {
	promptFn := func(string) (string, error) {
		return "", nil
	}

	tests := []struct {
		name      string
		noBrowser bool
		promptFn  func(string) (string, error)
		want      bool
	}{
		{
			name:      "browser mode disables manual callback prompt",
			noBrowser: false,
			promptFn:  promptFn,
			want:      false,
		},
		{
			name:      "no browser without prompt handler disables manual callback prompt",
			noBrowser: true,
			promptFn:  nil,
			want:      false,
		},
		{
			name:      "no browser with prompt handler enables manual callback prompt",
			noBrowser: true,
			promptFn:  promptFn,
			want:      true,
		},
	}

	for _, tt := range tests {
		if got := ShouldPromptForOAuthCallback(tt.noBrowser, tt.promptFn); got != tt.want {
			t.Fatalf("%s: got %v, want %v", tt.name, got, tt.want)
		}
	}
}
