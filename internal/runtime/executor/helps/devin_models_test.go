package helps

import (
	"testing"
)

func TestResolveDevinChatModelUID(t *testing.T) {
	tests := []struct {
		name          string
		rawModel      string
		thinkingLevel string
		budgetTokens  int
		want          string
	}{
		{
			name:     "direct full UID unchanged",
			rawModel: "claude-fable-5-1-max",
			want:     "claude-fable-5-1-max",
		},
		{
			name:     "direct swe-2-high unchanged",
			rawModel: "swe-2-high",
			want:     "swe-2-high",
		},
		{
			name:          "swe-2 default to high when no effort",
			rawModel:      "swe-2",
			thinkingLevel: "",
			want:          "swe-2-high",
		},
		{
			name:          "swe-2 clamp minimal to medium",
			rawModel:      "swe-2",
			thinkingLevel: "minimal",
			want:          "swe-2-medium",
		},
		{
			name:          "swe-2 clamp low to medium",
			rawModel:      "swe-2",
			thinkingLevel: "low",
			want:          "swe-2-medium",
		},
		{
			name:          "swe-2 with xhigh maps to max",
			rawModel:      "swe-2",
			thinkingLevel: "xhigh",
			want:          "swe-2-max",
		},
		{
			name:          "swe-2 request max",
			rawModel:      "swe-2",
			thinkingLevel: "max",
			want:          "swe-2-max",
		},
		{
			name:          "swe-2 suffix overrides body thinkingLevel",
			rawModel:      "swe-2(max)",
			thinkingLevel: "medium",
			want:          "swe-2-max",
		},
		{
			name:         "fable-5-1 budget tokens maps to max",
			rawModel:     "claude-fable-5-1",
			budgetTokens: 64000,
			want:         "claude-fable-5-1-max",
		},
		{
			name:         "fable-5-1 budget tokens maps to low",
			rawModel:     "claude-fable-5-1",
			budgetTokens: 2048,
			want:         "claude-fable-5-1-low",
		},
		{
			name:          "fable-5-1 with xhigh",
			rawModel:      "claude-fable-5-1",
			thinkingLevel: "xhigh",
			want:          "claude-fable-5-1-xhigh",
		},
		{
			name:          "astra with suffix in parenthesis",
			rawModel:      "gpt-6-astra(high)",
			thinkingLevel: "",
			want:          "gpt-6-astra-high",
		},
		{
			name:          "glm-5-3 clamp medium to high",
			rawModel:      "glm-5-3",
			thinkingLevel: "medium",
			want:          "glm-5-3-high",
		},
		{
			name:          "glm-5-2 free tier always maps to glm-5-2",
			rawModel:      "devin/glm-5-2",
			thinkingLevel: "high",
			want:          "glm-5-2",
		},
		{
			name:          "glm-5-2 suffix maps to free tier",
			rawModel:      "devin/glm-5-2(max)",
			thinkingLevel: "",
			want:          "glm-5-2",
		},
		{
			name:          "devin prefix lowercase swe-2",
			rawModel:      "devin/swe-2",
			thinkingLevel: "",
			want:          "swe-2-high",
		},
		{
			name:          "Devin prefix capitalized swe-2 with suffix",
			rawModel:      "Devin/swe-2(max)",
			thinkingLevel: "",
			want:          "swe-2-max",
		},
		{
			name:          "devin prefix claude-fable-5-1",
			rawModel:      "devin/claude-fable-5-1",
			thinkingLevel: "",
			want:          "claude-fable-5-1-medium",
		},
		{
			name:          "Devin prefix gpt-6-astra with suffix",
			rawModel:      "Devin/gpt-6-astra(high)",
			thinkingLevel: "",
			want:          "gpt-6-astra-high",
		},
		{
			name:          "devin prefix direct effort UID",
			rawModel:      "devin/swe-2-high",
			thinkingLevel: "",
			want:          "swe-2-high",
		},
		{
			name:          "devin prefix gemini-3-8-flash default high",
			rawModel:      "devin/gemini-3-8-flash",
			thinkingLevel: "",
			want:          "gemini-3-8-flash-high",
		},
		{
			name:          "devin prefix gemini-3.8-flash with low suffix",
			rawModel:      "devin/gemini-3.8-flash(low)",
			thinkingLevel: "",
			want:          "gemini-3-8-flash-low",
		},
		{
			name:          "devin prefix grok-4-6 default high",
			rawModel:      "devin/grok-4-6",
			thinkingLevel: "",
			want:          "grok-4-6-high",
		},
		{
			name:          "devin prefix grok-4.6 with xhigh suffix",
			rawModel:      "devin/grok-4.6:xhigh",
			thinkingLevel: "",
			want:          "grok-4-6-xhigh",
		},
		{
			name:          "devin prefix deepseek-v4-flash default high",
			rawModel:      "devin/deepseek-v4-flash",
			thinkingLevel: "",
			want:          "deepseek-v4-flash-high",
		},
		{
			name:          "devin prefix deepseek-v4.1-flash with max suffix",
			rawModel:      "devin/deepseek-v4.1-flash(max)",
			thinkingLevel: "",
			want:          "deepseek-v4-1-flash-max",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResolveDevinChatModelUID(tt.rawModel, tt.thinkingLevel, tt.budgetTokens)
			if got != tt.want {
				t.Errorf("ResolveDevinChatModelUID(%q, %q, %d) = %q, want %q", tt.rawModel, tt.thinkingLevel, tt.budgetTokens, got, tt.want)
			}
		})
	}
}
