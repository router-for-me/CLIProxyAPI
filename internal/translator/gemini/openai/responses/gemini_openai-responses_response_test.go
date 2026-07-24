package responses

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func parseSSEEvent(t *testing.T, chunk []byte) (string, gjson.Result) {
	t.Helper()

	lines := strings.Split(string(chunk), "\n")
	if len(lines) < 2 {
		t.Fatalf("unexpected SSE chunk: %q", chunk)
	}

	event := strings.TrimSpace(strings.TrimPrefix(lines[0], "event:"))
	dataLine := strings.TrimSpace(strings.TrimPrefix(lines[1], "data:"))
	if !gjson.Valid(dataLine) {
		t.Fatalf("invalid SSE data JSON: %q", dataLine)
	}
	return event, gjson.Parse(dataLine)
}

func TestConvertGeminiResponseToOpenAIResponses_UnwrapAndAggregateText(t *testing.T) {
	// Vertex-style Gemini stream wraps the actual response payload under "response".
	// This test ensures we unwrap and that output_text.done contains the full text.
	in := []string{
		`data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"text":""}]}}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2,"cachedContentTokenCount":0},"modelVersion":"test-model","responseId":"req_vrtx_1"},"traceId":"t1"}`,
		`data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"让"}]}}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2,"cachedContentTokenCount":0},"modelVersion":"test-model","responseId":"req_vrtx_1"},"traceId":"t1"}`,
		`data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"我先"}]}}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2,"cachedContentTokenCount":0},"modelVersion":"test-model","responseId":"req_vrtx_1"},"traceId":"t1"}`,
		`data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"了解"}]}}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2,"cachedContentTokenCount":0},"modelVersion":"test-model","responseId":"req_vrtx_1"},"traceId":"t1"}`,
		`data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"mcp__serena__list_dir","args":{"recursive":false,"relative_path":"internal"},"id":"toolu_1"}}]}}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2,"cachedContentTokenCount":0},"modelVersion":"test-model","responseId":"req_vrtx_1"},"traceId":"t1"}`,
		`data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"text":""}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5,"totalTokenCount":15,"cachedContentTokenCount":2},"modelVersion":"test-model","responseId":"req_vrtx_1"},"traceId":"t1"}`,
	}

	originalReq := []byte(`{"instructions":"test instructions","model":"gpt-5","max_output_tokens":123}`)

	var param any
	var out [][]byte
	for _, line := range in {
		out = append(out, ConvertGeminiResponseToOpenAIResponses(context.Background(), "test-model", originalReq, nil, []byte(line), &param)...)
	}

	var (
		gotTextDone     bool
		gotMessageDone  bool
		gotResponseDone bool
		gotFuncDone     bool

		textDone     string
		messageText  string
		responseID   string
		instructions string
		cachedTokens int64

		funcName string
		funcArgs string

		posTextDone    = -1
		posPartDone    = -1
		posMessageDone = -1
		posFuncAdded   = -1
	)

	for i, chunk := range out {
		ev, data := parseSSEEvent(t, chunk)
		switch ev {
		case "response.output_text.done":
			gotTextDone = true
			if posTextDone == -1 {
				posTextDone = i
			}
			textDone = data.Get("text").String()
		case "response.content_part.done":
			if posPartDone == -1 {
				posPartDone = i
			}
		case "response.output_item.done":
			switch data.Get("item.type").String() {
			case "message":
				gotMessageDone = true
				if posMessageDone == -1 {
					posMessageDone = i
				}
				messageText = data.Get("item.content.0.text").String()
			case "function_call":
				gotFuncDone = true
				funcName = data.Get("item.name").String()
				funcArgs = data.Get("item.arguments").String()
			}
		case "response.output_item.added":
			if data.Get("item.type").String() == "function_call" && posFuncAdded == -1 {
				posFuncAdded = i
			}
		case "response.completed":
			gotResponseDone = true
			responseID = data.Get("response.id").String()
			instructions = data.Get("response.instructions").String()
			cachedTokens = data.Get("response.usage.input_tokens_details.cached_tokens").Int()
		}
	}

	if !gotTextDone {
		t.Fatalf("missing response.output_text.done event")
	}
	if posTextDone == -1 || posPartDone == -1 || posMessageDone == -1 || posFuncAdded == -1 {
		t.Fatalf("missing ordering events: textDone=%d partDone=%d messageDone=%d funcAdded=%d", posTextDone, posPartDone, posMessageDone, posFuncAdded)
	}
	if !(posTextDone < posPartDone && posPartDone < posMessageDone && posMessageDone < posFuncAdded) {
		t.Fatalf("unexpected message/function ordering: textDone=%d partDone=%d messageDone=%d funcAdded=%d", posTextDone, posPartDone, posMessageDone, posFuncAdded)
	}
	if !gotMessageDone {
		t.Fatalf("missing message response.output_item.done event")
	}
	if !gotFuncDone {
		t.Fatalf("missing function_call response.output_item.done event")
	}
	if !gotResponseDone {
		t.Fatalf("missing response.completed event")
	}

	if textDone != "让我先了解" {
		t.Fatalf("unexpected output_text.done text: got %q", textDone)
	}
	if messageText != "让我先了解" {
		t.Fatalf("unexpected message done text: got %q", messageText)
	}

	if responseID != "resp_req_vrtx_1" {
		t.Fatalf("unexpected response id: got %q", responseID)
	}
	if instructions != "test instructions" {
		t.Fatalf("unexpected instructions echo: got %q", instructions)
	}
	if cachedTokens != 2 {
		t.Fatalf("unexpected cached token count: got %d", cachedTokens)
	}

	if funcName != "mcp__serena__list_dir" {
		t.Fatalf("unexpected function name: got %q", funcName)
	}
	if !gjson.Valid(funcArgs) {
		t.Fatalf("invalid function arguments JSON: %q", funcArgs)
	}
	if gjson.Get(funcArgs, "recursive").Bool() != false {
		t.Fatalf("unexpected recursive arg: %v", gjson.Get(funcArgs, "recursive").Value())
	}
	if gjson.Get(funcArgs, "relative_path").String() != "internal" {
		t.Fatalf("unexpected relative_path arg: %q", gjson.Get(funcArgs, "relative_path").String())
	}
}

func differentResponsesGeminiThoughtSignature(t *testing.T) string {
	t.Helper()
	raw, errDecode := base64.StdEncoding.DecodeString(testResponsesGeminiThoughtSignature)
	if errDecode != nil {
		t.Fatal(errDecode)
	}
	raw[len(raw)-1] ^= 1
	return base64.StdEncoding.EncodeToString(raw)
}

func decodedResponsesCarrierSignature(t *testing.T, encryptedContent string) string {
	t.Helper()
	signature, _, _, marked, ok := decodeGeminiResponsesCarrier(encryptedContent)
	if marked && !ok {
		t.Fatalf("invalid Responses carrier envelope: %q", encryptedContent)
	}
	return signature
}

func TestConvertGeminiResponseToOpenAIResponses_ConsecutiveSignedVisibleTextPreservesEverySignature(t *testing.T) {
	signature2 := differentResponsesGeminiThoughtSignature(t)
	in := []string{
		`data: {"response":{"candidates":[{"content":{"parts":[{"text":"a"}]}}],"modelVersion":"gemini-3.6-flash","responseId":"signed-text"}}`,
		`data: {"response":{"candidates":[{"content":{"parts":[{"text":"b","thoughtSignature":"` + testResponsesGeminiThoughtSignature + `"}]}}],"modelVersion":"gemini-3.6-flash","responseId":"signed-text"}}`,
		`data: {"response":{"candidates":[{"content":{"parts":[{"text":"c","thoughtSignature":"` + signature2 + `"}]},"finishReason":"STOP"}],"modelVersion":"gemini-3.6-flash","responseId":"signed-text"}}`,
	}
	var param any
	added := make(map[string]string)
	done := make(map[string]string)
	var completed gjson.Result
	for _, line := range in {
		for _, chunk := range ConvertGeminiResponseToOpenAIResponses(context.Background(), "gemini-3.6-flash-high", nil, nil, []byte(line), &param) {
			event, data := parseSSEEvent(t, chunk)
			if data.Get("item.type").String() == "reasoning" {
				switch event {
				case "response.output_item.added":
					added[data.Get("item.id").String()] = data.Get("item.encrypted_content").String()
				case "response.output_item.done":
					done[data.Get("item.id").String()] = data.Get("item.encrypted_content").String()
				}
			}
			if event == "response.completed" {
				completed = data.Get("response.output")
			}
		}
	}
	if len(added) != 2 || len(done) != 2 {
		t.Fatalf("reasoning items added/done = %d/%d, want 2/2", len(added), len(done))
	}
	for id, signature := range added {
		if done[id] != signature {
			t.Fatalf("reasoning item %s changed signature from %q to %q", id, signature, done[id])
		}
	}
	seen := map[string]bool{}
	completed.ForEach(func(_, item gjson.Result) bool {
		if item.Get("type").String() == "reasoning" {
			seen[decodedResponsesCarrierSignature(t, item.Get("encrypted_content").String())] = true
		}
		return true
	})
	if !seen[testResponsesGeminiThoughtSignature] || !seen[signature2] {
		t.Fatalf("completed signatures = %v, want both", seen)
	}

	request := []byte(`{"model":"gemini-3.6-flash-high","input":[]}`)
	request, _ = sjson.SetRawBytes(request, "input", []byte(completed.Raw))
	translated := ConvertOpenAIResponsesRequestToGemini("gemini-3.6-flash-high", request, false)
	var visibleParts []gjson.Result
	for _, part := range gjson.GetBytes(translated, "contents.0.parts").Array() {
		if !part.Get("thought").Bool() && part.Get("text").String() != "" {
			visibleParts = append(visibleParts, part)
		}
	}
	if len(visibleParts) != 2 || visibleParts[0].Get("text").String() != "ab" || visibleParts[0].Get("thoughtSignature").String() != testResponsesGeminiThoughtSignature || visibleParts[1].Get("text").String() != "c" || visibleParts[1].Get("thoughtSignature").String() != signature2 {
		t.Fatalf("signed visible text did not round-trip by segment: %s", translated)
	}
}

