package executor

import (
	"bytes"
	"mime/multipart"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestEnsureColonSpacedJSONLeavesInvalidPayloadUnchanged(t *testing.T) {
	input := []byte(`{"text":"unterminated}`)
	output := ensureColonSpacedJSON(input)
	if &output[0] != &input[0] || string(output) != string(input) {
		t.Fatal("invalid JSON payload changed")
	}
}

func TestNormalizeKimiToolMessageLinksReusesCanonicalPayload(t *testing.T) {
	input := []byte(`{"messages":[{"role":"assistant","reasoning_content":"checking","tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call_1","content":"ok"}]}`)
	output, errNormalize := normalizeKimiToolMessageLinks(input)
	if errNormalize != nil {
		t.Fatalf("normalizeKimiToolMessageLinks returned error: %v", errNormalize)
	}
	if &output[0] != &input[0] {
		t.Fatal("canonical Kimi tool history was copied")
	}
}

func TestNormalizeKimiToolMessageLinksPreservesLargeArguments(t *testing.T) {
	input := []byte(`{"messages":[{"role":"assistant","content":"lookup","tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":{"id":9007199254740993}}}]},{"role":"tool","call_id":"call_1","content":"ok"}]}`)
	output, errNormalize := normalizeKimiToolMessageLinks(input)
	if errNormalize != nil {
		t.Fatalf("normalizeKimiToolMessageLinks returned error: %v", errNormalize)
	}
	if got := gjson.GetBytes(output, "messages.0.tool_calls.0.function.arguments.id").Raw; got != "9007199254740993" {
		t.Fatalf("argument id = %s, want exact large integer", got)
	}
	if got := gjson.GetBytes(output, "messages.1.tool_call_id").String(); got != "call_1" {
		t.Fatalf("tool_call_id = %q, want call_1", got)
	}
	if got := gjson.GetBytes(output, "messages.0.reasoning_content").String(); got != "lookup" {
		t.Fatalf("reasoning_content = %q, want lookup", got)
	}
}

func TestCodexMultipartImageEditAppendsExistingImages(t *testing.T) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for _, value := range []string{"existing-1", "existing-2"} {
		if errWrite := writer.WriteField("images", value); errWrite != nil {
			t.Fatalf("write images field: %v", errWrite)
		}
	}
	imagePart, errCreate := writer.CreateFormFile("image[]", "source.png")
	if errCreate != nil {
		t.Fatalf("create image field: %v", errCreate)
	}
	if _, errWrite := imagePart.Write([]byte("png-data")); errWrite != nil {
		t.Fatalf("write image data: %v", errWrite)
	}
	if errClose := writer.Close(); errClose != nil {
		t.Fatalf("close multipart writer: %v", errClose)
	}

	output, _, errRewrite := codexRewriteOpenAIImageEditMultipartToJSON(body.Bytes(), "gpt-image-1.5", writer.Boundary(), false)
	if errRewrite != nil {
		t.Fatalf("rewrite multipart payload: %v", errRewrite)
	}
	if got := gjson.GetBytes(output, "images.0").String(); got != "existing-1" {
		t.Fatalf("images.0 = %q", got)
	}
	if got := gjson.GetBytes(output, "images.1").String(); got != "existing-2" {
		t.Fatalf("images.1 = %q", got)
	}
	if got := gjson.GetBytes(output, "images.2.image_url").String(); !strings.HasPrefix(got, "data:application/octet-stream;base64,") {
		t.Fatalf("images.2.image_url = %q", got)
	}
}

