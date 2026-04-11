// Claude thinking signature validation for Antigravity bypass mode.
//
// Spec reference: SIGNATURE-CHANNEL-SPEC.md
//
// # Encoding Detection (Spec §3)
//
// Claude signatures use base64 encoding in one or two layers. The raw string's
// first character determines the encoding depth — this is mathematically equivalent
// to the spec's "decode first, check byte" approach:
//
//   - 'E' prefix → single-layer: payload[0]==0x12, first 6 bits = 000100 = base64 index 4 = 'E'
//   - 'R' prefix → double-layer: inner[0]=='E' (0x45), first 6 bits = 010001 = base64 index 17 = 'R'
//
// All valid signatures are normalized to R-form (double-layer base64) before
// sending to the Antigravity backend.
//
// # Protobuf Structure (Spec §4.1, §4.2) — strict mode only
//
// After base64 decoding to raw bytes (first byte must be 0x12):
//
//	Top-level protobuf
//	├── Field 2 (bytes): container                    ← extractBytesField(payload, 2)
//	│   ├── Field 1 (bytes): channel block            ← extractBytesField(container, 1)
//	│   │   ├── Field 1 (varint): channel_id [required] → routing_class (11 | 12)
//	│   │   ├── Field 2 (varint): infra      [optional] → infrastructure_class (aws=1 | google=2)
//	│   │   ├── Field 3 (varint): version=2  [skipped]
//	│   │   ├── Field 5 (bytes):  ECDSA sig  [skipped, per Spec §11]
//	│   │   ├── Field 6 (bytes):  model_text [optional] → schema_features
//	│   │   └── Field 7 (varint): unknown    [optional] → schema_features
//	│   ├── Field 2 (bytes): nonce 12B       [skipped]
//	│   ├── Field 3 (bytes): session 12B     [skipped]
//	│   ├── Field 4 (bytes): SHA-384 48B     [skipped]
//	│   └── Field 5 (bytes): metadata        [skipped, per Spec §11]
//	└── Field 3 (varint): =1                 [skipped]
//
// # Output Dimensions (Spec §8)
//
//	routing_class:        routing_class_11 | routing_class_12 | unknown
//	infrastructure_class: infra_default (absent) | infra_aws (1) | infra_google (2) | infra_unknown
//	schema_features:      compact_schema (len 70-72, no f6/f7) | extended_model_tagged_schema (f6 exists) | unknown
//	legacy_route_hint:    only for ch=11 — legacy_default_group | legacy_aws_group | legacy_vertex_direct/proxy
//
// # Compatibility
//
// Verified against all confirmed spec samples (Anthropic Max 20x, Azure, Vertex,
// Bedrock) and legacy ch=11 signatures. Both single-layer (E) and double-layer (R)
// encodings are supported. Historical cache-mode 'modelGroup#' prefixes are stripped.
package claude

import (
	"encoding/base64"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/cache"
	"github.com/tidwall/gjson"
	"google.golang.org/protobuf/encoding/protowire"
)

const maxBypassSignatureLen = 32 * 1024 * 1024

type claudeSignatureTree struct {
	EncodingLayers      int
	ChannelID           uint64
	Field2              *uint64
	RoutingClass        string
	InfrastructureClass string
	SchemaFeatures      string
	ModelText           string
	LegacyRouteHint     string
	HasField7           bool
}

func ValidateClaudeBypassSignatures(inputRawJSON []byte) error {
	messages := gjson.GetBytes(inputRawJSON, "messages")
	if !messages.IsArray() {
		return nil
	}

	messageResults := messages.Array()
	for i := 0; i < len(messageResults); i++ {
		contentResults := messageResults[i].Get("content")
		if !contentResults.IsArray() {
			continue
		}
		parts := contentResults.Array()
		for j := 0; j < len(parts); j++ {
			part := parts[j]
			if part.Get("type").String() != "thinking" {
				continue
			}

			rawSignature := strings.TrimSpace(part.Get("signature").String())
			if rawSignature == "" {
				return fmt.Errorf("messages[%d].content[%d]: missing thinking signature", i, j)
			}

			if _, err := normalizeClaudeBypassSignature(rawSignature); err != nil {
				return fmt.Errorf("messages[%d].content[%d]: %w", i, j, err)
			}
		}
	}

	return nil
}

func normalizeClaudeBypassSignature(rawSignature string) (string, error) {
	sig := strings.TrimSpace(rawSignature)
	if sig == "" {
		return "", fmt.Errorf("empty signature")
	}

	if idx := strings.IndexByte(sig, '#'); idx >= 0 {
		sig = strings.TrimSpace(sig[idx+1:])
	}

	if sig == "" {
		return "", fmt.Errorf("empty signature after stripping prefix")
	}

	if len(sig) > maxBypassSignatureLen {
		return "", fmt.Errorf("signature exceeds maximum length (%d bytes)", maxBypassSignatureLen)
	}

	switch sig[0] {
	case 'R':
		if err := validateDoubleLayerSignature(sig); err != nil {
			return "", err
		}
		return sig, nil
	case 'E':
		if err := validateSingleLayerSignature(sig); err != nil {
			return "", err
		}
		return base64.StdEncoding.EncodeToString([]byte(sig)), nil
	default:
		return "", fmt.Errorf("invalid signature: expected 'E' or 'R' prefix, got %q", string(sig[0]))
	}
}

