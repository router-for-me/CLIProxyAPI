package signature

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
	"google.golang.org/protobuf/encoding/protowire"
)

func TestStripInvalidClaudeThinkingBlocks_RemovesGPTEncryptedContent(t *testing.T) {
	input := []byte(`{
		"messages": [
			{"role":"assistant","content":[
				{"type":"thinking","thinking":"codex reasoning","signature":"gAAAAABopenai-encrypted-content"},
				{"type":"text","text":"Answer"}
			]},
			{"role":"user","content":[{"type":"text","text":"next"}]}
		]
	}`)

	out := StripInvalidClaudeThinkingBlocks(input)
	content := gjson.GetBytes(out, "messages.0.content").Array()
	if len(content) != 1 {
		t.Fatalf("messages.0.content length = %d, want 1: %s", len(content), string(out))
	}
	if got := content[0].Get("text").String(); got != "Answer" {
		t.Fatalf("remaining content text = %q, want Answer", got)
	}
	if strings.Contains(string(out), "gAAAAABopenai-encrypted-content") || strings.Contains(string(out), "codex reasoning") {
		t.Fatalf("invalid thinking block was preserved: %s", string(out))
	}
}

func TestStripInvalidClaudeThinkingBlocksAndEmptyMessages_DropsMessagesLeftEmpty(t *testing.T) {
	input := []byte(`{
		"messages": [
			{"role":"assistant","content":[
				{"type":"thinking","thinking":"codex reasoning","signature":"gAAAAABopenai-encrypted-content"}
			]},
			{"role":"user","content":[{"type":"text","text":"next"}]}
		]
	}`)

	out := StripInvalidClaudeThinkingBlocksAndEmptyMessages(input)
	messages := gjson.GetBytes(out, "messages").Array()
	if len(messages) != 1 {
		t.Fatalf("messages length = %d, want 1: %s", len(messages), string(out))
	}
	if got := messages[0].Get("role").String(); got != "user" {
		t.Fatalf("remaining role = %q, want user", got)
	}
	if strings.Contains(string(out), "gAAAAABopenai-encrypted-content") || strings.Contains(string(out), "codex reasoning") {
		t.Fatalf("invalid thinking block was preserved: %s", string(out))
	}
}

func TestStripInvalidClaudeThinkingBlocks_RemovesMalformedEPrefix(t *testing.T) {
	input := []byte(`{
		"messages": [{"role":"assistant","content":[
			{"type":"thinking","thinking":"bad","signature":"Ebad"},
			{"type":"text","text":"Answer"}
		]}]
	}`)

	out := StripInvalidClaudeThinkingBlocks(input)
	content := gjson.GetBytes(out, "messages.0.content").Array()
	if len(content) != 1 {
		t.Fatalf("content length = %d, want 1: %s", len(content), string(out))
	}
	if strings.Contains(string(out), "Ebad") || strings.Contains(string(out), "bad") {
		t.Fatalf("malformed E-prefix thinking block was preserved: %s", string(out))
	}
}

func TestStripInvalidClaudeThinkingBlocks_Base64OnlyKeepsDecodableEPrefix(t *testing.T) {
	input := []byte(`{
		"messages": [{"role":"assistant","content":[
			{"type":"thinking","thinking":"bad","signature":"Ebad"},
			{"type":"text","text":"Answer"}
		]}]
	}`)

	out := StripInvalidClaudeThinkingBlocks(input, ClaudeSignatureValidationOptions{Base64Only: true})
	content := gjson.GetBytes(out, "messages.0.content").Array()
	if len(content) != 2 {
		t.Fatalf("content length = %d, want 2: %s", len(content), string(out))
	}
}

func TestStripInvalidClaudeThinkingBlocks_Base64OnlyRemovesInvalidBase64(t *testing.T) {
	input := []byte(`{
		"messages": [{"role":"assistant","content":[
			{"type":"thinking","thinking":"bad","signature":"E!!!invalid!!!"},
			{"type":"text","text":"Answer"}
		]}]
	}`)

	out := StripInvalidClaudeThinkingBlocks(input, ClaudeSignatureValidationOptions{Base64Only: true})
	content := gjson.GetBytes(out, "messages.0.content").Array()
	if len(content) != 1 {
		t.Fatalf("content length = %d, want 1: %s", len(content), string(out))
	}
	if strings.Contains(string(out), "E!!!invalid!!!") || strings.Contains(string(out), "bad") {
		t.Fatalf("invalid-base64 thinking block was preserved: %s", string(out))
	}
}

func TestStripInvalidClaudeThinkingBlocks_AllowsEmptySignatureEmptyTextPlaceholder(t *testing.T) {
	input := []byte(`{
		"messages": [{"role":"assistant","content":[
			{"type":"thinking","text":"","signature":""},
			{"type":"text","text":"Answer"}
		]}]
	}`)

	out := StripInvalidClaudeThinkingBlocks(input, ClaudeSignatureValidationOptions{
		Base64Only:                       true,
		AllowEmptySignatureWithEmptyText: true,
	})
	content := gjson.GetBytes(out, "messages.0.content").Array()
	if len(content) != 2 {
		t.Fatalf("content length = %d, want 2: %s", len(content), string(out))
	}
}

func TestStripInvalidClaudeThinkingBlocks_StrictRemovesMalformedClaudeTree(t *testing.T) {
	sig := base64.StdEncoding.EncodeToString([]byte{0x12, 0xFF, 0xFE, 0xFD})
	input := []byte(`{
		"messages": [{"role":"assistant","content":[
			{"type":"thinking","thinking":"bad","signature":"` + sig + `"},
			{"type":"text","text":"Answer"}
		]}]
	}`)

	out := StripInvalidClaudeThinkingBlocks(input, ClaudeSignatureValidationOptions{Strict: true})
	content := gjson.GetBytes(out, "messages.0.content").Array()
	if len(content) != 1 {
		t.Fatalf("content length = %d, want 1: %s", len(content), string(out))
	}
	if strings.Contains(string(out), sig) || strings.Contains(string(out), "bad") {
		t.Fatalf("strict-invalid thinking block was preserved: %s", string(out))
	}
}

