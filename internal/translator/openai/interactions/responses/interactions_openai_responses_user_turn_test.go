package responses

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
	interactionsTurnHello     = `{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}`
	interactionsTurnAssistant = `{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hi"}]}`
	interactionsTurnNext      = `{"type":"message","role":"user","content":[{"type":"input_text","text":"next"}]}`
	interactionsTurnFileID    = `{"type":"input_file","file_id":"file-1"}`
	interactionsTurnFileData  = `{"type":"input_file","filename":"a.pdf","file_data":"data:application/pdf;base64,JVBERi0xLjQK"}`
	interactionsTurnFileURL   = `{"type":"input_file","file_url":"https://example.test/a.pdf"}`
	interactionsTurnAudio     = `{"type":"input_audio","input_audio":{"data":"UklGRg==","format":"wav"}}`
	interactionsTurnNoAudio   = `{"type":"input_audio","input_audio":{"format":"wav"}}`
	interactionsTurnVideo     = `{"type":"input_video","video_url":"https://example.test/a.mp4"}`
	interactionsTurnText      = `{"type":"input_text","text":"keep me"}`
	interactionsTurnEmptyText = `{"type":"input_text","text":""}`
)

func interactionsUserTurn(role string, parts ...string) string {
	return `{"type":"message","role":"` + role + `","content":[` + strings.Join(parts, ",") + `]}`
}

func interactionsTurnPayload(instructions string, items ...string) []byte {
	prefix := `{"model":"gemini-3.5-flash",`
	if instructions != "" {
		prefix += `"instructions":"` + instructions + `",`
	}
	return []byte(prefix + `"input":[` + strings.Join(items, ",") + `]}`)
}

func TestConvertOpenAIResponsesRequestToInteractions_RefusesAnyEmptiedUserTurn(t *testing.T) {
	cases := []struct {
		name     string
		input    []byte
		wantType string
	}{
		{
			name:     "file id only",
			input:    interactionsTurnPayload("", interactionsUserTurn("user", interactionsTurnFileID)),
			wantType: "input_file",
		},
		{
			name:     "history then file id only",
			input:    interactionsTurnPayload("", interactionsTurnHello, interactionsTurnAssistant, interactionsUserTurn("user", interactionsTurnFileID)),
			wantType: "input_file",
		},
		{
			name:     "audio without bytes",
			input:    interactionsTurnPayload("", interactionsTurnHello, interactionsTurnAssistant, interactionsUserTurn("user", interactionsTurnNoAudio)),
			wantType: "input_audio",
		},
		{
			name:     "video has no counterpart",
			input:    interactionsTurnPayload("", interactionsTurnHello, interactionsTurnAssistant, interactionsUserTurn("user", interactionsTurnVideo)),
			wantType: "input_video",
		},
		{
			name:     "instructions and developer prompt do not hide the empty turn",
			input:    interactionsTurnPayload("sys", interactionsUserTurn("developer", interactionsTurnText), interactionsUserTurn("user", interactionsTurnFileID)),
			wantType: "input_file",
		},
		{
			name:     "emptied turn before a later text turn",
			input:    interactionsTurnPayload("", interactionsUserTurn("user", interactionsTurnFileID), interactionsTurnAssistant, interactionsTurnNext),
			wantType: "input_file",
		},
		{
			name:     "empty text beside the file id does not count as sendable",
			input:    interactionsTurnPayload("", interactionsUserTurn("user", interactionsTurnEmptyText, interactionsTurnFileID)),
			wantType: "input_file",
		},
		{
			name:     "bare input file item",
			input:    interactionsTurnPayload("", interactionsTurnHello, interactionsTurnAssistant, interactionsTurnFileID),
			wantType: "input_file",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body, err := convertOpenAIResponsesRequestToInteractions("gemini-3.5-flash", tc.input, false)
			var unsupported *translatorcommon.UnsupportedPartError
			if !errors.As(err, &unsupported) || unsupported.Type != tc.wantType || unsupported.StatusCode() != 400 {
				t.Fatalf("err = %v, want unsupported content part: %s; body = %s", err, tc.wantType, body)
			}
			if got, want := err.Error(), "unsupported content part: "+tc.wantType; got != want {
				t.Fatalf("error text = %q, want %q", got, want)
			}
			if !gjson.ValidBytes(body) {
				t.Fatalf("body is not JSON: %q", body)
			}

			envelope := sdktranslator.TranslateRequestEnvelope(context.Background(), sdktranslator.FormatOpenAIResponse, sdktranslator.FormatInteractions,
				sdktranslator.RequestEnvelope{Format: sdktranslator.FormatOpenAIResponse, Model: "gemini-3.5-flash", Body: tc.input})
			if !errors.As(envelope.Err, &unsupported) || unsupported.Type != tc.wantType {
				t.Fatalf("registry err = %v, want unsupported content part: %s", envelope.Err, tc.wantType)
			}
		})
	}
}

