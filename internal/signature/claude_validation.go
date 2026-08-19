// Claude thinking signature validation.
//
// Spec reference: SIGNATURE-CHANNEL-SPEC.md
//
// Encoding detection (Spec section 3)
//
// Claude signatures use base64 encoding in one or two layers. The raw string's
// first character determines the encoding depth. This is mathematically
// equivalent to the spec's "decode first, check byte" approach:
//
//   - E prefix: single-layer, payload[0] == 0x12, first 6 bits = 000100,
//     base64 index 4 = E.
//   - R prefix: double-layer, inner[0] == E (0x45), first 6 bits = 010001,
//     base64 index 17 = R.
//
// Valid signatures can be normalized to R-form (double-layer base64) before
// sending to the Antigravity backend.
//
// # Protobuf structure (Spec sections 4.1 and 4.2) in strict mode only
//
// After base64 decoding to raw bytes, the first byte must be 0x12:
//
//	Top-level protobuf
//	|- Field 2 (bytes): container                    -> extractClaudeBytesField(payload, 2)
//	|  |- Field 1 (bytes): channel block             -> extractClaudeBytesField(container, 1)
//	|  |  |- Field 1 (varint): channel_id [required] -> routing_class (11 | 12)
//	|  |  |- Field 2 (varint): infra [optional]      -> infrastructure_class (aws=1 | google=2)
//	|  |  |- Field 3 (varint): version=2             -> skipped
//	|  |  |- Field 5 (bytes): ECDSA sig              -> skipped, per Spec section 11
//	|  |  |- Field 6 (bytes): model_text [optional]  -> schema_features
//	|  |  `- Field 7 (varint): unknown [optional]    -> schema_features
//	|  |- Field 2 (bytes): nonce 12B                 -> skipped
//	|  |- Field 3 (bytes): session 12B               -> skipped
//	|  |- Field 4 (bytes): SHA-384 48B               -> skipped
//	|  `- Field 5 (bytes): metadata                  -> skipped, per Spec section 11
//	`- Field 3 (varint): =1                          -> skipped
//
// Output dimensions (Spec section 8)
//
//	routing_class:        routing_class_11 | routing_class_12 | unknown
//	infrastructure_class: infra_default (absent) | infra_aws (1) | infra_google (2) | infra_unknown
//	schema_features:      compact_schema (len 70-72, no f6/f7) | extended_model_tagged_schema (f6 exists) | unknown
//	legacy_route_hint:    only for ch=11, legacy_default_group | legacy_aws_group | legacy_vertex_direct/proxy
//
// # Compatibility
//
// Verified against all confirmed spec samples (Anthropic Max 20x, Azure,
// Vertex, Bedrock) and legacy ch=11 signatures. Both single-layer (E) and
// double-layer (R) encodings are supported. Historical cache-mode modelGroup#
// prefixes are stripped.
//
// # CAIS envelope (newest Claude Code models)
//
// Newer Claude Code models wrap the channel block in a CAIS envelope whose
// decoded payload starts with 0x08 (top-level field 1 varint) instead of 0x12,
// so the base64 string starts with 'C' instead of 'E'/'R'. The envelope version
// varint in top-level field 1 is the ONLY structural difference from the layout
// above; everything below it is unchanged.
//
// The channel block itself belongs to a newer schema generation that is shared
// by both envelopes: channel_id 16, no infra field 2, plus a block kind (field
// 8) and a context id (field 11). Observed traffic confirms this schema
// appears under the classic 0x12 envelope too (opus-4-6/4-7/4-8, sonnet-5) and
// under the CAIS envelope (opus-5, fable-5), so envelope form and channel schema
// generation vary independently and must not be inferred from each other:
//
//	Top-level protobuf
//	|- Field 1 (varint): envelope version [required marker, observed as 2]
//	|- Field 2 (bytes): container [required]
//	|  `- Field 1 (bytes): channel block [required]
//	|     |- Field 1  (varint): channel_id [required, observed as 16]
//	|     |- Field 3  (varint): version [optional, observed as 2]
//	|     |- Field 5  (bytes):  ECDSA signature [required, observed as 64B]
//	|     |- Field 6  (bytes):  model_text [required, "claude-" prefixed]
//	|     |- Field 7  (varint): unknown [optional, observed as 1]
//	|     |- Field 8  (bytes):  block kind [optional, observed as "thinking"]
//	|     `- Field 11 (bytes):  context id [optional, canonical UUID]
//	`- Field 3 (varint): trailer [optional, observed as 1]
//
// CAIS validation is structural rather than an exact replay of the observed
// bytes. The payload is an opaque upstream-issued blob and rejecting it drops
// the whole thinking block, so only the fields that actually identify the format
// are required: the 0x08 marker, the nested container/channel block, the
// signature bytes, and the "claude-" model text. Observed-but-incidental values
// such as channel_id 16 or the "thinking" block kind are recorded for debugging
// and checked only for wire type, so an upstream field bump cannot silently
// erase conversation history.
//
// # Which provider emits which envelope
//
// Three providers serve Claude models, and the envelope depends on the model
// generation rather than on the provider:
//
//   - Claude Code OAuth subscription (Claude Code Max): opus-4-5, sonnet-4-6 and
//     every later model up to opus-5 and fable-5. Emits the CAIS envelope for
//     the newest models (opus-5, fable-5) and the single-layer E envelope for the
//     opus-4-6/4-7/4-8 and sonnet-5 generation — but both carry the same
//     channel_id 16 channel schema, so only the envelope differs.
//   - Claude Messages API: the full Claude model range, same envelopes as the
//     Claude Code OAuth subscription.
//   - Antigravity: only opus-4-6-think and sonnet-4-6, and always the
//     double-layer R form on Google infrastructure (infra_google). Antigravity
//     never issues a CAIS envelope or a single-layer E signature, and its replay
//     path requires R form, so CompatibleAntigravityClaudeThinkingSignature
//     rejects CAIS signatures.
//
// A single conversation therefore mixes envelopes whenever a user switches model
// generations or providers, and every form must stay replayable toward the
// provider that issued it.
package signature

