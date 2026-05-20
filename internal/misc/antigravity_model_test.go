package misc

import "testing"

func TestAntigravityWireModel_Gemini35FlashVariants(t *testing.T) {
	tests := map[string]string{
		"gemini-3.5-flash-high":   "gemini-3-flash-agent",
		"gemini-3.5-flash-medium": "gemini-3.5-flash-low",
		"gemini-3.5-flash":        "gemini-3.5-flash-low",
		"gemini-3-flash-medium":   "gemini-3-flash",
		"gemini-3-flash":          "gemini-3-flash",
	}

	for input, want := range tests {
		if got := AntigravityWireModel(input); got != want {
			t.Fatalf("AntigravityWireModel(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestAntigravityDisplayName_Gemini35BackendKeys(t *testing.T) {
	tests := map[string]string{
		"gemini-3-flash-agent": "Gemini 3.5 Flash (High)",
		"gemini-3.5-flash-low": "Gemini 3.5 Flash (Medium)",
	}

	for input, want := range tests {
		if got := AntigravityDisplayName(input, "fallback"); got != want {
			t.Fatalf("AntigravityDisplayName(%q) = %q, want %q", input, got, want)
		}
	}
}
