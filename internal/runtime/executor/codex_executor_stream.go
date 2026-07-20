package executor

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/constant"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func (e *CodexExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	if opts.Alt == "responses/compact" || opts.Alt == constant.ClaudeResponsesCompactBridgeAlt {
		return nil, statusErr{code: http.StatusBadRequest, msg: "streaming not supported for /responses/compact"}
	}
	if isCodexOpenAIImageRequest(opts) {
		return e.executeOpenAIImageStream(ctx, auth, req, opts)
	}
	baseModel := thinking.ParseSuffix(req.Model).ModelName

	apiKey, baseURL := codexCreds(auth)
	if baseURL == "" {
		baseURL = "https://chatgpt.com/backend-api/codex"
	}

	reporter := helps.NewExecutorUsageReporter(ctx, e, baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	from := opts.SourceFormat
	responseFormat := cliproxyexecutor.ResponseFormatOrSource(opts)
	to := sdktranslator.FromString("codex")
	originalPayloadSource := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayloadSource = opts.OriginalRequest
	}
	originalPayload := originalPayloadSource
	originalTranslated, body := translateCodexRequestPair(from, to, baseModel, originalPayload, req.Payload, true)
	originalTranslated = applyClaudeResponsesCompactionReplay(originalTranslated, originalPayload, opts)
	body = applyClaudeResponsesCompactionReplay(body, req.Payload, opts)

	body, err = helps.ApplyRequestThinking(body, req, opts, from.String(), to.String(), e.Identifier())
	if err != nil {
		return nil, err
	}

	requestedModel := helps.PayloadRequestedModel(opts, req.Model)
	requestPath := helps.PayloadRequestPath(opts)
	body = helps.ApplyPayloadConfigWithRequest(e.cfg, baseModel, to.String(), from.String(), "", body, originalTranslated, requestedModel, requestPath, opts.Headers)
	body, _ = sjson.DeleteBytes(body, "previous_response_id")
	body, _ = sjson.DeleteBytes(body, "generate")
	body, _ = sjson.DeleteBytes(body, "prompt_cache_retention")
	body, _ = sjson.DeleteBytes(body, "safety_identifier")
	body, _ = sjson.DeleteBytes(body, "stream_options")
	body = helps.SetStringIfDifferent(body, "model", baseModel)
	body = normalizeCodexInstructions(body)
	if e.cfg == nil || e.cfg.DisableImageGeneration == config.DisableImageGenerationOff {
		body = ensureImageGenerationTool(body, baseModel, auth, opts.Headers)
	}
	body = sanitizeOpenAIResponsesReasoningEncryptedContent(ctx, "codex executor", body)
	body = normalizeCodexParallelToolCalls(body, opts.Headers)
	body, optimizeMultiAgentV2 := helps.OptimizeCodexMultiAgentV2Request(ctx, opts.Headers, body, e.cfg)
	body, replayScope, errReplay := applyCodexReasoningReplayCacheRequired(ctx, from, req, opts, body)
	if errReplay != nil {
		return nil, errReplay
	}
	estimatedClaudeInputTokens, errContext := validateClaudeBridgeContextWindow(baseModel, body, opts)
	if errContext != nil {
		return nil, errContext
	}
	reporter.SetTranslatedReasoningEffort(body, to.String())

	url := strings.TrimSuffix(baseURL, "/") + "/responses"
	var identityState codexIdentityConfuseState
	httpReq, upstreamBody, identityState, err := e.cacheHelper(ctx, from, url, auth, req, originalPayloadSource, body, opts.Headers)
	if err != nil {
		return nil, err
	}
	applyCodexHeaders(httpReq, auth, apiKey, true, e.cfg)
	applyModelHeaderOverrides(httpReq.Header, baseModel)
	applyCodexIdentityConfuseHeaders(httpReq.Header, &identityState)
	var authID, authLabel, authType, authValue string
	if auth != nil {
		authID = auth.ID
		authLabel = auth.Label
		authType, authValue = auth.AccountInfo()
	}
	helps.RecordAPIRequest(ctx, e.cfg, helps.UpstreamRequestLog{
		URL:       url,
		Method:    http.MethodPost,
		Headers:   httpReq.Header.Clone(),
		Body:      upstreamBody,
		Provider:  e.Identifier(),
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})

	httpClient := helps.NewUtlsHTTPClient(ctx, e.cfg, auth, 0)
	httpClient = reporter.TrackHTTPClient(httpClient)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return nil, err
	}
	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		data, readErr := io.ReadAll(httpResp.Body)
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("codex executor: close response body error: %v", errClose)
		}
		if readErr != nil {
			helps.RecordAPIResponseError(ctx, e.cfg, readErr)
			return nil, readErr
		}
		data = applyCodexIdentityConfuseResponsePayload(data, identityState)
		if errClearReplay := clearCodexReasoningReplayOnInvalidSignature(ctx, replayScope, httpResp.StatusCode, data); errClearReplay != nil {
			return nil, errClearReplay
		}
		helps.AppendAPIResponseChunk(ctx, e.cfg, data)
		helps.LogWithRequestID(ctx).Debugf("request error, error status: %d, error message: %s", httpResp.StatusCode, helps.SummarizeErrorBody(httpResp.Header.Get("Content-Type"), data))
		err = newCodexStatusErr(httpResp.StatusCode, data)
		return nil, err
	}
	out := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		defer close(out)
		defer func() {
			if errClose := httpResp.Body.Close(); errClose != nil {
				log.Errorf("codex executor: close response body error: %v", errClose)
			}
		}()
		var param any
		var usageEstimator *helps.ClaudeStreamUsageEstimator
		if opts.Alt == constant.ClaudeResponsesBridgeAlt {
			var errEstimator error
			usageEstimator, errEstimator = helps.NewClaudeStreamUsageEstimator(baseModel, estimatedClaudeInputTokens)
			if errEstimator != nil {
				log.WithError(errEstimator).WithField("model", baseModel).Warn("Claude Responses bridge live usage estimation is unavailable")
			}
		}
		thinkingTokenEmitter := helps.NewClaudeThinkingTokenCountEmitter(claudeThinkingTokenCountRequested(opts.Headers))
		type scanResult struct {
			line []byte
			err  error
			done bool
		}
		scanner := bufio.NewScanner(httpResp.Body)
		scanner.Buffer(nil, 52_428_800) // 50MB
		claudeInputTokens := helps.NewClaudeInputTokenState(from, to, responseFormat, originalPayload)
		scanResults := make(chan scanResult, 1)
		scanStop := make(chan struct{})
		defer close(scanStop)
		go func() {
			for scanner.Scan() {
				result := scanResult{line: bytes.Clone(scanner.Bytes())}
				select {
				case scanResults <- result:
				case <-scanStop:
					return
				case <-ctx.Done():
					return
				}
			}
			select {
			case scanResults <- scanResult{err: scanner.Err(), done: true}:
			case <-scanStop:
			case <-ctx.Done():
			}
		}()
		var usageTicker *time.Ticker
		var usageTicks <-chan time.Time
		if usageEstimator != nil {
			usageTicker = time.NewTicker(claudeLiveUsageTickInterval)
			usageTicks = usageTicker.C
			defer usageTicker.Stop()
		}
		outputItemsByIndex := make(map[int64][]byte)
		var outputItemsFallback [][]byte
		for {
			select {
			case now := <-usageTicks:
				if snapshot, emit := usageEstimator.ObserveTime(now); emit {
					thinkingTokenUpdate := thinkingTokenEmitter.Event(snapshot)
					if len(thinkingTokenUpdate) > 0 {
						select {
						case out <- cliproxyexecutor.StreamChunk{Payload: thinkingTokenUpdate}:
						case <-ctx.Done():
							return
						}
					}
					usageUpdate := helps.ClaudeCumulativeUsageEvent(snapshot)
					if len(usageUpdate) > 0 {
						select {
						case out <- cliproxyexecutor.StreamChunk{Payload: usageUpdate}:
						case <-ctx.Done():
							return
						}
					}
				}
				continue
			case result := <-scanResults:
				if result.done {
					if result.err != nil {
						if ctx.Err() != nil {
							return
						}
						helps.RecordAPIResponseError(ctx, e.cfg, result.err)
					}
					streamErr := newCodexIncompleteStreamError()
					helps.RecordAPIResponseError(ctx, e.cfg, streamErr)
					reporter.PublishFailure(ctx, streamErr)
					select {
					case out <- cliproxyexecutor.StreamChunk{Err: streamErr}:
					case <-ctx.Done():
					}
					return
				}
				line := applyCodexIdentityConfuseResponsePayload(result.line, identityState)
				helps.AppendAPIResponseChunk(ctx, e.cfg, line)
				translatedLine := bytes.Clone(line)
				terminalSuccess := false
				var usageUpdate []byte
				var usageSnapshot helps.ClaudeUsageSnapshot
				usageSnapshotEmitted := false

				if bytes.HasPrefix(line, dataTag) {
					data := bytes.TrimSpace(line[5:])
					data = helps.RestoreCodexMultiAgentV2Response(data, optimizeMultiAgentV2)
					translatedLine = append([]byte("data: "), data...)
					if usageEstimator != nil {
						if snapshot, emit := usageEstimator.ObserveCodexEvent(data); emit {
							usageSnapshot = snapshot
							usageSnapshotEmitted = true
							usageUpdate = helps.ClaudeCumulativeUsageEvent(snapshot)
						}
					}
					eventType := gjson.GetBytes(data, "type").String()
					if streamErr, terminalBody, ok := codexTerminalFailureErr(data); ok {
						if errClearReplay := clearCodexReasoningReplayOnInvalidSignature(ctx, replayScope, streamErr.StatusCode(), terminalBody); errClearReplay != nil {
							helps.RecordAPIResponseError(ctx, e.cfg, errClearReplay)
							reporter.PublishFailure(ctx, errClearReplay)
							select {
							case out <- cliproxyexecutor.StreamChunk{Err: errClearReplay}:
							case <-ctx.Done():
							}
							return
						}
						helps.RecordAPIResponseError(ctx, e.cfg, streamErr)
						reporter.PublishFailure(ctx, streamErr)
						select {
						case out <- cliproxyexecutor.StreamChunk{Err: streamErr}:
						case <-ctx.Done():
						}
						return
					}
					switch eventType {
					case "response.output_item.done":
						collectCodexOutputItemDone(data, outputItemsByIndex, &outputItemsFallback)
					case "response.completed", "response.incomplete":
						terminalSuccess = true
						if detail, ok := helps.ParseCodexUsage(data); ok {
							reporter.Publish(ctx, detail)
						}
						publishCodexImageToolUsage(ctx, reporter, body, data)
						data = patchCodexCompletedOutput(data, outputItemsByIndex, outputItemsFallback)
						if eventType == "response.completed" {
							cacheCodexReasoningReplayFromCompleted(replayScope, data)
						}
						translatedLine = append([]byte("data: "), data...)
					}
				}

				translatedLine = applyCodexIdentityExposeResponsePayload(translatedLine, identityState)
				chunks := helps.TranslateStreamWithClaudeInputTokens(ctx, to, responseFormat, req.Model, originalPayload, body, translatedLine, &param, claudeInputTokens)
				if usageSnapshotEmitted && usageSnapshot.OutputTokens == 0 && helps.ClaudeApplyMessageStartUsage(chunks, usageSnapshot) {
					usageUpdate = nil
				}
				if usageSnapshotEmitted {
					thinkingTokenUpdate := thinkingTokenEmitter.Event(usageSnapshot)
					if len(thinkingTokenUpdate) > 0 {
						select {
						case out <- cliproxyexecutor.StreamChunk{Payload: thinkingTokenUpdate}:
						case <-ctx.Done():
							return
						}
					}
				}
				for i := range chunks {
					select {
					case out <- cliproxyexecutor.StreamChunk{Payload: chunks[i]}:
					case <-ctx.Done():
						return
					}
				}
				thinkingTokenEmitter.ObserveTranslatedChunks(chunks)
				if len(usageUpdate) > 0 {
					select {
					case out <- cliproxyexecutor.StreamChunk{Payload: usageUpdate}:
					case <-ctx.Done():
						return
					}
				}
				if terminalSuccess {
					return
				}
			}
		}
	}()
	return &cliproxyexecutor.StreamResult{Headers: httpResp.Header.Clone(), Chunks: out}, nil
}
