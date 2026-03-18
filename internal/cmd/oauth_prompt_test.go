package cmd

import "testing"

func TestSelectOAuthCallbackPrompt(t *testing.T) {
	promptFn := func(string) (string, error) {
		return "", nil
	}

	tests := []struct {
		name    string
		options *LoginOptions
		wantNil bool
	}{
		{
			name:    "nil options disables callback prompt",
			options: nil,
			wantNil: true,
		},
		{
			name: "browser mode disables callback prompt",
			options: &LoginOptions{
				NoBrowser: false,
			},
			wantNil: true,
		},
		{
			name: "no browser keeps callback prompt",
			options: &LoginOptions{
				NoBrowser: true,
			},
			wantNil: false,
		},
	}

	for _, tt := range tests {
		got := selectOAuthCallbackPrompt(tt.options, promptFn)
		if (got == nil) != tt.wantNil {
			t.Fatalf("%s: got nil=%v, want nil=%v", tt.name, got == nil, tt.wantNil)
		}
	}
}
