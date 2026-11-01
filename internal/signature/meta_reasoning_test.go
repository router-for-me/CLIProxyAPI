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

// A tagged Muse envelope must be known-foreign on every non-Meta lane: it
// detects as its Meta family (never Unknown, so compatibility fallbacks cannot
// mistake it for an opaque blob worth preserving) and no lane finds it
// replayable. This is what keeps the change from reaching the Claude, GPT,
// Gemini, Kimi and Grok lanes.
func TestMetaTagIsForeignToEveryOtherLane(t *testing.T) {
	tagged := TagMetaReasoning("acct", testMetaEnvelope)
	if got := DetectSignatureProvider(tagged); got != SignatureProviderMeta {
		t.Fatalf("DetectSignatureProvider(tagged) = %q, want meta", got)
	}
	if !IsRecognizedReasoningSignature(tagged) {
		t.Fatal("tagged Muse envelope is not recognized as a Meta-family signature")
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
// with a shape-valid inner value, detects as its Meta family: it decides exactly
// like the raw envelope on every non-Meta lane, and a Meta target keeps it
// verbatim so that only the Meta executor unwraps it after checking the account.
func TestMetaTagWithShapeValidEnvelopeIsReplayableOnlyToMetaVerbatim(t *testing.T) {
	native := testMetaReasoningSignature(1024)
	if DetectSignatureProvider(native) != SignatureProviderMeta {
		t.Fatalf("fixture is not a recognized Meta envelope")
	}
	tagged := TagMetaReasoning("acct", native)
	if got := DetectSignatureProvider(tagged); got != SignatureProviderMeta {
		t.Fatalf("DetectSignatureProvider(tagged) = %q, want meta", got)
	}
	// A Meta target keeps the tag verbatim: translation cannot know which
	// account the request will be routed to, so only the Meta executor may
	// check the account and unwrap the envelope.
	if got, ok := CompatibleSignatureForProvider(SignatureProviderMeta, tagged); !ok || got != tagged {
		t.Fatalf("meta target got (%q, %v), want the tagged value unchanged", got, ok)
	}
	for _, target := range []SignatureProvider{
		SignatureProviderClaude, SignatureProviderGemini, SignatureProviderGPT,
		SignatureProviderKimi, SignatureProviderGrok, SignatureProviderSWE,
	} {
		if got, ok := CompatibleSignatureForProvider(target, tagged); ok {
			t.Fatalf("target %q accepted the tagged envelope as %q", target, got)
		}
		got := DecideSignatureCompatibility(target, tagged, SignatureBlockKindUnknown)
		if got.Compatible || got.NormalizedSignature != "" {
			t.Fatalf("target %q: tagged decision %+v claims the envelope is replayable", target, got)
		}
		want := DecideSignatureCompatibility(target, native, SignatureBlockKindUnknown)
		if got.Action != want.Action || got.ReplacementSignature != want.ReplacementSignature {
			t.Fatalf("target %q: tagged decision %+v differs from raw decision %+v", target, got, want)
		}
	}
	key, envelope, ok := ParseMetaReasoningTag(tagged)
	if !ok || key != "acct" || envelope != native {
		t.Fatalf("ParseMetaReasoningTag = (%q, %q, %v)", key, envelope, ok)
	}
}