func TestStripInvalidClaudeThinkingBlocks_KeepsClaudeSignaturePrefixes(t *testing.T) {
	singleLayer := base64.StdEncoding.EncodeToString([]byte{0x12, 0x34})
	doubleLayer := base64.StdEncoding.EncodeToString([]byte(singleLayer))
	input := []byte(`{
		"messages": [{"role":"assistant","content":[
			{"type":"thinking","thinking":"one","signature":"` + singleLayer + `"},
			{"type":"thinking","thinking":"two","signature":"modelGroup#` + doubleLayer + `"}
		]}]
	}`)

	out := StripInvalidClaudeThinkingBlocks(input)
	content := gjson.GetBytes(out, "messages.0.content").Array()
	if len(content) != 2 {
		t.Fatalf("content length = %d, want 2: %s", len(content), string(out))
	}
}

const observedFable5Sample = "CAISqwIKiAEIEBgCKkBHRlRBsNiptQUWfPoOhuQKwi5LnncZVO9bB5jqOs76D7uBtgktML0zqJtNmLHXHHcgD6lk4MQu4QBXzFd1lbC3Mg5jbGF1ZGUtZmFibGUtNTgBQgh0aGlua2luZ1okZDk3NDM5NzUtNGJiMC00OTM2LTllMjgtZDViMGQyMWJkYzQ4EgxCGh+XVFFFeySAjtAaDL/A1LltGu6MMJ+eXSIwsN0oBpDrqLv22UBfkMnTotnIbkvkOyb9xZHgigG6OZVHaI3gThm+maLKmgO5PrFLKlDFYp+YZksy/wKwszJlnLTPzAK+NUlfzagOE1ymtZTXhAYK260XyFYmg/te/C231+Fr/hoX+EJoUBnrn0gD7hqMISOT+TaFEuOXYsN517GfaxgB"

const observedContextID = "d9743975-4bb0-4936-9e28-d5b0d21bdc48"

// claudeCAISParts builds Claude CAIS signatures field by field so tests can
// assert both the observed layout and the upstream drift the validator must
// tolerate or reject.
type claudeCAISParts struct {
	includeTopEnvelope   bool
	topEnvelope          uint64
	includeTopTrailer    bool
	includeContainer     bool
	includeChannelBlock  bool
	includeChannelID     bool
	channelID            uint64
	channelIDAsBytes     bool
	includeChannelVerion bool
	includeSignature     bool
	signatureLen         int
	includeModelText     bool
	modelText            []byte
	includeField7        bool
	blockKind            string
	contextID            string
}

// defaultClaudeCAISParts mirrors the layout observed on claude-fable-5 and
// claude-opus-5 responses.
func defaultClaudeCAISParts(model string) claudeCAISParts {
	return claudeCAISParts{
		includeTopEnvelope:   true,
		topEnvelope:          2,
		includeTopTrailer:    true,
		includeContainer:     true,
		includeChannelBlock:  true,
		includeChannelID:     true,
		channelID:            16,
		includeChannelVerion: true,
		includeSignature:     true,
		signatureLen:         64,
		includeModelText:     true,
		modelText:            []byte(model),
		includeField7:        true,
		blockKind:            "thinking",
		contextID:            observedContextID,
	}
}

func (p claudeCAISParts) encode() string {
	var channelBlock []byte
	if p.includeChannelID {
		if p.channelIDAsBytes {
			channelBlock = protowire.AppendTag(channelBlock, 1, protowire.BytesType)
			channelBlock = protowire.AppendBytes(channelBlock, []byte{0x10})
		} else {
			channelBlock = protowire.AppendTag(channelBlock, 1, protowire.VarintType)
			channelBlock = protowire.AppendVarint(channelBlock, p.channelID)
		}
	}
	if p.includeChannelVerion {
		channelBlock = protowire.AppendTag(channelBlock, 3, protowire.VarintType)
		channelBlock = protowire.AppendVarint(channelBlock, 2)
	}
	if p.includeSignature {
		channelBlock = protowire.AppendTag(channelBlock, 5, protowire.BytesType)
		channelBlock = protowire.AppendBytes(channelBlock, make([]byte, p.signatureLen))
	}
	if p.includeModelText {
		channelBlock = protowire.AppendTag(channelBlock, 6, protowire.BytesType)
		channelBlock = protowire.AppendBytes(channelBlock, p.modelText)
	}
	if p.includeField7 {
		channelBlock = protowire.AppendTag(channelBlock, 7, protowire.VarintType)
		channelBlock = protowire.AppendVarint(channelBlock, 1)
	}
	if p.blockKind != "" {
		channelBlock = protowire.AppendTag(channelBlock, 8, protowire.BytesType)
		channelBlock = protowire.AppendString(channelBlock, p.blockKind)
	}
	if p.contextID != "" {
		channelBlock = protowire.AppendTag(channelBlock, 11, protowire.BytesType)
		channelBlock = protowire.AppendString(channelBlock, p.contextID)
	}

	var container []byte
	if p.includeChannelBlock {
		container = protowire.AppendTag(container, 1, protowire.BytesType)
		container = protowire.AppendBytes(container, channelBlock)
	}

	var payload []byte
	if p.includeTopEnvelope {
		payload = protowire.AppendTag(payload, 1, protowire.VarintType)
		payload = protowire.AppendVarint(payload, p.topEnvelope)
	}
	if p.includeContainer {
		payload = protowire.AppendTag(payload, 2, protowire.BytesType)
		payload = protowire.AppendBytes(payload, container)
	}
	if p.includeTopTrailer {
		payload = protowire.AppendTag(payload, 3, protowire.VarintType)
		payload = protowire.AppendVarint(payload, 1)
	}
	return base64.StdEncoding.EncodeToString(payload)
}

