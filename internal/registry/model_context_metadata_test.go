package registry

import "testing"

func TestConvertModelToMapUsesProviderContextFields(t *testing.T) {
	modelRegistry := &ModelRegistry{}
	for _, testCase := range []struct {
		name        string
		model       *ModelInfo
		handlerType string
		field       string
		want        int
	}{
		{
			name:        "Gemini catalog limit in OpenAI format",
			model:       &ModelInfo{ID: "gemini-image", InputTokenLimit: 131072},
			handlerType: "openai",
			field:       "context_length",
			want:        131072,
		},
		{
			name:        "Gemini catalog limit in Claude format",
			model:       &ModelInfo{ID: "gemini-image", InputTokenLimit: 131072},
			handlerType: "claude",
			field:       "max_input_tokens",
			want:        131072,
		},
		{
			name:        "context length in Gemini format",
			model:       &ModelInfo{ID: "gemini-3.8-flash", ContextLength: 1048576},
			handlerType: "gemini",
			field:       "inputTokenLimit",
			want:        1048576,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			result := modelRegistry.convertModelToMap(testCase.model, testCase.handlerType)
			if got, ok := result[testCase.field].(int); !ok || got != testCase.want {
				t.Fatalf("%s = %#v, want %d", testCase.field, result[testCase.field], testCase.want)
			}
		})
	}
}
