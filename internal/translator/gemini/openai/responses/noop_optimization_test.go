package responses

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertOpenAIResponsesRequestToGeminiBuildsGenerationConfigWithoutIntermediateObject(t *testing.T) {
	input := []byte(`{"input":"hello","temperature":0.5,"top_p":0.9,"stop_sequences":["done"],"text":{"format":{"type":"json_schema","schema":{"type":"object"}}}}`)

	output := ConvertOpenAIResponsesRequestToGemini("gemini-test", input, false)

	if got := gjson.GetBytes(output, "generationConfig.temperature").Float(); got != 0.5 {
		t.Fatalf("temperature = %v, want 0.5", got)
	}
	if got := gjson.GetBytes(output, "generationConfig.topP").Float(); got != 0.9 {
		t.Fatalf("topP = %v, want 0.9", got)
	}
	if got := gjson.GetBytes(output, "generationConfig.stopSequences.0").String(); got != "done" {
		t.Fatalf("stop sequence = %q, want done", got)
	}
	if got := gjson.GetBytes(output, "generationConfig.responseMimeType").String(); got != "application/json" {
		t.Fatalf("responseMimeType = %q, want application/json", got)
	}
	if !gjson.GetBytes(output, "generationConfig.responseJsonSchema").Exists() {
		t.Fatal("responseJsonSchema should be present")
	}
	if gjson.GetBytes(output, "generationConfig.responseSchema").Exists() {
		t.Fatal("responseSchema should not be present")
	}
}
