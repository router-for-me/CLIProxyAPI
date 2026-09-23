package executor

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

var benchmarkSanitizeOpenAIResponsesReasoningOutput []byte

func assertEmptyReasoningContent(t *testing.T, body []byte, path string) {
	t.Helper()
	content := gjson.GetBytes(body, path)
	if !content.Exists() || !content.IsArray() || len(content.Array()) != 0 {
		t.Fatalf("%s should be an empty array for Codex maxItems:0, got %s body=%s", path, content.Raw, body)
	}
}

func validOpenAIResponsesReasoningEncryptedContentForTest() string {
	payload := make([]byte, 1+8+16+16+32)
	payload[0] = 0x80
	for i := 9; i < len(payload); i++ {
		payload[i] = byte(i)
	}
	return base64.RawURLEncoding.EncodeToString(payload)
}

func TestSanitizeOpenAIResponsesReasoningEncryptedContent_StripsOrphanIDsWhenStoreDisabled(t *testing.T) {
	valid := validOpenAIResponsesReasoningEncryptedContentForTest()
	body := []byte(`{"store":false,"input":[` +
		`{"id":"rs_bad","type":"reasoning","encrypted_content":"bad","summary":[]},` +
		`{"id":"rs_orphan","type":"reasoning","summary":[]},` +
		`{"id":"rs_good","type":"reasoning","encrypted_content":"` + valid + `","summary":[]},` +
		`{"id":"msg_1","type":"message","role":"user","content":"hi"}` +
		`]}`)

	got := sanitizeOpenAIResponsesReasoningEncryptedContent(context.Background(), "test", body)

	if gjson.GetBytes(got, "input.0.encrypted_content").Exists() {
		t.Fatalf("invalid encrypted_content still present: %s", got)
	}
	if gjson.GetBytes(got, "input.0.id").Exists() {
		t.Fatalf("invalid reasoning id should be stripped when store=false: %s", got)
	}
	if gjson.GetBytes(got, "input.1.id").Exists() {
		t.Fatalf("orphan reasoning id should be stripped when store=false: %s", got)
	}
	if gotID := gjson.GetBytes(got, "input.2.id").String(); gotID != "rs_good" {
		t.Fatalf("valid reasoning id = %q, want rs_good; body=%s", gotID, got)
	}
	if gotEC := gjson.GetBytes(got, "input.2.encrypted_content").String(); gotEC != valid {
		t.Fatalf("valid encrypted_content not preserved: %s", got)
	}
	if gotID := gjson.GetBytes(got, "input.3.id").String(); gotID != "msg_1" {
		t.Fatalf("non-reasoning id should stay: %s", got)
	}
}

func TestSanitizeOpenAIResponsesReasoningEncryptedContent_KeepsIDsWhenStoreEnabled(t *testing.T) {
	body := []byte(`{"store":true,"input":[` +
		`{"id":"rs_bad","type":"reasoning","encrypted_content":"bad","summary":[]},` +
		`{"id":"rs_orphan","type":"reasoning","summary":[]}` +
		`]}`)

	got := sanitizeOpenAIResponsesReasoningEncryptedContent(context.Background(), "test", body)

	if gjson.GetBytes(got, "input.0.encrypted_content").Exists() {
		t.Fatalf("invalid encrypted_content still present: %s", got)
	}
	if gotID := gjson.GetBytes(got, "input.0.id").String(); gotID != "rs_bad" {
		t.Fatalf("store=true should keep reasoning id after dropping invalid encrypted_content, got %q body=%s", gotID, got)
	}
	if gotID := gjson.GetBytes(got, "input.1.id").String(); gotID != "rs_orphan" {
		t.Fatalf("store=true should keep orphan reasoning id, got %q body=%s", gotID, got)
	}
}

