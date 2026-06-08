// Gemini thought signature validation notes.
//
// The Antigravity Gemini request translator can preserve provider-compatible
// Gemini thought signatures and uses the skip sentinel only for synthetic or
// incompatible model parts.
//
// Gemini 3 and later models can return thoughtSignature on model content parts.
// Function-call parts are the strict case: when a model functionCall is replayed
// with a following functionResponse, Gemini validates that the original
// functionCall part still carries its provider-issued thoughtSignature. Text or
// other non-functionCall parts may also carry a signature; those should be
// preserved when replaying native Gemini history, but they are not the primary
// validation gate.
//
// Synthetic history and migration from other model families are different. If a
// functionCall part was not produced by Gemini API, there is no real signature
// to preserve. Gemini documents two bypass sentinels for that case:
//
//   - "skip_thought_signature_validator"
//   - "context_engineering_is_the_way_to_go"
//
// This repo currently emits "skip_thought_signature_validator" for non-Claude
// Antigravity Gemini model parts that contain functionCall, thought, or an
// existing thoughtSignature. That is a request-shape compatibility policy, not a
// proof that the replaced signature was malformed.
//
// This validator is intentionally more conservative than a decrypting verifier.
// Claude has a known E/R base64 envelope and a protobuf tree in this package.
// Gemini thought signatures are opaque provider state here, so local validation
// checks only the transport-level protobuf envelope and leaves the wrapped
// provider payload uninterpreted.
//
// Validation tiers:
//
//   - Sentinel tier: accept the documented bypass sentinels only when the
//     model functionCall is synthetic, migrated, or otherwise not traceable to a
//     prior Gemini model response in the same conversation.
//   - Opaque-shape tier: for real Gemini signatures, require a non-empty string,
//     bounded length, successful standard base64 decoding, and a known protobuf
//     envelope when the caller needs provider compatibility. Observed samples
//     currently include Gemini 3.x field-2 -> field-1 payloads and Gemini 2.5
//     repeated field-1 payloads. Base64 UUID payloads are classified separately
//     and should be replaced with the bypass sentinel rather than replayed.
//   - Replay tier: real validation means preserving the exact model part that
//     came from Gemini, including its thoughtSignature, id/name/function args,
//     part index, and ordering relative to sibling parallel function calls.
//   - Tool pairing tier: functionResponse parts must match the preceding
//     functionCall id/name and must not be interleaved between parallel calls.
//     The valid shape is all model functionCalls first, then their responses.
//   - Compatibility tier: GPT-compatible Gemini traffic stores the same state
//     under tool_calls[].extra_content.google.thought_signature. If that path is
//     translated back to native Gemini, the value must stay attached to the same
//     assistant tool call.
//
// Important non-goals:
//
//   - Do not treat a Gemini thoughtSignature as a Claude signature. Similar
//     base64 prefixes are not provenance.
//   - Do not attach a signature to user functionResponse/tool-result parts.
//   - Do not log complete signatures during validation failures; log only field
//     paths, lengths, and redacted prefixes.
//   - Do not preserve client-provided signatures across model/provider/session
//     boundaries unless the request pipeline can prove they came from the same
//     Gemini conversation state.
package signature

import (
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/tidwall/gjson"
	"google.golang.org/protobuf/encoding/protowire"
)

const (
	MaxGeminiThoughtSignatureLen = 32 * 1024 * 1024

	GeminiSkipThoughtSignatureValidator = "skip_thought_signature_validator"
	GeminiContextEngineeringBypass      = "context_engineering_is_the_way_to_go"
)

// GeminiThoughtSignatureValidationOptions controls how much local validation is
// applied to Gemini thought signatures. This validation checks only the opaque
// transport envelope; it does not prove that a signature came from Gemini or can
// be decrypted by Gemini.
type GeminiThoughtSignatureValidationOptions struct {
	// AllowBypassSentinel accepts Gemini's documented synthetic-history bypass
	// sentinels. Keep this false when validating provider-issued signatures.
	AllowBypassSentinel bool
	// RequireKnownEnvelope requires the decoded payload to match one of the
	// protobuf envelopes observed in Gemini samples. This rejects opaque base64
	// values such as base64 UUIDs.
	RequireKnownEnvelope bool
	// RequireObservedMarker requires the decoded payload to start with 0x12.
	// Current Gemini 3.x samples show this marker, but Gemini 2.5 samples use a
	// different protobuf prefix, so this should be used only for narrow Gemini 3
	// experiments.
	RequireObservedMarker bool
}

type GeminiThoughtSignatureEnvelope string

