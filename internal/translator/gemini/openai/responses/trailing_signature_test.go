package responses

import (
	"context"
	"strings"
	"testing"

	homekv "github.com/router-for-me/CLIProxyAPI/v7/internal/home"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func TestGeminiResponsesLateSignaturePreservesSummary(t *testing.T) {
	chunks := []string{
		`data: {"candidates":[{"content":{"parts":[{"text":"Let me think.","thought":true}]}}],"responseId":"issue-5513"}`,
		`data: {"candidates":[{"content":{"parts":[{"text":"The answer is 42."}]}}]}`,
		`data: {"candidates":[{"content":{"parts":[{"text":"","thoughtSignature":"` + testResponsesGeminiThoughtSignature + `"}]},"finishReason":"STOP"}]}`,
	}
	var state any
	var completed gjson.Result
	for _, chunk := range chunks {
		for _, event := range ConvertGeminiResponseToOpenAIResponses(context.Background(), "gemini-3.8-flash-high", nil, nil, []byte(chunk), &state) {
			name, data := parseSSEEvent(t, event)
			if (name == "response.output_item.added" || name == "response.output_item.done") && data.Get("item.type").String() == "reasoning" && len(data.Get("item.summary").Array()) == 0 && data.Get("item.encrypted_content").String() != "" {
				t.Fatalf("late signature emitted an empty reasoning item: %s", data.Raw)
			}
			if name == "response.completed" {
				completed = data.Get("response.output")
			}
		}
	}
	if len(completed.Array()) != 2 || completed.Get("0.summary.0.text").String() != "Let me think." || completed.Get("1.type").String() != "message" {
		t.Fatalf("unexpected completed output: %s", completed.Raw)
	}
	replayed := ConvertOpenAIResponsesRequestToGemini("gemini-3.8-flash-high", []byte(`{"input":`+completed.Raw+`}`), false)
	if gjson.GetBytes(replayed, "contents.0.parts.1.thoughtSignature").String() != testResponsesGeminiThoughtSignature {
		t.Fatalf("late text signature was not restored: %s", replayed)
	}
}

func TestGeminiResponsesCachedTextSignatureReplayBoundaries(t *testing.T) {
	const model = "gemini-3.8-flash-high"
	const messageID = "msg_resp_issue-5513-boundaries_0"
	const text = "An exact signed answer."
	if !cacheGeminiResponsesTextSignatures(model, messageID, text, []string{testResponsesGeminiThoughtSignature}) {
		t.Fatal("could not seed text signature replay cache")
	}
	message := `{"id":"` + messageID + `","type":"message","role":"assistant","content":[{"type":"output_text","text":"` + text + `"}]}`
	for _, testCase := range []struct {
		name          string
		model         string
		message       string
		wantSignature bool
	}{
		{name: "exact", model: model, message: message, wantSignature: true},
		{name: "different model", model: "gemini-other", message: message},
		{name: "edited text", model: model, message: strings.ReplaceAll(message, text, "Edited answer.")},
		{name: "different ID", model: model, message: strings.ReplaceAll(message, messageID, messageID+"-other")},
		{name: "no ID", model: model, message: strings.ReplaceAll(message, `"id":"`+messageID+`",`, "")},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			request := []byte(`{"input":[` + testCase.message + `]}`)
			output := ConvertOpenAIResponsesRequestToGemini(testCase.model, request, false)
			got := strings.Contains(string(output), testResponsesGeminiThoughtSignature)
			if got != testCase.wantSignature {
				t.Fatalf("signature restored=%v, want %v: %s", got, testCase.wantSignature, output)
			}
		})
	}
	carrier := []byte(`{"type":"reasoning","summary":[]}`)
	carrier, _ = sjson.SetBytes(carrier, "encrypted_content", encodeGeminiResponsesCarrier(testResponsesGeminiThoughtSignature, geminiResponsesCarrierPrevious, geminiResponsesCarrierText))
	request := []byte(`{"input":[` + message + `,` + string(carrier) + `]}`)
	output := ConvertOpenAIResponsesRequestToGemini(model, request, false)
	if strings.Count(string(output), testResponsesGeminiThoughtSignature) != 1 {
		t.Fatalf("explicit carrier duplicated cached signature: %s", output)
	}
}

