package util

import "testing"

func TestNormalizeOpenAIFastModeModel(t *testing.T) {
	tests := []struct {
		name                string
		input               string
		wantNormalizedModel string
		wantBaseModel       string
		wantFast            bool
		wantCompat          bool
	}{
		{
			name:                "plain fast alias",
			input:               "gpt-5.4-fast",
			wantNormalizedModel: "gpt-5.4",
			wantBaseModel:       "gpt-5.4",
			wantFast:            true,
			wantCompat:          true,
		},
		{
			name:                "fast alias with reasoning level",
			input:               "gpt-5.4-high-fast",
			wantNormalizedModel: "gpt-5.4(high)",
			wantBaseModel:       "gpt-5.4",
			wantFast:            true,
			wantCompat:          true,
		},
		{
			name:                "codex fast alias with whitespace and mixed case",
			input:               "  GPT-5.2-CODEX-XHIGH-FAST  ",
			wantNormalizedModel: "gpt-5.2-codex(xhigh)",
			wantBaseModel:       "gpt-5.2-codex",
			wantFast:            true,
			wantCompat:          true,
		},
		{
			name:                "non-fast hyphen alias stays unchanged",
			input:               "gpt-5.4-high",
			wantNormalizedModel: "gpt-5.4-high",
			wantBaseModel:       "gpt-5.4-high",
			wantFast:            false,
			wantCompat:          false,
		},
		{
			name:                "standard thinking suffix stays unchanged",
			input:               "gpt-5.4(high)",
			wantNormalizedModel: "gpt-5.4(high)",
			wantBaseModel:       "gpt-5.4",
			wantFast:            false,
			wantCompat:          false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NormalizeOpenAIFastModeModel(tt.input)
			if got.NormalizedModel != tt.wantNormalizedModel {
				t.Fatalf("NormalizedModel = %q, want %q", got.NormalizedModel, tt.wantNormalizedModel)
			}
			if got.BaseModel != tt.wantBaseModel {
				t.Fatalf("BaseModel = %q, want %q", got.BaseModel, tt.wantBaseModel)
			}
			if got.Fast != tt.wantFast {
				t.Fatalf("Fast = %v, want %v", got.Fast, tt.wantFast)
			}
			if got.UsedCompatibility != tt.wantCompat {
				t.Fatalf("UsedCompatibility = %v, want %v", got.UsedCompatibility, tt.wantCompat)
			}
		})
	}
}
