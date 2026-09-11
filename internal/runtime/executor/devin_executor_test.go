package executor

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestDevinExecutorIdentifierAndFormat(t *testing.T) {
	exec := NewDevinExecutor(&config.Config{})
	if exec.Identifier() != "devin" {
		t.Fatalf("Identifier() = %q, want %q", exec.Identifier(), "devin")
	}

	format := exec.RequestToFormat(cliproxyexecutor.Request{}, cliproxyexecutor.Options{})
	if format != sdktranslator.FormatInteractions {
		t.Fatalf("RequestToFormat() = %q, want %q", format, sdktranslator.FormatInteractions)
	}
}

func TestDevinExecutorPrepareRequest(t *testing.T) {
	exec := NewDevinExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Attributes: map[string]string{
			"api_key": "my-secret-key",
		},
	}
	req, err := http.NewRequest(http.MethodPost, "https://server.codeium.com/test", nil)
	if err != nil {
		t.Fatalf("NewRequest failed: %v", err)
	}

	if err := exec.PrepareRequest(req, auth); err != nil {
		t.Fatalf("PrepareRequest failed: %v", err)
	}

	authHeader := req.Header.Get("Authorization")
	if authHeader != "Basic my-secret-key-my-secret-key" {
		t.Fatalf("Authorization = %q, want %q", authHeader, "Basic my-secret-key-my-secret-key")
	}
	if req.Header.Get("Content-Type") != "application/connect+proto" {
		t.Fatalf("Content-Type = %q, want application/connect+proto", req.Header.Get("Content-Type"))
	}
	if req.Header.Get("Connect-Protocol-Version") != "1" {
		t.Fatalf("Connect-Protocol-Version = %q, want 1", req.Header.Get("Connect-Protocol-Version"))
	}
}

func TestDevinAuthCredentials(t *testing.T) {
	auth := &cliproxyauth.Auth{
		Attributes: map[string]string{
			"session_token": "token-xyz",
			"base_url":      "https://custom.endpoint.com",
			"device_seed":   "seed-456",
		},
	}
	apiKey, baseURL, seed := devinAuthCredentials(auth)
	if apiKey != "token-xyz" {
		t.Errorf("apiKey = %q, want token-xyz", apiKey)
	}
	if baseURL != "https://custom.endpoint.com" {
		t.Errorf("baseURL = %q, want https://custom.endpoint.com", baseURL)
	}
	if seed != "seed-456" {
		t.Errorf("seed = %q, want seed-456", seed)
	}
}

func TestDevinExecutor_GetSensitiveWords(t *testing.T) {
	eEmpty := &DevinExecutor{}
	if words := eEmpty.getSensitiveWords(); len(words) != 0 {
		t.Errorf("words = %v, want empty", words)
	}

	eWithWords := &DevinExecutor{
		cfg: &config.Config{
			Devin: config.DevinConfig{
				SensitiveWords: []string{"Hermes", "Claude Agent SDK"},
			},
		},
	}
	words := eWithWords.getSensitiveWords()
	if len(words) != 2 || words[0] != "Hermes" || words[1] != "Claude Agent SDK" {
		t.Errorf("words = %v, want [Hermes Claude Agent SDK]", words)
	}
}