func testClaudeCAISSignature(model string) string {
	return defaultClaudeCAISParts(model).encode()
}

// claudeModelFreeCAISParts builds only synthetic model-free CAIS envelopes. The
// envelope and channel identifiers, field numbers, wire types, and field
// presence constants were confirmed against all 8 real captures on 2026-09-02;
// no captured signature bytes are embedded in this test fixture.
type claudeModelFreeCAISParts struct {
	envelopeVersion       uint64
	channelID             uint64
	includeChannelVersion bool
	channelVersion        uint64
	includeField7         bool
	field7                uint64
	blockKind             string
	contextID             string
	includeTopTrailer     bool
	topTrailer            uint64
	includeCarrier        bool
	carrierLen            int
}

func defaultClaudeModelFreeCAISParts() claudeModelFreeCAISParts {
	return claudeModelFreeCAISParts{
		envelopeVersion:       4,
		channelID:             17,
		includeChannelVersion: true,
		channelVersion:        2,
		includeField7:         true,
		field7:                1,
		blockKind:             "thinking",
		includeTopTrailer:     true,
		topTrailer:            1,
		includeCarrier:        true,
		carrierLen:            96,
	}
}

func syntheticSignatureBytes(length int, salt byte) []byte {
	value := make([]byte, length)
	for i := range value {
		value[i] = byte(i*73 + int(salt))
	}
	return value
}

func (p claudeModelFreeCAISParts) encode() string {
	var channelBlock []byte
	channelBlock = protowire.AppendTag(channelBlock, 1, protowire.VarintType)
	channelBlock = protowire.AppendVarint(channelBlock, p.channelID)
	if p.includeChannelVersion {
		channelBlock = protowire.AppendTag(channelBlock, 3, protowire.VarintType)
		channelBlock = protowire.AppendVarint(channelBlock, p.channelVersion)
	}
	if p.includeField7 {
		channelBlock = protowire.AppendTag(channelBlock, 7, protowire.VarintType)
		channelBlock = protowire.AppendVarint(channelBlock, p.field7)
	}
	if p.blockKind != "" {
		channelBlock = protowire.AppendTag(channelBlock, 8, protowire.BytesType)
		channelBlock = protowire.AppendString(channelBlock, p.blockKind)
	}
	if p.contextID != "" {
		channelBlock = protowire.AppendTag(channelBlock, 11, protowire.BytesType)
		channelBlock = protowire.AppendString(channelBlock, p.contextID)
	}

	var container []byte
	container = protowire.AppendTag(container, 1, protowire.BytesType)
	container = protowire.AppendBytes(container, channelBlock)
	container = protowire.AppendTag(container, 2, protowire.BytesType)
	container = protowire.AppendBytes(container, syntheticSignatureBytes(12, 0x21))
	container = protowire.AppendTag(container, 3, protowire.BytesType)
	container = protowire.AppendBytes(container, syntheticSignatureBytes(12, 0x43))
	container = protowire.AppendTag(container, 4, protowire.BytesType)
	container = protowire.AppendBytes(container, syntheticSignatureBytes(48, 0x65))
	if p.includeCarrier {
		container = protowire.AppendTag(container, 5, protowire.BytesType)
		container = protowire.AppendBytes(container, syntheticSignatureBytes(p.carrierLen, 0x87))
	}

	var payload []byte
	payload = protowire.AppendTag(payload, 1, protowire.VarintType)
	payload = protowire.AppendVarint(payload, p.envelopeVersion)
	payload = protowire.AppendTag(payload, 2, protowire.BytesType)
	payload = protowire.AppendBytes(payload, container)
	if p.includeTopTrailer {
		payload = protowire.AppendTag(payload, 3, protowire.VarintType)
		payload = protowire.AppendVarint(payload, p.topTrailer)
	}
	return base64.StdEncoding.EncodeToString(payload)
}

func syntheticOpaqueProviderSignature(decodedLen int) string {
	payload := syntheticSignatureBytes(decodedLen, 0xfc)
	if len(payload) > 0 {
		payload[0] = 0xfc
	}
	return base64.RawStdEncoding.EncodeToString(payload)
}

// hasGrokOpaqueTransportShapeWithoutEnvelopeExclusions checks only the opaque
// payload properties that the Grok validator enforces. It must not
// call InspectGrokEncryptedContent: that validator rejects recognized Claude
// envelopes by invoking the Claude predicate under test, which would make a
// negative assertion circular.
func hasGrokOpaqueTransportShapeWithoutEnvelopeExclusions(raw string) bool {
	if raw == "" || raw != strings.TrimSpace(raw) || len(raw) > MaxGrokEncryptedContentLen || strings.Contains(raw, "=") {
		return false
	}
	decoded, err := base64.RawStdEncoding.DecodeString(raw)
	if err != nil || len(decoded) < MinGrokEncryptedContentDecodedLen {
		return false
	}
	return byteEntropyRatio(decoded) >= MinGrokEncryptedContentEntropyRatio
}

func TestClaudeCAISSignature_ObservedFable5Sample(t *testing.T) {
	if !IsValidClaudeCAISSignature(observedFable5Sample) {
		t.Fatal("IsValidClaudeCAISSignature(observedFable5Sample) = false, want true")
	}

	info, err := InspectClaudeCAISSignature(observedFable5Sample)
	if err != nil {
		t.Fatalf("InspectClaudeCAISSignature failed: %v", err)
	}

	if info.ModelText != "claude-fable-5" {
		t.Fatalf("ModelText = %q, want %q", info.ModelText, "claude-fable-5")
	}
	if info.BlockKind != "thinking" {
		t.Fatalf("BlockKind = %q, want %q", info.BlockKind, "thinking")
	}
	expectedUUID := "d9743975-4bb0-4936-9e28-d5b0d21bdc48"
	if info.ContextID != expectedUUID {
		t.Fatalf("ContextID = %q, want %q", info.ContextID, expectedUUID)
	}
	if info.FirstByte != 0x08 {
		t.Fatalf("FirstByte = 0x%02x, want 0x08", info.FirstByte)
	}
}

