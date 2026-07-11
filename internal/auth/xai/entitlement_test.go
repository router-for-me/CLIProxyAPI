package xai

import (
	"encoding/base64"
	"testing"
)

func TestAccessTokenStandardAPIHint(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		want    StandardAPIHint
	}{
		{name: "missing tier", payload: `{"scope":"api:access"}`, want: StandardAPIHintUnknown},
		{name: "null tier", payload: `{"tier":null}`, want: StandardAPIHintUnknown},
		{name: "positive numeric tier", payload: `{"tier":4}`, want: StandardAPIHintYes},
		{name: "zero numeric tier", payload: `{"tier":0}`, want: StandardAPIHintNo},
		{name: "negative numeric tier", payload: `{"tier":-1}`, want: StandardAPIHintNo},
		{name: "positive string tier", payload: `{"tier":"4"}`, want: StandardAPIHintYes},
		{name: "zero string tier", payload: `{"tier":"0"}`, want: StandardAPIHintNo},
		{name: "unknown named tier", payload: `{"tier":"pro"}`, want: StandardAPIHintUnknown},
		{name: "infinite string tier", payload: `{"tier":"+Inf"}`, want: StandardAPIHintUnknown},
		{name: "nan string tier", payload: `{"tier":"NaN"}`, want: StandardAPIHintUnknown},
		{name: "fractional numeric tier", payload: `{"tier":4.5}`, want: StandardAPIHintUnknown},
		{name: "named free tier", payload: `{"tier":"free"}`, want: StandardAPIHintNo},
		{name: "boolean tier", payload: `{"tier":true}`, want: StandardAPIHintUnknown},
		{name: "object tier", payload: `{"tier":{}}`, want: StandardAPIHintUnknown},
		{name: "array tier", payload: `{"tier":[]}`, want: StandardAPIHintUnknown},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			token := "header." + base64.RawURLEncoding.EncodeToString([]byte(tt.payload)) + ".signature"
			if got := AccessTokenStandardAPIHint(token); got != tt.want {
				t.Fatalf("AccessTokenStandardAPIHint() = %v, want %v", got, tt.want)
			}
			if got := AccessTokenSuggestsStandardAPI(token); got != (tt.want == StandardAPIHintYes) {
				t.Fatalf("AccessTokenSuggestsStandardAPI() = %v, want %v", got, tt.want == StandardAPIHintYes)
			}
		})
	}

	for _, token := range []string{"", "opaque-token", "header.%%%invalid.signature"} {
		if got := AccessTokenStandardAPIHint(token); got != StandardAPIHintUnknown {
			t.Fatalf("AccessTokenStandardAPIHint(%q) = %v, want unknown", token, got)
		}
	}
}

func TestExplicitUsingAPI(t *testing.T) {
	tests := []struct {
		name       string
		attributes map[string]string
		metadata   map[string]any
		want       bool
		wantOK     bool
	}{
		{name: "unset"},
		{name: "attribute true", attributes: map[string]string{UsingAPIKey: "true"}, want: true, wantOK: true},
		{name: "attribute false", attributes: map[string]string{UsingAPIKey: "false"}, wantOK: true},
		{name: "metadata bool true", metadata: map[string]any{UsingAPIKey: true}, want: true, wantOK: true},
		{name: "metadata string false", metadata: map[string]any{UsingAPIKey: "false"}, wantOK: true},
		{
			name:       "attribute takes precedence",
			attributes: map[string]string{UsingAPIKey: "false"},
			metadata:   map[string]any{UsingAPIKey: true},
			wantOK:     true,
		},
		{
			name:       "invalid attribute falls through to metadata",
			attributes: map[string]string{UsingAPIKey: "invalid"},
			metadata:   map[string]any{UsingAPIKey: true},
			want:       true,
			wantOK:     true,
		},
		{name: "invalid values are unset", attributes: map[string]string{UsingAPIKey: "invalid"}, metadata: map[string]any{UsingAPIKey: 1}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ExplicitUsingAPI(tt.attributes, tt.metadata)
			if got != tt.want || ok != tt.wantOK {
				t.Fatalf("ExplicitUsingAPI() = (%v, %v), want (%v, %v)", got, ok, tt.want, tt.wantOK)
			}
		})
	}
}
