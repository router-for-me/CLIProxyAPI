package signature

import (
	"strings"
	"testing"
)

const testMetaEnvelope = "Q-PaDgA1b2C3d4E5f6G7h8I9j0K_lMnOpQrStUvWxYz-0123456789AbCdEfGhIjKlMnOpQrStUvWxYz"

func TestTagMetaReasoningRoundTrip(t *testing.T) {
	tagged := TagMetaReasoning("a1b2c3", testMetaEnvelope)
	if want := "meta#a1b2c3#" + testMetaEnvelope; tagged != want {
		t.Fatalf("tag = %q, want %q", tagged, want)
	}
	key, envelope, ok := ParseMetaReasoningTag(tagged)
	if !ok || key != "a1b2c3" || envelope != testMetaEnvelope {
		t.Fatalf("parse = (%q, %q, %v)", key, envelope, ok)
	}
	if !IsMetaReasoningTagged(tagged) {
		t.Fatal("IsMetaReasoningTagged = false for a well-formed tag")
	}
}

func TestTagMetaReasoningIsIdempotentAndRefusesWithoutProvenance(t *testing.T) {
	tagged := TagMetaReasoning("acct", testMetaEnvelope)
	if again := TagMetaReasoning("other", tagged); again != tagged {
		t.Fatalf("retagging a tagged envelope changed it: %q", again)
	}
	for _, key := range []string{"", "has space", "has#hash", strings.Repeat("a", maxMetaReasoningAccountKeyLen+1), "ünï"} {
		if got := TagMetaReasoning(key, testMetaEnvelope); got != testMetaEnvelope {
			t.Fatalf("key %q produced a tag: %q", key, got)
		}
	}
	if got := TagMetaReasoning("acct", ""); got != "" {
		t.Fatalf("empty envelope produced %q", got)
	}
}

func TestParseMetaReasoningTagRejectsEverythingElse(t *testing.T) {
	for _, raw := range []string{
		"",
		testMetaEnvelope, // legacy untagged Muse envelope
		"meta#",
		"meta#acct",
		"meta#acct#",
		"meta##" + testMetaEnvelope,
		"meta#bad key#" + testMetaEnvelope,
		"gpt#acct#" + testMetaEnvelope,
		"claude#" + testMetaEnvelope,
		"META#acct#" + testMetaEnvelope, // the prefix is case-sensitive: only the proxy mints it
	} {
		if key, envelope, ok := ParseMetaReasoningTag(raw); ok {
			t.Fatalf("ParseMetaReasoningTag(%q) = (%q, %q, true), want not a tag", raw, key, envelope)
		}
	}
}

func TestMetaTagParticipatesInProviderPrefixMechanism(t *testing.T) {
	provider, rest, ok := SplitSignatureProviderPrefix("meta#acct#" + testMetaEnvelope)
	if !ok || provider != SignatureProviderMeta || rest != "acct#"+testMetaEnvelope {
		t.Fatalf("SplitSignatureProviderPrefix = (%q, %q, %v)", provider, rest, ok)
	}
}

// A tagged Muse envelope must be exactly as unreplayable on every non-Meta lane
// as the raw envelope was before the tag existed. This is what keeps the change
// from reaching the Claude, GPT, Gemini, Kimi and Grok lanes.
func TestMetaTagIsForeignToEveryOtherLane(t *testing.T) {
	tagged := TagMetaReasoning("acct", testMetaEnvelope)
	if got := DetectSignatureProvider(tagged); got != SignatureProviderUnknown {
		t.Fatalf("DetectSignatureProvider(tagged) = %q, want unknown", got)
	}
	if got, raw := DetectSignatureProvider(tagged), DetectSignatureProvider(testMetaEnvelope); got != raw {
		t.Fatalf("tagged detects as %q but the raw envelope detects as %q", got, raw)
	}
	if IsRecognizedReasoningSignature(tagged) {
		t.Fatal("tagged Muse envelope is recognized as a replayable reasoning signature")
	}
	for _, target := range []SignatureProvider{
		SignatureProviderClaude, SignatureProviderGemini, SignatureProviderGPT,
		SignatureProviderKimi, SignatureProviderGrok, SignatureProviderSWE,
	} {
		for _, kind := range []SignatureBlockKind{SignatureBlockKindUnknown, SignatureBlockKindClaudeThinking, SignatureBlockKindGPTReasoning} {
			got := DecideSignatureCompatibility(target, tagged, kind)
			want := DecideSignatureCompatibility(target, testMetaEnvelope, kind)
			if got.Compatible || got.Action != want.Action || got.ReplacementSignature != want.ReplacementSignature {
				t.Fatalf("target %q kind %q: tagged decision %+v differs from raw decision %+v", target, kind, got, want)
			}
		}
	}
	if _, err := InspectGrokEncryptedContent(tagged); err == nil {
		t.Fatal("Grok accepted a Muse-tagged envelope")
	}
	if IsValidKimiThinkingSignature(tagged) {
		t.Fatal("Kimi accepted a Muse-tagged envelope")
	}
	if IsValidGPTReasoningSignature(tagged) {
		t.Fatal("GPT accepted a Muse-tagged envelope")
	}
}

// Upstream recognizes native Muse envelopes by shape. A tagged envelope, even
// with a shape-valid inner value, must stay unrecognized everywhere: only the
// Meta executor may unwrap it after checking the selected account.
func TestMetaTagWithShapeValidEnvelopeIsNotReplayableAnywhere(t *testing.T) {
	native := testMetaReasoningSignature(1024)
	if DetectSignatureProvider(native) != SignatureProviderMeta {
		t.Fatalf("fixture is not a recognized Meta envelope")
	}
	tagged := TagMetaReasoning("acct", native)
	if got := DetectSignatureProvider(tagged); got != SignatureProviderUnknown {
		t.Fatalf("DetectSignatureProvider(tagged) = %q, want unknown", got)
	}
	for _, target := range []SignatureProvider{
		SignatureProviderMeta, SignatureProviderClaude, SignatureProviderGemini, SignatureProviderGPT,
		SignatureProviderKimi, SignatureProviderGrok, SignatureProviderSWE,
	} {
		if got, ok := CompatibleSignatureForProvider(target, tagged); ok {
			t.Fatalf("target %q accepted the tagged envelope as %q", target, got)
		}
	}
	key, envelope, ok := ParseMetaReasoningTag(tagged)
	if !ok || key != "acct" || envelope != native {
		t.Fatalf("ParseMetaReasoningTag = (%q, %q, %v)", key, envelope, ok)
	}
}
