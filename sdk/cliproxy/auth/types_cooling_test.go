package auth

import "testing"

func TestDisableCoolingOverrideSupportsExplicitFalse(t *testing.T) {
	tests := []struct {
		name        string
		auth        *Auth
		want        bool
		wantPresent bool
	}{
		{name: "unset", auth: &Auth{}},
		{name: "canonical true", auth: &Auth{Metadata: map[string]any{"disable_cooling": true}}, want: true, wantPresent: true},
		{name: "canonical false", auth: &Auth{Metadata: map[string]any{"disable_cooling": false}}, wantPresent: true},
		{name: "legacy false", auth: &Auth{Metadata: map[string]any{"disable-cooling": false}}, wantPresent: true},
		{name: "string false", auth: &Auth{Metadata: map[string]any{"disable_cooling": "false"}}, wantPresent: true},
		{name: "invalid", auth: &Auth{Metadata: map[string]any{"disable_cooling": "invalid"}}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, present := tc.auth.DisableCoolingOverride()
			if got != tc.want || present != tc.wantPresent {
				t.Fatalf("DisableCoolingOverride() = %t, %t, want %t, %t", got, present, tc.want, tc.wantPresent)
			}
		})
	}
}