const (
	GeminiThoughtSignatureEnvelopeUnknown        GeminiThoughtSignatureEnvelope = "unknown"
	GeminiThoughtSignatureEnvelopeProtobufField1 GeminiThoughtSignatureEnvelope = "protobuf_field_1"
	GeminiThoughtSignatureEnvelopeProtobufField2 GeminiThoughtSignatureEnvelope = "protobuf_field_2"
	GeminiThoughtSignatureEnvelopeASCIIUUID      GeminiThoughtSignatureEnvelope = "ascii_uuid"
)

// GeminiThoughtSignatureInfo describes the locally inspectable properties of an
// opaque Gemini thought signature.
type GeminiThoughtSignatureInfo struct {
	IsBypassSentinel  bool
	BypassSentinel    string
	DecodedLen        int
	FirstByte         byte
	HasObservedMarker bool
	KnownEnvelope     bool
	Envelope          GeminiThoughtSignatureEnvelope
	RecordCount       int
	OpaquePayloadLen  int
}

type geminiFunctionCallRef struct {
	id   string
	name string
	path string
}

type geminiFunctionResponseRef struct {
	part gjson.Result
	path string
}

func geminiThoughtSignatureValidationOptions(opts []GeminiThoughtSignatureValidationOptions) GeminiThoughtSignatureValidationOptions {
	if len(opts) == 0 {
		return GeminiThoughtSignatureValidationOptions{}
	}
	return opts[0]
}

// IsGeminiThoughtSignatureBypass reports whether rawSignature is one of
// Gemini's documented bypass sentinels for synthetic or migrated function-call
// history.
func IsGeminiThoughtSignatureBypass(rawSignature string) bool {
	switch strings.TrimSpace(rawSignature) {
	case GeminiSkipThoughtSignatureValidator, GeminiContextEngineeringBypass:
		return true
	default:
		return false
	}
}

// IsValidGeminiThoughtSignature returns whether rawSignature has a valid local
// Gemini thought-signature shape under opts.
func IsValidGeminiThoughtSignature(rawSignature string, opts ...GeminiThoughtSignatureValidationOptions) bool {
	_, err := InspectGeminiThoughtSignature(rawSignature, opts...)
	return err == nil
}

// InspectGeminiThoughtSignature validates and inspects the local transport
// shape of a Gemini thought signature. It intentionally treats provider-issued
// signatures as opaque base64 payloads.
func InspectGeminiThoughtSignature(rawSignature string, opts ...GeminiThoughtSignatureValidationOptions) (*GeminiThoughtSignatureInfo, error) {
	opt := geminiThoughtSignatureValidationOptions(opts)
	sig := strings.TrimSpace(rawSignature)
	if sig == "" {
		return nil, fmt.Errorf("empty Gemini thought signature")
	}

	if IsGeminiThoughtSignatureBypass(sig) {
		if !opt.AllowBypassSentinel {
			return nil, fmt.Errorf("Gemini thought signature bypass sentinel is not allowed")
		}
		return &GeminiThoughtSignatureInfo{
			IsBypassSentinel: true,
			BypassSentinel:   sig,
		}, nil
	}

	decoded, err := decodeGeminiThoughtSignature(sig)
	if err != nil {
		return nil, err
	}
	if len(decoded) == 0 {
		return nil, fmt.Errorf("invalid Gemini thought signature: empty decoded payload")
	}

	info := &GeminiThoughtSignatureInfo{
		DecodedLen:        len(decoded),
		FirstByte:         decoded[0],
		HasObservedMarker: decoded[0] == 0x12,
	}
	info.Envelope, info.KnownEnvelope = classifyGeminiThoughtSignatureEnvelope(decoded)
	info.RecordCount, info.OpaquePayloadLen = inspectGeminiEnvelope(decoded, info.Envelope)
	if opt.RequireKnownEnvelope && !info.KnownEnvelope {
		return nil, fmt.Errorf("invalid Gemini thought signature: unknown envelope %q", info.Envelope)
	}
	if opt.RequireObservedMarker && !info.HasObservedMarker {
		return nil, fmt.Errorf("invalid Gemini thought signature: expected observed marker 0x12, got 0x%02x", info.FirstByte)
	}

	return info, nil
}

