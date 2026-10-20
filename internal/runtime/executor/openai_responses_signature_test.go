package executor

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"

	internalcache "github.com/router-for-me/CLIProxyAPI/v8/internal/cache"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
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

// validMetaReasoningEncryptedContentForTest builds a shape-valid synthetic
// Meta envelope: "Q-PaDgE" pins the observed five-byte header, and 1426
// unpadded base64url chars decode to 45 + 1024 bytes. No captured signature
// is embedded in tests.
func validMetaReasoningEncryptedContentForTest() string {
	return "Q-PaDgE" + strings.Repeat("A", 1419)
}

// routedMetaReasoningEncryptedContentForTest appends a synthetic
// OpenRouter-style suffix: one '.' plus 67 alphanumeric characters.
func routedMetaReasoningEncryptedContentForTest() string {
	return validMetaReasoningEncryptedContentForTest() + "." + strings.Repeat("Ab3", 23)[:67]
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

func TestSanitizeOpenAIResponsesReasoningEncryptedContentWithCompat_PreservesUnknownFormatEncryptedContent(t *testing.T) {
	const unknownSig = "opaque-encrypted-reasoning-token-xyz"
	body := []byte(`{"store":false,"input":[{"type":"reasoning","summary":[],"content":null,"encrypted_content":"` + unknownSig + `"},{"type":"reasoning","summary":[],"content":null,"encrypted_content":""},{"type":"reasoning","summary":[],"content":null,"encrypted_content":"` + testClaudeCAISSample + `"}]}`)

	got := sanitizeOpenAIResponsesReasoningEncryptedContentWithCompat(context.Background(), "test", body, true)
	if gotSig := gjson.GetBytes(got, "input.0.encrypted_content").String(); gotSig != unknownSig {
		t.Fatalf("compat encrypted_content = %q, want %q; body=%s", gotSig, unknownSig, got)
	}
	if gjson.GetBytes(got, "input.1.encrypted_content").Exists() {
		t.Fatalf("empty encrypted_content should still be stripped: %s", got)
	}
	if gjson.GetBytes(got, "input.2.encrypted_content").Exists() {
		t.Fatalf("Claude signature should still be stripped: %s", got)
	}

	got = sanitizeOpenAIResponsesReasoningEncryptedContent(context.Background(), "test", body)
	if gjson.GetBytes(got, "input.0.encrypted_content").Exists() {
		t.Fatalf("non-compat sanitization unexpectedly preserved unknown-format encrypted_content: %s", got)
	}
}

func TestSanitizeOpenAIResponsesReasoningEncryptedContentKeepForeign_Issue6450(t *testing.T) {
	const museSig = "Q-PaDg-mock-muse-spark-encrypted-reasoning-payload"
	body := []byte(`{"store":false,"input":[` +
		`{"id":"rs_muse","type":"reasoning","summary":[],"content":[{"type":"reasoning_text","text":"cleartext thinking"}],"encrypted_content":"` + museSig + `"},` +
		`{"id":"rs_claude","type":"reasoning","summary":[],"content":null,"encrypted_content":"` + testClaudeCAISSample + `"},` +
		`{"id":"rs_empty","type":"reasoning","summary":[],"content":null,"encrypted_content":""},` +
		`{"id":"rs_orphan","type":"reasoning","summary":[]}` +
		`]}`)

	got := sanitizeOpenAIResponsesReasoningEncryptedContentKeepForeign(context.Background(), "test", body)

	// rs_muse: unknown format signature preserved, cleartext content moved to summary, id kept
	assertEmptyReasoningContent(t, got, "input.0.content")
	if gotSig := gjson.GetBytes(got, "input.0.encrypted_content").String(); gotSig != museSig {
		t.Fatalf("keepForeign encrypted_content = %q, want %q; body=%s", gotSig, museSig, got)
	}
	if gotID := gjson.GetBytes(got, "input.0.id").String(); gotID != "rs_muse" {
		t.Fatalf("keepForeign reasoning id = %q, want rs_muse; body=%s", gotID, got)
	}
	if gotSummary := gjson.GetBytes(got, "input.0.summary.0.text").String(); gotSummary != "cleartext thinking" {
		t.Fatalf("keepForeign summary = %q, want 'cleartext thinking'; body=%s", gotSummary, got)
	}

	// rs_claude: known foreign signature stripped, id dropped when store=false
	if gjson.GetBytes(got, "input.1.encrypted_content").Exists() {
		t.Fatalf("Claude signature should be stripped: %s", got)
	}
	if gjson.GetBytes(got, "input.1.id").Exists() {
		t.Fatalf("Claude reasoning id should be dropped: %s", got)
	}

	// rs_empty: empty signature stripped, id dropped
	if gjson.GetBytes(got, "input.2.encrypted_content").Exists() {
		t.Fatalf("empty signature should be stripped: %s", got)
	}
	if gjson.GetBytes(got, "input.2.id").Exists() {
		t.Fatalf("empty reasoning id should be dropped: %s", got)
	}

	// rs_orphan: missing signature, id dropped when store=false
	if gjson.GetBytes(got, "input.3.id").Exists() {
		t.Fatalf("orphan reasoning id should be dropped: %s", got)
	}
}

func TestSanitizeOpenAIResponsesReasoningEncryptedContentForMeta_PreservesRecognizedMuse(t *testing.T) {
	// Synthetic shape-valid Meta envelope: "Q-PaDgE" pins the observed
	// five-byte header, and 1426 unpadded base64url chars decode to
	// 45 + 1024 bytes. No captured signature is embedded in tests.
	metaSig := "Q-PaDgE" + strings.Repeat("A", 1419)
	const unknownSig = "Q-PaDg-mock-muse-spark-encrypted-reasoning-payload"
	body := []byte(`{"store":false,"input":[` +
		`{"id":"rs_meta","type":"reasoning","summary":[],"content":null,"encrypted_content":"` + metaSig + `"},` +
		`{"id":"rs_unknown","type":"reasoning","summary":[],"content":null,"encrypted_content":"` + unknownSig + `"},` +
		`{"id":"rs_claude","type":"reasoning","summary":[],"content":null,"encrypted_content":"` + testClaudeCAISSample + `"} ` +
		`]}`)

	got := sanitizeOpenAIResponsesReasoningEncryptedContentForMeta(context.Background(), "test", body)

	// The Meta target is native-only: the recognized Meta envelope survives,
	// while unknown-format and known-foreign values are both stripped so no
	// wrong signature can reach the Meta upstream.
	if gotSig := gjson.GetBytes(got, "input.0.encrypted_content").String(); gotSig != metaSig {
		t.Fatalf("recognized Meta encrypted_content not preserved; body=%s", got)
	}
	if gjson.GetBytes(got, "input.1.encrypted_content").Exists() {
		t.Fatalf("unknown-format encrypted_content should be stripped on Meta target: %s", got)
	}
	if gjson.GetBytes(got, "input.2.encrypted_content").Exists() {
		t.Fatalf("Claude signature should be stripped on Meta target: %s", got)
	}

	// The generic keepForeign path has no Meta allowance: once Muse is a
	// positively recognized family, its envelope is known-foreign there and
	// must be stripped instead of riding the unknown-format exemption.
	generic := sanitizeOpenAIResponsesReasoningEncryptedContentKeepForeign(context.Background(), "test", body)
	if gjson.GetBytes(generic, "input.0.encrypted_content").Exists() {
		t.Fatalf("recognized Meta signature should be stripped by generic keepForeign: %s", generic)
	}
	if gotSig := gjson.GetBytes(generic, "input.1.encrypted_content").String(); gotSig != unknownSig {
		t.Fatalf("unknown-format encrypted_content should remain preserved by generic keepForeign; body=%s", generic)
	}

	// The compat path preserves a recognized Meta envelope only when its
	// target model is itself a Meta-family model (cascaded/third-party
	// compat endpoints serving muse-*); other compat targets strip it.
	compatMeta := sanitizeOpenAIResponsesReasoningEncryptedContentWithCompatModel(context.Background(), "test", body, true, "muse-spark-1.3")
	if gotSig := gjson.GetBytes(compatMeta, "input.0.encrypted_content").String(); gotSig != metaSig {
		t.Fatalf("compat Meta-model target should preserve recognized Meta signature; body=%s", compatMeta)
	}
	if gjson.GetBytes(compatMeta, "input.1.encrypted_content").Exists() {
		t.Fatalf("compat Meta-model target should strip unknown-format signature: %s", compatMeta)
	}
	compatOther := sanitizeOpenAIResponsesReasoningEncryptedContentWithCompatModel(context.Background(), "test", body, true, "deepseek-v4")
	if gjson.GetBytes(compatOther, "input.0.encrypted_content").Exists() {
		t.Fatalf("compat non-Meta target should strip recognized Meta signature: %s", compatOther)
	}
}

func TestSanitizeOpenAIResponsesReasoningEncryptedContentForMeta_StripsRouterSuffixed(t *testing.T) {
	native := validMetaReasoningEncryptedContentForTest()
	routed := routedMetaReasoningEncryptedContentForTest()
	body := []byte(`{"store":false,"input":[` +
		`{"type":"reasoning","summary":[],"encrypted_content":"` + native + `"},` +
		`{"type":"reasoning","summary":[],"encrypted_content":"` + routed + `"}` +
		`]}`)

	got := sanitizeOpenAIResponsesReasoningEncryptedContentForMeta(context.Background(), "test", body)

	// The native Meta upstream is fail-closed on the router suffix: the
	// unsuffixed envelope survives while the routed form is stripped.
	if gotSig := gjson.GetBytes(got, "input.0.encrypted_content").String(); gotSig != native {
		t.Fatalf("native Meta encrypted_content not preserved; body=%s", got)
	}
	if gjson.GetBytes(got, "input.1.encrypted_content").Exists() {
		t.Fatalf("router-suffixed Meta encrypted_content should be stripped on native Meta target: %s", got)
	}
}

func TestSanitizeOpenAIResponsesReasoningEncryptedContentWithCompatModel_RoutedMetaTarget(t *testing.T) {
	native := validMetaReasoningEncryptedContentForTest()
	routed := routedMetaReasoningEncryptedContentForTest()
	gptSig := validOpenAIResponsesReasoningEncryptedContentForTest()
	const unknownSig = "opaque-encrypted-reasoning-token-xyz"
	buildBody := func() []byte {
		return []byte(`{"store":false,"input":[` +
			`{"type":"reasoning","summary":[],"encrypted_content":"` + native + `"},` +
			`{"type":"reasoning","summary":[],"encrypted_content":"` + routed + `"},` +
			`{"type":"reasoning","summary":[],"encrypted_content":"` + gptSig + `"},` +
			`{"type":"reasoning","summary":[],"encrypted_content":"` + unknownSig + `"},` +
			`{"type":"reasoning","summary":[],"encrypted_content":"` + testClaudeCAISSample + `"}` +
			`]}`)
	}
	assertRoutedTarget := func(t *testing.T, got []byte) {
		t.Helper()
		if gotSig := gjson.GetBytes(got, "input.0.encrypted_content").String(); gotSig != native {
			t.Fatalf("native Meta encrypted_content not preserved; body=%s", got)
		}
		if gotSig := gjson.GetBytes(got, "input.1.encrypted_content").String(); gotSig != routed {
			t.Fatalf("routed Meta encrypted_content = %q, want verbatim; body=%s", gotSig, got)
		}
		for index, item := range gjson.GetBytes(got, "input").Array() {
			if index < 2 {
				continue
			}
			if item.Get("encrypted_content").Exists() {
				t.Fatalf("input[%d] foreign encrypted_content should be stripped on Meta target; body=%s", index, got)
			}
		}
	}

	// A Meta-family model name on an OpenRouter baseURL preserves routed
	// envelopes verbatim regardless of the compat flag, while non-OpenRouter
	// third-party baseURLs strip OpenRouter's private suffix down to the
	// native Meta envelope.
	for _, isCompat := range []bool{false, true} {
		got := sanitizeOpenAIResponsesReasoningEncryptedContentWithCompatModelAndBaseURL(context.Background(), "test", buildBody(), isCompat, "meta/muse-wonder", "https://openrouter.ai/api/v1")
		assertRoutedTarget(t, got)
	}

	// Namespaced router model IDs resolve to their Meta tail segment.
	got := sanitizeOpenAIResponsesReasoningEncryptedContentWithCompatModelAndBaseURL(context.Background(), "test", buildBody(), false, "openrouter/meta/muse-wonder", "https://openrouter.ai/api/v1")
	assertRoutedTarget(t, got)

	otherProviderMeta := sanitizeOpenAIResponsesReasoningEncryptedContentWithCompatModelAndBaseURL(context.Background(), "test", buildBody(), false, "meta/muse-wonder", "https://example-proxy.internal/v1")
	if gotSig := gjson.GetBytes(otherProviderMeta, "input.1.encrypted_content").String(); gotSig != native {
		t.Fatalf("non-OpenRouter provider should strip OpenRouter suffix to native Meta envelope, got %q want %q", gotSig, native)
	}

	// OpenRouter Meta executor baseURL preserves routed Meta envelopes while
	// native api.meta.ai drops router-suffixed envelopes completely.
	routedMetaURL := sanitizeOpenAIResponsesReasoningEncryptedContentForMetaBaseURL(context.Background(), "test", buildBody(), "https://openrouter.ai/api/v1")
	assertRoutedTarget(t, routedMetaURL)

	nativeMetaURL := sanitizeOpenAIResponsesReasoningEncryptedContentForMetaBaseURL(context.Background(), "test", buildBody(), "https://api.meta.ai/api/v1")
	if gotSig := gjson.GetBytes(nativeMetaURL, "input.0.encrypted_content").String(); gotSig != native {
		t.Fatalf("native Meta URL should preserve native envelope; body=%s", nativeMetaURL)
	}
	if gjson.GetBytes(nativeMetaURL, "input.1.encrypted_content").Exists() {
		t.Fatalf("native Meta URL should strip router-suffixed envelope; body=%s", nativeMetaURL)
	}
}

func TestSanitizeOpenAIResponsesReasoningEncryptedContentWithCompatModel_StripsMetaForNonMetaTargets(t *testing.T) {
	native := validMetaReasoningEncryptedContentForTest()
	routed := routedMetaReasoningEncryptedContentForTest()
	body := []byte(`{"store":false,"input":[` +
		`{"type":"reasoning","summary":[],"encrypted_content":"` + native + `"},` +
		`{"type":"reasoning","summary":[],"encrypted_content":"` + routed + `"}` +
		`]}`)

	for _, tc := range []struct {
		model    string
		isCompat bool
	}{
		{"gpt-5.4", false},
		{"deepseek-v4", true},
	} {
		got := sanitizeOpenAIResponsesReasoningEncryptedContentWithCompatModel(context.Background(), "test", body, tc.isCompat, tc.model)
		for index, item := range gjson.GetBytes(got, "input").Array() {
			if item.Get("encrypted_content").Exists() {
				t.Fatalf("model=%s isCompat=%v: input[%d] Meta encrypted_content should be stripped; body=%s", tc.model, tc.isCompat, index, got)
			}
		}
	}
}

func TestSanitizeOpenAIResponsesReasoningEncryptedContent_StripsRouterSuffixOnNonOpenRouterUpstreams(t *testing.T) {
	gptNative := validOpenAIResponsesReasoningEncryptedContentForTest()
	suffix := strings.Repeat("Ab3", 27)[:79]
	gptRouted := gptNative + "." + suffix

	body := []byte(`{"store":false,"input":[{"type":"reasoning","summary":[],"encrypted_content":"` + gptRouted + `"}]}`)

	// Official Codex / non-OpenRouter upstreams must strip the private OpenRouter suffix.
	gotOfficial := sanitizeOpenAIResponsesReasoningEncryptedContentWithCompatModelAndBaseURL(
		context.Background(),
		"test",
		body,
		false,
		"gpt-5.4",
		"https://chatgpt.com/backend-api/codex",
	)
	if gotSig := gjson.GetBytes(gotOfficial, "input.0.encrypted_content").String(); gotSig != gptNative {
		t.Fatalf("official Codex encrypted_content = %q, want stripped native %q", gotSig, gptNative)
	}

	// OpenRouter upstream preserves its router suffix verbatim.
	gotOpenRouter := sanitizeOpenAIResponsesReasoningEncryptedContentWithCompatModelAndBaseURL(
		context.Background(),
		"test",
		body,
		false,
		"openrouter/openai/o4-mini",
		"https://openrouter.ai/api/v1",
	)
	if gotSig := gjson.GetBytes(gotOpenRouter, "input.0.encrypted_content").String(); gotSig != gptRouted {
		t.Fatalf("OpenRouter encrypted_content = %q, want verbatim %q", gotSig, gptRouted)
	}

	// Foreign GPT encrypted_content must be stripped when targeting a non-GPT family (e.g. Grok or Gemini on OpenRouter).
	for _, model := range []string{"openrouter/x-ai/grok-4.7", "openrouter/google/gemini-3.8-flash"} {
		gotForeign := sanitizeOpenAIResponsesReasoningEncryptedContentWithCompatModelAndBaseURL(
			context.Background(),
			"test",
			body,
			true,
			model,
			"https://openrouter.ai/api/v1",
		)
		if gjson.GetBytes(gotForeign, "input.0.encrypted_content").Exists() {
			t.Fatalf("model=%s: foreign GPT encrypted_content should be stripped; body=%s", model, gotForeign)
		}
	}

	// Cache-prefixed compatible signatures (e.g. "swe#sealed.v1.sig" or "gpt#...")
	// must be rewritten to their normalized unprefixed form in encrypted_content.
	prefixedSWEBody := []byte(`{"store":false,"input":[{"type":"reasoning","summary":[],"encrypted_content":"swe#sealed.v1.token"}]}`)
	gotSWE := sanitizeOpenAIResponsesReasoningEncryptedContentWithCompatModelAndBaseURL(
		context.Background(),
		"test",
		prefixedSWEBody,
		true,
		"devin/swe-2",
		"https://openrouter.ai/api/v1",
	)
	if gotSig := gjson.GetBytes(gotSWE, "input.0.encrypted_content").String(); gotSig != "sealed.v1.token" {
		t.Fatalf("prefixed SWE encrypted_content = %q, want normalized %q", gotSig, "sealed.v1.token")
	}

	// Gemini carrier wrapping a Meta signature must never be misclassified as a
	// Devin sealed signature.
	metaCarrier := base64.StdEncoding.EncodeToString([]byte("cpa-codex-gemini-carrier-v1:Q-PaDgE" + strings.Repeat("A", 1419)))
	if sigBytes, sigType := parseSignatureBytes(metaCarrier); len(sigBytes) != 0 || sigType != "" {
		t.Fatalf("parseSignatureBytes(metaCarrier) = (%q, %q), want empty", sigBytes, sigType)
	}

	// Replay cache backfill + channel switch: a routed signature cached from
	// OpenRouter is preserved on OpenRouter and stripped to native on official Codex.
	internalcache.ClearCodexReasoningReplayCache()
	t.Cleanup(internalcache.ClearCodexReasoningReplayCache)
	const sessionKey = "execution:test-channel-switch"
	internalcache.CacheCodexReasoningReplayItem("o4-mini", sessionKey, []byte(`{"type":"reasoning","summary":[],"encrypted_content":"`+gptRouted+`"}`))
	replayReq := cliproxyexecutor.Request{
		Model:   "o4-mini",
		Payload: []byte(`{"model":"o4-mini","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"next"}]}]}`),
	}
	replayOpts := cliproxyexecutor.Options{
		SourceFormat: "claude",
		Metadata: map[string]any{
			cliproxyexecutor.ExecutionSessionMetadataKey: "test-channel-switch",
		},
	}
	injected, _, errReplay := applyCodexReasoningReplayCacheRequired(context.Background(), "claude", replayReq, replayOpts, replayReq.Payload)
	if errReplay != nil {
		t.Fatalf("applyCodexReasoningReplayCacheRequired error = %v", errReplay)
	}
	sanitizedOpenRouter := sanitizeOpenAIResponsesReasoningEncryptedContentWithCompatModelAndBaseURL(
		context.Background(), "test", injected, false, "o4-mini", "https://openrouter.ai/api/v1",
	)
	if gotSig := gjson.GetBytes(sanitizedOpenRouter, "input.0.encrypted_content").String(); gotSig != gptRouted {
		t.Fatalf("replay cache -> OpenRouter encrypted_content = %q, want routed %q", gotSig, gptRouted)
	}
	sanitizedOfficial := sanitizeOpenAIResponsesReasoningEncryptedContentWithCompatModelAndBaseURL(
		context.Background(), "test", injected, false, "o4-mini", "https://chatgpt.com/backend-api/codex",
	)
	if gotSig := gjson.GetBytes(sanitizedOfficial, "input.0.encrypted_content").String(); gotSig != gptNative {
		t.Fatalf("replay cache -> official Codex encrypted_content = %q, want stripped native %q", gotSig, gptNative)
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
