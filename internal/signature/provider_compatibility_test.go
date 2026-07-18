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

func TestGeminiWrappedUUIDFunctionCallSignatureIsUnknown(t *testing.T) {
	sig := testGemini3ThoughtSignature([]byte("e24830a7-5cd6-42fe-998b-ee539e72b9c3"))

	if got := DetectSignatureProvider(sig); got != SignatureProviderUnknown {
		t.Fatalf("DetectSignatureProvider(wrapped UUID) = %q, want %q", got, SignatureProviderUnknown)
	}
	if got := DetectSignatureProviderForBlock(sig, SignatureBlockKindGeminiFunctionCall); got != SignatureProviderUnknown {
		t.Fatalf("DetectSignatureProviderForBlock(wrapped UUID tool call) = %q, want %q", got, SignatureProviderUnknown)
	}
	if normalized, ok := CompatibleSignatureForProviderBlock(SignatureProviderGemini, sig, SignatureBlockKindGeminiFunctionCall); ok || normalized != "" {
		t.Fatalf("wrapped UUID tool-call signature normalized=%q ok=%v, want empty and false", normalized, ok)
	}
	decision := DecideSignatureCompatibility(SignatureProviderGemini, sig, SignatureBlockKindGeminiFunctionCall)
	if decision.Action != SignatureActionReplaceWithGeminiBypass {
		t.Fatalf("function-call wrapped UUID action = %q, want %q", decision.Action, SignatureActionReplaceWithGeminiBypass)
	}
	if decision.ReplacementSignature != GeminiSkipThoughtSignatureValidator {
		t.Fatalf("function-call wrapped UUID replacement = %q, want %q", decision.ReplacementSignature, GeminiSkipThoughtSignatureValidator)
	}
	decision = DecideSignatureCompatibility(SignatureProviderGemini, sig, SignatureBlockKindGeminiModelPart)
	if decision.Action != SignatureActionReplaceWithGeminiBypass {
		t.Fatalf("model-part wrapped UUID action = %q, want %q", decision.Action, SignatureActionReplaceWithGeminiBypass)
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
	input := []byte(`{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"drop me","signature":""},{"type":"text","text":"answer"},{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"git status"},"signature":"bad","thoughtSignature":"bad2","thought_signature":"bad3","model":"claude-sonnet-4-5","extra_content":{"google":{"thought_signature":"bad4"}}}]}]}`)

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