func TestClaudeCAISSignature_DetectSignatureProvider(t *testing.T) {
	prefixes := []string{
		"",
		"ccmax#",
		"claude-code-max#",
		"claude_code_max#",
		"cais#",
		"claude-cais#",
		"claude_cais#",
		"claude#",
	}
	for _, prefix := range prefixes {
		sig := prefix + observedFable5Sample
		got := DetectSignatureProvider(sig)
		if got != SignatureProviderClaude {
			t.Errorf("DetectSignatureProvider(%q) = %q, want %q", sig, got, SignatureProviderClaude)
		}
	}
}

func TestClaudeCAISSignature_ObservedOpus5Layout(t *testing.T) {
	signature := testClaudeCAISSignature("claude-opus-5")
	info, err := InspectClaudeCAISSignature(signature)
	if err != nil {
		t.Fatalf("InspectClaudeCAISSignature failed: %v", err)
	}
	if info.ModelText != "claude-opus-5" {
		t.Fatalf("ModelText = %q, want claude-opus-5", info.ModelText)
	}
	decision := DecideSignatureCompatibilityForModel(SignatureProviderClaude, "claude-opus-5", signature, SignatureBlockKindClaudeThinking)
	if !decision.Compatible || decision.NormalizedSignature != signature || decision.DetectedProvider != SignatureProviderClaude {
		t.Fatalf("same-model opus-5 decision = %+v, want preserved with DetectedProvider=claude", decision)
	}
}

func TestClaudeCAISSignature_NotCompatibleWithGemini(t *testing.T) {
	if normalized, ok := CompatibleSignatureForProvider(SignatureProviderGemini, observedFable5Sample); ok || normalized != "" {
		t.Fatalf("CompatibleSignatureForProvider(Gemini) = %q, %v; want empty and false", normalized, ok)
	}
	if IsSignatureCompatibleWithProvider(SignatureProviderGemini, observedFable5Sample) {
		t.Fatal("IsSignatureCompatibleWithProvider(Gemini) = true, want false")
	}
	if isRecognizedGeminiProviderSignature(observedFable5Sample, SignatureBlockKindUnknown) {
		t.Fatal("isRecognizedGeminiProviderSignature = true, want false")
	}
	if _, err := InspectGeminiThoughtSignature(observedFable5Sample); err == nil {
		t.Fatal("InspectGeminiThoughtSignature should fail for Claude CAIS signature")
	}
}

func TestClaudeCAISSignature_CompatibleWithAllClaudeTargets(t *testing.T) {
	decision := DecideSignatureCompatibilityForModel(SignatureProviderClaude, "claude-fable-5", observedFable5Sample, SignatureBlockKindClaudeThinking)
	if !decision.Compatible || decision.Action != SignatureActionPreserve || decision.NormalizedSignature != observedFable5Sample || decision.DetectedProvider != SignatureProviderClaude {
		t.Fatalf("DecideSignatureCompatibilityForModel(Claude, claude-fable-5) = %+v, want compatible & preserved with DetectedProvider=claude", decision)
	}

	decisionCase := DecideSignatureCompatibilityForModel(SignatureProviderClaude, "CLAUDE-FABLE-5", observedFable5Sample, SignatureBlockKindClaudeThinking)
	if !decisionCase.Compatible || decisionCase.Action != SignatureActionPreserve {
		t.Fatalf("DecideSignatureCompatibilityForModel case-insensitive failed: %+v", decisionCase)
	}

	decisionDiff := DecideSignatureCompatibilityForModel(SignatureProviderClaude, "claude-opus-5", observedFable5Sample, SignatureBlockKindClaudeThinking)
	if !decisionDiff.Compatible || decisionDiff.Action != SignatureActionPreserve || decisionDiff.NormalizedSignature != observedFable5Sample {
		t.Fatalf("DecideSignatureCompatibilityForModel(Claude, claude-opus-5) = %+v, want compatible & preserved", decisionDiff)
	}

	opus5Sig := testClaudeCAISSignature("claude-opus-5")
	decisionOpusToOpus48 := DecideSignatureCompatibilityForModel(SignatureProviderClaude, "claude-opus-4-8", opus5Sig, SignatureBlockKindClaudeThinking)
	if !decisionOpusToOpus48.Compatible || decisionOpusToOpus48.Action != SignatureActionPreserve || decisionOpusToOpus48.NormalizedSignature != opus5Sig {
		t.Fatalf("DecideSignatureCompatibilityForModel(Claude, claude-opus-4-8) with opus-5 signature = %+v, want compatible & preserved", decisionOpusToOpus48)
	}

	if normalized, ok := CompatibleSignatureForProvider(SignatureProviderClaude, observedFable5Sample); !ok || normalized != observedFable5Sample {
		t.Fatalf("CompatibleSignatureForProvider(Claude, observedFable5Sample) = %q, %v; want %q, true", normalized, ok, observedFable5Sample)
	}

	decisionGemini := DecideSignatureCompatibilityForModel(SignatureProviderGemini, "claude-fable-5", observedFable5Sample, SignatureBlockKindClaudeThinking)
	if decisionGemini.Compatible {
		t.Fatalf("DecideSignatureCompatibilityForModel(Gemini, claude-fable-5) = %+v, want incompatible", decisionGemini)
	}
}

