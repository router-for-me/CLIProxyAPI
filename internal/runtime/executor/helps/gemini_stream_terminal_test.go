package helps

import "testing"

func TestIsGeminiTerminalStreamChunk_Interactions(t *testing.T) {
	for _, tc := range []struct {
		name  string
		chunk string
		want  bool
	}{
		{"completed event", "event: interaction.completed\ndata: {\"interaction\":{\"status\":\"completed\"}}\n\n", true},
		{"delta", "event: content.delta\ndata: {\"delta\":{\"text\":\"interaction.completed\"}}\n\n", false},
		{"failed", "event: interaction.failed\ndata: {\"interaction\":{\"status\":\"failed\"}}\n\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsGeminiTerminalStreamChunk([]byte(tc.chunk)); got != tc.want {
				t.Fatalf("IsGeminiTerminalStreamChunk(%q) = %v, want %v", tc.chunk, got, tc.want)
			}
		})
	}
}