func TestCodexImageBuildersPreservePayloads(t *testing.T) {
	tool := []byte(`{"type":"image_generation","model":"gpt-image-2"}`)
	request := codexBuildImagesResponsesRequest(`draw "this"`, []string{"data:image/png;base64,AA==", "", "data:image/jpeg;base64,BB=="}, tool)
	if !gjson.ValidBytes(request) {
		t.Fatalf("request is invalid JSON: %s", request)
	}
	if got := gjson.GetBytes(request, "input.0.content.0.text").String(); got != `draw "this"` {
		t.Fatalf("prompt = %q", got)
	}
	if got := gjson.GetBytes(request, "input.0.content.#").Int(); got != 3 {
		t.Fatalf("content count = %d, want 3", got)
	}
	if got := gjson.GetBytes(request, "tools.0.model").String(); got != "gpt-image-2" {
		t.Fatalf("tool model = %q", got)
	}

	result := codexImageCallResult{Result: "AA==", OutputFormat: "png", RevisedPrompt: `revised "prompt"`, Quality: "high", Size: "1024x1024"}
	response, errBuild := codexBuildImagesAPIResponse([]codexImageCallResult{result}, 123, []byte(`{"images":1}`), result, "b64_json")
	if errBuild != nil {
		t.Fatalf("codexBuildImagesAPIResponse returned error: %v", errBuild)
	}
	if !gjson.ValidBytes(response) {
		t.Fatalf("response is invalid JSON: %s", response)
	}
	if got := gjson.GetBytes(response, "data.0.b64_json").String(); got != "AA==" {
		t.Fatalf("b64_json = %q", got)
	}
	if got := gjson.GetBytes(response, "data.0.revised_prompt").String(); got != `revised "prompt"` {
		t.Fatalf("revised_prompt = %q", got)
	}
	if got := gjson.GetBytes(response, "usage.images").Int(); got != 1 {
		t.Fatalf("usage.images = %d", got)
	}
}

var benchmarkExecutorPayloadOutput []byte

func BenchmarkCodexBuildImagesAPIResponseLargePayload(b *testing.B) {
	image := strings.Repeat("A", 2<<20)
	results := []codexImageCallResult{
		{Result: image, OutputFormat: "png"},
		{Result: image, OutputFormat: "png"},
		{Result: image, OutputFormat: "png"},
		{Result: image, OutputFormat: "png"},
	}
	b.ReportAllocs()
	b.SetBytes(int64(len(image) * len(results)))
	b.ResetTimer()
	for b.Loop() {
		benchmarkExecutorPayloadOutput, _ = codexBuildImagesAPIResponse(results, 1, []byte(`{"images":4}`), codexImageCallResult{}, "b64_json")
	}
}

func BenchmarkNormalizeKimiToolMessageLinksLargeSinglePatch(b *testing.B) {
	content := strings.Repeat("x", 8<<20)
	input := []byte(`{"messages":[{"role":"assistant","content":"` + content + `","tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call_1","content":"ok"}]}`)
	b.ReportAllocs()
	b.SetBytes(int64(len(input)))
	b.ResetTimer()
	for b.Loop() {
		benchmarkExecutorPayloadOutput, _ = normalizeKimiToolMessageLinks(input)
	}
}

func BenchmarkNormalizeKimiToolMessageLinksLargeMultiplePatches(b *testing.B) {
	content := strings.Repeat("x", (8<<20)/32)
	var builder strings.Builder
	builder.Grow(8 << 20)
	builder.WriteString(`{"messages":[`)
	for index := 0; index < 32; index++ {
		if index > 0 {
			builder.WriteByte(',')
		}
		builder.WriteString(`{"role":"assistant","content":"`)
		builder.WriteString(content)
		builder.WriteString(`","tool_calls":[{"id":"call_`)
		builder.WriteString(strings.Repeat("x", index%3))
		builder.WriteString(`","type":"function","function":{"name":"lookup","arguments":"{}"}}]},{"role":"tool","call_id":"call_`)
		builder.WriteString(strings.Repeat("x", index%3))
		builder.WriteString(`","content":"ok"}`)
	}
	builder.WriteString(`]}`)
	input := []byte(builder.String())
	b.ReportAllocs()
	b.SetBytes(int64(len(input)))
	b.ResetTimer()
	for b.Loop() {
		benchmarkExecutorPayloadOutput, _ = normalizeKimiToolMessageLinks(input)
	}
}

func BenchmarkNormalizeKimiToolMessageLinksLargeCanonicalPayload(b *testing.B) {
	content := strings.Repeat("x", (8<<20)/64)
	var builder strings.Builder
	builder.Grow(8 << 20)
	builder.WriteString(`{"messages":[`)
	for index := 0; index < 64; index++ {
		if index > 0 {
			builder.WriteByte(',')
		}
		builder.WriteString(`{"role":"user","content":"`)
		builder.WriteString(content)
		builder.WriteString(`"}`)
	}
	builder.WriteString(`]}`)
	input := []byte(builder.String())
	b.ReportAllocs()
	b.SetBytes(int64(len(input)))
	b.ResetTimer()
	for b.Loop() {
		benchmarkExecutorPayloadOutput, _ = normalizeKimiToolMessageLinks(input)
	}
}
