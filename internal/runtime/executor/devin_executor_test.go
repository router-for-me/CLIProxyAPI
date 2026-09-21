package executor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	devinauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/devin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	interactionsclaude "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/interactions/claude"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	log "github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
	"github.com/tidwall/gjson"
	"google.golang.org/protobuf/encoding/protowire"
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
	if req.Header.Get("Accept") != "*/*" {
		t.Fatalf("Accept = %q, want */*", req.Header.Get("Accept"))
	}
	sentryTrace := req.Header.Get("Sentry-Trace")
	if sentryTrace == "" {
		t.Fatalf("Sentry-Trace header missing")
	}
	parts := strings.Split(sentryTrace, "-")
	if len(parts) != 3 || len(parts[0]) != 32 || len(parts[1]) != 16 || parts[2] != "1" {
		t.Fatalf("invalid Sentry-Trace format: %q", sentryTrace)
	}

	// Verify User-Agent suppression on the wire
	var receivedUA []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedUA = r.Header["User-Agent"]
	}))
	defer ts.Close()

	wireReq, err := http.NewRequest(http.MethodPost, ts.URL, nil)
	if err != nil {
		t.Fatalf("NewRequest failed: %v", err)
	}
	if err := exec.PrepareRequest(wireReq, auth); err != nil {
		t.Fatalf("PrepareRequest failed: %v", err)
	}
	resp, err := ts.Client().Do(wireReq)
	if err != nil {
		t.Fatalf("Do request failed: %v", err)
	}
	_ = resp.Body.Close()

	if len(receivedUA) != 0 {
		t.Errorf("expected User-Agent to be completely omitted on wire, got: %v", receivedUA)
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
				SensitiveWords: []string{"sample-word-1", "sample-word-2"},
			},
		},
	}
	words := eWithWords.getSensitiveWords()
	if len(words) != 2 || words[0] != "sample-word-1" || words[1] != "sample-word-2" {
		t.Errorf("words = %v, want [sample-word-1 sample-word-2]", words)
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
			{"type":"function_result","call_id":"call_1","result":"package main\n"}
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

func TestDetectSignatureType_GlobalDetectorIntegration(t *testing.T) {
	tests := []struct {
		name     string
		sig      string
		wantType string
	}{
		{
			name:     "Devin native sealed signature",
			sig:      "sealed.v1.abcde12345",
			wantType: "sealed",
		},
		{
			name:     "Anthropic CAQS signature",
			sig:      "CAQStest12345",
			wantType: "anthropic",
		},
		{
			name:     "Anthropic with claude# prefix",
			sig:      "claude#CAQStest12345",
			wantType: "anthropic",
		},
		{
			name:     "OpenAI gAAAA Fernet signature",
			sig:      "gAAAAABk1234567890",
			wantType: "openai",
		},
		{
			name:     "Gemini AY signature",
			sig:      "AY12345",
			wantType: "gemini",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := detectSignatureType(tt.sig)
			if got != tt.wantType {
				t.Errorf("detectSignatureType(%q) = %q, want %q", tt.sig, got, tt.wantType)
			}
			_, pType := parseSignatureBytes(tt.sig)
			if pType != tt.wantType {
				t.Errorf("parseSignatureBytes(%q) type = %q, want %q", tt.sig, pType, tt.wantType)
			}
		})
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

	interactionsJSON, respLog, err := consumeDevinFramesToInteractions(&streamBuf, "swe-2", "swe-2-high")
	if err != nil {
		t.Fatalf("consumeDevinFramesToInteractions failed: %v", err)
	}
	if respLog == nil {
		t.Fatal("expected non-nil respLog")
	}
	if respLog.FramesCount != 3 {
		t.Errorf("FramesCount = %d, want 3", respLog.FramesCount)
	}

	root := gjson.ParseBytes(interactionsJSON)
	if root.Get("status").String() != "completed" {
		t.Errorf("status = %q, want completed", root.Get("status").String())
	}
	if root.Get("usage.total_input_tokens").Int() != 120 {
		t.Errorf("input tokens = %d, want 120", root.Get("usage.total_input_tokens").Int())
	}
	if root.Get("usage.total_output_tokens").Int() != 50 {
		t.Errorf("output tokens = %d, want 50", root.Get("usage.total_output_tokens").Int())
	}
	if root.Get("usage.total_cached_tokens").Int() != 20 {
		t.Errorf("cached tokens = %d, want 20", root.Get("usage.total_cached_tokens").Int())
	}
	if root.Get("usage.total_tokens").Int() != 170 {
		t.Errorf("total tokens = %d, want 170", root.Get("usage.total_tokens").Int())
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

func TestDevinExecutor_Refresh(t *testing.T) {
	// Build mock protobuf response
	var planInfo []byte
	planInfo = protowire.AppendTag(planInfo, 2, protowire.BytesType)
	planInfo = protowire.AppendString(planInfo, "Pro")

	var orgInfo []byte
	orgInfo = protowire.AppendTag(orgInfo, 4, protowire.BytesType)
	orgInfo = protowire.AppendString(orgInfo, "org-test-devin")
	orgInfo = protowire.AppendTag(orgInfo, 8, protowire.BytesType)
	orgInfo = protowire.AppendString(orgInfo, "XCodeCLI")
	planInfo = protowire.AppendTag(planInfo, 33, protowire.BytesType)
	planInfo = protowire.AppendBytes(planInfo, orgInfo)

	var planStatus []byte
	planStatus = protowire.AppendTag(planStatus, 1, protowire.BytesType)
	planStatus = protowire.AppendBytes(planStatus, planInfo)
	planStatus = protowire.AppendTag(planStatus, 14, protowire.VarintType)
	planStatus = protowire.AppendVarint(planStatus, 95)
	planStatus = protowire.AppendTag(planStatus, 15, protowire.VarintType)
	planStatus = protowire.AppendVarint(planStatus, 45)
	planStatus = protowire.AppendTag(planStatus, 17, protowire.VarintType)
	planStatus = protowire.AppendVarint(planStatus, 1789200000)
	planStatus = protowire.AppendTag(planStatus, 18, protowire.VarintType)
	planStatus = protowire.AppendVarint(planStatus, 1789286400)

	var userStatus []byte
	userStatus = protowire.AppendTag(userStatus, 3, protowire.BytesType)
	userStatus = protowire.AppendString(userStatus, "refreshuser")
	userStatus = protowire.AppendTag(userStatus, 5, protowire.BytesType)
	userStatus = protowire.AppendString(userStatus, "team-xyz")
	userStatus = protowire.AppendTag(userStatus, 7, protowire.BytesType)
	userStatus = protowire.AppendString(userStatus, "refreshuser@example.com")
	userStatus = protowire.AppendTag(userStatus, 13, protowire.BytesType)
	userStatus = protowire.AppendBytes(userStatus, planStatus)
	userStatus = protowire.AppendTag(userStatus, 36, protowire.BytesType)
	userStatus = protowire.AppendString(userStatus, "user-id-999")

	var mockResp []byte
	mockResp = protowire.AppendTag(mockResp, 1, protowire.BytesType)
	mockResp = protowire.AppendBytes(mockResp, userStatus)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != devinauth.DevinGetUserStatusPath {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/proto")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(mockResp)
	}))
	defer server.Close()

	cfg := &config.Config{}
	exec := NewDevinExecutor(cfg)

	auth := &cliproxyauth.Auth{
		ID:       "devin-refresh.json",
		Provider: "devin",
		Attributes: map[string]string{
			"api_key":  "devin-session-token$test",
			"base_url": server.URL,
		},
		Metadata: map[string]any{
			"api_key":  "devin-session-token$test",
			"base_url": server.URL,
		},
	}

	updated, err := exec.Refresh(context.Background(), auth)
	if err != nil {
		t.Fatalf("exec.Refresh failed: %v", err)
	}

	if updated.Metadata["plan"] != "Pro" {
		t.Errorf("expected plan Pro, got %v", updated.Metadata["plan"])
	}
	if updated.Metadata["email"] != "refreshuser@example.com" {
		t.Errorf("expected email refreshuser@example.com, got %v", updated.Metadata["email"])
	}
	if updated.Metadata["user_name"] != "refreshuser" {
		t.Errorf("expected user_name refreshuser, got %v", updated.Metadata["user_name"])
	}
	if updated.Metadata["daily_quota_remaining_percent"] != nil {
		t.Errorf("expected daily quota to not be in metadata, got %v", updated.Metadata["daily_quota_remaining_percent"])
	}
	if updated.Metadata["weekly_quota_remaining_percent"] != nil {
		t.Errorf("expected weekly quota to not be in metadata, got %v", updated.Metadata["weekly_quota_remaining_percent"])
	}
	if updated.Quota.Signals["daily_quota_remaining_percent"] != "95%" {
		t.Errorf("expected quota signal 95%%, got %q", updated.Quota.Signals["daily_quota_remaining_percent"])
	}
	if updated.Quota.Signals["weekly_quota_remaining_percent"] != "45%" {
		t.Errorf("expected quota signal 45%%, got %q", updated.Quota.Signals["weekly_quota_remaining_percent"])
	}
	if updated.Quota.ObservedAt.IsZero() {
		t.Error("expected non-zero Quota.ObservedAt")
	}
}

func TestDevinExecutor_MaxCompletionTokensClamping(t *testing.T) {
	reg := registry.GetGlobalRegistry()
	clientID := "test-devin-clamp-client"
	modelID := "devin/swe-2-clamp-test"
	reg.RegisterClient(clientID, "devin", []*registry.ModelInfo{
		{
			ID:                  modelID,
			MaxCompletionTokens: 64000,
			ContextLength:       262000,
		},
	})
	defer reg.UnregisterClient(clientID)

	cfg := &config.Config{}
	exec := NewDevinExecutor(cfg)
	auth := &cliproxyauth.Auth{
		Attributes: map[string]string{
			"api_key": "test-key",
		},
	}

	// 1. When requested max_output_tokens exceeds MaxCompletionTokens (e.g. 100000 > 64000)
	payloadOversized := []byte(`{
		"generation_config": {
			"max_output_tokens": 100000
		},
		"input": [{"type":"user_input","content":[{"type":"text","text":"hello"}]}]
	}`)
	reqOversized := cliproxyexecutor.Request{
		Model:   modelID,
		Payload: payloadOversized,
	}
	opts := cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatInteractions,
	}

	httpReq, _, _, err := exec.prepareDevinHTTPRequest(context.Background(), auth, reqOversized, opts)
	if err != nil {
		t.Fatalf("prepareDevinHTTPRequest failed: %v", err)
	}

	// Read body, unwrap 5-byte Connect envelope, and inspect Field 8 Subfield 2 (maxTokens)
	bodyBytes, err := io.ReadAll(httpReq.Body)
	if err != nil {
		t.Fatalf("read body failed: %v", err)
	}
	flag, payloadBytes, err := helps.ReadConnectFrame(bytes.NewReader(bodyBytes))
	if err != nil || flag != 0 {
		t.Fatalf("unwrap failed: %v", err)
	}

	maxTokensFound := 0
	b := payloadBytes
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			break
		}
		b = b[n:]
		if num == 8 && typ == protowire.BytesType {
			subBytes, m := protowire.ConsumeBytes(b)
			if m >= 0 {
				sb := subBytes
				for len(sb) > 0 {
					snum, styp, sn := protowire.ConsumeTag(sb)
					if sn < 0 {
						break
					}
					sb = sb[sn:]
					if snum == 2 && styp == protowire.VarintType {
						val, vn := protowire.ConsumeVarint(sb)
						if vn >= 0 {
							maxTokensFound = int(val)
							break
						}
					}
					skip := protowire.ConsumeFieldValue(snum, styp, sb)
					if skip < 0 {
						break
					}
					sb = sb[skip:]
				}
			}
			break
		}
		skip := protowire.ConsumeFieldValue(num, typ, b)
		if skip < 0 {
			break
		}
		b = b[skip:]
	}

	if maxTokensFound != 64000 {
		t.Errorf("maxTokensFound = %d, want clamped 64000", maxTokensFound)
	}
}

func TestConsumeDevinFramesToInteractions_MultiToolCallsNoPanic(t *testing.T) {
	// Build a stream with multiple tool calls across frames to verify slice growth doesn't panic on strings.Builder
	var buf bytes.Buffer
	// Frame 1: tool call 0 start + partial args
	var tc0 []byte
	tc0 = protowire.AppendTag(tc0, 1, protowire.BytesType)
	tc0 = protowire.AppendString(tc0, "call_0")
	tc0 = protowire.AppendTag(tc0, 2, protowire.BytesType)
	tc0 = protowire.AppendString(tc0, "tool_0")
	tc0 = protowire.AppendTag(tc0, 3, protowire.BytesType)
	tc0 = protowire.AppendString(tc0, `{"a":`)

	var f1 []byte
	f1 = protowire.AppendTag(f1, 6, protowire.BytesType)
	f1 = protowire.AppendBytes(f1, tc0)
	buf.Write(helps.WrapConnectEnvelope(f1))

	// Frame 2: tool call 1 start + partial args (triggers append(toolBuilders) and slice reallocation)
	var tc1 []byte
	tc1 = protowire.AppendTag(tc1, 1, protowire.BytesType)
	tc1 = protowire.AppendString(tc1, "call_1")
	tc1 = protowire.AppendTag(tc1, 2, protowire.BytesType)
	tc1 = protowire.AppendString(tc1, "tool_1")
	tc1 = protowire.AppendTag(tc1, 3, protowire.BytesType)
	tc1 = protowire.AppendString(tc1, `{"b": 2}`)

	var f2 []byte
	f2 = protowire.AppendTag(f2, 6, protowire.BytesType)
	f2 = protowire.AppendBytes(f2, tc1)
	buf.Write(helps.WrapConnectEnvelope(f2))

	// Frame 3: tool call 0 continuation
	var tc0Cont []byte
	tc0Cont = protowire.AppendTag(tc0Cont, 1, protowire.BytesType)
	tc0Cont = protowire.AppendString(tc0Cont, "call_0")
	tc0Cont = protowire.AppendTag(tc0Cont, 3, protowire.BytesType)
	tc0Cont = protowire.AppendString(tc0Cont, `1}`)

	var f3 []byte
	f3 = protowire.AppendTag(f3, 6, protowire.BytesType)
	f3 = protowire.AppendBytes(f3, tc0Cont)
	buf.Write(helps.WrapConnectEnvelope(f3))

	// EOS frame
	buf.Write(helps.WrapConnectEnvelopeWithFlag(helps.ConnectFlagEndStream, []byte(`{}`)))

	interactionsJSON, respLog, err := consumeDevinFramesToInteractions(&buf, "devin/swe-2", "swe-2-high")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if respLog == nil {
		t.Fatal("expected non-nil respLog")
	}

	root := gjson.ParseBytes(interactionsJSON)
	steps := root.Get("steps").Array()
	if len(steps) != 2 {
		t.Fatalf("expected 2 function_call steps, got %d", len(steps))
	}
	if steps[0].Get("name").String() != "tool_0" || steps[0].Get("arguments").Raw != `{"a":1}` {
		t.Errorf("step 0 arguments = %q, want {\"a\":1}", steps[0].Get("arguments").Raw)
	}
	if steps[1].Get("name").String() != "tool_1" || steps[1].Get("arguments").Raw != `{"b": 2}` {
		t.Errorf("step 1 arguments = %q, want {\"b\": 2}", steps[1].Get("arguments").Raw)
	}
}

func TestStreamDevinFrames_InterleavedThinkingAndContent(t *testing.T) {
	// Frame 1: thinking part 1
	var f1 []byte
	f1 = protowire.AppendTag(f1, 9, protowire.BytesType)
	f1 = protowire.AppendString(f1, "thought 1")

	// Frame 2: content text
	var f2 []byte
	f2 = protowire.AppendTag(f2, 3, protowire.BytesType)
	f2 = protowire.AppendString(f2, "content 1")

	// Frame 3: thinking part 2 (interleaved after content)
	var f3 []byte
	f3 = protowire.AppendTag(f3, 9, protowire.BytesType)
	f3 = protowire.AppendString(f3, "thought 2")

	// Frame 4: content text 2
	var f4 []byte
	f4 = protowire.AppendTag(f4, 3, protowire.BytesType)
	f4 = protowire.AppendString(f4, "content 2")

	var buf bytes.Buffer
	buf.Write(helps.WrapConnectEnvelope(f1))
	buf.Write(helps.WrapConnectEnvelope(f2))
	buf.Write(helps.WrapConnectEnvelope(f3))
	buf.Write(helps.WrapConnectEnvelope(f4))
	buf.Write(helps.WrapConnectEnvelopeWithFlag(helps.ConnectFlagEndStream, []byte(`{}`)))

	e := &DevinExecutor{}
	out := make(chan cliproxyexecutor.StreamChunk, 50)
	opts := cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatInteractions,
	}

	go func() {
		defer close(out)
		e.streamDevinFrames(
			context.Background(),
			&buf,
			cliproxyexecutor.Request{Model: "devin/swe-2"},
			opts,
			"swe-2-high",
			sdktranslator.FormatInteractions,
			nil,
			out,
		)
	}()

	var events []gjson.Result
	for chunk := range out {
		if chunk.Err != nil {
			t.Fatalf("unexpected chunk error: %v", chunk.Err)
		}
		raw := string(chunk.Payload)
		if strings.HasPrefix(raw, "data: ") && !strings.Contains(raw, "[DONE]") {
			data := strings.TrimPrefix(raw, "data: ")
			data = strings.TrimSpace(data)
			events = append(events, gjson.Parse(data))
		}
	}

	// Verify step sequence:
	// 1. step.start (0, thought)
	// 2. step.stop (0)
	// 3. step.start (1, model_output)
	// 4. step.stop (1)
	// 5. step.start (2, thought)
	// 6. step.stop (2)
	// 7. step.start (3, model_output)
	// 8. step.stop (3)
	var stepEvents []string
	for _, ev := range events {
		eventType := ev.Get("event_type").String()
		if eventType == "step.start" {
			stepEvents = append(stepEvents, fmt.Sprintf("start(%d,%s)", ev.Get("index").Int(), ev.Get("step.type").String()))
		} else if eventType == "step.stop" {
			stepEvents = append(stepEvents, fmt.Sprintf("stop(%d)", ev.Get("index").Int()))
		}
	}

	expectedEvents := []string{
		"start(0,thought)",
		"stop(0)",
		"start(1,model_output)",
		"stop(1)",
		"start(2,thought)",
		"stop(2)",
		"start(3,model_output)",
		"stop(3)",
	}

	if len(stepEvents) != len(expectedEvents) {
		t.Fatalf("got step events %v, want %v", stepEvents, expectedEvents)
	}
	for i := range expectedEvents {
		if stepEvents[i] != expectedEvents[i] {
			t.Errorf("step event %d = %s, want %s", i, stepEvents[i], expectedEvents[i])
		}
	}
}