func TestConvertGeminiResponseToOpenAIResponsesNonStream_ConsecutiveSignedVisibleTextPreservesEverySignature(t *testing.T) {
	signature2 := differentResponsesGeminiThoughtSignature(t)
	raw := []byte(`{"candidates":[{"content":{"parts":[{"text":"a"},{"text":"b","thoughtSignature":"` + testResponsesGeminiThoughtSignature + `"},{"text":"c","thoughtSignature":"` + signature2 + `"}]},"finishReason":"STOP"}],"responseId":"signed-text-nonstream"}`)
	out := ConvertGeminiResponseToOpenAIResponsesNonStream(context.Background(), "gemini-3.6-flash-high", nil, nil, raw, nil)
	output := gjson.GetBytes(out, "output")
	if decodedResponsesCarrierSignature(t, output.Get("0.encrypted_content").String()) != testResponsesGeminiThoughtSignature || output.Get("1.content.0.text").String() != "ab" || decodedResponsesCarrierSignature(t, output.Get("2.encrypted_content").String()) != signature2 || output.Get("3.content.0.text").String() != "c" {
		t.Fatalf("non-stream signed visible text was not segmented: %s", out)
	}

	outputWithoutIDs := []byte(output.Raw)
	outputWithoutIDs, _ = sjson.DeleteBytes(outputWithoutIDs, "0.id")
	outputWithoutIDs, _ = sjson.DeleteBytes(outputWithoutIDs, "2.id")
	request := []byte(`{"model":"gemini-3.6-flash-high","input":[]}`)
	request, _ = sjson.SetRawBytes(request, "input", outputWithoutIDs)
	translated := ConvertOpenAIResponsesRequestToGemini("gemini-3.6-flash-high", request, false)
	var visibleParts []gjson.Result
	for _, part := range gjson.GetBytes(translated, "contents.0.parts").Array() {
		if !part.Get("thought").Bool() && part.Get("text").String() != "" {
			visibleParts = append(visibleParts, part)
		}
	}
	if len(visibleParts) != 2 || visibleParts[0].Get("thoughtSignature").String() != testResponsesGeminiThoughtSignature || visibleParts[1].Get("thoughtSignature").String() != signature2 {
		t.Fatalf("non-stream signatures did not round-trip after client stripped reasoning IDs: %s", translated)
	}
}

func TestConvertGeminiResponseToOpenAIResponses_SignedVisibleThenUnsignedPreservesBoundary(t *testing.T) {
	lines := []string{
		`data: {"response":{"candidates":[{"content":{"parts":[{"text":"signed","thoughtSignature":"` + testResponsesGeminiThoughtSignature + `"}]}}],"responseId":"signed-then-unsigned"}}`,
		`data: {"response":{"candidates":[{"content":{"parts":[{"text":"unsigned"}]},"finishReason":"STOP"}],"responseId":"signed-then-unsigned"}}`,
	}
	var param any
	var completed gjson.Result
	for _, line := range lines {
		for _, chunk := range ConvertGeminiResponseToOpenAIResponses(context.Background(), "gemini-3.6-flash-high", nil, nil, []byte(line), &param) {
			event, data := parseSSEEvent(t, chunk)
			if event == "response.completed" {
				completed = data.Get("response.output")
			}
		}
	}
	request := []byte(`{"model":"gemini-3.6-flash-high","input":[]}`)
	request, _ = sjson.SetRawBytes(request, "input", []byte(completed.Raw))
	translated := ConvertOpenAIResponsesRequestToGemini("gemini-3.6-flash-high", request, false)
	parts := gjson.GetBytes(translated, "contents.0.parts").Array()
	if len(parts) != 2 || parts[0].Get("text").String() != "signed" || parts[0].Get("thoughtSignature").String() != testResponsesGeminiThoughtSignature || parts[1].Get("text").String() != "unsigned" || parts[1].Get("thoughtSignature").String() != "" {
		t.Fatalf("signed/unsigned visible boundary changed: output=%s translated=%s", completed.Raw, translated)
	}
	if !strings.Contains(completed.Raw, geminiResponsesCarrierPrefix) || strings.Contains(string(translated), geminiResponsesCarrierPrefix) {
		t.Fatalf("Responses carrier must exist only on the client-facing wire: output=%s translated=%s", completed.Raw, translated)
	}
}

func TestConvertGeminiResponseToOpenAIResponses_LeadingCarrierDoesNotCrossSignedThought(t *testing.T) {
	signature2 := differentResponsesGeminiThoughtSignature(t)
	lines := []string{
		`data: {"response":{"candidates":[{"content":{"parts":[{"text":"","thoughtSignature":"` + testResponsesGeminiThoughtSignature + `"}]}}],"responseId":"leading-before-signed-thought"}}`,
		`data: {"response":{"candidates":[{"content":{"parts":[{"text":"reason","thought":true,"thoughtSignature":"` + signature2 + `"}]}}],"responseId":"leading-before-signed-thought"}}`,
		`data: {"response":{"candidates":[{"content":{"parts":[{"text":"answer"}]},"finishReason":"STOP"}],"responseId":"leading-before-signed-thought"}}`,
	}
	var param any
	var streamOutput gjson.Result
	for _, line := range lines {
		for _, chunk := range ConvertGeminiResponseToOpenAIResponses(context.Background(), "gemini-3.6-flash-high", nil, nil, []byte(line), &param) {
			event, data := parseSSEEvent(t, chunk)
			if event == "response.completed" {
				streamOutput = data.Get("response.output")
			}
		}
	}
	raw := []byte(`{"candidates":[{"content":{"parts":[{"text":"","thoughtSignature":"` + testResponsesGeminiThoughtSignature + `"},{"text":"reason","thought":true,"thoughtSignature":"` + signature2 + `"},{"text":"answer"}]},"finishReason":"STOP"}],"responseId":"leading-before-signed-thought-nonstream"}`)
	nonStream := ConvertGeminiResponseToOpenAIResponsesNonStream(context.Background(), "gemini-3.6-flash-high", nil, nil, raw, nil)

	for name, output := range map[string]gjson.Result{"stream": streamOutput, "non-stream": gjson.GetBytes(nonStream, "output")} {
		request := []byte(`{"model":"gemini-3.6-flash-high","input":[]}`)
		request, _ = sjson.SetRawBytes(request, "input", []byte(output.Raw))
		translated := ConvertOpenAIResponsesRequestToGemini("gemini-3.6-flash-high", request, false)
		parts := gjson.GetBytes(translated, "contents.0.parts").Array()
		if len(parts) != 3 || !parts[0].Get("text").Exists() || parts[0].Get("text").String() != "" || parts[0].Get("thoughtSignature").String() != testResponsesGeminiThoughtSignature || parts[1].Get("text").String() != "reason" || !parts[1].Get("thought").Bool() || parts[1].Get("thoughtSignature").String() != signature2 || parts[2].Get("text").String() != "answer" || parts[2].Get("thoughtSignature").String() != "" {
			t.Fatalf("%s leading carrier crossed signed thought: output=%s translated=%s", name, output.Raw, translated)
		}
	}
}

func TestConvertGeminiResponseToOpenAIResponsesNonStream_SignedVisibleThenUnsignedPreservesBoundary(t *testing.T) {
	raw := []byte(`{"candidates":[{"content":{"parts":[{"text":"signed","thoughtSignature":"` + testResponsesGeminiThoughtSignature + `"},{"text":"unsigned"}]},"finishReason":"STOP"}],"responseId":"signed-then-unsigned-nonstream"}`)
	out := ConvertGeminiResponseToOpenAIResponsesNonStream(context.Background(), "gemini-3.6-flash-high", nil, nil, raw, nil)
	request := []byte(`{"model":"gemini-3.6-flash-high","input":[]}`)
	request, _ = sjson.SetRawBytes(request, "input", []byte(gjson.GetBytes(out, "output").Raw))
	translated := ConvertOpenAIResponsesRequestToGemini("gemini-3.6-flash-high", request, false)
	parts := gjson.GetBytes(translated, "contents.0.parts").Array()
	if len(parts) != 2 || parts[0].Get("text").String() != "signed" || parts[0].Get("thoughtSignature").String() != testResponsesGeminiThoughtSignature || parts[1].Get("text").String() != "unsigned" || parts[1].Get("thoughtSignature").String() != "" {
		t.Fatalf("non-stream signed/unsigned visible boundary changed: output=%s translated=%s", gjson.GetBytes(out, "output").Raw, translated)
	}
}