func TestParseInteractionsPayload(t *testing.T) {
	interactionsPayload := []byte(`{
		"system_instruction": "You are a helpful coding assistant.",
		"generation_config": {
			"temperature": 0.8,
			"max_output_tokens": 16000,
			"thinking_level": "high"
		},
		"previous_interaction_id": "session-uuid-1",
		"input": [
			{"type":"user_input","content":[{"type":"text","text":"hello"}]},
			{"type":"thought","content":[{"type":"text","text":"planning..."}],"signature":"c2VhbGVkLnYxLnRlc3Q="},
			{"type":"model_output","content":[{"type":"text","text":"I can help with that."}]},
			{"type":"function_call","name":"read_file","id":"call_1","arguments":{"path":"main.go"}},
			{"type":"function_result","id":"call_1","result":"package main\n"}
		],
		"tools": [
			{"name":"read_file","description":"Read file content","parameters":{"type":"object"}}
		]
	}`)

	sys, prompts, tools, temp, maxTokens, sessID, cascadeID, level, _ := parseInteractionsPayload(interactionsPayload, nil)

	if sys != "You are a helpful coding assistant." {
		t.Errorf("systemPrompt = %q, want expected", sys)
	}
	if temp == nil || *temp != 0.8 {
		t.Errorf("temperature = %v, want 0.8", temp)
	}
	if maxTokens != 16000 {
		t.Errorf("maxTokens = %d, want 16000", maxTokens)
	}
	if level != "high" {
		t.Errorf("thinkingLevel = %q, want high", level)
	}
	if sessID != "session-uuid-1" || cascadeID != "session-uuid-1" {
		t.Errorf("session/cascade ID = %q / %q, want session-uuid-1", sessID, cascadeID)
	}

	if len(tools) != 1 || tools[0].Name != "read_file" {
		t.Fatalf("tools count/name mismatch: %+v", tools)
	}

	if len(prompts) != 3 {
		t.Fatalf("expected 3 prompt items (user, assistant-with-thought-and-call, tool-result), got %d: %+v", len(prompts), prompts)
	}

	// 1. User turn
	if prompts[0].Source != 1 || prompts[0].Content != "hello" {
		t.Errorf("prompt[0] user turn mismatch: %+v", prompts[0])
	}

	// 2. Assistant turn (attached thought + content + function call)
	if prompts[1].Source != 2 {
		t.Errorf("prompt[1] source = %d, want 2", prompts[1].Source)
	}
	if prompts[1].Thinking != "planning..." {
		t.Errorf("prompt[1] thinking = %q, want planning...", prompts[1].Thinking)
	}
	if string(prompts[1].Signature) != "sealed.v1.test" {
		t.Errorf("prompt[1] signature = %q, want sealed.v1.test", string(prompts[1].Signature))
	}
	if len(prompts[1].ToolCalls) != 1 || prompts[1].ToolCalls[0].Name != "read_file" {
		t.Errorf("prompt[1] tool calls mismatch: %+v", prompts[1].ToolCalls)
	}

	// 3. Tool result turn
	if prompts[2].Source != 4 || prompts[2].ToolCallID != "call_1" || prompts[2].Content != "package main\n" {
		t.Errorf("prompt[2] tool result mismatch: %+v", prompts[2])
	}
}

func TestParseInteractionsPayload_MultipleThoughtsAndZeroTemperature(t *testing.T) {
	interactionsPayload := []byte(`{
		"generation_config": {
			"temperature": 0.0
		},
		"input": [
			{"type": "user_input", "content": [{"type": "text", "text": "hello"}]},
			{"type": "thought", "text": "Thought part 1"},
			{"type": "thought", "text": "Thought part 2"},
			{"type": "model_output", "text": "Hello there!"}
		]
	}`)

	_, prompts, _, temp, _, _, _, _, _ := parseInteractionsPayload(interactionsPayload, nil)

	if temp == nil || *temp != 0.0 {
		t.Fatalf("temperature = %v, want 0.0", temp)
	}

	if len(prompts) != 2 {
		t.Fatalf("prompts len = %d, want 2", len(prompts))
	}

	asst := prompts[1]
	if asst.Source != 2 {
		t.Fatalf("assistant source = %d, want 2", asst.Source)
	}
	wantThinking := "Thought part 1\n\nThought part 2"
	if asst.Thinking != wantThinking {
		t.Fatalf("assistant thinking = %q, want %q", asst.Thinking, wantThinking)
	}
	if asst.Content != "Hello there!" {
		t.Fatalf("assistant content = %q, want Hello there!", asst.Content)
	}
}