func TestStreamDevinFrames_SequentialToolCallsSameIndexDifferentID(t *testing.T) {
	// Simulate two sequential tool calls with the same tc.Index (0) but different IDs:
	// 1. title_0 (name: title, arguments: {"title": "Triage issue 5802"})
	// 2. bash_1 (name: bash, arguments: {"command": "gh issue view 5802 2>&1 | head -100"})

	// Frame 1: title_0
	var tc0 []byte
	tc0 = protowire.AppendTag(tc0, 1, protowire.BytesType)
	tc0 = protowire.AppendString(tc0, "title_0")
	tc0 = protowire.AppendTag(tc0, 2, protowire.BytesType)
	tc0 = protowire.AppendString(tc0, "title")
	tc0 = protowire.AppendTag(tc0, 3, protowire.BytesType)
	tc0 = protowire.AppendString(tc0, `{"title": "Triage issue 5802"}`)

	var f1 []byte
	f1 = protowire.AppendTag(f1, 6, protowire.BytesType)
	f1 = protowire.AppendBytes(f1, tc0)

	// Frame 2: bash_1
	var tc1 []byte
	tc1 = protowire.AppendTag(tc1, 1, protowire.BytesType)
	tc1 = protowire.AppendString(tc1, "bash_1")
	tc1 = protowire.AppendTag(tc1, 2, protowire.BytesType)
	tc1 = protowire.AppendString(tc1, "bash")
	tc1 = protowire.AppendTag(tc1, 3, protowire.BytesType)
	tc1 = protowire.AppendString(tc1, `{"command": "gh issue view 5802 2>&1 | head -100"}`)

	var f2 []byte
	f2 = protowire.AppendTag(f2, 6, protowire.BytesType)
	f2 = protowire.AppendBytes(f2, tc1)

	var buf bytes.Buffer
	buf.Write(helps.WrapConnectEnvelope(f1))
	buf.Write(helps.WrapConnectEnvelope(f2))
	buf.Write(helps.WrapConnectEnvelopeWithFlag(helps.ConnectFlagEndStream, []byte(`{}`)))

	exec := NewDevinExecutor(&config.Config{})
	out := make(chan cliproxyexecutor.StreamChunk, 50)
	opts := cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatInteractions,
	}

	go func() {
		defer close(out)
		exec.streamDevinFrames(
			context.Background(),
			&buf,
			cliproxyexecutor.Request{Model: "devin/swe-2"},
			opts,
			"chat-model-uid",
			sdktranslator.FormatInteractions,
			nil,
			out,
		)
	}()

	var chunks []cliproxyexecutor.StreamChunk
	for chunk := range out {
		if chunk.Err != nil {
			t.Fatalf("unexpected chunk error: %v", chunk.Err)
		}
		chunks = append(chunks, chunk)
	}

	var events []gjson.Result
	for _, chunk := range chunks {
		lines := strings.Split(string(chunk.Payload), "\n")
		for _, line := range lines {
			if strings.HasPrefix(line, "data: ") {
				data := strings.TrimPrefix(line, "data: ")
				if strings.TrimSpace(data) != "[DONE]" {
					events = append(events, gjson.Parse(data))
				}
			}
		}
	}

	var toolCallsStarted []string
	var toolCallsStopped []int64
	stepArgs := make(map[int64]*strings.Builder)
	for _, ev := range events {
		eventType := ev.Get("event_type").String()
		if eventType == "step.start" && ev.Get("step.type").String() == "function_call" {
			idx := ev.Get("index").Int()
			toolCallsStarted = append(toolCallsStarted, fmt.Sprintf("index:%d,id:%s,name:%s", idx, ev.Get("step.id").String(), ev.Get("step.name").String()))
			stepArgs[idx] = &strings.Builder{}
		} else if eventType == "step.delta" && ev.Get("delta.type").String() == "arguments_delta" {
			idx := ev.Get("index").Int()
			if b, ok := stepArgs[idx]; ok {
				b.WriteString(ev.Get("delta.arguments").String())
			}
		} else if eventType == "step.stop" {
			toolCallsStopped = append(toolCallsStopped, ev.Get("index").Int())
		}
	}

	if len(toolCallsStarted) != 2 {
		t.Fatalf("expected 2 tool calls started, got %d: %v", len(toolCallsStarted), toolCallsStarted)
	}
	if toolCallsStarted[0] != "index:0,id:title_0,name:title" {
		t.Errorf("tool call 0 = %q, want index:0,id:title_0,name:title", toolCallsStarted[0])
	}
	if toolCallsStarted[1] != "index:1,id:bash_1,name:bash" {
		t.Errorf("tool call 1 = %q, want index:1,id:bash_1,name:bash", toolCallsStarted[1])
	}
	if len(toolCallsStopped) != 2 {
		t.Fatalf("expected 2 tool calls stopped, got %d: %v", len(toolCallsStopped), toolCallsStopped)
	}
	if toolCallsStopped[0] != 0 || toolCallsStopped[1] != 1 {
		t.Errorf("tool calls stopped indices = %v, want [0, 1]", toolCallsStopped)
	}
	if stepArgs[0].String() != `{"title": "Triage issue 5802"}` {
		t.Errorf("step 0 args = %q", stepArgs[0].String())
	}
	if !strings.Contains(stepArgs[1].String(), "2>&1") {
		t.Errorf("step 1 args should contain '2>&1': %s", stepArgs[1].String())
	}
}

func TestConsumeDevinFramesToInteractions_SequentialToolCallsSameIndexDifferentID(t *testing.T) {
	var tc0 []byte
	tc0 = protowire.AppendTag(tc0, 1, protowire.BytesType)
	tc0 = protowire.AppendString(tc0, "title_0")
	tc0 = protowire.AppendTag(tc0, 2, protowire.BytesType)
	tc0 = protowire.AppendString(tc0, "title")
	tc0 = protowire.AppendTag(tc0, 3, protowire.BytesType)
	tc0 = protowire.AppendString(tc0, `{"title": "Triage issue 5802"}`)

	var f1 []byte
	f1 = protowire.AppendTag(f1, 6, protowire.BytesType)
	f1 = protowire.AppendBytes(f1, tc0)

	var tc1 []byte
	tc1 = protowire.AppendTag(tc1, 1, protowire.BytesType)
	tc1 = protowire.AppendString(tc1, "bash_1")
	tc1 = protowire.AppendTag(tc1, 2, protowire.BytesType)
	tc1 = protowire.AppendString(tc1, "bash")
	tc1 = protowire.AppendTag(tc1, 3, protowire.BytesType)
	tc1 = protowire.AppendString(tc1, `{"command": "gh issue view 5802 2>&1 | head -100"}`)

	var f2 []byte
	f2 = protowire.AppendTag(f2, 6, protowire.BytesType)
	f2 = protowire.AppendBytes(f2, tc1)

	var buf bytes.Buffer
	buf.Write(helps.WrapConnectEnvelope(f1))
	buf.Write(helps.WrapConnectEnvelope(f2))
	buf.Write(helps.WrapConnectEnvelopeWithFlag(helps.ConnectFlagEndStream, []byte(`{}`)))

	interactionsJSON, respLog, err := consumeDevinFramesToInteractions(&buf, "devin/swe-2", "chat-model-uid")
	if err != nil {
		t.Fatalf("consumeDevinFramesToInteractions failed: %v", err)
	}
	if len(respLog.ToolCalls) != 2 {
		t.Fatalf("expected 2 tool calls in log, got %d: %v", len(respLog.ToolCalls), respLog.ToolCalls)
	}
	if respLog.ToolCalls[0].ID != "title_0" || respLog.ToolCalls[0].Name != "title" {
		t.Errorf("tool call 0 = %+v, want title_0/title", respLog.ToolCalls[0])
	}
	if respLog.ToolCalls[1].ID != "bash_1" || respLog.ToolCalls[1].Name != "bash" {
		t.Errorf("tool call 1 = %+v, want bash_1/bash", respLog.ToolCalls[1])
	}

	steps := gjson.GetBytes(interactionsJSON, "steps").Array()
	if len(steps) != 2 {
		t.Fatalf("expected 2 steps in interactions JSON, got %d", len(steps))
	}
	if steps[0].Get("name").String() != "title" || steps[0].Get("id").String() != "title_0" {
		t.Errorf("step 0 = %s", steps[0].Raw)
	}
	if steps[1].Get("name").String() != "bash" || steps[1].Get("id").String() != "bash_1" {
		t.Errorf("step 1 = %s", steps[1].Raw)
	}
	if !strings.Contains(steps[1].Get("arguments").String(), "2>&1") {
		t.Errorf("step 1 arguments should contain '2>&1': %s", steps[1].Get("arguments").String())
	}
}

func TestConsumeDevinFramesToInteractions_ToolCallsLimit128(t *testing.T) {
	var buf bytes.Buffer
	// Create 135 tool calls across sequential ID switches
	for i := 0; i < 135; i++ {
		var tc []byte
		tc = protowire.AppendTag(tc, 1, protowire.BytesType)
		tc = protowire.AppendString(tc, fmt.Sprintf("call_%d", i))
		tc = protowire.AppendTag(tc, 2, protowire.BytesType)
		tc = protowire.AppendString(tc, fmt.Sprintf("tool_%d", i))
		tc = protowire.AppendTag(tc, 3, protowire.BytesType)
		tc = protowire.AppendString(tc, `{"param":1}`)

		var f []byte
		f = protowire.AppendTag(f, 6, protowire.BytesType)
		f = protowire.AppendBytes(f, tc)
		buf.Write(helps.WrapConnectEnvelope(f))
	}
	buf.Write(helps.WrapConnectEnvelopeWithFlag(helps.ConnectFlagEndStream, []byte(`{}`)))

	_, respLog, err := consumeDevinFramesToInteractions(&buf, "devin/swe-2", "chat-model-uid")
	if err != nil {
		t.Fatalf("consumeDevinFramesToInteractions failed: %v", err)
	}
	if len(respLog.ToolCalls) != maxDevinToolCalls {
		t.Fatalf("expected exactly %d tool calls clamped, got %d", maxDevinToolCalls, len(respLog.ToolCalls))
	}
}

func TestStreamDevinFrames_SameIDDoesNotDuplicateStart(t *testing.T) {
	// Frame 1: initial title call
	var tc0 []byte
	tc0 = protowire.AppendTag(tc0, 1, protowire.BytesType)
	tc0 = protowire.AppendString(tc0, "call_1")
	tc0 = protowire.AppendTag(tc0, 2, protowire.BytesType)
	tc0 = protowire.AppendString(tc0, "tool_1")
	tc0 = protowire.AppendTag(tc0, 3, protowire.BytesType)
	tc0 = protowire.AppendString(tc0, `{"a":`)

	var f1 []byte
	f1 = protowire.AppendTag(f1, 6, protowire.BytesType)
	f1 = protowire.AppendBytes(f1, tc0)

	// Frame 2: continuation with same ID and Name
	var tc1 []byte
	tc1 = protowire.AppendTag(tc1, 1, protowire.BytesType)
	tc1 = protowire.AppendString(tc1, "call_1")
	tc1 = protowire.AppendTag(tc1, 2, protowire.BytesType)
	tc1 = protowire.AppendString(tc1, "tool_1")
	tc1 = protowire.AppendTag(tc1, 3, protowire.BytesType)
	tc1 = protowire.AppendString(tc1, `1}`)

	var f2 []byte
	f2 = protowire.AppendTag(f2, 6, protowire.BytesType)
	f2 = protowire.AppendBytes(f2, tc1)

	var buf bytes.Buffer
	buf.Write(helps.WrapConnectEnvelope(f1))
	buf.Write(helps.WrapConnectEnvelope(f2))
	buf.Write(helps.WrapConnectEnvelopeWithFlag(helps.ConnectFlagEndStream, []byte(`{}`)))

	exec := NewDevinExecutor(&config.Config{})
	out := make(chan cliproxyexecutor.StreamChunk, 50)
	opts := cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatInteractions,
	}

	go func() {
		defer close(out)
		exec.streamDevinFrames(
			context.Background(),
			&buf,
			cliproxyexecutor.Request{Model: "devin/swe-2"},
			opts,
			"chat-model-uid",
			sdktranslator.FormatInteractions,
			nil,
			out,
		)
	}()

	startCount := 0
	for chunk := range out {
		if chunk.Err != nil {
			t.Fatalf("unexpected chunk error: %v", chunk.Err)
		}
		raw := string(chunk.Payload)
		if strings.Contains(raw, `"event_type":"step.start"`) {
			startCount++
		}
	}
	if startCount != 1 {
		t.Fatalf("step.start should only be emitted once for the same tool call, got %d", startCount)
	}
}

func TestDevinExecutorClaudeToolUseAndResultViaInteractions(t *testing.T) {
	claudePayload := []byte(`{
		"model": "devin/swe-2",
		"messages": [
			{"role": "assistant", "content": [{"type": "tool_use", "id": "toolu_abc_123", "name": "bash", "input": {"command": "ls"}}]},
			{"role": "user", "content": [{"type": "tool_result", "tool_use_id": "toolu_abc_123", "content": "file.txt"}]}
		]
	}`)
	interactionsJSON := sdktranslator.TranslateRequest(sdktranslator.FormatClaude, sdktranslator.FormatInteractions, "devin/swe-2", claudePayload, false)
	_, prompts, _, _, _, _, _, _, _ := parseInteractionsPayload(interactionsJSON, nil)
	if len(prompts) != 2 {
		t.Fatalf("prompts len = %d, want 2", len(prompts))
	}
	if len(prompts[0].ToolCalls) != 1 {
		t.Fatalf("ToolCalls len = %d, want 1", len(prompts[0].ToolCalls))
	}
	if got := prompts[0].ToolCalls[0].ID; got != "toolu_abc_123" {
		t.Fatalf("tool call id = %q, want toolu_abc_123", got)
	}
	if got := prompts[1].ToolCallID; got != "toolu_abc_123" {
		t.Fatalf("tool result id = %q, want toolu_abc_123", got)
	}
	if got := prompts[1].Content; got != "file.txt" {
		t.Fatalf("tool result content = %q, want file.txt", got)
	}
}

func TestDevinExecutorOpenAIToolCallAndResultViaInteractions(t *testing.T) {
	openAIPayload := []byte(`{
		"model": "devin/swe-2",
		"messages": [
			{"role": "assistant", "tool_calls": [{"id": "call_xyz_456", "type": "function", "function": {"name": "read_file", "arguments": "{\"path\":\"main.go\"}"}}]},
			{"role": "tool", "tool_call_id": "call_xyz_456", "content": "package main"}
		]
	}`)
	interactionsJSON := sdktranslator.TranslateRequest(sdktranslator.FormatOpenAI, sdktranslator.FormatInteractions, "devin/swe-2", openAIPayload, false)
	_, prompts, _, _, _, _, _, _, _ := parseInteractionsPayload(interactionsJSON, nil)
	if len(prompts) != 2 {
		t.Fatalf("prompts len = %d, want 2", len(prompts))
	}
	if len(prompts[0].ToolCalls) != 1 {
		t.Fatalf("ToolCalls len = %d, want 1", len(prompts[0].ToolCalls))
	}
	if got := prompts[0].ToolCalls[0].ID; got != "call_xyz_456" {
		t.Fatalf("tool call id = %q, want call_xyz_456", got)
	}
	if got := prompts[1].ToolCallID; got != "call_xyz_456" {
		t.Fatalf("tool result id = %q, want call_xyz_456", got)
	}
	if got := prompts[1].Content; got != "package main" {
		t.Fatalf("tool result content = %q, want package main", got)
	}
}

func TestStreamDevinFrames_StopReasonMaxTokens(t *testing.T) {
	// Frame with StopReason = 3 (MAX_TOKENS) and partial content
	var f1 []byte
	f1 = protowire.AppendTag(f1, 3, protowire.BytesType)
	f1 = protowire.AppendString(f1, "cut short")
	f1 = protowire.AppendTag(f1, 5, protowire.VarintType)
	f1 = protowire.AppendVarint(f1, 3)

	var buf bytes.Buffer
	buf.Write(helps.WrapConnectEnvelope(f1))
	buf.Write(helps.WrapConnectEnvelopeWithFlag(helps.ConnectFlagEndStream, []byte(`{}`)))

	e := &DevinExecutor{}
	out := make(chan cliproxyexecutor.StreamChunk, 20)
	opts := cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatInteractions,
	}

	go func() {
		defer close(out)
		e.streamDevinFrames(
			context.Background(),
			&buf,
			cliproxyexecutor.Request{Model: "devin/swe-2"},
			opts,
			"swe-2-high",
			sdktranslator.FormatInteractions,
			nil,
			out,
		)
	}()

	var completedEvent gjson.Result
	for chunk := range out {
		if chunk.Err != nil {
			t.Fatalf("unexpected chunk error: %v", chunk.Err)
		}
		raw := string(chunk.Payload)
		if strings.HasPrefix(raw, "data: ") && !strings.Contains(raw, "[DONE]") {
			data := strings.TrimPrefix(raw, "data: ")
			data = strings.TrimSpace(data)
			parsed := gjson.Parse(data)
			if parsed.Get("event_type").String() == "interaction.completed" {
				completedEvent = parsed
			}
		}
	}

	if !completedEvent.Exists() {
		t.Fatalf("interaction.completed event not found")
	}
	if got := completedEvent.Get("interaction.status").String(); got != "incomplete" {
		t.Fatalf("interaction.status = %q, want incomplete. Event: %s", got, completedEvent.Raw)
	}
	if got := completedEvent.Get("interaction.finish_reason").String(); got != "length" {
		t.Fatalf("interaction.finish_reason = %q, want length. Event: %s", got, completedEvent.Raw)
	}
}

func TestConsumeDevinFramesToInteractions_StopReasonMaxTokens(t *testing.T) {
	// Frame with StopReason = 3 (MAX_TOKENS) and partial content
	var f1 []byte
	f1 = protowire.AppendTag(f1, 3, protowire.BytesType)
	f1 = protowire.AppendString(f1, "cut short")
	f1 = protowire.AppendTag(f1, 5, protowire.VarintType)
	f1 = protowire.AppendVarint(f1, 3)

	var buf bytes.Buffer
	buf.Write(helps.WrapConnectEnvelope(f1))
	buf.Write(helps.WrapConnectEnvelopeWithFlag(helps.ConnectFlagEndStream, []byte(`{}`)))

	out, _, err := consumeDevinFramesToInteractions(&buf, "devin/swe-2", "swe-2-high")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	parsed := gjson.ParseBytes(out)
	if got := parsed.Get("status").String(); got != "incomplete" {
		t.Fatalf("status = %q, want incomplete. Output: %s", got, string(out))
	}
	if got := parsed.Get("finish_reason").String(); got != "length" {
		t.Fatalf("finish_reason = %q, want length. Output: %s", got, string(out))
	}
}

func TestStreamDevinFrames_StopReasonContentFilter(t *testing.T) {
	// Frame with StopReason = 11 (CONTENT_FILTER)
	var f1 []byte
	f1 = protowire.AppendTag(f1, 3, protowire.BytesType)
	f1 = protowire.AppendString(f1, "blocked")
	f1 = protowire.AppendTag(f1, 5, protowire.VarintType)
	f1 = protowire.AppendVarint(f1, 11)

	var buf bytes.Buffer
	buf.Write(helps.WrapConnectEnvelope(f1))
	buf.Write(helps.WrapConnectEnvelopeWithFlag(helps.ConnectFlagEndStream, []byte(`{}`)))

	e := &DevinExecutor{}
	out := make(chan cliproxyexecutor.StreamChunk, 20)
	opts := cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatInteractions,
	}

	go func() {
		defer close(out)
		e.streamDevinFrames(
			context.Background(),
			&buf,
			cliproxyexecutor.Request{Model: "devin/swe-2"},
			opts,
			"swe-2-high",
			sdktranslator.FormatInteractions,
			nil,
			out,
		)
	}()

	var completedEvent gjson.Result
	for chunk := range out {
		if chunk.Err != nil {
			t.Fatalf("unexpected chunk error: %v", chunk.Err)
		}
		raw := string(chunk.Payload)
		if strings.HasPrefix(raw, "data: ") && !strings.Contains(raw, "[DONE]") {
			data := strings.TrimPrefix(raw, "data: ")
			data = strings.TrimSpace(data)
			parsed := gjson.Parse(data)
			if parsed.Get("event_type").String() == "interaction.completed" {
				completedEvent = parsed
			}
		}
	}

	if !completedEvent.Exists() {
		t.Fatalf("interaction.completed event not found")
	}
	if got := completedEvent.Get("interaction.status").String(); got != "incomplete" {
		t.Fatalf("interaction.status = %q, want incomplete", got)
	}
	if got := completedEvent.Get("interaction.finish_reason").String(); got != "content_filter" {
		t.Fatalf("interaction.finish_reason = %q, want content_filter", got)
	}
}

func TestConsumeDevinFramesToInteractions_StopReasonContentFilter(t *testing.T) {
	// Frame with StopReason = 11 (CONTENT_FILTER)
	var f1 []byte
	f1 = protowire.AppendTag(f1, 3, protowire.BytesType)
	f1 = protowire.AppendString(f1, "blocked")
	f1 = protowire.AppendTag(f1, 5, protowire.VarintType)
	f1 = protowire.AppendVarint(f1, 11)

	var buf bytes.Buffer
	buf.Write(helps.WrapConnectEnvelope(f1))
	buf.Write(helps.WrapConnectEnvelopeWithFlag(helps.ConnectFlagEndStream, []byte(`{}`)))

	out, _, err := consumeDevinFramesToInteractions(&buf, "devin/swe-2", "swe-2-high")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	parsed := gjson.ParseBytes(out)
	if got := parsed.Get("status").String(); got != "incomplete" {
		t.Fatalf("status = %q, want incomplete. Output: %s", got, string(out))
	}
	if got := parsed.Get("finish_reason").String(); got != "content_filter" {
		t.Fatalf("finish_reason = %q, want content_filter. Output: %s", got, string(out))
	}
}

