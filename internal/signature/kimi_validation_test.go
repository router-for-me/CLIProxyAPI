package signature

import (
	"encoding/base64"
	"encoding/json"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// kimiSignatureCorpusPath locates the harvested Kimi signature corpus. The
// corpus lives with the collection skill that produced it and is not tracked in
// this repository, matching how the Grok and Gemini native corpora are handled:
// tests that need real traffic skip when it is absent rather than committing
// captured payloads.
func kimiSignatureCorpusPath() (string, bool) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "", false
	}
	repo := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	path := filepath.Join(repo, ".agents", "skills", "cpa-signature-catalog-and-collection", "data", "signatures", "kimi", "samples.json")
	if _, err := os.Stat(path); err != nil {
		return path, false
	}
	return path, true
}

// masterSignatureCatalogPath locates the cross-provider signature catalog from
// the same collection skill.
func masterSignatureCatalogPath() (string, bool) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "", false
	}
	repo := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	path := filepath.Join(repo, ".agents", "skills", "cpa-signature-catalog-and-collection", "data", "master_signatures_catalog.json")
	if _, err := os.Stat(path); err != nil {
		return path, false
	}
	return path, true
}

const kimiCorpusSkipReason = "kimi signature corpus missing; see .agents/skills/cpa-signature-catalog-and-collection"