func TestSanitizeOpenAIResponsesReasoningEncryptedContent_MovesCleartextContentToSummary(t *testing.T) {
	// Codex Desktop replays third-party thinking as reasoning.content[reasoning_text].
	// Official Codex upstream rejects that shape with array_above_max_length (maxItems: 0).
	body := []byte(`{"store":false,"input":[` +
		`{"type":"reasoning","summary":[],"content":[{"type":"reasoning_text","text":"The model thinking process from a previous turn with a third-party provider..."}],"encrypted_content":null},` +
		`{"id":"msg_1","type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}` +
		`]}`)

	got := sanitizeOpenAIResponsesReasoningEncryptedContent(context.Background(), "test", body)

	assertEmptyReasoningContent(t, got, "input.0.content")
	if gotType := gjson.GetBytes(got, "input.0.summary.0.type").String(); gotType != "summary_text" {
		t.Fatalf("summary.0.type = %q, want summary_text; body=%s", gotType, got)
	}
	if gotText := gjson.GetBytes(got, "input.0.summary.0.text").String(); gotText != "The model thinking process from a previous turn with a third-party provider..." {
		t.Fatalf("summary.0.text = %q, want promoted reasoning_text; body=%s", gotText, got)
	}
	if gjson.GetBytes(got, "input.0.encrypted_content").Exists() {
		t.Fatalf("null encrypted_content should still be stripped: %s", got)
	}
	if gotType := gjson.GetBytes(got, "input.1.content.0.type").String(); gotType != "input_text" {
		t.Fatalf("non-reasoning content should stay: %s", got)
	}
}

func TestSanitizeOpenAIResponsesReasoningEncryptedContent_DoesNotDuplicateExistingSummary(t *testing.T) {
	body := []byte(`{"store":false,"input":[{"type":"reasoning","summary":[{"type":"summary_text","text":"already summarized"}],"content":[{"type":"reasoning_text","text":"duplicate thinking"}]}]}`)

	got := sanitizeOpenAIResponsesReasoningEncryptedContent(context.Background(), "test", body)

	assertEmptyReasoningContent(t, got, "input.0.content")
	if gotLen := len(gjson.GetBytes(got, "input.0.summary").Array()); gotLen != 1 {
		t.Fatalf("summary length = %d, want 1 (no duplicate); body=%s", gotLen, got)
	}
	if gotText := gjson.GetBytes(got, "input.0.summary.0.text").String(); gotText != "already summarized" {
		t.Fatalf("existing summary overwritten: %s", got)
	}
}

func TestSanitizeOpenAIResponsesReasoningEncryptedContent_PromotesMultipleReasoningTextParts(t *testing.T) {
	body := []byte(`{"store":false,"input":[{"type":"reasoning","summary":[],"content":[{"type":"reasoning_text","text":"step one"},{"type":"reasoning_text","text":"step two"}]}]}`)

	got := sanitizeOpenAIResponsesReasoningEncryptedContent(context.Background(), "test", body)

	assertEmptyReasoningContent(t, got, "input.0.content")
	if gotLen := len(gjson.GetBytes(got, "input.0.summary").Array()); gotLen != 2 {
		t.Fatalf("summary length = %d, want 2; body=%s", gotLen, got)
	}
	if gotText := gjson.GetBytes(got, "input.0.summary.0.text").String(); gotText != "step one" {
		t.Fatalf("summary.0.text = %q, want step one; body=%s", gotText, got)
	}
	if gotText := gjson.GetBytes(got, "input.0.summary.1.text").String(); gotText != "step two" {
		t.Fatalf("summary.1.text = %q, want step two; body=%s", gotText, got)
	}
}

