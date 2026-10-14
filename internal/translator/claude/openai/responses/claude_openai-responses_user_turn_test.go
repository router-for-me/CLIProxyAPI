package responses

import (
	"context"
	"errors"
	"testing"

	translatorcommon "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/common"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

const (
	responsesUserHello    = `{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}`
	responsesAssistantHi  = `{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hi"}]}`
	responsesUserNext     = `{"type":"message","role":"user","content":[{"type":"input_text","text":"next"}]}`
	responsesFileIDPart   = `{"type":"input_file","file_id":"file-1"}`
	responsesAudioPart    = `{"type":"input_audio","input_audio":{"data":"UklGRg==","format":"wav"}}`
	responsesTextPart     = `{"type":"input_text","text":"keep me"}`
	responsesInlineFile   = `{"type":"input_file","filename":"a.pdf","file_data":"data:application/pdf;base64,JVBERi0xLjQK"}`
	responsesDeveloperMsg = `{"type":"message","role":"developer","content":[{"type":"input_text","text":"dev"}]}`
)

func responsesUserTurn(parts ...string) string {
	content := ""
	for i, part := range parts {
		if i > 0 {
			content += ","
		}
		content += part
	}
	return `{"type":"message","role":"user","content":[` + content + `]}`
}

func responsesPayload(instructions string, items ...string) []byte {
	input := ""
	for i, item := range items {
		if i > 0 {
			input += ","
		}
		input += item
	}
	prefix := `{"model":"claude-sonnet-4",`
	if instructions != "" {
		prefix += `"instructions":"` + instructions + `",`
	}
	return []byte(prefix + `"input":[` + input + `]}`)
}

func TestConvertOpenAIResponsesRequestToClaude_RefusesAnyEmptiedUserTurn(t *testing.T) {
	cases := []struct {
		name     string
		input    []byte
		wantType string
	}{
		{
			name:     "history then file id only",
			input:    responsesPayload("", responsesUserHello, responsesAssistantHi, responsesUserTurn(responsesFileIDPart)),
			wantType: "input_file",
		},
		{
			name:     "audio only after history",
			input:    responsesPayload("", responsesUserHello, responsesAssistantHi, responsesUserTurn(responsesAudioPart)),
			wantType: "input_audio",
		},
		{
			name:     "instructions and developer prompt do not hide the empty turn",
			input:    responsesPayload("sys", responsesDeveloperMsg, responsesUserTurn(responsesFileIDPart)),
			wantType: "input_file",
		},
		{
			name:     "emptied turn before a later text turn",
			input:    responsesPayload("", responsesUserTurn(responsesFileIDPart), responsesAssistantHi, responsesUserNext),
			wantType: "input_file",
		},
		{
			name:     "file url carries no bytes",
			input:    responsesPayload("", responsesUserTurn(`{"type":"input_file","file_url":"https://example.test/a.pdf"}`)),
			wantType: "input_file",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body, err := ConvertOpenAIResponsesRequestToClaudeWithCompat("claude-sonnet-4", tc.input, false)
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

			envelope := sdktranslator.TranslateRequestEnvelope(context.Background(), sdktranslator.FormatOpenAIResponse, sdktranslator.FormatClaude,
				sdktranslator.RequestEnvelope{Format: sdktranslator.FormatOpenAIResponse, Model: "claude-sonnet-4", Body: tc.input})
			if !errors.As(envelope.Err, &unsupported) || unsupported.Type != tc.wantType {
				t.Fatalf("registry err = %v, want unsupported content part: %s", envelope.Err, tc.wantType)
			}
		})
	}
}

func TestConvertOpenAIResponsesRequestToClaude_KeepsTurnWithTextBesideAttachment(t *testing.T) {
	for name, attachment := range map[string]string{"file id": responsesFileIDPart, "audio": responsesAudioPart} {
		t.Run(name, func(t *testing.T) {
			input := responsesPayload("", responsesUserHello, responsesAssistantHi, responsesUserTurn(responsesTextPart, attachment))
			body, err := ConvertOpenAIResponsesRequestToClaudeWithCompat("claude-sonnet-4", input, false)
			if err != nil {
				t.Fatalf("err = %v", err)
			}
			if got := gjson.GetBytes(body, "messages.2.content").String(); got != "keep me" {
				t.Fatalf("text was lost: %s", body)
			}
		})
	}
}

func TestConvertOpenAIResponsesRequestToClaude_InlineFileStaysADocument(t *testing.T) {
	input := responsesPayload("", responsesUserHello, responsesAssistantHi, responsesUserTurn(responsesInlineFile))
	body, err := ConvertOpenAIResponsesRequestToClaudeWithCompat("claude-sonnet-4", input, false)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	part := gjson.GetBytes(body, "messages.2.content.0")
	if part.Get("type").String() != "document" || part.Get("source.data").String() != "JVBERi0xLjQK" {
		t.Fatalf("part = %s, body = %s", part.Raw, body)
	}
}

func TestConvertOpenAIResponsesRequestToClaude_ExportedWrappersKeepAJSONBody(t *testing.T) {
	input := responsesPayload("", responsesUserTurn(responsesFileIDPart))
	plain, errPlain := ConvertOpenAIResponsesRequestToClaude("claude-sonnet-4", input, false)
	compat, errCompat := ConvertOpenAIResponsesRequestToClaudeWithCompat("claude-sonnet-4", input, false)
	for name, body := range map[string][]byte{"plain": plain, "compat": compat} {
		if !gjson.ValidBytes(body) {
			t.Fatalf("%s: body is not JSON: %q", name, body)
		}
	}
	if errPlain == nil || errCompat == nil {
		t.Fatalf("plain err = %v, compat err = %v", errPlain, errCompat)
	}
}
