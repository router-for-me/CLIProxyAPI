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
	chatTurnHello     = `{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}`
	chatTurnAssistant = `{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hi"}]}`
	chatTurnNext      = `{"type":"message","role":"user","content":[{"type":"input_text","text":"next"}]}`
	chatTurnDeveloper = `{"type":"message","role":"developer","content":[{"type":"input_text","text":"dev"}]}`
	chatTurnFileID    = `{"type":"input_file","file_id":"file-1"}`
	chatTurnFileData  = `{"type":"input_file","filename":"a.pdf","file_data":"data:application/pdf;base64,JVBERi0xLjQK"}`
	chatTurnFileURL   = `{"type":"input_file","file_url":"https://example.test/a.pdf"}`
	chatTurnAudio     = `{"type":"input_audio","input_audio":{"data":"UklGRg==","format":"wav"}}`
	chatTurnNoAudio   = `{"type":"input_audio","input_audio":{"format":"wav"}}`
	chatTurnText      = `{"type":"input_text","text":"keep me"}`
	chatTurnEmptyText = `{"type":"input_text","text":""}`
)

func chatUserTurn(parts ...string) string {
	return `{"type":"message","role":"user","content":[` + strings.Join(parts, ",") + `]}`
}

func chatPayload(instructions string, items ...string) []byte {
	prefix := `{"model":"gpt-5",`
	if instructions != "" {
		prefix += `"instructions":"` + instructions + `",`
	}
	return []byte(prefix + `"input":[` + strings.Join(items, ",") + `]}`)
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_RefusesAnyEmptiedUserTurn(t *testing.T) {
	cases := []struct {
		name     string
		input    []byte
		wantType string
	}{
		{
			name:     "history then file url only",
			input:    chatPayload("", chatTurnHello, chatTurnAssistant, chatUserTurn(chatTurnFileURL)),
			wantType: "input_file",
		},
		{
			name:     "audio without bytes",
			input:    chatPayload("", chatTurnHello, chatTurnAssistant, chatUserTurn(chatTurnNoAudio)),
			wantType: "input_audio",
		},
		{
			name:     "instructions and developer prompt do not hide the empty turn",
			input:    chatPayload("sys", chatTurnDeveloper, chatUserTurn(chatTurnFileURL)),
			wantType: "input_file",
		},
		{
			name:     "emptied turn before a later text turn",
			input:    chatPayload("", chatUserTurn(chatTurnFileURL), chatTurnAssistant, chatTurnNext),
			wantType: "input_file",
		},
		{
			name:     "empty text beside the unsendable file does not count as sendable",
			input:    chatPayload("", chatUserTurn(chatTurnEmptyText, chatTurnFileURL)),
			wantType: "input_file",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body, err := convertOpenAIResponsesRequestToOpenAIChatCompletions("gpt-5", tc.input, false)
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

			envelope := sdktranslator.TranslateRequestEnvelope(context.Background(), sdktranslator.FormatOpenAIResponse, sdktranslator.FormatOpenAI,
				sdktranslator.RequestEnvelope{Format: sdktranslator.FormatOpenAIResponse, Model: "gpt-5", Body: tc.input})
			if !errors.As(envelope.Err, &unsupported) || unsupported.Type != tc.wantType {
				t.Fatalf("registry err = %v, want unsupported content part: %s", envelope.Err, tc.wantType)
			}
		})
	}
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_MapsFilesAndAudio(t *testing.T) {
	cases := map[string]struct {
		part string
		want map[string]string
	}{
		"file id": {
			part: chatTurnFileID,
			want: map[string]string{"type": "file", "file.file_id": "file-1"},
		},
		"file data": {
			part: chatTurnFileData,
			want: map[string]string{"type": "file", "file.file_data": "data:application/pdf;base64,JVBERi0xLjQK", "file.filename": "a.pdf"},
		},
		"audio": {
			part: chatTurnAudio,
			want: map[string]string{"type": "input_audio", "input_audio.data": "UklGRg==", "input_audio.format": "wav"},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			input := chatPayload("", chatTurnHello, chatTurnAssistant, chatUserTurn(tc.part))
			body, err := convertOpenAIResponsesRequestToOpenAIChatCompletions("gpt-5", input, false)
			if err != nil {
				t.Fatalf("err = %v", err)
			}
			part := gjson.GetBytes(body, "messages.2.content.0")
			for path, want := range tc.want {
				if got := part.Get(path).String(); got != want {
					t.Fatalf("%s = %q, want %q; part = %s, body = %s", path, got, want, part.Raw, body)
				}
			}
		})
	}
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_KeepsTurnWithTextBesideAttachment(t *testing.T) {
	for name, attachment := range map[string]string{"file url": chatTurnFileURL, "audio without bytes": chatTurnNoAudio} {
		t.Run(name, func(t *testing.T) {
			input := chatPayload("", chatTurnHello, chatTurnAssistant, chatUserTurn(chatTurnText, attachment))
			body, err := convertOpenAIResponsesRequestToOpenAIChatCompletions("gpt-5", input, false)
			if err != nil {
				t.Fatalf("err = %v", err)
			}
			if got := gjson.GetBytes(body, "messages.2.content.0.text").String(); got != "keep me" {
				t.Fatalf("text was lost: %s", body)
			}
		})
	}
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_MappedAttachmentBesideTextKeepsBoth(t *testing.T) {
	input := chatPayload("", chatUserTurn(chatTurnText, chatTurnFileID, chatTurnAudio))
	body, err := convertOpenAIResponsesRequestToOpenAIChatCompletions("gpt-5", input, false)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	content := gjson.GetBytes(body, "messages.0.content")
	if got := content.Get("#").Int(); got != 3 {
		t.Fatalf("content = %s, want text, file and audio", content.Raw)
	}
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_ExportedWrapperKeepsAJSONBody(t *testing.T) {
	input := chatPayload("", chatUserTurn(chatTurnFileURL))
	if body, _ := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("gpt-5", input, false); !gjson.ValidBytes(body) {
		t.Fatalf("body is not JSON: %q", body)
	}
}
