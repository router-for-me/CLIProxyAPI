package signature

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
	"google.golang.org/protobuf/encoding/protowire"
)

func testClaudeThinkingSignature() string {
	channelBlock := []byte{}
	channelBlock = protowire.AppendTag(channelBlock, 1, protowire.VarintType)
	channelBlock = protowire.AppendVarint(channelBlock, 12)
	channelBlock = protowire.AppendTag(channelBlock, 2, protowire.VarintType)
	channelBlock = protowire.AppendVarint(channelBlock, 2)
	channelBlock = protowire.AppendTag(channelBlock, 6, protowire.BytesType)
	channelBlock = protowire.AppendString(channelBlock, "claude-sonnet-4-6")

	container := []byte{}
	container = protowire.AppendTag(container, 1, protowire.BytesType)
	container = protowire.AppendBytes(container, channelBlock)

	payload := []byte{}
	payload = protowire.AppendTag(payload, 2, protowire.BytesType)
	payload = protowire.AppendBytes(payload, container)
	payload = protowire.AppendTag(payload, 3, protowire.VarintType)
	payload = protowire.AppendVarint(payload, 1)
	return base64.StdEncoding.EncodeToString(payload)
}

// TestBase64AlphabetSet_MatchesEncoderAlphabets pins the charset lookup tables
// against the encoders they stand in for. A wrong table would silently accept
// bytes that are not valid base64, or reject a legal payload character.
func TestBase64AlphabetSet_MatchesEncoderAlphabets(t *testing.T) {
	cases := []struct {
		name     string
		set      [256]bool
		alphabet string
	}{
		{"grok unpadded std", grokEncryptedContentCharSet, "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"},
		{"gpt base64url", gptReasoningSignatureCharSet, "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_="},
	}
	for _, tc := range cases {
		allowed := map[byte]bool{}
		for i := 0; i < len(tc.alphabet); i++ {
			allowed[tc.alphabet[i]] = true
		}
		for c := 0; c < 256; c++ {
			want := allowed[byte(c)]
			if got := tc.set[c]; got != want {
				t.Errorf("%s: byte 0x%02x (%q) accepted=%v, want %v", tc.name, c, string(rune(c)), got, want)
			}
		}
	}
}

// replaySafeEnvelopeFixtures returns one fixture per self-describing provider
// envelope that carries replayable state. Every entry must survive the structural
// pre-filter, because losing one would silently reclassify that provider.
func replaySafeEnvelopeFixtures() map[string]struct {
	sig  string
	want SignatureProvider
} {
	return map[string]struct {
		sig  string
		want SignatureProvider
	}{
		"claude single-layer E":  {testClaudeThinkingSignature(), SignatureProviderClaude},
		"claude double-layer R":  {testUnpaddedAntigravityClaudeThinkingSignature(), SignatureProviderClaude},
		"claude CAIS":            {testClaudeCAISSignature("claude-fable-5"), SignatureProviderClaude},
		"gemini protobuf field2": {testGeminiThoughtSignatureEnvelope(), SignatureProviderGemini},
		"gpt fernet":             {testGPTReasoningSignature(), SignatureProviderGPT},
	}
}

// TestSelfDescribingSignatureFirstChars_CoversEveryKnownEnvelope guards the
// structural pre-filter. DetectSignatureProviderForBlock skips every provider
// validator when maybeSelfDescribingSignatureEnvelope returns false, so an
// envelope missing from selfDescribingSignatureFirstChars would silently fall
// through to the residual class. Adding a provider envelope without registering
// its base64 first character fails here.
func TestSelfDescribingSignatureFirstChars_CoversEveryKnownEnvelope(t *testing.T) {
	for name, fixture := range replaySafeEnvelopeFixtures() {
		if !maybeSelfDescribingSignatureEnvelope(fixture.sig) {
			t.Errorf("%s: first char %q is not in selfDescribingSignatureFirstChars %q; register it or detection will skip this envelope",
				name, string(fixture.sig[0]), selfDescribingSignatureFirstChars)
		}
	}

	// The pre-filter must not be so wide that it stops filtering. Opaque xAI
	// ciphertext is the shape it exists to reject.
	for _, sig := range []string{
		"K1ZAIbzDbO",
		"jQDLUr+fD8RFP8nbkkfI",
		"qcgG7jzxH3D6mlVLBBaKXaG3",
	} {
		if maybeSelfDescribingSignatureEnvelope(sig) {
			t.Errorf("opaque ciphertext %q must not look like a self-describing envelope", sig)
		}
	}
}

