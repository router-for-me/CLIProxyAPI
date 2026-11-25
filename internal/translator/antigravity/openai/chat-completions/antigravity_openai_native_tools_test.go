package chat_completions

import (
	"context"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestOpenAIToAntigravityMixedGoogleSearchPreservesOnlyFunctions(t *testing.T) {
	input := []byte(`{
        "model":"gemini-4-neon-xhigh",
        "messages":[{"role":"user","content":"hi"}],
        "tools":[
            {"type":"function","function":{"name":"get_weather","parameters":{"type":"object","properties":{}}}},
            {"google_search":{}}
        ]
    }`)

	out, err := ConvertOpenAIRequestToAntigravity("gemini-4-neon-xhigh", input, false)
	if err != nil {
		t.Fatalf("convert error: %v", err)
	}
	if got := ojson.GetBytesNoCopy(out, "request.tools.#(functionDeclarations.0.name==\"get_weather\")").Raw; got == "" {
		t.Fatalf("function declaration was not preserved: %s", out)
	}
	for _, tool := range ojson.GetBytesNoCopy(out, "request.tools").Array() {
		if tool.Get("googleSearch").Exists() {
			t.Fatalf("googleSearch was mixed with function declarations: %s", out)
		}
	}
}

func TestOpenAIToAntigravityMixedFunctionAndNativeToolsPreserveAllTools(t *testing.T) {
	input := []byte(`{
        "model":"gemini-4-neon-xhigh",
        "messages":[{"role":"user","content":"run a search after checking ping"}],
        "tools":[
            {"type":"function","function":{"name":"ping","parameters":{"type":"object","properties":{}}}},
            {"code_execution":{}},
            {"url_context":{}}
        ]
    }`)

	out, err := ConvertOpenAIRequestToAntigravity("gemini-4-neon-xhigh", input, false)
	if err != nil {
		t.Fatalf("convert error: %v", err)
	}
	if got := ojson.GetBytesNoCopy(out, "request.tools.#(functionDeclarations.0.name==\"ping\")").Raw; got == "" {
		t.Fatalf("function declaration was not preserved: %s", out)
	}
	for _, path := range []string{"codeExecution", "urlContext"} {
		if got := ojson.GetBytesNoCopy(out, "request.tools.#("+path+")").Raw; got == "" {
			t.Fatalf("native %s tool was not preserved: %s", path, out)
		}
	}
}

func TestOpenAIToAntigravitySearchOnlyUsesDedicatedEnvelopeWhenSupported(t *testing.T) {
	const model = "issue-7892-search-capable"
	input := []byte(`{
        "model":"` + model + `",
        "messages":[
            {"role":"system","content":"Answer in metric units."},
            {"role":"user","content":"latest Go release"}
        ],
        "tools":[{"google_search":{"includedDomains":["go.dev"]}}]
    }`)

	envelope := sdktranslator.TranslateRequestEnvelope(context.Background(), sdktranslator.FormatOpenAI, sdktranslator.FormatAntigravity, sdktranslator.RequestEnvelope{
		Format:    sdktranslator.FormatOpenAI,
		Model:     model,
		Body:      input,
		ModelInfo: &registry.ModelInfo{ID: model, SupportsWebSearch: true},
	})
	if envelope.Err != nil {
		t.Fatalf("convert error: %v", envelope.Err)
	}
	if got := ojson.GetBytesNoCopy(envelope.Body, "requestType").String(); got != "web_search" {
		t.Fatalf("requestType = %q, want web_search: %s", got, envelope.Body)
	}
	if !ojson.GetBytesNoCopy(envelope.Body, "request.tools.0.googleSearch").Exists() {
		t.Fatalf("dedicated search envelope is missing googleSearch: %s", envelope.Body)
	}
	if got := ojson.GetBytesNoCopy(envelope.Body, "request.tools.0.googleSearch.includedDomains.0").String(); got != "go.dev" {
		t.Fatalf("included domain = %q, want go.dev: %s", got, envelope.Body)
	}
	parts := ojson.GetBytesNoCopy(envelope.Body, "request.systemInstruction.parts").Array()
	if len(parts) != 1 || parts[0].Get("text").String() != "Answer in metric units.\n\n"+antigravityOpenAIWebSearchSystemInstruction {
		t.Fatalf("system instructions were not preserved and appended: %s", envelope.Body)
	}
}

func TestOpenAIToAntigravitySearchOnlyHonorsToolChoice(t *testing.T) {
	for _, choice := range []string{`"none"`, `{"type":"function","function":{"name":"lookup"}}`} {
		t.Run(choice, func(t *testing.T) {
			const model = "issue-7892-search-choice"
			input := []byte(`{
        "model":"` + model + `",
        "messages":[{"role":"user","content":"latest Go release"}],
        "tools":[{"google_search":{}}],
        "tool_choice":` + choice + `
    }`)
			envelope := sdktranslator.TranslateRequestEnvelope(context.Background(), sdktranslator.FormatOpenAI, sdktranslator.FormatAntigravity, sdktranslator.RequestEnvelope{
				Format:    sdktranslator.FormatOpenAI,
				Model:     model,
				Body:      input,
				ModelInfo: &registry.ModelInfo{ID: model, SupportsWebSearch: true},
			})
			if envelope.Err != nil {
				t.Fatalf("convert error: %v", envelope.Err)
			}
			if got := ojson.GetBytesNoCopy(envelope.Body, "requestType").String(); got != "" {
				t.Fatalf("tool_choice %s built a web search envelope: %s", choice, envelope.Body)
			}
			if ojson.GetBytesNoCopy(envelope.Body, "request.tools.0.googleSearch").Exists() {
				t.Fatalf("tool_choice %s retained googleSearch: %s", choice, envelope.Body)
			}
		})
	}
}

func TestOpenAIToAntigravitySearchOnlyDoesNotLeaveFunctionToolConfig(t *testing.T) {
	const model = "issue-7892-search-required"
	input := []byte(`{
        "model":"` + model + `",
        "messages":[{"role":"user","content":"latest Go release"}],
        "tools":[{"google_search":{}}],
        "tool_choice":"required"
    }`)
	envelope := sdktranslator.TranslateRequestEnvelope(context.Background(), sdktranslator.FormatOpenAI, sdktranslator.FormatAntigravity, sdktranslator.RequestEnvelope{
		Format:    sdktranslator.FormatOpenAI,
		Model:     model,
		Body:      input,
		ModelInfo: &registry.ModelInfo{ID: model, SupportsWebSearch: true},
	})
	if envelope.Err != nil {
		t.Fatalf("convert error: %v", envelope.Err)
	}
	if ojson.GetBytesNoCopy(envelope.Body, "request.toolConfig.functionCallingConfig").Exists() {
		t.Fatalf("dedicated web search retained function calling config: %s", envelope.Body)
	}
	if !ojson.GetBytesNoCopy(envelope.Body, "request.tools.0.googleSearch").Exists() {
		t.Fatalf("dedicated web search tool was not retained: %s", envelope.Body)
	}
}

func TestOpenAIToAntigravitySearchOnlyUsesCapabilityFromLocalRegistry(t *testing.T) {
	const model = "issue-7892-search-registry"
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient("issue-7892-search-client", "antigravity", []*registry.ModelInfo{{ID: model, SupportsWebSearch: true}})
	t.Cleanup(func() { reg.UnregisterClient("issue-7892-search-client") })
	input := []byte(`{
        "model":"` + model + `",
        "messages":[{"role":"user","content":"latest Go release"}],
        "tools":[{"google_search":{}}]
    }`)

	envelope := sdktranslator.TranslateRequestEnvelope(context.Background(), sdktranslator.FormatOpenAI, sdktranslator.FormatAntigravity, sdktranslator.RequestEnvelope{
		Format: sdktranslator.FormatOpenAI,
		Model:  model,
		Body:   input,
	})
	if envelope.Err != nil {
		t.Fatalf("convert error: %v", envelope.Err)
	}
	if got := ojson.GetBytesNoCopy(envelope.Body, "requestType").String(); got != "web_search" {
		t.Fatalf("requestType = %q, want web_search: %s", got, envelope.Body)
	}
}

func TestOpenAIToAntigravityLocalVetoDisablesSearchWithoutEnvelopeMetadata(t *testing.T) {
	const model = "issue-7892-search-veto"
	disabled := false
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient("issue-7892-search-veto-client", "antigravity", []*registry.ModelInfo{{
		ID:                 model,
		SupportsWebSearch:  true,
		NativeCapabilities: &registry.NativeCapabilities{WebSearch: &disabled},
	}})
	t.Cleanup(func() { reg.UnregisterClient("issue-7892-search-veto-client") })
	input := []byte(`{"model":"` + model + `","messages":[{"role":"user","content":"search"}],"tools":[{"google_search":{}}]}`)
	out, err := ConvertOpenAIRequestToAntigravity(model, input, false)
	if err != nil {
		t.Fatalf("convert error: %v", err)
	}
	if ojson.GetBytesNoCopy(out, "requestType").String() == "web_search" || ojson.GetBytesNoCopy(out, "request.tools.0.googleSearch").Exists() {
		t.Fatalf("local web-search veto was ignored: %s", out)
	}
}

func TestOpenAIToAntigravityCompositeToolDoesNotBecomeDedicatedSearch(t *testing.T) {
	const model = "issue-7892-search-composite"
	input := []byte(`{"model":"` + model + `","messages":[{"role":"user","content":"search"}],"tools":[{"google_search":{},"code_execution":{}}]}`)
	envelope := sdktranslator.TranslateRequestEnvelope(context.Background(), sdktranslator.FormatOpenAI, sdktranslator.FormatAntigravity, sdktranslator.RequestEnvelope{
		Format:    sdktranslator.FormatOpenAI,
		Model:     model,
		Body:      input,
		ModelInfo: &registry.ModelInfo{ID: model, SupportsWebSearch: true},
	})
	if envelope.Err != nil {
		t.Fatalf("convert error: %v", envelope.Err)
	}
	if ojson.GetBytesNoCopy(envelope.Body, "requestType").String() == "web_search" {
		t.Fatalf("composite built-in tools became a dedicated search request: %s", envelope.Body)
	}
	if !ojson.GetBytesNoCopy(envelope.Body, "request.tools.0.codeExecution").Exists() {
		t.Fatalf("non-search tool was lost from composite tool fallback: %s", envelope.Body)
	}
	if ojson.GetBytesNoCopy(envelope.Body, "request.tools.#(googleSearch)").Exists() {
		t.Fatalf("googleSearch survived normal chat fallback: %s", envelope.Body)
	}
}

func TestOpenAIToAntigravityMultipleSearchToolsDoNotLoseNativeConfiguration(t *testing.T) {
	const model = "issue-7892-search-multiple"
	input := []byte(`{"model":"` + model + `","messages":[{"role":"user","content":"search"}],"tools":[{"google_search":{}},{"google_search":{"includedDomains":["go.dev"]}}]}`)
	envelope := sdktranslator.TranslateRequestEnvelope(context.Background(), sdktranslator.FormatOpenAI, sdktranslator.FormatAntigravity, sdktranslator.RequestEnvelope{
		Format:    sdktranslator.FormatOpenAI,
		Model:     model,
		Body:      input,
		ModelInfo: &registry.ModelInfo{ID: model, SupportsWebSearch: true},
	})
	if envelope.Err != nil {
		t.Fatalf("convert error: %v", envelope.Err)
	}
	if got := ojson.GetBytesNoCopy(envelope.Body, "requestType").String(); got != "" {
		t.Fatalf("multiple search tools silently collapsed into requestType %q: %s", got, envelope.Body)
	}
}

func TestOpenAIToAntigravitySearchOnlyStripsSearchWhenUnsupported(t *testing.T) {
	const model = "issue-7892-search-disabled"
	disabled := false
	input := []byte(`{
        "model":"` + model + `",
        "messages":[{"role":"user","content":"latest Go release"}],
        "tools":[{"google_search":{}}]
    }`)

	envelope := sdktranslator.TranslateRequestEnvelope(context.Background(), sdktranslator.FormatOpenAI, sdktranslator.FormatAntigravity, sdktranslator.RequestEnvelope{
		Format: sdktranslator.FormatOpenAI,
		Model:  model,
		Body:   input,
		ModelInfo: &registry.ModelInfo{
			ID:                 model,
			NativeCapabilities: &registry.NativeCapabilities{WebSearch: &disabled},
			SupportsWebSearch:  true,
		},
	})
	if envelope.Err != nil {
		t.Fatalf("convert error: %v", envelope.Err)
	}
	if got := ojson.GetBytesNoCopy(envelope.Body, "requestType").String(); got != "" {
		t.Fatalf("requestType = %q, want normal chat request: %s", got, envelope.Body)
	}
	for _, tool := range ojson.GetBytesNoCopy(envelope.Body, "request.tools").Array() {
		if tool.Get("googleSearch").Exists() {
			t.Fatalf("unsupported googleSearch was retained in normal chat request: %s", envelope.Body)
		}
	}
}

func TestOpenAIToAntigravityStandaloneNativeToolsRemainAvailable(t *testing.T) {
	for _, nativeTool := range []struct {
		name  string
		input string
		path  string
	}{
		{name: "code execution", input: `{"code_execution":{}}`, path: "codeExecution"},
		{name: "url context", input: `{"url_context":{}}`, path: "urlContext"},
	} {
		t.Run(nativeTool.name, func(t *testing.T) {
			input := []byte(`{"messages":[{"role":"user","content":"hi"}],"tools":[` + nativeTool.input + `]}`)
			out, err := ConvertOpenAIRequestToAntigravity("gemini-4-neon-xhigh", input, false)
			if err != nil {
				t.Fatalf("convert error: %v", err)
			}
			if !ojson.GetBytesNoCopy(out, "request.tools.0."+nativeTool.path).Exists() {
				t.Fatalf("standalone native tool was removed: %s", out)
			}
		})
	}
}

func TestOpenAIToAntigravitySearchOnlyAllowsNullToolChoice(t *testing.T) {
	const model = "issue-8115-search-null-choice"
	input := []byte(`{
		"model":"` + model + `",
		"messages":[{"role":"user","content":"latest Go release"}],
		"tools":[{"google_search":{}}],
		"tool_choice":null
	}`)
	envelope := sdktranslator.TranslateRequestEnvelope(context.Background(), sdktranslator.FormatOpenAI, sdktranslator.FormatAntigravity, sdktranslator.RequestEnvelope{
		Format:    sdktranslator.FormatOpenAI,
		Model:     model,
		Body:      input,
		ModelInfo: &registry.ModelInfo{ID: model, SupportsWebSearch: true},
	})
	if envelope.Err != nil {
		t.Fatalf("convert error: %v", envelope.Err)
	}
	if got := ojson.GetBytesNoCopy(envelope.Body, "requestType").String(); got != "web_search" {
		t.Fatalf("requestType = %q, want web_search: %s", got, envelope.Body)
	}
	if !ojson.GetBytesNoCopy(envelope.Body, "request.tools.0.googleSearch").Exists() {
		t.Fatalf("dedicated search envelope is missing googleSearch: %s", envelope.Body)
	}
}

func TestOpenAIToAntigravitySearchOnlyAllowedToolsToolChoice(t *testing.T) {
	const model = "issue-8115-search-allowed-tools"
	tests := []struct {
		name       string
		toolChoice string
		wantSearch bool
	}{
		{
			name:       "allowed_tools listing google_search permits search",
			toolChoice: `{"type":"allowed_tools","allowed_tools":{"mode":"auto","tools":[{"type":"google_search"}]}}`,
			wantSearch: true,
		},
		{
			name:       "allowed_tools with none mode disables search",
			toolChoice: `{"type":"allowed_tools","allowed_tools":{"mode":"none","tools":[{"type":"google_search"}]}}`,
			wantSearch: false,
		},
		{
			name:       "allowed_tools listing only function disables search",
			toolChoice: `{"type":"allowed_tools","allowed_tools":{"mode":"auto","tools":[{"type":"function","function":{"name":"lookup"}}]}}`,
			wantSearch: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := []byte(`{"model":"` + model + `","messages":[{"role":"user","content":"latest Go release"}],"tools":[{"google_search":{}}],"tool_choice":` + tt.toolChoice + `}`)
			envelope := sdktranslator.TranslateRequestEnvelope(context.Background(), sdktranslator.FormatOpenAI, sdktranslator.FormatAntigravity, sdktranslator.RequestEnvelope{
				Format:    sdktranslator.FormatOpenAI,
				Model:     model,
				Body:      input,
				ModelInfo: &registry.ModelInfo{ID: model, SupportsWebSearch: true},
			})
			if envelope.Err != nil {
				t.Fatalf("convert error: %v", envelope.Err)
			}
			gotSearch := ojson.GetBytesNoCopy(envelope.Body, "requestType").String() == "web_search"
			if gotSearch != tt.wantSearch {
				t.Fatalf("web_search = %t, want %t: %s", gotSearch, tt.wantSearch, envelope.Body)
			}
		})
	}
}

