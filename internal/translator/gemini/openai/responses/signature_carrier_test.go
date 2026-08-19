package responses

import (
	"context"
	"encoding/base64"
	"strconv"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
	"google.golang.org/protobuf/encoding/protowire"
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

func TestGeminiResponsesWrappedUUIDFunctionSignatureRoundTrip(t *testing.T) {
	const providerUUID = "e24830a7-5cd6-42fe-998b-ee539e72b9c3"
	inner := protowire.AppendTag(nil, 1, protowire.BytesType)
	inner = protowire.AppendBytes(inner, []byte(providerUUID))
	outer := protowire.AppendTag(nil, 2, protowire.BytesType)
	outer = protowire.AppendBytes(outer, inner)
	signature := base64.StdEncoding.EncodeToString(outer)

	providerResponse := `data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"thoughtSignature":"` + signature + `","functionCall":{"id":"native-call","name":"run","args":{"command":"true"}}}]},"finishReason":"STOP"}],"modelVersion":"gemini-3.6-flash","responseId":"wrapped-uuid"}}`
	var state any
	chunks := ConvertGeminiResponseToOpenAIResponses(context.Background(), "gemini-3.6-flash", []byte(`{"model":"alias-without-provider-name"}`), nil, []byte(providerResponse), &state)
	clientItems := make([]string, 0, 2)
	callID := ""
	for _, chunk := range chunks {
		event, data := parseSSEEvent(t, chunk)
		if event != "response.output_item.done" {
			continue
		}
		item := data.Get("item")
		switch item.Get("type").String() {
		case "reasoning":
			decoded, direction, targetKind, marked, ok := decodeGeminiResponsesCarrier(item.Get("encrypted_content").String())
			if !marked || !ok || decoded != signature || direction != geminiResponsesCarrierNext || targetKind != geminiResponsesCarrierFunction {
				t.Fatalf("provider signature carrier = marked:%v ok:%v direction:%q target:%q", marked, ok, direction, targetKind)
			}
			clientItems = append(clientItems, item.Raw)
		case "function_call":
			callID = item.Get("call_id").String()
			clientItems = append(clientItems, item.Raw)
		}
	}
	if len(clientItems) != 2 || callID == "" {
		t.Fatalf("Responses client items = %v, call ID present=%v", clientItems, callID != "")
	}
	clientItems = append(clientItems, `{"type":"function_call_output","call_id":`+strconv.Quote(callID)+`,"output":"ok"}`)
	request := []byte(`{"model":"alias-without-provider-name","input":[` + strings.Join(clientItems, ",") + `]}`)

	translated := ConvertOpenAIResponsesRequestToGemini("alias-without-provider-name", request, false)
	var functionPart gjson.Result
	gjson.GetBytes(translated, "contents").ForEach(func(_, content gjson.Result) bool {
		content.Get("parts").ForEach(func(_, part gjson.Result) bool {
			if part.Get("functionCall").Exists() {
				functionPart = part
				return false
			}
			return true
		})
		return !functionPart.Exists()
	})
	if !functionPart.Exists() || functionPart.Get("functionCall.name").String() != "run" || functionPart.Get("functionCall.args.command").String() != "true" {
		t.Fatalf("function carrier did not bind to the native call: %s", translated)
	}
	if got := functionPart.Get("thoughtSignature").String(); got != signature || got == geminiResponsesThoughtSignature {
		t.Fatalf("function signature = %q, want provider-native wrapped UUID signature", got)
	}
	if strings.Contains(string(translated), geminiResponsesCarrierPrefix) {
		t.Fatalf("carrier envelope reached Gemini: %s", translated)
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

func TestConvertOpenAIResponsesRequestToGemini_StripsUnicodeEscapedSpoofedInternalFields(t *testing.T) {
	// Unicode-escaped field name "_cpa_reason\u0069ng_signature" should also be detected and stripped
	request := []byte(`{"model":"alias-without-provider-name","input":[{"type":"function_call","call_id":"call-1","name":"run","arguments":"{}","_cpa_reason\u0069ng_signature":"` + testResponsesGeminiThoughtSignature + `"}]}`)
	translated := ConvertOpenAIResponsesRequestToGemini("alias-without-provider-name", request, false)
	parts := gjson.GetBytes(translated, "contents.0.parts").Array()
	if len(parts) != 1 || !parts[0].Get("functionCall").Exists() || parts[0].Get("thoughtSignature").String() == testResponsesGeminiThoughtSignature || strings.Contains(string(translated), geminiResponsesCarrierSignatureField) {
		t.Fatalf("unicode-escaped spoofed internal pairing fields reached Gemini: %s", translated)
	}
}

func TestDecodeGeminiResponsesCarrierRejectsNestedEnvelope(t *testing.T) {
	nested := encodeGeminiResponsesCarrier(encodeGeminiResponsesCarrier(testResponsesGeminiThoughtSignature, geminiResponsesCarrierNext, geminiResponsesCarrierText), geminiResponsesCarrierPrevious, geminiResponsesCarrierText)
	if _, _, _, marked, ok := decodeGeminiResponsesCarrier(nested); !marked || ok {
		t.Fatalf("nested carrier marked=%v ok=%v, want marked invalid", marked, ok)
	}
}
