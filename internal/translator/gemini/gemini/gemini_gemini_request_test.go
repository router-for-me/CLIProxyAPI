package gemini

import (
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

const largeInlineDataSize = 20 << 20

var largeInlineDataBenchmarkOutput []byte

func TestConvertGeminiRequestToGeminiReusesLargeNormalizedPayload(t *testing.T) {
	input := largeInlineDataGeminiRequest(true)

	// Assert the reuse invariant with t.Fatal rather than inside testing.Benchmark:
	// a failing benchmark aborts before any iteration completes and yields a zero
	// BenchmarkResult, so AllocedBytesPerOp would report 0 and silently satisfy the
	// allocation check below exactly when the payload is being copied.
	output := ConvertGeminiRequestToGemini("gemini-test", input, false)
	if &output[0] != &input[0] {
		t.Fatal("normalized request should reuse the input payload")
	}
	largeInlineDataBenchmarkOutput = output

	result := testing.Benchmark(func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			largeInlineDataBenchmarkOutput = ConvertGeminiRequestToGemini("gemini-test", input, false)
		}
	})

	if result.N == 0 {
		t.Fatal("allocation benchmark did not complete an iteration")
	}
	if allocated := result.AllocedBytesPerOp(); allocated >= 1<<20 {
		t.Fatalf("normalized 20 MiB inlineData request allocated %d bytes/op, want less than 1 MiB", allocated)
	}
}

func BenchmarkConvertGeminiRequestToGeminiLargeInlineData(b *testing.B) {
	for _, test := range []struct {
		name                  string
		includeSafetySettings bool
	}{
		{name: "normalized_passthrough", includeSafetySettings: true},
		{name: "attach_default_safety", includeSafetySettings: false},
	} {
		b.Run(test.name, func(b *testing.B) {
			input := largeInlineDataGeminiRequest(test.includeSafetySettings)
			b.ReportAllocs()
			b.SetBytes(int64(len(input)))
			b.ResetTimer()
			for b.Loop() {
				largeInlineDataBenchmarkOutput = ConvertGeminiRequestToGemini("gemini-test", input, false)
			}
		})
	}
}

func largeInlineDataGeminiRequest(includeSafetySettings bool) []byte {
	prefix := `{"contents":[{"role":"user","parts":[{"inlineData":{"mimeType":"video/mp4","data":"`
	suffix := `"}}]}]`
	if includeSafetySettings {
		suffix += `,"safetySettings":[]`
	}
	return []byte(prefix + strings.Repeat("A", largeInlineDataSize) + suffix + `}`)
}

func TestBackfillEmptyFunctionResponseNames_Single(t *testing.T) {
	input := []byte(`{
		"contents": [
			{
				"role": "model",
				"parts": [
					{"functionCall": {"name": "Bash", "args": {"cmd": "ls"}}}
				]
			},
			{
				"role": "user",
				"parts": [
					{"functionResponse": {"name": "", "response": {"output": "file1.txt"}}}
				]
			}
		]
	}`)

	out := backfillEmptyFunctionResponseNames(input)

	name := gjson.GetBytes(out, "contents.1.parts.0.functionResponse.name").String()
	if name != "Bash" {
		t.Errorf("Expected backfilled name 'Bash', got '%s'", name)
	}
}

func TestBackfillEmptyFunctionResponseNames_Parallel(t *testing.T) {
	input := []byte(`{
		"contents": [
			{
				"role": "model",
				"parts": [
					{"functionCall": {"name": "Read", "args": {"path": "/a"}}},
					{"functionCall": {"name": "Grep", "args": {"pattern": "x"}}}
				]
			},
			{
				"role": "user",
				"parts": [
					{"functionResponse": {"name": "", "response": {"result": "content a"}}},
					{"functionResponse": {"name": "", "response": {"result": "match x"}}}
				]
			}
		]
	}`)

	out := backfillEmptyFunctionResponseNames(input)

	name0 := gjson.GetBytes(out, "contents.1.parts.0.functionResponse.name").String()
	name1 := gjson.GetBytes(out, "contents.1.parts.1.functionResponse.name").String()
	if name0 != "Read" {
		t.Errorf("Expected first name 'Read', got '%s'", name0)
	}
	if name1 != "Grep" {
		t.Errorf("Expected second name 'Grep', got '%s'", name1)
	}
}