// TestGeminiASCIIUUIDIsGateIndependent documents why ascii_uuid is excluded from
// selfDescribingSignatureFirstChars. Its first byte is the first hex character of
// the UUID, so the base64 first character spreads over several values, and none of
// them need to be registered: the envelope is never replay-safe, so it resolves to
// SignatureProviderUnknown either way and Gemini model parts recover it through the
// bypass sentinel keyed on block kind.
func TestGeminiASCIIUUIDIsGateIndependent(t *testing.T) {
	// First hex digit chosen to land on distinct base64 first characters.
	for _, uuid := range []string{
		"09743975-4bb0-4936-9e28-d5b0d21bdc48",
		"49743975-4bb0-4936-9e28-d5b0d21bdc48",
		"89743975-4bb0-4936-9e28-d5b0d21bdc48",
		"a9743975-4bb0-4936-9e28-d5b0d21bdc48",
		"e9743975-4bb0-4936-9e28-d5b0d21bdc48",
	} {
		sig := testGeminiThoughtSignature([]byte(uuid))
		if got := DetectSignatureProvider(sig); got != SignatureProviderUnknown {
			t.Errorf("uuid %q: DetectSignatureProvider = %q, want %q regardless of the pre-filter",
				uuid[:8], got, SignatureProviderUnknown)
		}
		decision := DecideSignatureCompatibility(SignatureProviderGemini, sig, SignatureBlockKindGeminiFunctionCall)
		if decision.Action != SignatureActionReplaceWithGeminiBypass {
			t.Errorf("uuid %q: action = %q, want %q", uuid[:8], decision.Action, SignatureActionReplaceWithGeminiBypass)
		}
	}
}

// TestDetectSignatureProviderForBlock_ClassifiesEveryKnownEnvelope pins the
// classification of each envelope so a reordering of the validator chain cannot
// silently reassign one provider's signatures to another.
func TestDetectSignatureProviderForBlock_ClassifiesEveryKnownEnvelope(t *testing.T) {
	for name, fixture := range replaySafeEnvelopeFixtures() {
		if got := DetectSignatureProvider(fixture.sig); got != fixture.want {
			t.Errorf("%s: DetectSignatureProvider = %q, want %q", name, got, fixture.want)
		}
	}
}

// TestGeminiEnvelopeNeverClaimsClaudeSignatures pins the invariant that keeps
// Claude and Gemini separable independently of probe order in
// DetectSignatureProviderForBlock. Gemini validates wire shape only and has no
// literal marker, so it is the weakest judge; Claude envelopes survive it solely
// because they carry extra top-level fields beyond the container and therefore
// fail Gemini's single-record shape. Loosening the Gemini envelope check would
// make probe order start mattering, and fails here first.
func TestGeminiEnvelopeNeverClaimsClaudeSignatures(t *testing.T) {
	for name, sig := range map[string]string{
		"single-layer E":        testClaudeThinkingSignature(),
		"single-layer E opaque": testClaudeThinkingSignatureWithOpaqueLen(64),
		"double-layer R":        testUnpaddedAntigravityClaudeThinkingSignature(),
		"CAIS synthetic":        testClaudeCAISSignature("claude-opus-5"),
		"CAIS observed":         observedFable5Sample,
	} {
		if isRecognizedGeminiProviderSignature(sig, SignatureBlockKindUnknown) {
			t.Errorf("claude %s is claimed by the Gemini envelope check; probe order in DetectSignatureProviderForBlock is now load-bearing", name)
		}
		if got := DetectSignatureProvider(sig); got != SignatureProviderClaude {
			t.Errorf("claude %s: DetectSignatureProvider = %q, want %q", name, got, SignatureProviderClaude)
		}
		if _, ok := CompatibleSignatureForProvider(SignatureProviderGemini, sig); ok {
			t.Errorf("claude %s must not be replayable as a Gemini signature", name)
		}
	}
}

func TestDetectSignatureProvider_UsesProviderPrefix(t *testing.T) {
	claudeSig := "claude#" + testClaudeThinkingSignature()
	if got := DetectSignatureProvider(claudeSig); got != SignatureProviderClaude {
		t.Fatalf("DetectSignatureProvider(claude#...) = %q, want %q", got, SignatureProviderClaude)
	}

	geminiSig := "gemini#" + testGemini3ThoughtSignature([]byte{0x01, 0x0c, 0x39})
	if got := DetectSignatureProvider(geminiSig); got != SignatureProviderGemini {
		t.Fatalf("DetectSignatureProvider(gemini#...) = %q, want %q", got, SignatureProviderGemini)
	}
}

func TestDetectSignatureProvider_RejectsMisleadingClaudePrefix(t *testing.T) {
	mislabeledGeminiSig := "claude#" + testGemini3ThoughtSignature([]byte{0x01, 0x0c, 0x39})
	if got := DetectSignatureProvider(mislabeledGeminiSig); got != SignatureProviderUnknown {
		t.Fatalf("DetectSignatureProvider(mislabeled claude#Gemini) = %q, want %q", got, SignatureProviderUnknown)
	}
}

