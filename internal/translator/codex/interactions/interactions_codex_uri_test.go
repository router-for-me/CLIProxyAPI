package interactions

import (
	"context"
	"errors"
	"testing"

	translatorcommon "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/common"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

const interactionsCodexHistory = `{"type":"user_input","content":[{"type":"text","text":"a"}]},{"type":"model_output","content":[{"type":"text","text":"b"}]}`

func interactionsToCodexEnvelope(input string) sdktranslator.RequestEnvelope {
	return sdktranslator.TranslateRequestEnvelope(context.Background(), sdktranslator.FormatInteractions, sdktranslator.FormatCodex, sdktranslator.RequestEnvelope{
		Format: sdktranslator.FormatInteractions,
		Model:  "m",
		Body:   []byte(input),
	})
}

// interactionsFinalTurnToCodex translates two history turns followed by a final
// user step holding the given content parts.
func interactionsFinalTurnToCodex(finalContent string) sdktranslator.RequestEnvelope {
	return interactionsToCodexEnvelope(`{"model":"m","input":[` + interactionsCodexHistory + `,{"type":"user_input","content":[` + finalContent + `]}]}`)
}

func requireInteractionsToCodexRefusal(t *testing.T, envelope sdktranslator.RequestEnvelope, wantType string) {
	t.Helper()
	var unsupported *translatorcommon.UnsupportedPartError
	if !errors.As(envelope.Err, &unsupported) || unsupported.Type != wantType || unsupported.StatusCode() != 400 {
		t.Fatalf("envelope.Err = %v, want unsupported content part: %s; body = %s", envelope.Err, wantType, envelope.Body)
	}
	if got, want := envelope.Err.Error(), "unsupported content part: "+wantType; got != want {
		t.Fatalf("error text = %q, want %q", got, want)
	}
	if !gjson.ValidBytes(envelope.Body) {
		t.Fatalf("body is not JSON: %q", envelope.Body)
	}
}

// TestInteractionsToCodexKeepsTheMediaGeminiToInteractionsWrites feeds the part
// shape that the Gemini to Interactions translation emits for a fileData part.
func TestInteractionsToCodexKeepsTheMediaGeminiToInteractionsWrites(t *testing.T) {
	envelope := interactionsFinalTurnToCodex(`{"type":"image","uri":"gs://b/a.png","mime_type":"image/png"}`)
	if envelope.Err != nil {
		t.Fatalf("envelope.Err = %v, body = %s", envelope.Err, envelope.Body)
	}
	if got := gjson.GetBytes(envelope.Body, "input.#").Int(); got != 3 {
		t.Fatalf("input has %d items, want 3: %s", got, envelope.Body)
	}
	part := gjson.GetBytes(envelope.Body, "input.2.content.0")
	if part.Get("type").String() != "input_image" || part.Get("image_url").String() != "gs://b/a.png" {
		t.Fatalf("last turn lost the file: %s", envelope.Body)
	}
}

func TestInteractionsToCodexReadsURIAsAFileReference(t *testing.T) {
	for _, field := range []string{"uri", "file_uri", "fileUri", "url"} {
		t.Run("image "+field, func(t *testing.T) {
			envelope := interactionsFinalTurnToCodex(`{"type":"image","mime_type":"image/png","` + field + `":"https://example.test/a.png"}`)
			if envelope.Err != nil {
				t.Fatalf("envelope.Err = %v, body = %s", envelope.Err, envelope.Body)
			}
			part := gjson.GetBytes(envelope.Body, "input.2.content.0")
			if part.Get("type").String() != "input_image" || part.Get("image_url").String() != "https://example.test/a.png" {
				t.Fatalf("part = %s, body = %s", part.Raw, envelope.Body)
			}
		})
		t.Run("document "+field, func(t *testing.T) {
			envelope := interactionsFinalTurnToCodex(`{"type":"document","mime_type":"application/pdf","` + field + `":"https://example.test/a.pdf"}`)
			if envelope.Err != nil {
				t.Fatalf("envelope.Err = %v, body = %s", envelope.Err, envelope.Body)
			}
			part := gjson.GetBytes(envelope.Body, "input.2.content.0")
			if part.Get("type").String() != "input_file" || part.Get("file_url").String() != "https://example.test/a.pdf" {
				t.Fatalf("part = %s, body = %s", part.Raw, envelope.Body)
			}
		})
	}
}

func TestInteractionsToCodexInlineDataStillConverts(t *testing.T) {
	image := interactionsFinalTurnToCodex(`{"type":"image","mime_type":"image/png","data":"aGVsbG8="}`)
	if image.Err != nil {
		t.Fatalf("envelope.Err = %v, body = %s", image.Err, image.Body)
	}
	if got := gjson.GetBytes(image.Body, "input.2.content.0.image_url").String(); got != "data:image/png;base64,aGVsbG8=" {
		t.Fatalf("image_url = %q, body = %s", got, image.Body)
	}
	audio := interactionsFinalTurnToCodex(`{"type":"audio","mime_type":"audio/wav","data":"UklGRg=="}`)
	if audio.Err != nil {
		t.Fatalf("envelope.Err = %v, body = %s", audio.Err, audio.Body)
	}
	if got := gjson.GetBytes(audio.Body, "input.2.content.0.input_audio.data").String(); got != "UklGRg==" {
		t.Fatalf("input_audio = %s, body = %s", gjson.GetBytes(audio.Body, "input.2.content.0").Raw, audio.Body)
	}
}

func TestInteractionsToCodexUnrepresentableAttachmentOnlyTurnIsRefused(t *testing.T) {
	cases := []struct {
		name     string
		content  string
		wantType string
	}{
		{name: "audio by uri", content: `{"type":"audio","mime_type":"audio/wav","uri":"gs://b/a.wav"}`, wantType: "audio"},
		{name: "image without anything", content: `{"type":"image"}`, wantType: "image"},
		{name: "document without anything", content: `{"type":"document","mime_type":"application/pdf"}`, wantType: "document"},
		{name: "empty text does not hide it", content: `{"type":"text","text":""},{"type":"audio","mime_type":"audio/wav"}`, wantType: "audio"},
		{name: "whitespace text does not hide it", content: `{"type":"text","text":" \n"},{"type":"audio","mime_type":"audio/wav"}`, wantType: "audio"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			requireInteractionsToCodexRefusal(t, interactionsFinalTurnToCodex(tc.content), tc.wantType)
		})
	}
}