func TestBackfillEmptyFunctionResponseNames_PreservesExisting(t *testing.T) {
	input := []byte(`{
		"contents": [
			{
				"role": "model",
				"parts": [
					{"functionCall": {"name": "Bash", "args": {}}}
				]
			},
			{
				"role": "user",
				"parts": [
					{"functionResponse": {"name": "Bash", "response": {"result": "ok"}}}
				]
			}
		]
	}`)

	out := backfillEmptyFunctionResponseNames(input)

	name := gjson.GetBytes(out, "contents.1.parts.0.functionResponse.name").String()
	if name != "Bash" {
		t.Errorf("Expected preserved name 'Bash', got '%s'", name)
	}
}

func TestConvertGeminiRequestToGemini_BackfillsEmptyName(t *testing.T) {
	input := []byte(`{
		"contents": [
			{
				"role": "model",
				"parts": [
					{"functionCall": {"name": "Bash", "args": {"cmd": "ls"}}}
				]
			},
			{
				"role": "user",
				"parts": [
					{"functionResponse": {"name": "", "response": {"output": "file1.txt"}}}
				]
			}
		]
	}`)

	out := ConvertGeminiRequestToGemini("", input, false)

	name := gjson.GetBytes(out, "contents.1.parts.0.functionResponse.name").String()
	if name != "Bash" {
		t.Errorf("Expected backfilled name 'Bash', got '%s'", name)
	}
}

func TestBackfillEmptyFunctionResponseNames_MoreResponsesThanCalls(t *testing.T) {
	// Extra responses beyond the call count should not panic and should be left unchanged.
	input := []byte(`{
		"contents": [
			{
				"role": "model",
				"parts": [
					{"functionCall": {"name": "Bash", "args": {}}}
				]
			},
			{
				"role": "user",
				"parts": [
					{"functionResponse": {"name": "", "response": {"result": "ok"}}},
					{"functionResponse": {"name": "", "response": {"result": "extra"}}}
				]
			}
		]
	}`)

	out := backfillEmptyFunctionResponseNames(input)

	name0 := gjson.GetBytes(out, "contents.1.parts.0.functionResponse.name").String()
	if name0 != "Bash" {
		t.Errorf("Expected first name 'Bash', got '%s'", name0)
	}
	// Second response has no matching call, should remain empty
	name1 := gjson.GetBytes(out, "contents.1.parts.1.functionResponse.name").String()
	if name1 != "" {
		t.Errorf("Expected second name to remain empty, got '%s'", name1)
	}
}

func TestBackfillEmptyFunctionResponseNames_MultipleGroups(t *testing.T) {
	// Two sequential call/response groups should each get correct names.
	input := []byte(`{
		"contents": [
			{
				"role": "model",
				"parts": [
					{"functionCall": {"name": "Read", "args": {}}}
				]
			},
			{
				"role": "user",
				"parts": [
					{"functionResponse": {"name": "", "response": {"result": "content"}}}
				]
			},
			{
				"role": "model",
				"parts": [
					{"functionCall": {"name": "Grep", "args": {}}}
				]
			},
			{
				"role": "user",
				"parts": [
					{"functionResponse": {"name": "", "response": {"result": "match"}}}
				]
			}
		]
	}`)

	out := backfillEmptyFunctionResponseNames(input)

	name0 := gjson.GetBytes(out, "contents.1.parts.0.functionResponse.name").String()
	name1 := gjson.GetBytes(out, "contents.3.parts.0.functionResponse.name").String()
	if name0 != "Read" {
		t.Errorf("Expected first group name 'Read', got '%s'", name0)
	}
	if name1 != "Grep" {
		t.Errorf("Expected second group name 'Grep', got '%s'", name1)
	}
}
