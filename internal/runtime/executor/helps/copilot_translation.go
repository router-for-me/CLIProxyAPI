package helps

import (
	"context"
	"net/http"

	"github.com/MoeForge/moego/ojson"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	codexclaude "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/codex/claude"
	codexchat "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/codex/openai/chat-completions"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/translator/common"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// These are standard Responses conversions, not Codex wire defaults. Only a
// Copilot attempt selects them; SDK and plugin capability discovery stays unchanged.
var copilotResponses = func() *translator.Registry {
	r := translator.NewRegistryWithDefaultPluginHooks()
	for _, pair := range []struct {
		source  translator.Format
		request translator.RequestTransform
		stream  translator.ResponseStreamTransform
		flat    translator.ResponseNonStreamTransform
	}{
		{translator.FormatOpenAI, codexchat.ConvertOpenAIRequestToResponses, codexchat.ConvertCodexResponseToOpenAI, codexchat.ConvertCodexResponseToOpenAINonStream},
		{translator.FormatClaude, codexclaude.ConvertClaudeRequestToResponses, codexclaude.ConvertCodexResponseToClaude, codexclaude.ConvertCodexResponseToClaudeNonStream},
	} {
		r.Register(pair.source, translator.FormatOpenAIResponse, pair.request, translator.ResponseTransform{Stream: pair.stream, NonStream: func(ctx context.Context, model string, original, request, response []byte, state *any) []byte {
			return pair.flat(ctx, model, original, request, common.WrapResponsesTerminalEvent(response), state)
		}})
	}
	return r
}()

// Request and response formats may be chosen independently. Registry keys
// are always (client format, upstream format), even for the return path.
func CopilotTranslationRegistry(client, upstream translator.Format) *translator.Registry {
	if upstream == translator.FormatOpenAIResponse && copilotResponses.HasRequestTransformer(client, upstream) {
		return copilotResponses
	}
	return translator.Default()
}

func TranslateCopilotRequestPair(ctx context.Context, r *translator.Registry, headers http.Header, cfg *config.Config, from, to translator.Format, model string, original, working []byte, stream bool) ([]byte, []byte, bool, error) {
	return translateRequestPair(original, working, func(body []byte) ([]byte, bool, error) {
		if r != copilotResponses {
			out, changed, err := TranslateRequestWithAPIKeyModelCompatibilityAndUpdateIntent(ctx, headers, cfg, from, to, model, body, stream, false)
			if err == nil && to == translator.FormatClaude && (from == translator.FormatOpenAI || from == translator.FormatOpenAIResponse) {
				// The shared Claude translators synthesize a Claude Code user ID. Copilot
				// already identifies the session by header. Keep caller and plugin IDs.
				src := ojson.ParseBytes(body)
				if !src.Get("metadata.user_id").Exists() && !src.Get("user").Exists() && ojson.GetBytesNoCopy(out, "metadata.user_id").String() == common.DeriveClaudeUserID(body) {
					d := ojson.NewDoc(out)
					_ = d.Delete("metadata.user_id")
					if len(d.Get("metadata").Map()) == 0 {
						_ = d.Delete("metadata")
					}
					out = d.Bytes()
				}
			}
			return out, changed, err
		}
		// Neither private route takes Codex/Responses input.
		if IsCodexUserAgent(headers) {
			body = NormalizeCodexToolIntegerTypes(body, headers)
		}
		req := r.TranslateRequestEnvelope(ctx, from, to, translator.RequestEnvelope{Format: from, Model: model, Body: body, Stream: stream})
		return req.Body, req.ConfigurationUpdatesChanged, req.Err
	})
}
