package responses

import (
	"encoding/base64"
	"strings"
	"testing"

	internalsignature "github.com/router-for-me/CLIProxyAPI/v7/internal/signature"
	"github.com/tidwall/gjson"
)

const testResponsesGeminiThoughtSignature = "EjQKMgEMOdbHO0Gd+c9Mxk4ELwPGbpCEcp2mFfYYLix2UVtBH3fL8GECc4+JITVnHF4qZDsA"

func TestReorderOpenAIResponsesDetachedReasoningDoesNotCrossUserMessage(t *testing.T) {
	items := gjson.Parse(`[
		{"type":"message","role":"user","content":[{"type":"input_text","text":"next"}]},
		{"id":"rs_test_detached_after_1","type":"reasoning","encrypted_content":"` + testResponsesGeminiThoughtSignature + `","summary":[]},
		{"type":"function_call","call_id":"call-1","name":"run_command","arguments":"{}"}
	]`).Array()
	reordered := reorderOpenAIResponsesDetachedReasoning(items)
	if got := reordered[0].Get("role").String(); got != "user" {
		t.Fatalf("detached reasoning crossed user boundary: first role=%q", got)
	}
	if got := reordered[1].Get("type").String(); got != "reasoning" {
		t.Fatalf("item 1 = %q, want reasoning", got)
	}
}

func TestConvertOpenAIResponsesRequestToGemini_ReattachesReasoningAndSignatureToFunctionCall(t *testing.T) {
	inputJSON := `{
		"model":"gemini-3.6-flash-high",
		"input":[
			{"type":"message","role":"user","content":[{"type":"input_text","text":"run"}]},
			{"type":"reasoning","encrypted_content":"` + testResponsesGeminiThoughtSignature + `","summary":[{"type":"summary_text","text":"hidden thought"}]},
			{"type":"function_call","call_id":"call-1","name":"run_command","arguments":"{\"command\":\"true\"}"},
			{"type":"function_call_output","call_id":"call-1","output":"ok"}
		]
	}`
	result := ConvertOpenAIResponsesRequestToGemini("gemini-3.6-flash-high", []byte(inputJSON), false)
	parts := gjson.GetBytes(result, "contents.1.parts").Array()
	if len(parts) != 2 || !parts[0].Get("thought").Bool() {
		t.Fatalf("reasoning/function parts malformed: %s", result)
	}
	if got := parts[1].Get("functionCall.name").String(); got != "run_command" {
		t.Fatalf("function name = %q; result=%s", got, result)
	}
	if got := parts[1].Get("thoughtSignature").String(); got != testResponsesGeminiThoughtSignature {
		t.Fatalf("function signature = %q, want %q; result=%s", got, testResponsesGeminiThoughtSignature, result)
	}
}

func TestConvertOpenAIResponsesRequestToGemini_SyntheticParallelCallsOnlyFirstGetsSentinel(t *testing.T) {
	inputJSON := `{
		"model":"gemini-3.6-flash-high",
		"input":[
			{"type":"function_call","call_id":"call-1","name":"run_command","arguments":"{\"command\":\"one\"}"},
			{"type":"function_call","call_id":"call-2","name":"run_command","arguments":"{\"command\":\"two\"}"}
		]
	}`

	result := ConvertOpenAIResponsesRequestToGemini("gemini-3.6-flash-high", []byte(inputJSON), false)
	parts := gjson.GetBytes(result, "contents.0.parts").Array()
	if len(parts) != 2 {
		t.Fatalf("parts = %d, want 2 parallel calls; result=%s", len(parts), result)
	}
	if got := parts[0].Get("thoughtSignature").String(); got != internalsignature.GeminiSkipThoughtSignatureValidator {
		t.Fatalf("first synthetic call signature = %q, want sentinel; result=%s", got, result)
	}
	if signature := parts[1].Get("thoughtSignature"); signature.Exists() {
		t.Fatalf("second synthetic sibling should remain unsigned; result=%s", result)
	}
}

func TestConvertOpenAIResponsesRequestToGemini_NativeParallelCallsPreserveUnsignedSibling(t *testing.T) {
	inputJSON := `{
		"model":"gemini-3.6-flash-high",
		"input":[
			{"type":"message","role":"user","content":[{"type":"input_text","text":"run twice"}]},
			{"type":"reasoning","encrypted_content":"` + testResponsesGeminiThoughtSignature + `","summary":[]},
			{"type":"function_call","call_id":"call-1","name":"run_command","arguments":"{\"command\":\"one\"}"},
			{"type":"function_call","call_id":"call-2","name":"run_command","arguments":"{\"command\":\"two\"}"}
		]
	}`

	result := ConvertOpenAIResponsesRequestToGemini("gemini-3.6-flash-high", []byte(inputJSON), false)
	var calls []gjson.Result
	for _, content := range gjson.GetBytes(result, "contents").Array() {
		for _, part := range content.Get("parts").Array() {
			if part.Get("functionCall").Exists() {
				calls = append(calls, part)
			}
		}
	}
	if len(calls) != 2 {
		t.Fatalf("calls = %d, want 2; result=%s", len(calls), result)
	}
	if got := calls[0].Get("thoughtSignature").String(); got != testResponsesGeminiThoughtSignature {
		t.Fatalf("first call signature = %q, want native signature; result=%s", got, result)
	}
	if signature := calls[1].Get("thoughtSignature"); signature.Exists() {
		t.Fatalf("native unsigned sibling should remain unsigned; result=%s", result)
	}
}

func TestConvertOpenAIResponsesRequestToGemini_PreservesMultipleLeadingToolSignatures(t *testing.T) {
	secondRaw, errDecode := base64.StdEncoding.DecodeString(testResponsesGeminiThoughtSignature)
	if errDecode != nil {
		t.Fatal(errDecode)
	}
	secondRaw[len(secondRaw)-1] ^= 1
	secondSignature := base64.StdEncoding.EncodeToString(secondRaw)
	inputJSON := `{
		"model":"gemini-3.6-flash-high",
		"input":[
			{"type":"message","role":"user","content":[{"type":"input_text","text":"run twice"}]},
			{"id":"rs_before_1","type":"reasoning","encrypted_content":"` + testResponsesGeminiThoughtSignature + `","summary":[]},
			{"type":"function_call","call_id":"call-1","name":"run_command","arguments":"{\"command\":\"one\"}"},
			{"id":"rs_before_2","type":"reasoning","encrypted_content":"` + secondSignature + `","summary":[]},
			{"type":"function_call","call_id":"call-2","name":"run_command","arguments":"{\"command\":\"two\"}"},
			{"type":"function_call_output","call_id":"call-1","output":"one"},
			{"type":"function_call_output","call_id":"call-2","output":"two"}
		]
	}`
	result := ConvertOpenAIResponsesRequestToGemini("gemini-3.6-flash-high", []byte(inputJSON), false)
	var signatures, sequence []string
	for _, content := range gjson.GetBytes(result, "contents").Array() {
		for _, part := range content.Get("parts").Array() {
			if part.Get("functionCall").Exists() {
				signatures = append(signatures, part.Get("thoughtSignature").String())
				sequence = append(sequence, "call:"+part.Get("functionCall.id").String())
			}
			if part.Get("functionResponse").Exists() {
				sequence = append(sequence, "output:"+part.Get("functionResponse.id").String())
			}
		}
	}
	if len(signatures) != 2 || signatures[0] != testResponsesGeminiThoughtSignature || signatures[1] != secondSignature {
		t.Fatalf("tool signatures = %v; result=%s", signatures, result)
	}
	if got := strings.Join(sequence, ","); got != "call:call-1,call:call-2,output:call-1,output:call-2" {
		t.Fatalf("parallel tool call/output sequence = %q; result=%s", got, result)
	}
	if errValidate := internalsignature.ValidateGeminiFunctionCallPairing(result); errValidate != nil {
		t.Fatalf("parallel tool history is invalid: %v; result=%s", errValidate, result)
	}
}