func TestConvertGeminiResponseToOpenAIResponsesNonStream_TrailingCarrierDirectionDoesNotDependOnID(t *testing.T) {
	signature2 := differentResponsesGeminiThoughtSignature(t)
	raw := []byte(`{"candidates":[{"content":{"parts":[{"text":"answer","thoughtSignature":"` + testResponsesGeminiThoughtSignature + `"},{"text":"","thoughtSignature":"` + signature2 + `"}]},"finishReason":"STOP"}],"responseId":"trailing-direction-nonstream"}`)
	out := ConvertGeminiResponseToOpenAIResponsesNonStream(context.Background(), "gemini-3.6-flash-high", nil, nil, raw, nil)
	request := []byte(`{"model":"gemini-3.6-flash-high","input":[]}`)
	request, _ = sjson.SetRawBytes(request, "input", []byte(gjson.GetBytes(out, "output").Raw))
	translated := ConvertOpenAIResponsesRequestToGemini("gemini-3.6-flash-high", request, false)
	parts := gjson.GetBytes(translated, "contents.0.parts").Array()
	if len(parts) != 2 || parts[0].Get("text").String() != "answer" || parts[0].Get("thoughtSignature").String() != testResponsesGeminiThoughtSignature || !parts[1].Get("text").Exists() || parts[1].Get("text").String() != "" || parts[1].Get("thoughtSignature").String() != signature2 {
		t.Fatalf("non-stream trailing carrier changed direction: output=%s translated=%s", gjson.GetBytes(out, "output").Raw, translated)
	}
}

func TestConvertGeminiResponseToOpenAIResponses_TrailingCarrierDirectionSurvivesStrippedIDs(t *testing.T) {
	signature2 := differentResponsesGeminiThoughtSignature(t)
	lines := []string{
		`data: {"response":{"candidates":[{"content":{"parts":[{"text":"answer","thoughtSignature":"` + testResponsesGeminiThoughtSignature + `"}]}}],"responseId":"trailing-direction-stream"}}`,
		`data: {"response":{"candidates":[{"content":{"parts":[{"text":"","thoughtSignature":"` + signature2 + `"}]},"finishReason":"STOP"}],"responseId":"trailing-direction-stream"}}`,
	}
	var param any
	var completed gjson.Result
	for _, line := range lines {
		for _, chunk := range ConvertGeminiResponseToOpenAIResponses(context.Background(), "gemini-3.6-flash-high", nil, nil, []byte(line), &param) {
			event, data := parseSSEEvent(t, chunk)
			if event == "response.completed" {
				completed = data.Get("response.output")
			}
		}
	}
	withoutIDs := []byte(completed.Raw)
	withoutIDs, _ = sjson.DeleteBytes(withoutIDs, "1.id")
	withoutIDs, _ = sjson.DeleteBytes(withoutIDs, "2.id")
	request := []byte(`{"model":"gemini-3.6-flash-high","input":[]}`)
	request, _ = sjson.SetRawBytes(request, "input", withoutIDs)
	translated := ConvertOpenAIResponsesRequestToGemini("gemini-3.6-flash-high", request, false)
	parts := gjson.GetBytes(translated, "contents.0.parts").Array()
	if len(parts) != 2 || parts[0].Get("text").String() != "answer" || parts[0].Get("thoughtSignature").String() != testResponsesGeminiThoughtSignature || !parts[1].Get("text").Exists() || parts[1].Get("text").String() != "" || parts[1].Get("thoughtSignature").String() != signature2 {
		t.Fatalf("ID-stripped trailing carrier changed direction: output=%s translated=%s", completed.Raw, translated)
	}
	if !strings.Contains(completed.Raw, geminiResponsesCarrierPrefix) || strings.Contains(string(translated), geminiResponsesCarrierPrefix) {
		t.Fatalf("ID-stripped Responses carrier leaked across protocol boundary: output=%s translated=%s", completed.Raw, translated)
	}
}

func TestConvertGeminiResponseToOpenAIResponses_VisibleSignatureDoesNotOverwriteSignedThought(t *testing.T) {
	signature2 := differentResponsesGeminiThoughtSignature(t)
	in := []string{
		`data: {"response":{"candidates":[{"content":{"parts":[{"text":"one","thought":true,"thoughtSignature":"` + testResponsesGeminiThoughtSignature + `"}]}}],"responseId":"signed-thought-visible"}}`,
		`data: {"response":{"candidates":[{"content":{"parts":[{"text":"answer","thoughtSignature":"` + signature2 + `"}]},"finishReason":"STOP"}],"responseId":"signed-thought-visible"}}`,
	}
	var param any
	added := make(map[string]string)
	done := make(map[string]string)
	var completed gjson.Result
	for _, line := range in {
		for _, chunk := range ConvertGeminiResponseToOpenAIResponses(context.Background(), "gemini-3.6-flash-high", nil, nil, []byte(line), &param) {
			event, data := parseSSEEvent(t, chunk)
			if data.Get("item.type").String() == "reasoning" {
				switch event {
				case "response.output_item.added":
					added[data.Get("item.id").String()] = data.Get("item.encrypted_content").String()
				case "response.output_item.done":
					done[data.Get("item.id").String()] = data.Get("item.encrypted_content").String()
				}
			}
			if event == "response.completed" {
				completed = data.Get("response.output")
			}
		}
	}
	for id, signature := range added {
		if done[id] != signature {
			t.Fatalf("reasoning item %s changed signature from %q to %q", id, signature, done[id])
		}
	}
	if decodedResponsesCarrierSignature(t, completed.Get("0.encrypted_content").String()) != testResponsesGeminiThoughtSignature || decodedResponsesCarrierSignature(t, completed.Get("2.encrypted_content").String()) != signature2 {
		t.Fatalf("thought/visible signatures were not both preserved: %s", completed.Raw)
	}
	request := []byte(`{"model":"gemini-3.6-flash-high","input":[]}`)
	request, _ = sjson.SetRawBytes(request, "input", []byte(completed.Raw))
	translated := ConvertOpenAIResponsesRequestToGemini("gemini-3.6-flash-high", request, false)
	var signatures []string
	visibleSignature := ""
	for _, part := range gjson.GetBytes(translated, "contents.0.parts").Array() {
		if signature := part.Get("thoughtSignature").String(); signature != "" {
			signatures = append(signatures, signature)
			if part.Get("text").String() == "answer" {
				visibleSignature = signature
			}
		}
	}
	if len(signatures) != 2 || visibleSignature != signature2 {
		t.Fatalf("thought/visible signatures did not round-trip: signatures=%v visible=%q translated=%s", signatures, visibleSignature, translated)
	}
}

func TestConvertGeminiResponseToOpenAIResponses_FlushesVisibleSignatureBeforeLaterThought(t *testing.T) {
	signature2 := differentResponsesGeminiThoughtSignature(t)
	const signature3 = "third-distinct-gemini-signature-123456"
	in := []string{
		`data: {"response":{"candidates":[{"content":{"parts":[{"text":"thought-a","thought":true,"thoughtSignature":"` + testResponsesGeminiThoughtSignature + `"}]}}],"responseId":"visible-before-thought"}}`,
		`data: {"response":{"candidates":[{"content":{"parts":[{"text":"answer","thoughtSignature":"` + signature2 + `"}]}}],"responseId":"visible-before-thought"}}`,
		`data: {"response":{"candidates":[{"content":{"parts":[{"text":"thought-c","thought":true,"thoughtSignature":"` + signature3 + `"}]},"finishReason":"STOP"}],"responseId":"visible-before-thought"}}`,
	}
	var param any
	var completed gjson.Result
	for _, line := range in {
		for _, chunk := range ConvertGeminiResponseToOpenAIResponses(context.Background(), "gemini-3.6-flash-high", nil, nil, []byte(line), &param) {
			event, data := parseSSEEvent(t, chunk)
			if event == "response.completed" {
				completed = data.Get("response.output")
			}
		}
	}
	if decodedResponsesCarrierSignature(t, completed.Get("0.encrypted_content").String()) != testResponsesGeminiThoughtSignature || completed.Get("1.type").String() != "message" || decodedResponsesCarrierSignature(t, completed.Get("2.encrypted_content").String()) != signature2 || decodedResponsesCarrierSignature(t, completed.Get("3.encrypted_content").String()) != signature3 {
		t.Fatalf("visible signature crossed later thought: %s", completed.Raw)
	}
}

func TestConvertGeminiResponseToOpenAIResponses_FunctionAndTrailingSignaturesRoundTrip(t *testing.T) {
	signature2 := differentResponsesGeminiThoughtSignature(t)
	in := []string{
		`data: {"response":{"candidates":[{"content":{"parts":[{"thoughtSignature":"` + testResponsesGeminiThoughtSignature + `","functionCall":{"name":"run_command","args":{"command":"true"}}}]}}],"responseId":"function-trailing"}}`,
		`data: {"response":{"candidates":[{"content":{"parts":[{"text":"","thoughtSignature":"` + signature2 + `"}]},"finishReason":"STOP"}],"responseId":"function-trailing"}}`,
	}
	var param any
	var completed gjson.Result
	for _, line := range in {
		for _, chunk := range ConvertGeminiResponseToOpenAIResponses(context.Background(), "gemini-3.6-flash-high", nil, nil, []byte(line), &param) {
			event, data := parseSSEEvent(t, chunk)
			if event == "response.completed" {
				completed = data.Get("response.output")
			}
		}
	}
	request := []byte(`{"model":"gemini-3.6-flash-high","input":[]}`)
	request, _ = sjson.SetRawBytes(request, "input", []byte(completed.Raw))
	translated := ConvertOpenAIResponsesRequestToGemini("gemini-3.6-flash-high", request, false)
	var signatures []string
	for _, content := range gjson.GetBytes(translated, "contents").Array() {
		for _, part := range content.Get("parts").Array() {
			if signature := part.Get("thoughtSignature").String(); signature != "" {
				signatures = append(signatures, signature)
			}
		}
	}
	if len(signatures) != 2 || signatures[0] != testResponsesGeminiThoughtSignature || signatures[1] != signature2 {
		t.Fatalf("function/trailing signatures = %v; completed=%s translated=%s", signatures, completed.Raw, translated)
	}
}