func TestSanitizeOpenAIResponsesReasoningEncryptedContent_KeepsValidEncryptedContentWhenStrippingContent(t *testing.T) {
	valid := validOpenAIResponsesReasoningEncryptedContentForTest()
	body := []byte(`{"store":false,"input":[{"id":"rs_good","type":"reasoning","encrypted_content":"` + valid + `","summary":[],"content":[{"type":"reasoning_text","text":"cleartext thinking"}]}]}`)

	got := sanitizeOpenAIResponsesReasoningEncryptedContent(context.Background(), "test", body)

	assertEmptyReasoningContent(t, got, "input.0.content")
	if gotID := gjson.GetBytes(got, "input.0.id").String(); gotID != "rs_good" {
		t.Fatalf("valid reasoning id = %q, want rs_good; body=%s", gotID, got)
	}
	if gotEC := gjson.GetBytes(got, "input.0.encrypted_content").String(); gotEC != valid {
		t.Fatalf("valid encrypted_content not preserved: %s", got)
	}
	if gotText := gjson.GetBytes(got, "input.0.summary.0.text").String(); gotText != "cleartext thinking" {
		t.Fatalf("summary.0.text = %q, want promoted reasoning_text; body=%s", gotText, got)
	}
}

func TestSanitizeOpenAIResponsesReasoningEncryptedContent_NoopReturnsOriginalBody(t *testing.T) {
	valid := validOpenAIResponsesReasoningEncryptedContentForTest()
	body := []byte(`{"store":false,"input":[{"id":"rs_good","type":"reasoning","encrypted_content":"` + valid + `","summary":[]},{"role":"user","content":"hi"}]}`)
	got := sanitizeOpenAIResponsesReasoningEncryptedContent(context.Background(), "test", body)
	if string(got) != string(body) {
		t.Fatalf("noop path should return original body unchanged\ngot=%s\nwant=%s", got, body)
	}
	if len(got) > 0 && len(body) > 0 && &got[0] != &body[0] {
		t.Fatalf("noop path should return the original body slice")
	}
}

func TestSanitizeOpenAIResponsesReasoningEncryptedContentWithCompat_PreservesReasoningContentAndID(t *testing.T) {
	body := []byte(`{"store":false,"input":[` +
		`{"id":"rs_compat","type":"reasoning","summary":[],"content":[{"type":"reasoning_text","text":"keep cleartext thinking"}],"encrypted_content":null},` +
		`{"id":"msg_1","type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}` +
		`]}`)

	got := sanitizeOpenAIResponsesReasoningEncryptedContentWithCompat(context.Background(), "test", body, true)

	reasoningItem := gjson.GetBytes(got, "input.0")
	if gotID := reasoningItem.Get("id").String(); gotID != "rs_compat" {
		t.Fatalf("reasoning id = %q, want rs_compat; body=%s", gotID, got)
	}
	content := reasoningItem.Get("content")
	if !content.Exists() || !content.IsArray() || len(content.Array()) != 1 {
		t.Fatalf("content should have 1 item, got %s body=%s", content.Raw, got)
	}
	if gotType := reasoningItem.Get("content.0.type").String(); gotType != "reasoning_text" {
		t.Fatalf("content.0.type = %q, want reasoning_text; body=%s", gotType, got)
	}
	if gotText := reasoningItem.Get("content.0.text").String(); gotText != "keep cleartext thinking" {
		t.Fatalf("content.0.text = %q, want keep cleartext thinking; body=%s", gotText, got)
	}
	if gotLen := len(reasoningItem.Get("summary").Array()); gotLen != 0 {
		t.Fatalf("summary should remain empty for compat, got %d items; body=%s", gotLen, got)
	}
	if reasoningItem.Get("encrypted_content").Exists() {
		t.Fatalf("null encrypted_content should still be stripped: %s", got)
	}
}

func BenchmarkSanitizeOpenAIResponsesReasoningEncryptedContentLargeNoopPayload(b *testing.B) {
	body := []byte(`{"store":false,"input":[{"type":"message","role":"user","content":"` + strings.Repeat("x", 8<<20) + `"}]}`)
	b.ReportAllocs()
	b.SetBytes(int64(len(body)))
	b.ResetTimer()
	for b.Loop() {
		benchmarkSanitizeOpenAIResponsesReasoningOutput = sanitizeOpenAIResponsesReasoningEncryptedContent(context.Background(), "benchmark", body)
	}
}