func TestConvertOpenAIResponsesRequestToGemini_GroupsReversedParallelToolOutputs(t *testing.T) {
	inputJSON := `{
		"model":"gemini-3.6-flash-high",
		"input":[
			{"type":"function_call","call_id":"call-1","name":"run_command","arguments":"{\"command\":\"one\"}"},
			{"type":"function_call","call_id":"call-2","name":"run_command","arguments":"{\"command\":\"two\"}"},
			{"type":"function_call_output","call_id":"call-2","output":"two"},
			{"type":"function_call_output","call_id":"call-1","output":"one"}
		]
	}`
	result := ConvertOpenAIResponsesRequestToGemini("gemini-3.6-flash-high", []byte(inputJSON), false)
	if errValidate := internalsignature.ValidateGeminiFunctionCallPairing(result); errValidate != nil {
		t.Fatalf("parallel tool history is invalid: %v; result=%s", errValidate, result)
	}
	contents := gjson.GetBytes(result, "contents").Array()
	if len(contents) != 2 || contents[0].Get("role").String() != "model" || contents[1].Get("role").String() != "user" {
		t.Fatalf("parallel tool roles malformed; result=%s", result)
	}
	responses := contents[1].Get("parts").Array()
	if len(responses) != 2 {
		t.Fatalf("function response count = %d, want 2; result=%s", len(responses), result)
	}
	if got := responses[0].Get("functionResponse.id").String(); got != "call-1" {
		t.Fatalf("first function response = %q, want call-1; result=%s", got, result)
	}
	if got := responses[0].Get("functionResponse.response.result").String(); got != "one" {
		t.Fatalf("first function result = %q, want one; result=%s", got, result)
	}
	if got := responses[1].Get("functionResponse.id").String(); got != "call-2" {
		t.Fatalf("second function response = %q, want call-2; result=%s", got, result)
	}
	if got := responses[1].Get("functionResponse.response.result").String(); got != "two" {
		t.Fatalf("second function result = %q, want two; result=%s", got, result)
	}
}

func TestConvertOpenAIResponsesRequestToGemini_GroupsNonContiguousParallelToolOutputs(t *testing.T) {
	inputJSON := `{
		"model":"gemini-3.6-flash-high",
		"input":[
			{"type":"function_call","call_id":"call-1","name":"run_command","arguments":"{\"command\":\"one\"}"},
			{"type":"function_call","call_id":"call-2","name":"run_command","arguments":"{\"command\":\"two\"}"},
			{"type":"function_call_output","call_id":"call-1","output":"one"},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"between outputs"}]},
			{"type":"function_call_output","call_id":"call-2","output":"two"}
		]
	}`
	result := ConvertOpenAIResponsesRequestToGemini("gemini-3.6-flash-high", []byte(inputJSON), false)
	contents := gjson.GetBytes(result, "contents").Array()
	if len(contents) != 4 || contents[0].Get("role").String() != "model" || contents[1].Get("role").String() != "user" || contents[2].Get("role").String() != "user" || contents[3].Get("role").String() != "user" {
		t.Fatalf("non-contiguous tool output roles malformed; result=%s", result)
	}
	if got := contents[1].Get("parts.0.functionResponse.id").String(); got != "call-1" {
		t.Fatalf("first function response = %q, want call-1; result=%s", got, result)
	}
	if got := contents[2].Get("parts.0.text").String(); got != "between outputs" {
		t.Fatalf("intervening user message = %q; result=%s", got, result)
	}
	if got := contents[3].Get("parts.0.functionResponse.id").String(); got != "call-2" {
		t.Fatalf("second function response crossed user boundary: got %q; result=%s", got, result)
	}
}

func TestConvertOpenAIResponsesRequestToGemini_PreservesReasoningBeforePairedFunctionSignature(t *testing.T) {
	secondSignature := differentResponsesGeminiThoughtSignature(t)
	inputJSON := `{
		"model":"gemini-3.6-flash-high",
		"input":[
			{"type":"reasoning","encrypted_content":"` + testResponsesGeminiThoughtSignature + `","summary":[{"type":"summary_text","text":"first"}]},
			{"type":"reasoning","encrypted_content":"` + secondSignature + `","summary":[{"type":"summary_text","text":"second"}]},
			{"type":"function_call","call_id":"call-1","name":"run_command","arguments":"{\"command\":\"true\"}"},
			{"type":"function_call_output","call_id":"call-1","output":"ok"}
		]
	}`
	result := ConvertOpenAIResponsesRequestToGemini("gemini-3.6-flash-high", []byte(inputJSON), false)
	var signatures []string
	for _, content := range gjson.GetBytes(result, "contents").Array() {
		for _, part := range content.Get("parts").Array() {
			if signature := part.Get("thoughtSignature").String(); signature != "" {
				signatures = append(signatures, signature)
			}
		}
	}
	if len(signatures) != 2 || signatures[0] != testResponsesGeminiThoughtSignature || signatures[1] != secondSignature {
		t.Fatalf("reasoning/function signatures = %v; result=%s", signatures, result)
	}
}

func TestConvertOpenAIResponsesRequestToGemini_PreservesFunctionOutputOrderAcrossModelText(t *testing.T) {
	inputJSON := `{
		"model":"gemini-3.6-flash-high",
		"input":[
			{"type":"function_call","call_id":"call-1","name":"run_command","arguments":"{\"command\":\"one\"}"},
			{"type":"message","role":"assistant","content":[{"type":"output_text","text":"between"}]},
			{"type":"function_call","call_id":"call-2","name":"run_command","arguments":"{\"command\":\"two\"}"},
			{"type":"function_call_output","call_id":"call-1","output":"one"},
			{"type":"function_call_output","call_id":"call-2","output":"two"}
		]
	}`
	result := ConvertOpenAIResponsesRequestToGemini("gemini-3.6-flash-high", []byte(inputJSON), false)
	var sequence []string
	for _, content := range gjson.GetBytes(result, "contents").Array() {
		for _, part := range content.Get("parts").Array() {
			if id := part.Get("functionCall.id").String(); id != "" {
				sequence = append(sequence, "call:"+id)
			}
			if id := part.Get("functionResponse.id").String(); id != "" {
				sequence = append(sequence, "output:"+id)
			}
		}
	}
	if got := strings.Join(sequence, ","); got != "call:call-1,call:call-2,output:call-1,output:call-2" {
		t.Fatalf("function output order = %q; result=%s", got, result)
	}
}

func TestConvertOpenAIResponsesRequestToGemini_ReattachesTrailingDetachedSignatureToText(t *testing.T) {
	inputJSON := `{
		"model":"gemini-3.6-flash-high",
		"input":[
			{"type":"message","role":"user","content":[{"type":"input_text","text":"turn one"}]},
			{"type":"message","role":"assistant","content":[{"type":"output_text","text":"visible answer"}]},
			{"id":"rs_text_detached_after_1","type":"reasoning","encrypted_content":"` + testResponsesGeminiThoughtSignature + `","summary":[]},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"turn two"}]}
		]
	}`
	result := ConvertOpenAIResponsesRequestToGemini("gemini-3.6-flash-high", []byte(inputJSON), false)
	parts := gjson.GetBytes(result, "contents.1.parts").Array()
	if len(parts) != 1 {
		t.Fatalf("model parts = %d, want one signed visible part; result=%s", len(parts), result)
	}
	if got := parts[0].Get("text").String(); got != "visible answer" {
		t.Fatalf("visible text = %q; result=%s", got, result)
	}
	if got := parts[0].Get("thoughtSignature").String(); got != testResponsesGeminiThoughtSignature {
		t.Fatalf("signature = %q, want detached signature; result=%s", got, result)
	}
	if parts[0].Get("thought").Bool() {
		t.Fatalf("detached visible carrier must not emit an empty thought part; result=%s", result)
	}
}

func TestConvertOpenAIResponsesRequestToGemini_ReattachesUnmarkedTrailingSignatureToText(t *testing.T) {
	inputJSON := `{
		"model":"gemini-3.5-flash",
		"input":[
			{"type":"message","role":"user","content":[{"type":"input_text","text":"turn one"}]},
			{"type":"message","role":"assistant","content":[{"type":"output_text","text":"visible answer"}]},
			{"id":"rs_client_rewritten","type":"reasoning","encrypted_content":"` + testResponsesGeminiThoughtSignature + `","summary":[]},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"turn two"}]}
		]
	}`
	result := ConvertOpenAIResponsesRequestToGemini("gemini-3.5-flash", []byte(inputJSON), false)
	parts := gjson.GetBytes(result, "contents.1.parts").Array()
	if len(parts) != 1 {
		t.Fatalf("model parts = %d, want one signed visible part after client rewrites carrier ID; result=%s", len(parts), result)
	}
	if got := parts[0].Get("text").String(); got != "visible answer" {
		t.Fatalf("visible text = %q; result=%s", got, result)
	}
	if got := parts[0].Get("thoughtSignature").String(); got != testResponsesGeminiThoughtSignature {
		t.Fatalf("signature = %q, want unmarked trailing signature; result=%s", got, result)
	}
}

