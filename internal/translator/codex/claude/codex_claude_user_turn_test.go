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

func TestConvertClaudeRequestToCodex_RefusesAnyEmptiedUserTurn(t *testing.T) {
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
			body, err := ConvertClaudeRequestToCodexWithCompat("m", []byte(tc.input), false)
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

func TestConvertClaudeRequestToCodex_KeepsTurnWithTextBesideAttachment(t *testing.T) {
	input := []byte(`{"model":"m","messages":[{"role":"user","content":"hello"},{"role":"assistant","content":"hi"},{"role":"user","content":[{"type":"text","text":"keep me"},` + userTurnUpload + `]}]}`)
	body, err := ConvertClaudeRequestToCodexWithCompat("m", input, false)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got := gjson.GetBytes(body, "input.2.content.0.text").String(); got != "keep me" {
		t.Fatalf("text was lost: %s", body)
	}
}

func TestConvertClaudeRequestToCodex_Base64DocumentAfterHistoryStaysAFile(t *testing.T) {
	input := []byte(`{"model":"m","messages":[{"role":"user","content":"hello"},{"role":"assistant","content":"hi"},{"role":"user","content":[{"type":"document","source":{"type":"base64","media_type":"application/pdf","data":"JVBERi0xLjQK"}}]}]}`)
	body, err := ConvertClaudeRequestToCodexWithCompat("m", input, false)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	part := gjson.GetBytes(body, "input.2.content.0")
	if part.Get("type").String() != "input_file" || part.Get("file_data").String() != "data:application/pdf;base64,JVBERi0xLjQK" {
		t.Fatalf("part = %s, body = %s", part.Raw, body)
	}
}

func TestConvertClaudeRequestToCodex_EmptyTextDoesNotHideAnEmptiedUserTurn(t *testing.T) {
	cases := map[string]string{
		"array content": `{"model":"m","messages":[{"role":"user","content":[{"type":"text","text":""},` + userTurnUpload + `]}]}`,
		"after history": `{"model":"m","messages":[{"role":"user","content":"hello"},{"role":"assistant","content":"hi"},{"role":"user","content":[` + userTurnUpload + `,{"type":"text","text":""}]}]}`,
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			body, err := ConvertClaudeRequestToCodexWithCompat("m", []byte(input), false)
			var unsupported *translatorcommon.UnsupportedPartError
			if !errors.As(err, &unsupported) || unsupported.Type != "container_upload" || unsupported.StatusCode() != 400 {
				t.Fatalf("err = %v, want unsupported content part: container_upload; body = %s", err, body)
			}
			if !gjson.ValidBytes(body) {
				t.Fatalf("body is not JSON: %q", body)
			}
		})
	}
}

func TestConvertClaudeRequestToCodex_NonEmptyTextBesideAttachmentStillSucceeds(t *testing.T) {
	input := []byte(`{"model":"m","messages":[{"role":"user","content":[{"type":"text","text":""},{"type":"text","text":"keep me"},` + userTurnUpload + `]}]}`)
	body, err := ConvertClaudeRequestToCodexWithCompat("m", input, false)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got := gjson.GetBytes(body, "input.0.content.1.text").String(); got != "keep me" {
		t.Fatalf("text was lost: %s", body)
	}
}

// claudeImageTurnToCodex translates a conversation of two history turns followed by
// a final user turn holding the given content parts.
func claudeImageTurnToCodex(finalContent string) sdktranslator.RequestEnvelope {
	input := `{"model":"m","messages":[{"role":"user","content":"a"},{"role":"assistant","content":"b"},{"role":"user","content":[` + finalContent + `]}]}`
	return sdktranslator.TranslateRequestEnvelope(context.Background(), sdktranslator.FormatClaude, sdktranslator.FormatCodex, sdktranslator.RequestEnvelope{
		Format: sdktranslator.FormatClaude,
		Model:  "m",
		Body:   []byte(input),
	})
}

func TestClaudeToCodexImageOnlyTurnKeepsTheImage(t *testing.T) {
	cases := []struct {
		name   string
		source string
		field  string
		want   string
	}{
		{
			name:   "base64",
			source: `{"type":"base64","media_type":"image/png","data":"aGVsbG8="}`,
			field:  "image_url",
			want:   "data:image/png;base64,aGVsbG8=",
		},
		{
			name:   "http url",
			source: `{"type":"url","url":"https://example.test/a.png"}`,
			field:  "image_url",
			want:   "https://example.test/a.png",
		},
		{
			name:   "file id",
			source: `{"type":"file","file_id":"file-1"}`,
			field:  "file_id",
			want:   "file-1",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			envelope := claudeImageTurnToCodex(`{"type":"image","source":` + tc.source + `}`)
			if envelope.Err != nil {
				t.Fatalf("envelope.Err = %v, body = %s", envelope.Err, envelope.Body)
			}
			part := gjson.GetBytes(envelope.Body, "input.2.content.0")
			if part.Get("type").String() != "input_image" || part.Get(tc.field).String() != tc.want {
				t.Fatalf("part = %s, body = %s", part.Raw, envelope.Body)
			}
		})
	}
}

func TestClaudeToCodexUnrepresentableImageOnlyTurnIsRefused(t *testing.T) {
	cases := map[string]string{
		"non http url":   `{"type":"url","url":"ftp://example.test/a.png"}`,
		"empty url":      `{"type":"url","url":""}`,
		"file no id":     `{"type":"file"}`,
		"unknown kind":   `{"type":"unknown"}`,
		"base64 no data": `{"type":"base64","media_type":"image/png","data":""}`,
	}
	for name, source := range cases {
		t.Run(name, func(t *testing.T) {
			envelope := claudeImageTurnToCodex(`{"type":"image","source":` + source + `}`)
			var unsupported *translatorcommon.UnsupportedPartError
			if !errors.As(envelope.Err, &unsupported) || unsupported.Type != "image" || unsupported.StatusCode() != 400 {
				t.Fatalf("envelope.Err = %v, want unsupported content part: image; body = %s", envelope.Err, envelope.Body)
			}
			if got, want := envelope.Err.Error(), "unsupported content part: image"; got != want {
				t.Fatalf("error text = %q, want %q", got, want)
			}
		})
	}
}

func TestClaudeToCodexUnrepresentableImageBesideTextStillSucceeds(t *testing.T) {
	envelope := claudeImageTurnToCodex(`{"type":"text","text":"keep me"},{"type":"image","source":{"type":"file"}}`)
	if envelope.Err != nil {
		t.Fatalf("envelope.Err = %v, body = %s", envelope.Err, envelope.Body)
	}
	if got := gjson.GetBytes(envelope.Body, "input.2.content.0.text").String(); got != "keep me" {
		t.Fatalf("text was lost: %s", envelope.Body)
	}
}

func TestClaudeToCodexEmptyTextDoesNotHideAnUnrepresentableImage(t *testing.T) {
	envelope := claudeImageTurnToCodex(`{"type":"text","text":""},{"type":"image","source":{"type":"file"}}`)
	var unsupported *translatorcommon.UnsupportedPartError
	if !errors.As(envelope.Err, &unsupported) || unsupported.Type != "image" {
		t.Fatalf("envelope.Err = %v, want unsupported content part: image; body = %s", envelope.Err, envelope.Body)
	}
}
