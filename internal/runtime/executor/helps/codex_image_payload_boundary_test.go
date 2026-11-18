package helps

import (
	"net/http"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestCodexImagePayloadCompatibility(t *testing.T) {
	const original = `{"model":"gpt-6-astra","input":[{"role":"user","content":"hero"}]}`
	const injected = `{"model":"gpt-6-astra","input":[{"role":"user","content":"hero"}],"tools":[{"type":"image_generation"}]}`
	const originalWithFunction = `{"model":"gpt-6-astra","input":[{"role":"user","content":"hero"}],"tools":[{"type":"function","name":"lookup"}]}`
	const injectedWithFunction = `{"model":"gpt-6-astra","input":[{"role":"user","content":"hero"}],"tools":[{"type":"function","name":"lookup"},{"type":"image_generation"}]}`
	const liteRouteModel = "test-codex-image-payload-lite-route"
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(t.Name(), "codex", []*registry.ModelInfo{{
		ID: liteRouteModel, Type: "codex",
		Config: &registry.ModelConfig{OverrideHeader: map[string]string{codexResponsesLiteHeaderName: "true"}},
	}})
	t.Cleanup(func() { reg.UnregisterClient(t.Name()) })

	for _, tc := range []struct {
		name             string
		original         string
		body             string
		executor         string
		defaults         map[string]any
		overrides        map[string]any
		rawOverrides     map[string]any
		filters          []string
		exist            []string
		headers          http.Header
		auth             *cliproxyauth.Auth
		passthrough      bool
		wantTypes        []string
		wantEffort       string
		wantModel        string
		wantOutputFormat string
	}{
		{
			name:      "compatible_automatic_tool",
			wantTypes: []string{"image_generation"},
		},
		{
			name:       "default_reasoning_retracts_automatic_tool",
			defaults:   map[string]any{"reasoning.effort": "medium"},
			wantEffort: "medium",
		},
		{
			name:      "conditional_override_observes_prepared_tools_once",
			overrides: map[string]any{"reasoning.effort": "high"},
			exist:     []string{"tools"}, wantEffort: "high",
		},
		{
			name:      "original_empty_tools_remains_empty",
			original:  `{"model":"gpt-6-astra","input":[{"role":"user","content":"hero"}],"tools":[]}`,
			overrides: map[string]any{"reasoning.effort": "medium"},
			wantTypes: []string{}, wantEffort: "medium",
		},
		{
			name:      "caller_image_tool_is_preserved",
			original:  injected,
			overrides: map[string]any{"reasoning.effort": "medium"},
			wantTypes: []string{"image_generation"}, wantEffort: "medium",
		},
		{
			name: "explicit_tools_override_is_preserved",
			overrides: map[string]any{
				"reasoning.effort": "medium",
				"tools":            []any{map[string]any{"type": "image_generation"}},
			},
			wantTypes: []string{"image_generation"}, wantEffort: "medium",
		},
		{
			name:      "explicit_image_tool_field_is_preserved",
			overrides: map[string]any{"reasoning.effort": "medium", "tools.0.output_format": "webp"},
			wantTypes: []string{"image_generation"}, wantEffort: "medium", wantOutputFormat: "webp",
		},
		{
			name:      "explicit_image_tool_choice_is_preserved",
			overrides: map[string]any{"reasoning.effort": "medium", "tool_choice": map[string]any{"type": "image_generation"}},
			wantTypes: []string{"image_generation"}, wantEffort: "medium",
		},
		{
			name:       "auto_tool_choice_does_not_force_image_tool",
			overrides:  map[string]any{"reasoning.effort": "medium", "tool_choice": "auto"},
			wantEffort: "medium",
		},
		{
			name:     "other_tools_are_preserved",
			original: originalWithFunction, body: injectedWithFunction,
			overrides: map[string]any{"reasoning.effort": "medium"},
			wantTypes: []string{"function"}, wantEffort: "medium",
		},
		{
			name:     "filter_shifts_automatic_tool_index",
			original: originalWithFunction, body: injectedWithFunction,
			overrides: map[string]any{"reasoning.effort": "medium"},
			filters:   []string{"tools.0"}, wantEffort: "medium",
		},
		{
			name:     "filter_shifts_explicitly_modified_tool_index",
			original: originalWithFunction, body: injectedWithFunction,
			overrides: map[string]any{"reasoning.effort": "medium", "tools.1.output_format": "webp"},
			filters:   []string{"tools.0"},
			wantTypes: []string{"image_generation"}, wantEffort: "medium", wantOutputFormat: "webp",
		},
		{
			name:      "filtered_tools_are_not_restored",
			overrides: map[string]any{"reasoning.effort": "medium"},
			filters:   []string{"tools"}, wantEffort: "medium",
		},
		{
			name:      "spark_model_override_retracts_automatic_tool",
			overrides: map[string]any{"model": "gpt-5.3-codex-spark"},
			wantModel: "gpt-5.3-codex-spark",
		},
		{
			name:      "model_override_header_retracts_automatic_tool",
			overrides: map[string]any{"model": liteRouteModel},
			wantModel: liteRouteModel,
		},
		{
			name: "credential_lite_header_is_respected",
			auth: &cliproxyauth.Auth{Attributes: map[string]string{"header:" + codexResponsesLiteHeaderName: "true"}},
		},
		{
			name:      "credential_override_can_disable_client_lite_header",
			headers:   http.Header{http.CanonicalHeaderKey(codexResponsesLiteHeaderName): []string{"true"}},
			auth:      &cliproxyauth.Auth{Attributes: map[string]string{"header:" + codexResponsesLiteHeaderName: "false"}},
			wantTypes: []string{"image_generation"},
		},
		{
			name:         "configuration_update_reasoning_retracts_automatic_tool",
			rawOverrides: map[string]any{"input.-1": `{"type":"configuration_update","reasoning":{"effort":"medium"}}`},
		},
		{
			name:        "passthrough_does_not_retract_tools",
			passthrough: true,
			overrides:   map[string]any{"reasoning.effort": "medium"},
			wantTypes:   []string{"image_generation"}, wantEffort: "medium",
		},
		{
			name:      "other_executors_do_not_retract_tools",
			executor:  "xai",
			overrides: map[string]any{"reasoning.effort": "medium"},
			wantTypes: []string{"image_generation"}, wantEffort: "medium",
		},
	} {
		for _, mode := range []string{"bytes", "doc"} {
			t.Run(tc.name+"/"+mode, func(t *testing.T) {
				cfg := &config.Config{}
				if tc.passthrough {
					cfg.DisableImageGeneration = config.DisableImageGenerationPassthrough
				}
				models := []config.PayloadModelRule{{Name: "*", Exist: tc.exist}}
				if tc.defaults != nil {
					cfg.Payload.Default = []config.PayloadRule{{Models: models, Params: tc.defaults}}
				}
				if tc.overrides != nil {
					cfg.Payload.Override = []config.PayloadRule{{Models: models, Params: tc.overrides}}
				}
				if tc.rawOverrides != nil {
					cfg.Payload.OverrideRaw = []config.PayloadRule{{Models: models, Params: tc.rawOverrides}}
				}
				if tc.filters != nil {
					cfg.Payload.Filter = []config.PayloadFilterRule{{Models: models, Params: tc.filters}}
				}
				// A non-idempotent replacement catches accidental preflight or repeated evaluation.
				cfg.Payload.Replace = []config.PayloadReplaceRule{{
					Models:       []config.PayloadModelRule{{Name: "*"}},
					Target:       config.PayloadReplaceTarget{Role: "user", All: true},
					Replacements: []config.PayloadReplacement{{"hero": "hero!"}},
				}}
				source, body, executor := tc.original, tc.body, tc.executor
				if source == "" {
					source = original
				}
				if body == "" {
					body = injected
				}
				if executor == "" {
					executor = "codex"
				}
				callbacks := 0
				finalize := NewPayloadFinalizer(cfg, executor, "gpt-6-astra", "codex", "", []byte(source),
					cliproxyexecutor.Request{Model: "gpt-6-astra"}, cliproxyexecutor.Options{
						Headers:  tc.headers,
						Metadata: map[string]any{cliproxyexecutor.FinalizedResponseFormatCallbackMetadataKey: func(string) { callbacks++ }},
					}, tc.auth)
				before := PayloadConfigEvaluationCount()
				var doc *ojson.Doc
				if mode == "doc" {
					doc = ojson.NewDocString(body)
					finalize.FinalizeDoc(doc)
				} else {
					doc = ojson.NewDoc(finalize([]byte(body)))
				}
				if got := PayloadConfigEvaluationCount() - before; got != 1 || callbacks != 1 {
					t.Fatalf("evaluations=%d callbacks=%d, want one of each", got, callbacks)
				}
				if got := doc.Get("input.0.content").String(); got != "hero!" {
					t.Fatalf("replacement=%q, want hero!", got)
				}
				if got := doc.Get("reasoning.effort").String(); got != tc.wantEffort {
					t.Fatalf("reasoning.effort=%q, want %q", got, tc.wantEffort)
				}
				wantModel := tc.wantModel
				if wantModel == "" {
					wantModel = "gpt-6-astra"
				}
				if got := doc.Get("model").String(); got != wantModel {
					t.Fatalf("model=%q, want %q", got, wantModel)
				}
				tools := doc.Get("tools")
				if tc.wantTypes == nil {
					if tools.Exists() {
						t.Fatalf("unexpected tools after reconciliation: %s", tools.Raw)
					}
					return
				}
				items := tools.Array()
				if !tools.IsArray() || len(items) != len(tc.wantTypes) {
					t.Fatalf("tools=%s, want types %v", tools.Raw, tc.wantTypes)
				}
				for i, wantType := range tc.wantTypes {
					if got := items[i].Get("type").String(); got != wantType {
						t.Fatalf("tools[%d].type=%q, want %q", i, got, wantType)
					}
				}
				if tc.wantOutputFormat != "" && doc.Get("tools.0.output_format").String() != tc.wantOutputFormat {
					t.Fatalf("explicit image parameters lost: %s", tools.Raw)
				}
			})
		}
	}
}
