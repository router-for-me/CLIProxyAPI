package interactions

import (
	"context"
	"errors"
	"testing"

	translatorcommon "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/common"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

const interactionsGeminiHistory = `{"type":"user_input","content":[{"type":"text","text":"a"}]},{"type":"model_output","content":[{"type":"text","text":"b"}]}`

func interactionsToGeminiEnvelope(input string) sdktranslator.RequestEnvelope {
	return sdktranslator.TranslateRequestEnvelope(context.Background(), sdktranslator.FormatInteractions, sdktranslator.FormatGemini, sdktranslator.RequestEnvelope{
		Format: sdktranslator.FormatInteractions,
		Model:  "m",
		Body:   []byte(input),
	})
}

// interactionsFinalTurnToGemini translates two history turns followed by a final
// user step holding the given content parts.
func interactionsFinalTurnToGemini(finalContent string) sdktranslator.RequestEnvelope {
	return interactionsToGeminiEnvelope(`{"model":"m","input":[` + interactionsGeminiHistory + `,{"type":"user_input","content":[` + finalContent + `]}]}`)
}

func requireInteractionsToGeminiRefusal(t *testing.T, envelope sdktranslator.RequestEnvelope, wantType string) {
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

func TestGeminiFileDataSurvivesTheRoundTripThroughInteractions(t *testing.T) {
	interactions := geminiTurnToInteractions("", `{"fileData":{"mimeType":"image/png","fileUri":"gs://b/a.png"}}`)
	if interactions.Err != nil {
		t.Fatalf("Gemini to Interactions: %v, body = %s", interactions.Err, interactions.Body)
	}
	if got := gjson.GetBytes(interactions.Body, "input.2.content.0.uri").String(); got != "gs://b/a.png" {
		t.Fatalf("Interactions part = %s", gjson.GetBytes(interactions.Body, "input.2.content.0").Raw)
	}
	envelope := interactionsToGeminiEnvelope(string(interactions.Body))
	if envelope.Err != nil {
		t.Fatalf("Interactions to Gemini: %v, body = %s", envelope.Err, envelope.Body)
	}
	if got := gjson.GetBytes(envelope.Body, "contents.#").Int(); got != 3 {
		t.Fatalf("contents = %d, want 3: %s", got, envelope.Body)
	}
	fileData := gjson.GetBytes(envelope.Body, "contents.2.parts.0.fileData")
	if fileData.Get("fileUri").String() != "gs://b/a.png" || fileData.Get("mimeType").String() != "image/png" {
		t.Fatalf("last turn lost the file: %s", envelope.Body)
	}
}

func TestInteractionsToGeminiReadsURIAsFileURI(t *testing.T) {
	for _, field := range []string{"uri", "file_uri", "fileUri"} {
		t.Run(field, func(t *testing.T) {
			envelope := interactionsFinalTurnToGemini(`{"type":"document","mime_type":"application/pdf","` + field + `":"gs://b/a.pdf"}`)
			if envelope.Err != nil {
				t.Fatalf("envelope.Err = %v, body = %s", envelope.Err, envelope.Body)
			}
			fileData := gjson.GetBytes(envelope.Body, "contents.2.parts.0.fileData")
			if fileData.Get("fileUri").String() != "gs://b/a.pdf" || fileData.Get("mimeType").String() != "application/pdf" {
				t.Fatalf("fileData = %s, body = %s", fileData.Raw, envelope.Body)
			}
		})
	}
}

func TestInteractionsToGeminiInlineDataStillConverts(t *testing.T) {
	envelope := interactionsFinalTurnToGemini(`{"type":"image","mime_type":"image/png","data":"aGVsbG8="}`)
	if envelope.Err != nil {
		t.Fatalf("envelope.Err = %v, body = %s", envelope.Err, envelope.Body)
	}
	inlineData := gjson.GetBytes(envelope.Body, "contents.2.parts.0.inlineData")
	if inlineData.Get("mimeType").String() != "image/png" || inlineData.Get("data").String() != "aGVsbG8=" {
		t.Fatalf("inlineData = %s, body = %s", inlineData.Raw, envelope.Body)
	}
}

func TestInteractionsToGeminiUnrepresentableAttachmentOnlyTurnIsRefused(t *testing.T) {
	cases := []struct {
		name     string
		content  string
		wantType string
	}{
		{name: "audio without bytes or uri", content: `{"type":"audio","mime_type":"audio/wav"}`, wantType: "audio"},
		{name: "image without anything", content: `{"type":"image"}`, wantType: "image"},
		{name: "uri without a mime type", content: `{"type":"document","uri":"gs://b/a.pdf"}`, wantType: "document"},
		{name: "image_url that is not a data url", content: `{"type":"image_url","image_url":{"url":"https://example.test/a.png"}}`, wantType: "image_url"},
		{name: "empty text does not hide it", content: `{"type":"text","text":""},{"type":"video","mime_type":"video/mp4"}`, wantType: "video"},
		{name: "whitespace text does not hide it", content: `{"type":"text","text":" \n"},{"type":"audio","mime_type":"audio/wav"}`, wantType: "audio"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			requireInteractionsToGeminiRefusal(t, interactionsFinalTurnToGemini(tc.content), tc.wantType)
		})
	}
}

func TestInteractionsToGeminiUnrepresentableNativePartIsRefused(t *testing.T) {
	envelope := interactionsToGeminiEnvelope(`{"model":"m","input":[` + interactionsGeminiHistory + `,{"role":"user","parts":[{"fileData":{"mimeType":"image/png"}}]}]}`)
	requireInteractionsToGeminiRefusal(t, envelope, "fileData")
}

func TestInteractionsToGeminiRefusesAnEmptiedTurnBeforeALaterTextTurn(t *testing.T) {
	envelope := interactionsToGeminiEnvelope(`{"model":"m","input":[{"type":"user_input","content":[{"type":"audio","mime_type":"audio/wav"}]},{"type":"model_output","content":[{"type":"text","text":"ok"}]},{"type":"user_input","content":[{"type":"text","text":"next"}]}]}`)
	requireInteractionsToGeminiRefusal(t, envelope, "audio")
}

func TestInteractionsToGeminiUnrepresentableAttachmentBesideTextStillSucceeds(t *testing.T) {
	envelope := interactionsFinalTurnToGemini(`{"type":"text","text":"keep me"},{"type":"audio","mime_type":"audio/wav"}`)
	if envelope.Err != nil {
		t.Fatalf("envelope.Err = %v, body = %s", envelope.Err, envelope.Body)
	}
	if got := gjson.GetBytes(envelope.Body, "contents.2.parts.0.text").String(); got != "keep me" {
		t.Fatalf("text was lost: %s", envelope.Body)
	}
	if got := gjson.GetBytes(envelope.Body, "contents.2.parts.#").Int(); got != 1 {
		t.Fatalf("last turn has %d parts, want 1: %s", got, envelope.Body)
	}
}

func TestInteractionsToGeminiNeighbouringStepKeepsTheTurnAlive(t *testing.T) {
	cases := map[string]string{
		"text step":     `{"type":"user_input","content":[{"type":"text","text":"keep me"}]},{"type":"user_input","content":[{"type":"audio","mime_type":"audio/wav"}]}`,
		"tool result":   `{"type":"function_call","name":"f","call_id":"c1","arguments":{}},{"type":"function_result","name":"f","call_id":"c1","result":{"ok":true}},{"type":"user_input","content":[{"type":"audio","mime_type":"audio/wav"}]}`,
		"string step":   `"keep me",{"type":"user_input","content":[{"type":"audio","mime_type":"audio/wav"}]}`,
		"native step":   `{"role":"user","parts":[{"text":"keep me"}]},{"type":"user_input","content":[{"type":"audio","mime_type":"audio/wav"}]}`,
		"turn of steps": `{"role":"user","steps":[{"type":"user_input","content":[{"type":"text","text":"keep me"}]},{"type":"user_input","content":[{"type":"audio","mime_type":"audio/wav"}]}]}`,
	}
	for name, steps := range cases {
		t.Run(name, func(t *testing.T) {
			envelope := interactionsToGeminiEnvelope(`{"model":"m","input":[` + interactionsGeminiHistory + `,` + steps + `]}`)
			if envelope.Err != nil {
				t.Fatalf("envelope.Err = %v, body = %s", envelope.Err, envelope.Body)
			}
		})
	}
}

func TestInteractionsToGeminiExportedWrapperKeepsAJSONBody(t *testing.T) {
	input := []byte(`{"model":"m","input":[` + interactionsGeminiHistory + `,{"type":"user_input","content":[{"type":"audio","mime_type":"audio/wav"}]}]}`)
	if body, _ := ConvertInteractionsRequestToGemini("m", input, false); !gjson.ValidBytes(body) {
		t.Fatalf("body is not JSON: %q", body)
	}
}
