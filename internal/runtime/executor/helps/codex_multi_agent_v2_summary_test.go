package helps

import (
	"context"
	"fmt"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestCompatibilityTranslationPreservesExplicitClaudeVisibility(t *testing.T) {
	for _, compat := range []bool{false, true} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("compat=%t/stream=%t", compat, stream), func(t *testing.T) {
				for _, tc := range []struct {
					name    string
					from    sdktranslator.Format
					payload string
					want    string
				}{
					{name: "chat effort only", from: sdktranslator.FormatOpenAI, payload: `{"reasoning_effort":"high","messages":[{"role":"user","content":"hi"}]}`},
					{name: "chat show", from: sdktranslator.FormatOpenAI, payload: `{"reasoning_effort":"high","include_reasoning":true,"messages":[{"role":"user","content":"hi"}]}`, want: "summarized"},
					{name: "chat hide", from: sdktranslator.FormatOpenAI, payload: `{"reasoning_effort":"high","reasoning":{"exclude":true},"messages":[{"role":"user","content":"hi"}]}`, want: "omitted"},
					{name: "responses effort only", from: sdktranslator.FormatOpenAIResponse, payload: `{"reasoning":{"effort":"high"},"input":"hi"}`},
					{name: "responses show", from: sdktranslator.FormatOpenAIResponse, payload: `{"reasoning":{"effort":"high","summary":"auto"},"input":"hi"}`, want: "summarized"},
					{name: "responses hide", from: sdktranslator.FormatOpenAIResponse, payload: `{"reasoning":{"effort":"high","summary":null},"input":"hi"}`, want: "omitted"},
				} {
					t.Run(tc.name, func(t *testing.T) {
						out := TranslateRequestWithAPIKeyModelCompatibility(context.Background(), nil, &config.Config{}, tc.from, sdktranslator.FormatClaude, "claude-opus-5-5", []byte(tc.payload), stream, compat)
						display := gjson.GetBytes(out, "thinking.display")
						if display.String() != tc.want || (tc.want == "" && display.Exists()) {
							t.Fatalf("display=%s, want %q; body=%s", display.Raw, tc.want, out)
						}
					})
				}
			})
		}
	}
}