// ValidateGeminiThoughtSignatures validates thoughtSignature fields in a Gemini
// native payload. Function-call parts must have a valid signature. Other parts
// are optional, but if a thoughtSignature field is present it must be valid.
func ValidateGeminiThoughtSignatures(inputRawJSON []byte, opts ...GeminiThoughtSignatureValidationOptions) error {
	contents, contentsPath := geminiContents(inputRawJSON)
	if !contents.IsArray() {
		return nil
	}

	contentResults := contents.Array()
	for i := 0; i < len(contentResults); i++ {
		parts := contentResults[i].Get("parts")
		if !parts.IsArray() {
			continue
		}

		partResults := parts.Array()
		for j := 0; j < len(partResults); j++ {
			part := partResults[j]
			hasFunctionCall := part.Get("functionCall").Exists()
			hasSignature := part.Get("thoughtSignature").Exists()
			if !hasFunctionCall && !hasSignature {
				continue
			}

			partPath := fmt.Sprintf("%s[%d].parts[%d]", contentsPath, i, j)
			rawSignature := strings.TrimSpace(part.Get("thoughtSignature").String())
			if rawSignature == "" {
				if hasFunctionCall {
					return fmt.Errorf("%s: missing thoughtSignature on functionCall", partPath)
				}
				return fmt.Errorf("%s: empty thoughtSignature", partPath)
			}

			if _, err := InspectGeminiThoughtSignature(rawSignature, opts...); err != nil {
				return fmt.Errorf("%s: %w", partPath, err)
			}
		}
	}

	return nil
}

// ValidateGeminiFunctionCallPairing validates the replay shape around Gemini
// functionCall and functionResponse parts. It checks id/name pairing and
// prevents response parts from being interleaved inside the same content as
// function calls. It allows a final pending functionCall group because callers
// may validate a freshly returned model step before tool outputs exist.
func ValidateGeminiFunctionCallPairing(inputRawJSON []byte) error {
	contents, contentsPath := geminiContents(inputRawJSON)
	if !contents.IsArray() {
		return nil
	}

	var pending []geminiFunctionCallRef
	contentResults := contents.Array()
	for i := 0; i < len(contentResults); i++ {
		parts := contentResults[i].Get("parts")
		if !parts.IsArray() {
			continue
		}

		var calls []geminiFunctionCallRef
		var responses []geminiFunctionResponseRef
		partResults := parts.Array()
		for j := 0; j < len(partResults); j++ {
			part := partResults[j]
			partPath := fmt.Sprintf("%s[%d].parts[%d]", contentsPath, i, j)
			if call := part.Get("functionCall"); call.Exists() {
				if call.Get("name").String() == "" {
					return fmt.Errorf("%s: missing functionCall.name", partPath)
				}
				calls = append(calls, geminiFunctionCallRef{
					id:   call.Get("id").String(),
					name: call.Get("name").String(),
					path: partPath,
				})
			}
			if response := part.Get("functionResponse"); response.Exists() {
				responses = append(responses, geminiFunctionResponseRef{
					part: part,
					path: partPath,
				})
			}
		}

		if len(calls) > 0 && len(responses) > 0 {
			return fmt.Errorf("%s[%d]: functionCall and functionResponse parts must not be interleaved in the same content", contentsPath, i)
		}

		if len(calls) > 0 {
			if len(pending) > 0 {
				return fmt.Errorf("%s[%d]: functionCall appears before %d pending functionResponse part(s)", contentsPath, i, len(pending))
			}
			pending = calls
			continue
		}

		if len(responses) == 0 {
			continue
		}
		if len(pending) == 0 {
			return fmt.Errorf("%s[%d]: functionResponse without preceding functionCall", contentsPath, i)
		}
		if len(responses) != len(pending) {
			return fmt.Errorf("%s[%d]: functionResponse count %d does not match pending functionCall count %d", contentsPath, i, len(responses), len(pending))
		}

		for j := 0; j < len(responses); j++ {
			partPath := responses[j].path
			response := responses[j].part.Get("functionResponse")
			call := pending[j]
			responseID := response.Get("id").String()
			responseName := response.Get("name").String()

			if call.id != "" && responseID == "" {
				return fmt.Errorf("%s: missing functionResponse.id for %s", partPath, call.path)
			}
			if call.id != "" && responseID != call.id {
				return fmt.Errorf("%s: functionResponse.id %q does not match functionCall.id %q at %s", partPath, responseID, call.id, call.path)
			}
			if responseName == "" {
				return fmt.Errorf("%s: missing functionResponse.name", partPath)
			}
			if call.name != "" && responseName != call.name {
				return fmt.Errorf("%s: functionResponse.name %q does not match functionCall.name %q at %s", partPath, responseName, call.name, call.path)
			}
		}

		pending = nil
	}

	return nil
}

func decodeGeminiThoughtSignature(sig string) ([]byte, error) {
	if len(sig) > MaxGeminiThoughtSignatureLen {
		return nil, fmt.Errorf("Gemini thought signature exceeds maximum length (%d bytes)", MaxGeminiThoughtSignatureLen)
	}

	decoded, err := base64.StdEncoding.DecodeString(sig)
	if err == nil {
		return decoded, nil
	}
	if decoded, rawErr := base64.RawStdEncoding.DecodeString(sig); rawErr == nil {
		return decoded, nil
	}

	return nil, fmt.Errorf("invalid Gemini thought signature: base64 decode failed: %w", err)
}