func TestConvertOpenAIResponsesRequestToGemini_UnmarkedReasoningBeforeFunctionCallStillPairsCall(t *testing.T) {
	inputJSON := `{
		"model":"gemini-3.5-flash",
		"input":[
			{"type":"message","role":"user","content":[{"type":"input_text","text":"run"}]},
			{"type":"message","role":"assistant","content":[{"type":"output_text","text":"I will run it."}]},
			{"type":"reasoning","encrypted_content":"` + testResponsesGeminiThoughtSignature + `","summary":[]},
			{"type":"function_call","call_id":"call-1","name":"run_command","arguments":"{\"command\":\"true\"}"},
			{"type":"function_call_output","call_id":"call-1","output":"ok"}
		]
	}`
	result := ConvertOpenAIResponsesRequestToGemini("gemini-3.5-flash", []byte(inputJSON), false)
	modelParts := gjson.GetBytes(result, "contents.1.parts").Array()
	if len(modelParts) != 2 {
		t.Fatalf("model parts = %d, want unsigned preamble plus signed call; result=%s", len(modelParts), result)
	}
	if signature := modelParts[0].Get("thoughtSignature"); signature.Exists() {
		t.Fatalf("function-call signature was retargeted to preamble; result=%s", result)
	}
	if got := modelParts[1].Get("thoughtSignature").String(); got != testResponsesGeminiThoughtSignature {
		t.Fatalf("function signature = %q, want unmarked reasoning signature; result=%s", got, result)
	}
}

func TestConvertOpenAIResponsesRequestToGemini_ReattachesDetachedSignatureToFunctionCall(t *testing.T) {
	inputJSON := `{
		"model":"gemini-3.6-flash-high",
		"input":[
			{"type":"message","role":"user","content":[{"type":"input_text","text":"run"}]},
			{"type":"function_call","call_id":"call-1","name":"run_command","arguments":"{\"command\":\"true\"}"},
			{"id":"rs_function_detached_after_1","type":"reasoning","encrypted_content":"` + testResponsesGeminiThoughtSignature + `","summary":[]},
			{"type":"function_call_output","call_id":"call-1","output":"ok"}
		]
	}`
	result := ConvertOpenAIResponsesRequestToGemini("gemini-3.6-flash-high", []byte(inputJSON), false)
	functionParts := gjson.GetBytes(result, "contents.#(role==\"model\")#.parts").Array()
	found := false
	for _, partArray := range functionParts {
		for _, part := range partArray.Array() {
			if part.Get("functionCall.name").String() != "run_command" {
				continue
			}
			found = true
			if got := part.Get("thoughtSignature").String(); got != testResponsesGeminiThoughtSignature {
				t.Fatalf("function signature = %q, want detached signature; result=%s", got, result)
			}
		}
	}
	if !found {
		t.Fatalf("function call not found; result=%s", result)
	}
}

func TestConvertOpenAIResponsesRequestToGemini_ReattachesUnmarkedPostCallSignatureWithMatchingOutput(t *testing.T) {
	inputJSON := `{
		"model":"gemini-3.6-flash-high",
		"input":[
			{"type":"function_call","call_id":"call-1","name":"run_command","arguments":"{\"command\":\"true\"}"},
			{"type":"reasoning","encrypted_content":"` + testResponsesGeminiThoughtSignature + `","summary":[]},
			{"type":"function_call_output","call_id":"call-1","output":"ok"}
		]
	}`
	result := ConvertOpenAIResponsesRequestToGemini("gemini-3.6-flash-high", []byte(inputJSON), false)
	if got := gjson.GetBytes(result, "contents.0.parts.0.thoughtSignature").String(); got != testResponsesGeminiThoughtSignature {
		t.Fatalf("unmarked post-call signature = %q, want native signature; result=%s", got, result)
	}
}

func TestConvertOpenAIResponsesRequestToGemini_ReattachesDirectionalFunctionCarriersWithoutIDs(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		direction string
		input     func(string) string
	}{
		{
			name:      "leading",
			direction: geminiResponsesCarrierNext,
			input: func(carrier string) string {
				return `[{"type":"reasoning","encrypted_content":"` + carrier + `","summary":[]},{"type":"function_call","call_id":"call-1","name":"run_command","arguments":"{}"},{"type":"function_call_output","call_id":"call-1","output":"ok"}]`
			},
		},
		{
			name:      "post-call",
			direction: geminiResponsesCarrierPrevious,
			input: func(carrier string) string {
				return `[{"type":"function_call","call_id":"call-1","name":"run_command","arguments":"{}"},{"type":"reasoning","encrypted_content":"` + carrier + `","summary":[]},{"type":"function_call_output","call_id":"call-1","output":"ok"}]`
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			carrier := encodeGeminiResponsesCarrier(testResponsesGeminiThoughtSignature, testCase.direction, geminiResponsesCarrierFunction)
			inputJSON := []byte(`{"model":"gemini-3.6-flash-high","input":` + testCase.input(carrier) + `}`)
			result := ConvertOpenAIResponsesRequestToGemini("gemini-3.6-flash-high", inputJSON, false)
			if got := gjson.GetBytes(result, "contents.0.parts.0.thoughtSignature").String(); got != testResponsesGeminiThoughtSignature {
				t.Fatalf("directional function signature = %q, want native signature; result=%s", got, result)
			}
			if strings.Contains(string(result), geminiResponsesCarrierPrefix) {
				t.Fatalf("directional function carrier leaked to Gemini wire: %s", result)
			}
		})
	}
}

func TestConvertOpenAIResponsesRequestToGemini_DoesNotRetargetExtraPreviousCarrier(t *testing.T) {
	signature2 := differentResponsesGeminiThoughtSignature(t)
	for _, testCase := range []struct {
		name       string
		targetKind string
		input      func(string, string) string
		assert     func(*testing.T, []gjson.Result)
	}{
		{
			name:       "text",
			targetKind: geminiResponsesCarrierText,
			input: func(first, extra string) string {
				return `[{"type":"reasoning","encrypted_content":"` + first + `","summary":[]},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"signed"}]},{"type":"reasoning","encrypted_content":"` + extra + `","summary":[]},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"unsigned"}]}]`
			},
			assert: func(t *testing.T, parts []gjson.Result) {
				if len(parts) != 3 || parts[0].Get("text").String() != "signed" || parts[0].Get("thoughtSignature").String() != testResponsesGeminiThoughtSignature || !parts[1].Get("text").Exists() || parts[1].Get("text").String() != "" || parts[1].Get("thoughtSignature").String() != signature2 || parts[2].Get("text").String() != "unsigned" || parts[2].Get("thoughtSignature").String() != "" {
					t.Fatalf("extra previous text carrier retargeted: %v", parts)
				}
			},
		},
		{
			name:       "function",
			targetKind: geminiResponsesCarrierFunction,
			input: func(first, extra string) string {
				return `[{"type":"reasoning","encrypted_content":"` + first + `","summary":[]},{"type":"function_call","call_id":"call-1","name":"run_command","arguments":"{}"},{"type":"reasoning","encrypted_content":"` + extra + `","summary":[]},{"type":"function_call","call_id":"call-2","name":"run_command","arguments":"{}"}]`
			},
			assert: func(t *testing.T, parts []gjson.Result) {
				if len(parts) != 3 || parts[0].Get("functionCall.id").String() != "call-1" || parts[0].Get("thoughtSignature").String() != testResponsesGeminiThoughtSignature || !parts[1].Get("text").Exists() || parts[1].Get("text").String() != "" || parts[1].Get("thoughtSignature").String() != signature2 || parts[2].Get("functionCall.id").String() != "call-2" || parts[2].Get("thoughtSignature").String() != "" {
					t.Fatalf("extra previous function carrier retargeted: %v", parts)
				}
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			first := encodeGeminiResponsesCarrier(testResponsesGeminiThoughtSignature, geminiResponsesCarrierNext, testCase.targetKind)
			extra := encodeGeminiResponsesCarrier(signature2, geminiResponsesCarrierPrevious, testCase.targetKind)
			request := []byte(`{"model":"gemini-3.6-flash-high","input":` + testCase.input(first, extra) + `}`)
			translated := ConvertOpenAIResponsesRequestToGemini("gemini-3.6-flash-high", request, false)
			testCase.assert(t, gjson.GetBytes(translated, "contents.0.parts").Array())
		})
	}
}

