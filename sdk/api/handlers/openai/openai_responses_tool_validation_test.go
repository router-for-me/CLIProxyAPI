package openai

import "testing"

func TestValidateOpenAIResponsesToolsForChatTranslation_AllowsValidTools(t *testing.T) {
	raw := []byte(`{
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
	}`)

	if err := validateOpenAIResponsesToolsForChatTranslation(raw); err != nil {
		t.Fatalf("validateOpenAIResponsesToolsForChatTranslation() error = %v", err)
	}
}

func TestValidateOpenAIResponsesToolsForChatTranslation_FailsClosed(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
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
			raw:  `{"tools":[{"type":"function","name":"example_namespace__lookup_value"},{"type":"namespace","name":"example_namespace","tools":[{"type":"function","name":"lookup_value"}]}]}`,
		},
		{
			name: "duplicate function name",
			raw:  `{"tools":[{"type":"function","name":"standalone_tool"},{"type":"function","name":"standalone_tool"}]}`,
		},
		{
			name: "pre-qualified child name",
			raw:  `{"tools":[{"type":"namespace","name":"example_namespace","tools":[{"type":"function","name":"example_namespace__lookup_value"}]}]}`,
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
			if err := validateOpenAIResponsesToolsForChatTranslation([]byte(tt.raw)); err == nil {
				t.Fatalf("validateOpenAIResponsesToolsForChatTranslation() error = nil")
			}
		})
	}
}
