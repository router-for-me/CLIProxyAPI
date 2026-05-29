package signature

import (
	"encoding/base64"
	"fmt"
	"strings"
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
	if index, r, ok := firstInvalidGPTReasoningSignatureChar(sig); ok {
		return nil, fmt.Errorf("invalid GPT reasoning signature: contains non-base64url character U+%04X at byte %d", r, index)
	}
	if !strings.HasPrefix(sig, "gAAAA") {
		return nil, fmt.Errorf("invalid GPT reasoning signature: expected gAAAA prefix")
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

func firstInvalidGPTReasoningSignatureChar(sig string) (int, rune, bool) {
	for index, r := range sig {
		switch {
		case r >= 'A' && r <= 'Z':
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		case r == '-' || r == '_' || r == '=':
		default:
			return index, r, true
		}
	}
	return 0, 0, false
}