func TestSupplementSignaturesFromOriginal(t *testing.T) {
	originalRequest := []byte(`{
		"messages": [
			{"role":"user","content":"hello"},
			{"role":"assistant","content":[
				{"type":"thinking","thinking":"let me think","signature":"Q0FRU3Rlc3Q="},
				{"type":"text","text":"here is the answer"}
			]}
		]
	}`)

	prompts := []helps.DevinPrompt{
		{Source: 1, Content: "hello"},
		{Source: 2, Content: "here is the answer"}, // signature missing in interactions
	}

	supplementSignaturesFromOriginal(originalRequest, prompts)

	if len(prompts[1].Signature) == 0 {
		t.Fatal("expected signature to be supplemented from original request")
	}
	if string(prompts[1].Signature) != "CAQStest" {
		t.Errorf("signature = %q, want CAQStest", string(prompts[1].Signature))
	}
	if prompts[1].SignatureType != "anthropic" {
		t.Errorf("signatureType = %q, want anthropic", prompts[1].SignatureType)
	}
}

func TestDevinStatusError_RetryAfter(t *testing.T) {
	// 1. HTTP 429 with integer Retry-After
	hdr429 := http.Header{}
	hdr429.Set("Retry-After", "30")
	err1 := newDevinStatusError(http.StatusTooManyRequests, hdr429, []byte("rate limited"))
	if err1.code != 429 {
		t.Fatalf("expected code 429, got %d", err1.code)
	}
	if err1.retryAfter == nil || *err1.retryAfter != 30*time.Second {
		t.Fatalf("expected retryAfter 30s, got %v", err1.retryAfter)
	}

	// 2. HTTP 429 with HTTP Date
	hdrDate := http.Header{}
	futureTime := time.Now().Add(60 * time.Second).UTC().Format(http.TimeFormat)
	hdrDate.Set("Retry-After", futureTime)
	err2 := newDevinStatusError(http.StatusTooManyRequests, hdrDate, []byte("rate limited"))
	if err2.retryAfter == nil || *err2.retryAfter <= 0 || *err2.retryAfter > 65*time.Second {
		t.Fatalf("expected retryAfter ~60s, got %v", err2.retryAfter)
	}

	// 3. HTTP 500 with Retry-After (should not set retryAfter)
	err3 := newDevinStatusError(http.StatusInternalServerError, hdr429, []byte("server error"))
	if err3.retryAfter != nil {
		t.Fatalf("expected nil retryAfter for 500, got %v", err3.retryAfter)
	}
}

func TestResolveDevinSessionAndCascadeIDs(t *testing.T) {
	// 1. Direct UUID preservation
	rawUUID := "8176cf8a-feff-44c1-8e3e-b10f6d737ae1"
	sid, cid := resolveDevinSessionAndCascadeIDs(context.Background(), rawUUID, rawUUID, cliproxyexecutor.Options{})
	if sid != rawUUID || cid != rawUUID {
		t.Fatalf("sid/cid = %q/%q, want %q", sid, cid, rawUUID)
	}

	// 2. Non-UUID mapping to deterministic UUID
	sid1, cid1 := resolveDevinSessionAndCascadeIDs(context.Background(), "lcp:12345678", "", cliproxyexecutor.Options{})
	sid2, cid2 := resolveDevinSessionAndCascadeIDs(context.Background(), "lcp:12345678", "", cliproxyexecutor.Options{})
	if sid1 != sid2 || cid1 != cid2 {
		t.Fatalf("deterministic mapping failed: %q != %q", sid1, sid2)
	}
	if _, err := uuid.Parse(sid1); err != nil {
		t.Fatalf("mapped sid is not a valid UUID: %q", sid1)
	}

	// 3. Fallback to ctx session
	ctx := util.WithSessionID(context.Background(), "ctx-session-abc")
	sidCtx, cidCtx := resolveDevinSessionAndCascadeIDs(ctx, "", "", cliproxyexecutor.Options{})
	if _, err := uuid.Parse(sidCtx); err != nil {
		t.Fatalf("sidCtx is not a valid UUID: %q", sidCtx)
	}
	if sidCtx != cidCtx {
		t.Fatalf("sidCtx %q != cidCtx %q", sidCtx, cidCtx)
	}

	// 4. Fallback to fresh UUID when nothing supplied
	sidEmpty, cidEmpty := resolveDevinSessionAndCascadeIDs(context.Background(), "", "", cliproxyexecutor.Options{})
	if _, err := uuid.Parse(sidEmpty); err != nil {
		t.Fatalf("sidEmpty is not a valid UUID: %q", sidEmpty)
	}
	if sidEmpty != cidEmpty {
		t.Fatalf("sidEmpty %q != cidEmpty %q", sidEmpty, cidEmpty)
	}
}

