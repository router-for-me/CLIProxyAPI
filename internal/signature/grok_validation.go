package signature

import (
	"encoding/base64"
	"fmt"
	"math"
	"strings"
	"unicode/utf8"
)

const (
	// MaxGrokEncryptedContentLen is a transport safety cap for opaque replay blobs.
	MaxGrokEncryptedContentLen = 8 * 1024 * 1024
	// MinGrokEncryptedContentDecodedLen is a deliberately loose floor, and the
	// headroom has already proven necessary. An earlier corpus of 207 samples put
	// the shortest native payload at exactly 50 bytes, with several samples piled
	// on that value, which read like a protocol floor; a later 215-sample capture
	// from grok-4.5 and grok-composer-2.5-fast reached 43 and 48 bytes and moved
	// it. Both corpora agree there is no structure to anchor on, so the observed
	// minimum is a sampling artifact that keeps sliding, and sitting on it would
	// silently reject a future shorter payload as lost reasoning context. Keep the
	// floor low and let the entropy check do the real filtering.
	MinGrokEncryptedContentDecodedLen = 32
	// MinGrokEncryptedContentEntropyRatio rejects obvious non-ciphertext payloads.
	// Native samples are >= 0.892 against the sample-size entropy ceiling.
	MinGrokEncryptedContentEntropyRatio = 0.85
)

type GrokEncryptedContentInfo struct {
	RawLen     int
	DecodedLen int
}

// InspectGrokEncryptedContent validates the transport shape of xAI/Grok
// reasoning or compaction encrypted_content. This does not prove decryptability.
//
// This is NOT a provider classifier and must not be used as one. Unlike Claude,
// Gemini and GPT, xAI emits no self-describing envelope: observed payloads are
// indistinguishable from uniform random bytes (no magic prefix, no version byte,
// no fixed suffix, and decoded lengths spread evenly modulo the AES block size).
// Every high-entropy unpadded standard-base64 blob therefore satisfies the checks
// below. Callers must establish provenance before asking this question, either
// from an explicit provider cache prefix or from a confirmed xAI target model,
// and treat the result as a replay-safety check rather than an identification.
func InspectGrokEncryptedContent(raw string) (*GrokEncryptedContentInfo, error) {
	sig := strings.TrimSpace(raw)
	if sig == "" {
		return nil, fmt.Errorf("empty Grok encrypted_content")
	}
	if len(sig) > MaxGrokEncryptedContentLen {
		return nil, fmt.Errorf("Grok encrypted_content exceeds maximum length (%d bytes)", MaxGrokEncryptedContentLen)
	}
	if sig != raw {
		return nil, fmt.Errorf("Grok encrypted_content has leading or trailing whitespace")
	}
	if strings.Contains(sig, "=") {
		return nil, fmt.Errorf("invalid Grok encrypted_content: expected unpadded standard base64")
	}
	if index, r, ok := firstInvalidGrokEncryptedContentChar(sig); ok {
		return nil, fmt.Errorf("invalid Grok encrypted_content: contains non-base64 character U+%04X at byte %d", r, index)
	}
	if _, _, ok := SplitSignatureProviderPrefix(sig); ok {
		return nil, fmt.Errorf("invalid Grok encrypted_content: carries another provider's cache prefix")
	}
	// Foreign-envelope rejection only has to run for the narrow set of base64
	// first characters a self-describing envelope can produce. Native xAI
	// ciphertext is uniformly distributed, so this skips the whole chain for
	// roughly 92% of real traffic without decoding anything. Every branch below
	// stays exhaustive for the candidates that do reach it: Claude CAIS in
	// particular is high-entropy standard base64 that drops its padding whenever
	// the decoded length is a multiple of 3, so the padding gate above does not
	// exclude it on its own.
	if maybeSelfDescribingSignatureEnvelope(sig) {
		if strings.HasPrefix(sig, "gAAAA") {
			return nil, fmt.Errorf("Grok encrypted_content looks like GPT/Codex reasoning signature")
		}
		if IsValidClaudeThinkingSignature(sig, ClaudeSignatureValidationOptions{Strict: true}) {
			return nil, fmt.Errorf("Grok encrypted_content looks like Claude thinking signature")
		}
		if IsValidClaudeCAISSignature(sig) {
			return nil, fmt.Errorf("Grok encrypted_content looks like Claude CAIS thinking signature")
		}
		if _, err := InspectGeminiThoughtSignature(sig, GeminiThoughtSignatureValidationOptions{RequireKnownEnvelope: true}); err == nil {
			return nil, fmt.Errorf("Grok encrypted_content looks like Gemini thoughtSignature")
		}
	}
	// Kimi emits no envelope either, so the pre-filter above cannot narrow it and
	// this check has to run unconditionally. Length is the only separator the two
	// families have: Kimi is fixed at two code-path constants while xAI payload
	// length tracks reasoning volume continuously at 1-byte granularity. Neither
	// observed Kimi length appears anywhere in 1027 catalogued signatures or 215
	// native Grok samples, so rejecting them here costs no real Grok traffic.
	if IsValidKimiThinkingSignature(sig) {
		return nil, fmt.Errorf("Grok encrypted_content has a Kimi thinking signature length")
	}

	decoded, err := decodeGrokEncryptedContent(sig)
	if err != nil {
		return nil, err
	}
	if len(decoded) < MinGrokEncryptedContentDecodedLen {
		return nil, fmt.Errorf("invalid Grok encrypted_content: decoded payload too short (%d bytes)", len(decoded))
	}
	if entropyRatio := byteEntropyRatio(decoded); entropyRatio < MinGrokEncryptedContentEntropyRatio {
		return nil, fmt.Errorf("invalid Grok encrypted_content: decoded payload entropy ratio %.3f below %.3f", entropyRatio, MinGrokEncryptedContentEntropyRatio)
	}
	return &GrokEncryptedContentInfo{
		RawLen:     len(sig),
		DecodedLen: len(decoded),
	}, nil
}