func TestConvertOpenAIResponsesRequestToGemini_DoesNotBindStandaloneFunctionCarrier(t *testing.T) {
	carrier := encodeGeminiResponsesCarrier(testResponsesGeminiThoughtSignature, geminiResponsesCarrierStandalone, geminiResponsesCarrierFunction)
	inputJSON := []byte(`{"model":"gemini-3.6-flash-high","input":[{"type":"reasoning","encrypted_content":"` + carrier + `","summary":[]},{"type":"function_call","call_id":"call-1","name":"run_command","arguments":"{}"}]}`)
	result := ConvertOpenAIResponsesRequestToGemini("gemini-3.6-flash-high", inputJSON, false)
	parts := gjson.GetBytes(result, "contents.0.parts").Array()
	if len(parts) != 2 || parts[0].Get("thoughtSignature").String() != testResponsesGeminiThoughtSignature || parts[1].Get("thoughtSignature").String() != geminiResponsesThoughtSignature {
		t.Fatalf("standalone carrier was bound to function call: %s", result)
	}
}

func TestConvertOpenAIResponsesRequestToGemini_ReattachesUnmarkedParallelPostCallSignature(t *testing.T) {
	inputJSON := `{
		"model":"gemini-3.6-flash-high",
		"input":[
			{"type":"function_call","call_id":"call-1","name":"run_command","arguments":"{\"command\":\"one\"}"},
			{"type":"function_call","call_id":"call-2","name":"run_command","arguments":"{\"command\":\"two\"}"},
			{"type":"reasoning","encrypted_content":"` + testResponsesGeminiThoughtSignature + `","summary":[]},
			{"type":"function_call_output","call_id":"call-1","output":"one"},
			{"type":"function_call_output","call_id":"call-2","output":"two"}
		]
	}`
	result := ConvertOpenAIResponsesRequestToGemini("gemini-3.6-flash-high", []byte(inputJSON), false)
	parts := gjson.GetBytes(result, "contents.0.parts").Array()
	if len(parts) != 2 || parts[0].Get("thoughtSignature").String() != geminiResponsesThoughtSignature || parts[1].Get("thoughtSignature").String() != testResponsesGeminiThoughtSignature {
		t.Fatalf("parallel post-call signature was not attached to call-2: %s", result)
	}
}

func TestConvertOpenAIResponsesRequestToGemini_ReattachesAlternatingParallelPostCallSignatures(t *testing.T) {
	signature2 := differentResponsesGeminiThoughtSignature(t)
	inputJSON := `{
		"model":"gemini-3.6-flash-high",
		"input":[
			{"type":"function_call","call_id":"call-1","name":"run_command","arguments":"{\"command\":\"one\"}"},
			{"type":"reasoning","encrypted_content":"` + testResponsesGeminiThoughtSignature + `","summary":[]},
			{"type":"function_call","call_id":"call-2","name":"run_command","arguments":"{\"command\":\"two\"}"},
			{"type":"reasoning","encrypted_content":"` + signature2 + `","summary":[]},
			{"type":"function_call_output","call_id":"call-1","output":"one"},
			{"type":"function_call_output","call_id":"call-2","output":"two"}
		]
	}`
	result := ConvertOpenAIResponsesRequestToGemini("gemini-3.6-flash-high", []byte(inputJSON), false)
	parts := gjson.GetBytes(result, "contents.0.parts").Array()
	if len(parts) != 2 || parts[0].Get("functionCall.id").String() != "call-1" || parts[0].Get("thoughtSignature").String() != testResponsesGeminiThoughtSignature || parts[1].Get("functionCall.id").String() != "call-2" || parts[1].Get("thoughtSignature").String() != signature2 {
		t.Fatalf("alternating parallel post-call signatures shifted: %s", result)
	}
}

func TestConvertOpenAIResponsesRequestToGemini_PreservesExtraConsecutivePostCallCarrier(t *testing.T) {
	signature2 := differentResponsesGeminiThoughtSignature(t)
	inputJSON := `{
		"model":"gemini-3.6-flash-high",
		"input":[
			{"type":"function_call","call_id":"call-1","name":"run_command","arguments":"{}"},
			{"type":"reasoning","encrypted_content":"` + testResponsesGeminiThoughtSignature + `","summary":[]},
			{"type":"reasoning","encrypted_content":"` + signature2 + `","summary":[]},
			{"type":"function_call_output","call_id":"call-1","output":"ok"}
		]
	}`
	result := ConvertOpenAIResponsesRequestToGemini("gemini-3.6-flash-high", []byte(inputJSON), false)
	parts := gjson.GetBytes(result, "contents.0.parts").Array()
	if len(parts) != 2 || parts[0].Get("functionCall.id").String() != "call-1" || parts[0].Get("thoughtSignature").String() != testResponsesGeminiThoughtSignature || parts[1].Get("thoughtSignature").String() != signature2 {
		t.Fatalf("consecutive post-call carriers malformed: %s", result)
	}
}

func TestConvertOpenAIResponsesRequestToGemini_DoesNotPairUnmarkedPostCallSignatureAcrossMismatch(t *testing.T) {
	inputJSON := `{
		"model":"gemini-3.6-flash-high",
		"input":[
			{"type":"function_call","call_id":"call-1","name":"run_command","arguments":"{}"},
			{"type":"reasoning","encrypted_content":"` + testResponsesGeminiThoughtSignature + `","summary":[]},
			{"type":"function_call_output","call_id":"other-call","output":"ok"}
		]
	}`
	result := ConvertOpenAIResponsesRequestToGemini("gemini-3.6-flash-high", []byte(inputJSON), false)
	if got := gjson.GetBytes(result, "contents.0.parts.0.thoughtSignature").String(); got != geminiResponsesThoughtSignature {
		t.Fatalf("mismatched output paired signature %q; result=%s", got, result)
	}
}

func TestConvertOpenAIResponsesRequestToGemini_DoesNotPairUnmarkedPostCallSignatureAcrossUserMessage(t *testing.T) {
	inputJSON := `{
		"model":"gemini-3.6-flash-high",
		"input":[
			{"type":"function_call","call_id":"call-1","name":"run_command","arguments":"{}"},
			{"type":"reasoning","encrypted_content":"` + testResponsesGeminiThoughtSignature + `","summary":[]},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"boundary"}]},
			{"type":"function_call_output","call_id":"call-1","output":"ok"}
		]
	}`
	result := ConvertOpenAIResponsesRequestToGemini("gemini-3.6-flash-high", []byte(inputJSON), false)
	if got := gjson.GetBytes(result, "contents.0.parts.0.thoughtSignature").String(); got != geminiResponsesThoughtSignature {
		t.Fatalf("user-boundary carrier paired signature %q; result=%s", got, result)
	}
}

func TestConvertOpenAIResponsesRequestToGemini_StripsTrailingAssistantPrefill(t *testing.T) {
	inputJSON := `{
		"model": "gpt-5.4",
		"input": [
			{
				"type": "message",
				"role": "user",
				"content": [{"type": "input_text", "text": "hello"}]
			},
			{
				"type": "message",
				"role": "assistant",
				"content": [{"type": "output_text", "text": "previous answer"}]
			}
		]
	}`

	result := ConvertOpenAIResponsesRequestToGemini("gemini-3.1-pro-high", []byte(inputJSON), false)
	resultJSON := gjson.ParseBytes(result)
	contents := resultJSON.Get("contents").Array()

	if len(contents) != 1 {
		t.Fatalf("contents length = %d, want 1. contents=%s", len(contents), resultJSON.Get("contents").Raw)
	}
	if got := contents[0].Get("role").String(); got != "user" {
		t.Fatalf("final remaining role = %q, want %q", got, "user")
	}
}

func TestConvertOpenAIResponsesRequestToGemini_TextFormatJSONSchema(t *testing.T) {
	inputJSON := `{
		"model": "gemini-flash-lite",
		"temperature": 0.2,
		"input": [
			{
				"role": "user",
				"content": [
					{
						"type": "input_text",
						"text": "Return structured JSON."
					}
				]
			}
		],
		"text": {
			"format": {
				"type": "json_schema",
				"strict": true,
				"name": "response",
				"schema": {
					"type": "object",
					"properties": {
						"cleanedContent": {
							"type": "string"
						}
					},
					"required": [
						"cleanedContent"
					],
					"additionalProperties": false
				}
			}
		}
	}`

	output := ConvertOpenAIResponsesRequestToGemini("gemini-3.1-flash-lite", []byte(inputJSON), false)
	result := gjson.ParseBytes(output)
	genConfig := result.Get("generationConfig")

	if got := genConfig.Get("responseMimeType").String(); got != "application/json" {
		t.Fatalf("responseMimeType = %q, want application/json. Output: %s", got, output)
	}
	schema := genConfig.Get("responseJsonSchema")
	if !schema.Exists() {
		t.Fatalf("responseJsonSchema missing. Output: %s", output)
	}
	if genConfig.Get("responseSchema").Exists() {
		t.Fatalf("responseSchema should not be set with responseJsonSchema. Output: %s", output)
	}
	if got := schema.Get("type").String(); got != "object" {
		t.Fatalf("schema type = %q, want object. Output: %s", got, output)
	}
	if got := schema.Get("properties.cleanedContent.type").String(); got != "string" {
		t.Fatalf("cleanedContent type = %q, want string. Output: %s", got, output)
	}
	if additionalProperties := schema.Get("additionalProperties"); !additionalProperties.Exists() || additionalProperties.Bool() {
		t.Fatalf("additionalProperties = %s, want false. Output: %s", additionalProperties.Raw, output)
	}
	if got := genConfig.Get("temperature").Float(); got != 0.2 {
		t.Fatalf("temperature = %v, want 0.2. Output: %s", got, output)
	}
}

