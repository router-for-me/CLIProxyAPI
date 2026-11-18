package claude

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
	translatorcommon "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/common"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
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
			if !ojson.ValidBytes(body) {
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
	if got := ojson.GetBytesNoCopy(body, "input.2.content.0.text").String(); got != "keep me" {
		t.Fatalf("text was lost: %s", body)
	}
}

func TestConvertClaudeRequestToInteractions_Base64DocumentAfterHistoryStaysMedia(t *testing.T) {
	input := []byte(`{"model":"m","messages":[{"role":"user","content":"hello"},{"role":"assistant","content":"hi"},{"role":"user","content":[{"type":"document","source":{"type":"base64","media_type":"application/pdf","data":"JVBERi0xLjQK"}}]}]}`)
	body, err := ConvertClaudeRequestToInteractionsWithCompat("m", input, false)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	part := ojson.GetBytesNoCopy(body, "input.2.content.0")
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
	ojson.GetBytesNoCopy(body, "input.0.content").ForEach(func(_, part ojson.Result) bool {
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

func TestClaudeToInteractionsDocumentTextContentAndURLSources(t *testing.T) {
	translate := func(finalContent string) sdktranslator.RequestEnvelope {
		input := `{"model":"m","messages":[{"role":"user","content":"a"},{"role":"assistant","content":"b"},{"role":"user","content":[` + finalContent + `]}]}`
		return sdktranslator.TranslateRequestEnvelope(context.Background(), sdktranslator.FormatClaude, sdktranslator.FormatInteractions, sdktranslator.RequestEnvelope{
			Format: sdktranslator.FormatClaude,
			Model:  "m",
			Body:   []byte(input),
		})
	}
	envelope := translate(`{"type":"document","title":"note.txt","context":"secret note","source":{"type":"text","media_type":"text/plain","data":"Secret codeword: ORANGE-42"}},{"type":"text","text":"What is the secret codeword?"}`)
	if envelope.Err != nil {
		t.Fatalf("envelope.Err = %v, body = %s", envelope.Err, envelope.Body)
	}
	parts := ojson.GetBytesNoCopy(envelope.Body, "input.2.content").Array()
	if len(parts) != 2 {
		t.Fatalf("got %d parts, want 2: %s", len(parts), envelope.Body)
	}
	if !strings.Contains(parts[0].Get("text").String(), "ORANGE-42") || !strings.Contains(parts[0].Get("text").String(), "note.txt") {
		t.Fatalf("document text was dropped: %s", envelope.Body)
	}

	contentEnv := translate(`{"type":"document","title":"bundle","context":"mixed","source":{"type":"content","content":[{"type":"text","text":"inside doc"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aGVsbG8="}}]}}`)
	if contentEnv.Err != nil {
		t.Fatalf("content document err = %v, body = %s", contentEnv.Err, contentEnv.Body)
	}
	cParts := ojson.GetBytesNoCopy(contentEnv.Body, "input.2.content").Array()
	var sawText, sawImage bool
	for _, p := range cParts {
		if strings.Contains(p.Get("text").String(), "inside doc") {
			sawText = true
		}
		if p.Get("type").String() == "image" && p.Get("mime_type").String() == "image/png" && p.Get("data").String() == "aGVsbG8=" {
			sawImage = true
		}
	}
	if !sawText || !sawImage {
		t.Fatalf("content document lost parts (sawText=%v, sawImage=%v): %s", sawText, sawImage, contentEnv.Body)
	}

	urlDocEnv := translate(`{"type":"document","source":{"type":"url","url":"https://example.test/spec.pdf"}}`)
	if urlDocEnv.Err != nil {
		t.Fatalf("url document err = %v, body = %s", urlDocEnv.Err, urlDocEnv.Body)
	}
	uDoc := ojson.GetBytesNoCopy(urlDocEnv.Body, "input.2.content.0")
	if uDoc.Get("type").String() != "document" || uDoc.Get("uri").String() != "https://example.test/spec.pdf" || uDoc.Get("mime_type").String() != "application/pdf" {
		t.Fatalf("url document part = %s, body = %s", uDoc.Raw, urlDocEnv.Body)
	}

	urlImgEnv := translate(`{"type":"image","source":{"type":"url","url":"https://example.test/a.png"}}`)
	if urlImgEnv.Err != nil {
		t.Fatalf("url image err = %v, body = %s", urlImgEnv.Err, urlImgEnv.Body)
	}
	uImg := ojson.GetBytesNoCopy(urlImgEnv.Body, "input.2.content.0")
	if uImg.Get("type").String() != "image" || uImg.Get("uri").String() != "https://example.test/a.png" {
		t.Fatalf("url image part = %s, body = %s", uImg.Raw, urlImgEnv.Body)
	}
}
