package signature

import (
	"encoding/base64"
	"fmt"
	"strings"
	"unicode/utf8"
)

const MaxGPTReasoningSignatureLen = 32 * 1024 * 1024

type GPTReasoningSignatureInfo struct {
	DecodedLen    int
	CiphertextLen int
}

func IsValidGPTReasoningSignature(rawSignature string) bool {
	_, err := InspectGPTReasoningSignature(rawSignature)
	return err == nil
}

// InspectGPTReasoningSignature validates the Fernet-like outer format used
// by GPT/Codex reasoning encrypted_content. This is only a transport-shape
// check; it does not prove decryptability.
func InspectGPTReasoningSignature(rawSignature string) (*GPTReasoningSignatureInfo, error) {
	sig := strings.TrimSpace(rawSignature)
	if sig == "" {
		return nil, fmt.Errorf("empty GPT reasoning signature")
	}
	if len(sig) > MaxGPTReasoningSignatureLen {
		return nil, fmt.Errorf("GPT reasoning signature exceeds maximum length (%d bytes)", MaxGPTReasoningSignatureLen)
	}
	// The literal prefix is the cheapest discriminator and rejects every other
	// provider's envelope outright, so it runs before the full charset scan.
	// Probing this validator is on the hot path for signatures of every provider,
	// and scanning a multi-kilobyte payload only to reject it on five bytes was
	// pure waste.
	if !strings.HasPrefix(sig, "gAAAA") {
		return nil, fmt.Errorf("invalid GPT reasoning signature: expected gAAAA prefix")
	}
	if index, r, ok := firstInvalidGPTReasoningSignatureChar(sig); ok {
		return nil, fmt.Errorf("invalid GPT reasoning signature: contains non-base64url character U+%04X at byte %d", r, index)
	}

	decoded, err := decodeGPTReasoningSignature(sig)
	if err != nil {
		return nil, err
	}
	if len(decoded) < 73 {
		return nil, fmt.Errorf("invalid GPT reasoning signature: decoded payload too short")
	}
	if decoded[0] != 0x80 {
		return nil, fmt.Errorf("invalid GPT reasoning signature: expected version 0x80, got 0x%02x", decoded[0])
	}

	ciphertextLen := len(decoded) - 1 - 8 - 16 - 32
	if ciphertextLen <= 0 || ciphertextLen%16 != 0 {
		return nil, fmt.Errorf("invalid GPT reasoning signature: ciphertext length %d is not a positive AES block multiple", ciphertextLen)
	}

	return &GPTReasoningSignatureInfo{
		DecodedLen:    len(decoded),
		CiphertextLen: ciphertextLen,
	}, nil
}

func decodeGPTReasoningSignature(sig string) ([]byte, error) {
	if decoded, err := base64.RawURLEncoding.DecodeString(sig); err == nil {
		return decoded, nil
	}
	if decoded, err := base64.URLEncoding.DecodeString(sig); err == nil {
		return decoded, nil
	}
	return nil, fmt.Errorf("invalid GPT reasoning signature: base64url decode failed")
}

// gptReasoningSignatureCharSet is the base64url alphabet, padding included.
var gptReasoningSignatureCharSet = base64AlphabetSet("-_=")

// firstInvalidGPTReasoningSignatureChar scans bytes against a lookup table for the
// same reason as its Grok counterpart: every legal character is ASCII, and a
// comparison chain mispredicts on nearly every byte of a multi-kilobyte reasoning
// blob. The offending rune is decoded only for the error message.
func firstInvalidGPTReasoningSignatureChar(sig string) (int, rune, bool) {
	for index := 0; index < len(sig); index++ {
		if !gptReasoningSignatureCharSet[sig[index]] {
			r, _ := utf8.DecodeRuneInString(sig[index:])
			return index, r, true
		}
	}
	return 0, 0, false
}
