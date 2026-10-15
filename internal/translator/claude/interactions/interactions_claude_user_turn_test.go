package interactions

import (
	"context"
	"errors"
	"strings"
	"testing"

	translatorcommon "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/common"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

const (
	interactionsHistory = `{"type":"user_input","content":[{"type":"text","text":"a"}]},{"type":"model_output","content":[{"type":"text","text":"b"}]}`
	pdfByURI            = `{"type":"document","mime_type":"application/pdf","uri":"https://example.test/a.pdf"}`
	videoByURI          = `{"type":"video","mime_type":"video/mp4","uri":"https://example.test/a.mp4"}`
)

func interactionsToClaudeEnvelope(input string) sdktranslator.RequestEnvelope {
	return sdktranslator.TranslateRequestEnvelope(context.Background(), sdktranslator.FormatInteractions, sdktranslator.FormatClaude, sdktranslator.RequestEnvelope{
		Format: sdktranslator.FormatInteractions,
		Model:  "m",
		Body:   []byte(input),
	})
}

// interactionsFinalTurnToClaude translates two history turns followed by a final
// user step holding the given content parts.
func interactionsFinalTurnToClaude(finalContent string) sdktranslator.RequestEnvelope {
	return interactionsToClaudeEnvelope(`{"model":"m","input":[` + interactionsHistory + `,{"type":"user_input","content":[` + finalContent + `]}]}`)
}

func requireMediaRefusal(t *testing.T, envelope sdktranslator.RequestEnvelope, wantType string) {
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

func TestInteractionsToClaudeURIOnlyDocumentBecomesAURLSource(t *testing.T) {
	for _, field := range []string{"uri", "file_uri", "fileUri", "url"} {
		t.Run(field, func(t *testing.T) {
			envelope := interactionsFinalTurnToClaude(`{"type":"document","mime_type":"application/pdf","` + field + `":"https://example.test/a.pdf"}`)
			if envelope.Err != nil {
				t.Fatalf("envelope.Err = %v, body = %s", envelope.Err, envelope.Body)
			}
			if got := gjson.GetBytes(envelope.Body, "messages.#").Int(); got != 3 {
				t.Fatalf("messages = %d, want 3: %s", got, envelope.Body)
			}
			part := gjson.GetBytes(envelope.Body, "messages.2.content.0")
			if part.Get("type").String() != "document" || part.Get("source.type").String() != "url" || part.Get("source.url").String() != "https://example.test/a.pdf" {
				t.Fatalf("part = %s, body = %s", part.Raw, envelope.Body)
			}
		})
	}
}

func TestInteractionsToClaudeURIOnlyImageBecomesAURLSource(t *testing.T) {
	envelope := interactionsFinalTurnToClaude(`{"type":"image","mime_type":"image/png","uri":"https://example.test/a.png"}`)
	if envelope.Err != nil {
		t.Fatalf("envelope.Err = %v, body = %s", envelope.Err, envelope.Body)
	}
	part := gjson.GetBytes(envelope.Body, "messages.2.content.0")
	if part.Get("type").String() != "image" || part.Get("source.type").String() != "url" || part.Get("source.url").String() != "https://example.test/a.png" {
		t.Fatalf("part = %s, body = %s", part.Raw, envelope.Body)
	}
}

func TestInteractionsToClaudeInlineDocumentStaysBase64(t *testing.T) {
	envelope := interactionsFinalTurnToClaude(`{"type":"document","mime_type":"application/pdf","data":"JVBERi0xLjQK"}`)
	if envelope.Err != nil {
		t.Fatalf("envelope.Err = %v, body = %s", envelope.Err, envelope.Body)
	}
	part := gjson.GetBytes(envelope.Body, "messages.2.content.0")
	if part.Get("source.type").String() != "base64" || part.Get("source.media_type").String() != "application/pdf" || part.Get("source.data").String() != "JVBERi0xLjQK" {
		t.Fatalf("part = %s, body = %s", part.Raw, envelope.Body)
	}
}

func TestInteractionsToClaudeUnrepresentableMediaOnlyTurnIsRefused(t *testing.T) {
	cases := []struct {
		name     string
		part     string
		wantType string
	}{
		{name: "video by uri", part: videoByURI, wantType: "video"},
		{name: "audio by uri", part: `{"type":"audio","mime_type":"audio/wav","uri":"https://example.test/a.wav"}`, wantType: "audio"},
		{name: "document by gs uri", part: `{"type":"document","mime_type":"application/pdf","uri":"gs://bucket/a.pdf"}`, wantType: "document"},
		{name: "document by files api uri", part: `{"type":"document","mime_type":"application/pdf","uri":"files/abc123"}`, wantType: "document"},
		{name: "document with nothing", part: `{"type":"document","mime_type":"application/pdf"}`, wantType: "document"},
		{name: "file with nothing", part: `{"type":"file"}`, wantType: "document"},
		{name: "image by gs uri", part: `{"type":"image","mime_type":"image/png","uri":"gs://bucket/a.png"}`, wantType: "image"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			requireMediaRefusal(t, interactionsFinalTurnToClaude(tc.part), tc.wantType)
		})
	}
}

func TestInteractionsToClaudeRefusalIsNotHiddenBySystemInstructionOrHistory(t *testing.T) {
	requireMediaRefusal(t, interactionsToClaudeEnvelope(`{"model":"m","system_instruction":"be brief","input":[`+interactionsHistory+`,{"type":"user_input","content":[`+videoByURI+`]}]}`), "video")
}

func TestInteractionsToClaudeRefusesAnEmptiedTurnBeforeALaterTextTurn(t *testing.T) {
	envelope := interactionsToClaudeEnvelope(`{"model":"m","input":[{"type":"user_input","content":[` + videoByURI + `]},{"type":"model_output","content":[{"type":"text","text":"ok"}]},{"type":"user_input","content":[{"type":"text","text":"next"}]}]}`)
	requireMediaRefusal(t, envelope, "video")
}

func TestInteractionsToClaudeEmptyTextDoesNotHideAnUnrepresentableMedia(t *testing.T) {
	requireMediaRefusal(t, interactionsFinalTurnToClaude(`{"type":"text","text":""},`+videoByURI), "video")
}

func TestInteractionsToClaudeUnrepresentableMediaBesideTextStillSucceeds(t *testing.T) {
	envelope := interactionsFinalTurnToClaude(`{"type":"text","text":"keep me"},` + videoByURI)
	if envelope.Err != nil {
		t.Fatalf("envelope.Err = %v, body = %s", envelope.Err, envelope.Body)
	}
	content := gjson.GetBytes(envelope.Body, "messages.2.content").Array()
	if len(content) != 1 || content[0].Get("text").String() != "keep me" {
		t.Fatalf("text was lost or media leaked: %s", envelope.Body)
	}
}

func TestInteractionsToClaudeFlatContentInputKeepsTextBesideMedia(t *testing.T) {
	envelope := interactionsToClaudeEnvelope(`{"model":"m","input":[{"type":"text","text":"Describe this"},` + videoByURI + `]}`)
	if envelope.Err != nil {
		t.Fatalf("envelope.Err = %v, body = %s", envelope.Err, envelope.Body)
	}
	if got := gjson.GetBytes(envelope.Body, "messages.0.content.0.text").String(); got != "Describe this" {
		t.Fatalf("text was lost: %s", envelope.Body)
	}
}

func TestInteractionsToClaudeFlatContentInputConvertsAndRefusesBareMedia(t *testing.T) {
	envelope := interactionsToClaudeEnvelope(`{"model":"m","input":[` + interactionsHistory + `,` + pdfByURI + `]}`)
	if envelope.Err != nil {
		t.Fatalf("envelope.Err = %v, body = %s", envelope.Err, envelope.Body)
	}
	if got := gjson.GetBytes(envelope.Body, "messages.2.content.0.source.url").String(); got != "https://example.test/a.pdf" {
		t.Fatalf("bare document was lost: %s", envelope.Body)
	}
	requireMediaRefusal(t, interactionsToClaudeEnvelope(`{"model":"m","input":[`+interactionsHistory+`,`+videoByURI+`]}`), "video")
}

func TestInteractionsToClaudeNeighbouringUserStepsShareOneTurn(t *testing.T) {
	cases := map[string]string{
		"text step after the media step":    `{"model":"m","input":[` + interactionsHistory + `,{"type":"user_input","content":[` + videoByURI + `]},{"type":"user_input","content":[{"type":"text","text":"keep me"}]}]}`,
		"text step before the media step":   `{"model":"m","input":[` + interactionsHistory + `,{"type":"user_input","content":[{"type":"text","text":"keep me"}]},{"type":"user_input","content":[` + videoByURI + `]}]}`,
		"tool result beside the media step": `{"model":"m","input":[{"type":"user_input","content":[{"type":"text","text":"a"}]},{"type":"function_call","name":"lookup","call_id":"toolu_1","arguments":{}},{"type":"function_result","name":"lookup","call_id":"toolu_1","result":{"ok":true}},{"type":"user_input","content":[` + videoByURI + `]}]}`,
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			envelope := interactionsToClaudeEnvelope(input)
			if envelope.Err != nil {
				t.Fatalf("envelope.Err = %v, body = %s", envelope.Err, envelope.Body)
			}
		})
	}
}

