package chat_completions

import (
	"errors"
	"testing"

	translatorcommon "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/common"
	"github.com/tidwall/gjson"
)

const (
	userTurnFileID = `{"type":"file","file":{"file_id":"file-absent"}}`
	userTurnText   = `{"type":"text","text":"keep me"}`
	userTurnInline = `{"type":"file","file":{"filename":"a.pdf","file_data":"data:application/pdf;base64,JVBERi0xLjQK"}}`
)

func TestConvertOpenAIRequestToGemini_RefusesAnyEmptiedUserTurn(t *testing.T) {
	cases := []struct {
		name     string
		input    string
		wantType string
	}{
		{
			name:     "history then file id only",
			input:    `{"model":"m","messages":[{"role":"user","content":"hello"},{"role":"assistant","content":"hi"},{"role":"user","content":[` + userTurnFileID + `]}]}`,
			wantType: "file",
		},
		{
			name:     "system and developer prompts do not hide the empty turn",
			input:    `{"model":"m","messages":[{"role":"system","content":"sys"},{"role":"developer","content":"dev"},{"role":"user","content":[` + userTurnFileID + `]}]}`,
			wantType: "file",
		},
		{
			name:     "emptied turn before a later text turn",
			input:    `{"model":"m","messages":[{"role":"user","content":[` + userTurnFileID + `]},{"role":"assistant","content":"ok"},{"role":"user","content":"next"}]}`,
			wantType: "file",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body, err := convertOpenAIRequestToGemini("m", []byte(tc.input), false)
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
		})
	}
}

func TestConvertOpenAIRequestToGemini_KeepsTurnWithTextBesideAttachment(t *testing.T) {
	input := []byte(`{"model":"m","messages":[{"role":"user","content":"hello"},{"role":"assistant","content":"hi"},{"role":"user","content":[` + userTurnText + `,` + userTurnFileID + `]}]}`)
	body, err := convertOpenAIRequestToGemini("m", input, false)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got := gjson.GetBytes(body, "contents.2.parts.0.text").String(); got != "keep me" {
		t.Fatalf("text was lost: %s", body)
	}
}

func TestConvertOpenAIRequestToGemini_InlineFileAfterHistoryStaysInlineData(t *testing.T) {
	input := []byte(`{"model":"m","messages":[{"role":"user","content":"hello"},{"role":"assistant","content":"hi"},{"role":"user","content":[` + userTurnInline + `]}]}`)
	body, err := convertOpenAIRequestToGemini("m", input, false)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got := gjson.GetBytes(body, "contents.2.parts.0.inlineData.mime_type").String(); got != "application/pdf" {
		t.Fatalf("inline file was not kept: %s", body)
	}
}
