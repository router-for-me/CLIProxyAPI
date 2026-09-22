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

func TestHasUnsupportedUnicodePropertyEscape(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
		want    bool
	}{
		{
			name:    "Artifact regex with multiple \\p escapes",
			pattern: `^(?!__.*__$)[^\p{Cc}\p{Cf}\p{Zl}\p{Zp}"\./[\]]{1,200}$`,
			want:    true,
		},
		{
			name:    "Uppercase \\P property escape",
			pattern: `^\P{L}+$`,
			want:    true,
		},
		{
			name:    "Escaped backslash before p is literal and safe",
			pattern: `^\\p{Cc}$`,
			want:    false,
		},
		{
			name:    "Three backslashes means literal backslash plus real \\p escape",
			pattern: `^\\\p{Cc}$`,
			want:    true,
		},
		{
			name:    "Four backslashes means two literal backslashes",
			pattern: `^\\\\p{Cc}$`,
			want:    false,
		},
		{
			name:    "Standard hex character pattern",
			pattern: `^[0-9a-f]{32}$`,
			want:    false,
		},
		{
			name:    "Negative lookahead without unicode properties",
			pattern: `^(?!__.*__$).{1,200}$`,
			want:    false,
		},
		{
			name:    "Backreference safe in Python re",
			pattern: `^(a)\1$`,
			want:    false,
		},
		{
			name:    "Trailing single backslash",
			pattern: `abc\`,
			want:    false,
		},
		{
			name:    "Backslash followed by p without brace",
			pattern: `\p`,
			want:    false,
		},
		{
			name:    "Backslash followed by p and brace without close",
			pattern: `\p{`,
			want:    true,
		},
		{
			name:    "Empty string",
			pattern: ``,
			want:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := HasUnsupportedUnicodePropertyEscape(tt.pattern)
			if got != tt.want {
				t.Errorf("HasUnsupportedUnicodePropertyEscape(%q) = %v, want %v", tt.pattern, got, tt.want)
			}
		})
	}
}
