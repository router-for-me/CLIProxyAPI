package executor

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type openAICompatResponsesCompactionMode string

const (
	openAICompatResponsesCompactionNone    openAICompatResponsesCompactionMode = "none"
	openAICompatResponsesCompactionAlt     openAICompatResponsesCompactionMode = "alt"
	openAICompatResponsesCompactionTrigger openAICompatResponsesCompactionMode = "trigger"
)

func detectOpenAICompatResponsesCompactionMode(opts cliproxyexecutor.Options, payload, originalPayload []byte) openAICompatResponsesCompactionMode {
	mode := openAICompatResponsesCompactionNone
	if opts.Alt == "responses/compact" {
		mode = openAICompatResponsesCompactionAlt
	}
	if helps.HasResponsesCompactionTrigger(payload) || helps.HasResponsesCompactionTrigger(originalPayload) {
		mode = openAICompatResponsesCompactionTrigger
	}
	return mode
}

// openAICompatCompactionExpandMode selects how compaction items are normalized
// before an openai-compatible request reaches the upstream.
type openAICompatCompactionExpandMode int

const (
	// openAICompatCompactionExpandTranslate is used when CPA translates the request
	// to a chat/completions upstream. Such an upstream can never use a compaction
	// item, so any item that cannot be turned into plain context is removed.
	openAICompatCompactionExpandTranslate openAICompatCompactionExpandMode = iota
	// openAICompatCompactionExpandPassthrough is used when the request is forwarded
	// to a native endpoint such as /responses/compact. An opaque item that is not
	// CPA-sealed may be the upstream's own capsule and must be preserved; only
	// CPA-sealed capsules are removed.
	openAICompatCompactionExpandPassthrough
)

func (e *OpenAICompatExecutor) expandResponsesCompactionPayloads(ctx context.Context, auth *cliproxyauth.Auth, payload, originalPayload []byte, mode openAICompatCompactionExpandMode) ([]byte, []byte) {
	expandedPayload := expandOpenAICompatCompactionPayload(ctx, payload, mode)
	expandedOriginal := expandOpenAICompatCompactionPayload(ctx, originalPayload, mode)
	return expandedPayload, expandedOriginal
}

// expandOpenAICompatCompactionPayload normalizes compaction items in the request
// input. Compaction only affects context completeness, so an item that cannot be
// turned into plain context must never fail the whole request; such items are
// dropped with a warning instead. A CPA-sealed capsule that can be opened is inlined
// as plain text context, and CPA ciphertext is never forwarded verbatim to an
// upstream that cannot decrypt it.
func expandOpenAICompatCompactionPayload(ctx context.Context, payload []byte, mode openAICompatCompactionExpandMode) []byte {
	if len(payload) == 0 {
		return payload
	}
	input := gjson.GetBytes(payload, "input")
	if !input.IsArray() {
		return payload
	}

	items := make([]string, 0, len(input.Array()))
	changed := false
	for _, item := range input.Array() {
		if item.Get("type").String() != "compaction" {
			items = append(items, item.Raw)
			continue
		}

		encryptedContent := item.Get("encrypted_content").String()
		if !helps.IsCPACompactionCapsule(encryptedContent) {
			// Not CPA-sealed: it may be the native upstream's own capsule, which only the
			// passthrough path forwards.
			if mode == openAICompatCompactionExpandPassthrough {
				items = append(items, item.Raw)
				continue
			}
			helps.LogWithRequestID(ctx).Warnf("openai compat compaction: opaque compaction item dropped")
			changed = true
			continue
		}

		// CPA uses one capsule format for every lane, so a capsule sealed by any lane opens here.
		summary, errUnseal := helps.UnsealAntigravityCompaction(encryptedContent)
		if errUnseal != nil {
			helps.LogWithRequestID(ctx).Warnf("openai compat compaction: capsule could not be unsealed, dropping item")
			changed = true
			continue
		}
		contextItem := []byte(`{"type":"message","role":"developer","content":[{"type":"input_text"}]}`)
		contextItem, _ = sjson.SetBytes(contextItem, "content.0.text", "Context summary from previous turns:\n"+summary)
		items = append(items, string(contextItem))
		changed = true
	}

	if !changed {
		return payload
	}
	updated, errSet := sjson.SetRawBytes(payload, "input", []byte("["+strings.Join(items, ",")+"]"))
	if errSet != nil {
		helps.LogWithRequestID(ctx).Warnf("openai compat compaction: could not rewrite input, keeping original payload: %v", errSet)
		return payload
	}
	return updated
}