import (
	"encoding/base64"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/tidwall/gjson"
	"google.golang.org/protobuf/encoding/protowire"
)

const MaxClaudeThinkingSignatureLen = 32 * 1024 * 1024

// ClaudeSignatureValidationOptions controls how far Claude thinking signatures
// are inspected. The base validation always checks the cache prefix, base64
// layers, and decoded 0x12 Claude payload marker. Strict mode additionally
// verifies the known protobuf tree used by Claude thinking signatures.
type ClaudeSignatureValidationOptions struct {
	// PrefixOnly only checks for an optional cache prefix followed by an E/R
	// Claude signature prefix. Use it to preserve legacy shallow cleanup.
	PrefixOnly bool
	// Base64Only checks the optional cache prefix, E/R Claude signature prefix,
	// and base64 layers without validating the decoded Claude marker or protobuf
	// tree. Use it for conservative request cleanup.
	Base64Only bool
	// AllowEmptySignatureWithEmptyText preserves empty thinking placeholders with
	// no signature and no thinking/text payload during strip operations.
	AllowEmptySignatureWithEmptyText bool
	Strict                           bool
}

// ClaudeSignatureTree describes the protobuf fields currently used for Claude
// thinking signature routing.
type ClaudeSignatureTree struct {
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

func claudeSignatureValidationOptions(opts []ClaudeSignatureValidationOptions) ClaudeSignatureValidationOptions {
	if len(opts) == 0 {
		return ClaudeSignatureValidationOptions{}
	}
	return opts[0]
}

// IsValidClaudeThinkingSignature returns whether rawSignature is a valid Claude
// thinking signature under the requested validation options.
func IsValidClaudeThinkingSignature(rawSignature string, opts ...ClaudeSignatureValidationOptions) bool {
	opt := claudeSignatureValidationOptions(opts)
	if opt.PrefixOnly {
		return HasClaudeThinkingSignaturePrefix(rawSignature)
	}
	if opt.Base64Only {
		return HasDecodableClaudeThinkingSignature(rawSignature)
	}
	_, err := NormalizeClaudeThinkingSignature(rawSignature, opts...)
	return err == nil
}

// HasDecodableClaudeThinkingSignature reports whether rawSignature has the
// Claude E/R shape and its expected base64 layer(s) can be decoded.
func HasDecodableClaudeThinkingSignature(rawSignature string) bool {
	sig := stripClaudeSignaturePrefix(rawSignature)
	if sig == "" || len(sig) > MaxClaudeThinkingSignatureLen {
		return false
	}

	switch sig[0] {
	case 'E':
		decoded, err := base64.StdEncoding.DecodeString(sig)
		return err == nil && len(decoded) > 0
	case 'R':
		decoded, err := base64.StdEncoding.DecodeString(sig)
		if err != nil || len(decoded) == 0 || decoded[0] != 'E' {
			return false
		}
		innerDecoded, err := base64.StdEncoding.DecodeString(string(decoded))
		return err == nil && len(innerDecoded) > 0
	default:
		return false
	}
}

// HasClaudeThinkingSignaturePrefix reports whether rawSignature has the Claude
// E/R signature prefix after stripping an optional cache prefix.
func HasClaudeThinkingSignaturePrefix(rawSignature string) bool {
	sig := stripClaudeSignaturePrefix(rawSignature)
	if sig == "" {
		return false
	}
	return sig[0] == 'E' || sig[0] == 'R'
}

func stripClaudeSignaturePrefix(rawSignature string) string {
	sig := strings.TrimSpace(rawSignature)
	if sig == "" {
		return ""
	}
	if idx := strings.IndexByte(sig, '#'); idx >= 0 {
		sig = strings.TrimSpace(sig[idx+1:])
	}
	return sig
}

// ValidateClaudeThinkingSignatures validates every thinking block signature in a
// Claude messages payload.
func ValidateClaudeThinkingSignatures(inputRawJSON []byte, opts ...ClaudeSignatureValidationOptions) error {
	messages := gjson.GetBytes(inputRawJSON, "messages")
	if !messages.IsArray() {
		return nil
	}

	opt := claudeSignatureValidationOptions(opts)
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

			if _, err := NormalizeClaudeThinkingSignature(rawSignature, opt); err != nil {
				return fmt.Errorf("messages[%d].content[%d]: %w", i, j, err)
			}
		}
	}

	return nil
}

