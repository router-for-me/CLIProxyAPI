package common

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestSetResponsesToolCallIdentity(t *testing.T) {
	tests := []struct {
		name                string
		input               string
		toolName            string
		namespace           string
		itemPath            string
		namePath            string
		namespacePath       string
		wantName            string
		wantNamespace       string
		wantNamespaceExists bool
	}{
		{
			name:                "top level",
			input:               `{"name":"functions__exec"}`,
			toolName:            "exec",
			namespace:           "functions",
			namePath:            "name",
			namespacePath:       "namespace",
			wantName:            "exec",
			wantNamespace:       "functions",
			wantNamespaceExists: true,
		},
		{
			name:                "nested item",
			input:               `{"item":{"name":"functions__exec"}}`,
			toolName:            "exec",
			namespace:           "functions",
			itemPath:            "item",
			namePath:            "item.name",
			namespacePath:       "item.namespace",
			wantName:            "exec",
			wantNamespace:       "functions",
			wantNamespaceExists: true,
		},
		{
			name:          "remove stale namespace",
			input:         `{"name":"old","namespace":"stale"}`,
			toolName:      "plain",
			namePath:      "name",
			namespacePath: "namespace",
			wantName:      "plain",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := SetResponsesToolCallIdentity([]byte(test.input), test.toolName, test.namespace, test.itemPath)
			if actual := gjson.GetBytes(got, test.namePath).String(); actual != test.wantName {
				t.Fatalf("name = %q, want %q; output=%s", actual, test.wantName, got)
			}
			namespace := gjson.GetBytes(got, test.namespacePath)
			if namespace.Exists() != test.wantNamespaceExists {
				t.Fatalf("namespace exists = %t, want %t; output=%s", namespace.Exists(), test.wantNamespaceExists, got)
			}
			if test.wantNamespaceExists && namespace.String() != test.wantNamespace {
				t.Fatalf("namespace = %q, want %q; output=%s", namespace.String(), test.wantNamespace, got)
			}
		})
	}
}

func TestExtractResponsesCallID(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "call_id standard",
			input:    `{"call_id":"call_1"}`,
			expected: "call_1",
		},
		{
			name:     "tool_call_id",
			input:    `{"tool_call_id":"call_2"}`,
			expected: "call_2",
		},
		{
			name:     "callId",
			input:    `{"callId":"call_3"}`,
			expected: "call_3",
		},
		{
			name:     "id fallback",
			input:    `{"id":"call_4"}`,
			expected: "call_4",
		},
		{
			name:     "dedicated call_id takes precedence over id",
			input:    `{"id":"item_5","call_id":"call_5"}`,
			expected: "call_5",
		},
		{
			name:     "tool_call_id takes precedence over id",
			input:    `{"id":"item_6","tool_call_id":"call_6"}`,
			expected: "call_6",
		},
		{
			name:     "callId takes precedence over id",
			input:    `{"id":"item_7","callId":"call_7"}`,
			expected: "call_7",
		},
		{
			name:     "empty",
			input:    `{"output":"result"}`,
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			node := gjson.Parse(tt.input)
			if got := ExtractResponsesCallID(node); got != tt.expected {
				t.Fatalf("ExtractResponsesCallID(%s) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestNormalizeResponsesToolCallOutputs(t *testing.T) {
	t.Run("preserves explicit call_id and fills missing in reverse order", func(t *testing.T) {
		input := []byte(`[
			{"type":"function_call","call_id":"call_a","name":"tool_a"},
			{"type":"function_call","call_id":"call_b","name":"tool_b"},
			{"type":"function_call_output","output":"result_b"},
			{"type":"function_call_output","tool_call_id":"call_a","output":"result_a"}
		]`)
		items := gjson.ParseBytes(input).Array()
		normalized := NormalizeResponsesToolCallOutputs(items)
		if len(normalized) != 4 {
			t.Fatalf("expected 4 items, got %d", len(normalized))
		}

		// First output had no ID, but call_a is reserved by second output, so first output gets call_b
		if got := normalized[2].Get("call_id").String(); got != "call_b" {
			t.Fatalf("normalized[2].call_id = %q, want call_b", got)
		}
		// Second output had explicit tool_call_id call_a, so its call_id is set to call_a
		if got := normalized[3].Get("call_id").String(); got != "call_a" {
			t.Fatalf("normalized[3].call_id = %q, want call_a", got)
		}
	})

	t.Run("preserves explicit call_id across intervening user message", func(t *testing.T) {
		input := []byte(`[
			{"type":"function_call","call_id":"call_a","name":"tool_a"},
			{"type":"function_call","call_id":"call_b","name":"tool_b"},
			{"type":"function_call_output","output":"result_b"},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"intervening"}]},
			{"type":"function_call_output","call_id":"call_a","output":"result_a"}
		]`)
		items := gjson.ParseBytes(input).Array()
		normalized := NormalizeResponsesToolCallOutputs(items)
		if len(normalized) != 5 {
			t.Fatalf("expected 5 items, got %d", len(normalized))
		}

		// First output (index 2) had no ID, but call_a is reserved across the intervening message by index 4,
		// so index 2 must get call_b
		if got := normalized[2].Get("call_id").String(); got != "call_b" {
			t.Fatalf("normalized[2].call_id = %q, want call_b", got)
		}
		// Last output (index 4) has explicit call_a
		if got := normalized[4].Get("call_id").String(); got != "call_a" {
			t.Fatalf("normalized[4].call_id = %q, want call_a", got)
		}
	})

	t.Run("does not steal explicit unmatched call_id", func(t *testing.T) {
		input := []byte(`[
			{"type":"function_call","call_id":"call_a","name":"tool_a"},
			{"type":"function_call_output","call_id":"call_other","output":"result_other"}
		]`)
		items := gjson.ParseBytes(input).Array()
		normalized := NormalizeResponsesToolCallOutputs(items)
		if got := normalized[1].Get("call_id").String(); got != "call_other" {
			t.Fatalf("normalized[1].call_id = %q, want call_other", got)
		}
	})
}
