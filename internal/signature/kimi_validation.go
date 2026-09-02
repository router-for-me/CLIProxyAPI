package signature

import (
	"encoding/base64"
	"fmt"
	"strings"
)

// Kimi thinking signatures carry no self-describing envelope. Every byte is
// indistinguishable from uniform random data: a per-offset scan over 44 samples
// x 9709 bytes found zero positions below a 4-sigma floor, so there is no magic
// prefix, version byte, key id or timestamp to anchor on the way GPT (Fernet),
// Claude (CAIS/protobuf) and Gemini (Tink envelope) all provide.
//
// What Kimi does expose is size. The raw signature length is fixed per protocol
// mode and completely independent of the content it accompanies:
//
//	non-streaming : 12946 characters (9709 bytes)
//	streaming     :  4340 characters (3255 bytes)
//
// This is not quantization of a variable payload into buckets. There is no
// bucketing behaviour at all: a response whose thinking text grew from 6 to
// 14,803 characters (thinking_tokens 1 -> 6188, output_tokens 29 -> 2709) emits
// a byte-identical signature length, and a non-streaming response carrying a
// single thinking token still emits the full 12946. The two values are code-path
// constants, not size classes.
//
// Empirical basis for treating the pair as complete:
//   - All 8 Kimi models exposed upstream (k2, k2.5, k2.6, k2-thinking, k2.7-code,
//     k2.7-code-highspeed, k3, k3-256k) x streaming/non-streaming = 16 combinations,
//     no exceptions.
//   - Additional paths that produced no third value: thinking budget 128..12000,
//     absent thinking field, max_tokens truncation mid-thinking, non-English
//     prompts, 135k-character inputs, multi-turn replay of signed history, tool
//     calls, tool_result continuation, and the interleaved-thinking beta header.
//   - Two independent collection paths agree: CPA request logs (57 unique samples)
//     and the mitmproxy harvest in
//     .agents/skills/cpa-signature-catalog-and-collection/data/signatures/kimi/
//     (61 unique samples) both yield exactly {4340, 12946}.
//
// Cross-family safety: across 1027 catalog signatures plus 215 native Grok
// samples, no Claude, Gemini, GPT or Grok value lands on either length. The
// nearest miss is a 4344-character GPT token, which the gAAAA probe claims long
// before this check runs.
//
// Fragility this check accepts, and why it still runs last: the length pair is
// an observed regularity, not a protocol contract. Kimi never reads the field
// back - replaying an empty string, a single character, non-base64 text or a
// mutated blob all return 200, and omitting the signature entirely also returns
// 200, because reasoning continuity on that endpoint travels in OpenAI-style
// reasoning_content instead. A gateway change could therefore move these values
// without any client-visible error. Running the self-describing validators first
// bounds the damage: a drift only costs Kimi its own identification and cannot
// mislabel another provider's signature.
const (
	// KimiThinkingSignatureNonStreamingLen is the raw character length Kimi emits
	// for non-streaming Messages responses.
	KimiThinkingSignatureNonStreamingLen = 12946
	// KimiThinkingSignatureStreamingLen is the raw character length Kimi emits in
	// the streaming signature_delta event.
	KimiThinkingSignatureStreamingLen = 4340
)

// KimiThinkingSignatureMode records which upstream code path produced a
// signature. It is derived from length alone and carries no decoded content.
type KimiThinkingSignatureMode string

const (
	KimiThinkingSignatureModeNonStreaming KimiThinkingSignatureMode = "non_streaming"
	KimiThinkingSignatureModeStreaming    KimiThinkingSignatureMode = "streaming"
)

// kimiThinkingSignatureLens maps every accepted raw length to the mode that
// produces it. Keeping this as a package-level map rather than inline constants
// leaves room for a calibration pass to register a newly observed length without
// touching the probe itself.
var kimiThinkingSignatureLens = map[int]KimiThinkingSignatureMode{
	KimiThinkingSignatureNonStreamingLen: KimiThinkingSignatureModeNonStreaming,
	KimiThinkingSignatureStreamingLen:    KimiThinkingSignatureModeStreaming,
}

