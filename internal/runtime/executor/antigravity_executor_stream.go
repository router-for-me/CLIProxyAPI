package executor

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/sjson"
)

// ExecuteStream performs a streaming request to the Antigravity API.
func (e *AntigravityExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	if opts.Alt == "responses/compact" {
		return nil, statusErr{code: http.StatusNotImplemented, msg: "/responses/compact not supported"}
	}
	baseModel := thinking.ParseSuffix(req.Model).ModelName

	ctx = context.WithValue(ctx, "alt", "")
	if inCooldown, remaining, errCooldown := antigravityIsInShortCooldownRequired(ctx, auth, baseModel, time.Now()); errCooldown != nil {
		return nil, homeKVUnavailableStatusErr(errCooldown)
	} else if inCooldown && !antigravityShouldBypassShortCooldown(ctx, e.cfg) {
		log.Debugf("antigravity executor: auth %s in short cooldown for model %s (%s remaining), returning 429 to switch auth", auth.ID, baseModel, remaining)
		d := remaining
		return nil, statusErr{code: http.StatusTooManyRequests, msg: fmt.Sprintf("auth in short cooldown, %s remaining", remaining), retryAfter: &d}
	}

	reporter := helps.NewExecutorUsageReporter(ctx, e, baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	from := opts.SourceFormat
	responseFormat := cliproxyexecutor.ResponseFormatOrSource(opts)
	to := sdktranslator.FromString("antigravity")

	originalPayloadSource := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayloadSource = opts.OriginalRequest
	}
	originalPayload := originalPayloadSource
	originalPayload, errValidate := validateAntigravityRequestSignatures(ctx, baseModel, from, originalPayload)
	if errValidate != nil {
		return nil, errValidate
	}
	req.Payload = originalPayload
	token, updatedAuth, errToken := e.ensureAccessToken(ctx, auth)
	if errToken != nil {
		return nil, errToken
	}
	if updatedAuth != nil {
		auth = updatedAuth
		reporter.UpdateAccessTokenFingerprint(auth)
	}

	originalTranslated, translated := helps.TranslateRequestPairWithCodexMultiAgentV2(ctx, opts.Headers, e.cfg, from, to, baseModel, originalPayload, req.Payload, true)

	translated, err = helps.ApplyThinkingWithSourcePayload(translated, req.Payload, originalPayloadSource, req.Model, from.String(), to.String(), e.Identifier())
	if err != nil {
		return nil, err
	}

	requestedModel := helps.PayloadRequestedModel(opts, req.Model)
	requestPath := helps.PayloadRequestPath(opts)
	translated = helps.ApplyPayloadConfigWithRequest(e.cfg, baseModel, "antigravity", from.String(), "request", translated, originalTranslated, requestedModel, requestPath, opts.Headers)
	translated = e.obfuscateSensitiveWords(translated)
	translated = sanitizeAntigravityGeminiRequestSignatures(baseModel, translated)
	translated, _ = sjson.DeleteBytes(translated, "request.stream")
	reporter.SetTranslatedReasoningEffort(translated, to.String())

	useCredits := cliproxyauth.AntigravityCreditsRequested(ctx) && antigravityCreditsRetryEnabled(e.cfg)

	baseURL := resolveAntigravityRequestBaseURL(auth)
	httpClient := newAntigravityHTTPClient(ctx, e.cfg, auth, 0)
	httpClient = reporter.TrackHTTPClient(httpClient)

	// Credential retry rounds are owned by the conductor. Perform one upstream
	// request per credential so request-retry is not consumed twice.
	requestPayload := translated
	if useCredits {
		if cp := injectEnabledCreditTypes(translated); len(cp) > 0 {
			requestPayload = cp
			helps.MarkCreditsUsed(ctx)
		}
	}
	replayScope := antigravityReasoningReplayScope{}
	if antigravityUsesReasoningReplayCache(baseModel) {
		var errReplay error
		requestPayload, replayScope, errReplay = prepareAntigravityGeminiReasoningReplayPayload(ctx, baseModel, req, opts, requestPayload)
		if errReplay != nil {
			err = errReplay
			return nil, err
		}
	}
	requestPayload = ensureAntigravityGeminiLeadingUserContent(baseModel, requestPayload)
	httpReq, errReq := e.buildRequest(ctx, auth, token, baseModel, requestPayload, true, opts.Alt, baseURL, helps.DerivedAntigravitySessionID(opts.Metadata, req.Metadata))
	if errReq != nil {
		err = errReq
		return nil, err
	}
	httpResp, errDo := httpClient.Do(httpReq)
	if errDo != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, errDo)
		if errors.Is(errDo, context.Canceled) || errors.Is(errDo, context.DeadlineExceeded) {
			return nil, errDo
		}
		err = errDo
		return nil, err
	}
	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode < http.StatusOK || httpResp.StatusCode >= http.StatusMultipleChoices {
		bodyBytes, errRead := io.ReadAll(httpResp.Body)
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("antigravity executor: close response body error: %v", errClose)
		}
		if errRead != nil {
			helps.RecordAPIResponseError(ctx, e.cfg, errRead)
			if errors.Is(errRead, context.Canceled) || errors.Is(errRead, context.DeadlineExceeded) {
				err = errRead
				return nil, err
			}
			if errCtx := ctx.Err(); errCtx != nil {
				err = errCtx
				return nil, err
			}
			err = errRead
			return nil, err
		}
		helps.AppendAPIResponseChunk(ctx, e.cfg, bodyBytes)
		if httpResp.StatusCode == http.StatusTooManyRequests {
			decision := decideAntigravity429(bodyBytes)

			switch decision.kind {
			case antigravity429DecisionShortCooldownSwitchAuth:
				if decision.retryAfter != nil && *decision.retryAfter > 0 {
					if errMarkCooldown := markAntigravityShortCooldownRequired(ctx, auth, baseModel, time.Now(), *decision.retryAfter); errMarkCooldown != nil {
						err = homeKVUnavailableStatusErr(errMarkCooldown)
						return nil, err
					}
					log.Debugf("antigravity executor: short quota cooldown (%s) for model %s recorded", *decision.retryAfter, baseModel)
				}
			case antigravity429DecisionFullQuotaExhausted:
				if useCredits && antigravityHasExplicitCreditsBalanceExhaustedReason(bodyBytes) {
					markAntigravityCreditsPermanentlyDisabled(auth)
				}
				// No credits logic - just fall through to error return below
			}
		}

		if errClear := clearAntigravityReasoningReplayOnInvalidSignature(ctx, replayScope, httpResp.StatusCode, bodyBytes); errClear != nil {
			// Report the upstream failure rather than the cleanup failure.
			logAntigravityReasoningReplayDegraded(replayScope, "invalidate", errClear)
		}
		err = newAntigravityStatusErr(httpResp.StatusCode, bodyBytes)
		return nil, err
	}

	// Stream success
	if useCredits {
		clearAntigravityCreditsFailureState(auth)
	}
	replayAccumulator := newAntigravityReasoningReplayAccumulator(replayScope, requestPayload)
	out := make(chan cliproxyexecutor.StreamChunk)
	go func(resp *http.Response) {
		defer close(out)
		defer func() {
			if errClose := resp.Body.Close(); errClose != nil {
				log.Errorf("antigravity executor: close response line error: %v", errClose)
			}
		}()
		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(nil, streamScannerBuffer)
		claudeInputTokens := helps.NewClaudeInputTokenState(from, to, responseFormat, originalPayload)
		var param any
		for scanner.Scan() {
			line := scanner.Bytes()
			helps.AppendAPIResponseChunk(ctx, e.cfg, line)
			if replayAccumulator != nil {
				replayAccumulator.ObserveSSELine(line)
			}

			// Filter usage metadata for all models
			// Only retain usage statistics in the terminal chunk
			line = helps.FilterSSEUsageMetadata(line)

			payload := helps.JSONPayload(line)
			if payload == nil {
				continue
			}

			if detail, ok := helps.ParseAntigravityStreamUsage(payload); ok {
				reporter.Publish(ctx, detail)
			}

			payload = e.resolveWebSearchGroundingURLs(ctx, auth, from, originalPayload, translated, payload)
			chunks := helps.TranslateStreamWithClaudeInputTokens(ctx, to, responseFormat, req.Model, opts.OriginalRequest, translated, bytes.Clone(payload), &param, claudeInputTokens)
			for i := range chunks {
				select {
				case out <- cliproxyexecutor.StreamChunk{Payload: chunks[i]}:
				case <-ctx.Done():
					return
				}
			}
		}
		if errScan := scanner.Err(); errScan != nil {
			helps.RecordAPIResponseError(ctx, e.cfg, errScan)
			reporter.PublishFailure(ctx, errScan)
			select {
			case out <- cliproxyexecutor.StreamChunk{Err: errScan}:
			case <-ctx.Done():
			}
		} else {
			// Only a clean end of stream may produce a synthetic terminal event.
			// Translating [DONE] after a read error would report a truncated
			// stream as a successful completion.
			tail := helps.TranslateStreamWithClaudeInputTokens(ctx, to, responseFormat, req.Model, opts.OriginalRequest, translated, []byte("[DONE]"), &param, claudeInputTokens)
			for i := range tail {
				select {
				case out <- cliproxyexecutor.StreamChunk{Payload: tail[i]}:
				case <-ctx.Done():
					return
				}
			}
			if replayAccumulator != nil {
				replayAccumulator.Commit(ctx)
			}
			reporter.EnsurePublished(ctx)
		}
	}(httpResp)
	return &cliproxyexecutor.StreamResult{Headers: httpResp.Header.Clone(), Chunks: out}, nil
}
