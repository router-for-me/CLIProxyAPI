package helps

import (
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

const (
	chatHistory      = `{"role":"user","content":"hello"},{"role":"assistant","content":"hi"}`
	responsesHistory = `{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hi"}]}`
)

// Every executor reaches the translators through these helpers, so the refusal
// must survive the registry, the multi-agent wrapper and the compat switch.
func TestTranslateRequestReturningErrorRefusesEmptiedAttachmentTurns(t *testing.T) {
	ctx := context.Background()
	routes := []struct {
		name     string
		from, to sdktranslator.Format
		refused  string
		wantType string
		accepted string
	}{
		{
			name:     "openai chat to codex",
			from:     sdktranslator.FormatOpenAI,
			to:       sdktranslator.FormatCodex,
			refused:  `{"model":"m","messages":[` + chatHistory + `,{"role":"user","content":[{"type":"file","file":{"filename":"a.pdf"}}]}]}`,
			accepted: `{"model":"m","messages":[` + chatHistory + `,{"role":"user","content":[{"type":"file","file":{"file_id":"file-1"}}]}]}`,
			wantType: "file",
		},
		{
			name:     "openai chat to interactions",
			from:     sdktranslator.FormatOpenAI,
			to:       sdktranslator.FormatInteractions,
			refused:  `{"model":"m","messages":[` + chatHistory + `,{"role":"user","content":[{"type":"file","file":{"file_id":"file-1"}}]}]}`,
			accepted: `{"model":"m","messages":[` + chatHistory + `,{"role":"user","content":[{"type":"file","file":{"file_data":"data:application/pdf;base64,JVBERi0xLjQK","filename":"a.pdf"}}]}]}`,
			wantType: "file",
		},
		{
			name:     "responses to openai chat",
			from:     sdktranslator.FormatOpenAIResponse,
			to:       sdktranslator.FormatOpenAI,
			refused:  `{"model":"m","input":[` + responsesHistory + `,{"type":"message","role":"user","content":[{"type":"input_file","file_url":"https://example.test/a.pdf"}]}]}`,
			accepted: `{"model":"m","input":[` + responsesHistory + `,{"type":"message","role":"user","content":[{"type":"input_file","file_id":"file-1"}]}]}`,
			wantType: "input_file",
		},
		{
			name:     "responses to gemini",
			from:     sdktranslator.FormatOpenAIResponse,
			to:       sdktranslator.FormatGemini,
			refused:  `{"model":"m","input":[` + responsesHistory + `,{"type":"message","role":"user","content":[{"type":"input_file","file_id":"file-1"}]}]}`,
			accepted: `{"model":"m","input":[` + responsesHistory + `,{"type":"message","role":"user","content":[{"type":"input_file","file_data":"data:application/pdf;base64,JVBERi0xLjQK"}]}]}`,
			wantType: "input_file",
		},
		{
			name:     "responses to antigravity",
			from:     sdktranslator.FormatOpenAIResponse,
			to:       sdktranslator.FormatAntigravity,
			refused:  `{"model":"m","input":[` + responsesHistory + `,{"type":"message","role":"user","content":[{"type":"input_file","file_id":"file-1"}]}]}`,
			accepted: `{"model":"m","input":[` + responsesHistory + `,{"type":"message","role":"user","content":[{"type":"input_file","file_data":"data:application/pdf;base64,JVBERi0xLjQK"}]}]}`,
			wantType: "input_file",
		},
		{
			name:     "responses to interactions",
			from:     sdktranslator.FormatOpenAIResponse,
			to:       sdktranslator.FormatInteractions,
			refused:  `{"model":"m","input":[` + responsesHistory + `,{"type":"message","role":"user","content":[{"type":"input_file","file_id":"file-1"}]}]}`,
			accepted: `{"model":"m","input":[` + responsesHistory + `,{"type":"message","role":"user","content":[{"type":"input_audio","input_audio":{"data":"UklGRg==","format":"wav"}}]}]}`,
			wantType: "input_file",
		},
	}
	for _, route := range routes {
		for _, compat := range []bool{false, true} {
			name := route.name
			if compat {
				name += " compat"
			}
			t.Run(name, func(t *testing.T) {
				body, err := TranslateRequestReturningError(ctx, nil, &config.Config{}, route.from, route.to, "m", []byte(route.refused), false, compat)
				requireUnsupportedPart(t, "single", err, route.wantType)
				if !gjson.ValidBytes(body) {
					t.Fatalf("single: body is not JSON: %q", body)
				}

				_, _, err = TranslateRequestPairReturningError(ctx, nil, &config.Config{}, route.from, route.to, "m", []byte(route.refused), []byte(route.refused), false, compat)
				requireUnsupportedPart(t, "pair", err, route.wantType)

				if _, err = TranslateRequestReturningError(ctx, nil, &config.Config{}, route.from, route.to, "m", []byte(route.accepted), false, compat); err != nil {
					t.Fatalf("representable attachment was refused: %v", err)
				}
			})
		}
	}
}