func TestConsumeDevinFramesToInteractions(t *testing.T) {
	// Synthesize a Connect stream with 2 data frames and 1 EOS trailer
	var streamBuf bytes.Buffer

	// Frame 1: thinking + content
	var f1 []byte
	f1 = appendDevinFieldBytes(f1, 1, []byte("bot-uuid-1"))
	f1 = appendDevinFieldBytes(f1, 9, []byte("reasoning step"))
	f1 = appendDevinFieldBytes(f1, 3, []byte("hello response"))
	f1 = appendDevinFieldBytes(f1, 10, []byte("sealed.v1.sig"))
	streamBuf.Write(helps.WrapConnectEnvelope(f1))

	// Frame 2: tool call + usage
	var f2 []byte
	var tcBytes []byte
	tcBytes = appendDevinFieldBytes(tcBytes, 1, []byte("toolu_1"))
	tcBytes = appendDevinFieldBytes(tcBytes, 2, []byte("bash"))
	tcBytes = appendDevinFieldBytes(tcBytes, 3, []byte(`{"command":"ls"}`))
	f2 = appendDevinFieldBytes(f2, 6, tcBytes)

	var usageBytes []byte
	usageBytes = appendVarintField(usageBytes, 2, 100) // prompt
	usageBytes = appendVarintField(usageBytes, 3, 50)  // completion
	usageBytes = appendVarintField(usageBytes, 5, 20)  // cached
	f2 = appendDevinFieldBytes(f2, 7, usageBytes)
	streamBuf.Write(helps.WrapConnectEnvelope(f2))

	// Frame 3: EOS Trailer flag 0x02
	trailerJSON := []byte(`{}`)
	streamBuf.Write(helps.WrapConnectEnvelopeWithFlag(helps.ConnectFlagEndStream, trailerJSON))

	interactionsJSON, err := consumeDevinFramesToInteractions(&streamBuf, "swe-2", "swe-2-high")
	if err != nil {
		t.Fatalf("consumeDevinFramesToInteractions failed: %v", err)
	}

	root := gjson.ParseBytes(interactionsJSON)
	if root.Get("status").String() != "completed" {
		t.Errorf("status = %q, want completed", root.Get("status").String())
	}
	if root.Get("usage.total_input_tokens").Int() != 100 {
		t.Errorf("input tokens = %d, want 100", root.Get("usage.total_input_tokens").Int())
	}
	if root.Get("usage.total_output_tokens").Int() != 50 {
		t.Errorf("output tokens = %d, want 50", root.Get("usage.total_output_tokens").Int())
	}
	if root.Get("usage.total_cached_tokens").Int() != 20 {
		t.Errorf("cached tokens = %d, want 20", root.Get("usage.total_cached_tokens").Int())
	}

	steps := root.Get("steps").Array()
	if len(steps) != 3 {
		t.Fatalf("steps count = %d, want 3 (thought, model_output, function_call). Payload: %s", len(steps), string(interactionsJSON))
	}

	// Thought step has signature
	if steps[0].Get("type").String() != "thought" {
		t.Errorf("step[0] type = %q, want thought", steps[0].Get("type").String())
	}
	expectedSig := "sealed.v1.sig"
	if steps[0].Get("signature").String() != expectedSig {
		t.Errorf("step[0] signature = %q, want %q", steps[0].Get("signature").String(), expectedSig)
	}

	// Model output step
	if steps[1].Get("type").String() != "model_output" {
		t.Errorf("step[1] type = %q, want model_output", steps[1].Get("type").String())
	}
	if steps[1].Get("content.0.text").String() != "hello response" {
		t.Errorf("step[1] text = %q, want 'hello response'", steps[1].Get("content.0.text").String())
	}

	// Function call step
	if steps[2].Get("type").String() != "function_call" {
		t.Errorf("step[2] type = %q, want function_call", steps[2].Get("type").String())
	}
	if steps[2].Get("name").String() != "bash" {
		t.Errorf("step[2] tool name = %q, want bash", steps[2].Get("name").String())
	}
}

