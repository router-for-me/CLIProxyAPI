package management

import "testing"

func TestParseManualCodePair(t *testing.T) {
	tests := []struct {
		name      string
		raw       string
		wantCode  string
		wantState string
		wantOK    bool
	}{
		{
			name:   "bare code without state",
			raw:    "ac_01ABCdef-123",
			wantOK: false,
		},
		{
			name:      "code and state",
			raw:       "ac_01ABCdef-123#z0lAE_ru8ATNwBc1inRpt40wFm8axKj3-8uQSQdIq5Q",
			wantCode:  "ac_01ABCdef-123",
			wantState: "z0lAE_ru8ATNwBc1inRpt40wFm8axKj3-8uQSQdIq5Q",
			wantOK:    true,
		},
		{
			name:   "callback url is not a manual pair",
			raw:    "http://localhost:54545/callback?code=abc&state=def",
			wantOK: false,
		},
		{
			name:   "callback url with fragment is not a manual pair",
			raw:    "http://localhost:54545/callback?code=abc#def",
			wantOK: false,
		},
		{
			name:   "empty state",
			raw:    "abc#",
			wantOK: false,
		},
		{
			name:   "empty code",
			raw:    "#def",
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, state, ok := parseManualCodePair(tt.raw)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if !tt.wantOK {
				return
			}
			if code != tt.wantCode {
				t.Fatalf("code = %q, want %q", code, tt.wantCode)
			}
			if state != tt.wantState {
				t.Fatalf("state = %q, want %q", state, tt.wantState)
			}
		})
	}
}