func (e *OpenAICompatExecutor) executeResponsesCompaction(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, mode openAICompatResponsesCompactionMode) (cliproxyexecutor.Response, error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	payload := req.Payload
	if len(payload) == 0 && len(opts.OriginalRequest) > 0 {
		payload = opts.OriginalRequest
	}
	summaryPayload := helps.PrepareResponsesCompactionSummaryPayload(payload, baseModel)

	summaryReq := cliproxyexecutor.Request{
		Model:    req.Model,
		Payload:  summaryPayload,
		Metadata: req.Metadata,
	}
	summaryOpts := opts
	summaryOpts.Alt = ""
	summaryOpts.Stream = false
	summaryOpts.OriginalRequest = nil
	summaryOpts.SourceFormat = sdktranslator.FormatOpenAIResponse
	summaryOpts.ResponseFormat = sdktranslator.FormatOpenAIResponse

	summaryResp, errSummary := e.Execute(ctx, auth, summaryReq, summaryOpts)
	if errSummary != nil {
		return cliproxyexecutor.Response{}, errSummary
	}

	summaryText, errExtract := helps.ExtractResponsesCompactionSummaryText(summaryResp.Payload)
	if errExtract != nil || strings.TrimSpace(summaryText) == "" {
		if errExtract == nil {
			errExtract = fmt.Errorf("summary text is empty")
		}
		return cliproxyexecutor.Response{}, statusErr{code: http.StatusBadGateway, msg: fmt.Sprintf("openai compat executor: extract compaction summary: %v", errExtract)}
	}

	capsule, errSeal := helps.SealAntigravityCompaction(summaryText, baseModel)
	if errSeal != nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("seal compaction capsule: %w", errSeal)
	}

	inputTokens, outputTokens, totalTokens := openAICompatCompactionUsage(summaryResp.Payload)
	var respBytes []byte
	if mode == openAICompatResponsesCompactionTrigger {
		respBytes = helps.BuildResponsesCompactionTriggerResponse(baseModel, capsule, "compat_compact", inputTokens, outputTokens, totalTokens)
	} else {
		respBytes = helps.BuildResponsesCompactionResponse(baseModel, capsule, "compat_compact", inputTokens, outputTokens, totalTokens)
	}
	return cliproxyexecutor.Response{
		Payload: respBytes,
		Headers: summaryResp.Headers,
	}, nil
}

func (e *OpenAICompatExecutor) executeResponsesCompactionStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	payload := req.Payload
	if len(payload) == 0 && len(opts.OriginalRequest) > 0 {
		payload = opts.OriginalRequest
	}
	summaryPayload := helps.PrepareResponsesCompactionSummaryPayload(payload, baseModel)

	summaryReq := cliproxyexecutor.Request{
		Model:    req.Model,
		Payload:  summaryPayload,
		Metadata: req.Metadata,
	}
	summaryOpts := opts
	summaryOpts.Alt = ""
	summaryOpts.Stream = false
	summaryOpts.OriginalRequest = nil
	summaryOpts.SourceFormat = sdktranslator.FormatOpenAIResponse
	summaryOpts.ResponseFormat = sdktranslator.FormatOpenAIResponse

	summaryResp, errSummary := e.Execute(ctx, auth, summaryReq, summaryOpts)
	if errSummary != nil {
		return nil, errSummary
	}

	summaryText, errExtract := helps.ExtractResponsesCompactionSummaryText(summaryResp.Payload)
	if errExtract != nil || strings.TrimSpace(summaryText) == "" {
		if errExtract == nil {
			errExtract = fmt.Errorf("summary text is empty")
		}
		return nil, statusErr{code: http.StatusBadGateway, msg: fmt.Sprintf("openai compat executor: extract compaction summary: %v", errExtract)}
	}

	capsule, errSeal := helps.SealAntigravityCompaction(summaryText, baseModel)
	if errSeal != nil {
		return nil, fmt.Errorf("seal compaction capsule: %w", errSeal)
	}

	inputTokens, outputTokens, totalTokens := openAICompatCompactionUsage(summaryResp.Payload)
	chunks := helps.BuildResponsesCompactionStreamChunks(baseModel, capsule, "compat_compact", inputTokens, outputTokens, totalTokens)
	out := make(chan cliproxyexecutor.StreamChunk, len(chunks))
	for _, chunk := range chunks {
		out <- cliproxyexecutor.StreamChunk{Payload: chunk}
	}
	close(out)

	headers := summaryResp.Headers.Clone()
	if headers == nil {
		headers = make(http.Header)
	}
	headers.Set("Content-Type", "text/event-stream")
	return &cliproxyexecutor.StreamResult{Headers: headers, Chunks: out}, nil
}

func openAICompatCompactionUsage(payload []byte) (inputTokens, outputTokens, totalTokens int) {
	inputTokens = int(gjson.GetBytes(payload, "usage.input_tokens").Int())
	outputTokens = int(gjson.GetBytes(payload, "usage.output_tokens").Int())
	totalTokens = int(gjson.GetBytes(payload, "usage.total_tokens").Int())
	if totalTokens == 0 && inputTokens == 0 {
		usage := helps.ParseOpenAIUsage(payload)
		inputTokens = int(usage.InputTokens)
		outputTokens = int(usage.OutputTokens)
		totalTokens = int(usage.TotalTokens)
	}
	return inputTokens, outputTokens, totalTokens
}

func (e *OpenAICompatExecutor) shouldSynthesizeResponsesCompaction(auth *cliproxyauth.Auth) bool {
	compat := e.responsesCompactionConfig(auth)
	return compat != nil && compat.SynthesizeResponsesCompaction
}

func (e *OpenAICompatExecutor) responsesCompactionConfig(auth *cliproxyauth.Auth) *config.OpenAICompatibility {
	if compat := e.resolveCompatConfig(auth); compat != nil {
		return compat
	}
	if e.cfg == nil {
		return nil
	}
	providerKey := strings.TrimSpace(e.Identifier())
	for i := range e.cfg.OpenAICompatibility {
		compat := &e.cfg.OpenAICompatibility[i]
		if compat.Disabled {
			continue
		}
		if strings.EqualFold(util.OpenAICompatibleProviderKey(compat.Name), providerKey) || strings.EqualFold(strings.TrimSpace(compat.Name), providerKey) {
			return compat
		}
	}
	return nil
}
