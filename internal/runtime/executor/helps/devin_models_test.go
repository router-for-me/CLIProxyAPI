package helps

import (
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
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
			name:          "devin prefix glm-5-3-flash default high",
			rawModel:      "devin/glm-5-3-flash",
			thinkingLevel: "",
			want:          "glm-5-3-flash-high",
		},
		{
			name:          "devin prefix glm-5-3-flash with low suffix",
			rawModel:      "devin/glm-5-3-flash:low",
			thinkingLevel: "",
			want:          "glm-5-3-flash-low",
		},
		{
			name:          "devin prefix glm-5-3-flash with max suffix",
			rawModel:      "devin/glm-5-3-flash(max)",
			thinkingLevel: "",
			want:          "glm-5-3-flash-max",
		},
		{
			name:          "devin prefix gpt-5-6-sol with none suffix",
			rawModel:      "devin/gpt-5-6-sol:none",
			thinkingLevel: "",
			want:          "gpt-5-6-sol-none",
		},
		{
			name:          "devin prefix gpt-5-6-sol with body none effort",
			rawModel:      "devin/gpt-5-6-sol",
			thinkingLevel: "none",
			want:          "gpt-5-6-sol-none",
		},
		{
			name:          "devin prefix gpt-5-6-terra with none suffix",
			rawModel:      "devin/gpt-5-6-terra:none",
			thinkingLevel: "",
			want:          "gpt-5-6-terra-none",
		},
		{
			name:          "devin prefix nemotron-3-ultra with none suffix",
			rawModel:      "devin/nemotron-3-ultra:none",
			thinkingLevel: "",
			want:          "nemotron-3-ultra-none",
		},
		{
			name:          "glm-5-2 default maps to glm-5-2",
			rawModel:      "devin/glm-5-2",
			thinkingLevel: "high",
			want:          "glm-5-2",
		},
		{
			name:          "glm-5-2 with none effort maps to glm-5-2-none",
			rawModel:      "devin/glm-5-2:none",
			thinkingLevel: "",
			want:          "glm-5-2-none",
		},
		{
			name:          "glm-5-2 suffix max maps to max",
			rawModel:      "devin/glm-5-2(max)",
			thinkingLevel: "",
			want:          "glm-5-2-max",
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
		{
			name:          "swe-1-7 default maps to swe-1-7",
			rawModel:      "devin/swe-1-7",
			thinkingLevel: "",
			want:          "swe-1-7",
		},
		{
			name:          "swe-1-7 with medium maps to swe-1-7-medium",
			rawModel:      "devin/swe-1-7:medium",
			thinkingLevel: "",
			want:          "swe-1-7-medium",
		},
		{
			name:          "claude-haiku-4-5 maps to MODEL_PRIVATE_11",
			rawModel:      "devin/claude-haiku-4-5",
			thinkingLevel: "",
			want:          "MODEL_PRIVATE_11",
		},
		{
			name:          "claude-sonnet-4-5 non-thinking maps to MODEL_PRIVATE_2",
			rawModel:      "devin/claude-sonnet-4-5",
			thinkingLevel: "",
			want:          "MODEL_PRIVATE_2",
		},
		{
			name:          "claude-sonnet-4-5 thinking maps to MODEL_PRIVATE_3",
			rawModel:      "devin/claude-sonnet-4-5:high",
			thinkingLevel: "",
			want:          "MODEL_PRIVATE_3",
		},
		{
			name:          "gpt-4-1 maps to MODEL_CHAT_GPT_4_1_2025_04_14",
			rawModel:      "devin/gpt-4-1",
			thinkingLevel: "",
			want:          "MODEL_CHAT_GPT_4_1_2025_04_14",
		},
		{
			name:          "gemini-3-flash alias maps to gemini-3-8-flash-high",
			rawModel:      "devin/gemini-3-flash",
			thinkingLevel: "",
			want:          "gemini-3-8-flash-high",
		},
		{
			name:          "gpt-5-6-luna default maps to low",
			rawModel:      "devin/gpt-5-6-luna",
			thinkingLevel: "",
			want:          "gpt-5-6-luna-low",
		},
		{
			name:          "gpt-5-6-luna with high suffix",
			rawModel:      "devin/gpt-5-6-luna(high)",
			thinkingLevel: "",
			want:          "gpt-5-6-luna-high",
		},
		{
			name:          "gpt-5-6-luna with none suffix",
			rawModel:      "devin/gpt-5-6-luna:none",
			thinkingLevel: "",
			want:          "gpt-5-6-luna-none",
		},
		{
			name:          "gpt-5-6-sol default maps to low",
			rawModel:      "devin/gpt-5-6-sol",
			thinkingLevel: "",
			want:          "gpt-5-6-sol-low",
		},
		{
			name:          "gpt-5-6-terra default maps to low",
			rawModel:      "devin/gpt-5-6-terra",
			thinkingLevel: "",
			want:          "gpt-5-6-terra-low",
		},
		{
			name:          "gpt-5-5 default maps to low",
			rawModel:      "devin/gpt-5-5",
			thinkingLevel: "",
			want:          "gpt-5-5-low",
		},
		{
			name:          "gpt-5-4 default maps to low",
			rawModel:      "devin/gpt-5-4",
			thinkingLevel: "",
			want:          "gpt-5-4-low",
		},
		{
			name:          "gpt-5-4-mini default maps to medium",
			rawModel:      "devin/gpt-5-4-mini",
			thinkingLevel: "",
			want:          "gpt-5-4-mini-medium",
		},
		{
			name:          "gpt-5-3-codex default maps to medium",
			rawModel:      "devin/gpt-5-3-codex",
			thinkingLevel: "",
			want:          "gpt-5-3-codex-medium",
		},
		{
			name:          "claude-opus-5 default maps to medium",
			rawModel:      "devin/claude-opus-5",
			thinkingLevel: "",
			want:          "claude-opus-5-medium",
		},
		{
			name:          "claude-opus-4-8 default maps to medium",
			rawModel:      "devin/claude-opus-4-8",
			thinkingLevel: "",
			want:          "claude-opus-4-8-medium",
		},
		{
			name:          "claude-opus-4-7 default maps to medium",
			rawModel:      "devin/claude-opus-4-7",
			thinkingLevel: "",
			want:          "claude-opus-4-7-medium",
		},
		{
			name:          "claude-sonnet-5 default maps to medium",
			rawModel:      "devin/claude-sonnet-5",
			thinkingLevel: "",
			want:          "claude-sonnet-5-medium",
		},
		{
			name:          "gemini-3-7-flash default maps to high",
			rawModel:      "devin/gemini-3-7-flash",
			thinkingLevel: "",
			want:          "gemini-3-7-flash-high",
		},
		{
			name:          "gemini-3-6-flash default maps to high",
			rawModel:      "devin/gemini-3-6-flash",
			thinkingLevel: "",
			want:          "gemini-3-6-flash-high",
		},
		{
			name:          "gemini-3-5-flash default maps to high",
			rawModel:      "devin/gemini-3-5-flash",
			thinkingLevel: "",
			want:          "gemini-3-5-flash-high",
		},
		{
			name:          "deepseek-v4-pro default maps to high",
			rawModel:      "devin/deepseek-v4-pro",
			thinkingLevel: "",
			want:          "deepseek-v4-pro-high",
		},
		{
			name:          "grok-4-5 default maps to high",
			rawModel:      "devin/grok-4-5",
			thinkingLevel: "",
			want:          "grok-4-5-high",
		},
		{
			name:          "kimi-k3 default maps to high",
			rawModel:      "devin/kimi-k3",
			thinkingLevel: "",
			want:          "kimi-k3-high",
		},
		{
			name:          "nemotron-3-ultra default maps to high",
			rawModel:      "devin/nemotron-3-ultra",
			thinkingLevel: "",
			want:          "nemotron-3-ultra-high",
		},
		{
			name:          "swe-1-6 default maps to swe-1-6",
			rawModel:      "devin/swe-1-6",
			thinkingLevel: "",
			want:          "swe-1-6",
		},
		{
			name:          "swe-1-6 with fast maps to swe-1-6-fast",
			rawModel:      "devin/swe-1-6:fast",
			thinkingLevel: "",
			want:          "swe-1-6-fast",
		},
		{
			name:          "kimi-k2-6 default maps to kimi-k2-6",
			rawModel:      "devin/kimi-k2-6",
			thinkingLevel: "",
			want:          "kimi-k2-6",
		},
		{
			name:          "kimi-k2-7 default maps to kimi-k2-7",
			rawModel:      "devin/kimi-k2-7",
			thinkingLevel: "",
			want:          "kimi-k2-7",
		},
		{
			name:          "claude-opus-4-6 default maps to claude-opus-4-6",
			rawModel:      "devin/claude-opus-4-6",
			thinkingLevel: "",
			want:          "claude-opus-4-6",
		},
		{
			name:          "claude-sonnet-4-6 default maps to claude-sonnet-4-6",
			rawModel:      "devin/claude-sonnet-4-6",
			thinkingLevel: "",
			want:          "claude-sonnet-4-6",
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

func TestResolveDevinChatModelUID_AllCatalogModels(t *testing.T) {
	models := registry.GetDevinModels()
	if len(models) == 0 {
		t.Fatal("GetDevinModels() returned empty list")
	}

	testEfforts := []string{"", "none", "low", "medium", "high", "xhigh", "max"}
	for _, m := range models {
		baseID := strings.TrimPrefix(m.ID, "devin/")
		for _, eff := range testEfforts {
			resolved := ResolveDevinChatModelUID("devin/"+baseID, eff, 0)
			if resolved == "" {
				t.Errorf("model %q with effort %q resolved to empty string", baseID, eff)
			}
			// If model defines thinking levels, resolved UID must have an effort suffix
			if m.Thinking != nil && len(m.Thinking.Levels) > 0 {
				if !HasDevinEffortSuffix(resolved) && baseID != "swe-1-7" && baseID != "glm-5-2" {
					t.Errorf("thinking model %q with effort %q resolved to bare UID %q without effort suffix", baseID, eff, resolved)
				}
			}
		}
	}
}
