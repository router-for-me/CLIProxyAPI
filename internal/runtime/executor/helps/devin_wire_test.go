package helps

import (
	"bytes"
	"strings"
	"testing"
)

func TestConnectEnvelopeFraming(t *testing.T) {
	payload := []byte("hello devin connect-rpc")
	envelope := WrapConnectEnvelope(payload)

	if len(envelope) != 5+len(payload) {
		t.Fatalf("expected envelope len %d, got %d", 5+len(payload), len(envelope))
	}
	if envelope[0] != ConnectFlagData {
		t.Fatalf("expected flag 0x00, got 0x%02x", envelope[0])
	}

	r := bytes.NewReader(envelope)
	flag, readPayload, err := ReadConnectFrame(r)
	if err != nil {
		t.Fatalf("ReadConnectFrame failed: %v", err)
	}
	if flag != ConnectFlagData {
		t.Errorf("flag = 0x%02x, want 0x00", flag)
	}
	if !bytes.Equal(readPayload, payload) {
		t.Errorf("payload = %q, want %q", string(readPayload), string(payload))
	}
}

func TestGenerateDevinDeviceFingerprint(t *testing.T) {
	fp1 := GenerateDevinDeviceFingerprint("seed-1")
	if len(fp1) != DevinFingerprintHexLen {
		t.Fatalf("fp1 len = %d, want %d", len(fp1), DevinFingerprintHexLen)
	}
	// Check hex characters
	for _, c := range fp1 {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			t.Fatalf("invalid hex char in fingerprint: %c", c)
		}
	}

	// Deterministic seed produces deterministic fingerprint
	fp2 := GenerateDevinDeviceFingerprint("seed-1")
	if fp1 != fp2 {
		t.Fatalf("fingerprints for same seed do not match: %s != %s", fp1, fp2)
	}
}

func TestBuildDevinGetChatMessageRequest(t *testing.T) {
	prompts := []DevinPrompt{
		{
			MessageID: "msg-1",
			Source:    1,
			Content:   "hello",
		},
		{
			MessageID: "msg-2",
			Source:    2,
			Content:   "hi there",
			Thinking:  "thinking step",
			Signature: []byte("sealed.v1.test"),
		},
		{
			MessageID:  "msg-3",
			Source:     4,
			Content:    `{"result":"ok"}`,
			ToolCallID: "call-1",
		},
	}
	tools := []DevinTool{
		{
			Name:        "get_weather",
			Description: "lookup weather",
			Parameters:  []byte(`{"type":"object"}`),
		},
	}

	temp := 0.7
	req := BuildDevinGetChatMessageRequest(
		"token-123",
		"device-seed-1",
		"swe-2-high",
		"you are a helpful assistant",
		prompts,
		tools,
		&temp,
		4000,
		"session-1",
		"cascade-1",
		nil,
	)

	if len(req) == 0 {
		t.Fatal("encoded request is empty")
	}

	// Envelope check
	framed := WrapConnectEnvelope(req)
	flag, readPayload, err := ReadConnectFrame(bytes.NewReader(framed))
	if err != nil {
		t.Fatalf("ReadConnectFrame failed: %v", err)
	}
	if flag != ConnectFlagData || len(readPayload) != len(req) {
		t.Fatalf("framed payload length mismatch")
	}
}

func TestSanitizeDevinSystemPrompt_AndSensitiveWords(t *testing.T) {
	matcher := BuildSensitiveWordMatcher([]string{"API", "proxy"})
	rawPrompt := "x-anthropic-billing-header: cc_version=2.1.260;\nYou are Claude Code, Anthropic's official CLI for Claude.\nHelp the project with API and proxy."
	sanitized := SanitizeDevinSystemPrompt(rawPrompt, matcher)

	if strings.Contains(sanitized, "x-anthropic-billing-header") {
		t.Errorf("sanitized prompt still contains billing header: %s", sanitized)
	}
	if strings.Contains(sanitized, "You are Claude Code") {
		t.Errorf("sanitized prompt still contains Claude Code identity: %s", sanitized)
	}
	if strings.Contains(sanitized, "API") && !strings.Contains(sanitized, zeroWidthSpace) {
		t.Errorf("API was not obfuscated with zero-width space")
	}
}

