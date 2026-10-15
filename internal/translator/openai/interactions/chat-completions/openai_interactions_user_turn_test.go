package chat_completions

import (
	"context"
	"errors"
	"testing"

	translatorcommon "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/common"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

const (
	userTurnFileID     = `{"type":"file","file":{"file_id":"file-absent"}}`
	userTurnEmptyAudio = `{"type":"input_audio","input_audio":{"format":"wav"}}`
	userTurnText       = `{"type":"text","text":"keep me"}`
	userTurnEmptyText  = `{"type":"text","text":""}`
	userTurnInline     = `{"type":"file","file":{"filename":"a.pdf","file_data":"data:application/pdf;base64,JVBERi0xLjQK"}}`
	userTurnAudio      = `{"type":"input_audio","input_audio":{"data":"UklGRg==","format":"wav"}}`
)

func TestConvertOpenAIRequestToInteractions_RefusesAnyEmptiedUserTurn(t *testing.T) {
	cases := []struct {
		name     string
		input    string
		wantType string
	}{
		{
			name:     "file id only",
			input:    `{"model":"gemini-3.5-flash","messages":[{"role":"user","content":[` + userTurnFileID + `]}]}`,
			wantType: "file",
		},
		{
			name:     "history then file id only",
			input:    `{"model":"gemini-3.5-flash","messages":[{"role":"user","content":"hello"},{"role":"assistant","content":"hi"},{"role":"user","content":[` + userTurnFileID + `]}]}`,
			wantType: "file",
		},
		{
			name:     "system and developer prompts do not hide the empty turn",
			input:    `{"model":"gemini-3.5-flash","messages":[{"role":"system","content":"sys"},{"role":"developer","content":"dev"},{"role":"user","content":[` + userTurnFileID + `]}]}`,
			wantType: "file",
		},
		{
			name:     "emptied turn before a later text turn",
			input:    `{"model":"gemini-3.5-flash","messages":[{"role":"user","content":[` + userTurnFileID + `]},{"role":"assistant","content":"ok"},{"role":"user","content":"next"}]}`,
			wantType: "file",
		},
		{
			name:     "audio without bytes",
			input:    `{"model":"gemini-3.5-flash","messages":[{"role":"user","content":"hello"},{"role":"assistant","content":"hi"},{"role":"user","content":[` + userTurnEmptyAudio + `]}]}`,
			wantType: "input_audio",
		},
		{
			name:     "empty text beside the file id does not count as sendable",
			input:    `{"model":"gemini-3.5-flash","messages":[{"role":"user","content":[` + userTurnEmptyText + `,` + userTurnFileID + `]}]}`,
			wantType: "file",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body, err := convertOpenAIRequestToInteractions("gemini-3.5-flash", []byte(tc.input), false)
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

			envelope := sdktranslator.TranslateRequestEnvelope(context.Background(), sdktranslator.FormatOpenAI, sdktranslator.FormatInteractions,
				sdktranslator.RequestEnvelope{Format: sdktranslator.FormatOpenAI, Model: "gemini-3.5-flash", Body: []byte(tc.input)})
			if !errors.As(envelope.Err, &unsupported) || unsupported.Type != tc.wantType {
				t.Fatalf("registry err = %v, want unsupported content part: %s", envelope.Err, tc.wantType)
			}
		})
	}
}

func TestConvertOpenAIRequestToInteractions_KeepsTurnWithTextBesideAttachment(t *testing.T) {
	input := `{"model":"gemini-3.5-flash","messages":[{"role":"user","content":"hello"},{"role":"assistant","content":"hi"},{"role":"user","content":[` + userTurnText + `,` + userTurnFileID + `]}]}`
	body, err := convertOpenAIRequestToInteractions("gemini-3.5-flash", []byte(input), false)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got := gjson.GetBytes(body, "input.2.content.0.text").String(); got != "keep me" {
		t.Fatalf("text was lost: %s", body)
	}
}

func TestConvertOpenAIRequestToInteractions_InlineFileAndAudioAfterHistoryStayBytes(t *testing.T) {
	cases := map[string]struct {
		part     string
		wantType string
		wantData string
	}{
		"file":  {part: userTurnInline, wantType: "document", wantData: "JVBERi0xLjQK"},
		"audio": {part: userTurnAudio, wantType: "audio", wantData: "UklGRg=="},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			input := `{"model":"gemini-3.5-flash","messages":[{"role":"user","content":"hello"},{"role":"assistant","content":"hi"},{"role":"user","content":[` + tc.part + `]}]}`
			body, err := convertOpenAIRequestToInteractions("gemini-3.5-flash", []byte(input), false)
			if err != nil {
				t.Fatalf("err = %v", err)
			}
			part := gjson.GetBytes(body, "input.2.content.0")
			if part.Get("type").String() != tc.wantType || part.Get("data").String() != tc.wantData {
				t.Fatalf("part = %s, body = %s", part.Raw, body)
			}
		})
	}
}

func TestConvertOpenAIRequestToInteractions_FileURLStaysAFile(t *testing.T) {
	input := `{"model":"gemini-3.5-flash","messages":[{"role":"user","content":[{"type":"file","file":{"file_url":"https://example.test/a.pdf"}}]}]}`
	body, err := convertOpenAIRequestToInteractions("gemini-3.5-flash", []byte(input), false)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got := gjson.GetBytes(body, "input.0.content.0.file_url").String(); got != "https://example.test/a.pdf" {
		t.Fatalf("file url was lost: %s", body)
	}
}

func TestConvertOpenAIRequestToInteractions_ExportedWrapperKeepsAJSONBody(t *testing.T) {
	input := []byte(`{"model":"gemini-3.5-flash","messages":[{"role":"user","content":[` + userTurnFileID + `]}]}`)
	if body, _ := ConvertOpenAIRequestToInteractions("gemini-3.5-flash", input, false); !gjson.ValidBytes(body) {
		t.Fatalf("body is not JSON: %q", body)
	}
}
