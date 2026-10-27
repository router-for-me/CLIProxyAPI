package signature

import (
	"encoding/base64"
	"fmt"
	"strings"
)

// InspectAntigravityClaudeCAQSSignature validates the double-base64 CAQS
// envelope observed on Antigravity Claude 5.5. This is structural validation,
// not proof of cryptographic validity or cross-account replay compatibility.
// Only two encoding layers are supported; arbitrary recursive wrapping is not.
func InspectAntigravityClaudeCAQSSignature(rawSignature string) (*ClaudeCAISSignatureInfo, error) {
	sig := stripClaudeSignaturePrefix(rawSignature)
	if len(sig) == 0 || len(sig) > MaxClaudeThinkingSignatureLen {
		return nil, fmt.Errorf("invalid Antigravity CAQS signature length")
	}
	if sig[0] != 'Q' || strings.ContainsAny(sig, "\r\n") {
		return nil, fmt.Errorf("invalid Antigravity CAQS wrapper")
	}
	inner, errDecode := base64.StdEncoding.Strict().DecodeString(sig)
	if errDecode != nil {
		return nil, fmt.Errorf("invalid Antigravity CAQS encoding: %w", errDecode)
	}
	innerText := string(inner)
	if len(innerText) == 0 || innerText[0] != 'C' || strings.ContainsAny(innerText, " \t\r\n#") {
		return nil, fmt.Errorf("invalid Antigravity CAQS inner encoding")
	}
	decoded, errInner := base64.StdEncoding.Strict().DecodeString(innerText)
	if errInner != nil {
		return nil, fmt.Errorf("invalid Antigravity CAQS inner encoding: %w", errInner)
	}
	info, errInspect := inspectClaudeCAISPayload(decoded, true)
	if errInspect != nil {
		return nil, fmt.Errorf("invalid Antigravity CAQS payload: %w", errInspect)
	}
	// Scope this replay shape to the Google thinking envelope schema.
	// ChannelID rotates across upstream key/channel generations (e.g. 18 -> 19)
	// and is already required to be present as a varint by inspectClaudeCAISPayload.
	if info.EnvelopeVersion != 4 ||
		info.Infrastructure == nil || *info.Infrastructure != 2 ||
		info.BlockKind != "thinking" || !info.SignatureInContainer {
		return nil, fmt.Errorf("unsupported Antigravity CAQS envelope or channel schema")
	}
	return info, nil
}