func TestConvertOpenAIResponsesRequestToGemini_TextFormatJSONObject(t *testing.T) {
	inputJSON := `{
		"model": "gemini-flash-lite",
		"input": "Return a JSON object.",
		"text": {
			"format": {
				"type": "json_object"
			}
		}
	}`

	output := ConvertOpenAIResponsesRequestToGemini("gemini-3.1-flash-lite", []byte(inputJSON), false)
	result := gjson.ParseBytes(output)
	genConfig := result.Get("generationConfig")

	if got := genConfig.Get("responseMimeType").String(); got != "application/json" {
		t.Fatalf("responseMimeType = %q, want application/json. Output: %s", got, output)
	}
	if genConfig.Get("responseJsonSchema").Exists() {
		t.Fatalf("responseJsonSchema should not be set for json_object. Output: %s", output)
	}
}

func TestConvertOpenAIResponsesRequestToGemini_PreservesReasoningOnlyHistory(t *testing.T) {
	input := []byte(`{
		"model": "gpt-5",
		"input": [{
			"type": "reasoning",
			"encrypted_content": "gemini#` + testResponsesGeminiThoughtSignature + `",
			"summary": [{"type": "summary_text", "text": "reasoning summary"}]
		}]
	}`)

	output := ConvertOpenAIResponsesRequestToGemini("gemini-3.5-flash", input, false)
	parts := gjson.GetBytes(output, "contents.0.parts").Array()
	if got := gjson.GetBytes(output, "contents").Array(); len(got) != 1 {
		t.Fatalf("contents length = %d, want 1. Output: %s", len(got), output)
	}
	if len(parts) != 1 {
		t.Fatalf("parts length = %d, want 1. Output: %s", len(parts), output)
	}
	if got := parts[0].Get("thought").Bool(); !got {
		t.Fatalf("parts[0] should be thought. Output: %s", output)
	}
	if got := parts[0].Get("thoughtSignature").String(); got != testResponsesGeminiThoughtSignature {
		t.Fatalf("parts[0].thoughtSignature = %q, want %q. Output: %s", got, testResponsesGeminiThoughtSignature, output)
	}
	if got := parts[0].Get("text").String(); got != "reasoning summary" {
		t.Fatalf("thought text = %q, want reasoning summary. Output: %s", got, output)
	}
}

func TestConvertOpenAIResponsesRequestToGemini_DropsEmptyUnsignedReasoningCarrier(t *testing.T) {
	input := []byte(`{
		"model":"gemini-3.6-flash-high",
		"input":[{"type":"reasoning","encrypted_content":"","summary":[]}]
	}`)

	output := ConvertOpenAIResponsesRequestToGemini("gemini-3.6-flash-high", input, false)
	if got := gjson.GetBytes(output, "contents.#").Int(); got != 0 {
		t.Fatalf("contents = %d, want no empty unsigned model content; output=%s", got, output)
	}
}

func TestConvertOpenAIResponsesRequestToGemini_PreservesUnboundDetachedCarrierWithoutEmptyThought(t *testing.T) {
	input := []byte(`{
		"model": "gemini-3.6-flash-high",
		"input": [{
			"id": "rs_unbound_detached_after_1",
			"type": "reasoning",
			"encrypted_content": "` + testResponsesGeminiThoughtSignature + `",
			"summary": []
		}]
	}`)

	output := ConvertOpenAIResponsesRequestToGemini("gemini-3.6-flash-high", input, false)
	parts := gjson.GetBytes(output, "contents.0.parts").Array()
	if len(parts) != 1 {
		t.Fatalf("unbound carrier parts = %d, want one signed carrier; output=%s", len(parts), output)
	}
	if parts[0].Get("thought").Bool() || !parts[0].Get("text").Exists() || parts[0].Get("text").String() != "" {
		t.Fatalf("unbound carrier emitted an empty thought part: %s", output)
	}
	if got := parts[0].Get("thoughtSignature").String(); got != testResponsesGeminiThoughtSignature {
		t.Fatalf("unbound carrier signature = %q, want %q; output=%s", got, testResponsesGeminiThoughtSignature, output)
	}
}

func TestConvertOpenAIResponsesRequestToGemini_PreservesReasoningBeforeTrailingAssistantPrefill(t *testing.T) {
	inputJSON := `{
		"model": "gpt-5.4",
		"input": [
			{
				"type": "message",
				"role": "user",
				"content": [{"type": "input_text", "text": "hello"}]
			},
			{
				"type": "reasoning",
				"encrypted_content": "gemini#` + testResponsesGeminiThoughtSignature + `",
				"summary": [{"type": "summary_text", "text": "reasoning summary"}]
			},
			{
				"type": "message",
				"role": "assistant",
				"content": [{"type": "output_text", "text": "previous answer"}]
			}
		]
	}`

	output := ConvertOpenAIResponsesRequestToGemini("gemini-3.5-flash", []byte(inputJSON), false)
	contents := gjson.GetBytes(output, "contents").Array()
	if len(contents) != 2 {
		t.Fatalf("contents length = %d, want 2. Output: %s", len(contents), output)
	}
	if got := contents[0].Get("role").String(); got != "user" {
		t.Fatalf("contents[0].role = %q, want user", got)
	}
	if got := contents[1].Get("parts.1.thoughtSignature").String(); got != testResponsesGeminiThoughtSignature {
		t.Fatalf("reasoning visible thoughtSignature = %q, want preserved signature", got)
	}
}

func TestConvertOpenAIResponsesRequestToGemini_ReasoningSignatureCompatibility(t *testing.T) {
	tests := []struct {
		name          string
		encrypted     string
		wantSignature string
	}{
		{
			name:          "GPT encrypted_content is dropped from Gemini thought",
			encrypted:     validResponsesGPTReasoningSignature(),
			wantSignature: "",
		},
		{
			name:          "Gemini encrypted_content is preserved",
			encrypted:     "gemini#" + testResponsesGeminiThoughtSignature,
			wantSignature: testResponsesGeminiThoughtSignature,
		},
		{
			name:          "Missing encrypted_content leaves Gemini thought unsigned",
			encrypted:     "",
			wantSignature: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := []byte(`{
				"model": "gpt-5",
				"input": [{
					"type": "reasoning",
					"encrypted_content": "` + tt.encrypted + `",
					"summary": [{"type": "summary_text", "text": "reasoning summary"}]
				}]
			}`)

			output := ConvertOpenAIResponsesRequestToGemini("gemini-3.5-flash", input, false)
			parts := gjson.GetBytes(output, "contents.0.parts").Array()
			if len(parts) != 1 {
				t.Fatalf("parts length = %d, want 1. Output: %s", len(parts), output)
			}
			if got := parts[0].Get("thoughtSignature").String(); got != tt.wantSignature {
				t.Fatalf("thoughtSignature = %q, want %q. Output: %s", got, tt.wantSignature, output)
			}
			if got := parts[0].Get("text").String(); got != "reasoning summary" {
				t.Fatalf("thought text = %q, want reasoning summary. Output: %s", got, output)
			}
		})
	}
}

