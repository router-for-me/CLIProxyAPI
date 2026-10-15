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
	geminiTurnHello     = `{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}`
	geminiTurnAssistant = `{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hi"}]}`
	geminiTurnNext      = `{"type":"message","role":"user","content":[{"type":"input_text","text":"next"}]}`
	geminiTurnDeveloper = `{"type":"message","role":"developer","content":[{"type":"input_text","text":"dev"}]}`
	geminiTurnFileID    = `{"type":"input_file","file_id":"file-1"}`
	geminiTurnFileData  = `{"type":"input_file","filename":"a.pdf","file_data":"data:application/pdf;base64,JVBERi0xLjQK"}`
	geminiTurnFileURL   = `{"type":"input_file","file_url":"https://example.test/a.pdf","filename":"a.pdf"}`
	geminiTurnText      = `{"type":"input_text","text":"keep me"}`
	geminiTurnEmptyText = `{"type":"input_text","text":""}`
)

func geminiUserTurn(parts ...string) string {
	return `{"type":"message","role":"user","content":[` + strings.Join(parts, ",") + `]}`
}

func geminiTurnPayload(instructions string, items ...string) []byte {
	prefix := `{"model":"gemini-3-pro",`
	if instructions != "" {
		prefix += `"instructions":"` + instructions + `",`
	}
	return []byte(prefix + `"input":[` + strings.Join(items, ",") + `]}`)
}

func TestConvertOpenAIResponsesRequestToGemini_RefusesAnyEmptiedUserTurn(t *testing.T) {
	cases := []struct {
		name  string
		input []byte
	}{
		{
			name:  "file id only",
			input: geminiTurnPayload("", geminiUserTurn(geminiTurnFileID)),
		},
		{
			name:  "history then file id only",
			input: geminiTurnPayload("", geminiTurnHello, geminiTurnAssistant, geminiUserTurn(geminiTurnFileID)),
		},
		{
			name:  "instructions and developer prompt do not hide the empty turn",
			input: geminiTurnPayload("sys", geminiTurnDeveloper, geminiUserTurn(geminiTurnFileID)),
		},
		{
			name:  "emptied turn before a later text turn",
			input: geminiTurnPayload("", geminiUserTurn(geminiTurnFileID), geminiTurnAssistant, geminiTurnNext),
		},
		{
			name:  "empty text beside the file id does not count as sendable",
			input: geminiTurnPayload("", geminiTurnHello, geminiTurnAssistant, geminiUserTurn(geminiTurnEmptyText, geminiTurnFileID)),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body, err := ConvertOpenAIResponsesRequestToGemini("gemini-3-pro", tc.input, false)
			var unsupported *translatorcommon.UnsupportedPartError
			if !errors.As(err, &unsupported) || unsupported.Type != "input_file" || unsupported.StatusCode() != 400 {
				t.Fatalf("err = %v, want unsupported content part: input_file; body = %s", err, body)
			}
			if got, want := err.Error(), "unsupported content part: input_file"; got != want {
				t.Fatalf("error text = %q, want %q", got, want)
			}
			if !gjson.ValidBytes(body) {
				t.Fatalf("body is not JSON: %q", body)
			}

			envelope := sdktranslator.TranslateRequestEnvelope(context.Background(), sdktranslator.FormatOpenAIResponse, sdktranslator.FormatGemini,
				sdktranslator.RequestEnvelope{Format: sdktranslator.FormatOpenAIResponse, Model: "gemini-3-pro", Body: tc.input})
			if !errors.As(envelope.Err, &unsupported) || unsupported.Type != "input_file" {
				t.Fatalf("registry err = %v, want unsupported content part: input_file", envelope.Err)
			}
		})
	}
}

func TestConvertOpenAIResponsesRequestToGemini_KeepsTurnWithTextBesideFileID(t *testing.T) {
	input := geminiTurnPayload("", geminiTurnHello, geminiTurnAssistant, geminiUserTurn(geminiTurnText, geminiTurnFileID))
	body, err := ConvertOpenAIResponsesRequestToGemini("gemini-3-pro", input, false)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got := gjson.GetBytes(body, "contents.2.parts.0.text").String(); got != "keep me" {
		t.Fatalf("text was lost: %s", body)
	}
}

func TestConvertOpenAIResponsesRequestToGemini_InlineAndRemoteFilesStayParts(t *testing.T) {
	cases := map[string]struct {
		part string
		path string
		want string
	}{
		"inline bytes": {part: geminiTurnFileData, path: "contents.2.parts.0.inline_data.mime_type", want: "application/pdf"},
		"remote url":   {part: geminiTurnFileURL, path: "contents.2.parts.0.file_data.file_uri", want: "https://example.test/a.pdf"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			input := geminiTurnPayload("", geminiTurnHello, geminiTurnAssistant, geminiUserTurn(tc.part))
			body, err := ConvertOpenAIResponsesRequestToGemini("gemini-3-pro", input, false)
			if err != nil {
				t.Fatalf("err = %v", err)
			}
			if got := gjson.GetBytes(body, tc.path).String(); got != tc.want {
				t.Fatalf("%s = %q, want %q; body = %s", tc.path, got, tc.want, body)
			}
		})
	}
}

func TestConvertOpenAIResponsesRequestToGemini_ExportedWrapperKeepsAJSONBody(t *testing.T) {
	input := geminiTurnPayload("", geminiUserTurn(geminiTurnFileID))
	if body, _ := ConvertOpenAIResponsesRequestToGemini("gemini-3-pro", input, false); !gjson.ValidBytes(body) {
		t.Fatalf("body is not JSON: %q", body)
	}
}