// NormalizeClaudeThinkingSignature strips any cache prefix, validates the
// signature, and returns the double-layer R-form expected by Antigravity bypass
// mode.
func NormalizeClaudeThinkingSignature(rawSignature string, opts ...ClaudeSignatureValidationOptions) (string, error) {
	opt := claudeSignatureValidationOptions(opts)
	sig := stripClaudeSignaturePrefix(rawSignature)
	if sig == "" {
		return "", fmt.Errorf("empty signature")
	}

	if len(sig) > MaxClaudeThinkingSignatureLen {
		return "", fmt.Errorf("signature exceeds maximum length (%d bytes)", MaxClaudeThinkingSignatureLen)
	}

	switch sig[0] {
	case 'R':
		if err := validateClaudeDoubleLayerSignature(sig, opt); err != nil {
			return "", err
		}
		return sig, nil
	case 'E':
		if err := validateClaudeSingleLayerSignature(sig, opt); err != nil {
			return "", err
		}
		return base64.StdEncoding.EncodeToString([]byte(sig)), nil
	default:
		return "", fmt.Errorf("invalid signature: expected 'E' or 'R' prefix, got %q", string(sig[0]))
	}
}

// NormalizeClaudeProviderNativeThinkingSignature strips any cache prefix,
// validates the signature, and returns the single-layer E-form expected by
// Claude-native providers.
func NormalizeClaudeProviderNativeThinkingSignature(rawSignature string, opts ...ClaudeSignatureValidationOptions) (string, error) {
	opt := claudeSignatureValidationOptions(opts)
	sig := stripClaudeSignaturePrefix(rawSignature)
	if sig == "" {
		return "", fmt.Errorf("empty signature")
	}

	if len(sig) > MaxClaudeThinkingSignatureLen {
		return "", fmt.Errorf("signature exceeds maximum length (%d bytes)", MaxClaudeThinkingSignatureLen)
	}

	switch sig[0] {
	case 'E':
		if err := validateClaudeSingleLayerSignature(sig, opt); err != nil {
			return "", err
		}
		return sig, nil
	case 'R':
		if err := validateClaudeDoubleLayerSignature(sig, opt); err != nil {
			return "", err
		}
		decoded, err := base64.StdEncoding.DecodeString(sig)
		if err != nil {
			return "", fmt.Errorf("invalid double-layer signature: base64 decode failed: %w", err)
		}
		return string(decoded), nil
	default:
		return "", fmt.Errorf("invalid signature: expected 'E' or 'R' prefix, got %q", string(sig[0]))
	}
}

func validateClaudeDoubleLayerSignature(sig string, opt ClaudeSignatureValidationOptions) error {
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
	return validateClaudeSingleLayerSignatureContent(string(decoded), 2, opt)
}

