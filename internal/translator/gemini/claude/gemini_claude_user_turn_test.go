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

func TestConvertClaudeRequestToGemini_RefusesAnyEmptiedUserTurn(t *testing.T) {
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
			body, err := ConvertClaudeRequestToGeminiWithCompat("m", []byte(tc.input), false)
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

func TestConvertClaudeRequestToGemini_KeepsTurnWithTextBesideAttachment(t *testing.T) {
	input := []byte(`{"model":"m","messages":[{"role":"user","content":"hello"},{"role":"assistant","content":"hi"},{"role":"user","content":[{"type":"text","text":"keep me"},` + userTurnUpload + `]}]}`)
	body, err := ConvertClaudeRequestToGeminiWithCompat("m", input, false)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got := gjson.GetBytes(body, "contents.2.parts.0.text").String(); got != "keep me" {
		t.Fatalf("text was lost: %s", body)
	}
}

func TestConvertClaudeRequestToGemini_Base64DocumentAfterHistoryStaysInlineData(t *testing.T) {
	input := []byte(`{"model":"m","messages":[{"role":"user","content":"hello"},{"role":"assistant","content":"hi"},{"role":"user","content":[{"type":"document","source":{"type":"base64","media_type":"application/pdf","data":"JVBERi0xLjQK"}}]}]}`)
	body, err := ConvertClaudeRequestToGeminiWithCompat("m", input, false)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	part := gjson.GetBytes(body, "contents.2.parts.0.inline_data")
	if part.Get("mime_type").String() != "application/pdf" || part.Get("data").String() != "JVBERi0xLjQK" {
		t.Fatalf("part = %s, body = %s", part.Raw, body)
	}
}

// claudeImageTurnToGemini translates a conversation of two history turns followed by a final user
// turn holding the given content parts.
func claudeImageTurnToGemini(finalContent string) sdktranslator.RequestEnvelope {
	input := `{"model":"m","messages":[{"role":"user","content":"a"},{"role":"assistant","content":"b"},{"role":"user","content":[` + finalContent + `]}]}`
	return sdktranslator.TranslateRequestEnvelope(context.Background(), sdktranslator.FormatClaude, sdktranslator.FormatGemini, sdktranslator.RequestEnvelope{
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

func TestClaudeToGeminiUnrepresentableImageOnlyTurnIsRefused(t *testing.T) {
	cases := map[string]string{
		"url source":  `{"type":"url","url":"https://example.test/a.png"}`,
		"file source": `{"type":"file","file_id":"file-1"}`,
	}
	for name, source := range cases {
		t.Run(name, func(t *testing.T) {
			requireImageRefusal(t, claudeImageTurnToGemini(`{"type":"image","source":`+source+`}`))
		})
	}
}

func TestClaudeToGeminiUnrepresentableImageBesideTextStillSucceeds(t *testing.T) {
	for _, source := range []string{`{"type":"url","url":"https://example.test/a.png"}`, `{"type":"file","file_id":"file-1"}`} {
		envelope := claudeImageTurnToGemini(`{"type":"text","text":"keep me"},{"type":"image","source":` + source + `}`)
		if envelope.Err != nil {
			t.Fatalf("envelope.Err = %v, body = %s", envelope.Err, envelope.Body)
		}
		parts := gjson.GetBytes(envelope.Body, "contents.2.parts").Array()
		if len(parts) != 1 || parts[0].Get("text").String() != "keep me" {
			t.Fatalf("text was lost or an image leaked: %s", envelope.Body)
		}
	}
}

func TestClaudeToGeminiEmptyTextDoesNotHideAnUnrepresentableImage(t *testing.T) {
	requireImageRefusal(t, claudeImageTurnToGemini(`{"type":"text","text":""},{"type":"image","source":{"type":"url","url":"https://example.test/a.png"}}`))
}

func TestClaudeToGeminiBase64ImageOnlyTurnStaysInlineData(t *testing.T) {
	envelope := claudeImageTurnToGemini(`{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aGVsbG8="}}`)
	if envelope.Err != nil {
		t.Fatalf("envelope.Err = %v, body = %s", envelope.Err, envelope.Body)
	}
	inlineData := gjson.GetBytes(envelope.Body, "contents.2.parts.0.inline_data")
	if inlineData.Get("mime_type").String() != "image/png" || inlineData.Get("data").String() != "aGVsbG8=" {
		t.Fatalf("inlineData = %s, body = %s", inlineData.Raw, envelope.Body)
	}
}

func TestClaudeToGeminiWhitespaceTextDoesNotHideAnUnrepresentableImage(t *testing.T) {
	for _, text := range []string{"  ", ` \n\t`, `\u00a0`} {
		requireImageRefusal(t, claudeImageTurnToGemini(`{"type":"text","text":"`+text+`"},{"type":"image","source":{"type":"file","file_id":"file-1"}}`))
	}
	requireImageRefusal(t, claudeImageTurnToGemini(`{"type":"image","source":{"type":"url","url":"https://example.test/a.png"}},{"type":"text","text":"  "}`))
}

func TestClaudeToGeminiRealTextBesideWhitespaceAndAnImageStillSucceeds(t *testing.T) {
	envelope := claudeImageTurnToGemini(`{"type":"text","text":"  "},{"type":"text","text":"keep me"},{"type":"image","source":{"type":"file","file_id":"file-1"}}`)
	if envelope.Err != nil {
		t.Fatalf("envelope.Err = %v, body = %s", envelope.Err, envelope.Body)
	}
	found := false
	for _, part := range gjson.GetBytes(envelope.Body, "contents.2.parts").Array() {
		if part.Get("text").String() == "keep me" {
			found = true
		}
	}
	if !found {
		t.Fatalf("text was lost: %s", envelope.Body)
	}
}
