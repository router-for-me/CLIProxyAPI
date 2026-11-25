package helps

import (
	"testing"

	"github.com/MoeForge/moego/ojson"
)

func TestEnsureGeminiLeadingUserContent_AssistantRole(t *testing.T) {
	payload := []byte(`{"contents":[{"role":"assistant","parts":[{"text":"hi"}]}]}`)
	got := EnsureGeminiLeadingUserContent(payload, "contents")

	doc := ojson.NewDoc(got)
	if n := len(doc.Get("contents").Array()); n != 2 {
		t.Fatalf("expected 2 contents after prepending user turn for assistant role, got %d: %s", n, string(got))
	}
	if role := doc.Get("contents.0.role").String(); role != "user" {
		t.Fatalf("expected leading role user, got %q", role)
	}
	if role := doc.Get("contents.1.role").String(); role != "assistant" {
		t.Fatalf("expected second role assistant, got %q", role)
	}
}

func TestEnsureGeminiTrailingUserContent_SnakeCaseFunctionResponse(t *testing.T) {
	payload := []byte(`{"contents":[{"role":"user","parts":[{"text":"run tool"}]},{"role":"model","parts":[{"function_response":{"name":"get_weather","response":{"ok":true}}}]}]}`)
	got := EnsureGeminiTrailingUserContent(payload, "contents")

	doc := ojson.NewDoc(got)
	if n := len(doc.Get("contents").Array()); n != 2 {
		t.Fatalf("expected trailing snake_case function_response turn to be preserved without appending user turn, got %d turns: %s", n, string(got))
	}
}

func TestEnsureGeminiTrailingUserContent_SnakeCaseFunctionResponseMedia(t *testing.T) {
	cases := []struct {
		name      string
		mediaPart string
	}{
		{name: "inline_data", mediaPart: `{"inline_data":{"mime_type":"image/png","data":"AA=="}}`},
		{name: "file_data", mediaPart: `{"file_data":{"mime_type":"image/png","file_uri":"gs://bucket/img.png"}}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			payload := []byte(`{"contents":[{"role":"user","parts":[{"text":"inspect"}]},{"role":"model","parts":[{"function_response":{"name":"screenshot","response":{"ok":true},"parts":[` + tc.mediaPart + `]}}]}]}`)
			inputDoc := ojson.NewDoc(payload)
			if !contentHasFunctionResponseMedia(inputDoc.Get("contents.1")) {
				t.Fatalf("expected contentHasFunctionResponseMedia to detect nested %s under function_response.parts", tc.name)
			}

			got := EnsureGeminiTrailingUserContent(payload, "contents")

			doc := ojson.NewDoc(got)
			if n := len(doc.Get("contents").Array()); n != 3 {
				t.Fatalf("expected synthetic trailing user turn when snake_case function_response carries media, got %d turns: %s", n, string(got))
			}
			if role := doc.Get("contents.2.role").String(); role != "user" {
				t.Fatalf("expected appended trailing role user, got %q", role)
			}
		})
	}
}

func TestDropGeminiTrailingThoughtOnlyContent_SnakeCaseToolParts(t *testing.T) {
	cases := []struct {
		name string
		part string
	}{
		{name: "function_call", part: `{"thought":true,"function_call":{"name":"search","args":{"q":"go"}}}`},
		{name: "function_response", part: `{"thought":true,"function_response":{"name":"search","response":{"ok":true}}}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			payload := []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]},{"role":"model","parts":[` + tc.part + `]}]}`)
			got := DropGeminiTrailingThoughtOnlyContent(payload, "contents")

			doc := ojson.NewDoc(got)
			if n := len(doc.Get("contents").Array()); n != 2 {
				t.Fatalf("expected trailing model turn with snake_case %s to be preserved, got %d turns: %s", tc.name, n, string(got))
			}
		})
	}
}