func TestGeminiResponsesTextSignatureCacheRejectsInvalidSignature(t *testing.T) {
	if cacheGeminiResponsesTextSignatures("gemini-test", "msg_resp_invalid_0", "answer", []string{"invalid"}) {
		t.Fatal("invalid signature was accepted into replay cache")
	}
	var state any
	line := []byte(`data: {"candidates":[{"content":{"parts":[{"text":"answer"},{"text":"","thoughtSignature":"invalid"}]},"finishReason":"STOP"}],"responseId":"invalid-cache-fallback"}`)
	var completed gjson.Result
	for _, chunk := range ConvertGeminiResponseToOpenAIResponses(context.Background(), "gemini-test", nil, nil, line, &state) {
		name, data := parseSSEEvent(t, chunk)
		if name == "response.completed" {
			completed = data.Get("response.output")
		}
	}
	if len(completed.Array()) != 2 || completed.Get("1.type").String() != "reasoning" {
		t.Fatalf("uncacheable signature lost its fallback carrier: %s", completed.Raw)
	}
}

func TestGeminiResponsesLateSignatureReplayWithThinkingSuffix(t *testing.T) {
	var state any
	line := []byte(`data: {"candidates":[{"content":{"parts":[{"text":"suffix answer"},{"text":"","thoughtSignature":"` + testResponsesGeminiThoughtSignature + `"}]},"finishReason":"STOP"}],"responseId":"issue-5513-suffix"}`)
	var completed gjson.Result
	for _, chunk := range ConvertGeminiResponseToOpenAIResponses(context.Background(), "gemini-2.5-pro(8192)", nil, nil, line, &state) {
		name, data := parseSSEEvent(t, chunk)
		if name == "response.completed" {
			completed = data.Get("response.output")
		}
	}
	output := ConvertOpenAIResponsesRequestToGemini("gemini-2.5-pro", []byte(`{"input":`+completed.Raw+`}`), false)
	if gjson.GetBytes(output, "contents.0.parts.0.thoughtSignature").String() != testResponsesGeminiThoughtSignature {
		t.Fatalf("executor base model could not restore suffixed response signature: %s", output)
	}
}

func TestGeminiResponsesCachedSignaturesMergeExplicitPrefixInOrder(t *testing.T) {
	const model = "gemini-3.8-flash-high"
	const messageID = "msg_resp_issue-5513-merge_0"
	signature2 := differentResponsesGeminiThoughtSignature(t)
	if !cacheGeminiResponsesTextSignatures(model, messageID, "answer", []string{testResponsesGeminiThoughtSignature, signature2}) {
		t.Fatal("could not seed replay cache")
	}
	for _, explicitSignature := range []string{testResponsesGeminiThoughtSignature, signature2} {
		carrier := []byte(`{"type":"reasoning","summary":[]}`)
		carrier, _ = sjson.SetBytes(carrier, "encrypted_content", encodeGeminiResponsesCarrier(explicitSignature, geminiResponsesCarrierPrevious, geminiResponsesCarrierText))
		request := []byte(`{"input":[{"id":"` + messageID + `","type":"message","role":"assistant","content":[{"type":"output_text","text":"answer"}]},` + string(carrier) + `]}`)
		output := ConvertOpenAIResponsesRequestToGemini(model, request, false)
		parts := gjson.GetBytes(output, "contents.0.parts").Array()
		if len(parts) != 2 || parts[0].Get("text").String() != "answer" || parts[0].Get("thoughtSignature").String() != testResponsesGeminiThoughtSignature || parts[1].Get("thoughtSignature").String() != signature2 {
			t.Fatalf("partial explicit carrier changed cached signature order: %s", output)
		}
	}
}

