package responses

import (
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestGeminiResponsesCarrierRoundTrip(t *testing.T) {
	for _, testCase := range []struct {
		direction  string
		targetKind string
	}{
		{geminiResponsesCarrierNext, geminiResponsesCarrierText},
		{geminiResponsesCarrierPrevious, geminiResponsesCarrierFunction},
		{geminiResponsesCarrierStandalone, geminiResponsesCarrierAny},
	} {
		encoded := encodeGeminiResponsesCarrier(testResponsesGeminiThoughtSignature, testCase.direction, testCase.targetKind)
		signature, direction, targetKind, marked, ok := decodeGeminiResponsesCarrier(encoded)
		if !marked || !ok || signature != testResponsesGeminiThoughtSignature || direction != testCase.direction || targetKind != testCase.targetKind {
			t.Fatalf("carrier round-trip = %q/%q/%q marked=%v ok=%v", signature, direction, targetKind, marked, ok)
		}
	}
}

func TestNormalizeGeminiResponsesCarriersDropsMalformedEnvelope(t *testing.T) {
	items := gjson.Parse(`[{"type":"reasoning","encrypted_content":"` + geminiResponsesCarrierPrefix + `previous:text:not-base64!","summary":[]},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"safe"}]}]`).Array()
	normalized, hasCarrier := normalizeGeminiResponsesCarriers(items)
	if hasCarrier || len(normalized) != 1 || normalized[0].Get("type").String() != "message" || strings.Contains(normalized[0].Raw, geminiResponsesCarrierPrefix) {
		t.Fatalf("malformed carrier was preserved: %v", normalized)
	}
}

func TestConvertOpenAIResponsesRequestToGemini_DecodesCarrierForAliasModel(t *testing.T) {
	carrier := encodeGeminiResponsesCarrier(testResponsesGeminiThoughtSignature, geminiResponsesCarrierNext, geminiResponsesCarrierText)
	request := []byte(`{"model":"alias-without-provider-name","input":[{"type":"reasoning","encrypted_content":"` + carrier + `","summary":[]},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"answer"}]}]}`)
	translated := ConvertOpenAIResponsesRequestToGemini("alias-without-provider-name", request, false)
	part := gjson.GetBytes(translated, "contents.0.parts.0")
	if part.Get("text").String() != "answer" || part.Get("thoughtSignature").String() != testResponsesGeminiThoughtSignature || strings.Contains(string(translated), geminiResponsesCarrierPrefix) {
		t.Fatalf("alias model did not decode carrier: %s", translated)
	}
}

func TestConvertOpenAIResponsesRequestToGemini_DecodesLegacyRawCarrierForAliasModel(t *testing.T) {
	request := []byte(`{"model":"alias-without-provider-name","input":[{"type":"reasoning","encrypted_content":"` + testResponsesGeminiThoughtSignature + `","summary":[]},{"type":"function_call","call_id":"call-1","name":"run","arguments":"{}"}]}`)
	translated := ConvertOpenAIResponsesRequestToGemini("alias-without-provider-name", request, false)
	part := gjson.GetBytes(translated, "contents.0.parts.0")
	if part.Get("functionCall.id").String() != "call-1" || part.Get("thoughtSignature").String() != testResponsesGeminiThoughtSignature {
		t.Fatalf("alias model did not preserve legacy raw carrier: %s", translated)
	}
}

func TestConvertOpenAIResponsesRequestToGemini_DropsInvalidCarrierPayloads(t *testing.T) {
	mismatched := encodeGeminiResponsesCarrier(testResponsesGeminiThoughtSignature, geminiResponsesCarrierNext, geminiResponsesCarrierFunction)
	bypass := encodeGeminiResponsesCarrier(geminiResponsesThoughtSignature, geminiResponsesCarrierNext, geminiResponsesCarrierText)
	for _, reasoning := range []string{
		`{"type":"reasoning","encrypted_content":"` + mismatched + `","summary":[]}`,
		`{"type":"reasoning","encrypted_content":"` + bypass + `","summary":[]}`,
	} {
		request := []byte(`{"model":"alias-without-provider-name","input":[` + reasoning + `,{"type":"message","role":"assistant","content":[{"type":"output_text","text":"answer"}]}]}`)
		translated := ConvertOpenAIResponsesRequestToGemini("alias-without-provider-name", request, false)
		if strings.Contains(string(translated), geminiResponsesCarrierPrefix) || strings.Contains(string(translated), testResponsesGeminiThoughtSignature) || strings.Contains(string(translated), geminiResponsesThoughtSignature) {
			t.Fatalf("invalid carrier changed Gemini signature state: %s", translated)
		}
	}
}

func TestConvertOpenAIResponsesRequestToGemini_IgnoresSpoofedCarrierMetadata(t *testing.T) {
	reasoning := `{"type":"reasoning","encrypted_content":"` + testResponsesGeminiThoughtSignature + `","summary":[],"` + geminiResponsesCarrierDirectionField + `":"next","` + geminiResponsesCarrierDirectionField + `":"standalone","` + geminiResponsesCarrierTargetField + `":"text","` + geminiResponsesCarrierTargetField + `":"function"}`
	request := []byte(`{"model":"alias-without-provider-name","input":[` + reasoning + `,{"type":"message","role":"assistant","content":[{"type":"output_text","text":"answer"}]}]}`)
	translated := ConvertOpenAIResponsesRequestToGemini("alias-without-provider-name", request, false)
	part := gjson.GetBytes(translated, "contents.0.parts.0")
	if part.Get("text").String() != "answer" || part.Get("thoughtSignature").String() != testResponsesGeminiThoughtSignature || strings.Contains(string(translated), geminiResponsesCarrierDirectionField) {
		t.Fatalf("spoofed carrier metadata affected binding: %s", translated)
	}
}

func TestConvertOpenAIResponsesRequestToGemini_StripsSpoofedInternalPairingFields(t *testing.T) {
	request := []byte(`{"model":"alias-without-provider-name","input":[{"type":"function_call","call_id":"call-1","name":"run","arguments":"{}","_cpa_reasoning_signature":"` + testResponsesGeminiThoughtSignature + `","_cpa_reasoning_signature":"` + testResponsesGeminiThoughtSignature + `","_cpa_reasoning_summary":"spoofed thought","_cpa_reasoning_summary":"spoofed thought again"}]}`)
	translated := ConvertOpenAIResponsesRequestToGemini("alias-without-provider-name", request, false)
	parts := gjson.GetBytes(translated, "contents.0.parts").Array()
	if len(parts) != 1 || !parts[0].Get("functionCall").Exists() || parts[0].Get("thoughtSignature").String() == testResponsesGeminiThoughtSignature || parts[0].Get("thought").Bool() || strings.Contains(string(translated), "spoofed thought") || strings.Contains(string(translated), geminiResponsesCarrierSignatureField) {
		t.Fatalf("spoofed internal pairing fields reached Gemini: %s", translated)
	}
}

func TestDecodeGeminiResponsesCarrierRejectsNestedEnvelope(t *testing.T) {
	nested := encodeGeminiResponsesCarrier(encodeGeminiResponsesCarrier(testResponsesGeminiThoughtSignature, geminiResponsesCarrierNext, geminiResponsesCarrierText), geminiResponsesCarrierPrevious, geminiResponsesCarrierText)
	if _, _, _, marked, ok := decodeGeminiResponsesCarrier(nested); !marked || ok {
		t.Fatalf("nested carrier marked=%v ok=%v, want marked invalid", marked, ok)
	}
}