func TestConvertOpenAIResponsesRequestToGemini_MergesReasoningWithAssistantVisibleAnswer(t *testing.T) {
	inputJSON := `{
		"model": "gemini-3.5-flash",
		"input": [
			{
				"type": "reasoning",
				"encrypted_content": "gemini#` + testResponsesGeminiThoughtSignature + `",
				"summary": [{"type": "summary_text", "text": "internal reasoning"}]
			},
			{
				"type": "message",
				"role": "assistant",
				"content": [{"type": "output_text", "text": "visible answer"}]
			},
			{
				"type": "message",
				"role": "user",
				"content": [{"type": "input_text", "text": "continue"}]
			}
		]
	}`

	output := ConvertOpenAIResponsesRequestToGemini("gemini-3.5-flash", []byte(inputJSON), false)
	contents := gjson.GetBytes(output, "contents").Array()
	if len(contents) != 2 {
		t.Fatalf("contents length = %d, want 2. Output: %s", len(contents), output)
	}
	parts := contents[0].Get("parts").Array()
	if len(parts) != 2 {
		t.Fatalf("model parts length = %d, want 2. Output: %s", len(parts), output)
	}
	if got := parts[0].Get("thought").Bool(); !got {
		t.Fatalf("parts[0] should be thought. Output: %s", output)
	}
	if got := parts[0].Get("thoughtSignature").String(); got != "" {
		t.Fatalf("parts[0].thoughtSignature = %q, want empty. Output: %s", got, output)
	}
	if got := parts[1].Get("text").String(); got != "visible answer" {
		t.Fatalf("visible text = %q, want visible answer. Output: %s", got, output)
	}
	if got := parts[1].Get("thoughtSignature").String(); got != testResponsesGeminiThoughtSignature {
		t.Fatalf("visible thoughtSignature = %q, want preserved signature", got)
	}
}

func TestConvertOpenAIResponsesRequestToGemini_MergesReasoningWithUserRoleOutputText(t *testing.T) {
	inputJSON := `{
		"model": "gemini-3.5-flash",
		"input": [
			{
				"type": "reasoning",
				"encrypted_content": "gemini#` + testResponsesGeminiThoughtSignature + `",
				"summary": [{"type": "summary_text", "text": "reasoning summary"}]
			},
			{
				"type": "message",
				"role": "user",
				"content": [{"type": "output_text", "text": "visible from user role"}]
			}
		]
	}`
	output := ConvertOpenAIResponsesRequestToGemini("gemini-3.5-flash", []byte(inputJSON), false)
	contents := gjson.GetBytes(output, "contents").Array()
	if len(contents) != 1 {
		t.Fatalf("contents length = %d, want 1. Output: %s", len(contents), output)
	}
	if got := contents[0].Get("parts.1.text").String(); got != "visible from user role" {
		t.Fatalf("visible text = %q", got)
	}
}

func TestConvertOpenAIResponsesRequestToGemini_MergesReasoningWithAssistantStringContent(t *testing.T) {
	inputJSON := `{
		"model": "gemini-3.5-flash",
		"input": [
			{
				"type": "reasoning",
				"encrypted_content": "gemini#` + testResponsesGeminiThoughtSignature + `",
				"summary": [{"type": "summary_text", "text": "reasoning summary"}]
			},
			{
				"type": "message",
				"role": "assistant",
				"content": "string visible answer"
			}
		]
	}`
	output := ConvertOpenAIResponsesRequestToGemini("gemini-3.5-flash", []byte(inputJSON), false)
	if got := gjson.GetBytes(output, "contents.0.parts.1.text").String(); got != "string visible answer" {
		t.Fatalf("visible text = %q", got)
	}
}

func TestConvertOpenAIResponsesRequestToGemini_PreservesWhitespaceWhenMergingReasoning(t *testing.T) {
	inputJSON := `{
		"model": "gemini-3.5-flash",
		"input": [
			{
				"type": "reasoning",
				"encrypted_content": "gemini#` + testResponsesGeminiThoughtSignature + `",
				"summary": [{"type": "summary_text", "text": "reasoning summary"}]
			},
			{
				"type": "message",
				"role": "assistant",
				"content": [{"type": "output_text", "text": "  lead trail  "}]
			},
			{
				"type": "message",
				"role": "user",
				"content": [{"type": "input_text", "text": "next"}]
			}
		]
	}`
	output := ConvertOpenAIResponsesRequestToGemini("gemini-3.5-flash", []byte(inputJSON), false)
	if got := gjson.GetBytes(output, "contents.0.parts.1.text").String(); got != "  lead trail  " {
		t.Fatalf("visible text = %q, want preserved whitespace", got)
	}
}
func TestConvertOpenAIResponsesRequestToGemini_SystemAndDeveloperRoles(t *testing.T) {
	tests := []struct {
		name     string
		role     string
		wantText string
	}{
		{
			name:     "system role",
			role:     "system",
			wantText: "System message text",
		},
		{
			name:     "developer role",
			role:     "developer",
			wantText: "Developer message text",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := []byte(`{
				"instructions": "Be a helpful assistant",
				"input": [
					{
						"type": "message",
						"role": "` + tt.role + `",
						"content": [
							{
								"type": "input_text",
								"text": "` + tt.wantText + `"
							}
						]
					},
					{
						"type": "message",
						"role": "user",
						"content": [
							{
								"type": "input_text",
								"text": "Hello"
							}
						]
					}
				]
			}`)

			output := ConvertOpenAIResponsesRequestToGemini("gemini-3.5-flash", input, false)
			result := gjson.ParseBytes(output)

			systemInstruction := result.Get("systemInstruction")
			if !systemInstruction.Exists() {
				t.Fatalf("systemInstruction missing. Output: %s", output)
			}
			parts := systemInstruction.Get("parts")
			if got := parts.Get("#").Int(); got != 2 {
				t.Fatalf("systemInstruction parts = %d, want 2. Output: %s", got, output)
			}
			if got := parts.Get("0.text").String(); got != "Be a helpful assistant" {
				t.Fatalf("first systemInstruction part = %q, want %q. Output: %s", got, "Be a helpful assistant", output)
			}
			if got := parts.Get("1.text").String(); got != tt.wantText {
				t.Fatalf("second systemInstruction part = %q, want %q. Output: %s", got, tt.wantText, output)
			}

			result.Get("contents").ForEach(func(_, value gjson.Result) bool {
				if role := value.Get("role").String(); role == tt.role {
					t.Fatalf("role %q leaked into contents array. Output: %s", tt.role, output)
				}
				return true
			})
		})
	}
}

func TestConvertOpenAIResponsesRequestToGeminiCleansToolSchemaRequiredFields(t *testing.T) {
	inputJSON := `{
		"model": "gemini-2.0-flash",
		"input": "hi",
		"tools": [{
			"type": "function",
			"name": "search_company",
			"description": "Search",
			"parameters": {
				"type": "object",
				"title": "SearchCompany",
				"properties": {
					"country": {"type": "string"},
					"industry": {"type": "string"}
				},
				"required": ["country", "industry", "stale_field", "another_stale"]
			}
		}]
	}`

	output := ConvertOpenAIResponsesRequestToGemini("gemini-2.0-flash", []byte(inputJSON), false)
	schema := gjson.GetBytes(output, "tools.0.functionDeclarations.0.parametersJsonSchema")

	if !schema.Exists() {
		t.Fatalf("parametersJsonSchema missing. Output: %s", output)
	}
	if schema.Get("title").Exists() {
		t.Fatalf("schema title should be removed. Output: %s", output)
	}
	required := schema.Get("required").Array()
	if len(required) != 2 {
		t.Fatalf("required length = %d, want 2. Schema: %s", len(required), schema.Raw)
	}
	if got := required[0].String(); got != "country" {
		t.Fatalf("required[0] = %q, want country. Schema: %s", got, schema.Raw)
	}
	if got := required[1].String(); got != "industry" {
		t.Fatalf("required[1] = %q, want industry. Schema: %s", got, schema.Raw)
	}
}

func validResponsesGPTReasoningSignature() string {
	raw := make([]byte, 1+8+16+16+32)
	raw[0] = 0x80
	raw[8] = 1
	for i := 9; i < len(raw); i++ {
		raw[i] = byte(i)
	}
	return base64.URLEncoding.EncodeToString(raw)
}

