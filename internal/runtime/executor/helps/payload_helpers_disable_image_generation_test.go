package helps

import (
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
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

func TestApplyPayloadConfigWithRequest_HeaderGateRequiresWildcardMatch(t *testing.T) {
	cfg := &config.Config{
		Payload: config.PayloadConfig{
			Override: []config.PayloadRule{
				{
					Models: []config.PayloadModelRule{
						{
							Name:     "gpt-*",
							Protocol: "openai",
							Headers: map[string]string{
								"X-Client-Tier": "tenant-*-region-*",
							},
						},
					},
					Params: map[string]any{
						"metadata.enabled": true,
					},
				},
			},
		},
	}
	payload := []byte(`{"model":"gpt-5.4"}`)
	headers := http.Header{}
	headers.Set("X-Client-Tier", "tenant-alpha-region-us")

	out := ApplyPayloadConfigWithRequest(cfg, "gpt-5.4", "openai", "responses", "", payload, nil, "", "", headers)
	if !gjson.GetBytes(out, "metadata.enabled").Bool() {
		t.Fatalf("expected header-matched payload rule to apply, payload=%s", string(out))
	}

	headers.Set("X-Client-Tier", "tenant-alpha")
	out = ApplyPayloadConfigWithRequest(cfg, "gpt-5.4", "openai", "responses", "", payload, nil, "", "", headers)
	if gjson.GetBytes(out, "metadata.enabled").Exists() {
		t.Fatalf("expected header-mismatched payload rule to be skipped, payload=%s", string(out))
	}
}

func TestApplyPayloadConfigWithRequest_FormProtocolGateUsesSourceProtocol(t *testing.T) {
	cfg := &config.Config{
		Payload: config.PayloadConfig{
			Override: []config.PayloadRule{
				{
					Models: []config.PayloadModelRule{
						{Name: "gpt-*", Protocol: "openai", FormProtocol: "responses"},
					},
					Params: map[string]any{
						"metadata.source": "responses",
					},
				},
				{
					Models: []config.PayloadModelRule{
						{Name: "gpt-*", Protocol: "openai", FormProtocol: "openai"},
					},
					Params: map[string]any{
						"metadata.source": "openai",
					},
				},
			},
		},
	}
	payload := []byte(`{"model":"gpt-5.4"}`)

	out := ApplyPayloadConfigWithRequest(cfg, "gpt-5.4", "openai", "openai-response", "", payload, nil, "", "", nil)
	if got := gjson.GetBytes(out, "metadata.source").String(); got != "responses" {
		t.Fatalf("metadata.source = %q, want responses; payload=%s", got, string(out))
	}

	out = ApplyPayloadConfigWithRequest(cfg, "gpt-5.4", "openai", "openai", "", payload, nil, "", "", nil)
	if got := gjson.GetBytes(out, "metadata.source").String(); got != "openai" {
		t.Fatalf("metadata.source = %q, want openai; payload=%s", got, string(out))
	}
}

func TestApplyPayloadConfigWithRequest_PayloadConditionsNarrowRule(t *testing.T) {
	cfg := &config.Config{
		Payload: config.PayloadConfig{
			Override: []config.PayloadRule{
				{
					Models: []config.PayloadModelRule{
						{
							Name: "gpt-*",
							Match: []map[string]any{
								{"metadata.client": "codex"},
								{"tools.#(type==\"web_search\").enabled": true},
							},
							NotMatch: []map[string]any{
								{"metadata.mode": "dev"},
							},
							Exist: []string{
								"tools.#(type==\"web_search\").type",
							},
							NotExist: []string{
								"metadata.missing",
								"metadata.null_value",
							},
						},
					},
					Params: map[string]any{
						"metadata.applied": true,
					},
				},
			},
		},
	}
	payload := []byte(`{"model":"gpt-5.4","metadata":{"client":"codex","mode":"prod","null_value":null},"tools":[{"type":"function"},{"type":"web_search","enabled":true}]}`)

	out := ApplyPayloadConfigWithRequest(cfg, "gpt-5.4", "openai", "responses", "", payload, nil, "", "", nil)
	if !gjson.GetBytes(out, "metadata.applied").Bool() {
		t.Fatalf("expected payload condition-matched rule to apply, payload=%s", string(out))
	}
}

func TestApplyPayloadConfigWithRequest_PayloadConditionsSkipRule(t *testing.T) {
	testCases := []struct {
		name  string
		model config.PayloadModelRule
	}{
		{
			name: "match mismatch",
			model: config.PayloadModelRule{
				Name:  "gpt-*",
				Match: []map[string]any{{"metadata.client": "codex"}},
			},
		},
		{
			name: "not-match matched",
			model: config.PayloadModelRule{
				Name:     "gpt-*",
				NotMatch: []map[string]any{{"metadata.mode": "dev"}},
			},
		},
		{
			name: "exist missing",
			model: config.PayloadModelRule{
				Name:  "gpt-*",
				Exist: []string{"metadata.missing"},
			},
		},
		{
			name: "exist null",
			model: config.PayloadModelRule{
				Name:  "gpt-*",
				Exist: []string{"metadata.null_value"},
			},
		},
		{
			name: "not-exist present",
			model: config.PayloadModelRule{
				Name:     "gpt-*",
				NotExist: []string{"metadata.client"},
			},
		},
	}
	payload := []byte(`{"model":"gpt-5.4","metadata":{"client":"other","mode":"dev","null_value":null}}`)

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{
				Payload: config.PayloadConfig{
					Override: []config.PayloadRule{
						{
							Models: []config.PayloadModelRule{tc.model},
							Params: map[string]any{
								"metadata.applied": true,
							},
						},
					},
				},
			}

			out := ApplyPayloadConfigWithRequest(cfg, "gpt-5.4", "openai", "responses", "", payload, nil, "", "", nil)
			if gjson.GetBytes(out, "metadata.applied").Exists() {
				t.Fatalf("expected payload condition-mismatched rule to be skipped, payload=%s", string(out))
			}
		})
	}
}