func TestStreamDevinFrames_LateThinkingSignaturesToClaudeStreaming(t *testing.T) {
	tests := []struct {
		name          string
		buildFrames   func() [][]byte
		wantSignature string
		wantText      string
		wantToolID    string
		wantToolName  string
		wantToolArgs  string
	}{
		{
			name: "Summary_Signature_Text",
			buildFrames: func() [][]byte {
				// Frame 1: thinking summary
				var f1 []byte
				f1 = protowire.AppendTag(f1, 9, protowire.BytesType)
				f1 = protowire.AppendString(f1, "thought text")

				// Frame 2: signature
				var f2 []byte
				f2 = protowire.AppendTag(f2, 10, protowire.BytesType)
				f2 = protowire.AppendString(f2, "CAQS-early-sig-19B")
				f2 = protowire.AppendTag(f2, 21, protowire.BytesType)
				f2 = protowire.AppendString(f2, "anthropic")

				// Frame 3: text content
				var f3 []byte
				f3 = protowire.AppendTag(f3, 3, protowire.BytesType)
				f3 = protowire.AppendString(f3, "answer text")

				return [][]byte{f1, f2, f3}
			},
			wantSignature: "CAQS-early-sig-19B",
			wantText:      "answer text",
		},
		{
			name: "Summary_Text_Signature_Same_Frame",
			buildFrames: func() [][]byte {
				// Frame 1: thinking summary
				var f1 []byte
				f1 = protowire.AppendTag(f1, 9, protowire.BytesType)
				f1 = protowire.AppendString(f1, "thought text")

				// Frame 2: text content + signature in same frame
				var f2 []byte
				f2 = protowire.AppendTag(f2, 3, protowire.BytesType)
				f2 = protowire.AppendString(f2, "answer text")
				f2 = protowire.AppendTag(f2, 10, protowire.BytesType)
				f2 = protowire.AppendString(f2, "CAQS-sameframe-sig-")
				f2 = protowire.AppendTag(f2, 21, protowire.BytesType)
				f2 = protowire.AppendString(f2, "anthropic")

				return [][]byte{f1, f2}
			},
			wantSignature: "CAQS-sameframe-sig-",
			wantText:      "answer text",
		},
		{
			name: "Summary_Text_Signature",
			buildFrames: func() [][]byte {
				// Frame 1: thinking summary
				var f1 []byte
				f1 = protowire.AppendTag(f1, 9, protowire.BytesType)
				f1 = protowire.AppendString(f1, "thought text")

				// Frame 2: text content
				var f2 []byte
				f2 = protowire.AppendTag(f2, 3, protowire.BytesType)
				f2 = protowire.AppendString(f2, "answer text")

				// Frame 3: late signature
				var f3 []byte
				f3 = protowire.AppendTag(f3, 10, protowire.BytesType)
				f3 = protowire.AppendString(f3, "CAQS-late-signature-19B")
				f3 = protowire.AppendTag(f3, 21, protowire.BytesType)
				f3 = protowire.AppendString(f3, "anthropic")

				return [][]byte{f1, f2, f3}
			},
			wantSignature: "CAQS-late-signature-19B",
			wantText:      "answer text",
		},
		{
			name: "Summary_ToolCall_Signature",
			buildFrames: func() [][]byte {
				// Frame 1: thinking summary
				var f1 []byte
				f1 = protowire.AppendTag(f1, 9, protowire.BytesType)
				f1 = protowire.AppendString(f1, "thinking about tools")

				// Frame 2: tool call delta
				var tc0 []byte
				tc0 = protowire.AppendTag(tc0, 1, protowire.BytesType)
				tc0 = protowire.AppendString(tc0, "call_1")
				tc0 = protowire.AppendTag(tc0, 2, protowire.BytesType)
				tc0 = protowire.AppendString(tc0, "bash")
				tc0 = protowire.AppendTag(tc0, 3, protowire.BytesType)
				tc0 = protowire.AppendString(tc0, `{"cmd":"ls"}`)
				tc0 = protowire.AppendTag(tc0, 4, protowire.VarintType)
				tc0 = protowire.AppendVarint(tc0, 0)

				var f2 []byte
				f2 = protowire.AppendTag(f2, 6, protowire.BytesType)
				f2 = protowire.AppendBytes(f2, tc0)

				// Frame 3: late signature
				var f3 []byte
				f3 = protowire.AppendTag(f3, 10, protowire.BytesType)
				f3 = protowire.AppendString(f3, "CAQS-tool-signature-19B")
				f3 = protowire.AppendTag(f3, 21, protowire.BytesType)
				f3 = protowire.AppendString(f3, "anthropic")

				return [][]byte{f1, f2, f3}
			},
			wantSignature: "CAQS-tool-signature-19B",
			wantToolID:    "call_1",
			wantToolName:  "bash",
			wantToolArgs:  `{"cmd":"ls"}`,
		},
		{
			name: "Summary_SplitSignature_Across_Text",
			buildFrames: func() [][]byte {
				// Frame 1: thinking summary
				var f1 []byte
				f1 = protowire.AppendTag(f1, 9, protowire.BytesType)
				f1 = protowire.AppendString(f1, "thought text")

				// Frame 2: signature fragment 1 + text content
				var f2 []byte
				f2 = protowire.AppendTag(f2, 10, protowire.BytesType)
				f2 = protowire.AppendString(f2, "CAQS-part1-")
				f2 = protowire.AppendTag(f2, 3, protowire.BytesType)
				f2 = protowire.AppendString(f2, "answer text")

				// Frame 3: signature fragment 2
				var f3 []byte
				f3 = protowire.AppendTag(f3, 10, protowire.BytesType)
				f3 = protowire.AppendString(f3, "part2-done")
				f3 = protowire.AppendTag(f3, 21, protowire.BytesType)
				f3 = protowire.AppendString(f3, "anthropic")

				return [][]byte{f1, f2, f3}
			},
			wantSignature: "CAQS-part1-part2-done",
			wantText:      "answer text",
		},
		{
			name: "Summary_SignatureFragment1_Text_SignatureFragment2_IndependentFrames",
			buildFrames: func() [][]byte {
				// Frame 1: thinking summary
				var f1 []byte
				f1 = protowire.AppendTag(f1, 9, protowire.BytesType)
				f1 = protowire.AppendString(f1, "thought text")

				// Frame 2: signature fragment 1
				var f2 []byte
				f2 = protowire.AppendTag(f2, 10, protowire.BytesType)
				f2 = protowire.AppendString(f2, "CAQS-seg1-")

				// Frame 3: independent text frame without signature
				var f3 []byte
				f3 = protowire.AppendTag(f3, 3, protowire.BytesType)
				f3 = protowire.AppendString(f3, "interleaving text")

				// Frame 4: signature fragment 2
				var f4 []byte
				f4 = protowire.AppendTag(f4, 10, protowire.BytesType)
				f4 = protowire.AppendString(f4, "seg2-done")
				f4 = protowire.AppendTag(f4, 21, protowire.BytesType)
				f4 = protowire.AppendString(f4, "anthropic")

				// Frame 5: subsequent text
				var f5 []byte
				f5 = protowire.AppendTag(f5, 3, protowire.BytesType)
				f5 = protowire.AppendString(f5, " final text")

				return [][]byte{f1, f2, f3, f4, f5}
			},
			wantSignature: "CAQS-seg1-seg2-done",
			wantText:      "interleaving text final text",
		},
		{
			name: "Summary_ManyTextFrames_LateSignature",
			buildFrames: func() [][]byte {
				// Frame 1: thinking summary
				var f1 []byte
				f1 = protowire.AppendTag(f1, 9, protowire.BytesType)
				f1 = protowire.AppendString(f1, "thought text")

				// 6 consecutive text frames with no signature
				var frames [][]byte
				frames = append(frames, f1)
				for i := 1; i <= 6; i++ {
					var ft []byte
					ft = protowire.AppendTag(ft, 3, protowire.BytesType)
					ft = protowire.AppendString(ft, fmt.Sprintf("text-%d ", i))
					frames = append(frames, ft)
				}

				// Late signature arriving on frame 8
				var fsig []byte
				fsig = protowire.AppendTag(fsig, 10, protowire.BytesType)
				fsig = protowire.AppendString(fsig, "CAQS-late-after-6-frames")
				fsig = protowire.AppendTag(fsig, 21, protowire.BytesType)
				fsig = protowire.AppendString(fsig, "anthropic")
				frames = append(frames, fsig)

				return frames
			},
			wantSignature: "CAQS-late-after-6-frames",
			wantText:      "text-1 text-2 text-3 text-4 text-5 text-6 ",
		},
		{
			name: "Summary_ToolCall_Signature_ToolCallContinuation",
			buildFrames: func() [][]byte {
				// Frame 1: thinking summary
				var f1 []byte
				f1 = protowire.AppendTag(f1, 9, protowire.BytesType)
				f1 = protowire.AppendString(f1, "planning tool call")

				// Frame 2: tool call 0 partial args
				var tc0 []byte
				tc0 = protowire.AppendTag(tc0, 1, protowire.BytesType)
				tc0 = protowire.AppendString(tc0, "call_1")
				tc0 = protowire.AppendTag(tc0, 2, protowire.BytesType)
				tc0 = protowire.AppendString(tc0, "bash")
				tc0 = protowire.AppendTag(tc0, 3, protowire.BytesType)
				tc0 = protowire.AppendString(tc0, `{"command": "git`)
				tc0 = protowire.AppendTag(tc0, 4, protowire.VarintType)
				tc0 = protowire.AppendVarint(tc0, 0)
				var f2 []byte
				f2 = protowire.AppendTag(f2, 6, protowire.BytesType)
				f2 = protowire.AppendBytes(f2, tc0)

				// Frame 3: signature arriving between tool deltas
				var f3 []byte
				f3 = protowire.AppendTag(f3, 10, protowire.BytesType)
				f3 = protowire.AppendString(f3, "CAQS-interleaved-tool-sig")
				f3 = protowire.AppendTag(f3, 21, protowire.BytesType)
				f3 = protowire.AppendString(f3, "anthropic")

				// Frame 4: tool call 0 continuation
				var tc0Cont []byte
				tc0Cont = protowire.AppendTag(tc0Cont, 3, protowire.BytesType)
				tc0Cont = protowire.AppendString(tc0Cont, ` status"}`)
				tc0Cont = protowire.AppendTag(tc0Cont, 4, protowire.VarintType)
				tc0Cont = protowire.AppendVarint(tc0Cont, 0)
				var f4 []byte
				f4 = protowire.AppendTag(f4, 6, protowire.BytesType)
				f4 = protowire.AppendBytes(f4, tc0Cont)

				return [][]byte{f1, f2, f3, f4}
			},
			wantSignature: "CAQS-interleaved-tool-sig",
			wantToolID:    "call_1",
			wantToolName:  "bash",
			wantToolArgs:  `{"command": "git status"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			frames := tt.buildFrames()
			var buf bytes.Buffer
			for _, f := range frames {
				buf.Write(helps.WrapConnectEnvelope(f))
			}
			buf.Write(helps.WrapConnectEnvelopeWithFlag(helps.ConnectFlagEndStream, []byte(`{}`)))

			e := &DevinExecutor{}
			out := make(chan cliproxyexecutor.StreamChunk, 50)
			opts := cliproxyexecutor.Options{
				SourceFormat:    sdktranslator.FormatClaude,
				OriginalRequest: []byte(`{"model":"devin/swe-2","stream":true,"messages":[{"role":"user","content":"hi"}]}`),
			}

			go func() {
				defer close(out)
				e.streamDevinFrames(
					context.Background(),
					&buf,
					cliproxyexecutor.Request{Model: "devin/swe-2", Payload: opts.OriginalRequest},
					opts,
					"swe-2-high",
					sdktranslator.FormatClaude,
					nil,
					out,
				)
			}()

			var accumulatedSig strings.Builder
			var accumulatedThinking strings.Builder
			var accumulatedText strings.Builder
			var gotToolID string
			var gotToolName string
			var gotToolArgs strings.Builder

			startedBlocks := make(map[int]string)
			stoppedBlocks := make(map[int]bool)
			activeBlock := -1

			for chunk := range out {
				if chunk.Err != nil {
					t.Fatalf("unexpected chunk error: %v", chunk.Err)
				}
				lines := strings.Split(string(chunk.Payload), "\n")
				for _, line := range lines {
					line = strings.TrimSpace(line)
					if strings.HasPrefix(line, "data: ") {
						data := strings.TrimPrefix(line, "data: ")
						data = strings.TrimSpace(data)
						if data == "" || data == "[DONE]" {
							continue
						}
						parsed := gjson.Parse(data)
						eventType := parsed.Get("type").String()
						switch eventType {
						case "content_block_start":
							idx := int(parsed.Get("index").Int())
							bType := parsed.Get("content_block.type").String()
							if _, exists := startedBlocks[idx]; exists {
								t.Errorf("content_block_start for index %d duplicated", idx)
							}
							startedBlocks[idx] = bType
							activeBlock = idx
							if bType == "tool_use" {
								gotToolID = parsed.Get("content_block.id").String()
								gotToolName = parsed.Get("content_block.name").String()
							}
						case "content_block_delta":
							idx := int(parsed.Get("index").Int())
							if idx != activeBlock {
								t.Errorf("delta index %d received while active block is %d", idx, activeBlock)
							}
							if stoppedBlocks[idx] {
								t.Errorf("delta index %d received after block was stopped", idx)
							}
							deltaType := parsed.Get("delta.type").String()
							switch deltaType {
							case "thinking_delta":
								accumulatedThinking.WriteString(parsed.Get("delta.thinking").String())
							case "signature_delta":
								sig := parsed.Get("delta.signature").String()
								accumulatedSig.WriteString(sig)
							case "text_delta":
								accumulatedText.WriteString(parsed.Get("delta.text").String())
							case "input_json_delta":
								gotToolArgs.WriteString(parsed.Get("delta.partial_json").String())
							}
						case "content_block_stop":
							idx := int(parsed.Get("index").Int())
							if idx != activeBlock {
								t.Errorf("content_block_stop index %d does not match active block %d", idx, activeBlock)
							}
							if stoppedBlocks[idx] {
								t.Errorf("content_block_stop index %d duplicated", idx)
							}
							stoppedBlocks[idx] = true
							activeBlock = -1
						}
					}
				}
			}

			// Verify thinking
			if accumulatedThinking.Len() == 0 {
				t.Errorf("accumulated thinking is empty")
			}

			// Verify signature
			if got := accumulatedSig.String(); got != tt.wantSignature {
				t.Errorf("accumulated signature = %q, want %q", got, tt.wantSignature)
			}

			// Verify text if expected
			if tt.wantText != "" {
				if got := accumulatedText.String(); got != tt.wantText {
					t.Errorf("accumulated text = %q, want %q", got, tt.wantText)
				}
			}

			// Verify tool call if expected
			if tt.wantToolID != "" {
				if gotToolID != tt.wantToolID {
					t.Errorf("tool ID = %q, want %q", gotToolID, tt.wantToolID)
				}
				if gotToolName != tt.wantToolName {
					t.Errorf("tool name = %q, want %q", gotToolName, tt.wantToolName)
				}
				if got := gotToolArgs.String(); got != tt.wantToolArgs {
					t.Errorf("tool args = %q, want %q", got, tt.wantToolArgs)
				}
			}

			// Verify block closure integrity: every started block must be stopped
			for idx, bType := range startedBlocks {
				if !stoppedBlocks[idx] {
					t.Errorf("block %d (type: %s) was started but never stopped", idx, bType)
				}
			}
		})
	}
}

func TestStreamDevinFrames_LateThinkingSignatures_TrailerErrorClosesBlocks(t *testing.T) {
	tests := []struct {
		name        string
		buildFrames func() [][]byte
		wantBlocks  map[int]string // expected started block types
	}{
		{
			name: "TrailerError_WithBufferedText",
			buildFrames: func() [][]byte {
				// Frame 1: thinking summary
				var f1 []byte
				f1 = protowire.AppendTag(f1, 9, protowire.BytesType)
				f1 = protowire.AppendString(f1, "thought text")

				// Frame 2: text content
				var f2 []byte
				f2 = protowire.AppendTag(f2, 3, protowire.BytesType)
				f2 = protowire.AppendString(f2, "answer text")

				return [][]byte{f1, f2}
			},
			wantBlocks: map[int]string{
				0: "thinking",
				1: "text",
			},
		},
		{
			name: "TrailerError_WithBufferedToolCall",
			buildFrames: func() [][]byte {
				// Frame 1: thinking summary
				var f1 []byte
				f1 = protowire.AppendTag(f1, 9, protowire.BytesType)
				f1 = protowire.AppendString(f1, "planning tool call")

				// Frame 2: tool call delta
				var tc0 []byte
				tc0 = protowire.AppendTag(tc0, 1, protowire.BytesType)
				tc0 = protowire.AppendString(tc0, "call_err")
				tc0 = protowire.AppendTag(tc0, 2, protowire.BytesType)
				tc0 = protowire.AppendString(tc0, "bash")
				tc0 = protowire.AppendTag(tc0, 3, protowire.BytesType)
				tc0 = protowire.AppendString(tc0, `{"cmd":"err"}`)
				tc0 = protowire.AppendTag(tc0, 4, protowire.VarintType)
				tc0 = protowire.AppendVarint(tc0, 0)

				var f2 []byte
				f2 = protowire.AppendTag(f2, 6, protowire.BytesType)
				f2 = protowire.AppendBytes(f2, tc0)

				return [][]byte{f1, f2}
			},
			wantBlocks: map[int]string{
				0: "thinking",
				1: "tool_use",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			frames := tt.buildFrames()
			var buf bytes.Buffer
			for _, f := range frames {
				buf.Write(helps.WrapConnectEnvelope(f))
			}
			// Connect trailer with error code 14 (UNAVAILABLE)
			buf.Write(helps.WrapConnectEnvelopeWithFlag(helps.ConnectFlagEndStream, []byte(`{"error":{"code":"unavailable","message":"server overload"}}`)))

			e := &DevinExecutor{}
			out := make(chan cliproxyexecutor.StreamChunk, 50)
			opts := cliproxyexecutor.Options{
				SourceFormat:    sdktranslator.FormatClaude,
				OriginalRequest: []byte(`{"model":"devin/swe-2","stream":true,"messages":[{"role":"user","content":"hi"}]}`),
			}

			go func() {
				defer close(out)
				e.streamDevinFrames(
					context.Background(),
					&buf,
					cliproxyexecutor.Request{Model: "devin/swe-2", Payload: opts.OriginalRequest},
					opts,
					"swe-2-high",
					sdktranslator.FormatClaude,
					nil,
					out,
				)
			}()

			var sawChunkErr bool
			startedBlocks := make(map[int]string)
			stoppedBlocks := make(map[int]bool)

			for chunk := range out {
				if chunk.Err != nil {
					// Verify that all started blocks were already stopped before the error chunk
					for idx, bType := range startedBlocks {
						if !stoppedBlocks[idx] {
							t.Errorf("block %d (%s) was not stopped before stream error", idx, bType)
						}
					}
					sawChunkErr = true
					continue
				}
				lines := strings.Split(string(chunk.Payload), "\n")
				for _, line := range lines {
					line = strings.TrimSpace(line)
					if strings.HasPrefix(line, "data: ") {
						data := strings.TrimPrefix(line, "data: ")
						data = strings.TrimSpace(data)
						if data == "" || data == "[DONE]" {
							continue
						}
						parsed := gjson.Parse(data)
						eventType := parsed.Get("type").String()
						if eventType == "content_block_start" {
							idx := int(parsed.Get("index").Int())
							startedBlocks[idx] = parsed.Get("content_block.type").String()
						} else if eventType == "content_block_stop" {
							idx := int(parsed.Get("index").Int())
							stoppedBlocks[idx] = true
						}
					}
				}
			}

			if !sawChunkErr {
				t.Errorf("expected chunk error from trailer error, got none")
			}

			// Verify all expected blocks were started
			for idx, wantType := range tt.wantBlocks {
				if gotType := startedBlocks[idx]; gotType != wantType {
					t.Errorf("block %d type = %q, want %q", idx, gotType, wantType)
				}
			}

			// Verify that every block that was started was properly stopped
			for idx, bType := range startedBlocks {
				if !stoppedBlocks[idx] {
					t.Errorf("block %d (type: %s) was started but NOT closed with content_block_stop before error exit", idx, bType)
				}
			}
		})
	}
}

func TestDevinExecutor_ResponsesNamespaceToolsFlattenedInUpstreamRequest(t *testing.T) {
	responsesPayload := []byte(`{
		"model": "devin/gemini-3-7-flash",
		"tools": [
			{
				"type": "function",
				"name": "exec_command",
				"description": "Execute a command",
				"parameters": {"type": "object", "properties": {"cmd": {"type": "string"}}}
			},
			{
				"type": "namespace",
				"name": "multi_agent_v1",
				"description": "Multi agent tools",
				"tools": [
					{
						"type": "function",
						"name": "close_agent",
						"description": "Close an agent",
						"parameters": {"type": "object", "properties": {"target": {"type": "string"}}}
					},
					{
						"type": "function",
						"name": "resume_agent",
						"description": "Resume an agent",
						"parameters": {"type": "object", "properties": {"id": {"type": "string"}}}
					}
				]
			}
		],
		"input": [
			{"type": "message", "role": "user", "content": "hello"}
		]
	}`)

	interactionsJSON := sdktranslator.TranslateRequest(sdktranslator.FormatOpenAIResponse, sdktranslator.FormatInteractions, "devin/gemini-3-7-flash", responsesPayload, false)
	systemPrompt, prompts, tools, temp, maxTokens, sessionID, cascadeID, _, _ := parseInteractionsPayload(interactionsJSON, responsesPayload)

	if len(tools) != 3 {
		t.Fatalf("expected 3 tools, got %d", len(tools))
	}

	for i, tool := range tools {
		if tool.Name == "" {
			t.Fatalf("tool %d has empty Name", i)
		}
	}

	logBody := helps.BuildDevinUpstreamLogBody(
		interactionsJSON,
		false,
		"gemini-3-7-flash",
		systemPrompt,
		prompts,
		tools,
		temp,
		maxTokens,
		sessionID,
		cascadeID,
	)

	logBodyStr := string(logBody)
	if strings.Contains(logBodyStr, `"name": ""`) {
		t.Fatalf("devin upstream log body contains empty tool name: %s", logBodyStr)
	}
}

func TestDevinExecutor_ResponsesToolsFilterAndObfuscate(t *testing.T) {
	responsesPayload := []byte(`{
		"model": "devin/swe-2",
		"tools": [
			{
				"type": "namespace",
				"name": "mcp__codex_app",
				"description": "Codex App tools",
				"tools": [
					{
						"type": "function",
						"name": "automation_update",
						"description": "Recurring automations",
						"parameters": {"type": "object", "properties": {"id": {"type": "string"}}}
					},
					{
						"type": "function",
						"name": "read_resource",
						"description": "Read a resource",
						"parameters": {"type": "object", "properties": {"uri": {"type": "string"}}}
					}
				]
			},
			{
				"type": "function",
				"name": "exec_command",
				"description": "Runs a command in a bash shell, returning output or a session ID for ongoing interaction.",
				"parameters": {
					"type": "object",
					"properties": {"cmd": {"type": "string"}},
					"required": ["cmd"]
				}
			},
			{
				"type": "function",
				"name": "write_stdin",
				"description": "Writes characters to an existing unified exec session and returns recent output.",
				"parameters": {
					"type": "object",
					"properties": {"session_id": {"type": "string"}},
					"required": ["session_id"]
				}
			}
		],
		"input": [
			{"type": "message", "role": "user", "content": "hello"}
		]
	}`)

	interactionsJSON := sdktranslator.TranslateRequest(sdktranslator.FormatOpenAIResponse, sdktranslator.FormatInteractions, "devin/swe-2", responsesPayload, false)
	systemPrompt, prompts, tools, temp, maxTokens, sessionID, cascadeID, _, _ := parseInteractionsPayload(interactionsJSON, responsesPayload)

	// 1. automation_update must be filtered out, leaving read_resource, exec_command, write_stdin
	if len(tools) != 3 {
		t.Fatalf("expected 3 tools, got %d", len(tools))
	}

	for _, tool := range tools {
		if strings.Contains(tool.Name, "automation_update") {
			t.Fatalf("unexpected automation_update tool in Devin tools: %s", tool.Name)
		}
		if tool.Name == "exec_command" {
			want := "Runs a command in a bash shell, returning output or an session ID for ongoing interaction."
			if tool.Description != want {
				t.Fatalf("exec_command description = %q, want %q", tool.Description, want)
			}
		}
		if tool.Name == "write_stdin" {
			want := "Writes characters to a existing unified exec session and returns recent output."
			if tool.Description != want {
				t.Fatalf("write_stdin description = %q, want %q", tool.Description, want)
			}
		}
	}

	logBody := helps.BuildDevinUpstreamLogBody(
		interactionsJSON,
		false,
		"swe-2",
		systemPrompt,
		prompts,
		tools,
		temp,
		maxTokens,
		sessionID,
		cascadeID,
	)

	logBodyStr := string(logBody)
	if strings.Contains(logBodyStr, "automation_update") {
		t.Fatalf("upstream log body should not contain automation_update: %s", logBodyStr)
	}
	if !strings.Contains(logBodyStr, "an session ID") {
		t.Fatalf("upstream log body should contain 'an session ID': %s", logBodyStr)
	}
	if !strings.Contains(logBodyStr, "to a existing") {
		t.Fatalf("upstream log body should contain 'to a existing': %s", logBodyStr)
	}
}

func TestDevinExecutor_ToolResultImagesInInteractionsPayload(t *testing.T) {
	interactionsJSON := []byte(`{
		"model": "devin/swe-2",
		"input": [
			{
				"type": "function_call",
				"id": "tool_img_1",
				"name": "screenshot"
			},
			{
				"type": "function_result",
				"call_id": "tool_img_1",
				"result": [
					{
						"type": "text",
						"text": "Screenshot captured"
					},
					{
						"type": "image",
						"mime_type": "image/png",
						"data": "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg=="
					}
				]
			}
		]
	}`)

	_, prompts, _, _, _, _, _, _, _ := parseInteractionsPayload(interactionsJSON, nil)
	var toolPrompt *helps.DevinPrompt
	for i := range prompts {
		if prompts[i].Source == 4 && prompts[i].ToolCallID == "tool_img_1" {
			toolPrompt = &prompts[i]
			break
		}
	}

	if toolPrompt == nil {
		t.Fatalf("expected tool_result prompt with ToolCallID tool_img_1")
	}
	if len(toolPrompt.Images) != 1 {
		t.Fatalf("expected 1 image in toolPrompt.Images, got %d", len(toolPrompt.Images))
	}
	if toolPrompt.Images[0].MimeType != "image/png" {
		t.Errorf("expected mime_type image/png, got %s", toolPrompt.Images[0].MimeType)
	}
	if toolPrompt.Images[0].Base64Data != "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg==" {
		t.Errorf("unexpected base64 data: %s", toolPrompt.Images[0].Base64Data)
	}
	if !strings.Contains(toolPrompt.Content, "[Image 1: pasted_image_1.png]") {
		t.Errorf("expected content to contain image header, got: %s", toolPrompt.Content)
	}
}

func TestDevinExecutor_SupplementImagesFromOriginalToolResult(t *testing.T) {
	origRequest := []byte(`{
		"messages": [
			{
				"role": "assistant",
				"content": [
					{"type": "tool_use", "id": "tool_img_1", "name": "screenshot", "input": {}}
				]
			},
			{
				"role": "user",
				"content": [
					{
						"type": "tool_result",
						"tool_use_id": "tool_img_1",
						"content": [
							{
								"type": "image",
								"source": {
									"type": "base64",
									"media_type": "image/png",
									"data": "original-tool-result-base64"
								}
							}
						]
					}
				]
			}
		]
	}`)

	prompts := []helps.DevinPrompt{
		{
			Source:     4,
			ToolCallID: "tool_img_1",
			Content:    "",
		},
	}

	supplementImagesFromOriginal(origRequest, prompts)

	if len(prompts[0].Images) != 1 {
		t.Fatalf("expected 1 image supplemented to tool_result prompt, got %d", len(prompts[0].Images))
	}
	if prompts[0].Images[0].Base64Data != "original-tool-result-base64" {
		t.Errorf("expected base64 'original-tool-result-base64', got %s", prompts[0].Images[0].Base64Data)
	}
	if prompts[0].Images[0].MimeType != "image/png" {
		t.Errorf("expected mime_type 'image/png', got %s", prompts[0].Images[0].MimeType)
	}
	if !strings.Contains(prompts[0].Content, "[Image 1: pasted_image_1.png]") {
		t.Errorf("expected content to contain image header, got: %s", prompts[0].Content)
	}
}

func TestDevinExecutor_SupplementImagesFromOriginalMultipleToolsNoCrossPollution(t *testing.T) {
	origRequest := []byte(`{
		"messages": [
			{
				"role": "assistant",
				"content": [
					{"type": "tool_use", "id": "call_text_only", "name": "bash", "input": {"cmd": "ls"}},
					{"type": "tool_use", "id": "call_with_image", "name": "screenshot", "input": {}}
				]
			},
			{
				"role": "user",
				"content": [
					{
						"type": "tool_result",
						"tool_use_id": "call_text_only",
						"content": "file1.txt\nfile2.txt"
					},
					{
						"type": "tool_result",
						"tool_use_id": "call_with_image",
						"content": [
							{
								"type": "image",
								"source": {
									"type": "base64",
									"media_type": "image/png",
									"data": "screenshot-base64-data"
								}
							}
						]
					}
				]
			}
		]
	}`)

	prompts := []helps.DevinPrompt{
		{
			Source:     4,
			ToolCallID: "call_text_only",
			Content:    "file1.txt\nfile2.txt",
		},
		{
			Source:     4,
			ToolCallID: "call_with_image",
			Content:    "",
		},
	}

	supplementImagesFromOriginal(origRequest, prompts)

	// Tool A (text-only) must NOT receive any images
	if len(prompts[0].Images) != 0 {
		t.Fatalf("expected 0 images on text-only tool prompt, got %d", len(prompts[0].Images))
	}
	if strings.Contains(prompts[0].Content, "[Image ") {
		t.Errorf("text-only tool prompt should not have image header: %s", prompts[0].Content)
	}

	// Tool B (screenshot) MUST receive its image
	if len(prompts[1].Images) != 1 {
		t.Fatalf("expected 1 image on screenshot tool prompt, got %d", len(prompts[1].Images))
	}
	if prompts[1].Images[0].Base64Data != "screenshot-base64-data" {
		t.Errorf("screenshot tool image data mismatch: %s", prompts[1].Images[0].Base64Data)
	}
	if !strings.Contains(prompts[1].Content, "[Image 1: pasted_image_1.png]") {
		t.Errorf("screenshot tool prompt should contain image header: %s", prompts[1].Content)
	}
}

func TestDevinExecutor_ClaudeToolResultEndToEnd(t *testing.T) {
	claudeReq := []byte(`{
		"model": "devin/swe-2",
		"messages": [
			{
				"role": "user",
				"content": "Inspect the screenshot returned by the tool."
			},
			{
				"role": "assistant",
				"content": [
					{"type": "tool_use", "id": "tool_text_1", "name": "bash", "input": {"cmd": "pwd"}},
					{"type": "tool_use", "id": "tool_image_1", "name": "screenshot", "input": {}}
				]
			},
			{
				"role": "user",
				"content": [
					{
						"type": "tool_result",
						"tool_use_id": "tool_text_1",
						"content": "/workspace"
					},
					{
						"type": "tool_result",
						"tool_use_id": "tool_image_1",
						"content": [
							{
								"type": "text",
								"text": "Captured window"
							},
							{
								"type": "image",
								"source": {
									"type": "base64",
									"media_type": "image/png",
									"data": "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg=="
								}
							}
						]
					}
				]
			}
		]
	}`)

	// Real conversion from Claude request to Interactions format
	interactionsJSON := interactionsclaude.ConvertClaudeRequestToInteractions("devin/swe-2", claudeReq, true)

	systemPrompt, prompts, tools, temp, maxTokens, sessionID, cascadeID, _, _ := parseInteractionsPayload(interactionsJSON, claudeReq)

	var textToolPrompt *helps.DevinPrompt
	var imageToolPrompt *helps.DevinPrompt
	for i := range prompts {
		if prompts[i].Source == 4 {
			if prompts[i].ToolCallID == "tool_text_1" {
				textToolPrompt = &prompts[i]
			} else if prompts[i].ToolCallID == "tool_image_1" {
				imageToolPrompt = &prompts[i]
			}
		}
	}

	if textToolPrompt == nil {
		t.Fatalf("missing text tool prompt for tool_text_1")
	}
	if len(textToolPrompt.Images) != 0 {
		t.Fatalf("text tool prompt should have 0 images, got %d", len(textToolPrompt.Images))
	}

	if imageToolPrompt == nil {
		t.Fatalf("missing image tool prompt for tool_image_1")
	}
	if len(imageToolPrompt.Images) != 1 {
		t.Fatalf("expected 1 image in imageToolPrompt, got %d", len(imageToolPrompt.Images))
	}
	if imageToolPrompt.Images[0].Base64Data != "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg==" {
		t.Errorf("image data mismatch: %s", imageToolPrompt.Images[0].Base64Data)
	}
	if !strings.Contains(imageToolPrompt.Content, "[Image 1: pasted_image_1.png]") {
		t.Errorf("expected content to contain image header, got: %s", imageToolPrompt.Content)
	}

	// Verify upstream wire encoding
	wireReq := helps.BuildDevinGetChatMessageRequest(
		"test-token",
		"test-seed",
		"swe-2-medium",
		systemPrompt,
		prompts,
		tools,
		temp,
		maxTokens,
		sessionID,
		cascadeID,
		nil,
	)
	if len(wireReq) == 0 {
		t.Fatalf("expected non-empty wire request")
	}

	// Verify wire-level decoded prompts and per-tool image associations
	decoded := decodeWirePrompts(t, wireReq)
	var wireTextToolPrompt *decodedWirePrompt
	var wireImageToolPrompt *decodedWirePrompt
	for i := range decoded {
		if decoded[i].source == 4 {
			if decoded[i].toolCallID == "tool_text_1" {
				wireTextToolPrompt = &decoded[i]
			} else if decoded[i].toolCallID == "tool_image_1" {
				wireImageToolPrompt = &decoded[i]
			}
		}
	}
	if wireTextToolPrompt == nil {
		t.Fatalf("missing tool_text_1 prompt in decoded wire request")
	}
	if len(wireTextToolPrompt.images) != 0 {
		t.Fatalf("wire tool_text_1 prompt should have 0 images, got %d", len(wireTextToolPrompt.images))
	}
	if wireImageToolPrompt == nil {
		t.Fatalf("missing tool_image_1 prompt in decoded wire request")
	}
	if len(wireImageToolPrompt.images) != 1 {
		t.Fatalf("wire tool_image_1 prompt should have 1 image, got %d", len(wireImageToolPrompt.images))
	}
	if wireImageToolPrompt.images[0].Base64Data != "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg==" {
		t.Errorf("wire image data mismatch: %s", wireImageToolPrompt.images[0].Base64Data)
	}
	if wireImageToolPrompt.images[0].MimeType != "image/png" {
		t.Errorf("wire image mime mismatch: %s", wireImageToolPrompt.images[0].MimeType)
	}

	// Verify upstream log body
	logBody := helps.BuildDevinUpstreamLogBody(
		interactionsJSON,
		true,
		"swe-2-medium",
		systemPrompt,
		prompts,
		tools,
		temp,
		maxTokens,
		sessionID,
		cascadeID,
	)
	logBodyStr := string(logBody)
	if !strings.Contains(logBodyStr, `"mime_type": "image/png"`) || !strings.Contains(logBodyStr, `"data_len": 96`) {
		t.Fatalf("expected log body to contain image log entry with data_len, got: %s", logBodyStr)
	}
}

type decodedWirePrompt struct {
	source     int
	content    string
	toolCallID string
	images     []helps.DevinImage
}

func decodeWirePrompts(t *testing.T, reqBytes []byte) []decodedWirePrompt {
	t.Helper()
	var decoded []decodedWirePrompt
	b := reqBytes
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			break
		}
		b = b[n:]
		if num == 3 && typ == protowire.BytesType {
			promptBytes, m := protowire.ConsumeBytes(b)
			if m < 0 {
				t.Fatalf("failed to consume prompt bytes")
			}
			b = b[m:]

			var p decodedWirePrompt
			pb := promptBytes
			for len(pb) > 0 {
				pnum, ptyp, pn := protowire.ConsumeTag(pb)
				if pn < 0 {
					break
				}
				pb = pb[pn:]
				switch {
				case pnum == 2 && ptyp == protowire.VarintType:
					val, vm := protowire.ConsumeVarint(pb)
					if vm < 0 {
						break
					}
					p.source = int(val)
					pb = pb[vm:]
				case pnum == 3 && ptyp == protowire.BytesType:
					val, vm := protowire.ConsumeString(pb)
					if vm < 0 {
						break
					}
					p.content = val
					pb = pb[vm:]
				case pnum == 7 && ptyp == protowire.BytesType:
					val, vm := protowire.ConsumeString(pb)
					if vm < 0 {
						break
					}
					p.toolCallID = val
					pb = pb[vm:]
				case pnum == 10 && ptyp == protowire.BytesType:
					imgBytes, vm := protowire.ConsumeBytes(pb)
					if vm < 0 {
						break
					}
					pb = pb[vm:]
					var img helps.DevinImage
					ib := imgBytes
					for len(ib) > 0 {
						inum, ityp, in := protowire.ConsumeTag(ib)
						if in < 0 {
							break
						}
						ib = ib[in:]
						if inum == 1 && ityp == protowire.BytesType {
							s, im := protowire.ConsumeString(ib)
							if im < 0 {
								break
							}
							img.Base64Data = s
							ib = ib[im:]
						} else if inum == 2 && ityp == protowire.BytesType {
							s, im := protowire.ConsumeString(ib)
							if im < 0 {
								break
							}
							img.MimeType = s
							ib = ib[im:]
						} else {
							im := protowire.ConsumeFieldValue(inum, ityp, ib)
							if im < 0 {
								break
							}
							ib = ib[im:]
						}
					}
					p.images = append(p.images, img)
				default:
					vm := protowire.ConsumeFieldValue(pnum, ptyp, pb)
					if vm < 0 {
						break
					}
					pb = pb[vm:]
				}
			}
			decoded = append(decoded, p)
		} else {
			m := protowire.ConsumeFieldValue(num, typ, b)
			if m < 0 {
				break
			}
			b = b[m:]
		}
	}
	return decoded
}

func TestDevinExecutor_FunctionResultMixedStructuredAndBusinessJSON(t *testing.T) {
	interactionsJSON := []byte(`{
		"model": "devin/swe-2",
		"input": [
			{
				"type": "function_result",
				"call_id": "call_mixed",
				"result": [
					{"type": "text", "text": "log output"},
					{"exit_code": 0, "status": "ok"}
				]
			}
		]
	}`)

	_, prompts, _, _, _, _, _, _, _ := parseInteractionsPayload(interactionsJSON, nil)
	if len(prompts) != 1 {
		t.Fatalf("expected 1 prompt, got %d", len(prompts))
	}
	if !strings.Contains(prompts[0].Content, "log output") {
		t.Errorf("content missing text part: %s", prompts[0].Content)
	}
	if !strings.Contains(prompts[0].Content, `{"exit_code": 0, "status": "ok"}`) {
		t.Errorf("content missing business JSON part: %s", prompts[0].Content)
	}
}

func TestDevinExecutor_FunctionResultBusinessObjectWithTextFieldPreserved(t *testing.T) {
	// Case 1: Array contains only a business object with a "text" field, should preserve full JSON
	payloadOnlyBusiness := []byte(`{
		"model": "devin/swe-2",
		"input": [
			{
				"type": "function_result",
				"call_id": "call_err",
				"result": [
					{"text": "failed", "exit_code": 1, "retryable": true}
				]
			}
		]
	}`)

	_, prompts1, _, _, _, _, _, _, _ := parseInteractionsPayload(payloadOnlyBusiness, nil)
	if len(prompts1) != 1 {
		t.Fatalf("expected 1 prompt, got %d", len(prompts1))
	}
	if !strings.Contains(prompts1[0].Content, `"exit_code": 1`) || !strings.Contains(prompts1[0].Content, `"retryable": true`) {
		t.Errorf("expected business object fields to be preserved, got: %s", prompts1[0].Content)
	}

	// Case 2: Array contains image and business object with a "text" field
	payloadWithImage := []byte(`{
		"model": "devin/swe-2",
		"input": [
			{
				"type": "function_result",
				"call_id": "call_img_err",
				"result": [
					{
						"type": "image",
						"mime_type": "image/png",
						"data": "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg=="
					},
					{"text": "failed", "exit_code": 1, "retryable": true}
				]
			}
		]
	}`)

	_, prompts2, _, _, _, _, _, _, _ := parseInteractionsPayload(payloadWithImage, nil)
	if len(prompts2) != 1 {
		t.Fatalf("expected 1 prompt, got %d", len(prompts2))
	}
	if len(prompts2[0].Images) != 1 {
		t.Fatalf("expected 1 image in prompt, got %d", len(prompts2[0].Images))
	}
	if !strings.Contains(prompts2[0].Content, "[Image 1: pasted_image_1.png]") {
		t.Errorf("expected image header, got: %s", prompts2[0].Content)
	}
	if !strings.Contains(prompts2[0].Content, `"exit_code": 1`) || !strings.Contains(prompts2[0].Content, `"retryable": true`) {
		t.Errorf("expected business object fields to be preserved in mixed array, got: %s", prompts2[0].Content)
	}
}

func TestDevinExecutor_ClaudeToolResultMixedBusinessJSONEndToEnd(t *testing.T) {
	claudeReq := []byte(`{
		"model": "devin/swe-2",
		"messages": [
			{
				"role": "assistant",
				"content": [
					{"type": "tool_use", "id": "tool_mixed_1", "name": "run_test", "input": {}}
				]
			},
			{
				"role": "user",
				"content": [
					{
						"type": "tool_result",
						"tool_use_id": "tool_mixed_1",
						"content": [
							{"type": "text", "text": "Test suite finished"},
							{
								"type": "image",
								"source": {
									"type": "base64",
									"media_type": "image/png",
									"data": "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg=="
								}
							},
							{"text": "failed", "exit_code": 1, "retryable": true}
						]
					}
				]
			}
		]
	}`)

	interactionsJSON := interactionsclaude.ConvertClaudeRequestToInteractions("devin/swe-2", claudeReq, false)
	_, prompts, _, _, _, _, _, _, _ := parseInteractionsPayload(interactionsJSON, claudeReq)

	if len(prompts) != 2 {
		t.Fatalf("expected 2 prompts (assistant call + tool result), got %d", len(prompts))
	}
	toolPrompt := prompts[1]
	if toolPrompt.Source != 4 || toolPrompt.ToolCallID != "tool_mixed_1" {
		t.Fatalf("expected tool prompt with ToolCallID tool_mixed_1, got source %d, id %s", toolPrompt.Source, toolPrompt.ToolCallID)
	}
	if len(toolPrompt.Images) != 1 {
		t.Fatalf("expected 1 image in toolPrompt, got %d", len(toolPrompt.Images))
	}
	if !strings.Contains(toolPrompt.Content, "[Image 1: pasted_image_1.png]") {
		t.Errorf("content missing image header: %s", toolPrompt.Content)
	}
	if !strings.Contains(toolPrompt.Content, "Test suite finished") {
		t.Errorf("content missing text part: %s", toolPrompt.Content)
	}
	if !strings.Contains(toolPrompt.Content, `"exit_code": 1`) || !strings.Contains(toolPrompt.Content, `"retryable": true`) {
		t.Errorf("content missing business object fields: %s", toolPrompt.Content)
	}
}

func TestDevinExecutor_SupplementImagesEdgeCases(t *testing.T) {
	t.Run("mismatched_tool_id_no_images_attached", func(t *testing.T) {
		origRequest := []byte(`{
			"messages": [
				{
					"role": "user",
					"content": [
						{
							"type": "tool_result",
							"tool_use_id": "call_expected_1",
							"content": [
								{
									"type": "image",
									"source": {"type": "base64", "media_type": "image/png", "data": "img-data"}
								}
							]
						}
					]
				}
			]
		}`)

		prompts := []helps.DevinPrompt{
			{
				Source:     4,
				ToolCallID: "call_different_2",
				Content:    "some result",
			},
		}

		supplementImagesFromOriginal(origRequest, prompts)
		if len(prompts[0].Images) != 0 {
			t.Fatalf("expected 0 images for mismatched tool id, got %d", len(prompts[0].Images))
		}
	})

	t.Run("multiple_images_in_single_tool_result", func(t *testing.T) {
		origRequest := []byte(`{
			"messages": [
				{
					"role": "user",
					"content": [
						{
							"type": "tool_result",
							"tool_use_id": "call_multi_img",
							"content": [
								{
									"type": "image",
									"source": {"type": "base64", "media_type": "image/png", "data": "img-1"}
								},
								{
									"type": "image",
									"source": {"type": "base64", "media_type": "image/jpeg", "data": "img-2"}
								}
							]
						}
					]
				}
			]
		}`)

		prompts := []helps.DevinPrompt{
			{
				Source:     4,
				ToolCallID: "call_multi_img",
				Content:    "",
			},
		}

		supplementImagesFromOriginal(origRequest, prompts)
		if len(prompts[0].Images) != 2 {
			t.Fatalf("expected 2 images, got %d", len(prompts[0].Images))
		}
		if !strings.Contains(prompts[0].Content, "[Image 1: pasted_image_1.png]") {
			t.Errorf("missing header for image 1: %s", prompts[0].Content)
		}
		if !strings.Contains(prompts[0].Content, "[Image 2: pasted_image_2.jpg]") {
			t.Errorf("missing header for image 2: %s", prompts[0].Content)
		}
	})
}

func TestRegressionIssue5910_ToolCallAggregationByCallID(t *testing.T) {
	// Frame 1: call_1 start + partial args
	var tc1 []byte
	tc1 = protowire.AppendTag(tc1, 1, protowire.BytesType)
	tc1 = protowire.AppendString(tc1, "call_1")
	tc1 = protowire.AppendTag(tc1, 2, protowire.BytesType)
	tc1 = protowire.AppendString(tc1, "tool_1")
	tc1 = protowire.AppendTag(tc1, 3, protowire.BytesType)
	tc1 = protowire.AppendString(tc1, `{"a":`)

	var f1 []byte
	f1 = protowire.AppendTag(f1, 6, protowire.BytesType)
	f1 = protowire.AppendBytes(f1, tc1)

	// Frame 2: call_2 start + args
	var tc2 []byte
	tc2 = protowire.AppendTag(tc2, 1, protowire.BytesType)
	tc2 = protowire.AppendString(tc2, "call_2")
	tc2 = protowire.AppendTag(tc2, 2, protowire.BytesType)
	tc2 = protowire.AppendString(tc2, "tool_2")
	tc2 = protowire.AppendTag(tc2, 3, protowire.BytesType)
	tc2 = protowire.AppendString(tc2, `{"b":2}`)

	var f2 []byte
	f2 = protowire.AppendTag(f2, 6, protowire.BytesType)
	f2 = protowire.AppendBytes(f2, tc2)

	// Frame 3: call_1 continuation args
	var tc1Cont []byte
	tc1Cont = protowire.AppendTag(tc1Cont, 1, protowire.BytesType)
	tc1Cont = protowire.AppendString(tc1Cont, "call_1")
	tc1Cont = protowire.AppendTag(tc1Cont, 3, protowire.BytesType)
	tc1Cont = protowire.AppendString(tc1Cont, `1}`)

	var f3 []byte
	f3 = protowire.AppendTag(f3, 6, protowire.BytesType)
	f3 = protowire.AppendBytes(f3, tc1Cont)

	var buf bytes.Buffer
	buf.Write(helps.WrapConnectEnvelope(f1))
	buf.Write(helps.WrapConnectEnvelope(f2))
	buf.Write(helps.WrapConnectEnvelope(f3))
	buf.Write(helps.WrapConnectEnvelopeWithFlag(helps.ConnectFlagEndStream, []byte(`{}`)))

	_, respLog, err := consumeDevinFramesToInteractions(&buf, "devin/swe-2", "chat-model-uid")
	if err != nil {
		t.Fatalf("consumeDevinFramesToInteractions failed: %v", err)
	}

	if len(respLog.ToolCalls) != 2 {
		t.Fatalf("expected 2 tool calls, got %d", len(respLog.ToolCalls))
	}
	if respLog.ToolCalls[0].ID != "call_1" || respLog.ToolCalls[0].Arguments != `{"a":1}` {
		t.Errorf("tool call 0 = %+v, want call_1 with args {\"a\":1}", respLog.ToolCalls[0])
	}
	if respLog.ToolCalls[1].ID != "call_2" || respLog.ToolCalls[1].Arguments != `{"b":2}` {
		t.Errorf("tool call 1 = %+v, want call_2 with args {\"b\":2}", respLog.ToolCalls[1])
	}
}

func TestRegressionIssue5910_CustomToolCallInvalidJSONStr(t *testing.T) {
	// Upstream Devin sends raw arguments for custom tool calls in field 4 (invalid_json_str),
	// parse error in field 5 (invalid_json_err), and custom flag in field 6 (is_custom_tool_call).
	var tc []byte
	tc = protowire.AppendTag(tc, 1, protowire.BytesType)
	tc = protowire.AppendString(tc, "call_custom_1")
	tc = protowire.AppendTag(tc, 2, protowire.BytesType)
	tc = protowire.AppendString(tc, "bash")
	// Field 4: invalid_json_str = "ls -la"
	tc = protowire.AppendTag(tc, 4, protowire.BytesType)
	tc = protowire.AppendString(tc, "ls -la")
	// Field 5: invalid_json_err = "not valid json"
	tc = protowire.AppendTag(tc, 5, protowire.BytesType)
	tc = protowire.AppendString(tc, "not valid json")
	// Field 6: is_custom_tool_call = true
	tc = protowire.AppendTag(tc, 6, protowire.VarintType)
	tc = protowire.AppendVarint(tc, 1)

	var f1 []byte
	f1 = protowire.AppendTag(f1, 6, protowire.BytesType)
	f1 = protowire.AppendBytes(f1, tc)

	var buf bytes.Buffer
	buf.Write(helps.WrapConnectEnvelope(f1))
	buf.Write(helps.WrapConnectEnvelopeWithFlag(helps.ConnectFlagEndStream, []byte(`{}`)))

	interactionsJSON, respLog, err := consumeDevinFramesToInteractions(&buf, "devin/swe-2", "chat-model-uid")
	if err != nil {
		t.Fatalf("consumeDevinFramesToInteractions failed: %v", err)
	}

	if len(respLog.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(respLog.ToolCalls))
	}
	if respLog.ToolCalls[0].Arguments != "ls -la" {
		t.Errorf("expected Arguments %q from invalid_json_str, got %q", "ls -la", respLog.ToolCalls[0].Arguments)
	}

	steps := gjson.GetBytes(interactionsJSON, "steps").Array()
	if len(steps) != 1 {
		t.Fatalf("expected 1 step in interactions JSON, got %d", len(steps))
	}
	if steps[0].Get("name").String() != "bash" || steps[0].Get("id").String() != "call_custom_1" {
		t.Errorf("unexpected step 0: %s", steps[0].Raw)
	}
	if steps[0].Get("arguments").String() != "ls -la" {
		t.Errorf("expected step 0 arguments %q, got %q", "ls -la", steps[0].Get("arguments").String())
	}

	// Also verify streaming receives raw arguments for custom tool call
	var streamBuf bytes.Buffer
	streamBuf.Write(helps.WrapConnectEnvelope(f1))
	streamBuf.Write(helps.WrapConnectEnvelopeWithFlag(helps.ConnectFlagEndStream, []byte(`{}`)))

	exec := NewDevinExecutor(&config.Config{})
	out := make(chan cliproxyexecutor.StreamChunk, 50)
	opts := cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatInteractions,
	}

	go func() {
		defer close(out)
		exec.streamDevinFrames(
			context.Background(),
			&streamBuf,
			cliproxyexecutor.Request{Model: "devin/swe-2"},
			opts,
			"chat-model-uid",
			sdktranslator.FormatInteractions,
			nil,
			out,
		)
	}()

	var streamArgs strings.Builder
	for chunk := range out {
		if chunk.Err != nil {
			t.Fatalf("unexpected stream error: %v", chunk.Err)
		}
		lines := strings.Split(string(chunk.Payload), "\n")
		for _, line := range lines {
			if strings.HasPrefix(line, "data: ") {
				data := strings.TrimPrefix(line, "data: ")
				if gjson.Get(data, "event_type").String() == "step.delta" && gjson.Get(data, "delta.type").String() == "arguments_delta" {
					streamArgs.WriteString(gjson.Get(data, "delta.arguments").String())
				}
			}
		}
	}

	if streamArgs.String() != "ls -la" {
		t.Errorf("expected stream delta arguments %q, got %q", "ls -la", streamArgs.String())
	}
}

func TestRegressionIssue5910_StreamInterleavedToolCallsByCallID(t *testing.T) {
	// Frame 1: call_1 start + partial args
	var tc1 []byte
	tc1 = protowire.AppendTag(tc1, 1, protowire.BytesType)
	tc1 = protowire.AppendString(tc1, "call_1")
	tc1 = protowire.AppendTag(tc1, 2, protowire.BytesType)
	tc1 = protowire.AppendString(tc1, "tool_1")
	tc1 = protowire.AppendTag(tc1, 3, protowire.BytesType)
	tc1 = protowire.AppendString(tc1, `{"a":`)

	var f1 []byte
	f1 = protowire.AppendTag(f1, 6, protowire.BytesType)
	f1 = protowire.AppendBytes(f1, tc1)

	// Frame 2: call_2 start + full args
	var tc2 []byte
	tc2 = protowire.AppendTag(tc2, 1, protowire.BytesType)
	tc2 = protowire.AppendString(tc2, "call_2")
	tc2 = protowire.AppendTag(tc2, 2, protowire.BytesType)
	tc2 = protowire.AppendString(tc2, "tool_2")
	tc2 = protowire.AppendTag(tc2, 3, protowire.BytesType)
	tc2 = protowire.AppendString(tc2, `{"b":2}`)

	var f2 []byte
	f2 = protowire.AppendTag(f2, 6, protowire.BytesType)
	f2 = protowire.AppendBytes(f2, tc2)

	// Frame 3: call_1 continuation args
	var tc1Cont []byte
	tc1Cont = protowire.AppendTag(tc1Cont, 1, protowire.BytesType)
	tc1Cont = protowire.AppendString(tc1Cont, "call_1")
	tc1Cont = protowire.AppendTag(tc1Cont, 3, protowire.BytesType)
	tc1Cont = protowire.AppendString(tc1Cont, `1}`)

	var f3 []byte
	f3 = protowire.AppendTag(f3, 6, protowire.BytesType)
	f3 = protowire.AppendBytes(f3, tc1Cont)

	var buf bytes.Buffer
	buf.Write(helps.WrapConnectEnvelope(f1))
	buf.Write(helps.WrapConnectEnvelope(f2))
	buf.Write(helps.WrapConnectEnvelope(f3))
	buf.Write(helps.WrapConnectEnvelopeWithFlag(helps.ConnectFlagEndStream, []byte(`{}`)))

	exec := NewDevinExecutor(&config.Config{})
	out := make(chan cliproxyexecutor.StreamChunk, 50)
	opts := cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatInteractions,
	}

	go func() {
		defer close(out)
		exec.streamDevinFrames(
			context.Background(),
			&buf,
			cliproxyexecutor.Request{Model: "devin/swe-2"},
			opts,
			"chat-model-uid",
			sdktranslator.FormatInteractions,
			nil,
			out,
		)
	}()

	var chunks []cliproxyexecutor.StreamChunk
	for chunk := range out {
		if chunk.Err != nil {
			t.Fatalf("unexpected chunk error: %v", chunk.Err)
		}
		chunks = append(chunks, chunk)
	}

	var events []gjson.Result
	for _, chunk := range chunks {
		lines := strings.Split(string(chunk.Payload), "\n")
		for _, line := range lines {
			if strings.HasPrefix(line, "data: ") {
				data := strings.TrimPrefix(line, "data: ")
				if strings.TrimSpace(data) != "[DONE]" {
					events = append(events, gjson.Parse(data))
				}
			}
		}
	}

	var toolCallsStarted []string
	var toolCallsStopped []int64
	stepArgs := make(map[int64]*strings.Builder)
	for _, ev := range events {
		eventType := ev.Get("event_type").String()
		if eventType == "step.start" && ev.Get("step.type").String() == "function_call" {
			idx := ev.Get("index").Int()
			toolCallsStarted = append(toolCallsStarted, fmt.Sprintf("index:%d,id:%s,name:%s", idx, ev.Get("step.id").String(), ev.Get("step.name").String()))
			stepArgs[idx] = &strings.Builder{}
		} else if eventType == "step.delta" && ev.Get("delta.type").String() == "arguments_delta" {
			idx := ev.Get("index").Int()
			if b, ok := stepArgs[idx]; ok {
				b.WriteString(ev.Get("delta.arguments").String())
			}
		} else if eventType == "step.stop" {
			toolCallsStopped = append(toolCallsStopped, ev.Get("index").Int())
		}
	}

	if len(toolCallsStarted) != 2 {
		t.Fatalf("expected exactly 2 tool calls started, got %d: %v", len(toolCallsStarted), toolCallsStarted)
	}
	if toolCallsStarted[0] != "index:0,id:call_1,name:tool_1" {
		t.Errorf("tool call 0 = %q, want index:0,id:call_1,name:tool_1", toolCallsStarted[0])
	}
	if toolCallsStarted[1] != "index:1,id:call_2,name:tool_2" {
		t.Errorf("tool call 1 = %q, want index:1,id:call_2,name:tool_2", toolCallsStarted[1])
	}

	if len(toolCallsStopped) != 2 {
		t.Fatalf("expected exactly 2 tool calls stopped, got %d: %v", len(toolCallsStopped), toolCallsStopped)
	}
	if toolCallsStopped[0] != 0 || toolCallsStopped[1] != 1 {
		t.Errorf("tool calls stopped indices = %v, want [0, 1]", toolCallsStopped)
	}

	if stepArgs[0].String() != `{"a":1}` {
		t.Errorf("stepArgs[0] = %q, want {\"a\":1}", stepArgs[0].String())
	}
	if stepArgs[1].String() != `{"b":2}` {
		t.Errorf("stepArgs[1] = %q, want {\"b\":2}", stepArgs[1].String())
	}
}

func TestRegressionIssue5910_UsageStatsCacheWriteTokensInResponses(t *testing.T) {
	// Frame 1 with Usage Field 7
	var f7Bytes []byte
	// Field 2: prompt tokens = 3
	f7Bytes = protowire.AppendTag(f7Bytes, 2, protowire.VarintType)
	f7Bytes = protowire.AppendVarint(f7Bytes, 3)
	// Field 3: completion tokens = 10
	f7Bytes = protowire.AppendTag(f7Bytes, 3, protowire.VarintType)
	f7Bytes = protowire.AppendVarint(f7Bytes, 10)
	// Field 4: cache_write_tokens = 14361
	f7Bytes = protowire.AppendTag(f7Bytes, 4, protowire.VarintType)
	f7Bytes = protowire.AppendVarint(f7Bytes, 14361)
	// Field 5: cached tokens = 50
	f7Bytes = protowire.AppendTag(f7Bytes, 5, protowire.VarintType)
	f7Bytes = protowire.AppendVarint(f7Bytes, 50)

	var frame []byte
	frame = protowire.AppendTag(frame, 7, protowire.BytesType)
	frame = protowire.AppendBytes(frame, f7Bytes)

	var buf bytes.Buffer
	buf.Write(helps.WrapConnectEnvelope(frame))
	buf.Write(helps.WrapConnectEnvelopeWithFlag(helps.ConnectFlagEndStream, []byte(`{}`)))

	// 1. Non-streaming test
	interactionsJSON, respLog, err := consumeDevinFramesToInteractions(&buf, "devin/swe-2", "chat-model-uid")
	if err != nil {
		t.Fatalf("consumeDevinFramesToInteractions failed: %v", err)
	}

	if respLog.Usage == nil {
		t.Fatal("expected non-nil respLog.Usage")
	}
	if respLog.Usage.PromptTokens != 3 {
		t.Errorf("respLog.Usage.PromptTokens = %d, want 3", respLog.Usage.PromptTokens)
	}
	if respLog.Usage.CacheWriteTokens != 14361 {
		t.Errorf("respLog.Usage.CacheWriteTokens = %d, want 14361", respLog.Usage.CacheWriteTokens)
	}

	root := gjson.ParseBytes(interactionsJSON)
	if root.Get("usage.total_input_tokens").Int() != 53 {
		t.Errorf("usage.total_input_tokens = %d, want 53", root.Get("usage.total_input_tokens").Int())
	}
	if root.Get("usage.cache_write_tokens").Int() != 14361 {
		t.Errorf("usage.cache_write_tokens = %d, want 14361", root.Get("usage.cache_write_tokens").Int())
	}
	detail := helps.ParseInteractionsUsage(interactionsJSON)
	if detail.CacheCreationTokens != 14361 {
		t.Errorf("ParseInteractionsUsage CacheCreationTokens = %d, want 14361", detail.CacheCreationTokens)
	}
	if detail.InputTokens != 53 {
		t.Errorf("ParseInteractionsUsage InputTokens = %d, want 53", detail.InputTokens)
	}

	// 2. Streaming test
	var streamBuf bytes.Buffer
	streamBuf.Write(helps.WrapConnectEnvelope(frame))
	streamBuf.Write(helps.WrapConnectEnvelopeWithFlag(helps.ConnectFlagEndStream, []byte(`{}`)))

	exec := NewDevinExecutor(&config.Config{})
	out := make(chan cliproxyexecutor.StreamChunk, 50)
	opts := cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatInteractions,
	}

	go func() {
		defer close(out)
		exec.streamDevinFrames(
			context.Background(),
			&streamBuf,
			cliproxyexecutor.Request{Model: "devin/swe-2"},
			opts,
			"chat-model-uid",
			sdktranslator.FormatInteractions,
			nil,
			out,
		)
	}()

	var completedEvent []byte
	for chunk := range out {
		if chunk.Err != nil {
			t.Fatalf("unexpected chunk error: %v", chunk.Err)
		}
		lines := strings.Split(string(chunk.Payload), "\n")
		for _, line := range lines {
			if strings.HasPrefix(line, "data: ") {
				data := []byte(strings.TrimPrefix(line, "data: "))
				if gjson.GetBytes(data, "event_type").String() == "interaction.completed" {
					completedEvent = data
				}
			}
		}
	}

	if len(completedEvent) == 0 {
		t.Fatal("expected interaction.completed event in stream")
	}
	cRoot := gjson.ParseBytes(completedEvent)
	if cRoot.Get("interaction.usage.cache_write_tokens").Int() != 14361 {
		t.Errorf("interaction.usage.cache_write_tokens = %d, want 14361", cRoot.Get("interaction.usage.cache_write_tokens").Int())
	}
	sDetail, ok := helps.ParseInteractionsStreamUsage(completedEvent)
	if !ok {
		t.Fatal("ParseInteractionsStreamUsage returned false")
	}
	if sDetail.CacheCreationTokens != 14361 {
		t.Errorf("ParseInteractionsStreamUsage CacheCreationTokens = %d, want 14361", sDetail.CacheCreationTokens)
	}
	if sDetail.InputTokens != 53 {
		t.Errorf("ParseInteractionsStreamUsage InputTokens = %d, want 53", sDetail.InputTokens)
	}
}

func TestDevinExecutor_FunctionResult_Regression_Issue5911(t *testing.T) {
	t.Run("structured_results_projected_to_plain_text", func(t *testing.T) {
		// Case 1: Array of text content blocks
		payloadBlocks := []byte(`{
			"model": "devin/swe-2",
			"input": [
				{"type": "function_call", "id": "call_1", "name": "read"},
				{
					"type": "function_result",
					"call_id": "call_1",
					"result": [
						{"type": "text", "text": "hello "},
						{"type": "text", "text": "world"}
					]
				}
			]
		}`)
		_, prompts1, _, _, _, _, _, _, _ := parseInteractionsPayload(payloadBlocks, nil)
		if len(prompts1) != 2 {
			t.Fatalf("expected 2 prompts, got %d", len(prompts1))
		}
		if prompts1[1].Source != 4 || prompts1[1].ToolCallID != "call_1" {
			t.Fatalf("expected tool prompt source=4 tool_call_id=call_1, got source=%d id=%s", prompts1[1].Source, prompts1[1].ToolCallID)
		}
		if strings.Contains(prompts1[1].Content, `[{"type":`) || strings.Contains(prompts1[1].Content, `"text":`) {
			t.Errorf("structured result has JSON scaffolding: %q", prompts1[1].Content)
		}
		if !strings.Contains(prompts1[1].Content, "hello") || !strings.Contains(prompts1[1].Content, "world") {
			t.Errorf("structured result missing extracted text: %q", prompts1[1].Content)
		}

		// Case 2: Object with type=text and text field
		payloadObject := []byte(`{
			"model": "devin/swe-2",
			"input": [
				{"type": "function_call", "id": "call_2", "name": "read"},
				{
					"type": "function_result",
					"call_id": "call_2",
					"result": {"type": "text", "text": "single object output"}
				}
			]
		}`)
		_, prompts2, _, _, _, _, _, _, _ := parseInteractionsPayload(payloadObject, nil)
		if len(prompts2) != 2 {
			t.Fatalf("expected 2 prompts, got %d", len(prompts2))
		}
		if prompts2[1].Content != "single object output" {
			t.Errorf("expected plain text 'single object output', got %q", prompts2[1].Content)
		}

		// Case 3: Object with nested content array
		payloadNested := []byte(`{
			"model": "devin/swe-2",
			"input": [
				{"type": "function_call", "id": "call_3", "name": "read"},
				{
					"type": "function_result",
					"call_id": "call_3",
					"result": {"content": [{"type": "text", "text": "nested content text"}]}
				}
			]
		}`)
		_, prompts3, _, _, _, _, _, _, _ := parseInteractionsPayload(payloadNested, nil)
		if len(prompts3) != 2 {
			t.Fatalf("expected 2 prompts, got %d", len(prompts3))
		}
		if prompts3[1].Content != "nested content text" {
			t.Errorf("expected plain text 'nested content text', got %q", prompts3[1].Content)
		}

		// Case 4: Business object with output/result fields alongside business fields is preserved in full JSON
		payloadBusiness := []byte(`{
			"model": "devin/swe-2",
			"input": [
				{"type": "function_call", "id": "call_4", "name": "exec"},
				{
					"type": "function_result",
					"call_id": "call_4",
					"result": {"output": "permission denied", "exit_code": 1, "retryable": false}
				}
			]
		}`)
		_, prompts4, _, _, _, _, _, _, _ := parseInteractionsPayload(payloadBusiness, nil)
		if len(prompts4) != 2 {
			t.Fatalf("expected 2 prompts, got %d", len(prompts4))
		}
		if !strings.Contains(prompts4[1].Content, "permission denied") ||
			!strings.Contains(prompts4[1].Content, `"exit_code": 1`) ||
			!strings.Contains(prompts4[1].Content, `"retryable": false`) {
			t.Errorf("expected full business object to be preserved, got %q", prompts4[1].Content)
		}

		// Case 5: Business object with id, name, content is preserved in full JSON
		payloadBusinessReport := []byte(`{
			"model": "devin/swe-2",
			"input": [
				{"type": "function_call", "id": "call_5", "name": "get_report"},
				{
					"type": "function_result",
					"call_id": "call_5",
					"result": {"id": 42, "name": "report", "content": "body"}
				}
			]
		}`)
		_, prompts5, _, _, _, _, _, _, _ := parseInteractionsPayload(payloadBusinessReport, nil)
		if len(prompts5) != 2 {
			t.Fatalf("expected 2 prompts, got %d", len(prompts5))
		}
		if !strings.Contains(prompts5[1].Content, `"id": 42`) && !strings.Contains(prompts5[1].Content, `"id":42`) ||
			!strings.Contains(prompts5[1].Content, `"name": "report"`) && !strings.Contains(prompts5[1].Content, `"name":"report"`) ||
			!strings.Contains(prompts5[1].Content, `"content": "body"`) && !strings.Contains(prompts5[1].Content, `"content":"body"`) {
			t.Errorf("expected full business report object to be preserved, got %q", prompts5[1].Content)
		}

		// Case 6: Pure business string array is preserved as raw JSON
		payloadStringArray := []byte(`{
			"model": "devin/swe-2",
			"input": [
				{"type": "function_call", "id": "call_6", "name": "list"},
				{
					"type": "function_result",
					"call_id": "call_6",
					"result": ["a", "b"]
				}
			]
		}`)
		_, prompts6, _, _, _, _, _, _, _ := parseInteractionsPayload(payloadStringArray, nil)
		if len(prompts6) != 2 {
			t.Fatalf("expected 2 prompts, got %d", len(prompts6))
		}
		if !strings.Contains(prompts6[1].Content, `"a"`) || !strings.Contains(prompts6[1].Content, `"b"`) || !strings.Contains(prompts6[1].Content, `[`) {
			t.Errorf("expected string array to be preserved as raw JSON, got %q", prompts6[1].Content)
		}

		// Case 7: Mixed string and business object array is preserved as raw JSON
		payloadMixedArray := []byte(`{
			"model": "devin/swe-2",
			"input": [
				{"type": "function_call", "id": "call_7", "name": "run"},
				{
					"type": "function_result",
					"call_id": "call_7",
					"result": ["ok", {"exit_code": 0}]
				}
			]
		}`)
		_, prompts7, _, _, _, _, _, _, _ := parseInteractionsPayload(payloadMixedArray, nil)
		if len(prompts7) != 2 {
			t.Fatalf("expected 2 prompts, got %d", len(prompts7))
		}
		if !strings.Contains(prompts7[1].Content, `"ok"`) || !strings.Contains(prompts7[1].Content, `"exit_code": 0`) {
			t.Errorf("expected mixed array to be preserved as raw JSON, got %q", prompts7[1].Content)
		}
	})

	t.Run("empty_results_get_placeholder", func(t *testing.T) {
		// Case 1: Empty string result
		payloadEmptyStr := []byte(`{
			"model": "devin/swe-2",
			"input": [
				{"type": "function_call", "id": "call_1", "name": "noop"},
				{"type": "function_result", "call_id": "call_1", "result": ""}
			]
		}`)
		_, prompts1, _, _, _, _, _, _, _ := parseInteractionsPayload(payloadEmptyStr, nil)
		if len(prompts1) != 2 {
			t.Fatalf("expected 2 prompts, got %d", len(prompts1))
		}
		if prompts1[1].Content == "" {
			t.Errorf("expected non-empty placeholder for empty string result, got empty string")
		}

		// Case 2: Absent result/output/content
		payloadAbsent := []byte(`{
			"model": "devin/swe-2",
			"input": [
				{"type": "function_call", "id": "call_2", "name": "noop"},
				{"type": "function_result", "call_id": "call_2"}
			]
		}`)
		_, prompts2, _, _, _, _, _, _, _ := parseInteractionsPayload(payloadAbsent, nil)
		if len(prompts2) != 2 {
			t.Fatalf("expected 2 prompts, got %d", len(prompts2))
		}
		if prompts2[1].Content == "" {
			t.Errorf("expected non-empty placeholder for absent result, got empty string")
		}

		// Case 3: Array with empty wrapper content block
		payloadEmptyWrapper := []byte(`{
			"model": "devin/swe-2",
			"input": [
				{"type": "function_call", "id": "call_3", "name": "noop"},
				{"type": "function_result", "call_id": "call_3", "result": [{"type": "tool_result", "content": ""}]}
			]
		}`)
		_, prompts3, _, _, _, _, _, _, _ := parseInteractionsPayload(payloadEmptyWrapper, nil)
		if len(prompts3) != 2 {
			t.Fatalf("expected 2 prompts, got %d", len(prompts3))
		}
		if prompts3[1].Content != "{}" {
			t.Errorf("expected placeholder '{}' for empty wrapper result, got %q", prompts3[1].Content)
		}

		// Case 4: Array with whitespace-only content
		payloadWhitespaceWrapper := []byte(`{
			"model": "devin/swe-2",
			"input": [
				{"type": "function_call", "id": "call_4", "name": "noop"},
				{"type": "function_result", "call_id": "call_4", "result": [{"type": "text", "text": "   "}]}
			]
		}`)
		_, prompts4, _, _, _, _, _, _, _ := parseInteractionsPayload(payloadWhitespaceWrapper, nil)
		if len(prompts4) != 2 {
			t.Fatalf("expected 2 prompts, got %d", len(prompts4))
		}
		if prompts4[1].Content != "{}" {
			t.Errorf("expected placeholder '{}' for whitespace wrapper result, got %q", prompts4[1].Content)
		}
	})

	t.Run("orphaned_results_sent_as_user_text", func(t *testing.T) {
		// Case 1: Interactions payload with trimmed function_call
		payloadTrimmed := []byte(`{
			"model": "devin/swe-2",
			"input": [
				{"type": "function_result", "call_id": "call_trimmed", "result": "orphaned data"}
			]
		}`)
		_, prompts1, _, _, _, _, _, _, _ := parseInteractionsPayload(payloadTrimmed, nil)
		if len(prompts1) != 1 {
			t.Fatalf("expected 1 prompt, got %d", len(prompts1))
		}
		if prompts1[0].Source != 1 {
			t.Errorf("expected orphaned function_result to be source=1 (user text), got source=%d", prompts1[0].Source)
		}
		if prompts1[0].Content != "orphaned data" {
			t.Errorf("expected content 'orphaned data', got %q", prompts1[0].Content)
		}

		// Case 2: Messages fallback with assistant.tool_calls properly matched
		payloadMessagesMatched := []byte(`{
			"model": "devin/swe-2",
			"messages": [
				{
					"role": "assistant",
					"content": "",
					"tool_calls": [
						{
							"id": "call_msg_1",
							"type": "function",
							"function": {"name": "read_file", "arguments": "{\"path\":\"a.txt\"}"}
						}
					]
				},
				{
					"role": "tool",
					"tool_call_id": "call_msg_1",
					"content": "file content here"
				}
			]
		}`)
		_, prompts2, _, _, _, _, _, _, _ := parseInteractionsPayload(payloadMessagesMatched, nil)
		if len(prompts2) != 2 {
			t.Fatalf("expected 2 prompts, got %d", len(prompts2))
		}
		if prompts2[0].Source != 2 || len(prompts2[0].ToolCalls) != 1 || prompts2[0].ToolCalls[0].ID != "call_msg_1" {
			t.Errorf("assistant tool_calls not parsed: %+v", prompts2[0])
		}
		if prompts2[0].ToolCalls[0].Arguments != `{"path":"a.txt"}` {
			t.Errorf("expected tool arguments %q, got %q", `{"path":"a.txt"}`, prompts2[0].ToolCalls[0].Arguments)
		}
		if prompts2[1].Source != 4 || prompts2[1].ToolCallID != "call_msg_1" {
			t.Errorf("expected tool prompt source=4 tool_call_id=call_msg_1, got source=%d id=%s", prompts2[1].Source, prompts2[1].ToolCallID)
		}
		if prompts2[1].Content != "file content here" {
			t.Errorf("expected content 'file content here', got %q", prompts2[1].Content)
		}

		// Case 3: Messages fallback with orphaned tool message
		payloadMessagesOrphan := []byte(`{
			"model": "devin/swe-2",
			"messages": [
				{
					"role": "tool",
					"tool_call_id": "call_orphan",
					"content": "orphaned tool message"
				}
			]
		}`)
		_, prompts3, _, _, _, _, _, _, _ := parseInteractionsPayload(payloadMessagesOrphan, nil)
		if len(prompts3) != 1 {
			t.Fatalf("expected 1 prompt, got %d", len(prompts3))
		}
		if prompts3[0].Source != 1 {
			t.Errorf("expected orphaned tool message to be source=1 (user text), got source=%d", prompts3[0].Source)
		}
		if prompts3[0].Content != "orphaned tool message" {
			t.Errorf("expected content 'orphaned tool message', got %q", prompts3[0].Content)
		}

		// Case 4: Multiple calls and results pairing with extra orphan result
		payloadMulti := []byte(`{
			"model": "devin/swe-2",
			"input": [
				{"type": "function_call", "id": "call_A", "name": "f1"},
				{"type": "function_call", "id": "call_B", "name": "f2"},
				{"type": "function_result", "call_id": "call_A", "result": "res_A"},
				{"type": "function_result", "call_id": "call_B", "result": "res_B"},
				{"type": "function_result", "call_id": "call_C", "result": "res_C_extra"}
			]
		}`)
		_, prompts4, _, _, _, _, _, _, _ := parseInteractionsPayload(payloadMulti, nil)
		if len(prompts4) != 4 {
			t.Fatalf("expected 4 prompts (1 assistant with 2 calls, 2 tool results, 1 user text orphan), got %d", len(prompts4))
		}
		if prompts4[1].Source != 4 || prompts4[1].ToolCallID != "call_A" || prompts4[1].Content != "res_A" {
			t.Errorf("call_A prompt mismatch: %+v", prompts4[1])
		}
		if prompts4[2].Source != 4 || prompts4[2].ToolCallID != "call_B" || prompts4[2].Content != "res_B" {
			t.Errorf("call_B prompt mismatch: %+v", prompts4[2])
		}
		if prompts4[3].Source != 1 || prompts4[3].Content != "res_C_extra" {
			t.Errorf("expected extra call_C to be source=1 user text, got: %+v", prompts4[3])
		}

		// Case 5: Orphaned tool result does not steal subsequent user message's image in supplementImagesFromOriginal
		origWithUserImg := []byte(`{
			"messages": [
				{
					"role": "tool",
					"tool_call_id": "call_orphan_img",
					"content": "text only orphan"
				},
				{
					"role": "user",
					"content": [
						{"type": "text", "text": "user message with picture"},
						{
							"type": "image",
							"source": {
								"type": "base64",
								"media_type": "image/png",
								"data": "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg=="
							}
						}
					]
				}
			]
		}`)
		interactionsOrphanAndUser := []byte(`{
			"model": "devin/swe-2",
			"input": [
				{"type": "function_result", "call_id": "call_orphan_img", "result": "text only orphan"},
				{"type": "user_input", "content": [{"type": "text", "text": "user message with picture"}]}
			]
		}`)
		_, prompts5, _, _, _, _, _, _, _ := parseInteractionsPayload(interactionsOrphanAndUser, origWithUserImg)
		if len(prompts5) != 2 {
			t.Fatalf("expected 2 prompts, got %d", len(prompts5))
		}
		// Prompt 0 is downgraded orphaned tool result
		if prompts5[0].Source != 1 || prompts5[0].OriginalToolCallID != "call_orphan_img" {
			t.Errorf("prompts5[0] expected source=1 orphaned tool, got: %+v", prompts5[0])
		}
		if len(prompts5[0].Images) != 0 {
			t.Errorf("orphaned tool result should not have stolen user images, got %d images", len(prompts5[0].Images))
		}
		// Prompt 1 is real user turn
		if prompts5[1].Source != 1 {
			t.Errorf("prompts5[1] expected source=1 user turn, got: %+v", prompts5[1])
		}
		if len(prompts5[1].Images) != 1 {
			t.Errorf("user turn should have received 1 image from originalRequest, got %d", len(prompts5[1].Images))
		}
		if !strings.Contains(prompts5[1].Content, "[Image 1: pasted_image_1.png]") {
			t.Errorf("user turn content missing image header: %q", prompts5[1].Content)
		}

		// Case 6: Orphaned tool result without any ID does not steal subsequent user message's image
		origWithNoID := []byte(`{
			"messages": [
				{
					"role": "tool",
					"content": "no id orphan text"
				},
				{
					"role": "user",
					"content": [
						{"type": "text", "text": "user message"},
						{
							"type": "image",
							"source": {
								"type": "base64",
								"media_type": "image/png",
								"data": "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg=="
							}
						}
					]
				}
			]
		}`)
		interactionsNoID := []byte(`{
			"model": "devin/swe-2",
			"input": [
				{"type": "function_result", "result": "no id orphan text"},
				{"type": "user_input", "content": [{"type": "text", "text": "user message"}]}
			]
		}`)
		_, prompts6, _, _, _, _, _, _, _ := parseInteractionsPayload(interactionsNoID, origWithNoID)
		if len(prompts6) != 2 {
			t.Fatalf("expected 2 prompts, got %d", len(prompts6))
		}
		if prompts6[0].Source != 1 || !prompts6[0].IsOrphanedTool {
			t.Errorf("expected prompt 0 to be marked orphaned tool, got: %+v", prompts6[0])
		}
		if len(prompts6[0].Images) != 0 {
			t.Errorf("orphaned tool result with no ID stole user image: %+v", prompts6[0].Images)
		}
		if len(prompts6[1].Images) != 1 {
			t.Errorf("user message should have 1 image, got %d", len(prompts6[1].Images))
		}
	})
}

func TestDevinExecutor_NoModelSubstitutionWarningForIntentionalMapping_NonStream(t *testing.T) {
	// Build a Devin Connect-RPC response containing Usage with ModelName = "swe-2-high"
	var f7 []byte
	f7 = protowire.AppendTag(f7, 2, protowire.VarintType)
	f7 = protowire.AppendVarint(f7, 10)
	f7 = protowire.AppendTag(f7, 3, protowire.VarintType)
	f7 = protowire.AppendVarint(f7, 20)
	f7 = protowire.AppendTag(f7, 9, protowire.BytesType)
	f7 = protowire.AppendString(f7, "swe-2-high")

	var f1 []byte
	f1 = protowire.AppendTag(f1, 7, protowire.BytesType)
	f1 = protowire.AppendBytes(f1, f7)

	var buf bytes.Buffer
	buf.Write(helps.WrapConnectEnvelope(f1))
	buf.Write(helps.WrapConnectEnvelopeWithFlag(helps.ConnectFlagEndStream, []byte(`{}`)))

	mockRT := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/connect+proto"}},
			Body:       io.NopCloser(bytes.NewReader(buf.Bytes())),
		}, nil
	})

	hook := new(logtest.Hook)
	log.StandardLogger().AddHook(hook)
	t.Cleanup(func() {
		log.StandardLogger().ReplaceHooks(make(log.LevelHooks))
	})

	exec := NewDevinExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		ID:         "devin-auth-map-nonstream",
		Provider:   "devin",
		Attributes: map[string]string{"api_key": "test-key"},
	}

	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", mockRT)
	req := cliproxyexecutor.Request{
		Model:   "devin/swe-2",
		Payload: []byte(`{"messages":[{"role":"user","content":"hello"}]}`),
	}
	opts := cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAI,
	}

	_, err := exec.Execute(ctx, auth, req, opts)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	for _, entry := range hook.AllEntries() {
		if entry.Level == log.WarnLevel && strings.Contains(entry.Message, "upstream served model") {
			t.Fatalf("unexpected model substitution warning for intentional mapping: %s", entry.Message)
		}
	}
}

func TestDevinExecutor_NoModelSubstitutionWarningForIntentionalMapping_Stream(t *testing.T) {
	var f7 []byte
	f7 = protowire.AppendTag(f7, 2, protowire.VarintType)
	f7 = protowire.AppendVarint(f7, 10)
	f7 = protowire.AppendTag(f7, 3, protowire.VarintType)
	f7 = protowire.AppendVarint(f7, 20)
	f7 = protowire.AppendTag(f7, 9, protowire.BytesType)
	f7 = protowire.AppendString(f7, "swe-2-high")

	var f1 []byte
	f1 = protowire.AppendTag(f1, 7, protowire.BytesType)
	f1 = protowire.AppendBytes(f1, f7)

	var buf bytes.Buffer
	buf.Write(helps.WrapConnectEnvelope(f1))
	buf.Write(helps.WrapConnectEnvelopeWithFlag(helps.ConnectFlagEndStream, []byte(`{}`)))

	mockRT := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/connect+proto"}},
			Body:       io.NopCloser(bytes.NewReader(buf.Bytes())),
		}, nil
	})

	hook := new(logtest.Hook)
	log.StandardLogger().AddHook(hook)
	t.Cleanup(func() {
		log.StandardLogger().ReplaceHooks(make(log.LevelHooks))
	})

	exec := NewDevinExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		ID:         "devin-auth-map-stream",
		Provider:   "devin",
		Attributes: map[string]string{"api_key": "test-key"},
	}

	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", mockRT)
	req := cliproxyexecutor.Request{
		Model:   "devin/swe-2",
		Payload: []byte(`{"messages":[{"role":"user","content":"hello"}]}`),
	}
	opts := cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAI,
	}

	result, err := exec.ExecuteStream(ctx, auth, req, opts)
	if err != nil {
		t.Fatalf("ExecuteStream failed: %v", err)
	}
	for range result.Chunks {
	}

	for _, entry := range hook.AllEntries() {
		if entry.Level == log.WarnLevel && strings.Contains(entry.Message, "upstream served model") {
			t.Fatalf("unexpected model substitution warning for intentional mapping: %s", entry.Message)
		}
	}
}

func TestDevinExecutor_WarnsWhenUpstreamServesUnexpectedModel(t *testing.T) {
	var f7 []byte
	f7 = protowire.AppendTag(f7, 2, protowire.VarintType)
	f7 = protowire.AppendVarint(f7, 10)
	f7 = protowire.AppendTag(f7, 3, protowire.VarintType)
	f7 = protowire.AppendVarint(f7, 20)
	f7 = protowire.AppendTag(f7, 9, protowire.BytesType)
	f7 = protowire.AppendString(f7, "unexpected-model-xyz")

	var f1 []byte
	f1 = protowire.AppendTag(f1, 7, protowire.BytesType)
	f1 = protowire.AppendBytes(f1, f7)

	var buf bytes.Buffer
	buf.Write(helps.WrapConnectEnvelope(f1))
	buf.Write(helps.WrapConnectEnvelopeWithFlag(helps.ConnectFlagEndStream, []byte(`{}`)))

	mockRT := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/connect+proto"}},
			Body:       io.NopCloser(bytes.NewReader(buf.Bytes())),
		}, nil
	})

	hook := new(logtest.Hook)
	log.StandardLogger().AddHook(hook)
	t.Cleanup(func() {
		log.StandardLogger().ReplaceHooks(make(log.LevelHooks))
	})

	exec := NewDevinExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		ID:         "devin-auth-unexpected",
		Provider:   "devin",
		Attributes: map[string]string{"api_key": "test-key"},
	}

	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", mockRT)
	req := cliproxyexecutor.Request{
		Model:   "devin/swe-2",
		Payload: []byte(`{"messages":[{"role":"user","content":"hello"}]}`),
	}
	opts := cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAI,
	}

	_, err := exec.Execute(ctx, auth, req, opts)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	found := false
	for _, entry := range hook.AllEntries() {
		if entry.Level == log.WarnLevel && strings.Contains(entry.Message, "upstream served model \"unexpected-model-xyz\"") {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected model substitution warning for unexpected model, got none")
	}
}

func TestRegressionIssue5951_DevinToolActivityOrderedBeforeAssistantMessage(t *testing.T) {
	// Frame 1: tool call 1
	var tc1 []byte
	tc1 = protowire.AppendTag(tc1, 1, protowire.BytesType)
	tc1 = protowire.AppendString(tc1, "call_1")
	tc1 = protowire.AppendTag(tc1, 2, protowire.BytesType)
	tc1 = protowire.AppendString(tc1, "exec_command")
	tc1 = protowire.AppendTag(tc1, 3, protowire.BytesType)
	tc1 = protowire.AppendString(tc1, `{"cmd":"git status"}`)

	var f1 []byte
	f1 = protowire.AppendTag(f1, 6, protowire.BytesType)
	f1 = protowire.AppendBytes(f1, tc1)

	// Frame 2: tool call 2
	var tc2 []byte
	tc2 = protowire.AppendTag(tc2, 1, protowire.BytesType)
	tc2 = protowire.AppendString(tc2, "call_2")
	tc2 = protowire.AppendTag(tc2, 2, protowire.BytesType)
	tc2 = protowire.AppendString(tc2, "exec_command")
	tc2 = protowire.AppendTag(tc2, 3, protowire.BytesType)
	tc2 = protowire.AppendString(tc2, `{"cmd":"ls -la"}`)

	var f2 []byte
	f2 = protowire.AppendTag(f2, 6, protowire.BytesType)
	f2 = protowire.AppendBytes(f2, tc2)

	// Frame 3: assistant final message content text
	var f3 []byte
	f3 = protowire.AppendTag(f3, 3, protowire.BytesType)
	f3 = protowire.AppendString(f3, "Task completed. 要我盯 DAV-1085 结果吗？")

	// 1. Test Streaming in OpenAI Responses wire format (used by Codex Desktop)
	var streamBuf bytes.Buffer
	streamBuf.Write(helps.WrapConnectEnvelope(f1))
	streamBuf.Write(helps.WrapConnectEnvelope(f2))
	streamBuf.Write(helps.WrapConnectEnvelope(f3))
	streamBuf.Write(helps.WrapConnectEnvelopeWithFlag(helps.ConnectFlagEndStream, []byte(`{}`)))

	exec := NewDevinExecutor(&config.Config{})
	out := make(chan cliproxyexecutor.StreamChunk, 100)
	opts := cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAIResponse,
	}

	go func() {
		defer close(out)
		exec.streamDevinFrames(
			context.Background(),
			&streamBuf,
			cliproxyexecutor.Request{Model: "devin/swe-2"},
			opts,
			"swe-2-high",
			sdktranslator.FormatOpenAIResponse,
			nil,
			out,
		)
	}()

	var chunks []cliproxyexecutor.StreamChunk
	for chunk := range out {
		if chunk.Err != nil {
			t.Fatalf("unexpected chunk error: %v", chunk.Err)
		}
		chunks = append(chunks, chunk)
	}

	var doneItemTypes []string
	for _, chunk := range chunks {
		lines := strings.Split(string(chunk.Payload), "\n")
		for _, line := range lines {
			if strings.HasPrefix(line, "data: ") {
				data := strings.TrimPrefix(line, "data: ")
				if strings.TrimSpace(data) == "[DONE]" {
					continue
				}
				ev := gjson.Parse(data)
				if ev.Get("type").String() == "response.output_item.done" {
					doneItemTypes = append(doneItemTypes, ev.Get("item.type").String())
				}
			}
		}
	}

	// In Codex Desktop / OpenAI Responses, tool call items must be finalized before the assistant final message
	expectedOrder := []string{"function_call", "function_call", "message"}
	if len(doneItemTypes) != len(expectedOrder) {
		t.Fatalf("doneItemTypes len = %d (%v), want %d (%v)", len(doneItemTypes), doneItemTypes, len(expectedOrder), expectedOrder)
	}
	for i, want := range expectedOrder {
		if doneItemTypes[i] != want {
			t.Fatalf("doneItemTypes[%d] = %q, want %q; full order = %v", i, doneItemTypes[i], want, doneItemTypes)
		}
	}

	// Also verify the output array in response.completed
	var completedOutputTypes []string
	for _, chunk := range chunks {
		lines := strings.Split(string(chunk.Payload), "\n")
		for _, line := range lines {
			if strings.HasPrefix(line, "data: ") {
				data := strings.TrimPrefix(line, "data: ")
				if strings.TrimSpace(data) == "[DONE]" {
					continue
				}
				ev := gjson.Parse(data)
				if ev.Get("type").String() == "response.completed" {
					ev.Get("response.output").ForEach(func(_, item gjson.Result) bool {
						completedOutputTypes = append(completedOutputTypes, item.Get("type").String())
						return true
					})
				}
			}
		}
	}
	if len(completedOutputTypes) != len(expectedOrder) {
		t.Fatalf("completedOutputTypes len = %d (%v), want %d (%v)", len(completedOutputTypes), completedOutputTypes, len(expectedOrder), expectedOrder)
	}
	for i, want := range expectedOrder {
		if completedOutputTypes[i] != want {
			t.Fatalf("completedOutputTypes[%d] = %q, want %q", i, completedOutputTypes[i], want)
		}
	}

	// 2. Test Non-streaming output order
	var nonStreamBuf bytes.Buffer
	nonStreamBuf.Write(helps.WrapConnectEnvelope(f1))
	nonStreamBuf.Write(helps.WrapConnectEnvelope(f2))
	nonStreamBuf.Write(helps.WrapConnectEnvelope(f3))
	nonStreamBuf.Write(helps.WrapConnectEnvelopeWithFlag(helps.ConnectFlagEndStream, []byte(`{}`)))

	interactionsJSON, _, err := consumeDevinFramesToInteractions(&nonStreamBuf, "devin/swe-2", "swe-2-high")
	if err != nil {
		t.Fatalf("consumeDevinFramesToInteractions failed: %v", err)
	}
	root := gjson.ParseBytes(interactionsJSON)
	var stepTypes []string
	root.Get("steps").ForEach(func(_, step gjson.Result) bool {
		stepTypes = append(stepTypes, step.Get("type").String())
		return true
	})
	expectedSteps := []string{"function_call", "function_call", "model_output"}
	if len(stepTypes) != len(expectedSteps) {
		t.Fatalf("stepTypes len = %d (%v), want %d (%v)", len(stepTypes), stepTypes, len(expectedSteps), expectedSteps)
	}
	for i, want := range expectedSteps {
		if stepTypes[i] != want {
			t.Fatalf("stepTypes[%d] = %q, want %q; full order = %v", i, stepTypes[i], want, stepTypes)
		}
	}
}

func TestRegressionIssue5951_InterleavedToolArgumentsAndText(t *testing.T) {
	// Frame 1: call_1 partial args
	var tc1Part1 []byte
	tc1Part1 = protowire.AppendTag(tc1Part1, 1, protowire.BytesType)
	tc1Part1 = protowire.AppendString(tc1Part1, "call_1")
	tc1Part1 = protowire.AppendTag(tc1Part1, 2, protowire.BytesType)
	tc1Part1 = protowire.AppendString(tc1Part1, "exec_command")
	tc1Part1 = protowire.AppendTag(tc1Part1, 3, protowire.BytesType)
	tc1Part1 = protowire.AppendString(tc1Part1, `{"cmd":"git `)

	var f1 []byte
	f1 = protowire.AppendTag(f1, 6, protowire.BytesType)
	f1 = protowire.AppendBytes(f1, tc1Part1)

	// Frame 2: text arrival while call_1 is active
	var f2 []byte
	f2 = protowire.AppendTag(f2, 3, protowire.BytesType)
	f2 = protowire.AppendString(f2, "Working on it... ")

	// Frame 3: call_1 continuation args
	var tc1Part2 []byte
	tc1Part2 = protowire.AppendTag(tc1Part2, 1, protowire.BytesType)
	tc1Part2 = protowire.AppendString(tc1Part2, "call_1")
	tc1Part2 = protowire.AppendTag(tc1Part2, 3, protowire.BytesType)
	tc1Part2 = protowire.AppendString(tc1Part2, `status"}`)

	var f3 []byte
	f3 = protowire.AppendTag(f3, 6, protowire.BytesType)
	f3 = protowire.AppendBytes(f3, tc1Part2)

	// Frame 4: more final text
	var f4 []byte
	f4 = protowire.AppendTag(f4, 3, protowire.BytesType)
	f4 = protowire.AppendString(f4, "Done.")

	// Test streaming
	var streamBuf bytes.Buffer
	streamBuf.Write(helps.WrapConnectEnvelope(f1))
	streamBuf.Write(helps.WrapConnectEnvelope(f2))
	streamBuf.Write(helps.WrapConnectEnvelope(f3))
	streamBuf.Write(helps.WrapConnectEnvelope(f4))
	streamBuf.Write(helps.WrapConnectEnvelopeWithFlag(helps.ConnectFlagEndStream, []byte(`{}`)))

	exec := NewDevinExecutor(&config.Config{})
	out := make(chan cliproxyexecutor.StreamChunk, 100)
	opts := cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAIResponse,
	}

	go func() {
		defer close(out)
		exec.streamDevinFrames(
			context.Background(),
			&streamBuf,
			cliproxyexecutor.Request{Model: "devin/swe-2"},
			opts,
			"swe-2-high",
			sdktranslator.FormatOpenAIResponse,
			nil,
			out,
		)
	}()

	var chunks []cliproxyexecutor.StreamChunk
	for chunk := range out {
		if chunk.Err != nil {
			t.Fatalf("unexpected chunk error: %v", chunk.Err)
		}
		chunks = append(chunks, chunk)
	}

	var doneItemTypes []string
	var callCount int
	var completedCallArgs string
	for _, chunk := range chunks {
		lines := strings.Split(string(chunk.Payload), "\n")
		for _, line := range lines {
			if strings.HasPrefix(line, "data: ") {
				data := strings.TrimPrefix(line, "data: ")
				if strings.TrimSpace(data) == "[DONE]" {
					continue
				}
				ev := gjson.Parse(data)
				if ev.Get("type").String() == "response.output_item.added" && ev.Get("item.type").String() == "function_call" {
					callCount++
				}
				if ev.Get("type").String() == "response.output_item.done" {
					doneItemTypes = append(doneItemTypes, ev.Get("item.type").String())
					if ev.Get("item.type").String() == "function_call" {
						completedCallArgs = ev.Get("item.arguments").String()
					}
				}
			}
		}
	}

	if callCount != 1 {
		t.Fatalf("callCount = %d, want 1 (should not duplicate call_1)", callCount)
	}
	if completedCallArgs != `{"cmd":"git status"}` {
		t.Fatalf("completedCallArgs = %q, want %q", completedCallArgs, `{"cmd":"git status"}`)
	}
	expectedOrder := []string{"function_call", "message"}
	if len(doneItemTypes) != len(expectedOrder) {
		t.Fatalf("doneItemTypes len = %d (%v), want %d (%v)", len(doneItemTypes), doneItemTypes, len(expectedOrder), expectedOrder)
	}
	for i, want := range expectedOrder {
		if doneItemTypes[i] != want {
			t.Fatalf("doneItemTypes[%d] = %q, want %q", i, doneItemTypes[i], want)
		}
	}

	// 2. Test Non-streaming: tool arguments must be intact and assistant message must NOT be split
	var nonStreamBuf bytes.Buffer
	nonStreamBuf.Write(helps.WrapConnectEnvelope(f1))
	nonStreamBuf.Write(helps.WrapConnectEnvelope(f2))
	nonStreamBuf.Write(helps.WrapConnectEnvelope(f3))
	nonStreamBuf.Write(helps.WrapConnectEnvelope(f4))
	nonStreamBuf.Write(helps.WrapConnectEnvelopeWithFlag(helps.ConnectFlagEndStream, []byte(`{}`)))

	interactionsJSON, _, err := consumeDevinFramesToInteractions(&nonStreamBuf, "devin/swe-2", "swe-2-high")
	if err != nil {
		t.Fatalf("consumeDevinFramesToInteractions failed: %v", err)
	}
	root := gjson.ParseBytes(interactionsJSON)
	steps := root.Get("steps").Array()
	if len(steps) != 2 {
		t.Fatalf("steps count = %d, want 2 [function_call, model_output]. Steps: %s", len(steps), string(interactionsJSON))
	}
	if steps[0].Get("type").String() != "function_call" || steps[0].Get("id").String() != "call_1" {
		t.Fatalf("step[0] = %+v, want function_call call_1", steps[0].Raw)
	}
	if steps[0].Get("arguments").String() != `{"cmd":"git status"}` {
		t.Fatalf("step[0] arguments = %q, want %q", steps[0].Get("arguments").String(), `{"cmd":"git status"}`)
	}
	if steps[1].Get("type").String() != "model_output" {
		t.Fatalf("step[1] type = %q, want model_output", steps[1].Get("type").String())
	}
	if steps[1].Get("content.0.text").String() != "Working on it... Done." {
		t.Fatalf("step[1] text = %q, want 'Working on it... Done.'", steps[1].Get("content.0.text").String())
	}
}

func TestRegressionIssue5951_SameFrameToolAndContent(t *testing.T) {
	// A single frame carrying BOTH tool call delta and content text
	var tc []byte
	tc = protowire.AppendTag(tc, 1, protowire.BytesType)
	tc = protowire.AppendString(tc, "call_same_frame")
	tc = protowire.AppendTag(tc, 2, protowire.BytesType)
	tc = protowire.AppendString(tc, "exec_command")
	tc = protowire.AppendTag(tc, 3, protowire.BytesType)
	tc = protowire.AppendString(tc, `{"cmd":"pwd"}`)

	var f1 []byte
	f1 = protowire.AppendTag(f1, 6, protowire.BytesType)
	f1 = protowire.AppendBytes(f1, tc)
	f1 = protowire.AppendTag(f1, 3, protowire.BytesType)
	f1 = protowire.AppendString(f1, "Finished pwd execution.")

	// 1. Streaming test
	var streamBuf bytes.Buffer
	streamBuf.Write(helps.WrapConnectEnvelope(f1))
	streamBuf.Write(helps.WrapConnectEnvelopeWithFlag(helps.ConnectFlagEndStream, []byte(`{}`)))

	exec := NewDevinExecutor(&config.Config{})
	out := make(chan cliproxyexecutor.StreamChunk, 100)
	opts := cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAIResponse,
	}

	go func() {
		defer close(out)
		exec.streamDevinFrames(
			context.Background(),
			&streamBuf,
			cliproxyexecutor.Request{Model: "devin/swe-2"},
			opts,
			"swe-2-high",
			sdktranslator.FormatOpenAIResponse,
			nil,
			out,
		)
	}()

	var doneItemTypes []string
	var completedOutputTypes []string
	for chunk := range out {
		if chunk.Err != nil {
			t.Fatalf("unexpected chunk error: %v", chunk.Err)
		}
		lines := strings.Split(string(chunk.Payload), "\n")
		for _, line := range lines {
			if strings.HasPrefix(line, "data: ") {
				data := strings.TrimPrefix(line, "data: ")
				if strings.TrimSpace(data) == "[DONE]" {
					continue
				}
				ev := gjson.Parse(data)
				if ev.Get("type").String() == "response.output_item.done" {
					doneItemTypes = append(doneItemTypes, ev.Get("item.type").String())
				}
				if ev.Get("type").String() == "response.completed" {
					ev.Get("response.output").ForEach(func(_, item gjson.Result) bool {
						completedOutputTypes = append(completedOutputTypes, item.Get("type").String())
						return true
					})
				}
			}
		}
	}

	expectedOrder := []string{"function_call", "message"}
	if len(doneItemTypes) != len(expectedOrder) {
		t.Fatalf("doneItemTypes len = %d (%v), want %d (%v)", len(doneItemTypes), doneItemTypes, len(expectedOrder), expectedOrder)
	}
	for i, want := range expectedOrder {
		if doneItemTypes[i] != want {
			t.Fatalf("doneItemTypes[%d] = %q, want %q", i, doneItemTypes[i], want)
		}
	}
	if len(completedOutputTypes) != len(expectedOrder) {
		t.Fatalf("completedOutputTypes len = %d (%v), want %d (%v)", len(completedOutputTypes), completedOutputTypes, len(expectedOrder), expectedOrder)
	}
	for i, want := range expectedOrder {
		if completedOutputTypes[i] != want {
			t.Fatalf("completedOutputTypes[%d] = %q, want %q", i, completedOutputTypes[i], want)
		}
	}

	// 2. Non-streaming test
	var nonStreamBuf bytes.Buffer
	nonStreamBuf.Write(helps.WrapConnectEnvelope(f1))
	nonStreamBuf.Write(helps.WrapConnectEnvelopeWithFlag(helps.ConnectFlagEndStream, []byte(`{}`)))

	interactionsJSON, _, err := consumeDevinFramesToInteractions(&nonStreamBuf, "devin/swe-2", "swe-2-high")
	if err != nil {
		t.Fatalf("consumeDevinFramesToInteractions failed: %v", err)
	}
	root := gjson.ParseBytes(interactionsJSON)
	var stepTypes []string
	root.Get("steps").ForEach(func(_, step gjson.Result) bool {
		stepTypes = append(stepTypes, step.Get("type").String())
		return true
	})
	expectedSteps := []string{"function_call", "model_output"}
	if len(stepTypes) != len(expectedSteps) {
		t.Fatalf("stepTypes len = %d (%v), want %d (%v)", len(stepTypes), stepTypes, len(expectedSteps), expectedSteps)
	}
	for i, want := range expectedSteps {
		if stepTypes[i] != want {
			t.Fatalf("stepTypes[%d] = %q, want %q", i, stepTypes[i], want)
		}
	}
}

func TestRegressionIssue5951_PreToolExplanationAndPostToolAnswer(t *testing.T) {
	// Frame 1: pre-tool explanation text
	var f1 []byte
	f1 = protowire.AppendTag(f1, 3, protowire.BytesType)
	f1 = protowire.AppendString(f1, "I will inspect the repository:")

	// Frame 2: tool call
	var tc1 []byte
	tc1 = protowire.AppendTag(tc1, 1, protowire.BytesType)
	tc1 = protowire.AppendString(tc1, "call_ls")
	tc1 = protowire.AppendTag(tc1, 2, protowire.BytesType)
	tc1 = protowire.AppendString(tc1, "exec_command")
	tc1 = protowire.AppendTag(tc1, 3, protowire.BytesType)
	tc1 = protowire.AppendString(tc1, `{"cmd":"ls -la"}`)

	var f2 []byte
	f2 = protowire.AppendTag(f2, 6, protowire.BytesType)
	f2 = protowire.AppendBytes(f2, tc1)

	// Frame 3: post-tool final answer text
	var f3 []byte
	f3 = protowire.AppendTag(f3, 3, protowire.BytesType)
	f3 = protowire.AppendString(f3, "Inspection completed. All files in order.")

	// 1. Test Streaming: output_item.done and completed.output must be [message, function_call, message]
	var streamBuf bytes.Buffer
	streamBuf.Write(helps.WrapConnectEnvelope(f1))
	streamBuf.Write(helps.WrapConnectEnvelope(f2))
	streamBuf.Write(helps.WrapConnectEnvelope(f3))
	streamBuf.Write(helps.WrapConnectEnvelopeWithFlag(helps.ConnectFlagEndStream, []byte(`{}`)))

	exec := NewDevinExecutor(&config.Config{})
	out := make(chan cliproxyexecutor.StreamChunk, 100)
	opts := cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAIResponse,
	}

	go func() {
		defer close(out)
		exec.streamDevinFrames(
			context.Background(),
			&streamBuf,
			cliproxyexecutor.Request{Model: "devin/swe-2"},
			opts,
			"swe-2-high",
			sdktranslator.FormatOpenAIResponse,
			nil,
			out,
		)
	}()

	var chunks []cliproxyexecutor.StreamChunk
	for chunk := range out {
		if chunk.Err != nil {
			t.Fatalf("unexpected chunk error: %v", chunk.Err)
		}
		chunks = append(chunks, chunk)
	}

	var doneItemTypes []string
	var completedOutputTypes []string
	for _, chunk := range chunks {
		lines := strings.Split(string(chunk.Payload), "\n")
		for _, line := range lines {
			if strings.HasPrefix(line, "data: ") {
				data := strings.TrimPrefix(line, "data: ")
				if strings.TrimSpace(data) == "[DONE]" {
					continue
				}
				ev := gjson.Parse(data)
				if ev.Get("type").String() == "response.output_item.done" {
					doneItemTypes = append(doneItemTypes, ev.Get("item.type").String())
				}
				if ev.Get("type").String() == "response.completed" {
					ev.Get("response.output").ForEach(func(_, item gjson.Result) bool {
						completedOutputTypes = append(completedOutputTypes, item.Get("type").String())
						return true
					})
				}
			}
		}
	}

	expectedOrder := []string{"message", "function_call", "message"}
	if len(doneItemTypes) != len(expectedOrder) {
		t.Fatalf("doneItemTypes len = %d (%v), want %d (%v)", len(doneItemTypes), doneItemTypes, len(expectedOrder), expectedOrder)
	}
	for i, want := range expectedOrder {
		if doneItemTypes[i] != want {
			t.Fatalf("doneItemTypes[%d] = %q, want %q", i, doneItemTypes[i], want)
		}
	}
	if len(completedOutputTypes) != len(expectedOrder) {
		t.Fatalf("completedOutputTypes len = %d (%v), want %d (%v)", len(completedOutputTypes), completedOutputTypes, len(expectedOrder), expectedOrder)
	}
	for i, want := range expectedOrder {
		if completedOutputTypes[i] != want {
			t.Fatalf("completedOutputTypes[%d] = %q, want %q", i, completedOutputTypes[i], want)
		}
	}

	// 2. Test Non-streaming: steps must preserve [model_output, function_call, model_output]
	var nonStreamBuf bytes.Buffer
	nonStreamBuf.Write(helps.WrapConnectEnvelope(f1))
	nonStreamBuf.Write(helps.WrapConnectEnvelope(f2))
	nonStreamBuf.Write(helps.WrapConnectEnvelope(f3))
	nonStreamBuf.Write(helps.WrapConnectEnvelopeWithFlag(helps.ConnectFlagEndStream, []byte(`{}`)))

	interactionsJSON, _, err := consumeDevinFramesToInteractions(&nonStreamBuf, "devin/swe-2", "swe-2-high")
	if err != nil {
		t.Fatalf("consumeDevinFramesToInteractions failed: %v", err)
	}
	root := gjson.ParseBytes(interactionsJSON)
	var stepTypes []string
	root.Get("steps").ForEach(func(_, step gjson.Result) bool {
		stepTypes = append(stepTypes, step.Get("type").String())
		return true
	})
	expectedSteps := []string{"model_output", "function_call", "model_output"}
	if len(stepTypes) != len(expectedSteps) {
		t.Fatalf("stepTypes len = %d (%v), want %d (%v)", len(stepTypes), stepTypes, len(expectedSteps), expectedSteps)
	}
	for i, want := range expectedSteps {
		if stepTypes[i] != want {
			t.Fatalf("stepTypes[%d] = %q, want %q", i, stepTypes[i], want)
		}
	}
}

func TestRegressionIssue5951_MultiStageToolAndIntermediateExplanation(t *testing.T) {
	// Frame 1: Tool A
	var tcA []byte
	tcA = protowire.AppendTag(tcA, 1, protowire.BytesType)
	tcA = protowire.AppendString(tcA, "call_A")
	tcA = protowire.AppendTag(tcA, 2, protowire.BytesType)
	tcA = protowire.AppendString(tcA, "exec_command")
	tcA = protowire.AppendTag(tcA, 3, protowire.BytesType)
	tcA = protowire.AppendString(tcA, `{"cmd":"pwd"}`)

	var f1 []byte
	f1 = protowire.AppendTag(f1, 6, protowire.BytesType)
	f1 = protowire.AppendBytes(f1, tcA)

	// Frame 2: Intermediate explanation
	var f2 []byte
	f2 = protowire.AppendTag(f2, 3, protowire.BytesType)
	f2 = protowire.AppendString(f2, "Directory checked, now listing files.")

	// Frame 3: Tool B
	var tcB []byte
	tcB = protowire.AppendTag(tcB, 1, protowire.BytesType)
	tcB = protowire.AppendString(tcB, "call_B")
	tcB = protowire.AppendTag(tcB, 2, protowire.BytesType)
	tcB = protowire.AppendString(tcB, "exec_command")
	tcB = protowire.AppendTag(tcB, 3, protowire.BytesType)
	tcB = protowire.AppendString(tcB, `{"cmd":"ls"}`)

	var f3 []byte
	f3 = protowire.AppendTag(f3, 6, protowire.BytesType)
	f3 = protowire.AppendBytes(f3, tcB)

	// Frame 4: Final answer
	var f4 []byte
	f4 = protowire.AppendTag(f4, 3, protowire.BytesType)
	f4 = protowire.AppendString(f4, "All tasks finished successfully.")

	// 1. Test Streaming
	var streamBuf bytes.Buffer
	streamBuf.Write(helps.WrapConnectEnvelope(f1))
	streamBuf.Write(helps.WrapConnectEnvelope(f2))
	streamBuf.Write(helps.WrapConnectEnvelope(f3))
	streamBuf.Write(helps.WrapConnectEnvelope(f4))
	streamBuf.Write(helps.WrapConnectEnvelopeWithFlag(helps.ConnectFlagEndStream, []byte(`{}`)))

	exec := NewDevinExecutor(&config.Config{})
	out := make(chan cliproxyexecutor.StreamChunk, 100)
	opts := cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAIResponse,
	}

	go func() {
		defer close(out)
		exec.streamDevinFrames(
			context.Background(),
			&streamBuf,
			cliproxyexecutor.Request{Model: "devin/swe-2"},
			opts,
			"swe-2-high",
			sdktranslator.FormatOpenAIResponse,
			nil,
			out,
		)
	}()

	var chunks []cliproxyexecutor.StreamChunk
	for chunk := range out {
		if chunk.Err != nil {
			t.Fatalf("unexpected chunk error: %v", chunk.Err)
		}
		chunks = append(chunks, chunk)
	}

	var doneItemTypes []string
	var completedOutputTypes []string
	for _, chunk := range chunks {
		lines := strings.Split(string(chunk.Payload), "\n")
		for _, line := range lines {
			if strings.HasPrefix(line, "data: ") {
				data := strings.TrimPrefix(line, "data: ")
				if strings.TrimSpace(data) == "[DONE]" {
					continue
				}
				ev := gjson.Parse(data)
				if ev.Get("type").String() == "response.output_item.done" {
					doneItemTypes = append(doneItemTypes, ev.Get("item.type").String())
				}
				if ev.Get("type").String() == "response.completed" {
					ev.Get("response.output").ForEach(func(_, item gjson.Result) bool {
						completedOutputTypes = append(completedOutputTypes, item.Get("type").String())
						return true
					})
				}
			}
		}
	}

	expectedOrder := []string{"function_call", "function_call", "message"}
	if len(doneItemTypes) != len(expectedOrder) {
		t.Fatalf("doneItemTypes len = %d (%v), want %d (%v)", len(doneItemTypes), doneItemTypes, len(expectedOrder), expectedOrder)
	}
	for i, want := range expectedOrder {
		if doneItemTypes[i] != want {
			t.Fatalf("doneItemTypes[%d] = %q, want %q; full = %v", i, doneItemTypes[i], want, doneItemTypes)
		}
	}
	if len(completedOutputTypes) != len(expectedOrder) {
		t.Fatalf("completedOutputTypes len = %d (%v), want %d (%v)", len(completedOutputTypes), completedOutputTypes, len(expectedOrder), expectedOrder)
	}
	for i, want := range expectedOrder {
		if completedOutputTypes[i] != want {
			t.Fatalf("completedOutputTypes[%d] = %q, want %q; full = %v", i, completedOutputTypes[i], want, completedOutputTypes)
		}
	}

	// 2. Test Non-streaming
	var nonStreamBuf bytes.Buffer
	nonStreamBuf.Write(helps.WrapConnectEnvelope(f1))
	nonStreamBuf.Write(helps.WrapConnectEnvelope(f2))
	nonStreamBuf.Write(helps.WrapConnectEnvelope(f3))
	nonStreamBuf.Write(helps.WrapConnectEnvelope(f4))
	nonStreamBuf.Write(helps.WrapConnectEnvelopeWithFlag(helps.ConnectFlagEndStream, []byte(`{}`)))

	interactionsJSON, _, err := consumeDevinFramesToInteractions(&nonStreamBuf, "devin/swe-2", "swe-2-high")
	if err != nil {
		t.Fatalf("consumeDevinFramesToInteractions failed: %v", err)
	}
	root := gjson.ParseBytes(interactionsJSON)
	var stepTypes []string
	root.Get("steps").ForEach(func(_, step gjson.Result) bool {
		stepTypes = append(stepTypes, step.Get("type").String())
		return true
	})
	expectedSteps := []string{"function_call", "function_call", "model_output"}
	if len(stepTypes) != len(expectedSteps) {
		t.Fatalf("stepTypes len = %d (%v), want %d (%v)", len(stepTypes), stepTypes, len(expectedSteps), expectedSteps)
	}
	for i, want := range expectedSteps {
		if stepTypes[i] != want {
			t.Fatalf("stepTypes[%d] = %q, want %q; full = %v", i, stepTypes[i], want, stepTypes)
		}
	}
}

func TestRegressionIssue5951_InterleavedToolsWithTextAndContinuation(t *testing.T) {
	// Frame 1: call_1 start + partial args
	var tc1Part1 []byte
	tc1Part1 = protowire.AppendTag(tc1Part1, 1, protowire.BytesType)
	tc1Part1 = protowire.AppendString(tc1Part1, "call_1")
	tc1Part1 = protowire.AppendTag(tc1Part1, 2, protowire.BytesType)
	tc1Part1 = protowire.AppendString(tc1Part1, "tool_1")
	tc1Part1 = protowire.AppendTag(tc1Part1, 3, protowire.BytesType)
	tc1Part1 = protowire.AppendString(tc1Part1, `{"a":`)

	var f1 []byte
	f1 = protowire.AppendTag(f1, 6, protowire.BytesType)
	f1 = protowire.AppendBytes(f1, tc1Part1)

	// Frame 2: text arrival
	var f2 []byte
	f2 = protowire.AppendTag(f2, 3, protowire.BytesType)
	f2 = protowire.AppendString(f2, "Working on step 1... ")

	// Frame 3: call_2 start + full args
	var tc2 []byte
	tc2 = protowire.AppendTag(tc2, 1, protowire.BytesType)
	tc2 = protowire.AppendString(tc2, "call_2")
	tc2 = protowire.AppendTag(tc2, 2, protowire.BytesType)
	tc2 = protowire.AppendString(tc2, "tool_2")
	tc2 = protowire.AppendTag(tc2, 3, protowire.BytesType)
	tc2 = protowire.AppendString(tc2, `{"b":2}`)

	var f3 []byte
	f3 = protowire.AppendTag(f3, 6, protowire.BytesType)
	f3 = protowire.AppendBytes(f3, tc2)

	// Frame 4: call_1 continuation args
	var tc1Part2 []byte
	tc1Part2 = protowire.AppendTag(tc1Part2, 1, protowire.BytesType)
	tc1Part2 = protowire.AppendString(tc1Part2, "call_1")
	tc1Part2 = protowire.AppendTag(tc1Part2, 3, protowire.BytesType)
	tc1Part2 = protowire.AppendString(tc1Part2, `1}`)

	var f4 []byte
	f4 = protowire.AppendTag(f4, 6, protowire.BytesType)
	f4 = protowire.AppendBytes(f4, tc1Part2)

	// Frame 5: final text
	var f5 []byte
	f5 = protowire.AppendTag(f5, 3, protowire.BytesType)
	f5 = protowire.AppendString(f5, "All done.")

	// 1. Streaming test
	var streamBuf bytes.Buffer
	streamBuf.Write(helps.WrapConnectEnvelope(f1))
	streamBuf.Write(helps.WrapConnectEnvelope(f2))
	streamBuf.Write(helps.WrapConnectEnvelope(f3))
	streamBuf.Write(helps.WrapConnectEnvelope(f4))
	streamBuf.Write(helps.WrapConnectEnvelope(f5))
	streamBuf.Write(helps.WrapConnectEnvelopeWithFlag(helps.ConnectFlagEndStream, []byte(`{}`)))

	exec := NewDevinExecutor(&config.Config{})
	out := make(chan cliproxyexecutor.StreamChunk, 100)
	opts := cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAIResponse,
	}

	go func() {
		defer close(out)
		exec.streamDevinFrames(
			context.Background(),
			&streamBuf,
			cliproxyexecutor.Request{Model: "devin/swe-2"},
			opts,
			"swe-2-high",
			sdktranslator.FormatOpenAIResponse,
			nil,
			out,
		)
	}()

	var chunks []cliproxyexecutor.StreamChunk
	for chunk := range out {
		if chunk.Err != nil {
			t.Fatalf("unexpected chunk error: %v", chunk.Err)
		}
		chunks = append(chunks, chunk)
	}

	var doneItemTypes []string
	callCount := 0
	callArgs := make(map[string]string)
	for _, chunk := range chunks {
		lines := strings.Split(string(chunk.Payload), "\n")
		for _, line := range lines {
			if strings.HasPrefix(line, "data: ") {
				data := strings.TrimPrefix(line, "data: ")
				if strings.TrimSpace(data) == "[DONE]" {
					continue
				}
				ev := gjson.Parse(data)
				if ev.Get("type").String() == "response.output_item.added" && ev.Get("item.type").String() == "function_call" {
					callCount++
				}
				if ev.Get("type").String() == "response.output_item.done" {
					doneItemTypes = append(doneItemTypes, ev.Get("item.type").String())
					if ev.Get("item.type").String() == "function_call" {
						callArgs[ev.Get("item.id").String()] = ev.Get("item.arguments").String()
					}
				}
			}
		}
	}

	if callCount != 2 {
		t.Fatalf("callCount = %d, want 2 (no duplicate calls)", callCount)
	}
	if callArgs["call_1"] != `{"a":1}` {
		t.Fatalf("call_1 args = %q, want %q", callArgs["call_1"], `{"a":1}`)
	}
	if callArgs["call_2"] != `{"b":2}` {
		t.Fatalf("call_2 args = %q, want %q", callArgs["call_2"], `{"b":2}`)
	}
	expectedOrder := []string{"function_call", "function_call", "message"}
	if len(doneItemTypes) != len(expectedOrder) {
		t.Fatalf("doneItemTypes len = %d (%v), want %d (%v)", len(doneItemTypes), doneItemTypes, len(expectedOrder), expectedOrder)
	}
	for i, want := range expectedOrder {
		if doneItemTypes[i] != want {
			t.Fatalf("doneItemTypes[%d] = %q, want %q", i, doneItemTypes[i], want)
		}
	}

	// 2. Non-streaming test
	var nonStreamBuf bytes.Buffer
	nonStreamBuf.Write(helps.WrapConnectEnvelope(f1))
	nonStreamBuf.Write(helps.WrapConnectEnvelope(f2))
	nonStreamBuf.Write(helps.WrapConnectEnvelope(f3))
	nonStreamBuf.Write(helps.WrapConnectEnvelope(f4))
	nonStreamBuf.Write(helps.WrapConnectEnvelope(f5))
	nonStreamBuf.Write(helps.WrapConnectEnvelopeWithFlag(helps.ConnectFlagEndStream, []byte(`{}`)))

	interactionsJSON, _, err := consumeDevinFramesToInteractions(&nonStreamBuf, "devin/swe-2", "swe-2-high")
	if err != nil {
		t.Fatalf("consumeDevinFramesToInteractions failed: %v", err)
	}
	root := gjson.ParseBytes(interactionsJSON)
	steps := root.Get("steps").Array()
	if len(steps) != 3 {
		t.Fatalf("steps count = %d, want 3 [call_1, call_2, text]. Steps: %s", len(steps), string(interactionsJSON))
	}
	if steps[0].Get("id").String() != "call_1" || steps[0].Get("arguments").String() != `{"a":1}` {
		t.Fatalf("step[0] = %+v, want call_1 with {\"a\":1}", steps[0].Raw)
	}
	if steps[1].Get("id").String() != "call_2" || steps[1].Get("arguments").String() != `{"b":2}` {
		t.Fatalf("step[1] = %+v, want call_2 with {\"b\":2}", steps[1].Raw)
	}
	if steps[2].Get("type").String() != "model_output" {
		t.Fatalf("step[2] type = %q, want model_output", steps[2].Get("type").String())
	}
	if steps[2].Get("content.0.text").String() != "Working on step 1... All done." {
		t.Fatalf("step[2] text = %q, want 'Working on step 1... All done.'", steps[2].Get("content.0.text").String())
	}
}
