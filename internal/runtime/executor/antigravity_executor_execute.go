package executor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Execute performs a non-streaming request to the Antigravity API.
func (e *AntigravityExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	if opts.Alt == "responses/compact" {
		return resp, statusErr{code: http.StatusNotImplemented, msg: "/responses/compact not supported"}
	}
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	if inCooldown, remaining, errCooldown := antigravityIsInShortCooldownRequired(ctx, auth, baseModel, time.Now()); errCooldown != nil {
		return resp, homeKVUnavailableStatusErr(errCooldown)
	} else if inCooldown && !antigravityShouldBypassShortCooldown(ctx, e.cfg) {
		log.Debugf("antigravity executor: auth %s in short cooldown for model %s (%s remaining), returning 429 to switch auth", auth.ID, baseModel, remaining)
		d := remaining
		return resp, statusErr{code: http.StatusTooManyRequests, msg: fmt.Sprintf("auth in short cooldown, %s remaining", remaining), retryAfter: &d}
	}

	isClaude := strings.Contains(strings.ToLower(baseModel), "claude")
	if isClaude || strings.Contains(baseModel, "gemini-3-pro") || strings.Contains(baseModel, "gemini-3.1-flash-image") {
		return e.executeClaudeNonStream(ctx, auth, req, opts)
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
		return resp, errValidate
	}
	req.Payload = originalPayload
	token, updatedAuth, errToken := e.ensureAccessToken(ctx, auth)
	if errToken != nil {
		return resp, errToken
	}
	if updatedAuth != nil {
		auth = updatedAuth
		reporter.UpdateAccessTokenFingerprint(auth)
	}
	originalTranslated, translated := helps.TranslateRequestPairWithCodexMultiAgentV2(ctx, opts.Headers, e.cfg, from, to, baseModel, originalPayload, req.Payload, false)

	translated, err = helps.ApplyThinkingWithSourcePayload(translated, req.Payload, originalPayloadSource, req.Model, from.String(), to.String(), e.Identifier())
	if err != nil {
		return resp, err
	}

	requestedModel := helps.PayloadRequestedModel(opts, req.Model)
	requestPath := helps.PayloadRequestPath(opts)
	translated = helps.ApplyPayloadConfigWithRequest(e.cfg, baseModel, "antigravity", from.String(), "request", translated, originalTranslated, requestedModel, requestPath, opts.Headers)
	translated = e.obfuscateSensitiveWords(translated)
	translated = sanitizeAntigravityGeminiRequestSignatures(baseModel, translated)
	reporter.SetTranslatedReasoningEffort(translated, to.String())

	useCredits := cliproxyauth.AntigravityCreditsRequested(ctx) && antigravityCreditsRetryEnabled(e.cfg)

	baseURLs := antigravityBaseURLFallbackOrder(auth)
	httpClient := newAntigravityHTTPClient(ctx, e.cfg, auth, 0)
	httpClient = reporter.TrackHTTPClient(httpClient)
	attempts := antigravityRetryAttempts(auth, e.cfg)

attemptLoop:
	for attempt := 0; attempt < attempts; attempt++ {
		var lastStatus int
		var lastBody []byte
		var lastErr error

		for idx, baseURL := range baseURLs {
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
					return resp, err
				}
			}

			httpReq, errReq := e.buildRequest(ctx, auth, token, baseModel, requestPayload, false, opts.Alt, baseURL, helps.DerivedAntigravitySessionID(opts.Metadata, req.Metadata))
			if errReq != nil {
				err = errReq
				return resp, err
			}

			httpResp, errDo := httpClient.Do(httpReq)
			if errDo != nil {
				helps.RecordAPIResponseError(ctx, e.cfg, errDo)
				if errors.Is(errDo, context.Canceled) || errors.Is(errDo, context.DeadlineExceeded) {
					return resp, errDo
				}
				lastStatus = 0
				lastBody = nil
				lastErr = errDo
				if idx+1 < len(baseURLs) {
					log.Debugf("antigravity executor: request error on base url %s, retrying with fallback base url: %s", baseURL, baseURLs[idx+1])
					continue
				}
				err = errDo
				return resp, err
			}

			helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
			bodyBytes, errRead := io.ReadAll(httpResp.Body)
			if errClose := httpResp.Body.Close(); errClose != nil {
				log.Errorf("antigravity executor: close response body error: %v", errClose)
			}
			if errRead != nil {
				helps.RecordAPIResponseError(ctx, e.cfg, errRead)
				err = errRead
				return resp, err
			}
			helps.AppendAPIResponseChunk(ctx, e.cfg, bodyBytes)

			if httpResp.StatusCode == http.StatusTooManyRequests {
				decision := decideAntigravity429(bodyBytes)
				switch decision.kind {
				case antigravity429DecisionInstantRetrySameAuth:
					if attempt+1 < attempts {
						if decision.retryAfter != nil && *decision.retryAfter > 0 {
							wait := antigravityInstantRetryDelay(*decision.retryAfter)
							log.Debugf("antigravity executor: instant retry for model %s, waiting %s", baseModel, wait)
							if errWait := antigravityWait(ctx, wait); errWait != nil {
								return resp, errWait
							}
						}
						continue attemptLoop
					}
				case antigravity429DecisionShortCooldownSwitchAuth:
					if decision.retryAfter != nil && *decision.retryAfter > 0 {
						if errMarkCooldown := markAntigravityShortCooldownRequired(ctx, auth, baseModel, time.Now(), *decision.retryAfter); errMarkCooldown != nil {
							err = homeKVUnavailableStatusErr(errMarkCooldown)
							return resp, err
						}
						log.Debugf("antigravity executor: short quota cooldown (%s) for model %s, recorded cooldown", *decision.retryAfter, baseModel)
					}
				case antigravity429DecisionFullQuotaExhausted:
					if useCredits && antigravityHasExplicitCreditsBalanceExhaustedReason(bodyBytes) {
						markAntigravityCreditsPermanentlyDisabled(auth)
					}
					// No credits logic - just fall through to error return below
				}
			}

			if httpResp.StatusCode < http.StatusOK || httpResp.StatusCode >= http.StatusMultipleChoices {
				log.Debugf("antigravity executor: upstream error status: %d, body: %s", httpResp.StatusCode, helps.SummarizeErrorBody(httpResp.Header.Get("Content-Type"), bodyBytes))
				lastStatus = httpResp.StatusCode
				lastBody = append([]byte(nil), bodyBytes...)
				lastErr = nil
				if httpResp.StatusCode == http.StatusTooManyRequests && idx+1 < len(baseURLs) {
					log.Debugf("antigravity executor: rate limited on base url %s, retrying with fallback base url: %s", baseURL, baseURLs[idx+1])
					continue
				}
				if antigravityShouldRetryTransientResourceExhausted429(httpResp.StatusCode, bodyBytes) && attempt+1 < attempts {
					delay := antigravityTransient429RetryDelay(attempt)
					log.Debugf("antigravity executor: transient 429 resource exhausted for model %s, retrying in %s (attempt %d/%d)", baseModel, delay, attempt+1, attempts)
					if errWait := antigravityWait(ctx, delay); errWait != nil {
						return resp, errWait
					}
					continue attemptLoop
				}
				if antigravityShouldRetryNoCapacity(httpResp.StatusCode, bodyBytes) {
					if idx+1 < len(baseURLs) {
						log.Debugf("antigravity executor: no capacity on base url %s, retrying with fallback base url: %s", baseURL, baseURLs[idx+1])
						continue
					}
					if attempt+1 < attempts {
						delay := antigravityNoCapacityRetryDelay(attempt)
						log.Debugf("antigravity executor: no capacity for model %s, retrying in %s (attempt %d/%d)", baseModel, delay, attempt+1, attempts)
						if errWait := antigravityWait(ctx, delay); errWait != nil {
							return resp, errWait
						}
						continue attemptLoop
					}
				}
				if antigravityShouldRetrySoftRateLimit(httpResp.StatusCode, bodyBytes) {
					if attempt+1 < attempts {
						delay := antigravitySoftRateLimitDelay(attempt)
						log.Debugf("antigravity executor: soft rate limit for model %s, retrying in %s (attempt %d/%d)", baseModel, delay, attempt+1, attempts)
						if errWait := antigravityWait(ctx, delay); errWait != nil {
							return resp, errWait
						}
						continue attemptLoop
					}
				}
				if errClear := clearAntigravityReasoningReplayOnInvalidSignature(ctx, replayScope, httpResp.StatusCode, bodyBytes); errClear != nil {
					// Report the upstream failure rather than the cleanup failure.
					logAntigravityReasoningReplayDegraded(replayScope, "invalidate", errClear)
				}
				err = newAntigravityStatusErr(httpResp.StatusCode, bodyBytes)
				return resp, err
			}

			// Success
			if useCredits {
				clearAntigravityCreditsFailureState(auth)
			}
			cacheAntigravityReasoningReplayFromResponse(ctx, replayScope, requestPayload, bodyBytes)
			bodyBytes = e.resolveWebSearchGroundingURLs(ctx, auth, from, originalPayload, translated, bodyBytes)
			reporter.Publish(ctx, helps.ParseAntigravityUsage(bodyBytes))
			var param any
			converted := sdktranslator.TranslateNonStream(ctx, to, responseFormat, req.Model, opts.OriginalRequest, translated, bodyBytes, &param)
			if responseFormat == sdktranslator.FormatOpenAIResponse {
				converted = helps.EnsureResponsesUsageDetails(converted)
			}
			resp = cliproxyexecutor.Response{Payload: converted, Headers: httpResp.Header.Clone()}
			reporter.EnsurePublished(ctx)
			return resp, nil
		}

		switch {
		case lastStatus != 0:
			err = newAntigravityStatusErr(lastStatus, lastBody)
		case lastErr != nil:
			err = lastErr
		default:
			err = statusErr{code: http.StatusServiceUnavailable, msg: "antigravity executor: no base url available"}
		}
		return resp, err
	}

	return resp, err
}

