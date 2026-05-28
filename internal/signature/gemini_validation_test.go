package signature

import (
	"encoding/base64"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protowire"
)

func testGeminiThoughtSignature(payload []byte) string {
	return base64.StdEncoding.EncodeToString(payload)
}

func testGemini25ThoughtSignature(records ...[]byte) string {
	var payload []byte
	for _, record := range records {
		payload = protowire.AppendTag(payload, 1, protowire.BytesType)
		payload = protowire.AppendBytes(payload, record)
	}
	return testGeminiThoughtSignature(payload)
}

func testGemini3ThoughtSignature(payload []byte) string {
	var inner []byte
	inner = protowire.AppendTag(inner, 1, protowire.BytesType)
	inner = protowire.AppendBytes(inner, payload)

	var outer []byte
	outer = protowire.AppendTag(outer, 2, protowire.BytesType)
	outer = protowire.AppendBytes(outer, inner)
	return testGeminiThoughtSignature(outer)
}

func TestInspectGeminiThoughtSignature_AcceptsOpaqueBase64(t *testing.T) {
	sig := testGeminiThoughtSignature([]byte{0x12, 0x34, 0x56})

	info, err := InspectGeminiThoughtSignature(sig)
	if err != nil {
		t.Fatalf("InspectGeminiThoughtSignature failed: %v", err)
	}
	if info.IsBypassSentinel {
		t.Fatal("real signature should not be marked as bypass sentinel")
	}
	if info.DecodedLen != 3 {
		t.Fatalf("DecodedLen = %d, want 3", info.DecodedLen)
	}
	if info.FirstByte != 0x12 {
		t.Fatalf("FirstByte = 0x%02x, want 0x12", info.FirstByte)
	}
	if !info.HasObservedMarker {
		t.Fatal("HasObservedMarker should be true")
	}
	if info.Envelope != GeminiThoughtSignatureEnvelopeUnknown {
		t.Fatalf("Envelope = %q, want %q", info.Envelope, GeminiThoughtSignatureEnvelopeUnknown)
	}
	if info.KnownEnvelope {
		t.Fatal("KnownEnvelope should be false for incomplete opaque payload")
	}
}

func TestInspectGeminiThoughtSignature_AcceptsGemini31ProField2Envelope(t *testing.T) {
	// Shape observed in CPA-API/signatures/gemini/gemini-3.1-pro.txt.
	sig := testGemini3ThoughtSignature([]byte{0x01, 0x0c, 0x39, 0xd6, 0xc7, 0x34})

	info, err := InspectGeminiThoughtSignature(sig, GeminiThoughtSignatureValidationOptions{RequireKnownEnvelope: true})
	if err != nil {
		t.Fatalf("Gemini 3.1 Pro field-2 envelope should be known: %v", err)
	}
	if info.Envelope != GeminiThoughtSignatureEnvelopeProtobufField2 {
		t.Fatalf("Envelope = %q, want %q", info.Envelope, GeminiThoughtSignatureEnvelopeProtobufField2)
	}
	if !info.HasObservedMarker {
		t.Fatal("Gemini 3.1 Pro envelope should be marked as 0x12")
	}
	if info.RecordCount != 1 {
		t.Fatalf("RecordCount = %d, want 1", info.RecordCount)
	}
	if info.OpaquePayloadLen != 6 {
		t.Fatalf("OpaquePayloadLen = %d, want 6", info.OpaquePayloadLen)
	}
}

func TestInspectGeminiThoughtSignature_AcceptsCapturedGemini31FlashLiteEnvelope(t *testing.T) {
	// Captured in CPA-API/signatures/gemini/gemini-3.1-flash-lite.txt.
	const sig = "EjQKMgEMOdbHO0Gd+c9Mxk4ELwPGbpCEcp2mFfYYLix2UVtBH3fL8GECc4+JITVnHF4qZDsA"

	info, err := InspectGeminiThoughtSignature(sig, GeminiThoughtSignatureValidationOptions{RequireKnownEnvelope: true})
	if err != nil {
		t.Fatalf("captured Gemini 3.1 Flash Lite envelope should be known: %v", err)
	}
	if info.Envelope != GeminiThoughtSignatureEnvelopeProtobufField2 {
		t.Fatalf("Envelope = %q, want %q", info.Envelope, GeminiThoughtSignatureEnvelopeProtobufField2)
	}
	if info.RecordCount != 1 {
		t.Fatalf("RecordCount = %d, want 1", info.RecordCount)
	}
	if info.OpaquePayloadLen != 50 {
		t.Fatalf("OpaquePayloadLen = %d, want 50", info.OpaquePayloadLen)
	}
}

