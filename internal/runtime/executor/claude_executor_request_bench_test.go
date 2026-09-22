package executor

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

type claudeOAuthRemapBenchmarkBody struct {
	Model      string                             `json:"model"`
	Tools      []claudeOAuthRemapBenchmarkTool    `json:"tools"`
	ToolChoice claudeOAuthRemapBenchmarkChoice    `json:"tool_choice"`
	Messages   []claudeOAuthRemapBenchmarkMessage `json:"messages"`
	Padding    string                             `json:"padding"`
}

type claudeOAuthRemapBenchmarkTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"input_schema"`
}

type claudeOAuthRemapBenchmarkChoice struct {
	Type string `json:"type"`
	Name string `json:"name"`
}

type claudeOAuthRemapBenchmarkMessage struct {
	Role    string `json:"role"`
	Content []any  `json:"content"`
}

func BenchmarkRemapOAuthToolNames(b *testing.B) {
	benchmarks := []struct {
		name       string
		targetSize int
		references int
	}{
		{name: "4KiB_8Refs", targetSize: 4 << 10, references: 8},
		{name: "64KiB_100Refs", targetSize: 64 << 10, references: 100},
		{name: "256KiB_500Refs", targetSize: 256 << 10, references: 500},
	}

	for _, benchmark := range benchmarks {
		b.Run(benchmark.name, func(b *testing.B) {
			body := buildClaudeOAuthRemapBenchmarkBody(b, benchmark.targetSize, benchmark.references)
			options := claudeMCPAliasOptions{secret: "benchmark-caller"}
			b.ReportAllocs()
			b.SetBytes(int64(len(body)))
			for b.Loop() {
				remapped, reverseMap := remapOAuthToolNamesWithOptions(body, options)
				if len(remapped) == 0 || len(reverseMap) == 0 {
					b.Fatal("remap returned empty output")
				}
			}
		})
	}
}

func buildClaudeOAuthRemapBenchmarkBody(tb testing.TB, targetSize, references int) []byte {
	tb.Helper()

	const toolCount = 20
	tools := make([]claudeOAuthRemapBenchmarkTool, 0, toolCount)
	for i := range toolCount {
		tools = append(tools, claudeOAuthRemapBenchmarkTool{
			Name:        fmt.Sprintf("benchmark_tool_%02d", i),
			Description: "Benchmark tool with a stable representative schema.",
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{"value": map[string]any{"type": "string"}}},
		})
	}

	content := make([]any, 0, references)
	for i := range references {
		name := tools[i%len(tools)].Name
		switch i % 3 {
		case 0:
			content = append(content, map[string]any{"type": "tool_use", "id": fmt.Sprintf("toolu_%04d", i), "name": name, "input": map[string]any{"value": i}})
		case 1:
			content = append(content, map[string]any{"type": "tool_reference", "tool_name": name})
		default:
			content = append(content, map[string]any{"type": "tool_result", "tool_use_id": fmt.Sprintf("toolu_%04d", i), "content": []any{map[string]any{"type": "tool_reference", "tool_name": name}}})
		}
	}

	request := claudeOAuthRemapBenchmarkBody{
		Model:      "claude-opus-5",
		Tools:      tools,
		ToolChoice: claudeOAuthRemapBenchmarkChoice{Type: "tool", Name: tools[0].Name},
		Messages:   []claudeOAuthRemapBenchmarkMessage{{Role: "assistant", Content: content}},
	}
	body, errMarshal := json.Marshal(request)
	if errMarshal != nil {
		tb.Fatalf("marshal benchmark request: %v", errMarshal)
	}
	if remaining := targetSize - len(body); remaining > 0 {
		request.Padding = strings.Repeat("x", remaining)
		body, errMarshal = json.Marshal(request)
		if errMarshal != nil {
			tb.Fatalf("marshal padded benchmark request: %v", errMarshal)
		}
	}
	return body
}