// executeClaudeNonStream performs a claude non-streaming request to the Antigravity API.
func (e *AntigravityExecutor) executeClaudeNonStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	if inCooldown, remaining, errCooldown := antigravityIsInShortCooldownRequired(ctx, auth, baseModel, time.Now()); errCooldown != nil {
		return resp, homeKVUnavailableStatusErr(errCooldown)
	} else if inCooldown && !antigravityShouldBypassShortCooldown(ctx, e.cfg) {
		log.Debugf("antigravity executor: auth %s in short cooldown for model %s (%s remaining), returning 429 to switch auth", auth.ID, baseModel, remaining)
		d := remaining
		return resp, statusErr{code: http.StatusTooManyRequests, msg: fmt.Sprintf("auth in short cooldown, %s remaining", remaining), retryAfter: &d}
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
		return resp, errValidate
	}
	req.Payload = originalPayload
	token, updatedAuth, errToken := e.ensureAccessToken(ctx, auth)
	if errToken != nil {
		return resp, errToken
	}
	if updatedAuth != nil {
		auth = updatedAuth
		reporter.UpdateAccessTokenFingerprint(auth)
	}
	originalTranslated, translated := helps.TranslateRequestPairWithCodexMultiAgentV2(ctx, opts.Headers, e.cfg, from, to, baseModel, originalPayload, req.Payload, true)

	translated, err = helps.ApplyThinkingWithSourcePayload(translated, req.Payload, originalPayloadSource, req.Model, from.String(), to.String(), e.Identifier())
	if err != nil {
		return resp, err
	}

	requestedModel := helps.PayloadRequestedModel(opts, req.Model)
	requestPath := helps.PayloadRequestPath(opts)
	translated = helps.ApplyPayloadConfigWithRequest(e.cfg, baseModel, "antigravity", from.String(), "request", translated, originalTranslated, requestedModel, requestPath, opts.Headers)
	translated = e.obfuscateSensitiveWords(translated)
	translated = sanitizeAntigravityGeminiRequestSignatures(baseModel, translated)
	reporter.SetTranslatedReasoningEffort(translated, to.String())

	useCredits := cliproxyauth.AntigravityCreditsRequested(ctx) && antigravityCreditsRetryEnabled(e.cfg)

	baseURLs := antigravityBaseURLFallbackOrder(auth)
	httpClient := newAntigravityHTTPClient(ctx, e.cfg, auth, 0)
	httpClient = reporter.TrackHTTPClient(httpClient)

	attempts := antigravityRetryAttempts(auth, e.cfg)

