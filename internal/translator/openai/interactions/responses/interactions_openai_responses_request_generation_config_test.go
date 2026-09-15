package responses

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertOpenAIResponsesRequestToInteractions_MapsMaxOutputTokensAndSampling(t *testing.T) {
	raw := []byte(`{"model":"devin/swe-2","input":"hi","max_output_tokens":400,"temperature":0.2,"top_p":0.9}`)
	out := ConvertOpenAIResponsesRequestToInteractions("devin/swe-2", raw, false)
	if got := gjson.GetBytes(out, "generation_config.max_output_tokens").Int(); got != 400 {
		t.Fatalf("generation_config.max_output_tokens = %d, want 400. Output: %s", got, out)
	}
	if got := gjson.GetBytes(out, "generation_config.temperature").Float(); got != 0.2 {
		t.Fatalf("generation_config.temperature = %v, want 0.2. Output: %s", got, out)
	}
	if got := gjson.GetBytes(out, "generation_config.top_p").Float(); got != 0.9 {
		t.Fatalf("generation_config.top_p = %v, want 0.9. Output: %s", got, out)
	}
}

func TestConvertOpenAIResponsesRequestToInteractions_AntigravityKeepsAgentConfigOnly(t *testing.T) {
	raw := []byte(`{"model":"antigravity-preview-05-2026","input":"hi","max_output_tokens":400,"temperature":0.2}`)
	out := ConvertOpenAIResponsesRequestToInteractions("antigravity-preview-05-2026", raw, false)
	if got := gjson.GetBytes(out, "agent_config.max_total_tokens").Int(); got != 400 {
		t.Fatalf("agent_config.max_total_tokens = %d, want 400. Output: %s", got, out)
	}
	if gjson.GetBytes(out, "generation_config.max_output_tokens").Exists() || gjson.GetBytes(out, "generation_config.temperature").Exists() {
		t.Fatalf("antigravity request must not carry sampling knobs in generation_config. Output: %s", out)
	}
}