func TestInteractionsToCodexRefusesAnEmptiedTurnBeforeALaterTextTurn(t *testing.T) {
	envelope := interactionsToCodexEnvelope(`{"model":"m","input":[{"type":"user_input","content":[{"type":"audio","mime_type":"audio/wav"}]},{"type":"model_output","content":[{"type":"text","text":"ok"}]},{"type":"user_input","content":[{"type":"text","text":"next"}]}]}`)
	requireInteractionsToCodexRefusal(t, envelope, "audio")
}

// An instruction step names itself by role or by type alone. It closes the
// emptied user turn and must not stand in for the content that turn lost.
func TestInteractionsToCodexInstructionStepDoesNotHideAnEmptiedTurn(t *testing.T) {
	const emptied = `{"type":"user_input","content":[{"type":"audio","mime_type":"audio/wav","uri":"gs://b/a.wav"}]}`
	cases := map[string]string{
		"role developer":      `{"type":"user_input","role":"developer","content":[{"type":"text","text":"note"}]}`,
		"role system":         `{"type":"user_input","role":"system","content":[{"type":"text","text":"note"}]}`,
		"type developer":      `{"type":"developer","content":[{"type":"text","text":"note"}]}`,
		"type system":         `{"type":"system","content":[{"type":"text","text":"note"}]}`,
		"type system wrapper": `{"type":"system","steps":[{"type":"user_input","content":[{"type":"text","text":"note"}]}]}`,
	}
	for name, instruction := range cases {
		t.Run(name, func(t *testing.T) {
			envelope := interactionsToCodexEnvelope(`{"model":"m","input":[` + interactionsCodexHistory + `,` + emptied + `,` + instruction + `]}`)
			requireInteractionsToCodexRefusal(t, envelope, "audio")
		})
	}
}

