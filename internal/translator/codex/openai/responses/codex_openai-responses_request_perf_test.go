package responses

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertSystemRoleToDeveloper_ArrayInput(t *testing.T) {
	inputJSON := []byte(`{
		"model": "gpt-5.5",
		"input": [
			{"type":"message","role":"system","content":"You are helpful."},
			{"type":"message","role":"user","content":"Hello"}
		]
	}`)

	output := convertSystemRoleToDeveloper(inputJSON)

	if got := gjson.GetBytes(output, "input.0.role").String(); got != "developer" {
		t.Fatalf("input.0.role = %q, want %q: %s", got, "developer", string(output))
	}
	if got := gjson.GetBytes(output, "input.1.role").String(); got != "user" {
		t.Fatalf("input.1.role = %q, want %q: %s", got, "user", string(output))
	}
}

func TestConvertSystemRoleToDeveloper_NoSystemRole_ReturnsEquivalent(t *testing.T) {
	inputJSON := []byte(`{
		"model":"gpt-5.5",
		"input":[
			{"type":"message","role":"user","content":"Hello"},
			{"type":"message","role":"assistant","content":"Hi"},
			{"type":"message","role":"tool","content":"Done"}
		]
	}`)

	output := convertSystemRoleToDeveloper(inputJSON)
	if !bytes.Equal(output, inputJSON) {
		t.Fatalf("expected unchanged JSON when no system role exists:\ninput=%s\noutput=%s", string(inputJSON), string(output))
	}
}

func TestConvertSystemRoleToDeveloper_MultipleSystemRoles(t *testing.T) {
	inputJSON := []byte(`{
		"model":"gpt-5.5",
		"input":[
			{"type":"message","role":"system","content":"You are helpful."},
			{"type":"message","role":"user","content":"Hello"},
			{"type":"message","role":"system","content":[{"type":"input_text","text":"Be concise."}]}
		]
	}`)

	output := convertSystemRoleToDeveloper(inputJSON)

	if got := gjson.GetBytes(output, "input.0.role").String(); got != "developer" {
		t.Fatalf("input.0.role = %q, want %q: %s", got, "developer", string(output))
	}
	if got := gjson.GetBytes(output, "input.1.role").String(); got != "user" {
		t.Fatalf("input.1.role = %q, want %q: %s", got, "user", string(output))
	}
	if got := gjson.GetBytes(output, "input.2.role").String(); got != "developer" {
		t.Fatalf("input.2.role = %q, want %q: %s", got, "developer", string(output))
	}
}

func TestConvertSystemRoleToDeveloper_PreservesUnknownFields(t *testing.T) {
	inputJSON := []byte(`{
		"model":"gpt-5.5",
		"input":[
			{
				"type":"message",
				"role":"system",
				"content":[{"type":"input_text","text":"Keep extras intact."}],
				"metadata":{"trace_id":"abc123","rank":7},
				"custom_flags":["x","y"],
				"nullable":null
			}
		]
	}`)

	output := convertSystemRoleToDeveloper(inputJSON)

	if got := gjson.GetBytes(output, "input.0.role").String(); got != "developer" {
		t.Fatalf("input.0.role = %q, want %q: %s", got, "developer", string(output))
	}
	if got := gjson.GetBytes(output, "input.0.metadata.trace_id").String(); got != "abc123" {
		t.Fatalf("input.0.metadata.trace_id = %q, want %q: %s", got, "abc123", string(output))
	}
	if got := gjson.GetBytes(output, "input.0.metadata.rank").Int(); got != 7 {
		t.Fatalf("input.0.metadata.rank = %d, want 7: %s", got, string(output))
	}
	if got := gjson.GetBytes(output, "input.0.custom_flags.#").Int(); got != 2 {
		t.Fatalf("input.0.custom_flags length = %d, want 2: %s", got, string(output))
	}
	if got := gjson.GetBytes(output, "input.0.nullable"); !got.Exists() || got.Type != gjson.Null {
		t.Fatalf("input.0.nullable should remain null: %s", string(output))
	}
}