func TestConvertGeminiResponseToOpenAIResponsesNonStream_FunctionAndTrailingSignaturesPreserveOrder(t *testing.T) {
	signature2 := differentResponsesGeminiThoughtSignature(t)
	raw := []byte(`{"candidates":[{"content":{"parts":[{"thoughtSignature":"` + testResponsesGeminiThoughtSignature + `","functionCall":{"name":"run_command","args":{"command":"true"}}},{"text":"","thoughtSignature":"` + signature2 + `"}]},"finishReason":"STOP"}],"responseId":"function-trailing-nonstream"}`)
	out := ConvertGeminiResponseToOpenAIResponsesNonStream(context.Background(), "gemini-3.6-flash-high", nil, nil, raw, nil)
	if decodedResponsesCarrierSignature(t, gjson.GetBytes(out, "output.0.encrypted_content").String()) != testResponsesGeminiThoughtSignature || gjson.GetBytes(out, "output.1.type").String() != "function_call" || decodedResponsesCarrierSignature(t, gjson.GetBytes(out, "output.2.encrypted_content").String()) != signature2 {
		t.Fatalf("non-stream function/trailing order malformed: %s", out)
	}
}

func TestConvertGeminiResponseToOpenAIResponses_FunctionThenTrailingSignatureHasStreamParity(t *testing.T) {
	raw := []byte(`{"candidates":[{"content":{"parts":[{"text":"preamble"},{"functionCall":{"name":"run_command","args":{"command":"true"}}},{"text":"","thoughtSignature":"` + testResponsesGeminiThoughtSignature + `"}]},"finishReason":"STOP"}],"responseId":"function-trailing-parity"}`)

	var param any
	var streamOutput gjson.Result
	for _, chunk := range ConvertGeminiResponseToOpenAIResponses(context.Background(), "gemini-3.6-flash-high", nil, nil, append([]byte("data: "), raw...), &param) {
		event, data := parseSSEEvent(t, chunk)
		if event == "response.completed" {
			streamOutput = data.Get("response.output")
		}
	}
	nonStream := ConvertGeminiResponseToOpenAIResponsesNonStream(context.Background(), "gemini-3.6-flash-high", nil, nil, raw, nil)
	nonStreamOutput := gjson.GetBytes(nonStream, "output")
	for name, output := range map[string]gjson.Result{"stream": streamOutput, "non-stream": nonStreamOutput} {
		items := output.Array()
		if len(items) != 3 || items[0].Get("type").String() != "message" || items[1].Get("type").String() != "function_call" || items[2].Get("type").String() != "reasoning" {
			t.Fatalf("%s function/trailing order malformed: %s", name, output.Raw)
		}
		signature, direction, targetKind, marked, ok := decodeGeminiResponsesCarrier(items[2].Get("encrypted_content").String())
		if !marked || !ok || signature != testResponsesGeminiThoughtSignature || direction != geminiResponsesCarrierPrevious || targetKind != geminiResponsesCarrierFunction {
			t.Fatalf("%s function/trailing carrier malformed: %s", name, output.Raw)
		}
		request := []byte(`{"model":"gemini-3.6-flash-high","input":[]}`)
		request, _ = sjson.SetRawBytes(request, "input", []byte(output.Raw))
		translated := ConvertOpenAIResponsesRequestToGemini("gemini-3.6-flash-high", request, false)
		parts := gjson.GetBytes(translated, "contents.0.parts").Array()
		if len(parts) != 2 || parts[0].Get("text").String() != "preamble" || parts[1].Get("functionCall.name").String() != "run_command" || parts[1].Get("thoughtSignature").String() != testResponsesGeminiThoughtSignature {
			t.Fatalf("%s trailing function signature did not replay: %s", name, translated)
		}
	}
}

func TestConvertGeminiResponseToOpenAIResponsesNonStream_TrailingSignatureFollowsPendingReasoning(t *testing.T) {
	signature2 := differentResponsesGeminiThoughtSignature(t)
	raw := []byte(`{"candidates":[{"content":{"parts":[{"text":"thought","thought":true,"thoughtSignature":"` + testResponsesGeminiThoughtSignature + `"},{"text":"","thoughtSignature":"` + signature2 + `"}]},"finishReason":"STOP"}],"responseId":"reasoning-trailing-nonstream"}`)
	out := ConvertGeminiResponseToOpenAIResponsesNonStream(context.Background(), "gemini-3.6-flash-high", nil, nil, raw, nil)
	if decodedResponsesCarrierSignature(t, gjson.GetBytes(out, "output.0.encrypted_content").String()) != testResponsesGeminiThoughtSignature || decodedResponsesCarrierSignature(t, gjson.GetBytes(out, "output.1.encrypted_content").String()) != signature2 {
		t.Fatalf("non-stream reasoning/trailing order malformed: %s", out)
	}
}

func TestConvertGeminiResponseToOpenAIResponsesNonStream_UnsignedThoughtDoesNotStealFunctionSignature(t *testing.T) {
	raw := []byte(`{"candidates":[{"content":{"parts":[{"thoughtSignature":"` + testResponsesGeminiThoughtSignature + `","functionCall":{"name":"run_command","args":{"command":"true"}}},{"text":"later thought","thought":true}]},"finishReason":"STOP"}],"responseId":"function-unsigned-thought"}`)
	out := ConvertGeminiResponseToOpenAIResponsesNonStream(context.Background(), "gemini-3.6-flash-high", nil, nil, raw, nil)
	if decodedResponsesCarrierSignature(t, gjson.GetBytes(out, "output.0.encrypted_content").String()) != testResponsesGeminiThoughtSignature || gjson.GetBytes(out, "output.1.type").String() != "function_call" || gjson.GetBytes(out, "output.2.summary.0.text").String() != "later thought" || gjson.GetBytes(out, "output.2.encrypted_content").String() != "" {
		t.Fatalf("unsigned thought stole function signature: %s", out)
	}
}

func TestConvertGeminiResponseToOpenAIResponses_InterleavedThoughtAndTextPreservesOrder(t *testing.T) {
	signature2 := differentResponsesGeminiThoughtSignature(t)
	line := []byte(`data: {"response":{"candidates":[{"content":{"parts":[{"text":"thought-a","thought":true,"thoughtSignature":"` + testResponsesGeminiThoughtSignature + `"},{"text":"answer-a"},{"text":"thought-b","thought":true,"thoughtSignature":"` + signature2 + `"},{"text":"answer-b"}]},"finishReason":"STOP"}],"responseId":"interleaved"}}`)
	var param any
	var doneTypes []string
	var completed gjson.Result
	for _, chunk := range ConvertGeminiResponseToOpenAIResponses(context.Background(), "gemini-3.6-flash-high", nil, nil, line, &param) {
		event, data := parseSSEEvent(t, chunk)
		if event == "response.output_item.done" {
			doneTypes = append(doneTypes, data.Get("item.type").String())
		}
		if event == "response.completed" {
			completed = data.Get("response.output")
		}
	}
	if got := strings.Join(doneTypes, ","); got != "reasoning,message,reasoning,message" {
		t.Fatalf("interleaved done order = %q", got)
	}
	if completed.Get("0.summary.0.text").String() != "thought-a" || completed.Get("1.content.0.text").String() != "answer-a" || completed.Get("2.summary.0.text").String() != "thought-b" || completed.Get("3.content.0.text").String() != "answer-b" {
		t.Fatalf("interleaved completed output malformed: %s", completed.Raw)
	}
}

func TestConvertGeminiResponseToOpenAIResponsesNonStream_InterleavedThoughtAndTextPreservesOrder(t *testing.T) {
	signature2 := differentResponsesGeminiThoughtSignature(t)
	raw := []byte(`{"candidates":[{"content":{"parts":[{"text":"thought-a","thought":true,"thoughtSignature":"` + testResponsesGeminiThoughtSignature + `"},{"text":"answer-a"},{"text":"thought-b","thought":true,"thoughtSignature":"` + signature2 + `"},{"text":"answer-b"}]},"finishReason":"STOP"}],"responseId":"interleaved-nonstream"}`)
	out := ConvertGeminiResponseToOpenAIResponsesNonStream(context.Background(), "gemini-3.6-flash-high", nil, nil, raw, nil)
	if got := gjson.GetBytes(out, "output.#").Int(); got != 4 {
		t.Fatalf("interleaved non-stream output count = %d; output=%s", got, out)
	}
	if gjson.GetBytes(out, "output.0.type").String() != "reasoning" || gjson.GetBytes(out, "output.1.type").String() != "message" || gjson.GetBytes(out, "output.2.type").String() != "reasoning" || gjson.GetBytes(out, "output.3.type").String() != "message" {
		t.Fatalf("interleaved non-stream order malformed: %s", out)
	}
}