func TestInspectGeminiThoughtSignature_AcceptsGemini25Field1Envelope(t *testing.T) {
	sig := testGemini25ThoughtSignature([]byte{0x01, 0x8f}, []byte{0x01, 0x90, 0x91})

	info, err := InspectGeminiThoughtSignature(sig, GeminiThoughtSignatureValidationOptions{RequireKnownEnvelope: true})
	if err != nil {
		t.Fatalf("Gemini 2.5 field-1 envelope should be known: %v", err)
	}
	if info.Envelope != GeminiThoughtSignatureEnvelopeProtobufField1 {
		t.Fatalf("Envelope = %q, want %q", info.Envelope, GeminiThoughtSignatureEnvelopeProtobufField1)
	}
	if info.HasObservedMarker {
		t.Fatal("Gemini 2.5 field-1 envelope should not be marked as 0x12")
	}
	if info.RecordCount != 2 {
		t.Fatalf("RecordCount = %d, want 2", info.RecordCount)
	}
	if info.OpaquePayloadLen != 5 {
		t.Fatalf("OpaquePayloadLen = %d, want 5", info.OpaquePayloadLen)
	}
}

func TestInspectGeminiThoughtSignature_RejectsMalformedKnownEnvelope(t *testing.T) {
	// Field 2 with a nested field 1 is not enough. Observed Gemini 3 payloads
	// wrap an opaque blob that starts with internal version byte 0x01.
	sig := testGemini3ThoughtSignature([]byte{0x02, 0x0c, 0x39})

	if IsValidGeminiThoughtSignature(sig, GeminiThoughtSignatureValidationOptions{RequireKnownEnvelope: true}) {
		t.Fatal("malformed Gemini 3 envelope should fail known-envelope validation")
	}
}

func TestInspectGeminiThoughtSignature_ClassifiesASCIIUUIDAsOpaque(t *testing.T) {
	sig := testGeminiThoughtSignature([]byte("e24830a7-5cd6-42fe-998b-ee539e72b9c3"))

	info, err := InspectGeminiThoughtSignature(sig)
	if err != nil {
		t.Fatalf("opaque base64 UUID should pass default validation: %v", err)
	}
	if info.Envelope != GeminiThoughtSignatureEnvelopeASCIIUUID {
		t.Fatalf("Envelope = %q, want %q", info.Envelope, GeminiThoughtSignatureEnvelopeASCIIUUID)
	}
	if info.KnownEnvelope {
		t.Fatal("base64 UUID should not be a known protobuf envelope")
	}
	if IsValidGeminiThoughtSignature(sig, GeminiThoughtSignatureValidationOptions{RequireKnownEnvelope: true}) {
		t.Fatal("base64 UUID should fail when known envelope is required")
	}
}