func TestConvertSystemRoleToDeveloper_StringInput_NoPanic(t *testing.T) {
	inputJSON := []byte(`{"model":"gpt-5.5","input":"Hello from string input"}`)

	output := ConvertOpenAIResponsesRequestToCodex("gpt-5.5", inputJSON, false)

	if got := gjson.GetBytes(output, "input.0.role").String(); got != "user" {
		t.Fatalf("input.0.role = %q, want %q: %s", got, "user", string(output))
	}
	if got := gjson.GetBytes(output, "input.0.content.0.text").String(); got != "Hello from string input" {
		t.Fatalf("input.0.content.0.text = %q, want %q: %s", got, "Hello from string input", string(output))
	}
}

func TestConvertSystemRoleToDeveloper_EmptyInputArray(t *testing.T) {
	inputJSON := []byte(`{"model":"gpt-5.5","input":[]}`)

	output := convertSystemRoleToDeveloper(inputJSON)
	if !bytes.Equal(output, inputJSON) {
		t.Fatalf("empty input array should be unchanged:\ninput=%s\noutput=%s", string(inputJSON), string(output))
	}
}

func TestConvertSystemRoleToDeveloper_InvalidJSON(t *testing.T) {
	inputJSON := []byte(`{"model":"gpt-5.5","input":[`)

	output := convertSystemRoleToDeveloper(inputJSON)
	if !bytes.Equal(output, inputJSON) {
		t.Fatalf("invalid JSON should be returned unchanged:\ninput=%s\noutput=%s", string(inputJSON), string(output))
	}
}

func BenchmarkConvertSystemRoleToDeveloper_InputArray(b *testing.B) {
	cases := []responsesBenchmarkCase{
		{name: "1_small_first_system", inputCount: 1, contentSize: 64, systemPositions: map[int]struct{}{0: {}}},
		{name: "8_small_first_system", inputCount: 8, contentSize: 64, systemPositions: map[int]struct{}{0: {}}},
		{name: "64_medium_middle_system", inputCount: 64, contentSize: 256, systemPositions: map[int]struct{}{32: {}}},
		{name: "256_medium_multiple_system", inputCount: 256, contentSize: 256, systemPositions: everyNthPositionSet(256, 32)},
		{name: "1024_small_mostly_user", inputCount: 1024, contentSize: 64, systemPositions: map[int]struct{}{0: {}, 511: {}, 1023: {}}},
	}

	for _, tc := range cases {
		raw := buildResponsesRequestJSON(b, tc)
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(raw)))
			for i := 0; i < b.N; i++ {
				_ = convertSystemRoleToDeveloper(raw)
			}
		})
	}
}

func BenchmarkConvertOpenAIResponsesRequestToCodex_InputArray(b *testing.B) {
	cases := []responsesBenchmarkCase{
		{name: "1_small_no_tools", inputCount: 1, contentSize: 64, systemPositions: map[int]struct{}{0: {}}},
		{name: "8_small_no_tools", inputCount: 8, contentSize: 64, systemPositions: map[int]struct{}{0: {}}},
		{name: "64_medium_no_tools", inputCount: 64, contentSize: 256, systemPositions: map[int]struct{}{32: {}}},
		{name: "256_medium_small_tools", inputCount: 256, contentSize: 256, systemPositions: everyNthPositionSet(256, 64), toolCount: 2, toolTextSize: 96},
		{name: "1024_small_no_tools", inputCount: 1024, contentSize: 64, systemPositions: map[int]struct{}{0: {}, 1023: {}}},
	}

	for _, tc := range cases {
		raw := buildResponsesRequestJSON(b, tc)
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(raw)))
			for i := 0; i < b.N; i++ {
				_ = ConvertOpenAIResponsesRequestToCodex("gpt-5.5", raw, false)
			}
		})
	}
}

