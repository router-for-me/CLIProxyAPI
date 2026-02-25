package executor

import "testing"

func TestNormalizeGeminiCLIModel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		model string
		want  string
	}{
		{name: "gemini3 pro alias maps to 2_5_pro", model: "gemini-3-pro", want: "gemini-2.5-pro"},
		{name: "gemini3 flash alias maps to 2_5_flash", model: "gemini-3-flash", want: "gemini-2.5-flash"},
		{name: "gemini31 pro alias maps to 2_5_pro", model: "gemini-3.1-pro", want: "gemini-2.5-pro"},
		{name: "non gemini3 model unchanged", model: "gemini-2.5-pro", want: "gemini-2.5-pro"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := normalizeGeminiCLIModel(tt.model)
			if got != tt.want {
				t.Fatalf("normalizeGeminiCLIModel(%q)=%q, want %q", tt.model, got, tt.want)
			}
		})
	}
}