func classifyGeminiThoughtSignatureEnvelope(decoded []byte) (GeminiThoughtSignatureEnvelope, bool) {
	if len(decoded) == 0 {
		return GeminiThoughtSignatureEnvelopeUnknown, false
	}
	if isASCIIUUIDBytes(decoded) {
		return GeminiThoughtSignatureEnvelopeASCIIUUID, false
	}
	switch {
	case isGeminiField1Envelope(decoded):
		return GeminiThoughtSignatureEnvelopeProtobufField1, true
	case isGeminiField2Envelope(decoded):
		return GeminiThoughtSignatureEnvelopeProtobufField2, true
	default:
		return GeminiThoughtSignatureEnvelopeUnknown, false
	}
}

func isGeminiField1Envelope(decoded []byte) bool {
	info, ok := inspectGeminiField1Envelope(decoded)
	return ok && info.RecordCount > 0
}

func isGeminiField2Envelope(decoded []byte) bool {
	info, ok := inspectGeminiField2Envelope(decoded)
	return ok && info.RecordCount == 1 && info.OpaquePayloadLen > 0
}

func inspectGeminiEnvelope(decoded []byte, envelope GeminiThoughtSignatureEnvelope) (recordCount int, opaquePayloadLen int) {
	switch envelope {
	case GeminiThoughtSignatureEnvelopeProtobufField1:
		if info, ok := inspectGeminiField1Envelope(decoded); ok {
			return info.RecordCount, info.OpaquePayloadLen
		}
	case GeminiThoughtSignatureEnvelopeProtobufField2:
		if info, ok := inspectGeminiField2Envelope(decoded); ok {
			return info.RecordCount, info.OpaquePayloadLen
		}
	}
	return 0, 0
}

type geminiEnvelopeInfo struct {
	RecordCount      int
	OpaquePayloadLen int
}

func inspectGeminiField1Envelope(decoded []byte) (geminiEnvelopeInfo, bool) {
	var info geminiEnvelopeInfo
	offset := 0
	for offset < len(decoded) {
		num, typ, n := protowire.ConsumeTag(decoded[offset:])
		if n < 0 || num != 1 || typ != protowire.BytesType {
			return geminiEnvelopeInfo{}, false
		}
		offset += n
		value, n := protowire.ConsumeBytes(decoded[offset:])
		if n < 0 || !isLikelyGeminiOpaquePayload(value) {
			return geminiEnvelopeInfo{}, false
		}
		info.RecordCount++
		info.OpaquePayloadLen += len(value)
		offset += n
	}
	return info, offset == len(decoded) && info.RecordCount > 0
}

func inspectGeminiField2Envelope(decoded []byte) (geminiEnvelopeInfo, bool) {
	value, ok := consumeGeminiField2Field1Value(decoded)
	if !ok || !isLikelyGeminiOpaquePayload(value) {
		return geminiEnvelopeInfo{}, false
	}
	return geminiEnvelopeInfo{
		RecordCount:      1,
		OpaquePayloadLen: len(value),
	}, true
}

func consumeGeminiField2Field1Value(decoded []byte) ([]byte, bool) {
	num, typ, n := protowire.ConsumeTag(decoded)
	if n < 0 || num != 2 || typ != protowire.BytesType {
		return nil, false
	}
	offset := n
	container, n := protowire.ConsumeBytes(decoded[offset:])
	if n < 0 {
		return nil, false
	}
	offset += n
	if offset != len(decoded) {
		return nil, false
	}

	num, typ, n = protowire.ConsumeTag(container)
	if n < 0 || num != 1 || typ != protowire.BytesType {
		return nil, false
	}
	containerOffset := n
	value, n := protowire.ConsumeBytes(container[containerOffset:])
	if n < 0 {
		return nil, false
	}
	containerOffset += n
	if containerOffset != len(container) {
		return nil, false
	}
	return value, true
}

func isLikelyGeminiOpaquePayload(value []byte) bool {
	// Observed Gemini 2.5 and Gemini 3.x envelopes wrap provider-opaque
	// payloads that start with an internal version byte 0x01. The bytes after
	// that are high-entropy provider state and must remain opaque.
	return len(value) > 0 && value[0] == 0x01
}

func isASCIIUUIDBytes(decoded []byte) bool {
	if len(decoded) != 36 {
		return false
	}
	for i, b := range decoded {
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

func geminiContents(inputRawJSON []byte) (gjson.Result, string) {
	if contents := gjson.GetBytes(inputRawJSON, "contents"); contents.Exists() {
		return contents, "contents"
	}
	return gjson.GetBytes(inputRawJSON, "request.contents"), "request.contents"
}