func TestSanitizeClaudeMessagesForClaudeUpstream_ClaudeCAIS(t *testing.T) {
	inputSame := []byte(`{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"keep","signature":"` + observedFable5Sample + `"},{"type":"text","text":"answer"}]}]}`)

	outputSame, reportSame := SanitizeClaudeMessagesForClaudeUpstream(inputSame, "claude-fable-5")
	if reportSame.Preserved != 1 || reportSame.DroppedBlocks != 0 {
		t.Fatalf("unexpected report for same model: %+v", reportSame)
	}
	if got := gjson.GetBytes(outputSame, "messages.0.content.0.signature").String(); got != observedFable5Sample {
		t.Fatalf("signature = %q, want preserved %q", got, observedFable5Sample)
	}

	inputTool := []byte(`{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"keep","signature":"` + observedFable5Sample + `"},{"type":"text","text":"answer"},{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"pwd"},"signature":"` + observedFable5Sample + `"}]}]}`)
	outputTool, reportTool := SanitizeClaudeMessagesForClaudeUpstream(inputTool, "claude-fable-5")
	if reportTool.Preserved != 1 {
		t.Fatalf("unexpected report for tool input: %+v", reportTool)
	}
	partsTool := gjson.GetBytes(outputTool, "messages.0.content").Array()
	if len(partsTool) != 3 {
		t.Fatalf("content len = %d, want 3", len(partsTool))
	}
	if partsTool[0].Get("signature").String() != observedFable5Sample {
		t.Fatalf("thinking block signature lost: %s", partsTool[0].Raw)
	}
	if partsTool[2].Get("signature").Exists() {
		t.Fatalf("tool_use signature should be stripped: %s", partsTool[2].Raw)
	}

	inputDiff := []byte(`{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"keep","signature":"` + observedFable5Sample + `"},{"type":"text","text":"answer"}]}]}`)
	outputDiff, reportDiff := SanitizeClaudeMessagesForClaudeUpstream(inputDiff, "claude-opus-5")
	if reportDiff.Preserved != 1 || reportDiff.DroppedBlocks != 0 {
		t.Fatalf("unexpected report for cross model: %+v", reportDiff)
	}
	partsDiff := gjson.GetBytes(outputDiff, "messages.0.content").Array()
	if len(partsDiff) != 2 {
		t.Fatalf("content len = %d, want 2: %s", len(partsDiff), outputDiff)
	}
	if got := partsDiff[0].Get("signature").String(); got != observedFable5Sample {
		t.Fatalf("thinking signature = %q, want %q", got, observedFable5Sample)
	}
}

// TestClaudeCAISSignature_ToleratesUpstreamFieldDrift pins the deliberately
// structural validation: rejecting a signature drops the whole thinking block,
// so incidental values observed today must not become hard requirements.
func TestClaudeCAISSignature_ToleratesUpstreamFieldDrift(t *testing.T) {
	cases := []struct {
		name  string
		parts claudeCAISParts
	}{
		{"observed layout", defaultClaudeCAISParts("claude-opus-5")},
		{"new channel id", func() claudeCAISParts {
			p := defaultClaudeCAISParts("claude-opus-5")
			p.channelID = 17
			return p
		}()},
		{"new envelope version", func() claudeCAISParts {
			p := defaultClaudeCAISParts("claude-opus-5")
			p.topEnvelope = 3
			return p
		}()},
		{"no top-level trailer", func() claudeCAISParts {
			p := defaultClaudeCAISParts("claude-opus-5")
			p.includeTopTrailer = false
			return p
		}()},
		{"no channel version", func() claudeCAISParts {
			p := defaultClaudeCAISParts("claude-opus-5")
			p.includeChannelVerion = false
			return p
		}()},
		{"longer signature bytes", func() claudeCAISParts {
			p := defaultClaudeCAISParts("claude-opus-5")
			p.signatureLen = 96
			return p
		}()},
		{"no field 7", func() claudeCAISParts {
			p := defaultClaudeCAISParts("claude-opus-5")
			p.includeField7 = false
			return p
		}()},
		{"other block kind", func() claudeCAISParts {
			p := defaultClaudeCAISParts("claude-opus-5")
			p.blockKind = "redacted_thinking"
			return p
		}()},
		{"no block kind", func() claudeCAISParts {
			p := defaultClaudeCAISParts("claude-opus-5")
			p.blockKind = ""
			return p
		}()},
		{"no context id", func() claudeCAISParts {
			p := defaultClaudeCAISParts("claude-opus-5")
			p.contextID = ""
			return p
		}()},
		{"unreleased model name", func() claudeCAISParts {
			p := defaultClaudeCAISParts("claude-opus-6-preview")
			return p
		}()},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sig := tc.parts.encode()
			if _, err := InspectClaudeCAISSignature(sig); err != nil {
				t.Fatalf("InspectClaudeCAISSignature failed: %v", err)
			}
			if got := DetectSignatureProviderForBlock(sig, SignatureBlockKindClaudeThinking); got != SignatureProviderClaude {
				t.Fatalf("DetectSignatureProviderForBlock = %q, want %q", got, SignatureProviderClaude)
			}

			input := []byte(`{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"keep","signature":"` + sig + `"}]}]}`)
			output, report := SanitizeClaudeMessagesForClaudeUpstream(input, "claude-opus-5")
			if report.Preserved != 1 || report.DroppedBlocks != 0 {
				t.Fatalf("report = %+v, want preserved thinking block", report)
			}
			if got := gjson.GetBytes(output, "messages.0.content.0.signature").String(); got != sig {
				t.Fatalf("signature = %q, want preserved %q", got, sig)
			}
		})
	}
}