func TestDetectSignatureProvider_Gemini3EPrefixDoesNotLookClaude(t *testing.T) {
	// This byte shape base64-encodes with an E prefix but is a Gemini field-2
	// envelope, not a Claude thinking-signature tree.
	geminiSig := testGemini3ThoughtSignature([]byte{0x01, 0x0c, 0x39, 0xd6, 0xc7, 0x34})
	if !strings.HasPrefix(geminiSig, "E") {
		t.Fatalf("test signature should start with E, got %q", geminiSig[:1])
	}
	if got := DetectSignatureProvider(geminiSig); got != SignatureProviderGemini {
		t.Fatalf("DetectSignatureProvider(Gemini E-prefix) = %q, want %q", got, SignatureProviderGemini)
	}
}

func TestCompatibleSignatureForProvider_ClaudeUsesProviderNativeEForm(t *testing.T) {
	nativeSig := testClaudeThinkingSignature()
	doubleEncoded := base64.StdEncoding.EncodeToString([]byte(nativeSig))

	normalized, ok := CompatibleSignatureForProvider(SignatureProviderClaude, doubleEncoded)
	if !ok {
		t.Fatal("double-layer Claude signature should be compatible")
	}
	if normalized != nativeSig {
		t.Fatalf("CompatibleSignatureForProvider(Claude) = %q, want provider-native %q", normalized, nativeSig)
	}
}

func TestCompatibleAntigravityClaudeThinkingSignature_UsesDoubleLayerRForm(t *testing.T) {
	nativeSig := testClaudeThinkingSignature()
	expected := base64.StdEncoding.EncodeToString([]byte(nativeSig))

	normalized, ok := CompatibleAntigravityClaudeThinkingSignature(nativeSig)
	if !ok {
		t.Fatal("Claude signature should be compatible with Antigravity Claude")
	}
	if normalized != expected {
		t.Fatalf("CompatibleAntigravityClaudeThinkingSignature = %q, want %q", normalized, expected)
	}
}

func TestCompatibleAntigravityClaudeThinkingSignature_RejectsGeminiEPrefix(t *testing.T) {
	geminiSig := testGemini3ThoughtSignature([]byte{0x01, 0x0c, 0x39, 0xd6, 0xc7, 0x34})
	if !strings.HasPrefix(geminiSig, "E") {
		t.Fatalf("test signature should start with E, got %q", geminiSig[:1])
	}
	if normalized, ok := CompatibleAntigravityClaudeThinkingSignature(geminiSig); ok || normalized != "" {
		t.Fatalf("Gemini E-prefix signature normalized=%q ok=%v, want rejected", normalized, ok)
	}
}

func TestDetectSignatureProvider_DoesNotClassifyArbitraryBase64AsGemini(t *testing.T) {
	opaque := testGeminiThoughtSignature([]byte{0x45, 0x12})
	if got := DetectSignatureProvider(opaque); got != SignatureProviderUnknown {
		t.Fatalf("DetectSignatureProvider(arbitrary base64) = %q, want %q", got, SignatureProviderUnknown)
	}
}

func TestGeminiASCIIUUIDSignatureUsesBypass(t *testing.T) {
	plainUUID := "e24830a7-5cd6-42fe-998b-ee539e72b9c3"
	sig := testGeminiThoughtSignature([]byte(plainUUID))

	if got := DetectSignatureProvider(plainUUID); got != SignatureProviderUnknown {
		t.Fatalf("DetectSignatureProvider(plain UUID) = %q, want %q", got, SignatureProviderUnknown)
	}
	if got := DetectSignatureProvider("gemini#" + plainUUID); got != SignatureProviderUnknown {
		t.Fatalf("DetectSignatureProvider(gemini#plain UUID) = %q, want %q", got, SignatureProviderUnknown)
	}

	if got := DetectSignatureProvider(sig); got != SignatureProviderUnknown {
		t.Fatalf("DetectSignatureProvider(UUID) = %q, want %q", got, SignatureProviderUnknown)
	}
	if got := DetectSignatureProvider("gemini#" + sig); got != SignatureProviderUnknown {
		t.Fatalf("DetectSignatureProvider(gemini#UUID) = %q, want %q", got, SignatureProviderUnknown)
	}
	if got := DetectSignatureProviderForBlock(sig, SignatureBlockKindGeminiFunctionCall); got != SignatureProviderUnknown {
		t.Fatalf("DetectSignatureProviderForBlock(UUID tool call) = %q, want %q", got, SignatureProviderUnknown)
	}
	if _, ok := CompatibleSignatureForProvider(SignatureProviderGemini, sig); ok {
		t.Fatal("UUID signature should not be compatible")
	}
	if normalized, ok := CompatibleSignatureForProviderBlock(SignatureProviderGemini, sig, SignatureBlockKindGeminiFunctionCall); ok || normalized != "" {
		t.Fatalf("UUID tool-call signature normalized=%q ok=%v, want empty and false", normalized, ok)
	}
	decision := DecideSignatureCompatibility(SignatureProviderGemini, sig, SignatureBlockKindGeminiFunctionCall)
	if decision.Action != SignatureActionReplaceWithGeminiBypass {
		t.Fatalf("function-call UUID action = %q, want %q", decision.Action, SignatureActionReplaceWithGeminiBypass)
	}
	if decision.ReplacementSignature != GeminiSkipThoughtSignatureValidator {
		t.Fatalf("function-call UUID replacement = %q, want %q", decision.ReplacementSignature, GeminiSkipThoughtSignatureValidator)
	}
	decision = DecideSignatureCompatibility(SignatureProviderGemini, sig, SignatureBlockKindGeminiModelPart)
	if decision.Action != SignatureActionReplaceWithGeminiBypass {
		t.Fatalf("model-part UUID action = %q, want %q", decision.Action, SignatureActionReplaceWithGeminiBypass)
	}
}