func TestOpenAIToAntigravityMixedToolsAllowedToolsOnlySearchUsesDedicatedEnvelope(t *testing.T) {
	const model = "issue-8115-mixed-allowed-search"
	input := []byte(`{
		"model":"` + model + `",
		"messages":[{"role":"user","content":"latest Go release"}],
		"tools":[
			{"type":"function","function":{"name":"lookup","parameters":{"type":"object","properties":{}}}},
			{"google_search":{"includedDomains":["go.dev"]}}
		],
		"tool_choice":{
			"type":"allowed_tools",
			"allowed_tools":{
				"mode":"auto",
				"tools":[{"type":"google_search"}]
			}
		}
	}`)
	envelope := sdktranslator.TranslateRequestEnvelope(context.Background(), sdktranslator.FormatOpenAI, sdktranslator.FormatAntigravity, sdktranslator.RequestEnvelope{
		Format:    sdktranslator.FormatOpenAI,
		Model:     model,
		Body:      input,
		ModelInfo: &registry.ModelInfo{ID: model, SupportsWebSearch: true},
	})
	if envelope.Err != nil {
		t.Fatalf("convert error: %v", envelope.Err)
	}
	if got := ojson.GetBytesNoCopy(envelope.Body, "requestType").String(); got != "web_search" {
		t.Fatalf("requestType = %q, want web_search: %s", got, envelope.Body)
	}
	if got := ojson.GetBytesNoCopy(envelope.Body, "request.tools.0.googleSearch.includedDomains.0").String(); got != "go.dev" {
		t.Fatalf("included domain = %q, want go.dev: %s", got, envelope.Body)
	}
	if ojson.GetBytesNoCopy(envelope.Body, "request.tools.#(functionDeclarations)").Exists() {
		t.Fatalf("disallowed functionDeclarations survived search envelope: %s", envelope.Body)
	}
}
