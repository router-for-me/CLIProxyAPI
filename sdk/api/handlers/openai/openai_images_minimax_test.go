package openai

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestBuildMiniMaxImageGenerationRequest(t *testing.T) {
	raw := []byte(`{"model":"image-01","prompt":"draw","response_format":"b64_json","size":"1024x1536","n":2,"seed":42,"prompt_optimizer":true}`)

	payload, responseFormat, err := buildMiniMaxImageGenerationRequest(raw, "image-01", "draw", nil)
	if err != nil {
		t.Fatalf("buildMiniMaxImageGenerationRequest() error = %v", err)
	}
	if responseFormat != "b64_json" {
		t.Fatalf("responseFormat = %q, want b64_json", responseFormat)
	}
	if got := gjson.GetBytes(payload, "model").String(); got != "image-01" {
		t.Fatalf("model = %q, want image-01", got)
	}
	if got := gjson.GetBytes(payload, "response_format").String(); got != "base64" {
		t.Fatalf("response_format = %q, want base64", got)
	}
	if got := gjson.GetBytes(payload, "aspect_ratio").String(); got != "2:3" {
		t.Fatalf("aspect_ratio = %q, want 2:3", got)
	}
	if got := gjson.GetBytes(payload, "n").Int(); got != 2 {
		t.Fatalf("n = %d, want 2", got)
	}
	if got := gjson.GetBytes(payload, "seed").Int(); got != 42 {
		t.Fatalf("seed = %d, want 42", got)
	}
	if got := gjson.GetBytes(payload, "prompt_optimizer").Bool(); !got {
		t.Fatalf("prompt_optimizer = false, want true")
	}
}

func TestBuildMiniMaxImageEditRequestBuildsSubjectReference(t *testing.T) {
	raw := []byte(`{"model":"image-01-live","prompt":"edit","response_format":"url","aspect_ratio":"16:9"}`)

	payload, responseFormat, err := buildMiniMaxImageGenerationRequest(raw, "image-01-live", "edit", []string{"https://example.com/a.png", "data:image/png;base64,abc"})
	if err != nil {
		t.Fatalf("buildMiniMaxImageGenerationRequest() error = %v", err)
	}
	if responseFormat != "url" {
		t.Fatalf("responseFormat = %q, want url", responseFormat)
	}
	if got := gjson.GetBytes(payload, "response_format").String(); got != "url" {
		t.Fatalf("response_format = %q, want url", got)
	}
	if got := gjson.GetBytes(payload, "subject_reference.#").Int(); got != 2 {
		t.Fatalf("subject_reference length = %d, want 2", got)
	}
	if got := gjson.GetBytes(payload, "subject_reference.0.type").String(); got != "character" {
		t.Fatalf("subject_reference.0.type = %q, want character", got)
	}
	if got := gjson.GetBytes(payload, "subject_reference.1.image_file").String(); got != "data:image/png;base64,abc" {
		t.Fatalf("subject_reference.1.image_file = %q", got)
	}
}

func TestBuildMiniMaxImageRequestPreservesExplicitSubjectReference(t *testing.T) {
	raw := []byte(`{"model":"image-01-live","prompt":"edit","subject_reference":[{"type":"product","image_file":"https://example.com/product.png"}]}`)

	payload, _, err := buildMiniMaxImageGenerationRequest(raw, "image-01-live", "edit", []string{"https://example.com/ignored.png"})
	if err != nil {
		t.Fatalf("buildMiniMaxImageGenerationRequest() error = %v", err)
	}
	if got := gjson.GetBytes(payload, "subject_reference.#").Int(); got != 1 {
		t.Fatalf("subject_reference length = %d, want 1", got)
	}
	if got := gjson.GetBytes(payload, "subject_reference.0.type").String(); got != "product" {
		t.Fatalf("subject_reference.0.type = %q, want product", got)
	}
}

func TestBuildMiniMaxImageStreamEvents(t *testing.T) {
	body := []byte(`{"created":1,"data":[{"b64_json":"abc"},{"url":"https://example.com/a.png"}]}`)

	events, err := buildMiniMaxImageStreamEvents(body, "image_generation")
	if err != nil {
		t.Fatalf("buildMiniMaxImageStreamEvents() error = %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("events len = %d, want 2", len(events))
	}
	if got := gjson.GetBytes(events[0], "type").String(); got != "image_generation.completed" {
		t.Fatalf("event type = %q, want image_generation.completed", got)
	}
	if got := gjson.GetBytes(events[0], "b64_json").String(); got != "abc" {
		t.Fatalf("event b64_json = %q, want abc", got)
	}
	if got := gjson.GetBytes(events[1], "url").String(); got != "https://example.com/a.png" {
		t.Fatalf("event url = %q", got)
	}
}