func TestInteractionsToClaudeAssistantStepEndsTheUserTurn(t *testing.T) {
	envelope := interactionsToClaudeEnvelope(`{"model":"m","input":[{"type":"user_input","content":[{"type":"text","text":"a"}]},{"type":"user_input","content":[` + videoByURI + `]},{"type":"model_output","content":[{"type":"text","text":"b"}]},{"type":"user_input","content":[{"type":"text","text":"c"}]}]}`)
	if envelope.Err != nil {
		t.Fatalf("a text step in the same turn keeps it alive, envelope.Err = %v, body = %s", envelope.Err, envelope.Body)
	}
	envelope = interactionsToClaudeEnvelope(`{"model":"m","input":[{"type":"user_input","content":[` + videoByURI + `]},{"type":"model_output","content":[{"type":"text","text":"b"}]},{"type":"user_input","content":[{"type":"text","text":"c"}]}]}`)
	requireMediaRefusal(t, envelope, "video")
}

func TestInteractionsToClaudeExportedWrapperKeepsAJSONBody(t *testing.T) {
	input := []byte(`{"model":"m","input":[{"type":"user_input","content":[` + videoByURI + `]}]}`)
	if body, _ := ConvertInteractionsRequestToClaude("m", input, false); !gjson.ValidBytes(body) {
		t.Fatalf("body is not JSON: %q", body)
	}
}