func TestConvertGeminiResponseToOpenAIResponses_LeadingEmptyAndSignedTextRoundTripInOrder(t *testing.T) {
	signature2 := differentResponsesGeminiThoughtSignature(t)
	in := []string{
		`data: {"response":{"candidates":[{"content":{"parts":[{"text":"","thoughtSignature":"` + testResponsesGeminiThoughtSignature + `"}]}}],"responseId":"leading-empty-signed-text"}}`,
		`data: {"response":{"candidates":[{"content":{"parts":[{"text":"answer","thoughtSignature":"` + signature2 + `"}]},"finishReason":"STOP"}],"responseId":"leading-empty-signed-text"}}`,
	}
	var param any
	var completed gjson.Result
	for _, line := range in {
		for _, chunk := range ConvertGeminiResponseToOpenAIResponses(context.Background(), "gemini-3.6-flash-high", nil, nil, []byte(line), &param) {
			event, data := parseSSEEvent(t, chunk)
			if event == "response.completed" {
				completed = data.Get("response.output")
			}
		}
	}
	request := []byte(`{"model":"gemini-3.6-flash-high","input":[]}`)
	request, _ = sjson.SetRawBytes(request, "input", []byte(completed.Raw))
	translated := ConvertOpenAIResponsesRequestToGemini("gemini-3.6-flash-high", request, false)
	var signatures []string
	visibleSignature := ""
	for _, part := range gjson.GetBytes(translated, "contents.0.parts").Array() {
		if signature := part.Get("thoughtSignature").String(); signature != "" {
			signatures = append(signatures, signature)
			if part.Get("text").String() == "answer" {
				visibleSignature = signature
			}
		}
	}
	if len(signatures) != 2 || signatures[0] != testResponsesGeminiThoughtSignature || visibleSignature != signature2 {
		t.Fatalf("leading empty/signed text signatures=%v visible=%q translated=%s", signatures, visibleSignature, translated)
	}
}

func TestConvertGeminiResponseToOpenAIResponses_SignedTextAndTrailingSignatureRoundTripInOrder(t *testing.T) {
	signature2 := differentResponsesGeminiThoughtSignature(t)
	in := []string{
		`data: {"response":{"candidates":[{"content":{"parts":[{"text":"answer","thoughtSignature":"` + testResponsesGeminiThoughtSignature + `"}]}}],"responseId":"signed-text-trailing"}}`,
		`data: {"response":{"candidates":[{"content":{"parts":[{"text":"","thoughtSignature":"` + signature2 + `"}]},"finishReason":"STOP"}],"responseId":"signed-text-trailing"}}`,
	}
	var param any
	var completed gjson.Result
	for _, line := range in {
		for _, chunk := range ConvertGeminiResponseToOpenAIResponses(context.Background(), "gemini-3.6-flash-high", nil, nil, []byte(line), &param) {
			event, data := parseSSEEvent(t, chunk)
			if event == "response.completed" {
				completed = data.Get("response.output")
			}
		}
	}
	if completed.Get("0.type").String() != "message" || decodedResponsesCarrierSignature(t, completed.Get("1.encrypted_content").String()) != testResponsesGeminiThoughtSignature || decodedResponsesCarrierSignature(t, completed.Get("2.encrypted_content").String()) != signature2 {
		t.Fatalf("signed text/trailing completed order malformed: %s", completed.Raw)
	}
	request := []byte(`{"model":"gemini-3.6-flash-high","input":[]}`)
	request, _ = sjson.SetRawBytes(request, "input", []byte(completed.Raw))
	translated := ConvertOpenAIResponsesRequestToGemini("gemini-3.6-flash-high", request, false)
	var signatures []string
	for _, part := range gjson.GetBytes(translated, "contents.0.parts").Array() {
		if signature := part.Get("thoughtSignature").String(); signature != "" {
			signatures = append(signatures, signature)
		}
	}
	if len(signatures) != 2 || signatures[0] != testResponsesGeminiThoughtSignature || signatures[1] != signature2 {
		t.Fatalf("signed text/trailing signatures = %v; translated=%s", signatures, translated)
	}
}

func TestConvertGeminiResponseToOpenAIResponses_PreservesMultipleLeadingEmptySignatures(t *testing.T) {
	signature2 := differentResponsesGeminiThoughtSignature(t)
	line := []byte(`data: {"response":{"candidates":[{"content":{"parts":[{"text":"","thoughtSignature":"` + testResponsesGeminiThoughtSignature + `"},{"text":"","thoughtSignature":"` + signature2 + `"}]},"finishReason":"STOP"}],"responseId":"leading-empty-signatures"}}`)
	var param any
	var completed gjson.Result
	for _, chunk := range ConvertGeminiResponseToOpenAIResponses(context.Background(), "gemini-3.6-flash-high", nil, nil, line, &param) {
		event, data := parseSSEEvent(t, chunk)
		if event == "response.completed" {
			completed = data.Get("response.output")
		}
	}
	if decodedResponsesCarrierSignature(t, completed.Get("0.encrypted_content").String()) != testResponsesGeminiThoughtSignature || decodedResponsesCarrierSignature(t, completed.Get("1.encrypted_content").String()) != signature2 {
		t.Fatalf("leading empty signatures were not preserved: %s", completed.Raw)
	}
}

func TestConvertGeminiResponseToOpenAIResponsesNonStream_SignedTextAndTrailingSignatureRoundTripInOrder(t *testing.T) {
	signature2 := differentResponsesGeminiThoughtSignature(t)
	raw := []byte(`{"candidates":[{"content":{"parts":[{"text":"answer","thoughtSignature":"` + testResponsesGeminiThoughtSignature + `"},{"text":"","thoughtSignature":"` + signature2 + `"}]},"finishReason":"STOP"}],"responseId":"signed-text-trailing-nonstream"}`)
	out := ConvertGeminiResponseToOpenAIResponsesNonStream(context.Background(), "gemini-3.6-flash-high", nil, nil, raw, nil)
	if decodedResponsesCarrierSignature(t, gjson.GetBytes(out, "output.0.encrypted_content").String()) != testResponsesGeminiThoughtSignature || gjson.GetBytes(out, "output.1.type").String() != "message" || decodedResponsesCarrierSignature(t, gjson.GetBytes(out, "output.2.encrypted_content").String()) != signature2 {
		t.Fatalf("non-stream signed text/trailing order malformed: %s", out)
	}
}

func TestConvertGeminiResponseToOpenAIResponses_DistinctSignedThoughtsUseDistinctItems(t *testing.T) {
	signature2 := differentResponsesGeminiThoughtSignature(t)
	in := []string{
		`data: {"response":{"candidates":[{"content":{"parts":[{"text":"one","thought":true,"thoughtSignature":"` + testResponsesGeminiThoughtSignature + `"}]}}],"modelVersion":"gemini-3.6-flash","responseId":"signed-thoughts"}}`,
		`data: {"response":{"candidates":[{"content":{"parts":[{"text":"two","thought":true,"thoughtSignature":"` + signature2 + `"}]},"finishReason":"STOP"}],"modelVersion":"gemini-3.6-flash","responseId":"signed-thoughts"}}`,
	}
	var param any
	added := make(map[string]string)
	done := make(map[string]string)
	var completed gjson.Result
	for _, line := range in {
		for _, chunk := range ConvertGeminiResponseToOpenAIResponses(context.Background(), "gemini-3.6-flash-high", nil, nil, []byte(line), &param) {
			event, data := parseSSEEvent(t, chunk)
			if data.Get("item.type").String() == "reasoning" {
				switch event {
				case "response.output_item.added":
					added[data.Get("item.id").String()] = data.Get("item.encrypted_content").String()
				case "response.output_item.done":
					done[data.Get("item.id").String()] = data.Get("item.encrypted_content").String()
				}
			}
			if event == "response.completed" {
				completed = data.Get("response.output")
			}
		}
	}
	if len(added) != 2 || len(done) != 2 {
		t.Fatalf("reasoning items added/done = %d/%d, want 2/2", len(added), len(done))
	}
	for id, signature := range added {
		if done[id] != signature {
			t.Fatalf("reasoning item %s changed signature from %q to %q", id, signature, done[id])
		}
	}
	if got := decodedResponsesCarrierSignature(t, completed.Get("0.encrypted_content").String()); got != testResponsesGeminiThoughtSignature {
		t.Fatalf("first completed signature = %q", got)
	}
	if got := decodedResponsesCarrierSignature(t, completed.Get("1.encrypted_content").String()); got != signature2 {
		t.Fatalf("second completed signature = %q", got)
	}
}

func TestConvertGeminiResponseToOpenAIResponsesNonStream_DistinctSignedThoughtsUseDistinctItems(t *testing.T) {
	signature2 := differentResponsesGeminiThoughtSignature(t)
	raw := []byte(`{"candidates":[{"content":{"parts":[{"text":"one","thought":true,"thoughtSignature":"` + testResponsesGeminiThoughtSignature + `"},{"text":"two","thought":true,"thoughtSignature":"` + signature2 + `"}]},"finishReason":"STOP"}],"responseId":"signed-thoughts-nonstream"}`)
	out := ConvertGeminiResponseToOpenAIResponsesNonStream(context.Background(), "gemini-3.6-flash-high", nil, nil, raw, nil)
	if got := gjson.GetBytes(out, "output.#").Int(); got != 2 {
		t.Fatalf("reasoning output count = %d, want 2; output=%s", got, out)
	}
	if got := decodedResponsesCarrierSignature(t, gjson.GetBytes(out, "output.0.encrypted_content").String()); got != testResponsesGeminiThoughtSignature {
		t.Fatalf("first signature = %q; output=%s", got, out)
	}
	if got := decodedResponsesCarrierSignature(t, gjson.GetBytes(out, "output.1.encrypted_content").String()); got != signature2 {
		t.Fatalf("second signature = %q; output=%s", got, out)
	}
}

