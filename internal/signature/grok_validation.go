package signature

import (
	"encoding/base64"
	"fmt"
	"math"
	"strings"
)

const (
	// MaxGrokEncryptedContentLen is a transport safety cap for opaque replay blobs.
	MaxGrokEncryptedContentLen = 8 * 1024 * 1024
	// MinGrokEncryptedContentDecodedLen is derived from native Grok CLI captures;
	// shorter decoded payloads are treated as invalid replay state for xAI upstream.
	MinGrokEncryptedContentDecodedLen = 50
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
	if strings.HasPrefix(sig, "gAAAA") {
		return nil, fmt.Errorf("Grok encrypted_content looks like GPT/Codex reasoning signature")
	}
	if strings.Contains(sig, "=") {
		return nil, fmt.Errorf("invalid Grok encrypted_content: expected unpadded standard base64")
	}
	if index, r, ok := firstInvalidGrokEncryptedContentChar(sig); ok {
		return nil, fmt.Errorf("invalid Grok encrypted_content: contains non-base64 character U+%04X at byte %d", r, index)
	}
	if IsValidClaudeThinkingSignature(sig, ClaudeSignatureValidationOptions{Strict: true}) {
		return nil, fmt.Errorf("Grok encrypted_content looks like Claude thinking signature")
	}
	if _, err := InspectGeminiThoughtSignature(sig, GeminiThoughtSignatureValidationOptions{RequireKnownEnvelope: true}); err == nil {
		return nil, fmt.Errorf("Grok encrypted_content looks like Gemini thoughtSignature")
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

func firstInvalidGrokEncryptedContentChar(sig string) (int, rune, bool) {
	for index, r := range sig {
		switch {
		case r >= 'A' && r <= 'Z':
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		case r == '+' || r == '/':
		default:
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
