package executor

import (
	"testing"

	"github.com/MoeForge/moego/ojson"
)

func TestConvertStreamChunksToNonStream_PreservesNonTextThoughtParts(t *testing.T) {
	exec := &AntigravityExecutor{}

	t.Run("unsigned and signed thought executableCode parts are preserved", func(t *testing.T) {
		chunks := [][]byte{
			[]byte(`{"response":{"candidates":[{"content":{"role":"model","parts":[{"thought":true,"executableCode":{"language":"PYTHON","code":"print(1)"}}]}}]}}`),
			[]byte(`{"response":{"candidates":[{"content":{"role":"model","parts":[{"thought":true,"executableCode":{"language":"PYTHON","code":"print(2)"},"thoughtSignature":"SIG_EXEC_2"}]},"finishReason":"STOP"}]}}`),
		}
		out := exec.convertStreamChunksToNonStream(chunks)
		parts := ojson.GetBytes(out, "response.candidates.0.content.parts").Array()
		if len(parts) != 2 {
			t.Fatalf("parts len = %d, want 2; out=%s", len(parts), out)
		}
		if !parts[0].Get("thought").Bool() || parts[0].Get("executableCode.code").String() != "print(1)" {
			t.Fatalf("parts[0] unexpected: %s", parts[0].Raw)
		}
		if !parts[1].Get("thought").Bool() || parts[1].Get("executableCode.code").String() != "print(2)" || parts[1].Get("thoughtSignature").String() != "SIG_EXEC_2" {
			t.Fatalf("parts[1] unexpected: %s", parts[1].Raw)
		}
	})

	t.Run("thought codeExecutionResult fileData and snake_case function_call parts are preserved", func(t *testing.T) {
		chunks := [][]byte{
			[]byte(`{"response":{"candidates":[{"content":{"role":"model","parts":[{"thought":true,"codeExecutionResult":{"outcome":"OUTCOME_OK","output":"1\n"}}]}}]}}`),
			[]byte(`{"response":{"candidates":[{"content":{"role":"model","parts":[{"thought":true,"fileData":{"mimeType":"text/plain","fileUri":"gs://bucket/a.txt"},"thought_signature":"SIG_FILE"}]}}]}}`),
			[]byte(`{"response":{"candidates":[{"content":{"role":"model","parts":[{"thought":true,"function_call":{"name":"lookup","args":{"q":"x"}},"thoughtSignature":"SIG_FC"}]},"finishReason":"STOP"}]}}`),
		}
		out := exec.convertStreamChunksToNonStream(chunks)
		parts := ojson.GetBytes(out, "response.candidates.0.content.parts").Array()
		if len(parts) != 3 {
			t.Fatalf("parts len = %d, want 3; out=%s", len(parts), out)
		}
		if !parts[0].Get("thought").Bool() || parts[0].Get("codeExecutionResult.outcome").String() != "OUTCOME_OK" {
			t.Fatalf("parts[0] unexpected: %s", parts[0].Raw)
		}
		if !parts[1].Get("thought").Bool() || parts[1].Get("fileData.fileUri").String() != "gs://bucket/a.txt" || parts[1].Get("thoughtSignature").String() != "SIG_FILE" || parts[1].Get("thought_signature").Exists() {
			t.Fatalf("parts[1] unexpected: %s", parts[1].Raw)
		}
		if !parts[2].Get("thought").Bool() || parts[2].Get("function_call.name").String() != "lookup" || parts[2].Get("thoughtSignature").String() != "SIG_FC" {
			t.Fatalf("parts[2] unexpected: %s", parts[2].Raw)
		}
	})

	t.Run("preceding unsigned thought text flushes before signed thought non-text part", func(t *testing.T) {
		chunks := [][]byte{
			[]byte(`{"response":{"candidates":[{"content":{"role":"model","parts":[{"thought":true,"text":"planning"}]}}]}}`),
			[]byte(`{"response":{"candidates":[{"content":{"role":"model","parts":[{"thought":true,"executableCode":{"language":"PYTHON","code":"1+1"},"thoughtSignature":"SIG_EXEC"}]},"finishReason":"STOP"}]}}`),
		}
		out := exec.convertStreamChunksToNonStream(chunks)
		parts := ojson.GetBytes(out, "response.candidates.0.content.parts").Array()
		if len(parts) != 2 {
			t.Fatalf("parts len = %d, want 2; out=%s", len(parts), out)
		}
		if !parts[0].Get("thought").Bool() || parts[0].Get("text").String() != "planning" || parts[0].Get("thoughtSignature").Exists() {
			t.Fatalf("parts[0] unexpected: %s", parts[0].Raw)
		}
		if !parts[1].Get("thought").Bool() || parts[1].Get("executableCode.code").String() != "1+1" || parts[1].Get("thoughtSignature").String() != "SIG_EXEC" {
			t.Fatalf("parts[1] unexpected: %s", parts[1].Raw)
		}
	})

	t.Run("non-text part also carrying text field is preserved", func(t *testing.T) {
		chunks := [][]byte{
			[]byte(`{"response":{"candidates":[{"content":{"role":"model","parts":[{"thought":true,"text":"","executableCode":{"language":"PYTHON","code":"x=1"}}]},"finishReason":"STOP"}]}}`),
		}
		out := exec.convertStreamChunksToNonStream(chunks)
		parts := ojson.GetBytes(out, "response.candidates.0.content.parts").Array()
		if len(parts) != 1 {
			t.Fatalf("parts len = %d, want 1; out=%s", len(parts), out)
		}
		if !parts[0].Get("thought").Bool() || parts[0].Get("executableCode.code").String() != "x=1" {
			t.Fatalf("parts[0] unexpected: %s", parts[0].Raw)
		}
	})

	t.Run("empty unsigned thought text part is still dropped", func(t *testing.T) {
		chunks := [][]byte{
			[]byte(`{"response":{"candidates":[{"content":{"role":"model","parts":[{"thought":true,"text":""}]}}]}}`),
			[]byte(`{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"done"}]},"finishReason":"STOP"}]}}`),
		}
		out := exec.convertStreamChunksToNonStream(chunks)
		parts := ojson.GetBytes(out, "response.candidates.0.content.parts").Array()
		if len(parts) != 1 {
			t.Fatalf("parts len = %d, want 1; out=%s", len(parts), out)
		}
		if parts[0].Get("text").String() != "done" || parts[0].Get("thought").Exists() {
			t.Fatalf("parts[0] unexpected: %s", parts[0].Raw)
		}
	})
}