func TestInspectGeminiThoughtSignature_ObservedMarkerOption(t *testing.T) {
	sig := testGeminiThoughtSignature([]byte{0x45, 0x12})

	if _, err := InspectGeminiThoughtSignature(sig); err != nil {
		t.Fatalf("default validation should accept opaque base64 payload: %v", err)
	}
	_, err := InspectGeminiThoughtSignature(sig, GeminiThoughtSignatureValidationOptions{RequireObservedMarker: true})
	if err == nil {
		t.Fatal("RequireObservedMarker should reject payloads without 0x12 marker")
	}
	if !strings.Contains(err.Error(), "expected observed marker") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestInspectGeminiThoughtSignature_BypassSentinelRequiresOption(t *testing.T) {
	if IsValidGeminiThoughtSignature(GeminiSkipThoughtSignatureValidator) {
		t.Fatal("bypass sentinel should not be valid by default")
	}

	info, err := InspectGeminiThoughtSignature(GeminiSkipThoughtSignatureValidator, GeminiThoughtSignatureValidationOptions{AllowBypassSentinel: true})
	if err != nil {
		t.Fatalf("bypass sentinel should be accepted when explicitly allowed: %v", err)
	}
	if !info.IsBypassSentinel {
		t.Fatal("sentinel should be marked as bypass")
	}
	if info.BypassSentinel != GeminiSkipThoughtSignatureValidator {
		t.Fatalf("BypassSentinel = %q, want %q", info.BypassSentinel, GeminiSkipThoughtSignatureValidator)
	}
}

func TestInspectGeminiThoughtSignature_RejectsInvalidBase64(t *testing.T) {
	if IsValidGeminiThoughtSignature("not valid base64!!!") {
		t.Fatal("invalid base64 should be rejected")
	}
}

func TestValidateGeminiThoughtSignatures_FunctionCallRequiresSignature(t *testing.T) {
	input := []byte(`{
		"contents": [{
			"role": "model",
			"parts": [
				{"functionCall": {"id": "call-1", "name": "read_file", "args": {}}}
			]
		}]
	}`)

	err := ValidateGeminiThoughtSignatures(input)
	if err == nil {
		t.Fatal("missing functionCall thoughtSignature should fail")
	}
	if !strings.Contains(err.Error(), "missing thoughtSignature on functionCall") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateGeminiThoughtSignatures_AcceptsWrappedRequestAndSentinelWhenAllowed(t *testing.T) {
	input := []byte(`{
		"request": {
			"contents": [{
				"role": "model",
				"parts": [
					{
						"functionCall": {"id": "call-1", "name": "read_file", "args": {}},
						"thoughtSignature": "skip_thought_signature_validator"
					}
				]
			}]
		}
	}`)

	err := ValidateGeminiThoughtSignatures(input, GeminiThoughtSignatureValidationOptions{AllowBypassSentinel: true})
	if err != nil {
		t.Fatalf("sentinel should be valid when explicitly allowed: %v", err)
	}
}

func TestValidateGeminiThoughtSignatures_RejectsInvalidTextPartSignature(t *testing.T) {
	input := []byte(`{
		"contents": [{
				"role": "model",
				"parts": [
				{"text": "previous answer", "thoughtSignature": "bad!!!"}
			]
		}]
	}`)

	err := ValidateGeminiThoughtSignatures(input)
	if err == nil {
		t.Fatal("invalid text-part thoughtSignature should fail")
	}
	if !strings.Contains(err.Error(), "base64 decode failed") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateGeminiFunctionCallPairing_ValidParallelGroup(t *testing.T) {
	input := []byte(`{
		"contents": [
			{
				"role": "model",
				"parts": [
					{"functionCall": {"id": "call-1", "name": "weather", "args": {"city": "Paris"}}},
					{"functionCall": {"id": "call-2", "name": "weather", "args": {"city": "London"}}}
				]
			},
			{
				"role": "user",
				"parts": [
					{"functionResponse": {"id": "call-1", "name": "weather", "response": {"temp": "15C"}}},
					{"functionResponse": {"id": "call-2", "name": "weather", "response": {"temp": "12C"}}}
				]
			}
		]
	}`)

	if err := ValidateGeminiFunctionCallPairing(input); err != nil {
		t.Fatalf("valid pairing failed: %v", err)
	}
}

func TestValidateGeminiFunctionCallPairing_RejectsResponseCountMismatch(t *testing.T) {
	input := []byte(`{
		"contents": [
			{
				"role": "model",
				"parts": [
					{"functionCall": {"id": "call-1", "name": "weather", "args": {}}},
					{"functionCall": {"id": "call-2", "name": "weather", "args": {}}}
				]
			},
			{
				"role": "user",
				"parts": [
					{"functionResponse": {"id": "call-1", "name": "weather", "response": {}}}
				]
			}
		]
	}`)

	err := ValidateGeminiFunctionCallPairing(input)
	if err == nil {
		t.Fatal("response count mismatch should fail")
	}
	if !strings.Contains(err.Error(), "does not match pending functionCall count") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateGeminiFunctionCallPairing_RejectsMissingFunctionCallName(t *testing.T) {
	input := []byte(`{
		"contents": [{
			"role": "model",
			"parts": [
				{"functionCall": {"id": "call-1", "args": {}}}
			]
		}]
	}`)

	err := ValidateGeminiFunctionCallPairing(input)
	if err == nil {
		t.Fatal("missing functionCall name should fail")
	}
	if !strings.Contains(err.Error(), "missing functionCall.name") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateGeminiFunctionCallPairing_RejectsIDMismatch(t *testing.T) {
	input := []byte(`{
		"contents": [
			{
				"role": "model",
				"parts": [
					{"functionCall": {"id": "call-1", "name": "weather", "args": {}}}
				]
			},
			{
				"role": "user",
				"parts": [
					{"functionResponse": {"id": "call-other", "name": "weather", "response": {}}}
				]
			}
		]
	}`)

	err := ValidateGeminiFunctionCallPairing(input)
	if err == nil {
		t.Fatal("id mismatch should fail")
	}
	if !strings.Contains(err.Error(), "does not match functionCall.id") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateGeminiFunctionCallPairing_RejectsMissingResponseName(t *testing.T) {
	input := []byte(`{
		"contents": [
			{
				"role": "model",
				"parts": [
					{"functionCall": {"id": "call-1", "name": "weather", "args": {}}}
				]
			},
			{
				"role": "user",
				"parts": [
					{"functionResponse": {"id": "call-1", "response": {}}}
				]
			}
		]
	}`)

	err := ValidateGeminiFunctionCallPairing(input)
	if err == nil {
		t.Fatal("missing response name should fail")
	}
	if !strings.Contains(err.Error(), "missing functionResponse.name") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateGeminiFunctionCallPairing_RejectsSameContentInterleaving(t *testing.T) {
	input := []byte(`{
		"contents": [{
			"role": "model",
			"parts": [
				{"functionCall": {"id": "call-1", "name": "weather", "args": {}}},
				{"functionResponse": {"id": "call-1", "name": "weather", "response": {}}}
			]
		}]
	}`)

	err := ValidateGeminiFunctionCallPairing(input)
	if err == nil {
		t.Fatal("same-content interleaving should fail")
	}
	if !strings.Contains(err.Error(), "must not be interleaved") {
		t.Fatalf("unexpected error: %v", err)
	}
}
