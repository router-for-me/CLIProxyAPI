package gemini

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
	geminiAudioPart = `{"inlineData":{"mimeType":"audio/wav","data":"UklGRg=="}}`
	geminiVideoPart = `{"inline_data":{"mime_type":"video/mp4","data":"AAAAIGZ0eXA="}}`
)

// geminiTurnToClaude translates a user turn followed by a model turn and a final
// user turn holding the given parts.
func geminiTurnToClaude(finalParts string) sdktranslator.RequestEnvelope {
	input := `{"contents":[{"role":"user","parts":[{"text":"a"}]},{"role":"model","parts":[{"text":"b"}]},{"role":"user","parts":[` + finalParts + `]}]}`
	return sdktranslator.TranslateRequestEnvelope(context.Background(), sdktranslator.FormatGemini, sdktranslator.FormatClaude, sdktranslator.RequestEnvelope{
		Format: sdktranslator.FormatGemini,
		Model:  "claude-sonnet-4",
		Body:   []byte(input),
	})
}

func requireInlineDataRefusal(t *testing.T, envelope sdktranslator.RequestEnvelope) {
	t.Helper()
	var unsupported *translatorcommon.UnsupportedPartError
	if !errors.As(envelope.Err, &unsupported) || unsupported.Type != "inlineData" || unsupported.StatusCode() != 400 {
		t.Fatalf("envelope.Err = %v, want unsupported content part: inlineData; body = %s", envelope.Err, envelope.Body)
	}
	if got, want := envelope.Err.Error(), "unsupported content part: inlineData"; got != want {
		t.Fatalf("error text = %q, want %q", got, want)
	}
	if !gjson.ValidBytes(envelope.Body) {
		t.Fatalf("body is not JSON: %q", envelope.Body)
	}
}

func TestGeminiToClaudeAudioOnlyUserTurnIsRefusedInsteadOfBecomingPlaceholderText(t *testing.T) {
	cases := map[string]string{
		"audio":                      geminiAudioPart,
		"video":                      geminiVideoPart,
		"audio and video":            geminiAudioPart + `,` + geminiVideoPart,
		"empty text beside audio":    `{"text":""},` + geminiAudioPart,
		"whitespace beside audio":    `{"text":"  \n"},` + geminiAudioPart,
		"audio beside whitespace":    geminiAudioPart + `,{"text":" "}`,
		"inline data without a mime": `{"inlineData":{"data":"UklGRg=="}}`,
	}
	for name, parts := range cases {
		t.Run(name, func(t *testing.T) {
			envelope := geminiTurnToClaude(parts)
			requireInlineDataRefusal(t, envelope)
			if strings.Contains(string(envelope.Body), "Media content") {
				t.Fatalf("placeholder text was synthesized: %s", envelope.Body)
			}
		})
	}
}

func TestGeminiToClaudeRefusesAnEmptiedUserTurnBeforeALaterTextTurn(t *testing.T) {
	input := `{"contents":[{"role":"user","parts":[` + geminiAudioPart + `]},{"role":"model","parts":[{"text":"ok"}]},{"role":"user","parts":[{"text":"next"}]}]}`
	envelope := sdktranslator.TranslateRequestEnvelope(context.Background(), sdktranslator.FormatGemini, sdktranslator.FormatClaude, sdktranslator.RequestEnvelope{
		Format: sdktranslator.FormatGemini,
		Model:  "claude-sonnet-4",
		Body:   []byte(input),
	})
	requireInlineDataRefusal(t, envelope)
}

func TestGeminiToClaudeRealTextBesideAudioIsSentWithoutAPlaceholder(t *testing.T) {
	envelope := geminiTurnToClaude(`{"text":"  "},{"text":"keep me"},` + geminiAudioPart)
	if envelope.Err != nil {
		t.Fatalf("envelope.Err = %v, body = %s", envelope.Err, envelope.Body)
	}
	if strings.Contains(string(envelope.Body), "Media content") {
		t.Fatalf("placeholder text was synthesized: %s", envelope.Body)
	}
	found := false
	for _, block := range gjson.GetBytes(envelope.Body, "messages.2.content").Array() {
		if block.Get("text").String() == "keep me" {
			found = true
		}
	}
	if !found {
		t.Fatalf("text was lost: %s", envelope.Body)
	}
}

func TestGeminiToClaudeAudioBesideADocumentOrToolResultStillSucceeds(t *testing.T) {
	cases := map[string]string{
		"document":    geminiAudioPart + `,{"inlineData":{"mimeType":"application/pdf","data":"JVBERi0="}}`,
		"tool result": geminiAudioPart + `,{"functionResponse":{"name":"f","response":{"result":"ok"}}}`,
	}
	for name, parts := range cases {
		t.Run(name, func(t *testing.T) {
			envelope := geminiTurnToClaude(parts)
			if envelope.Err != nil {
				t.Fatalf("envelope.Err = %v, body = %s", envelope.Err, envelope.Body)
			}
		})
	}
}

func TestGeminiToClaudeModelAudioKeepsItsPlaceholder(t *testing.T) {
	input := `{"contents":[{"role":"user","parts":[{"text":"a"}]},{"role":"model","parts":[` + geminiAudioPart + `]},{"role":"user","parts":[{"text":"b"}]}]}`
	envelope := sdktranslator.TranslateRequestEnvelope(context.Background(), sdktranslator.FormatGemini, sdktranslator.FormatClaude, sdktranslator.RequestEnvelope{
		Format: sdktranslator.FormatGemini,
		Model:  "claude-sonnet-4",
		Body:   []byte(input),
	})
	if envelope.Err != nil {
		t.Fatalf("envelope.Err = %v, body = %s", envelope.Err, envelope.Body)
	}
	if got := gjson.GetBytes(envelope.Body, "messages.1.content.0.text").String(); got != "Media content: inline data (Type: audio/wav)" {
		t.Fatalf("assistant placeholder = %q, body = %s", got, envelope.Body)
	}
}

func TestGeminiToClaudeExportedWrapperKeepsAJSONBody(t *testing.T) {
	input := []byte(`{"contents":[{"role":"user","parts":[` + geminiAudioPart + `]}]}`)
	if body, _ := ConvertGeminiRequestToClaude("claude-sonnet-4", input, false); !gjson.ValidBytes(body) {
		t.Fatalf("body is not JSON: %q", body)
	}
}