func BenchmarkConvertOpenAIResponsesRequestToCodex_LargePayload_MostlyUserRoles(b *testing.B) {
	raw := buildResponsesRequestJSON(b, responsesBenchmarkCase{
		name:            "large_payload_mostly_user_roles",
		inputCount:      256,
		contentSize:     4096,
		systemPositions: map[int]struct{}{0: {}, 128: {}, 255: {}},
		toolCount:       8,
		toolTextSize:    1024,
	})

	b.ReportAllocs()
	b.SetBytes(int64(len(raw)))
	for i := 0; i < b.N; i++ {
		_ = ConvertOpenAIResponsesRequestToCodex("gpt-5.5", raw, false)
	}
}

func BenchmarkConvertOpenAIResponsesRequestToCodex_LargePayload_WithSystemRoles(b *testing.B) {
	raw := buildResponsesRequestJSON(b, responsesBenchmarkCase{
		name:            "large_payload_with_system_roles",
		inputCount:      256,
		contentSize:     4096,
		systemPositions: everyNthPositionSet(256, 8),
		toolCount:       8,
		toolTextSize:    1024,
	})

	b.ReportAllocs()
	b.SetBytes(int64(len(raw)))
	for i := 0; i < b.N; i++ {
		_ = ConvertOpenAIResponsesRequestToCodex("gpt-5.5", raw, false)
	}
}

type responsesBenchmarkCase struct {
	name            string
	inputCount      int
	contentSize     int
	systemPositions map[int]struct{}
	toolCount       int
	toolTextSize    int
}

func buildResponsesRequestJSON(tb testing.TB, tc responsesBenchmarkCase) []byte {
	tb.Helper()

	input := make([]map[string]any, 0, tc.inputCount)
	for i := 0; i < tc.inputCount; i++ {
		role := "user"
		if _, ok := tc.systemPositions[i]; ok {
			role = "system"
		}
		input = append(input, map[string]any{
			"type": "message",
			"role": role,
			"content": []map[string]string{
				{
					"type": "input_text",
					"text": buildPayloadText(i, tc.contentSize),
				},
			},
		})
	}

	request := map[string]any{
		"model": "gpt-5.5",
		"input": input,
	}

	if tc.toolCount > 0 {
		tools := make([]map[string]any, 0, tc.toolCount)
		for i := 0; i < tc.toolCount; i++ {
			tools = append(tools, map[string]any{
				"type":        "function",
				"name":        fmt.Sprintf("tool_%d", i),
				"description": buildToolDescription(i, tc.toolTextSize),
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"query": map[string]any{
							"type":        "string",
							"description": buildToolDescription(i+tc.toolCount, tc.toolTextSize/2+1),
						},
					},
				},
			})
		}
		request["tools"] = tools
	}

	raw, err := json.Marshal(request)
	if err != nil {
		tb.Fatalf("marshal benchmark request: %v", err)
	}
	return raw
}

func buildPayloadText(index int, size int) string {
	if size <= 0 {
		return fmt.Sprintf("payload-%d", index)
	}
	var builder strings.Builder
	builder.Grow(size + 32)
	builder.WriteString(fmt.Sprintf("message-%d:", index))
	for builder.Len() < size {
		builder.WriteString("abcdefghij")
	}
	return builder.String()[:size]
}

func buildToolDescription(index int, size int) string {
	if size <= 0 {
		return fmt.Sprintf("tool-description-%d", index)
	}
	var builder strings.Builder
	builder.Grow(size + 32)
	builder.WriteString(fmt.Sprintf("tool-%d:", index))
	for builder.Len() < size {
		builder.WriteString("tooling-")
	}
	return builder.String()[:size]
}

func everyNthPositionSet(total int, step int) map[int]struct{} {
	positions := make(map[int]struct{})
	if total <= 0 || step <= 0 {
		return positions
	}
	for i := 0; i < total; i += step {
		positions[i] = struct{}{}
	}
	return positions
}