func TestClaudeCAISSignature_RejectsMalformedPayloads(t *testing.T) {
	truncated := func() string {
		decoded, err := base64.StdEncoding.DecodeString(observedFable5Sample)
		if err != nil {
			t.Fatalf("decode observed sample: %v", err)
		}
		return base64.StdEncoding.EncodeToString(decoded[:len(decoded)/2])
	}()

	cases := []struct {
		name      string
		signature string
	}{
		{"empty", ""},
		{"whitespace only", "   "},
		{"not base64", "CAIS!!!not-base64"},
		{"truncated payload", truncated},
		// 'E' prefix is the classic Claude form and must not reach CAIS parsing.
		{"classic claude prefix", base64.StdEncoding.EncodeToString([]byte{0x12, 0x00})},
		// 'C' prefix but a non-0x08 marker byte, the only way to reach the marker
		// check (a 'C' prefix constrains the first byte to 0x08-0x0b).
		{"wrong marker byte", base64.StdEncoding.EncodeToString([]byte{0x0a, 0x00})},
		{"no container", func() string {
			p := defaultClaudeCAISParts("claude-opus-5")
			p.includeContainer = false
			return p.encode()
		}()},
		{"no channel block", func() string {
			p := defaultClaudeCAISParts("claude-opus-5")
			p.includeChannelBlock = false
			return p.encode()
		}()},
		{"no channel id", func() string {
			p := defaultClaudeCAISParts("claude-opus-5")
			p.includeChannelID = false
			return p.encode()
		}()},
		{"channel id wrong wire type", func() string {
			p := defaultClaudeCAISParts("claude-opus-5")
			p.channelIDAsBytes = true
			return p.encode()
		}()},
		{"no signature bytes", func() string {
			p := defaultClaudeCAISParts("claude-opus-5")
			p.includeSignature = false
			return p.encode()
		}()},
		{"empty signature bytes", func() string {
			p := defaultClaudeCAISParts("claude-opus-5")
			p.signatureLen = 0
			return p.encode()
		}()},
		{"no model text", func() string {
			p := defaultClaudeCAISParts("claude-opus-5")
			p.includeModelText = false
			return p.encode()
		}()},
		{"foreign model text", func() string {
			p := defaultClaudeCAISParts("gemini-3-pro")
			return p.encode()
		}()},
		{"invalid utf-8 model text", func() string {
			p := defaultClaudeCAISParts("claude-opus-5")
			p.modelText = []byte{'c', 'l', 'a', 'u', 'd', 'e', '-', 0xff, 0xfe}
			return p.encode()
		}()},
		{"non-uuid context id", func() string {
			p := defaultClaudeCAISParts("claude-opus-5")
			p.contextID = "not-a-canonical-uuid-value-000000000"
			return p.encode()
		}()},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if IsValidClaudeCAISSignature(tc.signature) {
				t.Fatalf("IsValidClaudeCAISSignature(%q) = true, want false", tc.signature)
			}
		})
	}
}

// TestClaudeCAISSignature_DoesNotShadowClassicClaudeSignature guards the
// detection order: CAIS validation runs before classic Claude validation, so it
// must not claim E/R signatures and change how they are normalized.
func TestClaudeCAISSignature_DoesNotShadowClassicClaudeSignature(t *testing.T) {
	classic := testClaudeThinkingSignature()
	if IsValidClaudeCAISSignature(classic) {
		t.Fatal("IsValidClaudeCAISSignature(classic Claude signature) = true, want false")
	}
	if got := DetectSignatureProviderForBlock(classic, SignatureBlockKindClaudeThinking); got != SignatureProviderClaude {
		t.Fatalf("DetectSignatureProviderForBlock(classic) = %q, want %q", got, SignatureProviderClaude)
	}

	input := []byte(`{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"keep","signature":"` + classic + `"}]}]}`)
	output, report := SanitizeClaudeMessagesForClaudeUpstream(input, "claude-sonnet-4-6")
	if report.Preserved != 1 || report.DroppedBlocks != 0 {
		t.Fatalf("report = %+v, want preserved classic thinking block", report)
	}
	if got := gjson.GetBytes(output, "messages.0.content.0.signature").String(); got != classic {
		t.Fatalf("signature = %q, want provider-native E-form %q", got, classic)
	}
}

// TestClaudeCAISSignature_CachePrefixSurvivesClaudeUpstreamSanitize covers the
// cached-signature path: cache.GetModelGroup collapses every Claude model to the
// "claude" prefix, so a CAIS signature reaches the sanitizer as "claude#..." and
// must be replayed with the prefix stripped instead of being dropped.
func TestClaudeCAISSignature_CachePrefixSurvivesClaudeUpstreamSanitize(t *testing.T) {
	for _, prefix := range []string{"claude#", "anthropic#", "cais#", "ccmax#"} {
		t.Run(prefix, func(t *testing.T) {
			prefixed := prefix + observedFable5Sample
			input := []byte(`{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"keep","signature":"` + prefixed + `"}]}]}`)
			output, report := SanitizeClaudeMessagesForClaudeUpstream(input, "claude-fable-5")
			if report.Preserved != 1 || report.DroppedBlocks != 0 {
				t.Fatalf("report = %+v, want preserved thinking block", report)
			}
			if got := gjson.GetBytes(output, "messages.0.content.0.signature").String(); got != observedFable5Sample {
				t.Fatalf("signature = %q, want unprefixed %q", got, observedFable5Sample)
			}
		})
	}
}

func TestCompatibleAntigravityClaudeThinkingSignature_RejectsClaudeCAIS(t *testing.T) {
	if normalized, ok := CompatibleAntigravityClaudeThinkingSignature(observedFable5Sample); ok || normalized != "" {
		t.Fatalf("CompatibleAntigravityClaudeThinkingSignature(ClaudeCAIS) = %q, %v; want empty and false", normalized, ok)
	}
	if normalized, ok := CompatibleAntigravityClaudeThinkingSignature("ccmax#" + observedFable5Sample); ok || normalized != "" {
		t.Fatalf("CompatibleAntigravityClaudeThinkingSignature(ccmax#ClaudeCAIS) = %q, %v; want empty and false", normalized, ok)
	}
	if normalized, ok := CompatibleAntigravityClaudeThinkingSignature("cais#" + observedFable5Sample); ok || normalized != "" {
		t.Fatalf("CompatibleAntigravityClaudeThinkingSignature(cais#ClaudeCAIS) = %q, %v; want empty and false", normalized, ok)
	}
	if normalized, ok := CompatibleAntigravityClaudeThinkingSignature("claude-cais#" + observedFable5Sample); ok || normalized != "" {
		t.Fatalf("CompatibleAntigravityClaudeThinkingSignature(claude-cais#ClaudeCAIS) = %q, %v; want empty and false", normalized, ok)
	}
}