attemptLoop:
	for attempt := 0; attempt < attempts; attempt++ {
		var lastStatus int
		var lastBody []byte
		var lastErr error

		for idx, baseURL := range baseURLs {
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
					return resp, err
				}
			}
			httpReq, errReq := e.buildRequest(ctx, auth, token, baseModel, requestPayload, true, opts.Alt, baseURL, helps.DerivedAntigravitySessionID(opts.Metadata, req.Metadata))
			if errReq != nil {
				err = errReq
				return resp, err
			}

			httpResp, errDo := httpClient.Do(httpReq)
			if errDo != nil {
				helps.RecordAPIResponseError(ctx, e.cfg, errDo)
				if errors.Is(errDo, context.Canceled) || errors.Is(errDo, context.DeadlineExceeded) {
					return resp, errDo
				}
				lastStatus = 0
				lastBody = nil
				lastErr = errDo
				if idx+1 < len(baseURLs) {
					log.Debugf("antigravity executor: request error on base url %s, retrying with fallback base url: %s", baseURL, baseURLs[idx+1])
					continue
				}
				err = errDo
				return resp, err
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
						return resp, err
					}
					if errCtx := ctx.Err(); errCtx != nil {
						err = errCtx
						return resp, err
					}
					lastStatus = 0
					lastBody = nil
					lastErr = errRead
					if idx+1 < len(baseURLs) {
						log.Debugf("antigravity executor: read error on base url %s, retrying with fallback base url: %s", baseURL, baseURLs[idx+1])
						continue
					}
					err = errRead
					return resp, err
				}
				helps.AppendAPIResponseChunk(ctx, e.cfg, bodyBytes)
				if httpResp.StatusCode == http.StatusTooManyRequests {
					decision := decideAntigravity429(bodyBytes)

					switch decision.kind {
					case antigravity429DecisionInstantRetrySameAuth:
						if attempt+1 < attempts {
							if decision.retryAfter != nil && *decision.retryAfter > 0 {
								wait := antigravityInstantRetryDelay(*decision.retryAfter)
								log.Debugf("antigravity executor: instant retry for model %s, waiting %s", baseModel, wait)
								if errWait := antigravityWait(ctx, wait); errWait != nil {
									return resp, errWait
								}
							}
							continue attemptLoop
						}
					case antigravity429DecisionShortCooldownSwitchAuth:
						if decision.retryAfter != nil && *decision.retryAfter > 0 {
							if errMarkCooldown := markAntigravityShortCooldownRequired(ctx, auth, baseModel, time.Now(), *decision.retryAfter); errMarkCooldown != nil {
								err = homeKVUnavailableStatusErr(errMarkCooldown)
								return resp, err
							}
							log.Debugf("antigravity executor: short quota cooldown (%s) for model %s, recorded cooldown", *decision.retryAfter, baseModel)
						}
					case antigravity429DecisionFullQuotaExhausted:
						if useCredits && antigravityHasExplicitCreditsBalanceExhaustedReason(bodyBytes) {
							markAntigravityCreditsPermanentlyDisabled(auth)
						}
						// No credits logic - just fall through to error return below
					}
				}

				lastStatus = httpResp.StatusCode
				lastBody = append([]byte(nil), bodyBytes...)
				lastErr = nil
				if httpResp.StatusCode == http.StatusTooManyRequests && idx+1 < len(baseURLs) {
					log.Debugf("antigravity executor: rate limited on base url %s, retrying with fallback base url: %s", baseURL, baseURLs[idx+1])
					continue
				}
				if antigravityShouldRetryTransientResourceExhausted429(httpResp.StatusCode, bodyBytes) && attempt+1 < attempts {
					delay := antigravityTransient429RetryDelay(attempt)
					log.Debugf("antigravity executor: transient 429 resource exhausted for model %s, retrying in %s (attempt %d/%d)", baseModel, delay, attempt+1, attempts)
					if errWait := antigravityWait(ctx, delay); errWait != nil {
						return resp, errWait
					}
					continue attemptLoop
				}
				if antigravityShouldRetryNoCapacity(httpResp.StatusCode, bodyBytes) {
					if idx+1 < len(baseURLs) {
						log.Debugf("antigravity executor: no capacity on base url %s, retrying with fallback base url: %s", baseURL, baseURLs[idx+1])
						continue
					}
					if attempt+1 < attempts {
						delay := antigravityNoCapacityRetryDelay(attempt)
						log.Debugf("antigravity executor: no capacity for model %s, retrying in %s (attempt %d/%d)", baseModel, delay, attempt+1, attempts)
						if errWait := antigravityWait(ctx, delay); errWait != nil {
							return resp, errWait
						}
						continue attemptLoop
					}
				}
				if antigravityShouldRetrySoftRateLimit(httpResp.StatusCode, bodyBytes) {
					if attempt+1 < attempts {
						delay := antigravitySoftRateLimitDelay(attempt)
						log.Debugf("antigravity executor: soft rate limit for model %s, retrying in %s (attempt %d/%d)", baseModel, delay, attempt+1, attempts)
						if errWait := antigravityWait(ctx, delay); errWait != nil {
							return resp, errWait
						}
						continue attemptLoop
					}
				}
				if errClear := clearAntigravityReasoningReplayOnInvalidSignature(ctx, replayScope, httpResp.StatusCode, bodyBytes); errClear != nil {
					// Report the upstream failure rather than the cleanup failure.
					logAntigravityReasoningReplayDegraded(replayScope, "invalidate", errClear)
				}
				err = newAntigravityStatusErr(httpResp.StatusCode, bodyBytes)
				return resp, err
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
						log.Errorf("antigravity executor: close response body error: %v", errClose)
					}
				}()
				scanner := bufio.NewScanner(resp.Body)
				scanner.Buffer(nil, streamScannerBuffer)
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

					out <- cliproxyexecutor.StreamChunk{Payload: payload}
				}
				if errScan := scanner.Err(); errScan != nil {
					helps.RecordAPIResponseError(ctx, e.cfg, errScan)
					reporter.PublishFailure(ctx, errScan)
					out <- cliproxyexecutor.StreamChunk{Err: errScan}
				} else {
					if replayAccumulator != nil {
						replayAccumulator.Commit(ctx)
					}
					reporter.EnsurePublished(ctx)
				}
			}(httpResp)

			var buffer bytes.Buffer
			for chunk := range out {
				if chunk.Err != nil {
					return resp, chunk.Err
				}
				if len(chunk.Payload) > 0 {
					_, _ = buffer.Write(chunk.Payload)
					_, _ = buffer.Write([]byte("\n"))
				}
			}
			resp = cliproxyexecutor.Response{Payload: e.convertStreamToNonStream(buffer.Bytes())}

			resp.Payload = e.resolveWebSearchGroundingURLs(ctx, auth, from, originalPayload, translated, resp.Payload)
			reporter.Publish(ctx, helps.ParseAntigravityUsage(resp.Payload))
			var param any
			converted := sdktranslator.TranslateNonStream(ctx, to, responseFormat, req.Model, opts.OriginalRequest, translated, resp.Payload, &param)
			if responseFormat == sdktranslator.FormatOpenAIResponse {
				converted = helps.EnsureResponsesUsageDetails(converted)
			}
			resp = cliproxyexecutor.Response{Payload: converted, Headers: httpResp.Header.Clone()}
			reporter.EnsurePublished(ctx)

			return resp, nil
		}

		switch {
		case lastStatus != 0:
			err = newAntigravityStatusErr(lastStatus, lastBody)
		case lastErr != nil:
			err = lastErr
		default:
			err = statusErr{code: http.StatusServiceUnavailable, msg: "antigravity executor: no base url available"}
		}
		return resp, err
	}

	return resp, err
}