func TestConvertOpenAIResponsesRequestToGemini_FunctionCallOutputWithImages(t *testing.T) {
	inputJSON := `{
		"model": "gemini-3.7-flash-high",
		"input": [
			{
				"role": "user",
				"content": [
					{
						"type": "input_text",
						"text": "Below is the image from tool. Reply IMAGE_SEEN."
					}
				]
			},
			{
				"type": "function_call",
				"id": "fc_test",
				"call_id": "call_test",
				"name": "read",
				"arguments": "{}"
			},
			{
				"type": "function_call_output",
				"call_id": "call_test",
				"output": [
					{
						"type": "input_text",
						"text": "Read image file [image/png]"
					},
					{
						"type": "input_image",
						"detail": "auto",
						"image_url": "data:image/png;base64,iVBORw0KGgoAAAANSUhEUg=="
					}
				]
			}
		]
	}`

	output := ConvertOpenAIResponsesRequestToGemini("gemini-3.7-flash-high", []byte(inputJSON), false)
	userContent := gjson.GetBytes(output, "contents.2")
	if userContent.Get("role").String() != "user" {
		t.Fatalf("expected role user in third content, got %s", userContent.Raw)
	}

	parts := userContent.Get("parts").Array()
	if len(parts) < 2 {
		t.Fatalf("expected at least 2 parts (functionResponse + inline_data), got %d; raw: %s", len(parts), userContent.Raw)
	}

	fr := parts[0].Get("functionResponse")
	if !fr.Exists() {
		t.Fatalf("expected first part to be functionResponse, got %s", parts[0].Raw)
	}
	if got := fr.Get("name").String(); got != "read" {
		t.Fatalf("expected functionResponse.name = %q, got %q", "read", got)
	}
	if got := fr.Get("id").String(); got != "call_test" {
		t.Fatalf("expected functionResponse.id = %q, got %q", "call_test", got)
	}
	if got := fr.Get("response.result").String(); got != "Read image file [image/png]" {
		t.Fatalf("expected functionResponse.response.result = %q, got %q", "Read image file [image/png]", got)
	}

	img := parts[1].Get("inline_data")
	if !img.Exists() {
		t.Fatalf("expected second part to have inline_data, got %s", parts[1].Raw)
	}
	if got := img.Get("mime_type").String(); got != "image/png" {
		t.Fatalf("expected mime_type = %q, got %q", "image/png", got)
	}
	if got := img.Get("data").String(); got != "iVBORw0KGgoAAAANSUhEUg==" {
		t.Fatalf("expected data = %q, got %q", "iVBORw0KGgoAAAANSUhEUg==", got)
	}
}

func TestConvertOpenAIResponsesRequestToGemini_FunctionCallOutputVariations(t *testing.T) {
	t.Run("stringified JSON array with image", func(t *testing.T) {
		inputJSON := `{
			"model": "gemini-3.7-flash-high",
			"input": [
				{
					"type": "function_call",
					"call_id": "call_1",
					"name": "screenshot",
					"arguments": "{}"
				},
				{
					"type": "function_call_output",
					"call_id": "call_1",
					"output": "[{\"type\":\"input_text\",\"text\":\"done\"},{\"type\":\"input_image\",\"image_url\":\"data:image/jpeg;base64,/9j/4AAQSkZJRg==\"}]"
				}
			]
		}`
		output := ConvertOpenAIResponsesRequestToGemini("gemini-3.7-flash-high", []byte(inputJSON), false)
		userContent := gjson.GetBytes(output, "contents.1")
		parts := userContent.Get("parts").Array()
		if len(parts) != 1 {
			t.Fatalf("expected 1 part, got %d; raw: %s", len(parts), userContent.Raw)
		}
		expectedResult := `[{"type":"input_text","text":"done"},{"type":"input_image","image_url":"data:image/jpeg;base64,/9j/4AAQSkZJRg=="}]`
		if got := parts[0].Get("functionResponse.response.result").String(); got != expectedResult {
			t.Fatalf("expected result %q, got %q", expectedResult, got)
		}
	})

	t.Run("plain structured JSON array without images", func(t *testing.T) {
		inputJSON := `{
			"model": "gemini-3.7-flash-high",
			"input": [
				{
					"type": "function_call",
					"call_id": "call_1",
					"name": "list_items",
					"arguments": "{}"
				},
				{
					"type": "function_call_output",
					"call_id": "call_1",
					"output": [{"id": 1, "name": "first"}, {"id": 2, "name": "second"}]
				}
			]
		}`
		output := ConvertOpenAIResponsesRequestToGemini("gemini-3.7-flash-high", []byte(inputJSON), false)
		userContent := gjson.GetBytes(output, "contents.1")
		parts := userContent.Get("parts").Array()
		if len(parts) != 1 {
			t.Fatalf("expected 1 part, got %d; raw: %s", len(parts), userContent.Raw)
		}
		resultArr := parts[0].Get("functionResponse.response.result").Array()
		if len(resultArr) != 2 {
			t.Fatalf("expected 2 array items in result, got %d; raw: %s", len(resultArr), parts[0].Raw)
		}
		if got := resultArr[0].Get("name").String(); got != "first" {
			t.Fatalf("expected item 0 name 'first', got %q", got)
		}
	})

	t.Run("plain string output", func(t *testing.T) {
		inputJSON := `{
			"model": "gemini-3.7-flash-high",
			"input": [
				{
					"type": "function_call",
					"call_id": "call_1",
					"name": "echo",
					"arguments": "{}"
				},
				{
					"type": "function_call_output",
					"call_id": "call_1",
					"output": "plain string result"
				}
			]
		}`
		output := ConvertOpenAIResponsesRequestToGemini("gemini-3.7-flash-high", []byte(inputJSON), false)
		userContent := gjson.GetBytes(output, "contents.1")
		parts := userContent.Get("parts").Array()
		if len(parts) != 1 {
			t.Fatalf("expected 1 part, got %d; raw: %s", len(parts), userContent.Raw)
		}
		if got := parts[0].Get("functionResponse.response.result").String(); got != "plain string result" {
			t.Fatalf("expected 'plain string result', got %q", got)
		}
	})

	t.Run("structured JSON object with image_url property not an image block", func(t *testing.T) {
		inputJSON := `{
			"model": "gemini-3.7-flash-high",
			"input": [
				{
					"type": "function_call",
					"call_id": "call_1",
					"name": "get_hero",
					"arguments": "{}"
				},
				{
					"type": "function_call_output",
					"call_id": "call_1",
					"output": {
						"ok": true,
						"caption": "hero",
						"image_url": "https://example.com/hero.png"
					}
				}
			]
		}`
		output := ConvertOpenAIResponsesRequestToGemini("gemini-3.7-flash-high", []byte(inputJSON), false)
		userContent := gjson.GetBytes(output, "contents.1")
		parts := userContent.Get("parts").Array()
		if len(parts) != 1 {
			t.Fatalf("expected 1 part, got %d; raw: %s", len(parts), userContent.Raw)
		}
		if got := parts[0].Get("functionResponse.response.result.caption").String(); got != "hero" {
			t.Fatalf("expected caption 'hero', got %q", got)
		}
	})

	t.Run("mixed array with text and non-image structured object", func(t *testing.T) {
		inputJSON := `{
			"model": "gemini-3.7-flash-high",
			"input": [
				{
					"type": "function_call",
					"call_id": "call_1",
					"name": "query",
					"arguments": "{}"
				},
				{
					"type": "function_call_output",
					"call_id": "call_1",
					"output": [
						{"type": "input_text", "text": "summary header"},
						{"id": 1, "status": "active"}
					]
				}
			]
		}`
		output := ConvertOpenAIResponsesRequestToGemini("gemini-3.7-flash-high", []byte(inputJSON), false)
		userContent := gjson.GetBytes(output, "contents.1")
		parts := userContent.Get("parts").Array()
		if len(parts) != 1 {
			t.Fatalf("expected 1 part, got %d; raw: %s", len(parts), userContent.Raw)
		}
		resultArr := parts[0].Get("functionResponse.response.result").Array()
		if len(resultArr) != 2 {
			t.Fatalf("expected raw JSON array with 2 items, got %d; raw: %s", len(resultArr), parts[0].Raw)
		}
		if got := resultArr[1].Get("status").String(); got != "active" {
			t.Fatalf("expected item 1 status 'active', got %q", got)
		}
	})

	t.Run("stringified single-element object array preserved as string", func(t *testing.T) {
		inputJSON := `{
			"model": "gemini-3.7-flash-high",
			"input": [
				{
					"type": "function_call",
					"call_id": "call_1",
					"name": "lookup",
					"arguments": "{}"
				},
				{
					"type": "function_call_output",
					"call_id": "call_1",
					"output": "[{\"id\":1}]"
				}
			]
		}`
		output := ConvertOpenAIResponsesRequestToGemini("gemini-3.7-flash-high", []byte(inputJSON), false)
		userContent := gjson.GetBytes(output, "contents.1")
		parts := userContent.Get("parts").Array()
		if len(parts) != 1 {
			t.Fatalf("expected 1 part, got %d; raw: %s", len(parts), userContent.Raw)
		}
		if got := parts[0].Get("functionResponse.response.result").String(); got != `[{"id":1}]` {
			t.Fatalf("expected result to be %q, got %q", `[{"id":1}]`, got)
		}
	})

	t.Run("nested image_url object with detail", func(t *testing.T) {
		inputJSON := `{
			"model": "gemini-3.7-flash-high",
			"input": [
				{
					"type": "function_call",
					"call_id": "call_1",
					"name": "photo",
					"arguments": "{}"
				},
				{
					"type": "function_call_output",
					"call_id": "call_1",
					"output": [
						{"type": "input_image", "image_url": {"url": "data:image/png;base64,iVBORw0KGgoAAAANSUhEUg=="}, "detail": "high"}
					]
				}
			]
		}`
		output := ConvertOpenAIResponsesRequestToGemini("gemini-3.7-flash-high", []byte(inputJSON), false)
		userContent := gjson.GetBytes(output, "contents.1")
		parts := userContent.Get("parts").Array()
		if len(parts) != 2 {
			t.Fatalf("expected 2 parts (functionResponse + inline_data), got %d; raw: %s", len(parts), userContent.Raw)
		}
		if got := parts[1].Get("inline_data.mime_type").String(); got != "image/png" {
			t.Fatalf("expected mime_type 'image/png', got %q", got)
		}
		if got := parts[1].Get("inline_data.data").String(); got != "iVBORw0KGgoAAAANSUhEUg==" {
			t.Fatalf("expected data 'iVBORw0KGgoAAAANSUhEUg==', got %q", got)
		}
	})
}

