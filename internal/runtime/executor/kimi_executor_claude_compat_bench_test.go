package executor

import (
	"testing"
)

// BenchmarkApplyKimiClaudeCompatibility benchmarks the full compatibility transformation
func BenchmarkApplyKimiClaudeCompatibility(b *testing.B) {
	// Realistic request with all incompatible fields
	input := []byte(`{
		"model": "claude-sonnet-4-6",
		"max_tokens": 1024,
		"messages": [
			{"role": "user", "content": "Hello"},
			{"role": "assistant", "content": [
				{"type": "text", "text": "Hi"},
				{"type": "tool_use", "id": "tool_1", "name": "test_tool", "input": {"arg": "value"}}
			]},
			{"role": "user", "content": [
				{"type": "text", "text": "Result"},
				{"type": "tool_result", "tool_use_id": "tool_1", "content": "success"},
				{"type": "tool_result", "tool_use_id": "tool_2", "content": [
					{"type": "text", "text": "text result"},
					{"type": "tool_reference", "tool_name": "ref_tool", "tool_use_id": "ref_1"}
				]}
			]}
		],
		"metadata": {"user_id": "{\"device_id\":\"abc\"}"},
		"context_management": {"edits": [{"type": "x"}]},
		"thinking": {"type": "adaptive", "budget_tokens": 2000},
		"output_config": {"effort": "high"}
	}`)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = applyKimiClaudeCompatibility(input, false)
	}
}

// BenchmarkApplyKimiClaudeCompatibilityStrict benchmarks strict mode transformation
func BenchmarkApplyKimiClaudeCompatibilityStrict(b *testing.B) {
	input := []byte(`{
		"model": "claude-sonnet-4-6",
		"max_tokens": 1024,
		"messages": [{"role": "user", "content": "Hello"}],
		"metadata": {"user_id": "user123"},
		"thinking": {"type": "adaptive"},
		"output_config": {"effort": "high"}
	}`)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = applyKimiClaudeCompatibility(input, true)
	}
}

// BenchmarkStripToolReferences benchmarks the tool_reference filtering
func BenchmarkStripToolReferences(b *testing.B) {
	input := []byte(`{
		"messages": [
			{"role": "user", "content": [
				{"type": "text", "text": "text1"},
				{"type": "tool_result", "tool_use_id": "tool_1", "content": [
					{"type": "text", "text": "result1"},
					{"type": "tool_reference", "tool_name": "tool_a", "tool_use_id": "ref_1"},
					{"type": "text", "text": "result2"}
				]}
			]},
			{"role": "user", "content": [
				{"type": "tool_result", "tool_use_id": "tool_2", "content": [
					{"type": "tool_reference", "tool_name": "tool_b", "tool_use_id": "ref_2"},
					{"type": "text", "text": "result3"}
				]}
			]}
		]
	}`)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = stripToolReferences(input)
	}
}

// BenchmarkStripKimiIncompatibleFields benchmarks field stripping
func BenchmarkStripKimiIncompatibleFields(b *testing.B) {
	input := []byte(`{
		"model": "claude-sonnet-4-6",
		"max_tokens": 1024,
		"messages": [{"role": "user", "content": "Hello"}],
		"metadata": {"user_id": "user123"},
		"context_management": {"edits": []},
		"thinking": {"type": "adaptive"},
		"output_config": {"effort": "high"}
	}`)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = stripKimiIncompatibleFields(input, false)
	}
}

// BenchmarkStripKimiIncompatibleFieldsStrict benchmarks strict mode field stripping
func BenchmarkStripKimiIncompatibleFieldsStrict(b *testing.B) {
	input := []byte(`{
		"model": "claude-sonnet-4-6",
		"max_tokens": 1024,
		"messages": [{"role": "user", "content": "Hello"}],
		"metadata": {"user_id": "user123"},
		"thinking": {"type": "enabled", "budget_tokens": 4096},
		"output_config": {"effort": "high"}
	}`)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = stripKimiIncompatibleFields(input, true)
	}
}