func validateDoubleLayerSignature(sig string) error {
	decoded, err := base64.StdEncoding.DecodeString(sig)
	if err != nil {
		return fmt.Errorf("invalid double-layer signature: base64 decode failed: %w", err)
	}
	if len(decoded) == 0 {
		return fmt.Errorf("invalid double-layer signature: empty after decode")
	}
	if decoded[0] != 'E' {
		return fmt.Errorf("invalid double-layer signature: inner does not start with 'E', got 0x%02x", decoded[0])
	}
	return validateSingleLayerSignatureContent(string(decoded), 2)
}

func validateSingleLayerSignature(sig string) error {
	return validateSingleLayerSignatureContent(sig, 1)
}

func validateSingleLayerSignatureContent(sig string, encodingLayers int) error {
	decoded, err := base64.StdEncoding.DecodeString(sig)
	if err != nil {
		return fmt.Errorf("invalid single-layer signature: base64 decode failed: %w", err)
	}
	if len(decoded) == 0 {
		return fmt.Errorf("invalid single-layer signature: empty after decode")
	}
	if decoded[0] != 0x12 {
		return fmt.Errorf("invalid Claude signature: expected first byte 0x12, got 0x%02x", decoded[0])
	}
	if !cache.SignatureBypassStrictMode() {
		return nil
	}
	_, err = inspectClaudeSignaturePayload(decoded, encodingLayers)
	return err
}

func inspectDoubleLayerSignature(sig string) (*claudeSignatureTree, error) {
	decoded, err := base64.StdEncoding.DecodeString(sig)
	if err != nil {
		return nil, fmt.Errorf("invalid double-layer signature: base64 decode failed: %w", err)
	}
	if len(decoded) == 0 {
		return nil, fmt.Errorf("invalid double-layer signature: empty after decode")
	}
	if decoded[0] != 'E' {
		return nil, fmt.Errorf("invalid double-layer signature: inner does not start with 'E', got 0x%02x", decoded[0])
	}
	return inspectSingleLayerSignatureWithLayers(string(decoded), 2)
}

func inspectSingleLayerSignature(sig string) (*claudeSignatureTree, error) {
	return inspectSingleLayerSignatureWithLayers(sig, 1)
}

func inspectSingleLayerSignatureWithLayers(sig string, encodingLayers int) (*claudeSignatureTree, error) {
	decoded, err := base64.StdEncoding.DecodeString(sig)
	if err != nil {
		return nil, fmt.Errorf("invalid single-layer signature: base64 decode failed: %w", err)
	}
	if len(decoded) == 0 {
		return nil, fmt.Errorf("invalid single-layer signature: empty after decode")
	}
	return inspectClaudeSignaturePayload(decoded, encodingLayers)
}

func inspectClaudeSignaturePayload(payload []byte, encodingLayers int) (*claudeSignatureTree, error) {
	if len(payload) == 0 {
		return nil, fmt.Errorf("invalid Claude signature: empty payload")
	}
	if payload[0] != 0x12 {
		return nil, fmt.Errorf("invalid Claude signature: expected first byte 0x12, got 0x%02x", payload[0])
	}
	container, err := extractBytesField(payload, 2, "top-level protobuf")
	if err != nil {
		return nil, err
	}
	channelBlock, err := extractBytesField(container, 1, "Claude Field 2 container")
	if err != nil {
		return nil, err
	}
	return inspectClaudeChannelBlock(channelBlock, encodingLayers)
}