func TestBuildDevinGetChatMessageRequest_WithImages(t *testing.T) {
	prompts := []DevinPrompt{
		{
			MessageID: "msg-img-1",
			Source:    1,
			Content:   "[Image 1: pasted_image_1.png]\n\nocr this image",
			Images: []DevinImage{
				{
					Base64Data: "iVBORw0KGgoAAAANSUhEUgAA...",
					MimeType:   "image/png",
				},
			},
		},
	}

	temp := 0.0
	req := BuildDevinGetChatMessageRequest(
		"token-123",
		"device-seed-1",
		"swe-2-max",
		"assistant instructions",
		prompts,
		nil,
		&temp,
		4000,
		"session-1",
		"cascade-1",
		nil,
	)

	if len(req) == 0 {
		t.Fatal("encoded request with images is empty")
	}

	// Verify that the payload contains the image base64 data and mime type
	if !bytes.Contains(req, []byte("iVBORw0KGgoAAAANSUhEUgAA...")) {
		t.Error("encoded payload missing image base64 data")
	}
	if !bytes.Contains(req, []byte("image/png")) {
		t.Error("encoded payload missing image mime type")
	}
}

func TestParseDevinFrame(t *testing.T) {
	// Synthesize a response frame containing:
	// #1 output_id, #3 delta_text, #9 delta_thinking, #10 delta_signature, #21 delta_signature_type
	var payload []byte
	payload = appendFieldBytes(payload, 1, []byte("bot-uuid-123"))
	payload = appendFieldBytes(payload, 3, []byte("Hello world"))
	payload = appendFieldBytes(payload, 9, []byte("Let me think..."))
	payload = appendFieldBytes(payload, 10, []byte("CAQS-signature-bytes"))
	payload = appendFieldBytes(payload, 21, []byte("anthropic"))

	res, err := ParseDevinFrame(payload)
	if err != nil {
		t.Fatalf("ParseDevinFrame failed: %v", err)
	}

	if res.OutputID != "bot-uuid-123" {
		t.Errorf("OutputID = %q, want bot-uuid-123", res.OutputID)
	}
	if res.ContentText != "Hello world" {
		t.Errorf("ContentText = %q, want 'Hello world'", res.ContentText)
	}
	if res.ThinkingText != "Let me think..." {
		t.Errorf("ThinkingText = %q, want 'Let me think...'", res.ThinkingText)
	}
	if string(res.DeltaSignature) != "CAQS-signature-bytes" {
		t.Errorf("DeltaSignature = %q, want 'CAQS-signature-bytes'", string(res.DeltaSignature))
	}
	if res.DeltaSignatureType != "anthropic" {
		t.Errorf("DeltaSignatureType = %q, want 'anthropic'", res.DeltaSignatureType)
	}
}

func TestParseDevinTrailerError(t *testing.T) {
	quotaJSON := []byte(`{"error":{"code":"failed_precondition","message":"User monthly ACU quota exhausted (trace ID: 12345)"}}`)
	code, err := ParseDevinTrailerError(quotaJSON)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if code != 429 {
		t.Errorf("status code = %d, want 429 for quota error", code)
	}

	configJSON := []byte(`{"error":{"code":"failed_precondition","message":"Client version 3000.1.0 is no longer supported"}}`)
	code, err = ParseDevinTrailerError(configJSON)
	if code != 400 {
		t.Errorf("status code = %d, want 400 for config error", code)
	}

	unauthJSON := []byte(`{"error":{"code":"unauthenticated","message":"Invalid or expired session token"}}`)
	code, err = ParseDevinTrailerError(unauthJSON)
	if code != 401 {
		t.Errorf("status code = %d, want 401", code)
	}
}

func TestUTF8SplitBuffer(t *testing.T) {
	buf := &UTF8SplitBuffer{}
	// "你好" in UTF-8: \xe4\xbd\xa0 \xe5\xa5\xbd (3 bytes each)
	chunk1 := []byte{0xe4, 0xbd}       // first 2 bytes of 你
	chunk2 := []byte{0xa0, 0xe5, 0xa5} // last 1 byte of 你, first 2 bytes of 好
	chunk3 := []byte{0xbd}             // last 1 byte of 好

	s1 := buf.Feed(chunk1)
	if s1 != "" {
		t.Errorf("expected empty string from split chunk1, got %q", s1)
	}

	s2 := buf.Feed(chunk2)
	if s2 != "你" {
		t.Errorf("expected '你' from chunk2, got %q", s2)
	}

	s3 := buf.Feed(chunk3)
	if s3 != "好" {
		t.Errorf("expected '好' from chunk3, got %q", s3)
	}
}

func appendFieldBytes(dst []byte, fieldNum int, val []byte) []byte {
	tag := uint64(fieldNum<<3 | 2)
	dst = appendVarint(dst, tag)
	dst = appendVarint(dst, uint64(len(val)))
	dst = append(dst, val...)
	return dst
}

func appendVarint(dst []byte, v uint64) []byte {
	for v >= 0x80 {
		dst = append(dst, byte(v)|0x80)
		v >>= 7
	}
	dst = append(dst, byte(v))
	return dst
}