func TestGeminiWrappedUUIDFunctionCallSignatureIsCompatible(t *testing.T) {
	sig := testGemini3ThoughtSignature([]byte("e24830a7-5cd6-42fe-998b-ee539e72b9c3"))

	if got := DetectSignatureProvider(sig); got != SignatureProviderGemini {
		t.Fatalf("DetectSignatureProvider(wrapped UUID) = %q, want %q", got, SignatureProviderGemini)
	}
	if got := DetectSignatureProviderForBlock(sig, SignatureBlockKindGeminiFunctionCall); got != SignatureProviderGemini {
		t.Fatalf("DetectSignatureProviderForBlock(wrapped UUID tool call) = %q, want %q", got, SignatureProviderGemini)
	}
	if normalized, ok := CompatibleSignatureForProviderBlock(SignatureProviderGemini, sig, SignatureBlockKindGeminiFunctionCall); !ok || normalized != sig {
		t.Fatalf("wrapped UUID tool-call signature normalized=%q ok=%v, want original and true", normalized, ok)
	}
	for _, blockKind := range []SignatureBlockKind{SignatureBlockKindGeminiFunctionCall, SignatureBlockKindGeminiModelPart} {
		decision := DecideSignatureCompatibility(SignatureProviderGemini, sig, blockKind)
		if !decision.Compatible || decision.Action != SignatureActionPreserve || decision.NormalizedSignature != sig {
			t.Fatalf("wrapped UUID decision for %s = %+v, want preserved", blockKind, decision)
		}
	}
}

func TestCompatibleSignatureForProvider_StripsGeminiPrefix(t *testing.T) {
	sig := testGemini3ThoughtSignature([]byte{0x01, 0x0c, 0x39})
	normalized, ok := CompatibleSignatureForProvider(SignatureProviderGemini, "gemini#"+sig)
	if !ok {
		t.Fatal("gemini-prefixed signature should be compatible with Gemini")
	}
	if normalized != sig {
		t.Fatalf("normalized = %q, want %q", normalized, sig)
	}
}

func TestSplitSignatureProviderPrefix_UsesStrictProviderAliases(t *testing.T) {
	gptSig := "gpt#" + testGPTReasoningSignature()
	if got := DetectSignatureProvider(gptSig); got != SignatureProviderGPT {
		t.Fatalf("DetectSignatureProvider(gpt#...) = %q, want %q", got, SignatureProviderGPT)
	}

	mislabeledPrefix := "claude-cache#" + testClaudeThinkingSignature()
	if _, _, ok := SplitSignatureProviderPrefix(mislabeledPrefix); ok {
		t.Fatal("claude-cache# should not be accepted as an explicit provider prefix")
	}
	if got := DetectSignatureProvider(mislabeledPrefix); got != SignatureProviderUnknown {
		t.Fatalf("DetectSignatureProvider(claude-cache#...) = %q, want %q", got, SignatureProviderUnknown)
	}
}

func TestDecideSignatureCompatibility_GeminiFunctionCallUsesBypass(t *testing.T) {
	decision := DecideSignatureCompatibility(SignatureProviderGemini, "claude#"+testClaudeThinkingSignature(), SignatureBlockKindGeminiFunctionCall)
	if decision.Action != SignatureActionReplaceWithGeminiBypass {
		t.Fatalf("Action = %q, want %q", decision.Action, SignatureActionReplaceWithGeminiBypass)
	}
	if decision.ReplacementSignature != GeminiSkipThoughtSignatureValidator {
		t.Fatalf("ReplacementSignature = %q, want %q", decision.ReplacementSignature, GeminiSkipThoughtSignatureValidator)
	}
}

func TestSanitizeClaudeMessagesSignaturesForModel_NormalizesSameProviderClaude(t *testing.T) {
	nativeSig := testClaudeThinkingSignature()
	sig := "claude#" + nativeSig
	input := []byte(`{"model":"claude-sonnet","messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"keep","signature":"` + sig + `"},{"type":"text","text":"answer"}]}]}`)
	expectedSig, err := NormalizeClaudeProviderNativeThinkingSignature(nativeSig)
	if err != nil {
		t.Fatalf("NormalizeClaudeProviderNativeThinkingSignature failed: %v", err)
	}

	output, report := SanitizeClaudeMessagesSignaturesForModel(input, "claude-sonnet-4-5")
	if report.Preserved != 1 || report.DroppedBlocks != 0 {
		t.Fatalf("unexpected report: %+v", report)
	}
	if got := gjson.GetBytes(output, "messages.0.content.0.signature").String(); got != expectedSig {
		t.Fatalf("signature = %q, want normalized %q", got, expectedSig)
	}
}

