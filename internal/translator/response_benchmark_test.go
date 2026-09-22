package translator

import (
	"bytes"
	"context"
	"strings"
	"testing"

	translatorapi "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/translator"
	"github.com/tidwall/gjson"
)

var benchmarkResponseTranslationOutput []byte

func BenchmarkResponseTranslationLargePayload(b *testing.B) {
	payload := strings.Repeat("x", 8<<20)
	cases := []struct {
		name    string
		from    string
		to      string
		rawJSON []byte
	}{
		{
			name:    "gemini_to_openai",
			from:    "gemini",
			to:      "openai",
			rawJSON: []byte(`{"modelVersion":"gemini-test","candidates":[{"index":0,"content":{"parts":[{"text":"` + payload + `"}]},"finishReason":"STOP"}]}`),
		},
		{
			name:    "codex_to_openai",
			from:    "codex",
			to:      "openai",
			rawJSON: []byte(`{"type":"response.completed","response":{"id":"resp_1","created_at":1700000000,"model":"gpt-test","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"` + payload + `"}]}]}}`),
		},
		{
			name:    "claude_to_openai",
			from:    "claude",
			to:      "openai",
			rawJSON: claudeLargeTextResponse(payload),
		},
		{
			name:    "claude_to_openai-response",
			from:    "claude",
			to:      "openai-response",
			rawJSON: claudeLargeTextResponse(payload),
		},
	}

	for _, testCase := range cases {
		b.Run(testCase.name, func(b *testing.B) {
			output := translatorapi.ResponseNonStream(testCase.from, testCase.to, context.Background(), "benchmark-model", nil, nil, testCase.rawJSON, nil)
			if !gjson.ValidBytes(output) {
				b.Fatalf("translator generated invalid JSON: %s", output)
			}
			if !bytes.Contains(output, []byte(payload)) {
				b.Fatal("translator dropped the benchmark payload")
			}
			b.ReportAllocs()
			b.SetBytes(int64(len(testCase.rawJSON)))
			b.ResetTimer()

			for b.Loop() {
				benchmarkResponseTranslationOutput = translatorapi.ResponseNonStream(testCase.from, testCase.to, context.Background(), "benchmark-model", nil, nil, testCase.rawJSON, nil)
			}
		})
	}
}

func claudeLargeTextResponse(payload string) []byte {
	return []byte("data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"model\":\"claude-test\"}}\n" +
		"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\"}}\n" +
		"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"" + payload + "\"}}\n" +
		"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n")
}
