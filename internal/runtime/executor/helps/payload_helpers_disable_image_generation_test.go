package helps

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/tidwall/gjson"
)

func TestApplyPayloadConfigWithRoot_DisableImageGeneration_RemovesToolsEntry(t *testing.T) {
	cfg := &config.Config{
		SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationAll},
	}
	payload := []byte(`{"tools":[{"type":"image_generation","output_format":"png"},{"type":"function","name":"f1"}]}`)

	out := ApplyPayloadConfigWithRoot(cfg, "gpt-5.4", "openai-response", "", payload, nil, "", "")

	tools := gjson.GetBytes(out, "tools")
	if !tools.Exists() || !tools.IsArray() {
		t.Fatalf("expected tools array, got %v", tools.Type)
	}
	arr := tools.Array()
	if len(arr) != 1 {
		t.Fatalf("expected 1 tool after removal, got %d", len(arr))
	}
	if got := arr[0].Get("type").String(); got != "function" {
		t.Fatalf("expected remaining tool type=function, got %q", got)
	}
}

func TestApplyPayloadConfigWithRoot_DisableImageGeneration_RemovesToolsEntryWithRoot(t *testing.T) {
	cfg := &config.Config{
		SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationAll},
	}
	payload := []byte(`{"request":{"tools":[{"type":"image_generation"},{"type":"web_search"}]}}`)

	out := ApplyPayloadConfigWithRoot(cfg, "gpt-5.4", "gemini-cli", "request", payload, nil, "", "")

	tools := gjson.GetBytes(out, "request.tools")
	if !tools.Exists() || !tools.IsArray() {
		t.Fatalf("expected request.tools array, got %v", tools.Type)
	}
	arr := tools.Array()
	if len(arr) != 1 {
		t.Fatalf("expected 1 tool after removal, got %d", len(arr))
	}
	if got := arr[0].Get("type").String(); got != "web_search" {
		t.Fatalf("expected remaining tool type=web_search, got %q", got)
	}
}

func TestApplyPayloadConfigWithRoot_DisableImageGeneration_RemovesToolChoiceByType(t *testing.T) {
	cfg := &config.Config{
		SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationAll},
	}
	payload := []byte(`{"tools":[{"type":"image_generation"},{"type":"function","name":"f1"}],"tool_choice":{"type":"image_generation"}}`)

	out := ApplyPayloadConfigWithRoot(cfg, "gpt-5.4", "openai-response", "", payload, nil, "", "")

	if gjson.GetBytes(out, "tool_choice").Exists() {
		t.Fatalf("expected tool_choice to be removed")
	}
}

func TestApplyPayloadConfigWithRoot_DisableImageGeneration_RemovesToolChoiceByNameWithRoot(t *testing.T) {
	cfg := &config.Config{
		SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationAll},
	}
	payload := []byte(`{"request":{"tools":[{"type":"image_generation"},{"type":"web_search"}],"tool_choice":{"type":"tool","name":"image_generation"}}}`)

	out := ApplyPayloadConfigWithRoot(cfg, "gpt-5.4", "gemini-cli", "request", payload, nil, "", "")

	if gjson.GetBytes(out, "request.tool_choice").Exists() {
		t.Fatalf("expected request.tool_choice to be removed")
	}
}

func TestApplyPayloadConfigWithRoot_DisableImageGenerationChat_KeepsImageGenerationOnImagesEndpoints(t *testing.T) {
	cfg := &config.Config{
		SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationChat},
	}
	payload := []byte(`{"tools":[{"type":"image_generation"},{"type":"function","name":"f1"}],"tool_choice":{"type":"image_generation"}}`)

	out := ApplyPayloadConfigWithRoot(cfg, "gpt-5.4", "openai-response", "", payload, nil, "", "/v1/images/generations")

	tools := gjson.GetBytes(out, "tools")
	if !tools.Exists() || !tools.IsArray() {
		t.Fatalf("expected tools array, got %v", tools.Type)
	}
	arr := tools.Array()
	if len(arr) != 2 {
		t.Fatalf("expected 2 tools (no removal), got %d", len(arr))
	}
	if !gjson.GetBytes(out, "tool_choice").Exists() {
		t.Fatalf("expected tool_choice to be kept on images endpoint")
	}
}

func TestApplyPayloadConfigWithRoot_DisableImageGeneration_PayloadOverrideCanRestoreImageGeneration(t *testing.T) {
	cfg := &config.Config{
		SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationAll},
		Payload: config.PayloadConfig{
			OverrideRaw: []config.PayloadRule{
				{
					Models: []config.PayloadModelRule{
						{Name: "gpt-5.4", Protocol: "openai-response"},
					},
					Params: map[string]any{
						"tools":       `[{"type":"image_generation"},{"type":"function","name":"f1"}]`,
						"tool_choice": `{"type":"image_generation"}`,
					},
				},
			},
		},
	}
	payload := []byte(`{"tools":[{"type":"image_generation"},{"type":"function","name":"f1"}],"tool_choice":{"type":"image_generation"}}`)

	out := ApplyPayloadConfigWithRoot(cfg, "gpt-5.4", "openai-response", "", payload, nil, "", "")

	tools := gjson.GetBytes(out, "tools")
	if !tools.Exists() || !tools.IsArray() {
		t.Fatalf("expected tools array, got %v", tools.Type)
	}
	arr := tools.Array()
	if len(arr) != 2 {
		t.Fatalf("expected 2 tools after payload override, got %d", len(arr))
	}
	if got := arr[0].Get("type").String(); got != "image_generation" {
		t.Fatalf("expected first tool type=image_generation, got %q", got)
	}
	if !gjson.GetBytes(out, "tool_choice").Exists() {
		t.Fatalf("expected tool_choice to be restored by payload override")
	}
}
