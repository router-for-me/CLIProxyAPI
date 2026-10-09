package claude

import (
	"context"
	"errors"
	"testing"

	translatorcommon "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/common"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

const userTurnUpload = `{"type":"container_upload","file_id":"file-1"}`

func TestConvertClaudeRequestToInteractions_RefusesAnyEmptiedUserTurn(t *testing.T) {
	cases := []struct {
		name     string
		input    string
		wantType string
	}{
		{
			name:     "history then attachment only",
			input:    `{"model":"m","messages":[{"role":"user","content":"hello"},{"role":"assistant","content":[{"type":"text","text":"hi"}]},{"role":"user","content":[` + userTurnUpload + `]}]}`,
			wantType: "container_upload",
		},
		{
			name:     "system prompt and system reminder",
			input:    `{"model":"m","system":"sys","messages":[{"role":"system","content":"reminder"},{"role":"user","content":[` + userTurnUpload + `]}]}`,
			wantType: "container_upload",
		},
		{
			name:     "emptied turn before a later text turn",
			input:    `{"model":"m","messages":[{"role":"user","content":[` + userTurnUpload + `]},{"role":"assistant","content":[{"type":"text","text":"ok"}]},{"role":"user","content":"next"}]}`,
			wantType: "container_upload",
		},
		{
			name:     "document without bytes",
			input:    `{"model":"m","messages":[{"role":"user","content":"hello"},{"role":"assistant","content":"hi"},{"role":"user","content":[{"type":"document","source":{"type":"file","file_id":"file-1"}}]}]}`,
			wantType: "document",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body, err := ConvertClaudeRequestToInteractionsWithCompat("m", []byte(tc.input), false)
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

func TestConvertClaudeRequestToInteractions_KeepsTurnWithTextBesideAttachment(t *testing.T) {
	input := []byte(`{"model":"m","messages":[{"role":"user","content":"hello"},{"role":"assistant","content":"hi"},{"role":"user","content":[{"type":"text","text":"keep me"},` + userTurnUpload + `]}]}`)
	body, err := ConvertClaudeRequestToInteractionsWithCompat("m", input, false)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got := gjson.GetBytes(body, "input.2.content.0.text").String(); got != "keep me" {
		t.Fatalf("text was lost: %s", body)
	}
}

func TestConvertClaudeRequestToInteractions_Base64DocumentAfterHistoryStaysMedia(t *testing.T) {
	input := []byte(`{"model":"m","messages":[{"role":"user","content":"hello"},{"role":"assistant","content":"hi"},{"role":"user","content":[{"type":"document","source":{"type":"base64","media_type":"application/pdf","data":"JVBERi0xLjQK"}}]}]}`)
	body, err := ConvertClaudeRequestToInteractionsWithCompat("m", input, false)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	part := gjson.GetBytes(body, "input.2.content.0")
	if part.Get("type").String() != "document" || part.Get("mime_type").String() != "application/pdf" || part.Get("data").String() != "JVBERi0xLjQK" {
		t.Fatalf("part = %s, body = %s", part.Raw, body)
	}
}

func TestConvertClaudeRequestToInteractions_WhitespaceTextDoesNotHideAnUnrepresentableImage(t *testing.T) {
	fileImage := `{"type":"image","source":{"type":"file","file_id":"f1"}}`
	cases := []struct {
		name  string
		parts string
	}{
		{name: "spaces before image", parts: `{"type":"text","text":"  "},` + fileImage},
		{name: "mixed whitespace before image", parts: `{"type":"text","text":" \n\t"},` + fileImage},
		{name: "spaces after image", parts: fileImage + `,{"type":"text","text":"  "}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input := []byte(`{"model":"m","messages":[{"role":"user","content":[` + tc.parts + `]}]}`)
			body, err := ConvertClaudeRequestToInteractionsWithCompat("m", input, false)
			var unsupported *translatorcommon.UnsupportedPartError
			if !errors.As(err, &unsupported) || unsupported.Type != "image" || unsupported.StatusCode() != 400 {
				t.Fatalf("err = %v, want unsupported content part: image; body = %s", err, body)
			}
		})
	}
}

func TestConvertClaudeRequestToInteractions_RealTextBesideWhitespaceAndAnImageStillSucceeds(t *testing.T) {
	input := []byte(`{"model":"m","messages":[{"role":"user","content":[{"type":"text","text":"  "},{"type":"text","text":"keep me"},{"type":"image","source":{"type":"file","file_id":"f1"}}]}]}`)
	body, err := ConvertClaudeRequestToInteractionsWithCompat("m", input, false)
	if err != nil {
		t.Fatalf("err = %v, body = %s", err, body)
	}
	found := false
	gjson.GetBytes(body, "input.0.content").ForEach(func(_, part gjson.Result) bool {
		if part.Get("text").String() == "keep me" {
			found = true
		}
		return true
	})
	if !found {
		t.Fatalf("text was lost: %s", body)
	}
}

func TestClaudeToInteractionsRegistrationCarriesWhitespaceImageRefusal(t *testing.T) {
	input := []byte(`{"model":"m","messages":[{"role":"user","content":[{"type":"text","text":"  "},{"type":"image","source":{"type":"file","file_id":"f1"}}]}]}`)
	envelope := sdktranslator.TranslateRequestEnvelope(context.Background(), sdktranslator.FormatClaude, sdktranslator.FormatInteractions, sdktranslator.RequestEnvelope{
		Format: sdktranslator.FormatClaude,
		Model:  "m",
		Body:   input,
	})
	var unsupported *translatorcommon.UnsupportedPartError
	if !errors.As(envelope.Err, &unsupported) || unsupported.Type != "image" {
		t.Fatalf("envelope.Err = %v, body = %s", envelope.Err, envelope.Body)
	}
}