func TestConvertGeminiResponseToOpenAIResponses_VisibleSignatureCompletesActiveReasoning(t *testing.T) {
	in := []string{
		`data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"hidden thought","thought":true}]}}],"modelVersion":"gemini-3.6-flash","responseId":"resp_active_reasoning"}}`,
		`data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"visible answer","thoughtSignature":"` + testResponsesGeminiThoughtSignature + `"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":2,"thoughtsTokenCount":3,"totalTokenCount":15},"modelVersion":"gemini-3.6-flash","responseId":"resp_active_reasoning"}}`,
	}
	var param any
	var out [][]byte
	for _, line := range in {
		out = append(out, ConvertGeminiResponseToOpenAIResponses(context.Background(), "gemini-3.6-flash-high", nil, nil, []byte(line), &param)...)
	}
	var doneTypes []string
	var addedID, addedSignature, doneID, doneSignature string
	for _, chunk := range out {
		event, data := parseSSEEvent(t, chunk)
		if event == "response.output_item.added" && data.Get("item.type").String() == "reasoning" {
			addedID = data.Get("item.id").String()
			addedSignature = data.Get("item.encrypted_content").String()
		}
		if event != "response.output_item.done" {
			continue
		}
		doneTypes = append(doneTypes, data.Get("item.type").String())
		if data.Get("item.type").String() == "reasoning" {
			doneID = data.Get("item.id").String()
			doneSignature = data.Get("item.encrypted_content").String()
		}
	}
	if got := strings.Join(doneTypes, ","); got != "reasoning,message" {
		t.Fatalf("done item order = %q, want reasoning,message", got)
	}
	if addedID == "" || addedID != doneID || decodedResponsesCarrierSignature(t, addedSignature) != testResponsesGeminiThoughtSignature || doneSignature != addedSignature {
		t.Fatalf("reasoning item changed between added and done: added=(%q,%q) done=(%q,%q)", addedID, addedSignature, doneID, doneSignature)
	}
}

func TestConvertGeminiResponseToOpenAIResponses_LateThoughtSignatureIsImmutable(t *testing.T) {
	signature := differentResponsesGeminiThoughtSignature(t)
	in := []string{
		`data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"one","thought":true}]}}],"responseId":"late-thought-signature"}}`,
		`data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"two","thought":true,"thoughtSignature":"` + signature + `"}]},"finishReason":"STOP"}],"responseId":"late-thought-signature"}}`,
	}
	var param any
	var addedID, addedSignature, doneID, doneSignature, doneText string
	for _, line := range in {
		for _, chunk := range ConvertGeminiResponseToOpenAIResponses(context.Background(), "gemini-3.6-flash-high", nil, nil, []byte(line), &param) {
			event, data := parseSSEEvent(t, chunk)
			switch event {
			case "response.output_item.added":
				if data.Get("item.type").String() == "reasoning" {
					addedID = data.Get("item.id").String()
					addedSignature = data.Get("item.encrypted_content").String()
				}
			case "response.output_item.done":
				if data.Get("item.type").String() == "reasoning" {
					doneID = data.Get("item.id").String()
					doneSignature = data.Get("item.encrypted_content").String()
					doneText = data.Get("item.summary.0.text").String()
				}
			}
		}
	}
	if addedID == "" || addedID != doneID || decodedResponsesCarrierSignature(t, addedSignature) != signature || doneSignature != addedSignature || doneText != "onetwo" {
		t.Fatalf("late thought signature replay malformed: added=(%q,%q) done=(%q,%q,%q)", addedID, addedSignature, doneID, doneSignature, doneText)
	}
}

func TestConvertGeminiResponseToOpenAIResponses_DoneFlushesUnsignedReasoningWithoutCompletion(t *testing.T) {
	var param any
	var out [][]byte
	out = append(out, ConvertGeminiResponseToOpenAIResponses(context.Background(), "gemini-3.6-flash-high", nil, nil, []byte(`data: {"response":{"candidates":[{"content":{"parts":[{"text":"unsigned thought","thought":true}]}}],"responseId":"done-flush"}}`), &param)...)
	out = append(out, ConvertGeminiResponseToOpenAIResponses(context.Background(), "gemini-3.6-flash-high", nil, nil, []byte("[DONE]"), &param)...)
	var addedSignature string
	var deltas []string
	doneCount := 0
	completedCount := 0
	for _, chunk := range out {
		event, data := parseSSEEvent(t, chunk)
		switch event {
		case "response.output_item.added":
			if data.Get("item.type").String() == "reasoning" {
				addedSignature = data.Get("item.encrypted_content").String()
			}
		case "response.reasoning_summary_text.delta":
			deltas = append(deltas, data.Get("delta").String())
		case "response.output_item.done":
			doneCount++
		case "response.completed":
			completedCount++
		}
	}
	if addedSignature != "" || strings.Join(deltas, "") != "unsigned thought" || doneCount != 0 || completedCount != 0 {
		t.Fatalf("DONE flush malformed: added=%q deltas=%q done=%d completed=%d", addedSignature, deltas, doneCount, completedCount)
	}
	if duplicate := ConvertGeminiResponseToOpenAIResponses(context.Background(), "gemini-3.6-flash-high", nil, nil, []byte("[DONE]"), &param); len(duplicate) != 0 {
		t.Fatalf("duplicate DONE emitted %d events", len(duplicate))
	}
}

func TestConvertGeminiResponseToOpenAIResponsesNonStream_VisibleSignatureCompletesReasoning(t *testing.T) {
	raw := []byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":"hidden thought","thought":true},{"text":"visible answer","thoughtSignature":"` + testResponsesGeminiThoughtSignature + `"}]},"finishReason":"STOP"}],"modelVersion":"gemini-3.6-flash","responseId":"resp_nonstream_active"}`)
	out := ConvertGeminiResponseToOpenAIResponsesNonStream(context.Background(), "gemini-3.6-flash-high", nil, nil, raw, nil)
	if got := gjson.GetBytes(out, "output.0.type").String(); got != "reasoning" {
		t.Fatalf("output.0.type = %q, want reasoning; output=%s", got, out)
	}
	if got := decodedResponsesCarrierSignature(t, gjson.GetBytes(out, "output.0.encrypted_content").String()); got != testResponsesGeminiThoughtSignature {
		t.Fatalf("reasoning signature = %q, want %q; output=%s", got, testResponsesGeminiThoughtSignature, out)
	}
	if got := gjson.GetBytes(out, "output.1.type").String(); got != "message" {
		t.Fatalf("output.1.type = %q, want message; output=%s", got, out)
	}
}

func TestConvertGeminiResponseToOpenAIResponses_PreservesTextAroundFunction(t *testing.T) {
	in := []string{
		`data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"preface"}]}}],"modelVersion":"gemini-3.6-flash","responseId":"resp_mixed_stream"}}`,
		`data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"run_command","args":{"command":"true"}}}]}}],"modelVersion":"gemini-3.6-flash","responseId":"resp_mixed_stream"}}`,
		`data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"after"}]},"finishReason":"STOP"}],"modelVersion":"gemini-3.6-flash","responseId":"resp_mixed_stream"}}`,
	}
	var param any
	var out [][]byte
	for _, line := range in {
		out = append(out, ConvertGeminiResponseToOpenAIResponses(context.Background(), "gemini-3.6-flash-high", nil, nil, []byte(line), &param)...)
	}
	var doneTypes []string
	var completed gjson.Result
	for _, chunk := range out {
		event, data := parseSSEEvent(t, chunk)
		if event == "response.output_item.done" {
			doneTypes = append(doneTypes, data.Get("item.type").String())
		}
		if event == "response.completed" {
			completed = data.Get("response.output")
		}
	}
	if got := strings.Join(doneTypes, ","); got != "message,function_call,message" {
		t.Fatalf("done item order = %q, want message,function_call,message", got)
	}
	if got := completed.Get("0.content.0.text").String(); got != "preface" {
		t.Fatalf("completed first message = %q", got)
	}
	if got := completed.Get("2.content.0.text").String(); got != "after" {
		t.Fatalf("completed trailing message = %q", got)
	}

	request := []byte(`{"model":"gemini-3.6-flash-high","input":[]}`)
	request, _ = sjson.SetRawBytes(request, "input", []byte(completed.Raw))
	functionOutput := []byte(`{"type":"function_call_output","call_id":"","output":"ok"}`)
	functionOutput, _ = sjson.SetBytes(functionOutput, "call_id", completed.Get("1.call_id").String())
	request, _ = sjson.SetRawBytes(request, "input.-1", functionOutput)
	translated := ConvertOpenAIResponsesRequestToGemini("gemini-3.6-flash-high", request, false)
	contents := gjson.GetBytes(translated, "contents").Array()
	if len(contents) != 2 || contents[0].Get("role").String() != "model" || contents[1].Get("role").String() != "user" {
		t.Fatalf("mixed turn round-trip roles malformed: %s", translated)
	}
	parts := contents[0].Get("parts").Array()
	if len(parts) != 3 || parts[0].Get("text").String() != "preface" || !parts[1].Get("functionCall").Exists() || parts[2].Get("text").String() != "after" {
		t.Fatalf("mixed turn model parts malformed: %s", translated)
	}
	if !contents[1].Get("parts.0.functionResponse").Exists() {
		t.Fatalf("function response must immediately follow the combined model turn: %s", translated)
	}
}

