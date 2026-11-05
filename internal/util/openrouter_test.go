package util

import "testing"

func TestIsOpenRouterURL(t *testing.T) {
	for raw, want := range map[string]bool{
		"https://openrouter.ai/api/v1": true, "https://eu.openrouter.ai/api": true, "HTTPS://OPENROUTER.AI": true,
		"https://notopenrouter.ai/v1": false, "https://api.example.com/v1": false, "": false, "::bad": false,
	} {
		if got := IsOpenRouterURL(raw); got != want {
			t.Errorf("IsOpenRouterURL(%q) = %v, want %v", raw, got, want)
		}
	}
}
