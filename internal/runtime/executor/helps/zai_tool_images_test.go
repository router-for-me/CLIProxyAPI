package helps

import (
	"bytes"
	"testing"

	"github.com/tidwall/gjson"
)

const zaiTestURL = "https://api.z.ai/api/v1/"

func TestNormalizeZAIToolOutputImagesMovesMixedOutputAndPreservesUnknownParts(t *testing.T) {
	body := []byte(`{"input":[{"type":"function_call_output","call_id":"call_1","output":[{"type":"input_text","text":"keep"},{"type":"input_image","image_url":"data:image/png;base64,ONE","detail":"original"},{"type":"unknown","value":{"keep":true}},{"type":"input_image","image_url":12}]}]}`)
	out := NormalizeZAIToolOutputImages("glm-5.3-flash", zaiTestURL, body)
	input := gjson.GetBytes(out, "input")
	if got := len(input.Array()); got != 2 {
		t.Fatalf("input items = %d, want 2: %s", got, out)
	}
	if got := input.Get("0.call_id").String(); got != "call_1" {
		t.Fatalf("call_id = %q", got)
	}
	if got := input.Get("0.output.0.text").String(); got != "keep" {
		t.Fatalf("tool text = %q", got)
	}
	if got := input.Get("0.output.1.value.keep").Bool(); !got {
		t.Fatalf("unknown part changed: %s", input.Get("0.output.1").Raw)
	}
	if got := input.Get("0.output.2.image_url").Int(); got != 12 {
		t.Fatalf("unsupported image part changed: %d", got)
	}
	if got := input.Get("0.output.3.text").String(); got != zaiToolImageReference {
		t.Fatalf("tool reference = %q", got)
	}
	if got := input.Get("1.content.0.text").String(); got != zaiToolImageMarker {
		t.Fatalf("marker = %q", got)
	}
	if got := input.Get("1.content.1.image_url").String(); got != "data:image/png;base64,ONE" {
		t.Fatalf("relocated image = %q", got)
	}
	if got := input.Get("1.content.1.detail").String(); got != "original" {
		t.Fatalf("detail = %q", got)
	}
	if got := bytes.Count(out, []byte("data:image/png;base64,ONE")); got != 1 {
		t.Fatalf("image appears %d times: %s", got, out)
	}
}

func TestNormalizeZAIToolOutputImagesPreservesStringOutputAndIsIdempotent(t *testing.T) {
	body := []byte(`{"input":[{"type":"function_call_output","call_id":"one","output":"[{\"type\":\"input_text\",\"text\":\"keep\"},{\"type\":\"input_image\",\"image_url\":\"data:image/png;base64,TWO\",\"detail\":\"high\"}]"}]}`)
	out := NormalizeZAIToolOutputImages("glm-5.3-flash", zaiTestURL, body)
	if got := gjson.GetBytes(out, "input.0.output").Type; got != gjson.String {
		t.Fatalf("output type = %v, want string", got)
	}
	stringOutput := gjson.Parse(gjson.GetBytes(out, "input.0.output").String())
	if got := stringOutput.Get("0.text").String(); got != "keep" {
		t.Fatalf("string text = %q", got)
	}
	if got := stringOutput.Get("1.text").String(); got != zaiToolImageReference {
		t.Fatalf("string reference = %q", got)
	}
	if again := NormalizeZAIToolOutputImages("glm-5.3-flash", zaiTestURL, out); !bytes.Equal(again, out) {
		t.Fatalf("normalization not idempotent: %s", again)
	}
}

func TestNormalizeZAIToolOutputImagesLeavesOtherScopesAndDirectImages(t *testing.T) {
	body := []byte(`{"input":[{"type":"message","role":"user","content":[{"type":"input_image","image_url":"data:image/png;base64,DIRECT"}]},{"type":"function_call_output","output":[{"type":"input_image","image_url":"data:image/png;base64,TOOL"}]}]}`)
	for _, scope := range [][2]string{{"glm-5.3", zaiTestURL}, {"glm-5.3-flash", "https://example.com/v1"}} {
		if got := NormalizeZAIToolOutputImages(scope[0], scope[1], body); !bytes.Equal(got, body) {
			t.Fatalf("scope %q %q changed body: %s", scope[0], scope[1], got)
		}
	}
	out := NormalizeZAIToolOutputImages("glm-5.3-flash", zaiTestURL, body)
	if got := gjson.GetBytes(out, "input.0.content.0.image_url").String(); got != "data:image/png;base64,DIRECT" {
		t.Fatalf("direct image changed: %q", got)
	}
}