func TestConvertGeminiResponseToOpenAIResponses_SignedTextBeforeSignedFunctionRoundTrips(t *testing.T) {
	toolRaw, errDecode := base64.StdEncoding.DecodeString(testResponsesGeminiThoughtSignature)
	if errDecode != nil {
		t.Fatal(errDecode)
	}
	toolRaw[len(toolRaw)-1] ^= 1
	toolSignature := base64.StdEncoding.EncodeToString(toolRaw)
	in := []string{
		`data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"before "}]}}],"modelVersion":"gemini-3.6-flash","responseId":"resp_signed_mixed"}}`,
		`data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"tool","thoughtSignature":"` + testResponsesGeminiThoughtSignature + `"}]}}],"modelVersion":"gemini-3.6-flash","responseId":"resp_signed_mixed"}}`,
		`data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"thoughtSignature":"` + toolSignature + `","functionCall":{"name":"run_command","args":{"command":"true"}}}]},"finishReason":"STOP"}],"modelVersion":"gemini-3.6-flash","responseId":"resp_signed_mixed"}}`,
	}
	var param any
	var completed gjson.Result
	for _, line := range in {
		for _, chunk := range ConvertGeminiResponseToOpenAIResponses(context.Background(), "gemini-3.6-flash-high", nil, nil, []byte(line), &param) {
			event, data := parseSSEEvent(t, chunk)
			if event == "response.completed" {
				completed = data.Get("response.output")
			}
		}
	}
	request := []byte(`{"model":"gemini-3.6-flash-high","input":[]}`)
	request, _ = sjson.SetRawBytes(request, "input", []byte(completed.Raw))
	callID := completed.Get("3.call_id").String()
	functionOutput := []byte(`{"type":"function_call_output","call_id":"","output":"ok"}`)
	functionOutput, _ = sjson.SetBytes(functionOutput, "call_id", callID)
	request, _ = sjson.SetRawBytes(request, "input.-1", functionOutput)
	translated := ConvertOpenAIResponsesRequestToGemini("gemini-3.6-flash-high", request, false)

	var textSignature, functionSignature string
	for _, content := range gjson.GetBytes(translated, "contents").Array() {
		for _, part := range content.Get("parts").Array() {
			if part.Get("functionCall").Exists() {
				functionSignature = part.Get("thoughtSignature").String()
			} else if part.Get("text").String() == "before tool" {
				textSignature = part.Get("thoughtSignature").String()
			}
		}
	}
	if textSignature != testResponsesGeminiThoughtSignature {
		t.Fatalf("text signature = %q, want %q; translated=%s", textSignature, testResponsesGeminiThoughtSignature, translated)
	}
	if functionSignature != toolSignature {
		t.Fatalf("function signature = %q, want %q; completed=%s translated=%s", functionSignature, toolSignature, completed.Raw, translated)
	}
}

func TestConvertGeminiResponseToOpenAIResponsesNonStream_PreservesTextAroundSignedFunction(t *testing.T) {
	raw := []byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":"preface"},{"thoughtSignature":"` + testResponsesGeminiThoughtSignature + `","functionCall":{"name":"run_command","args":{"command":"true"}}},{"text":"after"}]},"finishReason":"STOP"}],"modelVersion":"gemini-3.6-flash","responseId":"resp_nonstream_order"}`)
	out := ConvertGeminiResponseToOpenAIResponsesNonStream(context.Background(), "gemini-3.6-flash-high", nil, nil, raw, nil)
	if got := gjson.GetBytes(out, "output.0.type").String(); got != "message" {
		t.Fatalf("output.0.type = %q, want message; output=%s", got, out)
	}
	if got := gjson.GetBytes(out, "output.1.type").String(); got != "reasoning" {
		t.Fatalf("output.1.type = %q, want reasoning; output=%s", got, out)
	}
	if got := gjson.GetBytes(out, "output.2.type").String(); got != "function_call" {
		t.Fatalf("output.2.type = %q, want function_call; output=%s", got, out)
	}
	if got := gjson.GetBytes(out, "output.3.type").String(); got != "message" {
		t.Fatalf("output.3.type = %q, want trailing message; output=%s", got, out)
	}
	if got := gjson.GetBytes(out, "output.3.content.0.text").String(); got != "after" {
		t.Fatalf("trailing message = %q, want after; output=%s", got, out)
	}
}

func TestConvertGeminiResponseToOpenAIResponses_DetachedSignatureAfterVisibleText(t *testing.T) {
	in := []string{
		`data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"visible answer"}]}}],"modelVersion":"gemini-3.6-flash","responseId":"resp_detached"}}`,
		`data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"","thoughtSignature":"` + testResponsesGeminiThoughtSignature + `"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":2,"thoughtsTokenCount":3,"totalTokenCount":15},"modelVersion":"gemini-3.6-flash","responseId":"resp_detached"}}`,
	}
	var param any
	var out [][]byte
	for _, line := range in {
		out = append(out, ConvertGeminiResponseToOpenAIResponses(context.Background(), "gemini-3.6-flash-high", nil, nil, []byte(line), &param)...)
	}
	var doneTypes []string
	var doneSignature string
	var completedOutput gjson.Result
	for _, chunk := range out {
		event, data := parseSSEEvent(t, chunk)
		switch event {
		case "response.output_item.done":
			doneTypes = append(doneTypes, data.Get("item.type").String())
			if data.Get("item.type").String() == "reasoning" {
				doneSignature = data.Get("item.encrypted_content").String()
			}
		case "response.completed":
			completedOutput = data.Get("response.output")
		}
	}
	if got := strings.Join(doneTypes, ","); got != "message,reasoning" {
		t.Fatalf("done item order = %q, want message,reasoning", got)
	}
	if decodedResponsesCarrierSignature(t, doneSignature) != testResponsesGeminiThoughtSignature {
		t.Fatalf("detached signature = %q, want %q", doneSignature, testResponsesGeminiThoughtSignature)
	}
	if got := decodedResponsesCarrierSignature(t, completedOutput.Get("1.encrypted_content").String()); got != testResponsesGeminiThoughtSignature {
		t.Fatalf("completed detached signature = %q, want %q; output=%s", got, testResponsesGeminiThoughtSignature, completedOutput.Raw)
	}
}

func TestConvertGeminiResponseToOpenAIResponses_GeminiToolSignature(t *testing.T) {
	line := `data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"thoughtSignature":"` + testResponsesGeminiThoughtSignature + `","functionCall":{"id":"native-id","name":"run_command","args":{"command":"true"}}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":2,"thoughtsTokenCount":3,"totalTokenCount":15},"modelVersion":"gemini-3.6-flash","responseId":"resp_tool_sig"}}`
	var param any
	out := ConvertGeminiResponseToOpenAIResponses(context.Background(), "gemini-3.6-flash-high", nil, nil, []byte(line), &param)
	var doneTypes []string
	var signature string
	for _, chunk := range out {
		event, data := parseSSEEvent(t, chunk)
		if event != "response.output_item.done" {
			continue
		}
		doneTypes = append(doneTypes, data.Get("item.type").String())
		if data.Get("item.type").String() == "reasoning" {
			signature = data.Get("item.encrypted_content").String()
		}
	}
	if got := strings.Join(doneTypes, ","); got != "reasoning,function_call" {
		t.Fatalf("tool signature item order = %q, want reasoning,function_call", got)
	}
	if decodedResponsesCarrierSignature(t, signature) != testResponsesGeminiThoughtSignature {
		t.Fatalf("tool signature = %q, want %q", signature, testResponsesGeminiThoughtSignature)
	}
}

