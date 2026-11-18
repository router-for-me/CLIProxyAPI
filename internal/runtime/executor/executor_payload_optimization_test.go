package executor

import (
	"bytes"
	"context"
	"fmt"
	"mime/multipart"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
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
	if got := ojson.GetBytes(output, "messages.0.tool_calls.0.function.arguments.id").Raw; got != "9007199254740993" {
		t.Fatalf("argument id = %s, want exact large integer", got)
	}
	if got := ojson.GetBytes(output, "messages.1.tool_call_id").String(); got != "call_1" {
		t.Fatalf("tool_call_id = %q, want call_1", got)
	}
	if got := ojson.GetBytes(output, "messages.0.reasoning_content").String(); got != "lookup" {
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
	if got := ojson.GetBytes(output, "images.0").String(); got != "existing-1" {
		t.Fatalf("images.0 = %q", got)
	}
	if got := ojson.GetBytes(output, "images.1").String(); got != "existing-2" {
		t.Fatalf("images.1 = %q", got)
	}
	if got := ojson.GetBytes(output, "images.2.image_url").String(); !strings.HasPrefix(got, "data:application/octet-stream;base64,") {
		t.Fatalf("images.2.image_url = %q", got)
	}
}

func TestCodexImageBuildersPreservePayloads(t *testing.T) {
	tool := []byte(`{"type":"image_generation","model":"gpt-image-2"}`)
	request := codexBuildImagesResponsesRequest(`draw "this"`, []string{"data:image/png;base64,AA==", "", "data:image/jpeg;base64,BB=="}, tool)
	if !ojson.ValidBytes(request) {
		t.Fatalf("request is invalid JSON: %s", request)
	}
	if got := ojson.GetBytes(request, "input.0.content.0.text").String(); got != `draw "this"` {
		t.Fatalf("prompt = %q", got)
	}
	if got := ojson.GetBytes(request, "input.0.content.#").Int(); got != 3 {
		t.Fatalf("content count = %d, want 3", got)
	}
	if got := ojson.GetBytes(request, "tools.0.model").String(); got != "gpt-image-2" {
		t.Fatalf("tool model = %q", got)
	}

	result := codexImageCallResult{Result: "AA==", OutputFormat: "png", RevisedPrompt: `revised "prompt"`, Quality: "high", Size: "1024x1024"}
	response, errBuild := codexBuildImagesAPIResponse([]codexImageCallResult{result}, 123, []byte(`{"images":1}`), result, "b64_json")
	if errBuild != nil {
		t.Fatalf("codexBuildImagesAPIResponse returned error: %v", errBuild)
	}
	if !ojson.ValidBytes(response) {
		t.Fatalf("response is invalid JSON: %s", response)
	}
	if got := ojson.GetBytes(response, "data.0.b64_json").String(); got != "AA==" {
		t.Fatalf("b64_json = %q", got)
	}
	if got := ojson.GetBytes(response, "data.0.revised_prompt").String(); got != `revised "prompt"` {
		t.Fatalf("revised_prompt = %q", got)
	}
	if got := ojson.GetBytes(response, "usage.images").Int(); got != 1 {
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

func TestObserveClaudeStreamJSONPayloadClonesMessageID(t *testing.T) {
	buf := []byte(`{"type":"message_start","message":{"id":"msg_reusable_buffer_01"}}`)
	var msgID string
	var sawMessageStop bool

	observeClaudeStreamJSONPayload(buf, false, &msgID, &sawMessageStop)
	if msgID != "msg_reusable_buffer_01" {
		t.Fatalf("msgID = %q, want msg_reusable_buffer_01", msgID)
	}
	for i := range buf {
		buf[i] = 'X'
	}
	if msgID != "msg_reusable_buffer_01" {
		t.Fatalf("msgID mutated after buffer overwrite: got %q, want msg_reusable_buffer_01", msgID)
	}
}

func TestExecutorPayloadSingleDocPipelinesByteConsistency(t *testing.T) {
	ctx := context.Background()

	t.Run("claude doc pipeline matches byte pipeline", func(t *testing.T) {
		raw := []byte(`{"model":"claude-sonnet-4-5","betas":["custom-beta-1"],"prompt_cache_options":{"ttl":"5m"},"tool_choice":{"type":"any"},"thinking":{"type":"enabled","budget_tokens":2048},"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`)

		byteOut := bytes.Clone(raw)
		byteOut = checkSystemInstructionsWithMode(byteOut, false)
		byteOut = ensureModelMaxTokens(byteOut, "claude-sonnet-4-5")
		byteOut = disableThinkingIfToolChoiceForced(byteOut)
		byteOut = ensureCacheControl(byteOut)
		byteOut = enforceCacheControlLimit(byteOut, 4)
		byteOut = stripPromptCacheOptions(byteOut)
		betasByte, byteOut := extractAndRemoveBetas(byteOut)

		doc := ojson.NewDoc(bytes.Clone(raw))
		checkSystemInstructionsWithModeDoc(doc, false)
		ensureModelMaxTokensDoc(doc, "claude-sonnet-4-5")
		disableThinkingIfToolChoiceForcedDoc(doc)
		ensureCacheControlDoc(doc)
		enforceCacheControlLimitDoc(doc, 4)
		stripPromptCacheOptionsDoc(doc)
		betasDoc, _ := extractAndRemoveBetasDoc(doc)
		docOut := doc.Bytes()

		if !bytes.Equal(docOut, byteOut) {
			t.Fatalf("claude doc output mismatch:\ndoc:  %s\nbyte: %s", docOut, byteOut)
		}
		if strings.Join(betasDoc, ",") != strings.Join(betasByte, ",") {
			t.Fatalf("claude betas mismatch: doc=%v byte=%v", betasDoc, betasByte)
		}
	})

	t.Run("codex doc pipeline matches byte pipeline", func(t *testing.T) {
		raw := []byte(`{"model":"gpt-5","instructions":null,"parallel_tool_calls":true,"temperature":0.7,"input":[{"type":"reasoning","id":"rs_1","content":[{"type":"reasoning_text","text":"step 1"}],"encrypted_content":"invalid"},{"type":"web_search_call","id":"ws_1"},{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`)
		opts := cliproxyexecutor.Options{}

		byteOut := bytes.Clone(raw)
		byteOut = normalizeCodexInstructions(byteOut)
		byteOut = ensureWebSearchToolForHistory(byteOut, opts.Headers)
		byteOut = ensureImageGenerationTool(byteOut, "gpt-5", nil, opts.Headers)
		byteOut = normalizeCodexParallelToolCalls(byteOut, opts.Headers)
		byteOut = sanitizeOpenAIResponsesReasoningEncryptedContentWithCompatModelAndBaseURL(ctx, "codex executor", byteOut, false, "gpt-5", "")
		byteOut = applyCodexSIWCRequestBodyRules(byteOut, false, opts)

		doc := ojson.NewDoc(bytes.Clone(raw))
		normalizeCodexInstructionsDoc(doc)
		ensureWebSearchToolForHistoryDoc(doc, opts.Headers)
		ensureImageGenerationToolDoc(doc, "gpt-5", nil, opts.Headers)
		normalizeCodexParallelToolCallsDoc(doc, opts.Headers)
		sanitizeOpenAIResponsesReasoningEncryptedContentWithCompatModelAndBaseURLDoc(ctx, "codex executor", doc, false, "gpt-5", "")
		applyCodexSIWCRequestBodyRulesDoc(doc, false, opts)
		docOut := doc.Bytes()

		if !bytes.Equal(docOut, byteOut) {
			t.Fatalf("codex doc output mismatch:\ndoc:  %s\nbyte: %s", docOut, byteOut)
		}
	})

	t.Run("xai doc pipeline matches byte pipeline", func(t *testing.T) {
		raw := []byte(`{"model":"grok-4","tool_choice":"auto","tools":[{"type":"custom","name":"my_tool"}],"input":[{"type":"additional_tools","tools":[{"type":"function","name":"extra_fn","parameters":{"type":"object","properties":{}}}]},{"type":"custom_tool_call","call_id":"call_1","name":"my_tool","input":"{\"a\":1}"},{"type":"reasoning","content":null,"encrypted_content":"invalid"}],"image":{"image_url":"https://example.com/a.png"}}`)

		byteOut := bytes.Clone(raw)
		byteOut = normalizeXAIToolsWithFold(byteOut, false)
		byteOut = promoteXAIAdditionalTools(byteOut)
		byteOut = normalizeXAIToolChoiceForTools(byteOut)
		byteOut = normalizeXAIInputCustomToolCalls(byteOut)
		byteOut = normalizeXAIInputReasoningItems(byteOut)
		byteOut = sanitizeXAIInputEncryptedContentForBaseURL(byteOut, "")
		byteOut = normalizeXAIImageRefs(byteOut)

		doc := ojson.NewDoc(bytes.Clone(raw))
		normalizeXAIToolsWithFoldDoc(doc, false)
		promoteXAIAdditionalToolsDoc(doc)
		normalizeXAIToolChoiceForToolsDoc(doc)
		normalizeXAIInputCustomToolCallsDoc(doc)
		normalizeXAIInputReasoningItemsDoc(doc)
		sanitizeXAIInputEncryptedContentForBaseURLDoc(doc, "")
		normalizeXAIImageRefsDoc(doc)
		docOut := doc.Bytes()

		if !bytes.Equal(docOut, byteOut) {
			t.Fatalf("xai doc output mismatch:\ndoc:  %s\nbyte: %s", docOut, byteOut)
		}
	})

	t.Run("kimi doc pipeline matches byte pipeline", func(t *testing.T) {
		raw := []byte(`{"model":"kimi-k2.5","temperature":0.2,"thinking":{"type":"enabled"},"tools":[{"type":"function","function":{"name":"lookup","parameters":{"$defs":{" item":{"type":"string"}},"properties":{"x":{"$ref":"#/$defs/ item"}}}}}],"messages":[{"role":"assistant","content":"thinking","tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{}"}}]},{"role":"tool","call_id":"call_1","content":"ok"}]}`)

		byteOut, errByte := normalizeKimiToolMessageLinks(bytes.Clone(raw))
		if errByte != nil {
			t.Fatalf("normalizeKimiToolMessageLinks: %v", errByte)
		}
		byteOut = normalizeKimiTools(byteOut)
		byteOut = normalizeKimiTemperature(byteOut)

		doc := ojson.NewDoc(bytes.Clone(raw))
		if errDoc := normalizeKimiToolMessageLinksDoc(doc); errDoc != nil {
			t.Fatalf("normalizeKimiToolMessageLinksDoc: %v", errDoc)
		}
		normalizeKimiToolsDoc(doc)
		normalizeKimiTemperatureDoc(doc)
		docOut := doc.Bytes()

		if !bytes.Equal(docOut, byteOut) {
			t.Fatalf("kimi doc output mismatch:\ndoc:  %s\nbyte: %s", docOut, byteOut)
		}
	})

	t.Run("gemini and aistudio doc pipeline matches byte pipeline", func(t *testing.T) {
		raw := []byte(`{"generationConfig":{"thinkingConfig":{"thinkingLevel":"medium"},"responseModalities":["TEXT","IMAGE"],"imageConfig":{"aspectRatio":"1:1"}},"contents":[{"role":"user","parts":[{"text":"draw"}]}]}`)

		byteOut := fixGeminiImageAspectRatio("gemini-2.5-flash-image-preview", bytes.Clone(raw))
		byteOut = normalizeAIStudioThinkingLevel(byteOut)

		doc := ojson.NewDoc(bytes.Clone(raw))
		fixGeminiImageAspectRatioDoc("gemini-2.5-flash-image-preview", doc)
		normalizeAIStudioThinkingLevelDoc(doc)
		docOut := doc.Bytes()

		if !bytes.Equal(docOut, byteOut) {
			t.Fatalf("gemini/aistudio doc output mismatch:\ndoc:  %s\nbyte: %s", docOut, byteOut)
		}
	})
}

func TestAntigravityReasoningReplayAccumulatorClonesStreamSignatures(t *testing.T) {
	scope := antigravityReasoningReplayScope{
		modelName:  "gemini-2.5-pro",
		sessionKey: "session:test-reusable-buf",
	}
	reqPayload := []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}}`)
	acc := newAntigravityReasoningReplayAccumulator(scope, reqPayload)
	if acc == nil {
		t.Fatal("expected non-nil accumulator")
	}

	line1 := []byte(`data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"thoughtSignature":"sig_detached_reusable_01"}]}}]}}`)
	acc.ObserveSSELine(line1)
	for i := range line1 {
		line1[i] = 'X'
	}

	line2 := []byte(`data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"id":"call_1","name":"lookup","args":{"q":"test"}}}]},"finishReason":"STOP"}]}}`)
	acc.ObserveSSELine(line2)
	for i := range line2 {
		line2[i] = 'Y'
	}

	if !acc.seenSignatures["sig_detached_reusable_01"] {
		t.Fatalf("seenSignatures missing sig_detached_reusable_01 after buffer overwrite: %+v", acc.seenSignatures)
	}
	if len(acc.items) != 1 {
		t.Fatalf("len(acc.items) = %d, want 1", len(acc.items))
	}
	if got := ojson.GetBytesNoCopy(acc.items[0], "thoughtSignature").String(); got != "sig_detached_reusable_01" {
		t.Fatalf("replayed item thoughtSignature = %q, want sig_detached_reusable_01", got)
	}
}

func TestXAIResponseFilterAndNamespaceRestorerCloneStreamItemIDs(t *testing.T) {
	filter := newXAIInternalXSearchResponseFilter(true, nil)
	addedBuf := []byte(`{"type":"response.output_item.added","output_index":0,"item":{"id":"ctc_reusable_01","call_id":"xs_call_reusable_01","type":"custom_tool_call","name":"x_keyword_search"}}`)
	if got := filter.apply(addedBuf); got != nil {
		t.Fatalf("expected initial internal x_search item to be dropped, got %s", got)
	}
	for i := range addedBuf {
		addedBuf[i] = 'X'
	}

	deltaByItemID := []byte(`{"type":"response.custom_tool_call_input.delta","item_id":"ctc_reusable_01","delta":"{}"}`)
	if got := filter.apply(deltaByItemID); got != nil {
		t.Fatalf("expected delta referencing ctc_reusable_01 to be dropped after buffer overwrite, got %s", got)
	}
	doneByCallID := []byte(`{"type":"response.custom_tool_call_input.done","call_id":"xs_call_reusable_01"}`)
	if got := filter.apply(doneByCallID); got != nil {
		t.Fatalf("expected done referencing xs_call_reusable_01 to be dropped after buffer overwrite, got %s", got)
	}

	restorer := newXAINamespaceRestorer(map[string]xaiNamespaceToolRef{
		"mcp__codex_app": {
			namespace:    "mcp__codex_app",
			isDispatcher: true,
			children:     map[string]struct{}{"lookup": {}},
		},
	})
	dispAddedBuf := []byte(`{"type":"response.output_item.added","item":{"id":"fc_disp_reusable_01","type":"function_call","name":"mcp__codex_app"}}`)
	_ = restorer.restore(dispAddedBuf)
	for i := range dispAddedBuf {
		dispAddedBuf[i] = 'X'
	}
	argsDone := []byte(`{"type":"response.function_call_arguments.done","item_id":"fc_disp_reusable_01","arguments":"{\"name\":\"lookup\",\"arguments\":{\"k\":\"v\"}}"}`)
	restored := restorer.restore(argsDone)
	if got := ojson.GetBytesNoCopy(restored, "arguments").String(); got != `{"k":"v"}` {
		t.Fatalf("restored arguments = %q, want {\"k\":\"v\"}", got)
	}
}

func TestNormalizeXAIImageRefsDocArbitraryNestedExtensionPath(t *testing.T) {
	raw := []byte(`{"model":"grok-4","extension":{"image":{"image_url":"https://example.com/ext.png"}},"deep":{"arr":[{"reference_images":[{"image_url":{"url":"https://example.com/ref.png"}}]}]}}`)
	doc := ojson.NewDoc(bytes.Clone(raw))
	_ = doc.Set("model", "grok-3")
	_ = doc.Set("stream", true)
	if !normalizeXAIImageRefsDoc(doc) {
		t.Fatal("expected normalizeXAIImageRefsDoc to report true")
	}
	out := doc.Bytes()
	if got := ojson.GetBytesNoCopy(out, "extension.image.url").String(); got != "https://example.com/ext.png" {
		t.Fatalf("extension.image.url = %q, want https://example.com/ext.png", got)
	}
	if ojson.GetBytesNoCopy(out, "extension.image.image_url").Exists() {
		t.Fatalf("extension.image.image_url should be removed: %s", out)
	}
	if got := ojson.GetBytesNoCopy(out, "deep.arr.0.reference_images.0.url").String(); got != "https://example.com/ref.png" {
		t.Fatalf("deep.arr.0.reference_images.0.url = %q, want https://example.com/ref.png", got)
	}
	if ojson.GetBytesNoCopy(out, "deep.arr.0.reference_images.0.image_url").Exists() {
		t.Fatalf("deep.arr.0.reference_images.0.image_url should be removed: %s", out)
	}

	nestedInsideDeleted := ojson.NewDoc([]byte(`{"image":{"image_url":{"url":"outer","image":{"image_url":"inner"}}}}`))
	if !normalizeXAIImageRefsDoc(nestedInsideDeleted) {
		t.Fatal("expected nestedInsideDeleted to be normalized")
	}
	if got := string(nestedInsideDeleted.Bytes()); got != `{"image":{"url":"outer"}}` {
		t.Fatalf("nestedInsideDeleted = %s, want {\"image\":{\"url\":\"outer\"}}", got)
	}

	unicodeEscapedKey := ojson.NewDoc([]byte(`{"image":{"url":"keep"},"extension":{"im\u0061ge":{"image_url":"ext"}}}`))
	if !normalizeXAIImageRefsDoc(unicodeEscapedKey) {
		t.Fatal("expected unicodeEscapedKey to be normalized")
	}
	unicodeOut := unicodeEscapedKey.Bytes()
	if got := ojson.GetBytesNoCopy(unicodeOut, "extension.image.url").String(); got != "ext" {
		t.Fatalf("extension.image.url = %q, want ext; body=%s", got, unicodeOut)
	}
	if ojson.GetBytesNoCopy(unicodeOut, "extension.image.image_url").Exists() {
		t.Fatalf("extension.image.image_url should be removed: %s", unicodeOut)
	}

	newlyAddedTopKey := ojson.NewDoc([]byte(`{"model":"grok-4"}`))
	_ = newlyAddedTopKey.Set("extension.image.image_url", "new")
	if !normalizeXAIImageRefsDoc(newlyAddedTopKey) {
		t.Fatal("expected newlyAddedTopKey to be normalized")
	}
	if got := string(newlyAddedTopKey.Bytes()); got != `{"model":"grok-4","extension":{"image":{"url":"new"}}}` {
		t.Fatalf("newlyAddedTopKey = %s, want {\"model\":\"grok-4\",\"extension\":{\"image\":{\"url\":\"new\"}}}", got)
	}

	mutatedEmptyKey := ojson.NewDoc([]byte(`{"":{"image":{"image_url":"old"}}}`))
	_ = mutatedEmptyKey.Set(".image.image_url", "new")
	if !normalizeXAIImageRefsDoc(mutatedEmptyKey) {
		t.Fatal("expected mutatedEmptyKey to be normalized")
	}
	if got := string(mutatedEmptyKey.Bytes()); got != `{"":{"image":{"url":"new"}}}` {
		t.Fatalf("mutatedEmptyKey = %s, want {\"\":{\"image\":{\"url\":\"new\"}}}", got)
	}

	deletedEmptyKey := ojson.NewDoc([]byte(`{"":{"image":{"image_url":"old"}},"other":1}`))
	_ = deletedEmptyKey.Delete(".image")
	if normalizeXAIImageRefsDoc(deletedEmptyKey) {
		t.Fatal("expected deletedEmptyKey to report false after .image was deleted")
	}
	if got := string(deletedEmptyKey.Bytes()); got != `{"":{},"other":1}` {
		t.Fatalf("deletedEmptyKey = %s, want {\"\":{},\"other\":1}", got)
	}
}

func TestClaudeCacheControlDocChecksDoNotMaterializeLargeMessagesArray(t *testing.T) {
	t.Run("split messages container avoids 1MiB materialization", func(t *testing.T) {
		chunk := strings.Repeat("a", 64*1024)
		var b strings.Builder
		b.Grow(16*len(chunk) + 1024)
		b.WriteString(`{"model":"claude-sonnet-4-5","system":[{"type":"text","text":"sys"}],"messages":[`)
		for i := 0; i < 16; i++ {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(`{"role":"user","content":[{"type":"text","text":"`)
			b.WriteString(chunk)
			b.WriteString(`"}]}`)
		}
		b.WriteString(`]}`)
		raw := []byte(b.String())

		doc := ojson.NewDoc(raw)
		// Mutate messages.0 so "messages" splits into an ojson container (v.c != nil).
		_ = doc.Set("messages.0.content.0.text", "short")

		var mBefore, mAfter runtime.MemStats
		runtime.ReadMemStats(&mBefore)
		start := time.Now()
		ensureCacheControlDoc(doc)
		if got := countCacheControlsDoc(doc); got != 2 {
			t.Fatalf("countCacheControlsDoc = %d, want 2", got)
		}
		enforceCacheControlLimitDoc(doc, 4)
		upgradeClaudeCacheControlTTLDoc(doc, claudeCacheControlTTL1h)
		normalizeCacheControlTTLDoc(doc)
		stripClaudeCacheControlTTLDoc(doc)
		elapsed := time.Since(start)
		runtime.ReadMemStats(&mAfter)

		allocated := mAfter.TotalAlloc - mBefore.TotalAlloc
		if allocated > 64*1024 {
			t.Fatalf("split messages: cache control doc helpers allocated %d bytes (> 64 KiB)", allocated)
		}
		if elapsed > 20*time.Millisecond {
			t.Fatalf("split messages took %v (> 20ms); expected O(N) single-pass traversal", elapsed)
		}
	})

	t.Run("unsplit 1024 messages runs in single-pass O(N) time and <64KiB allocs", func(t *testing.T) {
		chunk := strings.Repeat("b", 1024)
		var b strings.Builder
		b.Grow(1024*(len(chunk)+64) + 256)
		b.WriteString(`{"model":"claude-sonnet-4-5","system":[{"type":"text","text":"sys","cache_control":{"type":"ephemeral"}}],"messages":[`)
		for i := 0; i < 1024; i++ {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(`{"role":"user","content":[{"type":"text","text":"`)
			b.WriteString(chunk)
			if i == 1023 {
				b.WriteString(`","cache_control":{"type":"ephemeral"}}]}`)
			} else {
				b.WriteString(`"}]}`)
			}
		}
		b.WriteString(`]}`)
		raw := []byte(b.String())

		doc := ojson.NewDoc(raw)
		// Mutate only "model" so "messages" remains unsplit in root tail.
		_ = doc.Set("model", "claude-sonnet-4-5-20250929")

		var mBefore, mAfter runtime.MemStats
		runtime.ReadMemStats(&mBefore)
		start := time.Now()
		if got := countCacheControlsDoc(doc); got != 2 {
			t.Fatalf("countCacheControlsDoc = %d, want 2", got)
		}
		enforceCacheControlLimitDoc(doc, 4)
		normalizeCacheControlTTLDoc(doc)
		elapsed := time.Since(start)
		runtime.ReadMemStats(&mAfter)

		allocated := mAfter.TotalAlloc - mBefore.TotalAlloc
		if allocated > 64*1024 {
			t.Fatalf("unsplit 1024 messages: allocated %d bytes (> 64 KiB)", allocated)
		}
		if elapsed > 20*time.Millisecond {
			t.Fatalf("unsplit 1024 messages took %v (> 20ms); expected O(N) single-pass traversal", elapsed)
		}
	})
}

func TestStripClaudeCacheControlTTLThenInjectMessagesPreservesExistingCacheControl(t *testing.T) {
	t.Run("single-block message preserves scope after ttl strip", func(t *testing.T) {
		raw := []byte(`{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":[{"type":"text","text":"hello","cache_control":{"type":"ephemeral","ttl":"5m","scope":"global"}}]}]}`)
		doc := ojson.NewDoc(raw)
		if !stripClaudeCacheControlTTLDoc(doc) {
			t.Fatal("expected stripClaudeCacheControlTTLDoc to report true")
		}
		if injectMessagesCacheControlDoc(doc) {
			t.Fatal("expected injectMessagesCacheControlDoc to report false when message already has valid cache_control")
		}
		out := doc.Bytes()
		if got := ojson.GetBytesNoCopy(out, "messages.0.content.0.cache_control.scope").String(); got != "global" {
			t.Fatalf("scope = %q, want global; body=%s", got, out)
		}
		if ojson.GetBytesNoCopy(out, "messages.0.content.0.cache_control.ttl").Exists() {
			t.Fatalf("ttl should be stripped: %s", out)
		}
	})

	t.Run("multi-block message does not inject duplicate breakpoint after ttl strip", func(t *testing.T) {
		raw := []byte(`{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":[{"type":"text","text":"first","cache_control":{"type":"ephemeral","ttl":"1h"}},{"type":"text","text":"second"}]}]}`)
		doc := ojson.NewDoc(raw)
		if !stripClaudeCacheControlTTLDoc(doc) {
			t.Fatal("expected stripClaudeCacheControlTTLDoc to report true")
		}
		if injectMessagesCacheControlDoc(doc) {
			t.Fatal("expected injectMessagesCacheControlDoc to report false when first block already has valid cache_control")
		}
		out := doc.Bytes()
		if count := countCacheControls(out); count != 1 {
			t.Fatalf("countCacheControls = %d, want 1; body=%s", count, out)
		}
		if ojson.GetBytesNoCopy(out, "messages.0.content.1.cache_control").Exists() {
			t.Fatalf("unexpected duplicate cache_control on second block: %s", out)
		}
	})
}

func TestClaudeStreamDocScannerEdgeCases(t *testing.T) {
	t.Run("large cache_control objects >300 bytes are counted limited to 4 and have ttl stripped", func(t *testing.T) {
		pad := strings.Repeat(" ", 320)
		largeCC := `{"type":"ephemeral","ttl":"1h"` + pad + `}`
		raw := []byte(fmt.Sprintf(
			`{"model":"claude-sonnet-4-5","system":[{"type":"text","text":"s0","cache_control":%s},{"type":"text","text":"s1","cache_control":%s}],"messages":[{"role":"user","content":[{"type":"text","text":"m0","cache_control":%s},{"type":"text","text":"m1","cache_control":%s},{"type":"text","text":"m2","cache_control":%s}]}]}`,
			largeCC, largeCC, largeCC, largeCC, largeCC,
		))
		doc := ojson.NewDoc(raw)
		if got := countCacheControlsDoc(doc); got != 5 {
			t.Fatalf("countCacheControlsDoc = %d, want 5", got)
		}
		if !enforceCacheControlLimitDoc(doc, 4) {
			t.Fatal("expected enforceCacheControlLimitDoc to report true")
		}
		if got := countCacheControlsDoc(doc); got != 4 {
			t.Fatalf("countCacheControlsDoc after limit = %d, want 4", got)
		}
		if !stripClaudeCacheControlTTLDoc(doc) {
			t.Fatal("expected stripClaudeCacheControlTTLDoc to report true")
		}
		out := doc.Bytes()
		if ojson.GetBytesNoCopy(out, "system.1.cache_control.ttl").Exists() ||
			ojson.GetBytesNoCopy(out, "messages.0.content.0.cache_control.ttl").Exists() ||
			ojson.GetBytesNoCopy(out, "messages.0.content.1.cache_control.ttl").Exists() ||
			ojson.GetBytesNoCopy(out, "messages.0.content.2.cache_control.ttl").Exists() {
			t.Fatalf("expected all remaining cache_control ttl fields to be stripped: %s", out)
		}
	})

	t.Run("unicode escaped redacted_thinking is not treated as cacheable", func(t *testing.T) {
		escapedRedactedThinking := `\u0072\u0065\u0064\u0061\u0063\u0074\u0065\u0064\u005f\u0074\u0068\u0069\u006e\u006b\u0069\u006e\u0067`
		raw := []byte(`{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":[{"type":"text","text":"question"}]},{"role":"assistant","content":[{"type":"text","text":"answer"},{"type":"` + escapedRedactedThinking + `","data":"enc"}]}]}`)
		doc := ojson.NewDoc(bytes.Clone(raw))
		if !injectMessagesCacheControlDoc(doc) {
			t.Fatal("expected injectMessagesCacheControlDoc to report true")
		}
		out := doc.Bytes()
		if ojson.GetBytesNoCopy(out, "messages.1.content.0.cache_control").Exists() ||
			ojson.GetBytesNoCopy(out, "messages.1.content.1.cache_control").Exists() {
			t.Fatalf("assistant message ending with unicode-escaped redacted_thinking must not receive cache_control: %s", out)
		}
		if !ojson.GetBytesNoCopy(out, "messages.0.content.0.cache_control").Exists() {
			t.Fatalf("expected user message to receive rolling cache_control: %s", out)
		}
	})

	t.Run("unescaped ideographic space is treated as whitespace identically to strings.TrimSpace", func(t *testing.T) {
		raw := []byte("{\"model\":\"claude-sonnet-4-5\",\"messages\":[{\"role\":\"user\",\"content\":[{\"type\":\"text\",\"text\":\"first\"}]},{\"role\":\"user\",\"content\":[{\"type\":\"text\",\"text\":\"\u3000\u3000\"}]}]}")
		want := injectMessagesCacheControl(bytes.Clone(raw))
		doc := ojson.NewDoc(bytes.Clone(raw))
		injectMessagesCacheControlDoc(doc)
		got := doc.Bytes()
		if !bytes.Equal(got, want) {
			t.Fatalf("injectMessagesCacheControlDoc mismatch for ideographic space:\ngot:  %s\nwant: %s", got, want)
		}
		if ojson.GetBytesNoCopy(got, "messages.1.content.0.cache_control").Exists() {
			t.Fatalf("whitespace-only \u3000 block must not receive cache_control: %s", got)
		}
		if !ojson.GetBytesNoCopy(got, "messages.0.content.0.cache_control").Exists() {
			t.Fatalf("expected first user message to receive cache_control: %s", got)
		}
	})
}

func TestNormalizeXAIImageRefsDocAvoidsLargePayloadCopy(t *testing.T) {
	largePrompt := strings.Repeat("x", 1<<20)
	raw := []byte(`{"model":"grok-imagine-image","prompt":"` + largePrompt + `","image":{"image_url":"https://example.com/a.png"}}`)
	doc := ojson.NewDoc(raw)

	// Warm up ojson WriteTo buffer pool before measuring allocations.
	warmDoc := ojson.NewDoc([]byte(`{"model":"grok-imagine-image","image":{"image_url":"https://example.com/w.png"}}`))
	normalizeXAIImageRefsDoc(warmDoc)

	var mBefore, mAfter runtime.MemStats
	runtime.ReadMemStats(&mBefore)
	if !normalizeXAIImageRefsDoc(doc) {
		t.Fatal("expected normalizeXAIImageRefsDoc to report true")
	}
	runtime.ReadMemStats(&mAfter)

	allocated := mAfter.TotalAlloc - mBefore.TotalAlloc
	if allocated > 32*1024 {
		t.Fatalf("normalizeXAIImageRefsDoc allocated %d bytes for 1MiB payload, want < 32 KiB", allocated)
	}
	out := doc.Bytes()
	if got := ojson.GetBytesNoCopy(out, "image.url").String(); got != "https://example.com/a.png" {
		t.Fatalf("image.url = %q, want https://example.com/a.png", got)
	}
	if ojson.GetBytesNoCopy(out, "image.image_url").Exists() {
		t.Fatalf("image.image_url should be deleted")
	}
}