func appendDevinFieldBytes(dst []byte, fieldNum int, val []byte) []byte {
	tag := uint64(fieldNum<<3 | 2)
	dst = appendVarintRaw(dst, tag)
	dst = appendVarintRaw(dst, uint64(len(val)))
	dst = append(dst, val...)
	return dst
}

func appendVarintField(dst []byte, fieldNum int, v uint64) []byte {
	tag := uint64(fieldNum<<3 | 0)
	dst = appendVarintRaw(dst, tag)
	dst = appendVarintRaw(dst, v)
	return dst
}

func appendVarintRaw(dst []byte, v uint64) []byte {
	for v >= 0x80 {
		dst = append(dst, byte(v)|0x80)
		v >>= 7
	}
	dst = append(dst, byte(v))
	return dst
}

func TestParseInteractionsPayload_WithImages(t *testing.T) {
	interactionsPayload := []byte(`{
		"input": [
			{
				"type": "user_input",
				"content": [
					{"type": "text", "text": "transcribe this"},
					{"type": "image", "mime_type": "image/png", "data": "iVBORw0KGgoAAAANSUhEUgAA"}
				]
			}
		]
	}`)

	_, prompts, _, _, _, _, _, _, _ := parseInteractionsPayload(interactionsPayload, nil)

	if len(prompts) != 1 {
		t.Fatalf("expected 1 prompt, got %d", len(prompts))
	}
	p := prompts[0]
	if len(p.Images) != 1 {
		t.Fatalf("expected 1 image in prompt, got %d", len(p.Images))
	}
	if p.Images[0].Base64Data != "iVBORw0KGgoAAAANSUhEUgAA" {
		t.Errorf("image base64 = %q", p.Images[0].Base64Data)
	}
	if p.Images[0].MimeType != "image/png" {
		t.Errorf("image mime = %q, want image/png", p.Images[0].MimeType)
	}
	if !strings.HasPrefix(p.Content, "[Image 1: pasted_image_1.png]\n\ntranscribe this") {
		t.Errorf("prompt content = %q, want expected prefix", p.Content)
	}
}

func TestSupplementImagesFromOriginal(t *testing.T) {
	origRequest := []byte(`{
		"messages": [
			{
				"role": "user",
				"content": [
					{"type": "text", "text": "look at this"},
					{"type": "image_url", "image_url": {"url": "data:image/jpeg;base64,/9j/4AAQSkZJRgABAQEASABIAAD"}}
				]
			}
		]
	}`)

	prompts := []helps.DevinPrompt{
		{
			Source:  1,
			Content: "look at this",
		},
	}

	supplementImagesFromOriginal(origRequest, prompts)

	if len(prompts[0].Images) != 1 {
		t.Fatalf("expected 1 image supplemented, got %d", len(prompts[0].Images))
	}
	if prompts[0].Images[0].MimeType != "image/jpeg" {
		t.Errorf("mime_type = %q, want image/jpeg", prompts[0].Images[0].MimeType)
	}
	if prompts[0].Images[0].Base64Data != "/9j/4AAQSkZJRgABAQEASABIAAD" {
		t.Errorf("base64 = %q", prompts[0].Images[0].Base64Data)
	}
	if !strings.Contains(prompts[0].Content, "[Image 1: pasted_image_1.jpg]") {
		t.Errorf("content missing image header: %q", prompts[0].Content)
	}
}
