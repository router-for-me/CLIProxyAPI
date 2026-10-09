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
	userTurnFileID     = `{"type":"file","file":{"file_id":"file-1"}}`
	userTurnFileURL    = `{"type":"file","file":{"file_url":"https://example.test/a.pdf","filename":"a.pdf"}}`
	userTurnEmptyFile  = `{"type":"file","file":{"filename":"a.pdf"}}`
	userTurnEmptyAudio = `{"type":"input_audio","input_audio":{"format":"wav"}}`
	userTurnText       = `{"type":"text","text":"keep me"}`
	userTurnEmptyText  = `{"type":"text","text":""}`
	userTurnInline     = `{"type":"file","file":{"filename":"a.pdf","file_data":"data:application/pdf;base64,JVBERi0xLjQK"}}`
	userTurnAudio      = `{"type":"input_audio","input_audio":{"data":"UklGRg==","format":"wav"}}`
)

func TestConvertOpenAIRequestToCodex_RefusesAnyEmptiedUserTurn(t *testing.T) {
	cases := []struct {
		name     string
		input    string
		wantType string
	}{
		{
			name:     "history then file without a source",
			input:    `{"model":"m","messages":[{"role":"user","content":"hello"},{"role":"assistant","content":"hi"},{"role":"user","content":[` + userTurnEmptyFile + `]}]}`,
			wantType: "file",
		},
		{
			name:     "system and developer prompts do not hide the empty turn",
			input:    `{"model":"m","messages":[{"role":"system","content":"sys"},{"role":"developer","content":"dev"},{"role":"user","content":[` + userTurnEmptyFile + `]}]}`,
			wantType: "file",
		},
		{
			name:     "emptied turn before a later text turn",
			input:    `{"model":"m","messages":[{"role":"user","content":[` + userTurnEmptyFile + `]},{"role":"assistant","content":"ok"},{"role":"user","content":"next"}]}`,
			wantType: "file",
		},
		{
			name:     "audio without bytes",
			input:    `{"model":"m","messages":[{"role":"user","content":"hello"},{"role":"assistant","content":"hi"},{"role":"user","content":[` + userTurnEmptyAudio + `]}]}`,
			wantType: "input_audio",
		},
		{
			name:     "empty text beside the empty file does not count as sendable",
			input:    `{"model":"m","messages":[{"role":"user","content":[` + userTurnEmptyText + `,` + userTurnEmptyFile + `]}]}`,
			wantType: "file",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body, err := convertOpenAIRequestToCodex("m", []byte(tc.input), false)
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

			envelope := sdktranslator.TranslateRequestEnvelope(context.Background(), sdktranslator.FormatOpenAI, sdktranslator.FormatCodex,
				sdktranslator.RequestEnvelope{Format: sdktranslator.FormatOpenAI, Model: "m", Body: []byte(tc.input)})
			if !errors.As(envelope.Err, &unsupported) || unsupported.Type != tc.wantType {
				t.Fatalf("registry err = %v, want unsupported content part: %s", envelope.Err, tc.wantType)
			}
		})
	}
}

func TestConvertOpenAIRequestToCodex_FileIDStaysAnInputFile(t *testing.T) {
	cases := map[string]struct {
		part string
		key  string
		want string
	}{
		"file id":  {part: userTurnFileID, key: "file_id", want: "file-1"},
		"file url": {part: userTurnFileURL, key: "file_url", want: "https://example.test/a.pdf"},
		"file data": {
			part: userTurnInline,
			key:  "file_data",
			want: "data:application/pdf;base64,JVBERi0xLjQK",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			input := `{"model":"m","messages":[{"role":"user","content":"hello"},{"role":"assistant","content":"hi"},{"role":"user","content":[` + tc.part + `]}]}`
			body, err := convertOpenAIRequestToCodex("m", []byte(input), false)
			if err != nil {
				t.Fatalf("err = %v", err)
			}
			part := gjson.GetBytes(body, "input.2.content.0")
			if part.Get("type").String() != "input_file" || part.Get(tc.key).String() != tc.want {
				t.Fatalf("part = %s, body = %s", part.Raw, body)
			}
		})
	}
}

func TestConvertOpenAIRequestToCodex_KeepsTurnWithTextBesideAttachment(t *testing.T) {
	input := `{"model":"m","messages":[{"role":"user","content":"hello"},{"role":"assistant","content":"hi"},{"role":"user","content":[` + userTurnText + `,` + userTurnEmptyFile + `]}]}`
	body, err := convertOpenAIRequestToCodex("m", []byte(input), false)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got := gjson.GetBytes(body, "input.2.content.0.text").String(); got != "keep me" {
		t.Fatalf("text was lost: %s", body)
	}
}

func TestConvertOpenAIRequestToCodex_AudioAfterHistoryStaysAudio(t *testing.T) {
	input := `{"model":"m","messages":[{"role":"user","content":"hello"},{"role":"assistant","content":"hi"},{"role":"user","content":[` + userTurnAudio + `]}]}`
	body, err := convertOpenAIRequestToCodex("m", []byte(input), false)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	part := gjson.GetBytes(body, "input.2.content.0")
	if part.Get("type").String() != "input_audio" || part.Get("data").String() != "UklGRg==" {
		t.Fatalf("part = %s, body = %s", part.Raw, body)
	}
}

func TestConvertOpenAIRequestToCodex_ExportedWrapperKeepsAJSONBody(t *testing.T) {
	input := []byte(`{"model":"m","messages":[{"role":"user","content":[` + userTurnEmptyFile + `]}]}`)
	if body, _ := ConvertOpenAIRequestToCodex("m", input, false); !gjson.ValidBytes(body) {
		t.Fatalf("body is not JSON: %q", body)
	}
}