func IsValidGrokEncryptedContent(raw string) bool {
	_, err := InspectGrokEncryptedContent(raw)
	return err == nil
}

func decodeGrokEncryptedContent(sig string) ([]byte, error) {
	decoded, err := base64.RawStdEncoding.DecodeString(sig)
	if err != nil {
		return nil, fmt.Errorf("invalid Grok encrypted_content: base64 decode failed: %w", err)
	}
	return decoded, nil
}

// grokEncryptedContentCharSet is the unpadded standard base64 alphabet.
var grokEncryptedContentCharSet = base64AlphabetSet("+/")

// firstInvalidGrokEncryptedContentChar scans bytes against a lookup table rather
// than ranging over runes. Every legal character is ASCII, so rune iteration only
// adds cost, and the table removes the branch mispredictions that dominated this
// scan on multi-kilobyte payloads. The offending rune is decoded once, for the
// error message, so multi-byte input is still reported accurately.
func firstInvalidGrokEncryptedContentChar(sig string) (int, rune, bool) {
	for index := 0; index < len(sig); index++ {
		if !grokEncryptedContentCharSet[sig[index]] {
			r, _ := utf8.DecodeRuneInString(sig[index:])
			return index, r, true
		}
	}
	return 0, 0, false
}

func byteEntropyRatio(buf []byte) float64 {
	if len(buf) == 0 {
		return 0
	}
	var counts [256]int
	for _, b := range buf {
		counts[b]++
	}
	n := float64(len(buf))
	entropy := 0.0
	for _, count := range counts {
		if count == 0 {
			continue
		}
		p := float64(count) / n
		entropy -= p * math.Log2(p)
	}
	maxSymbols := len(buf)
	if maxSymbols > 256 {
		maxSymbols = 256
	}
	if maxSymbols <= 1 {
		return 0
	}
	return entropy / math.Log2(float64(maxSymbols))
}