func TestConvertGeminiResponseToOpenAIResponsesNonStream_DetachedSignature(t *testing.T) {
	raw := []byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":"visible answer"},{"text":"","thoughtSignature":"` + testResponsesGeminiThoughtSignature + `"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":2,"thoughtsTokenCount":3,"totalTokenCount":15},"modelVersion":"gemini-3.6-flash","responseId":"resp_nonstream_detached"}`)
	out := ConvertGeminiResponseToOpenAIResponsesNonStream(context.Background(), "gemini-3.6-flash-high", nil, nil, raw, nil)
	if got := gjson.GetBytes(out, "output.0.type").String(); got != "reasoning" {
		t.Fatalf("output.0.type = %q, want reasoning; output=%s", got, out)
	}
	if got := decodedResponsesCarrierSignature(t, gjson.GetBytes(out, "output.0.encrypted_content").String()); got != testResponsesGeminiThoughtSignature {
		t.Fatalf("detached signature = %q, want %q; output=%s", got, testResponsesGeminiThoughtSignature, out)
	}
	if got := gjson.GetBytes(out, "output.1.type").String(); got != "message" {
		t.Fatalf("output.1.type = %q, want message; output=%s", got, out)
	}
}

func TestConvertGeminiResponseToOpenAIResponses_ReasoningEncryptedContent(t *testing.T) {
	sig := "RXE0RENrZ0lDeEFDR0FJcVFOZDdjUzlleGFuRktRdFcvSzNyZ2MvWDNCcDQ4RmxSbGxOWUlOVU5kR1l1UHMrMGdkMVp0Vkg3ekdKU0g4YVljc2JjN3lNK0FrdGpTNUdqamI4T3Z0VVNETzdQd3pmcFhUOGl3U3hXUEJvTVFRQ09mWTFyMEtTWGZxUUlJakFqdmFGWk83RW1XRlBKckJVOVpkYzdDKw=="
	in := []string{
		`data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"thought":true,"thoughtSignature":"` + sig + `","text":""}]}}],"modelVersion":"test-model","responseId":"req_vrtx_sig"},"traceId":"t1"}`,
		`data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"thought":true,"text":"a"}]}}],"modelVersion":"test-model","responseId":"req_vrtx_sig"},"traceId":"t1"}`,
		`data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"hello"}]}}],"modelVersion":"test-model","responseId":"req_vrtx_sig"},"traceId":"t1"}`,
		`data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"text":""}]},"finishReason":"STOP"}],"modelVersion":"test-model","responseId":"req_vrtx_sig"},"traceId":"t1"}`,
	}

	var param any
	var out [][]byte
	for _, line := range in {
		out = append(out, ConvertGeminiResponseToOpenAIResponses(context.Background(), "test-model", nil, nil, []byte(line), &param)...)
	}

	var (
		addedEnc string
		doneEnc  string
	)
	for _, chunk := range out {
		ev, data := parseSSEEvent(t, chunk)
		switch ev {
		case "response.output_item.added":
			if data.Get("item.type").String() == "reasoning" {
				addedEnc = data.Get("item.encrypted_content").String()
			}
		case "response.output_item.done":
			if data.Get("item.type").String() == "reasoning" {
				doneEnc = data.Get("item.encrypted_content").String()
			}
		}
	}

	if decodedResponsesCarrierSignature(t, addedEnc) != sig {
		t.Fatalf("unexpected encrypted_content in response.output_item.added: got %q", addedEnc)
	}
	if doneEnc != addedEnc || decodedResponsesCarrierSignature(t, doneEnc) != sig {
		t.Fatalf("unexpected encrypted_content in response.output_item.done: got %q", doneEnc)
	}
}

func TestConvertGeminiResponseToOpenAIResponses_FunctionCallEventOrder(t *testing.T) {
	in := []string{
		`data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"tool0"}}]}}],"modelVersion":"test-model","responseId":"req_vrtx_1"},"traceId":"t1"}`,
		`data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"tool1"}}]}}],"modelVersion":"test-model","responseId":"req_vrtx_1"},"traceId":"t1"}`,
		`data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"tool2","args":{"a":1}}}]}}],"modelVersion":"test-model","responseId":"req_vrtx_1"},"traceId":"t1"}`,
		`data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"text":""}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5,"totalTokenCount":15,"cachedContentTokenCount":0},"modelVersion":"test-model","responseId":"req_vrtx_1"},"traceId":"t1"}`,
	}

	var param any
	var out [][]byte
	for _, line := range in {
		out = append(out, ConvertGeminiResponseToOpenAIResponses(context.Background(), "test-model", nil, nil, []byte(line), &param)...)
	}

	posAdded := []int{-1, -1, -1}
	posArgsDelta := []int{-1, -1, -1}
	posArgsDone := []int{-1, -1, -1}
	posItemDone := []int{-1, -1, -1}
	posCompleted := -1
	deltaByIndex := map[int]string{}

	for i, chunk := range out {
		ev, data := parseSSEEvent(t, chunk)
		switch ev {
		case "response.output_item.added":
			if data.Get("item.type").String() != "function_call" {
				continue
			}
			idx := int(data.Get("output_index").Int())
			if idx >= 0 && idx < len(posAdded) {
				posAdded[idx] = i
			}
		case "response.function_call_arguments.delta":
			idx := int(data.Get("output_index").Int())
			if idx >= 0 && idx < len(posArgsDelta) {
				posArgsDelta[idx] = i
				deltaByIndex[idx] = data.Get("delta").String()
			}
		case "response.function_call_arguments.done":
			idx := int(data.Get("output_index").Int())
			if idx >= 0 && idx < len(posArgsDone) {
				posArgsDone[idx] = i
			}
		case "response.output_item.done":
			if data.Get("item.type").String() != "function_call" {
				continue
			}
			idx := int(data.Get("output_index").Int())
			if idx >= 0 && idx < len(posItemDone) {
				posItemDone[idx] = i
			}
		case "response.completed":
			posCompleted = i

			output := data.Get("response.output")
			if !output.Exists() || !output.IsArray() {
				t.Fatalf("missing response.output in response.completed")
			}
			if len(output.Array()) != 3 {
				t.Fatalf("unexpected response.output length: got %d", len(output.Array()))
			}
			if data.Get("response.output.0.name").String() != "tool0" || data.Get("response.output.0.arguments").String() != "{}" {
				t.Fatalf("unexpected output[0]: %s", data.Get("response.output.0").Raw)
			}
			if data.Get("response.output.1.name").String() != "tool1" || data.Get("response.output.1.arguments").String() != "{}" {
				t.Fatalf("unexpected output[1]: %s", data.Get("response.output.1").Raw)
			}
			if data.Get("response.output.2.name").String() != "tool2" {
				t.Fatalf("unexpected output[2] name: %s", data.Get("response.output.2").Raw)
			}
			if !gjson.Valid(data.Get("response.output.2.arguments").String()) {
				t.Fatalf("unexpected output[2] arguments: %q", data.Get("response.output.2.arguments").String())
			}
		}
	}

	if posCompleted == -1 {
		t.Fatalf("missing response.completed event")
	}
	for idx := 0; idx < 3; idx++ {
		if posAdded[idx] == -1 || posArgsDelta[idx] == -1 || posArgsDone[idx] == -1 || posItemDone[idx] == -1 {
			t.Fatalf("missing function call events for output_index %d: added=%d argsDelta=%d argsDone=%d itemDone=%d", idx, posAdded[idx], posArgsDelta[idx], posArgsDone[idx], posItemDone[idx])
		}
		if !(posAdded[idx] < posArgsDelta[idx] && posArgsDelta[idx] < posArgsDone[idx] && posArgsDone[idx] < posItemDone[idx]) {
			t.Fatalf("unexpected ordering for output_index %d: added=%d argsDelta=%d argsDone=%d itemDone=%d", idx, posAdded[idx], posArgsDelta[idx], posArgsDone[idx], posItemDone[idx])
		}
		if idx > 0 && !(posItemDone[idx-1] < posAdded[idx]) {
			t.Fatalf("function call events overlap between %d and %d: prevDone=%d nextAdded=%d", idx-1, idx, posItemDone[idx-1], posAdded[idx])
		}
	}

	if deltaByIndex[0] != "{}" {
		t.Fatalf("unexpected delta for output_index 0: got %q", deltaByIndex[0])
	}
	if deltaByIndex[1] != "{}" {
		t.Fatalf("unexpected delta for output_index 1: got %q", deltaByIndex[1])
	}
	if deltaByIndex[2] == "" || !gjson.Valid(deltaByIndex[2]) || gjson.Get(deltaByIndex[2], "a").Int() != 1 {
		t.Fatalf("unexpected delta for output_index 2: got %q", deltaByIndex[2])
	}
	if !(posItemDone[2] < posCompleted) {
		t.Fatalf("response.completed should be after last output_item.done: last=%d completed=%d", posItemDone[2], posCompleted)
	}
}

func TestConvertGeminiResponseToOpenAIResponses_ResponseOutputOrdering(t *testing.T) {
	in := []string{
		`data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"tool0","args":{"x":"y"}}}]}}],"modelVersion":"test-model","responseId":"req_vrtx_2"},"traceId":"t2"}`,
		`data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"hi"}]}}],"modelVersion":"test-model","responseId":"req_vrtx_2"},"traceId":"t2"}`,
		`data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"text":""}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2,"cachedContentTokenCount":0},"modelVersion":"test-model","responseId":"req_vrtx_2"},"traceId":"t2"}`,
	}

	var param any
	var out [][]byte
	for _, line := range in {
		out = append(out, ConvertGeminiResponseToOpenAIResponses(context.Background(), "test-model", nil, nil, []byte(line), &param)...)
	}

	posFuncDone := -1
	posMsgAdded := -1
	posCompleted := -1

	for i, chunk := range out {
		ev, data := parseSSEEvent(t, chunk)
		switch ev {
		case "response.output_item.done":
			if data.Get("item.type").String() == "function_call" && data.Get("output_index").Int() == 0 {
				posFuncDone = i
			}
		case "response.output_item.added":
			if data.Get("item.type").String() == "message" && data.Get("output_index").Int() == 1 {
				posMsgAdded = i
			}
		case "response.completed":
			posCompleted = i
			if data.Get("response.output.0.type").String() != "function_call" {
				t.Fatalf("expected response.output[0] to be function_call: %s", data.Get("response.output.0").Raw)
			}
			if data.Get("response.output.1.type").String() != "message" {
				t.Fatalf("expected response.output[1] to be message: %s", data.Get("response.output.1").Raw)
			}
			if data.Get("response.output.1.content.0.text").String() != "hi" {
				t.Fatalf("unexpected message text in response.output[1]: %s", data.Get("response.output.1").Raw)
			}
		}
	}

	if posFuncDone == -1 || posMsgAdded == -1 || posCompleted == -1 {
		t.Fatalf("missing required events: funcDone=%d msgAdded=%d completed=%d", posFuncDone, posMsgAdded, posCompleted)
	}
	if !(posFuncDone < posMsgAdded) {
		t.Fatalf("expected function_call to complete before message is added: funcDone=%d msgAdded=%d", posFuncDone, posMsgAdded)
	}
	if !(posMsgAdded < posCompleted) {
		t.Fatalf("expected response.completed after message added: msgAdded=%d completed=%d", posMsgAdded, posCompleted)
	}
}
