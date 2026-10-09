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

func TestConvertClaudeRequestToOpenAI_RefusesAnyEmptiedUserTurn(t *testing.T) {
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
			body, err := ConvertClaudeRequestToOpenAIWithCompat("m", []byte(tc.input), false)
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

func TestConvertClaudeRequestToOpenAI_KeepsTurnWithTextBesideAttachment(t *testing.T) {
	input := []byte(`{"model":"m","messages":[{"role":"user","content":"hello"},{"role":"assistant","content":"hi"},{"role":"user","content":[{"type":"text","text":"keep me"},` + userTurnUpload + `]}]}`)
	body, err := ConvertClaudeRequestToOpenAIWithCompat("m", input, false)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got := gjson.GetBytes(body, "messages.2.content.0.text").String(); got != "keep me" {
		t.Fatalf("text was lost: %s", body)
	}
}

func TestConvertClaudeRequestToOpenAI_Base64DocumentAfterHistoryStaysAFilePart(t *testing.T) {
	input := []byte(`{"model":"m","messages":[{"role":"user","content":"hello"},{"role":"assistant","content":"hi"},{"role":"user","content":[{"type":"document","source":{"type":"base64","media_type":"application/pdf","data":"JVBERi0xLjQK"}}]}]}`)
	body, err := ConvertClaudeRequestToOpenAIWithCompat("m", input, false)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	part := gjson.GetBytes(body, "messages.2.content.0")
	if part.Get("type").String() != "file" || part.Get("file.file_data").String() != "data:application/pdf;base64,JVBERi0xLjQK" {
		t.Fatalf("part = %s, body = %s", part.Raw, body)
	}
}

func TestConvertClaudeRequestToOpenAI_ToolResultKeepsTurnBesideUnsendableFile(t *testing.T) {
	input := []byte(`{"model":"m","messages":[{"role":"user","content":"run"},{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"t","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"done"},` + userTurnUpload + `]}]}`)
	body, err := ConvertClaudeRequestToOpenAIWithCompat("m", input, false)
	if err != nil {
		t.Fatalf("a tool result is a sendable part, err = %v; body = %s", err, body)
	}
}

// claudeImageTurnToOpenAI translates a conversation of two history turns followed by a final user
// turn holding the given content parts.
func claudeImageTurnToOpenAI(finalContent string) sdktranslator.RequestEnvelope {
	input := `{"model":"m","messages":[{"role":"user","content":"a"},{"role":"assistant","content":"b"},{"role":"user","content":[` + finalContent + `]}]}`
	return sdktranslator.TranslateRequestEnvelope(context.Background(), sdktranslator.FormatClaude, sdktranslator.FormatOpenAI, sdktranslator.RequestEnvelope{
		Format: sdktranslator.FormatClaude,
		Model:  "m",
		Body:   []byte(input),
	})
}

func requireImageRefusal(t *testing.T, envelope sdktranslator.RequestEnvelope) {
	t.Helper()
	var unsupported *translatorcommon.UnsupportedPartError
	if !errors.As(envelope.Err, &unsupported) || unsupported.Type != "image" || unsupported.StatusCode() != 400 {
		t.Fatalf("envelope.Err = %v, want unsupported content part: image; body = %s", envelope.Err, envelope.Body)
	}
	if got, want := envelope.Err.Error(), "unsupported content part: image"; got != want {
		t.Fatalf("error text = %q, want %q", got, want)
	}
}

func TestClaudeToOpenAIImageOnlyTurnKeepsTheImage(t *testing.T) {
	cases := map[string]struct {
		source string
		want   string
	}{
		"base64":   {source: `{"type":"base64","media_type":"image/png","data":"aGVsbG8="}`, want: "data:image/png;base64,aGVsbG8="},
		"http url": {source: `{"type":"url","url":"https://example.test/a.png"}`, want: "https://example.test/a.png"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			envelope := claudeImageTurnToOpenAI(`{"type":"image","source":` + tc.source + `}`)
			if envelope.Err != nil {
				t.Fatalf("envelope.Err = %v, body = %s", envelope.Err, envelope.Body)
			}
			part := gjson.GetBytes(envelope.Body, "messages.2.content.0")
			if part.Get("type").String() != "image_url" || part.Get("image_url.url").String() != tc.want {
				t.Fatalf("part = %s, body = %s", part.Raw, envelope.Body)
			}
		})
	}
}

func TestClaudeToOpenAIFileImageOnlyTurnIsRefused(t *testing.T) {
	requireImageRefusal(t, claudeImageTurnToOpenAI(`{"type":"image","source":{"type":"file","file_id":"file-1"}}`))
}

func TestClaudeToOpenAIFileImageBesideTextStillSucceeds(t *testing.T) {
	envelope := claudeImageTurnToOpenAI(`{"type":"text","text":"keep me"},{"type":"image","source":{"type":"file","file_id":"file-1"}}`)
	if envelope.Err != nil {
		t.Fatalf("envelope.Err = %v, body = %s", envelope.Err, envelope.Body)
	}
	parts := gjson.GetBytes(envelope.Body, "messages.2.content").Array()
	if len(parts) != 1 || parts[0].Get("text").String() != "keep me" {
		t.Fatalf("text was lost or an image leaked: %s", envelope.Body)
	}
}

func TestClaudeToOpenAIEmptyTextDoesNotHideAFileImage(t *testing.T) {
	requireImageRefusal(t, claudeImageTurnToOpenAI(`{"type":"text","text":""},{"type":"image","source":{"type":"file","file_id":"file-1"}}`))
}