func TestConvertOpenAIResponsesRequestToGemini_AdditionalToolsNamespaceAndCustom(t *testing.T) {
	inputJSON := `{
		"model": "gemini-2.5-flash",
		"input": [
			{
				"type": "additional_tools",
				"role": "developer",
				"tools": [
					{
						"type": "namespace",
						"name": "functions",
						"tools": [
							{
								"type": "custom",
								"name": "exec",
								"description": "Execute a command"
							},
							{
								"type": "function",
								"name": "continuity_probe",
								"description": "Return a continuity probe",
								"parameters": {
									"type": "object",
									"properties": {
										"value": {"type": "string"}
									},
									"required": ["value"]
								}
							}
						]
					}
				]
			},
			{
				"role": "user",
				"content": [
					{
						"type": "input_text",
						"text": "Run probe"
					}
				]
			}
		],
		"tool_choice": {
			"type": "function",
			"name": "continuity_probe",
			"namespace": "functions"
		}
	}`

	output := ConvertOpenAIResponsesRequestToGemini("gemini-2.5-flash", []byte(inputJSON), false)
	decls := gjson.GetBytes(output, "tools.0.functionDeclarations").Array()
	if len(decls) != 2 {
		t.Fatalf("expected 2 functionDeclarations, got %d; raw: %s", len(decls), output)
	}

	execDecl := decls[0]
	if got := execDecl.Get("name").String(); got != "functions__exec" {
		t.Fatalf("decl 0 name = %q, want functions__exec", got)
	}
	if got := execDecl.Get("parametersJsonSchema.properties.input.type").String(); got != "string" {
		t.Fatalf("decl 0 custom input schema missing: %s", execDecl.Raw)
	}

	probeDecl := decls[1]
	if got := probeDecl.Get("name").String(); got != "functions__continuity_probe" {
		t.Fatalf("decl 1 name = %q, want functions__continuity_probe", got)
	}

	mode := gjson.GetBytes(output, "toolConfig.functionCallingConfig.mode").String()
	if mode != "ANY" {
		t.Fatalf("toolConfig mode = %q, want ANY", mode)
	}
	allowed := gjson.GetBytes(output, "toolConfig.functionCallingConfig.allowedFunctionNames.0").String()
	if allowed != "functions__continuity_probe" {
		t.Fatalf("allowedFunctionNames = %q, want functions__continuity_probe", allowed)
	}
}

func TestConvertOpenAIResponsesRequestToGemini_ReplaysCustomToolCallAndOutput(t *testing.T) {
	inputJSON := `{
		"model": "gemini-2.5-flash",
		"input": [
			{
				"type": "additional_tools",
				"tools": [
					{
						"type": "namespace",
						"name": "functions",
						"tools": [
							{"type": "custom", "name": "exec"}
						]
					}
				]
			},
			{
				"type": "custom_tool_call",
				"call_id": "call_1",
				"name": "exec",
				"namespace": "functions",
				"input": "pwd"
			},
			{
				"type": "custom_tool_call_output",
				"call_id": "call_1",
				"output": "/workspace"
			}
		]
	}`

	output := ConvertOpenAIResponsesRequestToGemini("gemini-2.5-flash", []byte(inputJSON), false)
	contents := gjson.GetBytes(output, "contents").Array()
	if len(contents) < 2 {
		t.Fatalf("expected at least 2 contents, got %d; raw: %s", len(contents), output)
	}

	callPart := contents[0].Get("parts.0.functionCall")
	if !callPart.Exists() {
		t.Fatalf("missing functionCall in content 0: %s", contents[0].Raw)
	}
	if got := callPart.Get("name").String(); got != "functions__exec" {
		t.Fatalf("functionCall name = %q, want functions__exec", got)
	}
	if got := callPart.Get("args.input").String(); got != "pwd" {
		t.Fatalf("functionCall args.input = %q, want pwd", got)
	}

	respPart := contents[1].Get("parts.0.functionResponse")
	if !respPart.Exists() {
		t.Fatalf("missing functionResponse in content 1: %s", contents[1].Raw)
	}
	if got := respPart.Get("name").String(); got != "functions__exec" {
		t.Fatalf("functionResponse name = %q, want functions__exec", got)
	}
}

func TestConvertOpenAIResponsesRequestToGemini_TwoTurnCustomToolRoundtripWithReasoning(t *testing.T) {
	// Turn 2 request: includes reasoning carrier before custom_tool_call, then custom_tool_call_output
	inputJSON := `{
		"model": "gemini-3.6-flash-high",
		"input": [
			{
				"type": "additional_tools",
				"tools": [
					{
						"type": "namespace",
						"name": "functions",
						"tools": [
							{"type": "custom", "name": "exec"}
						]
					}
				]
			},
			{"type": "message", "role": "user", "content": [{"type": "input_text", "text": "Run pwd"}]},
			{"type": "reasoning", "encrypted_content": "` + testResponsesGeminiThoughtSignature + `", "summary": [{"type": "summary_text", "text": "executing pwd"}]},
			{
				"type": "custom_tool_call",
				"call_id": "call_1",
				"name": "exec",
				"namespace": "functions",
				"input": "pwd"
			},
			{
				"type": "custom_tool_call_output",
				"call_id": "call_1",
				"output": "/workspace"
			}
		]
	}`

	output := ConvertOpenAIResponsesRequestToGemini("gemini-3.6-flash-high", []byte(inputJSON), false)
	contents := gjson.GetBytes(output, "contents").Array()
	if len(contents) != 3 {
		t.Fatalf("expected 3 contents (user, model, user), got %d; raw: %s", len(contents), output)
	}

	modelParts := contents[1].Get("parts").Array()
	if len(modelParts) != 2 {
		t.Fatalf("expected 2 parts in model content (thought + functionCall), got %d; raw: %s", len(modelParts), contents[1].Raw)
	}
	if !modelParts[0].Get("thought").Bool() || modelParts[0].Get("text").String() != "executing pwd" {
		t.Fatalf("expected thought part with 'executing pwd', got: %s", modelParts[0].Raw)
	}
	if modelParts[1].Get("functionCall.name").String() != "functions__exec" {
		t.Fatalf("expected functionCall name 'functions__exec', got: %s", modelParts[1].Raw)
	}
	if modelParts[1].Get("thoughtSignature").String() != testResponsesGeminiThoughtSignature {
		t.Fatalf("expected thoughtSignature on functionCall, got: %s", modelParts[1].Raw)
	}

	userRespParts := contents[2].Get("parts").Array()
	if len(userRespParts) != 1 {
		t.Fatalf("expected 1 part in user tool response, got %d; raw: %s", len(userRespParts), contents[2].Raw)
	}
	if userRespParts[0].Get("functionResponse.name").String() != "functions__exec" {
		t.Fatalf("expected functionResponse name 'functions__exec', got: %s", userRespParts[0].Raw)
	}
	if userRespParts[0].Get("functionResponse.response.result").String() != "/workspace" {
		t.Fatalf("expected functionResponse result '/workspace', got: %s", userRespParts[0].Raw)
	}
}