func TestSanitizeClaudeMessagesSignaturesForModel_DropsClaudeThinkingForGemini(t *testing.T) {
	sig := "claude#" + testClaudeThinkingSignature()
	input := []byte(`{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"drop","signature":"` + sig + `"},{"type":"text","text":"answer"}]}]}`)

	output, report := SanitizeClaudeMessagesSignaturesForModel(input, "gemini-3.5-flash")
	if report.DroppedBlocks != 1 {
		t.Fatalf("DroppedBlocks = %d, want 1; report=%+v", report.DroppedBlocks, report)
	}
	content := gjson.GetBytes(output, "messages.0.content").Array()
	if len(content) != 1 {
		t.Fatalf("content length = %d, want 1: %s", len(content), output)
	}
	if got := content[0].Get("text").String(); got != "answer" {
		t.Fatalf("remaining text = %q, want answer", got)
	}
}

func TestSanitizeClaudeMessagesSignaturesForModel_PreservesGeminiThinkingForGemini(t *testing.T) {
	nativeSig := testGemini3ThoughtSignature([]byte{0x01, 0x0c, 0x39})
	sig := "gemini#" + nativeSig
	input := []byte(`{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"keep","signature":"` + sig + `"},{"type":"text","text":"answer"}]}]}`)

	output, report := SanitizeClaudeMessagesSignaturesForModel(input, "gemini-3.5-flash")
	if report.Preserved != 1 || report.DroppedBlocks != 0 {
		t.Fatalf("unexpected report: %+v", report)
	}
	if got := gjson.GetBytes(output, "messages.0.content.0.signature").String(); got != nativeSig {
		t.Fatalf("signature = %q, want normalized %q", got, nativeSig)
	}
}

func TestSanitizeClaudeMessagesSignaturesForModel_PreservesGPTForGPT(t *testing.T) {
	sig := testGPTReasoningSignature()
	input := []byte(`{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"keep","signature":"` + sig + `"},{"type":"text","text":"answer"}]}]}`)

	output, report := SanitizeClaudeMessagesSignaturesForModel(input, "gpt-5.2")
	if report.Preserved != 1 || report.DroppedBlocks != 0 {
		t.Fatalf("unexpected report: %+v", report)
	}
	if got := gjson.GetBytes(output, "messages.0.content.0.signature").String(); got != sig {
		t.Fatalf("signature = %q, want preserved %q", got, sig)
	}
}

func TestSanitizeClaudeMessagesSignaturesForModel_DropsEmptyAssistantMessage(t *testing.T) {
	sig := "claude#" + testClaudeThinkingSignature()
	input := []byte(`{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"drop","signature":"` + sig + `"}]},{"role":"user","content":[{"type":"text","text":"next"}]}]}`)

	output, report := SanitizeClaudeMessagesSignaturesForModel(input, "gpt-5.2")
	if report.DroppedBlocks != 1 {
		t.Fatalf("DroppedBlocks = %d, want 1", report.DroppedBlocks)
	}
	messages := gjson.GetBytes(output, "messages").Array()
	if len(messages) != 1 {
		t.Fatalf("messages length = %d, want 1: %s", len(messages), output)
	}
	if got := messages[0].Get("role").String(); got != "user" {
		t.Fatalf("remaining role = %q, want user", got)
	}
}

func TestSanitizeClaudeMessagesForClaudeUpstream_DropsInvalidThinkingAndCleansToolUse(t *testing.T) {
	input := []byte(`{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"drop me","signature":""},{"type":"text","text":"answer"},{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"git status"},"signature":"bad","thoughtSignature":"bad2","thought_signature":"bad3","model":"claude-sonnet-4-5","extra_content":{"google":{"thought_signature":"bad4"}}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"ok"}]}]}`)

	output, report := SanitizeClaudeMessagesForClaudeUpstream(input, "claude-sonnet-4-5")
	if report.DroppedBlocks != 1 {
		t.Fatalf("DroppedBlocks = %d, want 1; report=%+v", report.DroppedBlocks, report)
	}
	parts := gjson.GetBytes(output, "messages.0.content").Array()
	if len(parts) != 2 {
		t.Fatalf("content length = %d, want 2: %s", len(parts), output)
	}
	if parts[0].Get("type").String() != "text" {
		t.Fatalf("first remaining part = %s, want text", parts[0].Raw)
	}
	toolUse := parts[1]
	if toolUse.Get("type").String() != "tool_use" {
		t.Fatalf("second remaining part = %s, want tool_use", toolUse.Raw)
	}
	if got := toolUse.Get("id").String(); got != "toolu_1" {
		t.Fatalf("tool_use id = %q, want toolu_1", got)
	}
	for _, path := range []string{
		"signature",
		"thoughtSignature",
		"thought_signature",
		"model",
		"extra_content",
	} {
		if toolUse.Get(path).Exists() {
			t.Fatalf("tool_use.%s should be removed: %s", path, toolUse.Raw)
		}
	}
}