func loadKimiCorpus(t *testing.T) []string {
	t.Helper()
	path, ok := kimiSignatureCorpusPath()
	if !ok {
		t.Skip(kimiCorpusSkipReason)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read kimi corpus: %v", err)
	}
	var doc struct {
		Samples []struct {
			Signature string `json:"signature"`
		} `json:"samples"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse kimi corpus: %v", err)
	}
	out := make([]string, 0, len(doc.Samples))
	for _, sample := range doc.Samples {
		if sample.Signature != "" {
			out = append(out, sample.Signature)
		}
	}
	if len(out) == 0 {
		t.Skip(kimiCorpusSkipReason)
	}
	return out
}

// synthesizeKimiSignature builds a signature-shaped payload of the requested
// decoded size from a seeded PRNG. Kimi identification rests entirely on raw
// length plus the payload being high-entropy unpadded base64, and none of that
// requires captured traffic, so the contract tests below run everywhere instead
// of depending on a local corpus.
func synthesizeKimiSignature(t *testing.T, decodedLen int, seed int64) string {
	t.Helper()
	buf := make([]byte, decodedLen)
	prng := rand.New(rand.NewSource(seed))
	if _, err := prng.Read(buf); err != nil {
		t.Fatalf("synthesize payload: %v", err)
	}
	return base64.RawStdEncoding.EncodeToString(buf)
}

// TestKimiThinkingSignatureLengths_MatchDecodedSizes pins the arithmetic that
// makes the two constants reachable at all: unpadded base64 of 9709 and 3255
// bytes is exactly 12946 and 4340 characters. A future edit that changes one
// constant without the other would otherwise produce a length no real payload
// can have.
func TestKimiThinkingSignatureLengths_MatchDecodedSizes(t *testing.T) {
	tests := []struct {
		name       string
		decodedLen int
		wantRawLen int
	}{
		{"non streaming", 9709, KimiThinkingSignatureNonStreamingLen},
		{"streaming", 3255, KimiThinkingSignatureStreamingLen},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sig := synthesizeKimiSignature(t, tc.decodedLen, 1)
			if len(sig) != tc.wantRawLen {
				t.Fatalf("raw length = %d, want %d", len(sig), tc.wantRawLen)
			}
			info, err := InspectKimiThinkingSignature(sig)
			if err != nil {
				t.Fatalf("synthesized payload rejected: %v", err)
			}
			if info.DecodedLen != tc.decodedLen {
				t.Errorf("DecodedLen = %d, want %d", info.DecodedLen, tc.decodedLen)
			}
		})
	}
}

func TestInspectKimiThinkingSignature_ReportsMode(t *testing.T) {
	tests := []struct {
		name       string
		decodedLen int
		wantMode   KimiThinkingSignatureMode
	}{
		{"non streaming", 9709, KimiThinkingSignatureModeNonStreaming},
		{"streaming", 3255, KimiThinkingSignatureModeStreaming},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			info, err := InspectKimiThinkingSignature(synthesizeKimiSignature(t, tc.decodedLen, 7))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if info.Mode != tc.wantMode {
				t.Errorf("Mode = %q, want %q", info.Mode, tc.wantMode)
			}
		})
	}
}

// TestInspectKimiThinkingSignature_RejectsNeighbouringLengths is the core
// negative test for a size-only probe: one character in either direction must
// fall out of the family.
func TestInspectKimiThinkingSignature_RejectsNeighbouringLengths(t *testing.T) {
	for _, decodedLen := range []int{9709, 3255} {
		native := synthesizeKimiSignature(t, decodedLen, 3)
		for _, tc := range []struct {
			name string
			sig  string
		}{
			{"one character short", native[:len(native)-1]},
			{"one character long", native + "A"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				if IsValidKimiThinkingSignature(tc.sig) {
					t.Errorf("length %d accepted as Kimi signature", len(tc.sig))
				}
			})
		}
	}
}

func TestInspectKimiThinkingSignature_RejectsMalformedInput(t *testing.T) {
	native := synthesizeKimiSignature(t, 3255, 5)
	tests := []struct {
		name string
		sig  string
	}{
		{"empty", ""},
		{"whitespace only", "   "},
		{"leading whitespace", " " + native},
		{"trailing whitespace", native + " "},
		{"padded base64", native[:len(native)-2] + "=="},
		{"non base64 character", native[:len(native)-1] + "!"},
		{"provider cache prefix", "claude#" + native},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if IsValidKimiThinkingSignature(tc.sig) {
				t.Errorf("malformed input accepted as Kimi signature")
			}
		})
	}
}

// TestInspectKimiThinkingSignature_RejectsLowEntropyFiller pins the one attack a
// length check alone cannot survive: a caller that knows the constant and pads
// to it with structured bytes.
func TestInspectKimiThinkingSignature_RejectsLowEntropyFiller(t *testing.T) {
	for _, length := range []int{KimiThinkingSignatureStreamingLen, KimiThinkingSignatureNonStreamingLen} {
		if IsValidKimiThinkingSignature(strings.Repeat("A", length)) {
			t.Errorf("repeated-character filler of length %d accepted as Kimi signature", length)
		}
	}
}

// TestInspectKimiThinkingSignature_RejectsSelfDescribingEnvelope guards the
// exported entry point. DetectSignatureProviderForBlock already runs the
// envelope probes first, but callers can reach this validator directly, so a
// foreign envelope must not be accepted here on length alone.
func TestInspectKimiThinkingSignature_RejectsSelfDescribingEnvelope(t *testing.T) {
	if IsValidKimiThinkingSignature(observedFable5Sample) {
		t.Errorf("Claude CAIS sample accepted as Kimi signature")
	}
}

// TestDetectSignatureProvider_KimiRunsAfterEnvelopeProbes pins the ordering
// invariant. Kimi's base64 is uniformly distributed, so roughly 6% of real
// signatures start with one of the "CERg" envelope characters; those must still
// resolve to Kimi after the envelope probes decline, and a real envelope must
// never be captured by the size probe.
func TestDetectSignatureProvider_KimiRunsAfterEnvelopeProbes(t *testing.T) {
	if got := DetectSignatureProvider(observedFable5Sample); got != SignatureProviderClaude {
		t.Fatalf("DetectSignatureProvider = %q, want %q for a Claude CAIS sample", got, SignatureProviderClaude)
	}

	var checked int
	for seed := int64(0); seed < 200 && checked < 3; seed++ {
		sig := synthesizeKimiSignature(t, 3255, seed)
		if !maybeSelfDescribingSignatureEnvelope(sig) {
			continue
		}
		checked++
		if got := DetectSignatureProvider(sig); got != SignatureProviderKimi {
			t.Fatalf("DetectSignatureProvider = %q, want %q for an envelope-prefixed Kimi payload", got, SignatureProviderKimi)
		}
	}
	if checked == 0 {
		t.Skip("no synthesized payload landed on an envelope first character")
	}
}

func TestInspectGrokEncryptedContent_RejectsKimiLengths(t *testing.T) {
	for _, decodedLen := range []int{9709, 3255} {
		sig := synthesizeKimiSignature(t, decodedLen, 11)
		if IsValidGrokEncryptedContent(sig) {
			t.Errorf("Kimi-length payload (%d bytes) accepted as Grok encrypted_content", decodedLen)
		}
	}
}

func TestSignatureProviderFromModelName_Kimi(t *testing.T) {
	tests := []struct {
		model string
		want  SignatureProvider
	}{
		{"kimi-k3", SignatureProviderKimi},
		{"kimi-k3-256k", SignatureProviderKimi},
		{"kimi-k2.7-code-highspeed", SignatureProviderKimi},
		{"k3", SignatureProviderKimi},
		{"k2-thinking", SignatureProviderKimi},
		{"moonshot-v1-128k", SignatureProviderKimi},
		{"claude-opus-5", SignatureProviderClaude},
		{"gemini-3.6-flash", SignatureProviderGemini},
		{"gpt-5.6-sol", SignatureProviderGPT},
	}
	for _, tc := range tests {
		t.Run(tc.model, func(t *testing.T) {
			if got := SignatureProviderFromModelName(tc.model); got != tc.want {
				t.Errorf("SignatureProviderFromModelName(%q) = %q, want %q", tc.model, got, tc.want)
			}
		})
	}
}

// TestDecideSignatureCompatibility_KimiDropsSignatureNotBlock encodes the
// measured upstream behaviour: Kimi returns 200 for a mutated, truncated,
// non-base64 or entirely absent thinking signature, so a foreign signature costs
// the field rather than the reasoning text.
func TestDecideSignatureCompatibility_KimiDropsSignatureNotBlock(t *testing.T) {
	decision := DecideSignatureCompatibility(SignatureProviderKimi, observedFable5Sample, SignatureBlockKindClaudeThinking)
	if decision.Compatible {
		t.Fatalf("Claude signature reported compatible with a Kimi target")
	}
	if decision.Action != SignatureActionDropSignature {
		t.Errorf("Action = %q, want %q", decision.Action, SignatureActionDropSignature)
	}
}

func TestDecideSignatureCompatibility_KimiPreservesNativeSignature(t *testing.T) {
	native := synthesizeKimiSignature(t, 9709, 13)
	decision := DecideSignatureCompatibility(SignatureProviderKimi, native, SignatureBlockKindClaudeThinking)
	if !decision.Compatible {
		t.Fatalf("Kimi-shaped signature reported incompatible with a Kimi target: %s", decision.Reason)
	}
	if decision.Action != SignatureActionPreserve {
		t.Errorf("Action = %q, want %q", decision.Action, SignatureActionPreserve)
	}
	if decision.NormalizedSignature != native {
		t.Errorf("NormalizedSignature was rewritten for a Kimi signature")
	}
}

// TestInspectKimiThinkingSignature_NativeCorpus validates the synthesized
// contract above against real harvested traffic when the corpus is available.
func TestInspectKimiThinkingSignature_NativeCorpus(t *testing.T) {
	modes := map[KimiThinkingSignatureMode]int{}
	for _, sig := range loadKimiCorpus(t) {
		info, err := InspectKimiThinkingSignature(sig)
		if err != nil {
			t.Fatalf("native Kimi signature (len %d) rejected: %v", len(sig), err)
		}
		if got := DetectSignatureProvider(sig); got != SignatureProviderKimi {
			t.Fatalf("DetectSignatureProvider = %q, want %q", got, SignatureProviderKimi)
		}
		modes[info.Mode]++
	}
	if modes[KimiThinkingSignatureModeNonStreaming] == 0 || modes[KimiThinkingSignatureModeStreaming] == 0 {
		t.Fatalf("corpus does not cover both modes: %v", modes)
	}
}

// TestDetectSignatureProvider_KimiProbeDoesNotDisturbCatalog replays the whole
// cross-provider catalog to prove the size probe changed nothing for the
// self-describing families and never claims a Grok payload.
func TestDetectSignatureProvider_KimiProbeDoesNotDisturbCatalog(t *testing.T) {
	path, ok := masterSignatureCatalogPath()
	if !ok {
		t.Skip("signature catalog missing; see .agents/skills/cpa-signature-catalog-and-collection")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read signature catalog: %v", err)
	}
	var doc struct {
		Records []struct {
			FullSignature   string `json:"full_signature"`
			ClaimedProvider string `json:"claimed_provider"`
		} `json:"records"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse signature catalog: %v", err)
	}

	matrix := map[string]map[SignatureProvider]int{}
	for _, record := range doc.Records {
		if record.FullSignature == "" {
			continue
		}
		detected := DetectSignatureProvider(record.FullSignature)
		if matrix[record.ClaimedProvider] == nil {
			matrix[record.ClaimedProvider] = map[SignatureProvider]int{}
		}
		matrix[record.ClaimedProvider][detected]++
	}
	if len(matrix) == 0 {
		t.Skip("signature catalog has no usable records")
	}

	for claimed, row := range matrix {
		t.Logf("%-8s -> %v", claimed, row)
		if captured := row[SignatureProviderKimi]; captured > 0 {
			t.Errorf("%d %s signatures captured by the Kimi size probe", captured, claimed)
		}
	}
	// xAI stays in the residual class by contract: its ciphertext carries no
	// envelope and no fixed length, so any positive claim would also capture
	// unrelated opaque payloads.
	for detected, count := range matrix["grok"] {
		if detected != SignatureProviderUnknown {
			t.Errorf("%d grok signatures classified as %q, want %q", count, detected, SignatureProviderUnknown)
		}
	}
}
