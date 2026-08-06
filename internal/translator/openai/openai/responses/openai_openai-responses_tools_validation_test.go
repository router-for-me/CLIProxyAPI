package responses

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestValidateOpenAIResponsesToolsForChatTranslation_AllowsValidTools(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{
			name: "function and namespace",
			raw: `{
				"tools": [
					{"type":"function","name":"standalone_tool","parameters":{"type":"object","properties":{}}},
					{
						"type":"namespace",
						"name":"example_namespace",
						"tools":[
							{"type":"function","name":"lookup_value","parameters":{"type":"object","properties":{}}}
						]
					}
				]
			}`,
		},
		{
			name: "ordinary prefix is not qualified",
			raw:  `{"tools":[{"type":"namespace","name":"math","tools":[{"type":"function","name":"math_add"}]}]}`,
		},
		{
			name: "similar prefix is not qualified",
			raw:  `{"tools":[{"type":"namespace","name":"math","tools":[{"type":"function","name":"mathematics_lookup"}]}]}`,
		},
		{
			name: "similar standalone name does not collide",
			raw:  `{"tools":[{"type":"function","name":"math_add"},{"type":"namespace","name":"math","tools":[{"type":"function","name":"math_add"}]}]}`,
		},
		{
			name: "valid JSON without tools",
			raw:  `{"model":"test-model","input":"test"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := ValidateOpenAIResponsesToolsForChatTranslation([]byte(tt.raw)); err != nil {
				t.Fatalf("ValidateOpenAIResponsesToolsForChatTranslation() error = %v", err)
			}
		})
	}
}

func TestValidateOpenAIResponsesToolsForChatTranslation_FailsClosed(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{
			name: "malformed JSON",
			raw:  `{"tools":[`,
		},
		{
			name: "invalid tools JSON",
			raw:  `{"tools":{}}`,
		},
		{
			name: "empty namespace name",
			raw:  `{"tools":[{"type":"namespace","name":"","tools":[{"type":"function","name":"lookup_value"}]}]}`,
		},
		{
			name: "empty child name",
			raw:  `{"tools":[{"type":"namespace","name":"example_namespace","tools":[{"type":"function","name":""}]}]}`,
		},
		{
			name: "duplicate child name",
			raw:  `{"tools":[{"type":"namespace","name":"example_namespace","tools":[{"type":"function","name":"lookup_value"},{"type":"function","name":"lookup_value"}]}]}`,
		},
		{
			name: "flattened name collision",
			raw:  `{"tools":[{"type":"function","name":"math__math_add"},{"type":"namespace","name":"math","tools":[{"type":"function","name":"math_add"}]}]}`,
		},
		{
			name: "duplicate function name",
			raw:  `{"tools":[{"type":"function","name":"standalone_tool"},{"type":"function","name":"standalone_tool"}]}`,
		},
		{
			name: "pre-qualified child name",
			raw:  `{"tools":[{"type":"namespace","name":"math","tools":[{"type":"function","name":"math__add"}]}]}`,
		},
		{
			name: "namespace separator",
			raw:  `{"tools":[{"type":"namespace","name":"example__namespace","tools":[{"type":"function","name":"lookup_value"}]}]}`,
		},
		{
			name: "unsupported top-level type",
			raw:  `{"tools":[{"type":"unsupported_tool","name":"lookup_value"}]}`,
		},
		{
			name: "unsupported namespace child type",
			raw:  `{"tools":[{"type":"namespace","name":"example_namespace","tools":[{"type":"unsupported_tool","name":"lookup_value"}]}]}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := ValidateOpenAIResponsesToolsForChatTranslation([]byte(tt.raw)); err == nil {
				t.Fatalf("ValidateOpenAIResponsesToolsForChatTranslation() error = nil")
			}
		})
	}
}

func TestValidateOpenAIResponsesNamespaceTools_AllowsProviderSpecificTools(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{
			name: "top-level provider tool",
			raw:  `{"tools":[{"type":"web_search","name":"lookup_value"}]}`,
		},
		{
			name: "namespace provider child",
			raw:  `{"tools":[{"type":"namespace","name":"example_namespace","tools":[{"type":"custom","name":"lookup_value"}]}]}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := ValidateOpenAIResponsesNamespaceTools([]byte(tt.raw)); err != nil {
				t.Fatalf("ValidateOpenAIResponsesNamespaceTools() error = %v", err)
			}
		})
	}
}

func TestValidateOpenAIResponsesNamespaceTools_StillRejectsFunctionNamespaceCollisions(t *testing.T) {
	raw := []byte(`{"tools":[{"type":"function","name":"math__math_add"},{"type":"namespace","name":"math","tools":[{"type":"function","name":"math_add"}]}]}`)
	if err := ValidateOpenAIResponsesNamespaceTools(raw); err == nil {
		t.Fatalf("ValidateOpenAIResponsesNamespaceTools() error = nil")
	}
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_QualifiesOrdinaryPrefixes(t *testing.T) {
	raw := []byte(`{
		"tools": [
			{
				"type":"namespace",
				"name":"math",
				"tools":[
					{"type":"function","name":"math_add"},
					{"type":"function","name":"mathematics_lookup"}
				]
			}
		]
	}`)

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("test-model", raw, false)
	if got := gjson.GetBytes(out, "tools.0.function.name").String(); got != "math__math_add" {
		t.Fatalf("tools.0.function.name = %q, want math__math_add; output=%s", got, out)
	}
	if got := gjson.GetBytes(out, "tools.1.function.name").String(); got != "math__mathematics_lookup" {
		t.Fatalf("tools.1.function.name = %q, want math__mathematics_lookup; output=%s", got, out)
	}
}
