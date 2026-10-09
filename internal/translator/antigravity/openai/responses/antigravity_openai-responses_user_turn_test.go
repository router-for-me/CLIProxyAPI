package responses

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	translatorcommon "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/common"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

const (
	antigravityTurnHello     = `{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}`
	antigravityTurnAssistant = `{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hi"}]}`
	antigravityTurnNext      = `{"type":"message","role":"user","content":[{"type":"input_text","text":"next"}]}`
	antigravityTurnDeveloper = `{"type":"message","role":"developer","content":[{"type":"input_text","text":"dev"}]}`
	antigravityTurnFileID    = `{"type":"input_file","file_id":"file-1"}`
	antigravityTurnFileData  = `{"type":"input_file","filename":"a.pdf","file_data":"data:application/pdf;base64,JVBERi0xLjQK"}`
	antigravityTurnText      = `{"type":"input_text","text":"keep me"}`
)

func antigravityUserTurn(parts ...string) string {
	return `{"type":"message","role":"user","content":[` + strings.Join(parts, ",") + `]}`
}

func antigravityTurnPayload(extra string, items ...string) []byte {
	return []byte(`{"model":"gemini-3-flash",` + extra + `"input":[` + strings.Join(items, ",") + `]}`)
}

func TestOpenAIResponsesToAntigravityRegistryCarriesTheRefusal(t *testing.T) {
	cases := []struct {
		name  string
		input []byte
		info  *registry.ModelInfo
	}{
		{
			name:  "history then file id only",
			input: antigravityTurnPayload("", antigravityTurnHello, antigravityTurnAssistant, antigravityUserTurn(antigravityTurnFileID)),
		},
		{
			name:  "instructions and developer prompt do not hide the empty turn",
			input: antigravityTurnPayload(`"instructions":"sys",`, antigravityTurnDeveloper, antigravityUserTurn(antigravityTurnFileID)),
		},
		{
			name:  "emptied turn before a later text turn",
			input: antigravityTurnPayload("", antigravityUserTurn(antigravityTurnFileID), antigravityTurnAssistant, antigravityTurnNext),
		},
		{
			name:  "native web search request",
			input: antigravityTurnPayload(`"tools":[{"type":"web_search"}],`, antigravityTurnHello, antigravityTurnAssistant, antigravityUserTurn(antigravityTurnFileID)),
			info: func() *registry.ModelInfo {
				enabled := true
				return &registry.ModelInfo{ID: "gemini-3-flash", NativeCapabilities: &registry.NativeCapabilities{WebSearch: &enabled}}
			}(),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			envelope := sdktranslator.TranslateRequestEnvelope(context.Background(), sdktranslator.FormatOpenAIResponse, sdktranslator.FormatAntigravity,
				sdktranslator.RequestEnvelope{Format: sdktranslator.FormatOpenAIResponse, Model: "gemini-3-flash", Body: tc.input, ModelInfo: tc.info})
			var unsupported *translatorcommon.UnsupportedPartError
			if !errors.As(envelope.Err, &unsupported) || unsupported.Type != "input_file" || unsupported.StatusCode() != 400 {
				t.Fatalf("envelope.Err = %v, want unsupported content part: input_file; body = %s", envelope.Err, envelope.Body)
			}
			if !gjson.ValidBytes(envelope.Body) {
				t.Fatalf("body is not JSON: %q", envelope.Body)
			}

			direct := ConvertOpenAIResponsesRequestEnvelopeToAntigravity(context.Background(),
				sdktranslator.RequestEnvelope{Model: "gemini-3-flash", Body: tc.input, ModelInfo: tc.info})
			if !errors.As(direct.Err, &unsupported) || unsupported.Type != "input_file" {
				t.Fatalf("direct err = %v, want unsupported content part: input_file", direct.Err)
			}
		})
	}
}

func TestOpenAIResponsesToAntigravityKeepsTurnWithTextBesideFileID(t *testing.T) {
	input := antigravityTurnPayload("", antigravityTurnHello, antigravityTurnAssistant, antigravityUserTurn(antigravityTurnText, antigravityTurnFileID))
	envelope := sdktranslator.TranslateRequestEnvelope(context.Background(), sdktranslator.FormatOpenAIResponse, sdktranslator.FormatAntigravity,
		sdktranslator.RequestEnvelope{Format: sdktranslator.FormatOpenAIResponse, Model: "gemini-3-flash", Body: input})
	if envelope.Err != nil {
		t.Fatalf("envelope.Err = %v", envelope.Err)
	}
	if got := gjson.GetBytes(envelope.Body, "request.contents.2.parts.0.text").String(); got != "keep me" {
		t.Fatalf("text was lost: %s", envelope.Body)
	}
}

func TestOpenAIResponsesToAntigravityInlineFileStaysInlineData(t *testing.T) {
	input := antigravityTurnPayload("", antigravityTurnHello, antigravityTurnAssistant, antigravityUserTurn(antigravityTurnFileData))
	envelope := sdktranslator.TranslateRequestEnvelope(context.Background(), sdktranslator.FormatOpenAIResponse, sdktranslator.FormatAntigravity,
		sdktranslator.RequestEnvelope{Format: sdktranslator.FormatOpenAIResponse, Model: "gemini-3-flash", Body: input})
	if envelope.Err != nil {
		t.Fatalf("envelope.Err = %v", envelope.Err)
	}
	if got := gjson.GetBytes(envelope.Body, "request.contents.2.parts.0.inline_data.mime_type").String(); got != "application/pdf" {
		t.Fatalf("inline file was not kept: %s", envelope.Body)
	}
}

func TestConvertOpenAIResponsesRequestToAntigravity_ExportedWrapperKeepsAJSONBody(t *testing.T) {
	input := antigravityTurnPayload("", antigravityUserTurn(antigravityTurnFileID))
	if body, _ := ConvertOpenAIResponsesRequestToAntigravity("gemini-3-flash", input, false); !gjson.ValidBytes(body) {
		t.Fatalf("body is not JSON: %q", body)
	}
}
