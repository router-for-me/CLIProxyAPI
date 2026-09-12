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

const observedFable51CAQSSample = "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"
const observedFable51CAQSNarrationSample = "CAQS1wcKEQgRGAI4AUIJbmFycmF0aW9uEgx5LrAwGWmNqIuLoPoaDCz9ejHfn6/z4XQk9SIwhyZlO5uaOp3VmdX3F7fs7KEDyPDqwVnN7xaSmeJOV5Arz+KM6Jxjf9GCXPbFBh9UKvMGF85qvThSqk2AZdjBT+OH4tN1F9puuxtUcp9vlwta78FgdgwUxd6/h0gD2r+5lEUJ9XfwxBvSi3X6fBGoHh+GV/3b0bQKutA7FgmvO7x6s5oAYg4EKs7CSkIOnkwo3g1ahX43xHjkaMsQeoziouORPGM6kCWq9E2TpwhEse3ouQ6PzvbTkj2W/vOhy2RVDXf5qvS/zt4w488A17nI5z8upoVvO0rmY5KQhaZRuAOmjMUqnFyNAZzikwNo9O8ukArtXkSWkGjbIbJ4rd1cX0zdYzmTE8Nos/LroXsqB2Dk7GxZn2TP6uqtoLL/6aowhC+yYOYPeSobtERH9AUuAgBEZk0vGHR+viVeJf6cCm2dFQtv0hEYgPlgcSwoDiKrMq7fOEiBkpM8QD114FR39f0C8cJemOScabiaToHE75grwQx1NVoBjYRHNb/sftU3tnSiiqhsCTxxZVYtB5p4WLhLH21KeI7Fjs1DQhXv9MwfByAHJXusYsQ4SPwAUYnxn+NN/XzN/0OeCxz+CtUo1QD9q4LU/tWM58EcA/y1sAUE9p+6ILvXYQ9bitNf8RQFzp9hNCFWKgiuRtKTGKGI2PEaWOUp7Lmo8LM1DeFyKU4RiylXXKEdhS53WoK+gY0/iwJPBMoFZv+EAWZCGxOfYjgv51XVTl2tHLCwevmuSQqzdZSdKtuyoheaDaAblRJNwK/8CngwAZk8ef+CCdM5lCFkZ4dKVrmbSDBGi5PZDkY185oRVqVWO1VyiqjQ4AfwMp3vYE+vDlI+ylm72KU4TqScVgSm/SUGVsER7Wg5Kuqgb7fOvPhPVX6/C4v7sfUwejjax0T7JFXqlUGqxP8yGUck2PA9XPeZXFpuQVmhuWgWp4lWAWCQggvMnhQILFojjxnIwvA8v16n3FpK8ZODxQ2hlg6NJBai5S3OdSxGIR30io4g24rfSkXinMoaofM6VSUeUs0io05byPmuGocGoC6ivpnU+rOuIR5ShNHiIuPQl1PMfuPxzOPPMu4GyuFO659GTqtyokntivgIGtS1XC/2CX/sp42A3gflhPR7oZmtnAOFUm3mQT94SU9dGUTT06eXyx4g+4VaiW6keFOAJ6dfa6PMd0MUJc0z/nXmpzEFr3gZJo8FUrpTyw/HTTq5RVbvPnxAz5ZAcVMJy10t0hwS0Qyf5xgB"

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

func TestClaudeCAQSSignature_ObservedFable51Sample(t *testing.T) {
	if !IsValidClaudeCAISSignature(observedFable51CAQSSample) {
		t.Fatal("IsValidClaudeCAISSignature(observedFable51CAQSSample) = false, want true")
	}

	info, err := InspectClaudeCAISSignature(observedFable51CAQSSample)
	if err != nil {
		t.Fatalf("InspectClaudeCAISSignature failed: %v", err)
	}

	if info.EnvelopeVersion != 4 {
		t.Fatalf("EnvelopeVersion = %d, want 4", info.EnvelopeVersion)
	}
	if info.ChannelID != 17 {
		t.Fatalf("ChannelID = %d, want 17", info.ChannelID)
	}
	if info.BlockKind != "thinking" {
		t.Fatalf("BlockKind = %q, want %q", info.BlockKind, "thinking")
	}
	if info.FirstByte != 0x08 {
		t.Fatalf("FirstByte = 0x%02x, want 0x08", info.FirstByte)
	}
	if info.SignatureLen != 4064 {
		t.Fatalf("SignatureLen = %d, want 4064", info.SignatureLen)
	}

	if got := DetectSignatureProvider(observedFable51CAQSSample); got != SignatureProviderClaude {
		t.Fatalf("DetectSignatureProvider(observedFable51CAQSSample) = %v, want %v", got, SignatureProviderClaude)
	}
}

func TestClaudeCAISSignature_ObservedFable51CAQSNarrationSample(t *testing.T) {
	if !IsValidClaudeCAISSignature(observedFable51CAQSNarrationSample) {
		t.Fatal("IsValidClaudeCAISSignature(observedFable51CAQSNarrationSample) = false, want true")
	}

	info, err := InspectClaudeCAISSignature(observedFable51CAQSNarrationSample)
	if err != nil {
		t.Fatalf("InspectClaudeCAISSignature failed: %v", err)
	}

	if info.EnvelopeVersion != 4 {
		t.Fatalf("EnvelopeVersion = %d, want 4", info.EnvelopeVersion)
	}
	if info.ChannelID != 17 {
		t.Fatalf("ChannelID = %d, want 17", info.ChannelID)
	}
	if info.BlockKind != "narration" {
		t.Fatalf("BlockKind = %q, want %q", info.BlockKind, "narration")
	}
	if info.FirstByte != 0x08 {
		t.Fatalf("FirstByte = 0x%02x, want 0x08", info.FirstByte)
	}
	if info.SignatureLen != 883 {
		t.Fatalf("SignatureLen = %d, want 883", info.SignatureLen)
	}

	if got := DetectSignatureProvider(observedFable51CAQSNarrationSample); got != SignatureProviderClaude {
		t.Fatalf("DetectSignatureProvider(observedFable51CAQSNarrationSample) = %v, want %v", got, SignatureProviderClaude)
	}
}

func TestClaudeCAQSSignature_RejectsMalformedPayloads(t *testing.T) {
	cases := []struct {
		name      string
		signature string
	}{
		{"invalid block kind", func() string {
			decoded, err := base64.StdEncoding.DecodeString(observedFable51CAQSSample)
			if err != nil {
				t.Fatalf("decode sample: %v", err)
			}
			mutated := strings.Replace(string(decoded), "thinking", "unknown!", 1)
			return base64.StdEncoding.EncodeToString([]byte(mutated))
		}()},
		{"unrecognized block kind", func() string {
			decoded, err := base64.StdEncoding.DecodeString(observedFable51CAQSSample)
			if err != nil {
				t.Fatalf("decode sample: %v", err)
			}
			mutated := strings.Replace(string(decoded), "thinking", "redacted", 1)
			return base64.StdEncoding.EncodeToString([]byte(mutated))
		}()},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if IsValidClaudeCAISSignature(tc.signature) {
				t.Fatalf("IsValidClaudeCAISSignature(%s) = true, want false", tc.name)
			}
			if _, err := InspectClaudeCAISSignature(tc.signature); err == nil {
				t.Fatalf("InspectClaudeCAISSignature(%s) succeeded, want error", tc.name)
			}
		})
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