func (e *AntigravityExecutor) convertStreamToNonStream(stream []byte) []byte {
	responseTemplate := ""
	var traceID string
	var finishReason string
	var modelVersion string
	var responseID string
	var role string
	var usageRaw string
	parts := make([]map[string]interface{}, 0)
	var pendingKind string
	var pendingText strings.Builder
	var pendingThoughtSig string

	flushPending := func() {
		if pendingKind == "" {
			return
		}
		text := pendingText.String()
		switch pendingKind {
		case "text":
			if strings.TrimSpace(text) == "" {
				pendingKind = ""
				pendingText.Reset()
				pendingThoughtSig = ""
				return
			}
			parts = append(parts, map[string]interface{}{"text": text})
		case "thought":
			if strings.TrimSpace(text) == "" && pendingThoughtSig == "" {
				pendingKind = ""
				pendingText.Reset()
				pendingThoughtSig = ""
				return
			}
			part := map[string]interface{}{"thought": true}
			part["text"] = text
			if pendingThoughtSig != "" {
				part["thoughtSignature"] = pendingThoughtSig
			}
			parts = append(parts, part)
		}
		pendingKind = ""
		pendingText.Reset()
		pendingThoughtSig = ""
	}

	normalizePart := func(partResult gjson.Result) map[string]interface{} {
		var m map[string]interface{}
		_ = json.Unmarshal([]byte(partResult.Raw), &m)
		if m == nil {
			m = map[string]interface{}{}
		}
		sig := partResult.Get("thoughtSignature").String()
		if sig == "" {
			sig = partResult.Get("thought_signature").String()
		}
		if sig != "" {
			m["thoughtSignature"] = sig
			delete(m, "thought_signature")
		}
		if inlineData, ok := m["inline_data"]; ok {
			m["inlineData"] = inlineData
			delete(m, "inline_data")
		}
		return m
	}

	for _, line := range bytes.Split(stream, []byte("\n")) {
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) == 0 || !gjson.ValidBytes(trimmed) {
			continue
		}

		root := gjson.ParseBytes(trimmed)
		responseNode := root.Get("response")
		if !responseNode.Exists() {
			if root.Get("candidates").Exists() {
				responseNode = root
			} else {
				continue
			}
		}
		responseTemplate = responseNode.Raw

		if traceResult := root.Get("traceId"); traceResult.Exists() && traceResult.String() != "" {
			traceID = traceResult.String()
		}

		if roleResult := responseNode.Get("candidates.0.content.role"); roleResult.Exists() {
			role = roleResult.String()
		}

		if finishResult := responseNode.Get("candidates.0.finishReason"); finishResult.Exists() && finishResult.String() != "" {
			finishReason = finishResult.String()
		}

		if modelResult := responseNode.Get("modelVersion"); modelResult.Exists() && modelResult.String() != "" {
			modelVersion = modelResult.String()
		}
		if responseIDResult := responseNode.Get("responseId"); responseIDResult.Exists() && responseIDResult.String() != "" {
			responseID = responseIDResult.String()
		}
		if usageResult := responseNode.Get("usageMetadata"); usageResult.Exists() {
			usageRaw = usageResult.Raw
		} else if usageMetadataResult := root.Get("usageMetadata"); usageMetadataResult.Exists() {
			usageRaw = usageMetadataResult.Raw
		}

		if partsResult := responseNode.Get("candidates.0.content.parts"); partsResult.IsArray() {
			for _, part := range partsResult.Array() {
				hasFunctionCall := part.Get("functionCall").Exists()
				hasInlineData := part.Get("inlineData").Exists() || part.Get("inline_data").Exists()
				sig := part.Get("thoughtSignature").String()
				if sig == "" {
					sig = part.Get("thought_signature").String()
				}
				text := part.Get("text").String()
				thought := part.Get("thought").Bool()

				if hasFunctionCall || hasInlineData {
					flushPending()
					parts = append(parts, normalizePart(part))
					continue
				}

				if thought || part.Get("text").Exists() {
					kind := "text"
					if thought {
						kind = "thought"
					}
					if pendingKind != "" && pendingKind != kind {
						flushPending()
					}
					pendingKind = kind
					pendingText.WriteString(text)
					if kind == "thought" && sig != "" {
						pendingThoughtSig = sig
					}
					continue
				}

				flushPending()
				parts = append(parts, normalizePart(part))
			}
		}
	}
	flushPending()

	if responseTemplate == "" {
		responseTemplate = `{"candidates":[{"content":{"role":"model","parts":[]}}]}`
	}

	partsJSON, _ := json.Marshal(parts)
	updatedTemplate, _ := sjson.SetRawBytes([]byte(responseTemplate), "candidates.0.content.parts", partsJSON)
	responseTemplate = string(updatedTemplate)
	if role != "" {
		updatedTemplate, _ = sjson.SetBytes([]byte(responseTemplate), "candidates.0.content.role", role)
		responseTemplate = string(updatedTemplate)
	}
	if finishReason != "" {
		updatedTemplate, _ = sjson.SetBytes([]byte(responseTemplate), "candidates.0.finishReason", finishReason)
		responseTemplate = string(updatedTemplate)
	}
	if modelVersion != "" {
		updatedTemplate, _ = sjson.SetBytes([]byte(responseTemplate), "modelVersion", modelVersion)
		responseTemplate = string(updatedTemplate)
	}
	if responseID != "" {
		updatedTemplate, _ = sjson.SetBytes([]byte(responseTemplate), "responseId", responseID)
		responseTemplate = string(updatedTemplate)
	}
	if usageRaw != "" {
		updatedTemplate, _ = sjson.SetRawBytes([]byte(responseTemplate), "usageMetadata", []byte(usageRaw))
		responseTemplate = string(updatedTemplate)
	} else if !gjson.Get(responseTemplate, "usageMetadata").Exists() {
		updatedTemplate, _ = sjson.SetBytes([]byte(responseTemplate), "usageMetadata.promptTokenCount", 0)
		responseTemplate = string(updatedTemplate)
		updatedTemplate, _ = sjson.SetBytes([]byte(responseTemplate), "usageMetadata.candidatesTokenCount", 0)
		responseTemplate = string(updatedTemplate)
		updatedTemplate, _ = sjson.SetBytes([]byte(responseTemplate), "usageMetadata.totalTokenCount", 0)
		responseTemplate = string(updatedTemplate)
	}

	output := `{"response":{},"traceId":""}`
	updatedOutput, _ := sjson.SetRawBytes([]byte(output), "response", []byte(responseTemplate))
	output = string(updatedOutput)
	if traceID != "" {
		updatedOutput, _ = sjson.SetBytes([]byte(output), "traceId", traceID)
		output = string(updatedOutput)
	}
	return []byte(output)
}