func validateClaudeSingleLayerSignature(sig string, opt ClaudeSignatureValidationOptions) error {
	return validateClaudeSingleLayerSignatureContent(sig, 1, opt)
}

func validateClaudeSingleLayerSignatureContent(sig string, encodingLayers int, opt ClaudeSignatureValidationOptions) error {
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
	if !opt.Strict {
		return nil
	}
	_, err = InspectClaudeSignaturePayload(decoded, encodingLayers)
	return err
}

// InspectClaudeDoubleLayerSignature decodes and inspects a double-layer Claude
// thinking signature.
func InspectClaudeDoubleLayerSignature(sig string) (*ClaudeSignatureTree, error) {
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
	return inspectClaudeSingleLayerSignatureWithLayers(string(decoded), 2)
}

// InspectClaudeSingleLayerSignature decodes and inspects a single-layer Claude
// thinking signature.
func InspectClaudeSingleLayerSignature(sig string) (*ClaudeSignatureTree, error) {
	return inspectClaudeSingleLayerSignatureWithLayers(sig, 1)
}

func inspectClaudeSingleLayerSignatureWithLayers(sig string, encodingLayers int) (*ClaudeSignatureTree, error) {
	decoded, err := base64.StdEncoding.DecodeString(sig)
	if err != nil {
		return nil, fmt.Errorf("invalid single-layer signature: base64 decode failed: %w", err)
	}
	if len(decoded) == 0 {
		return nil, fmt.Errorf("invalid single-layer signature: empty after decode")
	}
	return InspectClaudeSignaturePayload(decoded, encodingLayers)
}

// InspectClaudeSignaturePayload inspects the decoded Claude thinking signature
// protobuf payload.
func InspectClaudeSignaturePayload(payload []byte, encodingLayers int) (*ClaudeSignatureTree, error) {
	if len(payload) == 0 {
		return nil, fmt.Errorf("invalid Claude signature: empty payload")
	}
	if payload[0] != 0x12 {
		return nil, fmt.Errorf("invalid Claude signature: expected first byte 0x12, got 0x%02x", payload[0])
	}
	container, err := extractClaudeBytesField(payload, 2, "top-level protobuf")
	if err != nil {
		return nil, err
	}
	channelBlock, err := extractClaudeBytesField(container, 1, "Claude Field 2 container")
	if err != nil {
		return nil, err
	}
	return inspectClaudeChannelBlock(channelBlock, encodingLayers)
}

