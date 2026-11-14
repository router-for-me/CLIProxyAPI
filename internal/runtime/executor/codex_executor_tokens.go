package executor

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tiktoken-go/tokenizer"
)

func (e *CodexExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	ctx = helps.WithCodexCommentaryPolicy(ctx, e.cfg)
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	if _, errExpand := e.expandCodexResponsesCompaction(auth, &req, &opts, baseModel); errExpand != nil {
		return cliproxyexecutor.Response{}, statusErr{code: http.StatusBadRequest, msg: errExpand.Error()}
	}

	from := opts.SourceFormat
	responseFormat := cliproxyexecutor.ResponseFormatOrSource(opts)
	to := sdktranslator.FromString("codex")
	body, updatesChanged, err := helps.TranslateRequestWithAPIKeyModelCompatibilityAndUpdateIntentForExecutor(ctx, opts.Headers, e.cfg, e.Identifier(), from, to, baseModel, req.Payload, false, helps.APIKeyModelIsCompat(req))
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}
	originalTranslatedForPayload := append([]byte(nil), body...)
	if len(opts.OriginalRequest) > 0 {
		originalTranslatedForPayload, _, _ = helps.TranslateRequestWithAPIKeyModelCompatibilityAndUpdateIntentForExecutor(ctx, opts.Headers, e.cfg, e.Identifier(), from, to, baseModel, opts.OriginalRequest, false, helps.APIKeyModelIsCompat(req))
	}

	doc := ojson.NewDoc(body)
	if err = helps.ApplyRequestThinkingDoc(doc, req, opts, from.String(), to.String(), e.Identifier(), updatesChanged); err != nil {
		return cliproxyexecutor.Response{}, err
	}

	helps.SetStringIfDifferentDoc(doc, "model", baseModel)
	_ = doc.Delete("previous_response_id")
	_ = doc.Delete("generate")
	_ = doc.Delete("prompt_cache_retention")
	_ = doc.Delete("safety_identifier")
	_ = doc.Delete("stream_options")
	helps.SetBoolIfDifferentDoc(doc, "stream", false)
	normalizeCodexInstructionsDoc(doc, helps.IsNativeCodexRequest(req.Payload, opts))

	ctx = helps.WithPayloadFinalizer(ctx, helps.NewPayloadFinalizer(e.cfg, e.Identifier(), baseModel, to.String(), "", originalTranslatedForPayload, req, opts))
	helps.FinalizePayloadDoc(ctx, doc)
	body = doc.Bytes()
	enc, err := tokenizerForCodexModel(baseModel)
	if err != nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("codex executor: tokenizer init failed: %w", err)
	}

	count, err := helps.CountResponsesInputTokens(enc, body)
	if err != nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("codex executor: token counting failed: %w", err)
	}

	usageJSON := fmt.Sprintf(`{"response":{"usage":{"input_tokens":%d,"output_tokens":0,"total_tokens":%d}}}`, count, count)
	translated := sdktranslator.TranslateTokenCount(ctx, to, responseFormat, count, []byte(usageJSON))
	return cliproxyexecutor.Response{Payload: translated}, nil
}

func tokenizerForCodexModel(model string) (tokenizer.Codec, error) {
	sanitized := strings.ToLower(strings.TrimSpace(model))
	switch {
	case sanitized == "":
		return tokenizer.Get(tokenizer.Cl100kBase)
	case strings.HasPrefix(sanitized, "gpt-5"):
		return tokenizer.ForModel(tokenizer.GPT5)
	case strings.HasPrefix(sanitized, "gpt-4.1"):
		return tokenizer.ForModel(tokenizer.GPT41)
	case strings.HasPrefix(sanitized, "gpt-4o"):
		return tokenizer.ForModel(tokenizer.GPT4o)
	case strings.HasPrefix(sanitized, "gpt-4"):
		return tokenizer.ForModel(tokenizer.GPT4)
	case strings.HasPrefix(sanitized, "gpt-3.5"), strings.HasPrefix(sanitized, "gpt-3"):
		return tokenizer.ForModel(tokenizer.GPT35Turbo)
	default:
		return tokenizer.Get(tokenizer.Cl100kBase)
	}
}
