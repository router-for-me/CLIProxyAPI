// Regression tests for #7569: Gemini image parts (inlineData) must surface
// as image_generation_call items in the Responses output, thought images
// must not leak (#7566), and non-image inlineData must not be marked as PNG.
package responses

import (
	"context"
	"testing"

	"github.com/MoeForge/moego/ojson"
)

// TestConvertGeminiInlineDataImagePart7569 reproduces #7569: a Gemini image
// part (inlineData) must surface as an image_generation_call item in the
// Responses output, not be silently dropped. Thought images must not leak.
func TestConvertGeminiInlineDataImagePart7569(t *testing.T) {
	ctx := context.Background()
	var param any
	lines := []string{
		`data: {"candidates":[{"content":{"role":"model","parts":[{"thought":true,"inlineData":{"mimeType":"image/png","data":"RFJBRlQiW0FHRS1QUkQ="}}]}}],"modelVersion":"gemini-3.1-flash-image","responseId":"resp_7569"}`,
		`data: {"candidates":[{"content":{"role":"model","parts":[{"inlineData":{"mimeType":"image/png","data":"RklOQUxfSU1BR0U="}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":1120,"totalTokenCount":1123},"modelVersion":"gemini-3.1-flash-image","responseId":"resp_7569"}`,
	}
	var events [][]byte
	for _, line := range lines {
		events = append(events, ConvertGeminiResponseToOpenAIResponses(ctx, "gemini-3.1-flash-image", nil, nil, []byte(line), &param)...)
	}
	events = append(events, ConvertGeminiResponseToOpenAIResponses(ctx, "gemini-3.1-flash-image", nil, nil, []byte("[DONE]"), &param)...)

	var (
		sawImageAdded   bool
		sawImageDone    bool
		addedFormat     string
		doneFormat      string
		doneResult      string
		imageDoneCount  int
		thoughtLeaked   bool
		completedOutput ojson.Result
	)
	for _, ev := range events {
		event, data := parseSSEEvent(t, ev)
		switch event {
		case "response.output_item.added":
			if data.Get("item.type").String() == "image_generation_call" {
				sawImageAdded = true
				addedFormat = data.Get("item.output_format").String()
			}
		case "response.output_item.done":
			if data.Get("item.type").String() == "image_generation_call" {
				sawImageDone = true
				doneFormat = data.Get("item.output_format").String()
				doneResult = data.Get("item.result").String()
				imageDoneCount++
				if doneResult == "RFJBRlQiW0FHRS1QUkQ=" {
					thoughtLeaked = true
				}
			}
		case "response.completed":
			completedOutput = data.Get("response.output")
		}
	}
	if !sawImageAdded {
		t.Fatalf("expected response.output_item.added for image_generation_call; events: %q", string(eventsToString(events)))
	}
	if !sawImageDone {
		t.Fatalf("expected response.output_item.done for image_generation_call; events: %q", string(eventsToString(events)))
	}
	if addedFormat != "png" || doneFormat != "png" {
		t.Fatalf("output_format added=%q done=%q, want both png", addedFormat, doneFormat)
	}
	if doneResult != "RklOQUxfSU1BR0U=" {
		t.Fatalf("result = %q, want RklOQUxfSU1BR0U=", doneResult)
	}
	if imageDoneCount != 1 {
		t.Fatalf("expected exactly 1 image_generation_call done event, got %d", imageDoneCount)
	}
	if thoughtLeaked {
		t.Fatalf("thought image data RFJBRlQiW0FHRS1QUkQ= must not appear")
	}
	img := completedOutput.Get("#(type==\"image_generation_call\")")
	if !img.Exists() {
		t.Fatalf("expected image_generation_call in response.completed output: %q", string(eventsToString(events)))
	}
	if got := img.Get("result").String(); got != "RklOQUxfSU1BR0U=" {
		t.Fatalf("response.completed image result = %q, want RklOQUxfSU1BR0U=", got)
	}
}

// TestConvertGeminiInlineDataImagePartNonStream7569 is the non-stream twin of
// TestConvertGeminiInlineDataImagePart7569.
func TestConvertGeminiInlineDataImagePartNonStream7569(t *testing.T) {
	raw := []byte(`{"candidates":[{"content":{"role":"model","parts":[{"inlineData":{"mimeType":"image/png","data":"RklOQUxfSU1BR0U="}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":1120,"totalTokenCount":1123},"modelVersion":"gemini-3.1-flash-image","responseId":"resp_7569"}`)
	result := ConvertGeminiResponseToOpenAIResponsesNonStream(context.Background(), "gemini-3.1-flash-image", nil, nil, raw, nil)
	img := ojson.GetBytesNoCopy(result, "output.#(type==\"image_generation_call\")")
	if !img.Exists() {
		t.Fatalf("expected image_generation_call in non-stream output: %s", string(result))
	}
	if got := img.Get("result").String(); got != "RklOQUxfSU1BR0U=" {
		t.Fatalf("result = %q, want RklOQUxfSU1BR0U=; output: %s", got, string(result))
	}
	if got := img.Get("output_format").String(); got != "png" {
		t.Fatalf("output_format = %q, want png; output: %s", got, string(result))
	}
}

// TestConvertGeminiInlineDataNonImage7569 ensures non-image inlineData
// (e.g. audio) is not misclassified as a PNG image_generation_call.
func TestConvertGeminiInlineDataNonImage7569(t *testing.T) {
	raw := []byte(`{"candidates":[{"content":{"role":"model","parts":[{"inlineData":{"mimeType":"audio/pcm","data":"AAAA"}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":10,"totalTokenCount":13},"modelVersion":"gemini-test","responseId":"resp_7569b"}`)
	result := ConvertGeminiResponseToOpenAIResponsesNonStream(context.Background(), "gemini-test", nil, nil, raw, nil)
	img := ojson.GetBytesNoCopy(result, "output.#(type==\"image_generation_call\")")
	if img.Exists() {
		t.Fatalf("audio/pcm inlineData must not be emitted as image_generation_call; output: %s", string(result))
	}
}

// TestConvertGeminiInlineDataImagePreservesSignature7569 ensures an image
// part carrying a thought signature still preserves the signature carrier.
func TestConvertGeminiInlineDataImagePreservesSignature7569(t *testing.T) {
	sig := "EjQKDg==" // synthetic thought signature
	raw := []byte(`{"candidates":[{"content":{"role":"model","parts":[{"inlineData":{"mimeType":"image/jpeg","data":"Ymll"},"thoughtSignature":"` + sig + `"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":100,"totalTokenCount":103},"modelVersion":"gemini-3-pro-image","responseId":"resp_7569c"}`)
	result := ConvertGeminiResponseToOpenAIResponsesNonStream(context.Background(), "gemini-3-pro-image", nil, nil, raw, nil)
	img := ojson.GetBytesNoCopy(result, "output.#(type==\"image_generation_call\")")
	if !img.Exists() {
		t.Fatalf("expected image_generation_call in non-stream output: %s", string(result))
	}
	if got := img.Get("output_format").String(); got != "jpeg" {
		t.Fatalf("output_format = %q, want jpeg; output: %s", got, string(result))
	}
	reasoning := ojson.GetBytesNoCopy(result, "output.#(type==\"reasoning\")")
	if !reasoning.Exists() {
		t.Fatalf("expected reasoning carrier to preserve thought signature; output: %s", string(result))
	}
}

func eventsToString(events [][]byte) []byte {
	var out []byte
	for _, e := range events {
		out = append(out, e...)
		out = append(out, '\n')
	}
	return out
}