func inspectClaudeChannelBlock(channelBlock []byte, encodingLayers int) (*ClaudeSignatureTree, error) {
	tree := &ClaudeSignatureTree{
		EncodingLayers:      encodingLayers,
		RoutingClass:        "unknown",
		InfrastructureClass: "infra_unknown",
		SchemaFeatures:      "unknown_schema_features",
	}
	haveChannelID := false
	hasField6 := false
	hasField7 := false

	err := walkClaudeProtobufFields(channelBlock, func(num protowire.Number, typ protowire.Type, raw []byte) error {
		switch num {
		case 1:
			if typ != protowire.VarintType {
				return fmt.Errorf("invalid Claude signature: Field 2.1.1 channel_id must be varint")
			}
			channelID, err := decodeClaudeVarintField(raw, "Field 2.1.1 channel_id")
			if err != nil {
				return err
			}
			tree.ChannelID = channelID
			haveChannelID = true
		case 2:
			if typ != protowire.VarintType {
				return fmt.Errorf("invalid Claude signature: Field 2.1.2 field2 must be varint")
			}
			field2, err := decodeClaudeVarintField(raw, "Field 2.1.2 field2")
			if err != nil {
				return err
			}
			tree.Field2 = &field2
		case 6:
			if typ != protowire.BytesType {
				return fmt.Errorf("invalid Claude signature: Field 2.1.6 model_text must be bytes")
			}
			modelBytes, err := decodeClaudeBytesField(raw, "Field 2.1.6 model_text")
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
			if _, err := decodeClaudeVarintField(raw, "Field 2.1.7"); err != nil {
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

func extractClaudeBytesField(msg []byte, fieldNum protowire.Number, scope string) ([]byte, error) {
	var value []byte
	err := walkClaudeProtobufFields(msg, func(num protowire.Number, typ protowire.Type, raw []byte) error {
		if num != fieldNum {
			return nil
		}
		if typ != protowire.BytesType {
			return fmt.Errorf("invalid Claude signature: %s field %d must be bytes", scope, fieldNum)
		}
		bytesValue, err := decodeClaudeBytesField(raw, fmt.Sprintf("%s field %d", scope, fieldNum))
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

func walkClaudeProtobufFields(msg []byte, visit func(num protowire.Number, typ protowire.Type, raw []byte) error) error {
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

func decodeClaudeVarintField(raw []byte, label string) (uint64, error) {
	value, n := protowire.ConsumeVarint(raw)
	if n < 0 {
		return 0, fmt.Errorf("invalid Claude signature: failed to decode %s: %w", label, protowire.ParseError(n))
	}
	return value, nil
}

func decodeClaudeBytesField(raw []byte, label string) ([]byte, error) {
	value, n := protowire.ConsumeBytes(raw)
	if n < 0 {
		return nil, fmt.Errorf("invalid Claude signature: failed to decode %s: %w", label, protowire.ParseError(n))
	}
	return value, nil
}

// claudeCAISSignatureMarker is the decoded first byte identifying the CAIS
// envelope (protobuf tag for top-level field 1, varint).
const claudeCAISSignatureMarker = 0x08

// claudeCAISModelTextPrefix is the model_text prefix that distinguishes a CAIS
// channel block from an arbitrary protobuf payload.
const claudeCAISModelTextPrefix = "claude-"

// ClaudeCAISSignatureInfo describes the locally inspected structure of a Claude
// CAIS thinking signature.
type ClaudeCAISSignatureInfo struct {
	FirstByte       byte
	EnvelopeVersion uint64
	ChannelID       uint64
	ModelText       string
	BlockKind       string
	ContextID       string

	SignatureLen int
}

// IsValidClaudeCAISSignature returns whether rawSignature is a valid Claude CAIS
// thinking signature.
func IsValidClaudeCAISSignature(rawSignature string) bool {
	_, err := InspectClaudeCAISSignature(rawSignature)
	return err == nil
}

// InspectClaudeCAISSignature decodes and validates a Claude CAIS thinking
// signature. See the CAIS envelope section in this file's package comment for
// the layout and for why validation is structural rather than exact.
func InspectClaudeCAISSignature(rawSignature string) (*ClaudeCAISSignatureInfo, error) {
	sig := stripClaudeSignaturePrefix(rawSignature)
	if sig == "" {
		return nil, fmt.Errorf("empty signature")
	}
	if len(sig) > MaxClaudeThinkingSignatureLen {
		return nil, fmt.Errorf("signature exceeds maximum length (%d bytes)", MaxClaudeThinkingSignatureLen)
	}
	// A payload whose first byte is 0x08 always base64-encodes to a string
	// starting with 'C' (0x08>>2 == 2). Checking that first keeps this validator
	// cheap on the hot paths that probe every signature, since classic Claude
	// (E/R) and Gemini envelopes are rejected without a base64 decode.
	if sig[0] != 'C' {
		return nil, fmt.Errorf("invalid Claude CAIS signature: expected 'C' prefix, got %q", string(sig[0]))
	}

	decoded, err := base64.StdEncoding.DecodeString(sig)
	if err != nil {
		return nil, fmt.Errorf("invalid Claude CAIS signature: base64 decode failed: %w", err)
	}
	if len(decoded) == 0 {
		return nil, fmt.Errorf("invalid Claude CAIS signature: empty after decode")
	}
	if decoded[0] != claudeCAISSignatureMarker {
		return nil, fmt.Errorf("invalid Claude CAIS signature: expected first byte 0x%02x, got 0x%02x", claudeCAISSignatureMarker, decoded[0])
	}

	info := &ClaudeCAISSignatureInfo{FirstByte: decoded[0]}

	var container []byte
	err = walkClaudeProtobufFields(decoded, func(num protowire.Number, typ protowire.Type, raw []byte) error {
		switch num {
		case 1:
			value, errField := decodeClaudeCAISVarint(raw, typ, "CAIS top-level field 1 envelope version")
			if errField != nil {
				return errField
			}
			info.EnvelopeVersion = value
		case 2:
			value, errField := decodeClaudeCAISBytes(raw, typ, "CAIS top-level field 2 container")
			if errField != nil {
				return errField
			}
			container = value
		case 3:
			if _, errField := decodeClaudeCAISVarint(raw, typ, "CAIS top-level field 3 trailer"); errField != nil {
				return errField
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if container == nil {
		return nil, fmt.Errorf("invalid Claude CAIS signature: missing top-level field 2 container")
	}

	var channelBlock []byte
	err = walkClaudeProtobufFields(container, func(num protowire.Number, typ protowire.Type, raw []byte) error {
		if num != 1 {
			return nil
		}
		value, errField := decodeClaudeCAISBytes(raw, typ, "CAIS container field 1 channel block")
		if errField != nil {
			return errField
		}
		channelBlock = value
		return nil
	})
	if err != nil {
		return nil, err
	}
	if channelBlock == nil {
		return nil, fmt.Errorf("invalid Claude CAIS signature: missing container field 1 channel block")
	}

	var haveChannelID, haveSignatureBytes, haveModelText bool
	err = walkClaudeProtobufFields(channelBlock, func(num protowire.Number, typ protowire.Type, raw []byte) error {
		switch num {
		case 1:
			value, errField := decodeClaudeCAISVarint(raw, typ, "CAIS channel field 1 channel_id")
			if errField != nil {
				return errField
			}
			info.ChannelID = value
			haveChannelID = true
		case 3:
			if _, errField := decodeClaudeCAISVarint(raw, typ, "CAIS channel field 3 version"); errField != nil {
				return errField
			}
		case 5:
			value, errField := decodeClaudeCAISBytes(raw, typ, "CAIS channel field 5 signature bytes")
			if errField != nil {
				return errField
			}
			if len(value) == 0 {
				return fmt.Errorf("invalid Claude CAIS signature: channel field 5 signature bytes must not be empty")
			}
			info.SignatureLen = len(value)
			haveSignatureBytes = true
		case 6:
			value, errField := decodeClaudeCAISUTF8(raw, typ, "CAIS channel field 6 model_text")
			if errField != nil {
				return errField
			}
			if !strings.HasPrefix(value, claudeCAISModelTextPrefix) {
				return fmt.Errorf("invalid Claude CAIS signature: channel field 6 model_text must start with %q, got %q", claudeCAISModelTextPrefix, value)
			}
			info.ModelText = value
			haveModelText = true
		case 7:
			if _, errField := decodeClaudeCAISVarint(raw, typ, "CAIS channel field 7"); errField != nil {
				return errField
			}
		case 8:
			value, errField := decodeClaudeCAISUTF8(raw, typ, "CAIS channel field 8 block kind")
			if errField != nil {
				return errField
			}
			info.BlockKind = value
		case 11:
			value, errField := decodeClaudeCAISUTF8(raw, typ, "CAIS channel field 11 context id")
			if errField != nil {
				return errField
			}
			if !isCanonicalUUID(value) {
				return fmt.Errorf("invalid Claude CAIS signature: channel field 11 context id must be a canonical UUID, got %q", value)
			}
			info.ContextID = value
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	switch {
	case !haveChannelID:
		return nil, fmt.Errorf("invalid Claude CAIS signature: missing channel field 1 channel_id")
	case !haveSignatureBytes:
		return nil, fmt.Errorf("invalid Claude CAIS signature: missing channel field 5 signature bytes")
	case !haveModelText:
		return nil, fmt.Errorf("invalid Claude CAIS signature: missing channel field 6 model_text")
	}

	return info, nil
}

func decodeClaudeCAISVarint(raw []byte, typ protowire.Type, label string) (uint64, error) {
	if typ != protowire.VarintType {
		return 0, fmt.Errorf("invalid Claude CAIS signature: %s must be varint", label)
	}
	return decodeClaudeVarintField(raw, label)
}

func decodeClaudeCAISBytes(raw []byte, typ protowire.Type, label string) ([]byte, error) {
	if typ != protowire.BytesType {
		return nil, fmt.Errorf("invalid Claude CAIS signature: %s must be bytes", label)
	}
	return decodeClaudeBytesField(raw, label)
}

func decodeClaudeCAISUTF8(raw []byte, typ protowire.Type, label string) (string, error) {
	value, err := decodeClaudeCAISBytes(raw, typ, label)
	if err != nil {
		return "", err
	}
	if !utf8.Valid(value) {
		return "", fmt.Errorf("invalid Claude CAIS signature: %s must be valid UTF-8", label)
	}
	return string(value), nil
}

func isCanonicalUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i := 0; i < len(s); i++ {
		b := s[i]
		switch i {
		case 8, 13, 18, 23:
			if b != '-' {
				return false
			}
		default:
			if !((b >= '0' && b <= '9') || (b >= 'a' && b <= 'f') || (b >= 'A' && b <= 'F')) {
				return false
			}
		}
	}
	return true
}