func TestSanitizeClaudeMessagesForClaudeUpstreamPreservesValidParallelToolHistory(t *testing.T) {
	input := []byte(`{"messages":[
		{"role":"assistant","content":[
			{"type":"text","text":"running tools"},
			{"type":"tool_use","id":"toolu_1","name":"Read","input":{"path":"a"}},
			{"type":"tool_use","id":"toolu_2","name":"Read","input":{"path":"b"}}
		]},
		{"role":"user","content":[
			{"type":"tool_result","tool_use_id":"toolu_2","content":"b"},
			{"type":"tool_result","tool_use_id":"toolu_1","content":"a"},
			{"type":"text","text":"continue"}
		]}
	]}`)

	output, report := SanitizeClaudeMessagesForClaudeUpstream(input, "claude-sonnet-4-5")
	if report.DroppedBlocks != 0 {
		t.Fatalf("DroppedBlocks = %d, want 0; report=%+v", report.DroppedBlocks, report)
	}
	if string(output) != string(input) {
		t.Fatalf("valid tool history changed:\n got: %s\nwant: %s", output, input)
	}
	if &output[0] != &input[0] {
		t.Fatal("valid tool history was copied")
	}
}

func TestSanitizeClaudeMessagesForClaudeUpstreamPairsAcrossConsecutiveSameRoleMessages(t *testing.T) {
	input := []byte(`{"messages":[
		{"role":"assistant","content":[
			{"type":"text","text":"running tools"},
			{"type":"tool_use","id":"toolu_1","name":"Read","input":{"path":"a"}}
		]},
		{"role":"assistant","content":[
			{"type":"tool_use","id":"toolu_2","name":"Read","input":{"path":"b"}}
		]},
		{"role":"user","content":[
			{"type":"tool_result","tool_use_id":"toolu_1","content":"a"}
		]},
		{"role":"user","content":[
			{"type":"tool_result","tool_use_id":"toolu_2","content":"b"},
			{"type":"text","text":"continue"}
		]}
	]}`)

	output, report := SanitizeClaudeMessagesForClaudeUpstream(input, "claude-sonnet-4-5")
	if report.DroppedBlocks != 0 {
		t.Fatalf("DroppedBlocks = %d, want 0; report=%+v", report.DroppedBlocks, report)
	}
	if string(output) != string(input) {
		t.Fatalf("valid split logical turn changed:\n got: %s\nwant: %s", output, input)
	}
	if &output[0] != &input[0] {
		t.Fatal("valid split logical turn was copied")
	}
}

func TestSanitizeClaudeMessagesForClaudeUpstreamStopsSplitResultsAfterText(t *testing.T) {
	input := []byte(`{"messages":[
		{"role":"assistant","content":[
			{"type":"text","text":"running"},
			{"type":"tool_use","id":"toolu_1","name":"Read","input":{"path":"a"}},
			{"type":"tool_use","id":"toolu_2","name":"Read","input":{"path":"b"}}
		]},
		{"role":"user","content":[
			{"type":"tool_result","tool_use_id":"toolu_1","content":"a"},
			{"type":"text","text":"tool two was cancelled"}
		]},
		{"role":"user","content":[
			{"type":"tool_result","tool_use_id":"toolu_2","content":"late"}
		]}
	]}`)

	output, report := SanitizeClaudeMessagesForClaudeUpstream(input, "claude-sonnet-4-5")
	if report.DroppedBlocks != 2 {
		t.Fatalf("DroppedBlocks = %d, want 2; report=%+v", report.DroppedBlocks, report)
	}
	messages := gjson.GetBytes(output, "messages").Array()
	if len(messages) != 2 {
		t.Fatalf("messages length = %d, want 2 after empty trailing user message removal: %s", len(messages), output)
	}
	assistantParts := messages[0].Get("content").Array()
	if len(assistantParts) != 2 || assistantParts[1].Get("id").String() != "toolu_1" {
		t.Fatalf("assistant content = %s, want text plus toolu_1", messages[0].Get("content").Raw)
	}
	userParts := messages[1].Get("content").Array()
	if len(userParts) != 2 || userParts[0].Get("tool_use_id").String() != "toolu_1" || userParts[1].Get("text").String() != "tool two was cancelled" {
		t.Fatalf("user content = %s, want toolu_1 result then text", messages[1].Get("content").Raw)
	}
}