func inspectClaudeChannelBlock(channelBlock []byte, encodingLayers int) (*claudeSignatureTree, error) {
	tree := &claudeSignatureTree{
		EncodingLayers:      encodingLayers,
		RoutingClass:        "unknown",
		InfrastructureClass: "infra_unknown",
		SchemaFeatures:      "unknown_schema_features",
	}
	haveChannelID := false
	hasField6 := false
	hasField7 := false

	err := walkProtobufFields(channelBlock, func(num protowire.Number, typ protowire.Type, raw []byte) error {
		switch num {
		case 1:
			if typ != protowire.VarintType {
				return fmt.Errorf("invalid Claude signature: Field 2.1.1 channel_id must be varint")
			}
			channelID, err := decodeVarintField(raw, "Field 2.1.1 channel_id")
			if err != nil {
				return err
			}
			tree.ChannelID = channelID
			haveChannelID = true
		case 2:
			if typ != protowire.VarintType {
				return fmt.Errorf("invalid Claude signature: Field 2.1.2 field2 must be varint")
			}
			field2, err := decodeVarintField(raw, "Field 2.1.2 field2")
			if err != nil {
				return err
			}
			tree.Field2 = &field2
		case 6:
			if typ != protowire.BytesType {
				return fmt.Errorf("invalid Claude signature: Field 2.1.6 model_text must be bytes")
			}
			modelBytes, err := decodeBytesField(raw, "Field 2.1.6 model_text")
			if err != nil {
				return err
			}
			if !utf8.Valid(modelBytes) {
				return fmt.Errorf("invalid Claude signature: Field 2.1.6 model_text is not valid UTF-8")
			}
			tree.ModelText = string(modelBytes)
			hasField6 = true
		case 7:
			if typ != protowire.VarintType {
				return fmt.Errorf("invalid Claude signature: Field 2.1.7 must be varint")
			}
			if _, err := decodeVarintField(raw, "Field 2.1.7"); err != nil {
				return err
			}
			hasField7 = true
			tree.HasField7 = true
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if !haveChannelID {
		return nil, fmt.Errorf("invalid Claude signature: missing Field 2.1.1 channel_id")
	}

	switch tree.ChannelID {
	case 11:
		tree.RoutingClass = "routing_class_11"
	case 12:
		tree.RoutingClass = "routing_class_12"
	}

	if tree.Field2 == nil {
		tree.InfrastructureClass = "infra_default"
	} else {
		switch *tree.Field2 {
		case 1:
			tree.InfrastructureClass = "infra_aws"
		case 2:
			tree.InfrastructureClass = "infra_google"
		default:
			tree.InfrastructureClass = "infra_unknown"
		}
	}

	switch {
	case hasField6:
		tree.SchemaFeatures = "extended_model_tagged_schema"
	case !hasField6 && !hasField7 && len(channelBlock) >= 70 && len(channelBlock) <= 72:
		tree.SchemaFeatures = "compact_schema"
	}

	if tree.ChannelID == 11 {
		switch {
		case tree.Field2 == nil:
			tree.LegacyRouteHint = "legacy_default_group"
		case *tree.Field2 == 1:
			tree.LegacyRouteHint = "legacy_aws_group"
		case *tree.Field2 == 2 && tree.EncodingLayers == 2:
			tree.LegacyRouteHint = "legacy_vertex_direct"
		case *tree.Field2 == 2 && tree.EncodingLayers == 1:
			tree.LegacyRouteHint = "legacy_vertex_proxy"
		}
	}

	return tree, nil
}

func extractBytesField(msg []byte, fieldNum protowire.Number, scope string) ([]byte, error) {
	var value []byte
	err := walkProtobufFields(msg, func(num protowire.Number, typ protowire.Type, raw []byte) error {
		if num != fieldNum {
			return nil
		}
		if typ != protowire.BytesType {
			return fmt.Errorf("invalid Claude signature: %s field %d must be bytes", scope, fieldNum)
		}
		bytesValue, err := decodeBytesField(raw, fmt.Sprintf("%s field %d", scope, fieldNum))
		if err != nil {
			return err
		}
		value = bytesValue
		return nil
	})
	if err != nil {
		return nil, err
	}
	if value == nil {
		return nil, fmt.Errorf("invalid Claude signature: missing %s field %d", scope, fieldNum)
	}
	return value, nil
}

func walkProtobufFields(msg []byte, visit func(num protowire.Number, typ protowire.Type, raw []byte) error) error {
	for offset := 0; offset < len(msg); {
		num, typ, n := protowire.ConsumeTag(msg[offset:])
		if n < 0 {
			return fmt.Errorf("invalid Claude signature: malformed protobuf tag: %w", protowire.ParseError(n))
		}
		offset += n
		valueLen := protowire.ConsumeFieldValue(num, typ, msg[offset:])
		if valueLen < 0 {
			return fmt.Errorf("invalid Claude signature: malformed protobuf field %d: %w", num, protowire.ParseError(valueLen))
		}
		fieldRaw := msg[offset : offset+valueLen]
		if err := visit(num, typ, fieldRaw); err != nil {
			return err
		}
		offset += valueLen
	}
	return nil
}

func decodeVarintField(raw []byte, label string) (uint64, error) {
	value, n := protowire.ConsumeVarint(raw)
	if n < 0 {
		return 0, fmt.Errorf("invalid Claude signature: failed to decode %s: %w", label, protowire.ParseError(n))
	}
	return value, nil
}

func decodeBytesField(raw []byte, label string) ([]byte, error) {
	value, n := protowire.ConsumeBytes(raw)
	if n < 0 {
		return nil, fmt.Errorf("invalid Claude signature: failed to decode %s: %w", label, protowire.ParseError(n))
	}
	return value, nil
}