func TestGeminiResponsesLateThoughtSignatureDoesNotBindEarlierMessage(t *testing.T) {
	signature2 := differentResponsesGeminiThoughtSignature(t)
	lines := []string{
		`data: {"candidates":[{"content":{"parts":[{"text":"earlier answer"}]}}],"responseId":"issue-5513-thought-boundary"}`,
		`data: {"candidates":[{"content":{"parts":[{"text":"later thought","thought":true,"thoughtSignature":"` + testResponsesGeminiThoughtSignature + `"}]}}]}`,
		`data: {"candidates":[{"content":{"parts":[{"text":"","thoughtSignature":"` + signature2 + `"}]},"finishReason":"STOP"}]}`,
	}
	var state any
	var completed gjson.Result
	for _, line := range lines {
		for _, chunk := range ConvertGeminiResponseToOpenAIResponses(context.Background(), "gemini-3.8-flash-high", nil, nil, []byte(line), &state) {
			name, data := parseSSEEvent(t, chunk)
			if name == "response.completed" {
				completed = data.Get("response.output")
			}
		}
	}
	output := ConvertOpenAIResponsesRequestToGemini("gemini-3.8-flash-high", []byte(`{"input":`+completed.Raw+`}`), false)
	parts := gjson.GetBytes(output, "contents.0.parts").Array()
	if len(parts) < 2 || parts[0].Get("thoughtSignature").String() != "" || parts[1].Get("thoughtSignature").String() != testResponsesGeminiThoughtSignature || !strings.Contains(string(output), signature2) {
		t.Fatalf("late thought signature rebound to the earlier message: %s", output)
	}
}

func TestGeminiResponsesCacheRecoveryPreservesFallbackSignatureOrder(t *testing.T) {
	previousHome := homekv.Current()
	t.Cleanup(func() { homekv.SetCurrent(previousHome) })
	signature2 := differentResponsesGeminiThoughtSignature(t)
	lines := []string{
		`data: {"candidates":[{"content":{"parts":[{"text":"recovery answer"}]}}],"responseId":"issue-5513-cache-recovery"}`,
		`data: {"candidates":[{"content":{"parts":[{"text":"","thoughtSignature":"` + testResponsesGeminiThoughtSignature + `"}]}}]}`,
		`data: {"candidates":[{"content":{"parts":[{"text":"","thoughtSignature":"` + signature2 + `"}]},"finishReason":"STOP"}]}`,
	}
	var state any
	var completed gjson.Result
	for index, line := range lines {
		// A disabled Home client rejects the first cache write. Returning to
		// local storage makes the next write succeed without timers or mocks.
		if index == 1 {
			homekv.SetCurrent(&homekv.Client{})
		} else {
			homekv.SetCurrent(nil)
		}
		for _, chunk := range ConvertGeminiResponseToOpenAIResponses(context.Background(), "gemini-3.8-flash-high", nil, nil, []byte(line), &state) {
			name, data := parseSSEEvent(t, chunk)
			if name == "response.completed" {
				completed = data.Get("response.output")
			}
		}
	}
	if len(completed.Array()) != 2 || decodedResponsesCarrierSignature(t, completed.Get("1.encrypted_content").String()) != testResponsesGeminiThoughtSignature {
		t.Fatalf("expected only the first write's fallback carrier: %s", completed.Raw)
	}
	output := ConvertOpenAIResponsesRequestToGemini("gemini-3.8-flash-high", []byte(`{"input":`+completed.Raw+`}`), false)
	parts := gjson.GetBytes(output, "contents.0.parts").Array()
	if len(parts) != 2 || parts[0].Get("thoughtSignature").String() != testResponsesGeminiThoughtSignature || parts[1].Get("thoughtSignature").String() != signature2 {
		t.Fatalf("cache recovery changed fallback signature order: %s", output)
	}
}