func TestSanitizeClaudeMessagesForClaudeUpstreamRequiresExactToolUseIDs(t *testing.T) {
	input := []byte(`{"messages":[
		{"role":"assistant","content":[
			{"type":"text","text":"running"},
			{"type":"tool_use","id":" toolu_1 ","name":"Read","input":{"path":"a"}}
		]},
		{"role":"user","content":[
			{"type":"tool_result","tool_use_id":"toolu_1","content":"a"},
			{"type":"text","text":"continue"}
		]}
	]}`)

	output, report := SanitizeClaudeMessagesForClaudeUpstream(input, "claude-sonnet-4-5")
	if report.DroppedBlocks != 2 {
		t.Fatalf("DroppedBlocks = %d, want 2 for non-identical raw IDs; report=%+v", report.DroppedBlocks, report)
	}
	messages := gjson.GetBytes(output, "messages").Array()
	if len(messages) != 2 {
		t.Fatalf("messages length = %d, want 2: %s", len(messages), output)
	}
	if got := messages[0].Get("content.#").Int(); got != 1 || messages[0].Get("content.0.text").String() != "running" {
		t.Fatalf("assistant content = %s, want only running text", messages[0].Get("content").Raw)
	}
	if got := messages[1].Get("content.#").Int(); got != 1 || messages[1].Get("content.0.text").String() != "continue" {
		t.Fatalf("user content = %s, want only continue text", messages[1].Get("content").Raw)
	}
}

func TestSanitizeClaudeMessagesForClaudeUpstreamRejectsNonStringToolUseIDs(t *testing.T) {
	input := []byte(`{"messages":[
		{"role":"assistant","content":[
			{"type":"text","text":"running"},
			{"type":"tool_use","id":1,"name":"Read","input":{"path":"a"}}
		]},
		{"role":"user","content":[
			{"type":"tool_result","tool_use_id":"1","content":"a"},
			{"type":"text","text":"continue"}
		]}
	]}`)

	output, report := SanitizeClaudeMessagesForClaudeUpstream(input, "claude-sonnet-4-5")
	if report.DroppedBlocks != 2 {
		t.Fatalf("DroppedBlocks = %d, want 2 for non-string tool_use.id; report=%+v", report.DroppedBlocks, report)
	}
	messages := gjson.GetBytes(output, "messages").Array()
	if len(messages) != 2 || messages[0].Get("content.#").Int() != 1 || messages[1].Get("content.#").Int() != 1 {
		t.Fatalf("sanitized messages = %s, want only the two text blocks", output)
	}
}

func TestSanitizeClaudeMessagesForClaudeUpstreamRejectsDuplicateToolUseIDs(t *testing.T) {
	input := []byte(`{"messages":[
		{"role":"assistant","content":[
			{"type":"text","text":"running"},
			{"type":"tool_use","id":"toolu_1","name":"Read","input":{"path":"a"}},
			{"type":"tool_use","id":"toolu_1","name":"Read","input":{"path":"b"}}
		]},
		{"role":"user","content":[
			{"type":"tool_result","tool_use_id":"toolu_1","content":"a"},
			{"type":"tool_result","tool_use_id":"toolu_1","content":"b"},
			{"type":"text","text":"continue"}
		]}
	]}`)

	output, report := SanitizeClaudeMessagesForClaudeUpstream(input, "claude-sonnet-4-5")
	if report.DroppedBlocks != 4 {
		t.Fatalf("DroppedBlocks = %d, want all four ambiguous duplicate-ID blocks dropped; report=%+v", report.DroppedBlocks, report)
	}
	messages := gjson.GetBytes(output, "messages").Array()
	if len(messages) != 2 || messages[0].Get("content.#").Int() != 1 || messages[1].Get("content.#").Int() != 1 {
		t.Fatalf("sanitized messages = %s, want only the two text blocks", output)
	}
}

func TestSanitizeClaudeMessagesForClaudeUpstreamDropsOrphanToolUsesAndResults(t *testing.T) {
	input := []byte(`{"messages":[
		{"role":"assistant","content":[
			{"type":"text","text":"answer"},
			{"type":"tool_use","id":"toolu_orphan","name":"Read","input":{"path":"a"}}
		]},
		{"role":"user","content":[
			{"type":"tool_result","tool_use_id":"toolu_stale","content":"stale"},
			{"type":"text","text":"new question"}
		]}
	]}`)

	output, report := SanitizeClaudeMessagesForClaudeUpstream(input, "claude-sonnet-4-5")
	if report.DroppedBlocks != 2 {
		t.Fatalf("DroppedBlocks = %d, want 2; report=%+v", report.DroppedBlocks, report)
	}
	if got := gjson.GetBytes(output, "messages.0.content.#").Int(); got != 1 {
		t.Fatalf("assistant content length = %d, want 1: %s", got, output)
	}
	if got := gjson.GetBytes(output, "messages.0.content.0.text").String(); got != "answer" {
		t.Fatalf("assistant text = %q, want answer: %s", got, output)
	}
	if got := gjson.GetBytes(output, "messages.1.content.#").Int(); got != 1 {
		t.Fatalf("user content length = %d, want 1: %s", got, output)
	}
	if got := gjson.GetBytes(output, "messages.1.content.0.text").String(); got != "new question" {
		t.Fatalf("user text = %q, want new question: %s", got, output)
	}
}

