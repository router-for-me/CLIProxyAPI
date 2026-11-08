package helps

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestValidateCodexHostedImageGenerationDoc(t *testing.T) {
	for _, tc := range []struct {
		name   string
		body   string
		reject bool
	}{
		{"explicit", `{"tools":[{"type":"image_generation"}],"tool_choice":{"type":"image_generation"}}`, true},
		{"required", `{"tools":[{"type":"image_generation"}],"tool_choice":"required"}`, true},
		{"explicit_with_other_tools", `{"tools":[{"type":"function","name":"lookup"},{"type":"image_generation"}],"tool_choice":{"type":"image_generation"}}`, true},
		{"allowed_required", `{"tools":[{"type":"function","name":"lookup"},{"type":"image_generation"}],"tool_choice":{"type":"allowed_tools","mode":"required","tools":[{"type":"image_generation"}]}}`, true},
		{"additional_explicit", `{"input":[{"type":"additional_tools","tools":[{"type":"image_generation"}]}],"tool_choice":{"type":"image_generation"}}`, true},
		{"additional_required", `{"input":[{"type":"additional_tools","tools":[{"type":"image_generation"}]}],"tool_choice":"required"}`, true},
		{"omitted", `{"tools":[{"type":"image_generation"}]}`, false},
		{"auto", `{"tools":[{"type":"image_generation"}],"tool_choice":"auto"}`, false},
		{"none", `{"tools":[{"type":"image_generation"}],"tool_choice":"none"}`, false},
		{"mixed_required", `{"tools":[{"type":"image_generation"},{"type":"function","name":"lookup"}],"tool_choice":"required"}`, false},
		{"additional_mixed_required", `{"tools":[{"type":"image_generation"}],"input":[{"type":"additional_tools","tools":[{"type":"function","name":"lookup"}]}],"tool_choice":"required"}`, false},
		{"allowed_auto", `{"tools":[{"type":"image_generation"}],"tool_choice":{"type":"allowed_tools","mode":"auto","tools":[{"type":"image_generation"}]}}`, false},
		{"allowed_mixed", `{"tools":[{"type":"image_generation"},{"type":"function","name":"lookup"}],"tool_choice":{"type":"allowed_tools","mode":"required","tools":[{"type":"image_generation"},{"type":"function","name":"lookup"}]}}`, false},
		{"allowed_excludes_images", `{"tools":[{"type":"image_generation"},{"type":"function","name":"lookup"}],"tool_choice":{"type":"allowed_tools","mode":"required","tools":[{"type":"function","name":"lookup"}]}}`, false},
		{"allowed_empty", `{"tools":[{"type":"image_generation"}],"tool_choice":{"type":"allowed_tools","mode":"required","tools":[]}}`, false},
		{"filtered_declaration", `{"tool_choice":{"type":"image_generation"}}`, false},
		{"standalone_function", `{"tools":[{"type":"function","name":"image_gen.imagegen"}],"tool_choice":"required"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, model := range []string{"gpt-6-sol", "gpt-6.1-sol", "gpt-5.6-sol", "gpt-reserve", "codex/gpt-6-astra(high)"} {
				doc := ojson.NewDocString(tc.body)
				_ = doc.Set("model", model)
				before := doc.String()
				errValidate := ValidateCodexHostedImageGenerationDoc(doc, nil, nil, true)
				if (errValidate != nil) != tc.reject {
					t.Fatalf("model=%s error=%v, reject=%t", model, errValidate, tc.reject)
				}
				if doc.String() != before {
					t.Fatal("validation changed the configured payload")
				}
				if errValidate != nil {
					var requestErr cliproxyexecutor.RequestScopedError
					var statusErr interface{ StatusCode() int }
					if !errors.As(errValidate, &requestErr) || !requestErr.IsRequestScoped() || !errors.As(errValidate, &statusErr) || statusErr.StatusCode() != http.StatusBadRequest {
						t.Fatalf("expected request-scoped HTTP 400, got %v", errValidate)
					}
					if !strings.Contains(errValidate.Error(), "/v1/images/generations") || !strings.Contains(errValidate.Error(), "/v1/images/edits") {
						t.Fatalf("missing Images API guidance: %v", errValidate)
					}
				}
			}
		})
	}
}

func TestValidateCodexHostedImageGenerationRouteAndPassthrough(t *testing.T) {
	for _, tc := range []struct {
		name        string
		model       string
		header      string
		metadata    any
		mode        config.DisableImageGenerationMode
		nativeCodex bool
		reject      bool
	}{
		{name: "legacy_model", model: "gpt-5.4", nativeCodex: true},
		{name: "lite_header", model: "gpt-5.4", header: "true", nativeCodex: true, reject: true},
		{name: "lite_metadata_bool", model: "gpt-5.4", metadata: true, nativeCodex: true, reject: true},
		{name: "lite_metadata_string", model: "gpt-5.4", metadata: "true", nativeCodex: true, reject: true},
		{name: "false_hints", model: "gpt-5.4", header: "false", metadata: false, nativeCodex: true},
		{name: "api_key_or_siwc", model: "gpt-6-sol"},
		{name: "explicit_passthrough", model: "gpt-6-sol", mode: config.DisableImageGenerationPassthrough, nativeCodex: true},
		{name: "configured_tool_restored_in_chat_mode", model: "gpt-6-sol", mode: config.DisableImageGenerationChat, nativeCodex: true, reject: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc := ojson.NewDocString(`{"tools":[{"type":"image_generation"}],"tool_choice":{"type":"image_generation"}}`)
			_ = doc.Set("model", tc.model)
			if tc.metadata != nil {
				_ = doc.Set("client_metadata.ws_request_header_x_openai_internal_codex_responses_lite", tc.metadata)
			}
			headers := make(http.Header)
			headers.Set("X-OpenAI-Internal-Codex-Responses-Lite", tc.header)
			cfg := &config.Config{SDKConfig: config.SDKConfig{DisableImageGeneration: tc.mode}}
			if errValidate := ValidateCodexHostedImageGenerationDoc(doc, headers, cfg, tc.nativeCodex); (errValidate != nil) != tc.reject {
				t.Fatalf("error=%v, reject=%t", errValidate, tc.reject)
			}
		})
	}
}