// MinKimiThinkingSignatureEntropyRatio keeps a same-length attacker-supplied
// filler from claiming the family. Native samples sit at 0.997+ against the
// sample-size ceiling, so this floor has multiple sigma of headroom while still
// rejecting padded or repetitive input.
const MinKimiThinkingSignatureEntropyRatio = 0.85

// KimiThinkingSignatureInfo describes an accepted Kimi thinking signature.
type KimiThinkingSignatureInfo struct {
	RawLen     int
	DecodedLen int
	Mode       KimiThinkingSignatureMode
}

// InspectKimiThinkingSignature validates the transport shape of a Kimi Messages
// thinking signature.
//
// Unlike the Claude, Gemini and GPT validators this proves nothing about the
// payload: it reports that the value has the size and character class Kimi
// produces. Because size is the only available signal, this probe must run after
// every self-describing envelope check has declined, so that a Claude, Gemini or
// GPT signature can never be captured by a length coincidence.
func InspectKimiThinkingSignature(raw string) (*KimiThinkingSignatureInfo, error) {
	sig := strings.TrimSpace(raw)
	if sig == "" {
		return nil, fmt.Errorf("empty Kimi thinking signature")
	}
	if sig != raw {
		return nil, fmt.Errorf("Kimi thinking signature has leading or trailing whitespace")
	}
	mode, ok := kimiThinkingSignatureLens[len(sig)]
	if !ok {
		return nil, fmt.Errorf("invalid Kimi thinking signature: unexpected length %d", len(sig))
	}
	if strings.Contains(sig, "=") {
		return nil, fmt.Errorf("invalid Kimi thinking signature: expected unpadded standard base64")
	}
	if index, r, ok := firstInvalidGrokEncryptedContentChar(sig); ok {
		return nil, fmt.Errorf("invalid Kimi thinking signature: contains non-base64 character U+%04X at byte %d", r, index)
	}
	if _, _, ok := SplitSignatureProviderPrefix(sig); ok {
		return nil, fmt.Errorf("invalid Kimi thinking signature: carries another provider's cache prefix")
	}
	// Defense in depth. DetectSignatureProviderForBlock already runs the
	// self-describing probes first, but this validator is exported and callers
	// may reach it directly, so a foreign envelope of coincidentally matching
	// length must not be accepted here either.
	if maybeSelfDescribingSignatureEnvelope(sig) {
		if strings.HasPrefix(sig, "gAAAA") {
			return nil, fmt.Errorf("Kimi thinking signature looks like GPT/Codex reasoning signature")
		}
		if IsValidClaudeCAISSignature(sig) {
			return nil, fmt.Errorf("Kimi thinking signature looks like Claude CAIS thinking signature")
		}
		if IsValidClaudeThinkingSignature(sig, ClaudeSignatureValidationOptions{Strict: true}) {
			return nil, fmt.Errorf("Kimi thinking signature looks like Claude thinking signature")
		}
		if IsValidGeminiThoughtSignature(sig, GeminiThoughtSignatureValidationOptions{RequireKnownEnvelope: true}) {
			return nil, fmt.Errorf("Kimi thinking signature looks like Gemini thoughtSignature")
		}
	}
	decoded, err := base64.RawStdEncoding.DecodeString(sig)
	if err != nil {
		return nil, fmt.Errorf("invalid Kimi thinking signature: base64 decode failed: %w", err)
	}
	if entropyRatio := byteEntropyRatio(decoded); entropyRatio < MinKimiThinkingSignatureEntropyRatio {
		return nil, fmt.Errorf("invalid Kimi thinking signature: decoded payload entropy ratio %.3f below %.3f", entropyRatio, MinKimiThinkingSignatureEntropyRatio)
	}
	return &KimiThinkingSignatureInfo{
		RawLen:     len(sig),
		DecodedLen: len(decoded),
		Mode:       mode,
	}, nil
}

// IsValidKimiThinkingSignature reports whether raw has the transport shape of a
// Kimi thinking signature.
func IsValidKimiThinkingSignature(raw string) bool {
	_, err := InspectKimiThinkingSignature(raw)
	return err == nil
}
