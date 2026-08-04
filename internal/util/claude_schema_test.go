package util

import "testing"

func TestNormalizeClaudeToolInputSchema(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name: "root anyOf without type",
			input: `{
				"anyOf": [
					{"type":"object","properties":{"a":{"type":"string"}}},
					{"type":"object","properties":{"b":{"type":"integer"}}}
				]
			}`,
			expected: `{
				"type":"object",
				"properties":{
					"a":{"type":"string"},
					"b":{"type":"integer"}
				}
			}`,
		},
		{
			name: "root oneOf keeps nested union",
			input: `{
				"type":"object",
				"properties":{
					"nested":{"oneOf":[{"type":"string"},{"type":"number"}]}
				},
				"oneOf":[
					{"properties":{"a":{"type":"string"}},"required":["a"]},
					{"properties":{"b":{"type":"string"}},"required":["b"]}
				]
			}`,
			expected: `{
				"type":"object",
				"properties":{
					"nested":{"oneOf":[{"type":"string"},{"type":"number"}]},
					"a":{"type":"string"},
					"b":{"type":"string"}
				}
			}`,
		},
		{
			name: "root anyOf drops alternative required fields",
			input: `{
				"type":"object",
				"properties":{"a":{"type":"string"},"b":{"type":"string"}},
				"anyOf":[{"required":["a"]},{"required":["b"]}]
			}`,
			expected: `{
				"type":"object",
				"properties":{"a":{"type":"string"},"b":{"type":"string"}}
			}`,
		},
		{
			name: "root allOf merges properties and required fields",
			input: `{
				"type":"object",
				"properties":{"base":{"type":"boolean"}},
				"required":["base"],
				"allOf":[
					{"type":"object","properties":{"a":{"type":"string"}},"required":["a"]},
					{"properties":{"b":{"type":"integer"}},"required":["a","b"]}
				]
			}`,
			expected: `{
				"type":"object",
				"properties":{
					"base":{"type":"boolean"},
					"a":{"type":"string"},
					"b":{"type":"integer"}
				},
				"required":["base","a","b"]
			}`,
		},
		{
			name: "ordinary object schema",
			input: `{
				"type":"object",
				"properties":{"query":{"type":"string"}},
				"required":["query"],
				"additionalProperties":false
			}`,
			expected: `{
				"type":"object",
				"properties":{"query":{"type":"string"}},
				"required":["query"],
				"additionalProperties":false
			}`,
		},
		{
			name:     "invalid schema",
			input:    `{"type":`,
			expected: `{"type":"object","properties":{}}`,
		},
		{
			name:     "boolean schema",
			input:    `true`,
			expected: `{"type":"object","properties":{}}`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			actual := NormalizeClaudeToolInputSchema([]byte(test.input))
			compareJSON(t, test.expected, string(actual))
		})
	}
}