func TestConvertOpenAIResponsesRequestToInteractions_KeepsTurnWithTextBesideAttachment(t *testing.T) {
	for name, attachment := range map[string]string{"file id": interactionsTurnFileID, "audio without bytes": interactionsTurnNoAudio} {
		t.Run(name, func(t *testing.T) {
			input := interactionsTurnPayload("", interactionsTurnHello, interactionsTurnAssistant, interactionsUserTurn("user", interactionsTurnText, attachment))
			body, err := convertOpenAIResponsesRequestToInteractions("gemini-3.5-flash", input, false)
			if err != nil {
				t.Fatalf("err = %v", err)
			}
			if got := gjson.GetBytes(body, "input.2.content.0.text").String(); got != "keep me" {
				t.Fatalf("text was lost: %s", body)
			}
		})
	}
}

func TestConvertOpenAIResponsesRequestToInteractions_MapsBytesAndUrls(t *testing.T) {
	cases := map[string]struct {
		part string
		want map[string]string
	}{
		"file data": {
			part: interactionsTurnFileData,
			want: map[string]string{"type": "document", "mime_type": "application/pdf", "data": "JVBERi0xLjQK", "filename": "a.pdf"},
		},
		"file url": {
			part: interactionsTurnFileURL,
			want: map[string]string{"type": "document", "file_url": "https://example.test/a.pdf"},
		},
		"audio": {
			part: interactionsTurnAudio,
			want: map[string]string{"type": "audio", "data": "UklGRg==", "mime_type": "audio/wav"},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			input := interactionsTurnPayload("", interactionsTurnHello, interactionsTurnAssistant, interactionsUserTurn("user", tc.part))
			body, err := convertOpenAIResponsesRequestToInteractions("gemini-3.5-flash", input, false)
			if err != nil {
				t.Fatalf("err = %v", err)
			}
			part := gjson.GetBytes(body, "input.2.content.0")
			for path, want := range tc.want {
				if got := part.Get(path).String(); got != want {
					t.Fatalf("%s = %q, want %q; part = %s, body = %s", path, got, want, part.Raw, body)
				}
			}
		})
	}
}

func TestConvertOpenAIResponsesRequestToInteractions_BareFileItemStaysADocument(t *testing.T) {
	input := interactionsTurnPayload("", interactionsTurnHello, interactionsTurnAssistant, interactionsTurnFileData)
	body, err := convertOpenAIResponsesRequestToInteractions("gemini-3.5-flash", input, false)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	step := gjson.GetBytes(body, "input.2")
	if step.Get("type").String() != "user_input" || step.Get("content.0.type").String() != "document" || step.Get("content.0.data").String() != "JVBERi0xLjQK" {
		t.Fatalf("step = %s, body = %s", step.Raw, body)
	}
}

func TestConvertOpenAIResponsesRequestToInteractions_DeveloperAttachmentIsNotAUserTurn(t *testing.T) {
	input := interactionsTurnPayload("", interactionsUserTurn("developer", interactionsTurnFileID), interactionsTurnNext)
	if _, err := convertOpenAIResponsesRequestToInteractions("gemini-3.5-flash", input, false); err != nil {
		t.Fatalf("err = %v, want no refusal for a developer message", err)
	}
}

func TestConvertOpenAIResponsesRequestToInteractions_ExportedWrapperKeepsAJSONBody(t *testing.T) {
	input := interactionsTurnPayload("", interactionsUserTurn("user", interactionsTurnFileID))
	if body, _ := ConvertOpenAIResponsesRequestToInteractions("gemini-3.5-flash", input, false); !gjson.ValidBytes(body) {
		t.Fatalf("body is not JSON: %q", body)
	}
}