// An instruction step is still sent, as a developer message, when nothing was lost.
func TestInteractionsToCodexTypeOnlyInstructionStepIsSentAsDeveloper(t *testing.T) {
	for _, stepType := range []string{"system", "developer"} {
		t.Run(stepType, func(t *testing.T) {
			envelope := interactionsToCodexEnvelope(`{"model":"m","input":[` + interactionsCodexHistory + `,{"type":"` + stepType + `","content":[{"type":"text","text":"note"}]}]}`)
			if envelope.Err != nil {
				t.Fatalf("envelope.Err = %v, body = %s", envelope.Err, envelope.Body)
			}
			last := gjson.GetBytes(envelope.Body, "input.2")
			if last.Get("role").String() != "developer" || last.Get("content.0.text").String() != "note" {
				t.Fatalf("last item = %s, body = %s", last.Raw, envelope.Body)
			}
		})
	}
}

func TestInteractionsToCodexUnrepresentableAttachmentBesideTextStillSucceeds(t *testing.T) {
	envelope := interactionsFinalTurnToCodex(`{"type":"text","text":"keep me"},{"type":"audio","mime_type":"audio/wav"}`)
	if envelope.Err != nil {
		t.Fatalf("envelope.Err = %v, body = %s", envelope.Err, envelope.Body)
	}
	if got := gjson.GetBytes(envelope.Body, "input.2.content.0.text").String(); got != "keep me" {
		t.Fatalf("text was lost: %s", envelope.Body)
	}
	if got := gjson.GetBytes(envelope.Body, "input.#").Int(); got != 3 {
		t.Fatalf("input has %d items, want 3: %s", got, envelope.Body)
	}
}

func TestInteractionsToCodexNeighbouringStepKeepsTheTurnAlive(t *testing.T) {
	cases := map[string]string{
		"text step":     `{"type":"user_input","content":[{"type":"text","text":"keep me"}]},{"type":"user_input","content":[{"type":"audio","mime_type":"audio/wav"}]}`,
		"tool result":   `{"type":"function_call","name":"f","call_id":"c1","arguments":{}},{"type":"function_result","name":"f","call_id":"c1","result":{"ok":true}},{"type":"user_input","content":[{"type":"audio","mime_type":"audio/wav"}]}`,
		"string step":   `"keep me",{"type":"user_input","content":[{"type":"audio","mime_type":"audio/wav"}]}`,
		"turn of steps": `{"role":"user","steps":[{"type":"user_input","content":[{"type":"text","text":"keep me"}]},{"type":"user_input","content":[{"type":"audio","mime_type":"audio/wav"}]}]}`,
	}
	for name, steps := range cases {
		t.Run(name, func(t *testing.T) {
			envelope := interactionsToCodexEnvelope(`{"model":"m","input":[` + interactionsCodexHistory + `,` + steps + `]}`)
			if envelope.Err != nil {
				t.Fatalf("envelope.Err = %v, body = %s", envelope.Err, envelope.Body)
			}
		})
	}
}

func TestInteractionsToCodexAssistantMediaIsNotAUserTurn(t *testing.T) {
	envelope := interactionsToCodexEnvelope(`{"model":"m","input":[{"type":"user_input","content":[{"type":"text","text":"a"}]},{"type":"model_output","content":[{"type":"audio","mime_type":"audio/wav"}]},{"type":"user_input","content":[{"type":"text","text":"c"}]}]}`)
	if envelope.Err != nil {
		t.Fatalf("envelope.Err = %v, body = %s", envelope.Err, envelope.Body)
	}
}

func TestInteractionsToCodexExportedWrapperKeepsAJSONBody(t *testing.T) {
	input := []byte(`{"model":"m","input":[` + interactionsCodexHistory + `,{"type":"user_input","content":[{"type":"audio","mime_type":"audio/wav"}]}]}`)
	if body, _ := ConvertInteractionsRequestToCodex("m", input, false); !gjson.ValidBytes(body) {
		t.Fatalf("body is not JSON: %q", body)
	}
}