const (
	inlineAudio = `{"type":"audio","mime_type":"audio/wav","data":"UklGRg=="}`
	inlineVideo = `{"type":"video","mime_type":"video/mp4","data":"AAAAIGZ0eXA="}`
)

func TestInteractionsToClaudeInlineAudioOrVideoOnlyUserTurnIsRefused(t *testing.T) {
	cases := []struct {
		name     string
		content  string
		wantType string
	}{
		{name: "audio", content: inlineAudio, wantType: "audio"},
		{name: "video", content: inlineVideo, wantType: "video"},
		{name: "empty text does not hide audio", content: `{"type":"text","text":""},` + inlineAudio, wantType: "audio"},
		{name: "whitespace text does not hide video", content: `{"type":"text","text":" \n"},` + inlineVideo, wantType: "video"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			envelope := interactionsFinalTurnToClaude(tc.content)
			requireMediaRefusal(t, envelope, tc.wantType)
			if strings.Contains(string(envelope.Body), "omitted") {
				t.Fatalf("a placeholder was synthesized for a user attachment: %s", envelope.Body)
			}
		})
	}
}

func TestInteractionsToClaudeInlineAudioBesideTextSendsTheTextWithoutAPlaceholder(t *testing.T) {
	envelope := interactionsFinalTurnToClaude(`{"type":"text","text":"keep me"},` + inlineAudio + `,` + inlineVideo)
	if envelope.Err != nil {
		t.Fatalf("envelope.Err = %v, body = %s", envelope.Err, envelope.Body)
	}
	content := gjson.GetBytes(envelope.Body, "messages.2.content").Array()
	if len(content) != 1 || content[0].Get("text").String() != "keep me" {
		t.Fatalf("text was lost or a placeholder leaked: %s", envelope.Body)
	}
}

func TestInteractionsToClaudeAssistantMediaKeepsThePlaceholder(t *testing.T) {
	envelope := interactionsToClaudeEnvelope(`{"model":"m","input":[{"type":"user_input","content":[{"type":"text","text":"a"}]},{"type":"model_output","content":[` + inlineAudio + `]},{"type":"user_input","content":[{"type":"text","text":"c"}]}]}`)
	if envelope.Err != nil {
		t.Fatalf("envelope.Err = %v, body = %s", envelope.Err, envelope.Body)
	}
	if got := gjson.GetBytes(envelope.Body, "messages.1.content.0.text").String(); got != "[audio content omitted]" {
		t.Fatalf("assistant placeholder = %q, body = %s", got, envelope.Body)
	}
}

func TestInteractionsToClaudeInlineAndURLDocumentsStillConvert(t *testing.T) {
	inline := interactionsFinalTurnToClaude(`{"type":"document","mime_type":"application/pdf","data":"JVBERi0xLjQK"}`)
	if inline.Err != nil {
		t.Fatalf("envelope.Err = %v, body = %s", inline.Err, inline.Body)
	}
	if got := gjson.GetBytes(inline.Body, "messages.2.content.0.source.type").String(); got != "base64" {
		t.Fatalf("inline document = %s", inline.Body)
	}
	byURL := interactionsFinalTurnToClaude(pdfByURI)
	if byURL.Err != nil {
		t.Fatalf("envelope.Err = %v, body = %s", byURL.Err, byURL.Body)
	}
	if got := gjson.GetBytes(byURL.Body, "messages.2.content.0.source.url").String(); got != "https://example.test/a.pdf" {
		t.Fatalf("url document = %s", byURL.Body)
	}
}