func TestClaudeModelFreeCAISSignature_PreservesSignedHistory(t *testing.T) {
	signature := defaultClaudeModelFreeCAISParts().encode()
	input := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"before"}]},{"role":"system","content":[{"type":"text","text":"directive"}]},{"role":"assistant","content":[{"type":"thinking","thinking":"reason","signature":"` + signature + `"}]},{"role":"user","content":[{"type":"text","text":"after"}]}]}`)

	output, report := SanitizeClaudeMessagesForClaudeUpstream(input, "claude-fable-5-1")
	if got := gjson.GetBytes(output, "messages.#").Int(); got != 4 {
		t.Errorf("messages.# = %d, want 4; body=%s", got, output)
	}

	messages := gjson.GetBytes(output, "messages").Array()
	wantRoles := []string{"user", "system", "assistant", "user"}
	if len(messages) == len(wantRoles) {
		for i, want := range wantRoles {
			if got := messages[i].Get("role").String(); got != want {
				t.Errorf("messages[%d].role = %q, want %q; body=%s", i, got, want, output)
			}
		}
	}
	for i, message := range messages {
		if message.Get("role").String() != "system" || i == len(messages)-1 {
			continue
		}
		if got := messages[i+1].Get("role").String(); got != "assistant" {
			t.Errorf("messages[%d] role=system is followed by %q, want assistant; body=%s", i, got, output)
		}
	}
	if got := gjson.GetBytes(output, "messages.2.content.0.signature").String(); got != signature {
		t.Errorf("model-free thinking signature was not preserved; body=%s", output)
	}
	if report.Preserved != 1 || report.DroppedBlocks != 0 {
		t.Errorf("report = %+v, want one preserved block and no drops; body=%s", report, output)
	}
}

func TestClaudeModelFreeCAISSignature_UsesStructuralGenerationRecognition(t *testing.T) {
	accepted := []struct {
		name  string
		parts claudeModelFreeCAISParts
	}{
		{"captured field shape", defaultClaudeModelFreeCAISParts()},
		// The generation allowlists are deliberately Cartesian. These cases pin
		// every currently accepted pair without claiming that every pair has
		// appeared in model-free captures.
		{"known envelope 2 with channel 17", func() claudeModelFreeCAISParts {
			p := defaultClaudeModelFreeCAISParts()
			p.envelopeVersion = 2
			return p
		}()},
		{"envelope 4 with known channel 16", func() claudeModelFreeCAISParts {
			p := defaultClaudeModelFreeCAISParts()
			p.channelID = 16
			return p
		}()},
		{"known envelope 2 with channel 16", func() claudeModelFreeCAISParts {
			p := defaultClaudeModelFreeCAISParts()
			p.envelopeVersion = 2
			p.channelID = 16
			return p
		}()},
		{"channel version absent", func() claudeModelFreeCAISParts {
			p := defaultClaudeModelFreeCAISParts()
			p.includeChannelVersion = false
			return p
		}()},
		{"channel version changed", func() claudeModelFreeCAISParts {
			p := defaultClaudeModelFreeCAISParts()
			p.channelVersion = 99
			return p
		}()},
		{"field 7 absent", func() claudeModelFreeCAISParts {
			p := defaultClaudeModelFreeCAISParts()
			p.includeField7 = false
			return p
		}()},
		{"field 7 changed", func() claudeModelFreeCAISParts {
			p := defaultClaudeModelFreeCAISParts()
			p.field7 = 123
			return p
		}()},
		{"trailer absent", func() claudeModelFreeCAISParts {
			p := defaultClaudeModelFreeCAISParts()
			p.includeTopTrailer = false
			return p
		}()},
		{"trailer changed", func() claudeModelFreeCAISParts {
			p := defaultClaudeModelFreeCAISParts()
			p.topTrailer = 7
			return p
		}()},
		{"tool use block kind", func() claudeModelFreeCAISParts {
			p := defaultClaudeModelFreeCAISParts()
			p.blockKind = "tool_use"
			return p
		}()},
		{"context id present", func() claudeModelFreeCAISParts {
			p := defaultClaudeModelFreeCAISParts()
			p.contextID = "00000000-0000-4000-8000-000000000001"
			return p
		}()},
		{"one-byte carrier", func() claudeModelFreeCAISParts {
			p := defaultClaudeModelFreeCAISParts()
			p.carrierLen = 1
			return p
		}()},
		{"200 KiB carrier", func() claudeModelFreeCAISParts {
			p := defaultClaudeModelFreeCAISParts()
			p.carrierLen = 200 * 1024
			return p
		}()},
	}
	for _, tc := range accepted {
		signature := tc.parts.encode()
		if got := DetectSignatureProviderForBlock(signature, SignatureBlockKindClaudeThinking); got != SignatureProviderClaude {
			t.Errorf("%s: provider = %q, want %q", tc.name, got, SignatureProviderClaude)
		}
	}

	rejected := []struct {
		name  string
		parts claudeModelFreeCAISParts
	}{
		{"unknown envelope version", func() claudeModelFreeCAISParts {
			p := defaultClaudeModelFreeCAISParts()
			p.envelopeVersion = 5
			return p
		}()},
		{"unknown channel id", func() claudeModelFreeCAISParts {
			p := defaultClaudeModelFreeCAISParts()
			p.channelID = 18
			return p
		}()},
		{"legacy-only channel id", func() claudeModelFreeCAISParts {
			p := defaultClaudeModelFreeCAISParts()
			p.channelID = 11
			return p
		}()},
		{"missing carrier", func() claudeModelFreeCAISParts {
			p := defaultClaudeModelFreeCAISParts()
			p.includeCarrier = false
			return p
		}()},
		{"empty carrier", func() claudeModelFreeCAISParts {
			p := defaultClaudeModelFreeCAISParts()
			p.carrierLen = 0
			return p
		}()},
	}
	for _, tc := range rejected {
		signature := tc.parts.encode()
		if got := DetectSignatureProviderForBlock(signature, SignatureBlockKindClaudeThinking); got == SignatureProviderClaude {
			t.Errorf("%s: provider = %q, want non-Claude", tc.name, got)
		}
	}
}

func TestClaudeModelFreeCAISSignature_UsesEnvelopeClassificationWithOpaqueOverlap(t *testing.T) {
	claudeSignature := defaultClaudeModelFreeCAISParts().encode()
	if got := DetectSignatureProviderForBlock(claudeSignature, SignatureBlockKindClaudeThinking); got != SignatureProviderClaude {
		t.Errorf("model-free CAIS provider = %q, want %q", got, SignatureProviderClaude)
	}
	// Grok has no provider-distinguishing payload envelope. This synthetic CAIS
	// fixture also satisfies Grok's opaque base64, size, and entropy properties.
	// Classification must therefore come from the CAIS tree, not from a false
	// claim that the transport shapes are disjoint.
	if !hasGrokOpaqueTransportShapeWithoutEnvelopeExclusions(claudeSignature) {
		t.Error("model-free CAIS fixture no longer exercises the Grok transport-shape overlap")
	}

	// No Kimi or Grok provider corpus is present locally. These ordinary controls
	// reproduce their documented opaque transport shapes with deterministic
	// high-entropy bytes rather than embedding captured values. The Kimi value is
	// not adversarially CAIS-shaped: it only proves the residual fixed-length path
	// remains reachable after the self-describing envelope probes decline.
	kimiSignature := syntheticOpaqueProviderSignature(KimiThinkingSignatureStreamingLen * 3 / 4)
	if len(kimiSignature) != KimiThinkingSignatureStreamingLen || !IsValidKimiThinkingSignature(kimiSignature) {
		t.Fatal("synthetic Kimi control does not satisfy Kimi's streaming transport shape")
	}
	grokSignature := syntheticOpaqueProviderSignature(257)
	if !IsValidGrokEncryptedContent(grokSignature) {
		t.Fatal("synthetic Grok control does not satisfy Grok's transport shape")
	}

	controls := []struct {
		name      string
		signature string
		provider  SignatureProvider
	}{
		{"Gemini protobuf envelope", testGeminiThoughtSignatureEnvelope(), SignatureProviderGemini},
		{"GPT Fernet envelope", testGPTReasoningSignature(), SignatureProviderGPT},
		{"Kimi streaming transport", kimiSignature, SignatureProviderKimi},
		{"Grok opaque transport", grokSignature, SignatureProviderUnknown},
	}
	for _, control := range controls {
		if IsValidClaudeCAISSignature(control.signature) {
			t.Errorf("%s was claimed as Claude CAIS", control.name)
		}
		if got := DetectSignatureProviderForBlock(control.signature, SignatureBlockKindClaudeThinking); got != control.provider {
			t.Errorf("%s provider = %q, want %q", control.name, got, control.provider)
		}
	}
}

func TestClaudeCAISSignature_RejectsInvalidUTF8BlockKind(t *testing.T) {
	parts := defaultClaudeCAISParts("claude-opus-5")
	parts.blockKind = string([]byte{0xff, 0xfe})
	signature := parts.encode()

	if IsValidClaudeCAISSignature(signature) {
		t.Fatal("model-tagged CAIS accepted invalid UTF-8 in channel field 8")
	}
}

func TestClaudeModelFreeCAISSignature_UnknownGenerationReasonIsSpecific(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*claudeModelFreeCAISParts)
		want   string
	}{
		{"channel id", func(parts *claudeModelFreeCAISParts) { parts.channelID = 18 }, "unknown channel_id 18"},
		{"envelope version", func(parts *claudeModelFreeCAISParts) { parts.envelopeVersion = 5 }, "unknown envelope version 5"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			parts := defaultClaudeModelFreeCAISParts()
			tc.mutate(&parts)
			signature := parts.encode()
			input := []byte(`{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"reason","signature":"` + signature + `"},{"type":"text","text":"answer"}]}]}`)

			_, report := SanitizeClaudeMessagesForClaudeUpstream(input, "claude-fable-5-1")
			if len(report.Decisions) != 1 {
				t.Fatalf("decision count = %d, want 1; report=%+v", len(report.Decisions), report)
			}
			decision := report.Decisions[0]
			if decision.Action != SignatureActionDropBlock || decision.DetectedProvider != SignatureProviderUnknown {
				t.Fatalf("decision = %+v, want unknown provider and dropped block", decision)
			}
			if !strings.Contains(decision.Reason, tc.want) {
				t.Fatalf("reason = %q, want specific diagnosis containing %q", decision.Reason, tc.want)
			}
			if strings.Contains(decision.Reason, "cross-provider") {
				t.Fatalf("reason = %q, must not claim a cross-provider mismatch", decision.Reason)
			}
		})
	}
}

func TestClaudeModelFreeCAISSignature_RejectsLegacyOnlyChannelID(t *testing.T) {
	parts := defaultClaudeModelFreeCAISParts()
	parts.channelID = 11
	signature := parts.encode()

	if got := DetectSignatureProviderForBlock(signature, SignatureBlockKindClaudeThinking); got != SignatureProviderUnknown {
		t.Fatalf("channel_id 11 provider = %q, want %q because 11 is unobserved under CAIS", got, SignatureProviderUnknown)
	}
}