func TestSanitizeClaudeMessagesForClaudeUpstreamRequiresLeadingToolResults(t *testing.T) {
	input := []byte(`{"messages":[
		{"role":"assistant","content":[
			{"type":"tool_use","id":"toolu_1","name":"Read","input":{"path":"a"}}
		]},
		{"role":"user","content":[
			{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aW1n"}},
			{"type":"tool_result","tool_use_id":"toolu_1","content":"late"},
			{"type":"text","text":"continue"}
		]}
	]}`)

	output, report := SanitizeClaudeMessagesForClaudeUpstream(input, "claude-sonnet-4-5")
	if report.DroppedBlocks != 2 {
		t.Fatalf("DroppedBlocks = %d, want 2; report=%+v", report.DroppedBlocks, report)
	}
	messages := gjson.GetBytes(output, "messages").Array()
	if len(messages) != 1 {
		t.Fatalf("messages length = %d, want 1: %s", len(messages), output)
	}
	parts := messages[0].Get("content").Array()
	if len(parts) != 2 || parts[0].Get("type").String() != "image" || parts[1].Get("text").String() != "continue" {
		t.Fatalf("remaining user content = %s, want image then text", messages[0].Get("content").Raw)
	}
}

func TestSanitizeClaudeMessagesForClaudeUpstreamDropsOnlyUnmatchedParallelToolUse(t *testing.T) {
	input := []byte(`{"messages":[
		{"role":"assistant","content":[
			{"type":"text","text":"running"},
			{"type":"tool_use","id":"toolu_1","name":"Read","input":{"path":"a"}},
			{"type":"tool_use","id":"toolu_2","name":"Read","input":{"path":"b"}}
		]},
		{"role":"user","content":[
			{"type":"tool_result","tool_use_id":"toolu_1","content":"a"},
			{"type":"text","text":"tool two was cancelled"}
		]}
	]}`)

	output, report := SanitizeClaudeMessagesForClaudeUpstream(input, "claude-sonnet-4-5")
	if report.DroppedBlocks != 1 {
		t.Fatalf("DroppedBlocks = %d, want 1; report=%+v", report.DroppedBlocks, report)
	}
	if got := gjson.GetBytes(output, "messages.0.content.1.id").String(); got != "toolu_1" {
		t.Fatalf("remaining tool_use id = %q, want toolu_1: %s", got, output)
	}
	if gjson.GetBytes(output, "messages.0.content.2").Exists() {
		t.Fatalf("unmatched parallel tool_use was preserved: %s", output)
	}
	if got := gjson.GetBytes(output, "messages.1.content.0.tool_use_id").String(); got != "toolu_1" {
		t.Fatalf("matching tool_result id = %q, want toolu_1: %s", got, output)
	}
}

func TestSanitizeClaudeMessagesSignaturesForTargetPreservesOrphanToolHistoryByDefault(t *testing.T) {
	input := []byte(`{"messages":[
		{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"Read","input":{}}]},
		{"role":"user","content":[{"type":"text","text":"no result"}]}
	]}`)

	output, report := SanitizeClaudeMessagesSignaturesForTarget(input, ClaudeMessagesSignatureSanitizeOptions{
		TargetProvider:    SignatureProviderClaude,
		TargetModel:       "claude-sonnet-4-5",
		DropEmptyMessages: true,
	})
	if report.DroppedBlocks != 0 {
		t.Fatalf("DroppedBlocks = %d, want 0; report=%+v", report.DroppedBlocks, report)
	}
	if string(output) != string(input) {
		t.Fatalf("generic signature sanitizer changed tool history:\n got: %s\nwant: %s", output, input)
	}
}

func TestSanitizeClaudeMessagesForClaudeUpstream_NormalizesValidThinkingAndDropsEmptyMessage(t *testing.T) {
	nativeSig := testClaudeThinkingSignature()
	doubleEncoded := base64.StdEncoding.EncodeToString([]byte(nativeSig))
	input := []byte(`{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"keep","signature":"` + doubleEncoded + `"},{"type":"text","text":"answer"}]},{"role":"assistant","content":[{"type":"thinking","thinking":"drop"}]},{"role":"user","content":[{"type":"text","text":"next"}]}]}`)

	output, report := SanitizeClaudeMessagesForClaudeUpstream(input, "claude-sonnet-4-5")
	if report.Preserved != 1 || report.DroppedBlocks != 1 {
		t.Fatalf("unexpected report: %+v", report)
	}
	messages := gjson.GetBytes(output, "messages").Array()
	if len(messages) != 2 {
		t.Fatalf("messages length = %d, want 2: %s", len(messages), output)
	}
	if got := messages[0].Get("content.0.signature").String(); got != nativeSig {
		t.Fatalf("signature = %q, want provider-native %q", got, nativeSig)
	}
	if got := messages[1].Get("role").String(); got != "user" {
		t.Fatalf("remaining second role = %q, want user", got)
	}
}
