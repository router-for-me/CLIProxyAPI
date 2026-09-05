package executor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func (e *ClaudeExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	if opts.Alt == "responses/compact" {
		return resp, statusErr{code: http.StatusNotImplemented, msg: "/responses/compact not supported"}
	}
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	upstreamModel := e.upstreamModel(baseModel)

	apiKey, baseURL := claudeCreds(auth)
	if baseURL == "" {
		baseURL = "https://api.anthropic.com"
	}
	url := fmt.Sprintf("%s/v1/messages?beta=true", baseURL)
	fp := resolveClaudeFingerprintPolicy(e.cfg, auth, apiKey)
	// Real Claude OAuth always signs CCH. An opted-in API key signs only where
	// native does, so a third-party gateway keeps a cache-stable billing header.
	// Default API-key and delegated-provider requests preserve the caller body.
	cchSigning := claudeCCHSigningEnabled(apiKey, claudeCCHUpstreamAnthropic, fp.ProfileClaudeCodeCLI, url)

	reporter := helps.NewExecutorUsageReporter(ctx, e, baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)
	from := opts.SourceFormat
	responseFormat := cliproxyexecutor.ResponseFormatOrSource(opts)
	to := sdktranslator.FromString("claude")
	var replayScope claudeThinkingReplayScope
	if claudeThinkingReplayEnabled(auth, req, opts) {
		req, replayScope = prepareClaudeThinkingReplayRequest(ctx, auth, req, opts)
	}
	defer func() {
		if err != nil && replayScope.replayApplied && shouldClearKimiThinkingReplayAfterError(err) {
			clearClaudeThinkingReplayContent(ctx, replayScope)
		}
	}()
	// Use an upstream stream whenever the downstream response needs translation
	// from Claude events. Native Claude responses use the JSON response path.
	upstreamStream := responseFormat != to
	originalPayloadSource := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayloadSource = opts.OriginalRequest
	}
	originalPayload := originalPayloadSource
	incomingHeaders, claudeCodeDetection := detectIncomingClaudeCodeRequest(ctx, opts.Headers, originalPayload, false, e.cfg)
	confirmedClaudeCode := claudeCodeDetection.Confirmed
	claudeSessionID := ""
	if fp.ProfileClaudeCodeCLI {
		claudeSessionID = helps.ClaudeAgentSessionUUIDForRequest(incomingHeaders, originalPayload, req.Payload, confirmedClaudeCode, opts.Metadata, req.Metadata)
	}

	continuityCtx := &helps.ClaudeContinuityContext{}
	ctx = helps.WithClaudeContinuityContext(ctx, continuityCtx)
	ctx = helps.WithIncomingHeaders(ctx, incomingHeaders)
	if claudeSessionID != "" {
		ctx = helps.WithClaudeSessionID(ctx, claudeSessionID)
	}

	originalTranslated := helps.TranslateRequestWithAPIKeyModelCompatibility(ctx, opts.Headers, e.cfg, from, to, baseModel, originalPayload, upstreamStream, helps.APIKeyModelIsCompat(req))
	body := helps.TranslateRequestWithAPIKeyModelCompatibility(ctx, opts.Headers, e.cfg, from, to, baseModel, req.Payload, upstreamStream, helps.APIKeyModelIsCompat(req))
	body = helps.SetStringIfDifferent(body, "model", upstreamModel)

	body, err = helps.ApplyRequestThinking(body, req, opts, from.String(), to.String(), e.Identifier())
	if err != nil {
		return resp, err
	}
	if rebuildMidSystemMessageEnabled(e.cfg, auth) {
		body = rebuildMidSystemMessagesToTopLevel(body)
	}

	// Apply cloaking (system prompt injection, fake user ID, sensitive word obfuscation)
	// based on client type and configuration.
	_, wireSettings := resolveClaudeWirePolicy(e.cfg, auth, apiKey, confirmedClaudeCode)
	bodyBeforeCloaking := body
	isProbeOrHelper := helps.IsClaudeProbeOrHelperRequest(bodyBeforeCloaking)
	var cloaked bool
	body, cloaked, err = applyCloakingInternal(
		ctx,
		e.cfg,
		auth,
		body,
		apiKey,
		confirmedClaudeCode,
		cchSigning,
		false,
	)
	if err != nil {
		return resp, err
	}
	systemPlacementState := captureClaudeCodeSystemPlacement(bodyBeforeCloaking, body, cloaked)
	fableState := captureClaudeCodeFableState(bodyBeforeCloaking, body, cloaked)
	// Only the Messages endpoint on Anthropic itself was captured; count_tokens
	// keeps its own shape and other gateways never see this field.
	diagnosticsState := claudeDiagnosticsRequestState{}
	if !isProbeOrHelper {
		isProbeOrHelper = helps.IsClaudeProbeOrHelperRequest(body)
	}
	if continuityCtx.Initialized {
		diagnosticsState = claudeDiagnosticsRequestState{
			key:      continuityCtx.Key,
			sequence: continuityCtx.Sequence,
			promptID: continuityCtx.PromptID,
		}
	}
	contextManagementState := claudeCodeContextManagementState{
		eligible:    cloaked && isAnthropicUpstreamBase(baseURL),
		callerOwned: gjson.GetBytes(body, "context_management").Exists(),
	}
	diagnosticsInjectedByCPA := false
	if contextManagementState.eligible {
		body, contextManagementState.automaticallyInjected = injectClaudeCodeContextManagement(body)
		if fp.InjectDiagnostics && !isProbeOrHelper {
			diagnosticsInjectedByCPA = true
			if continuityCtx.Initialized {
				body, diagnosticsState = injectClaudeDiagnosticsWithState(body, continuityCtx.Key, continuityCtx.Sequence, continuityCtx.PreviousMessageID, continuityCtx.PromptID)
			} else {
				body, diagnosticsState = injectClaudeDiagnostics(body, auth, claudeSessionID)
			}
		}
	}

	requestedModel := helps.PayloadRequestedModel(opts, req.Model)
	requestPath := helps.PayloadRequestPath(opts)
	var touchedPayloadPaths map[string]bool
	body, touchedPayloadPaths = helps.ApplyPayloadConfigWithTrackedPaths(
		e.cfg,
		baseModel,
		to.String(),
		from.String(),
		"",
		body,
		originalTranslated,
		requestedModel,
		requestPath,
		opts.Headers,
		"context_management",
		"fallbacks",
		"thinking.display",
		"diagnostics",
	)
	contextManagementState.payloadRuleTouched = touchedPayloadPaths["context_management"]
	body = reconcileClaudeCodeSystemPlacementAfterPayload(body, systemPlacementState)
	wasProbeOrHelper := isProbeOrHelper
	isProbeOrHelper = helps.IsClaudeProbeOrHelperRequest(body)
	if isProbeOrHelper {
		diagnosticsState = claudeDiagnosticsRequestState{}
		if diagnosticsInjectedByCPA && !touchedPayloadPaths["diagnostics"] {
			body, _ = sjson.DeleteBytes(body, "diagnostics")
		}
		if cloaked {
			body = helps.StripClaudeBillingTags(body)
		}
		if continuityCtx != nil {
			*continuityCtx = helps.ClaudeContinuityContext{}
		}
	} else if wasProbeOrHelper {
		// Declassified as probe (e.g. payload override changed max_tokens: 1 to normal request):
		// Initialize continuity and diagnostics if cloaked and eligible.
		if cloaked {
			sessionID := helps.ClaudeSessionIDFromContext(ctx)
			if sessionID == "" && auth != nil {
				sessionID = helps.ClaudeAgentSessionUUIDForRequest(incomingHeaders, body, body, confirmedClaudeCode)
			}
			if sessionID != "" && auth != nil {
				credIdentity := claudeDiagnosticsCredentialIdentity(auth)
				isNewTurn := helps.IsClaudeNewPromptTurn(body)
				continuityKey, seq, prevMsgID, storedPrevReq, storedPromptID := helps.BeginClaudeContinuity(credIdentity, sessionID, isNewTurn, "")
				if continuityCtx != nil {
					continuityCtx.Key = continuityKey
					continuityCtx.Sequence = seq
					continuityCtx.PreviousMessageID = prevMsgID
					continuityCtx.PreviousRequestID = storedPrevReq
					continuityCtx.PromptID = storedPromptID
					continuityCtx.Initialized = true
				}
				body = helps.InjectClaudeBillingTags(body, storedPrevReq, storedPromptID)
				if fp.InjectDiagnostics && isAnthropicUpstreamBase(baseURL) {
					body, diagnosticsState = injectClaudeDiagnosticsWithState(body, continuityKey, seq, prevMsgID, storedPromptID)
				}
			}
		}
	}
	body = reconcileClaudeCodeFableModelAfterPayload(
		body,
		fableState,
		touchedPayloadPaths["fallbacks"],
		touchedPayloadPaths["thinking.display"],
		cloaked,
		isProbeOrHelper,
	)
	body = ensureModelMaxTokens(body, baseModel)

	// Disable thinking if tool_choice forces tool use (Anthropic API constraint)
	body = disableThinkingIfToolChoiceForced(body)
	body = reconcileClaudeCodeContextManagement(body, contextManagementState)
	body = normalizeClaudeSamplingForUpstream(body, confirmedClaudeCode)

	// Default cache_control for translated entrypoints (Responses/Chat/Gemini) and other
	// non-native callers. Confirmed native Claude Code owns its marker placement and must
	// not be rewritten. Cloaked requests always run section-independent ensure so cloaking's
	// first-user marker cannot suppress system/latest-user breakpoints.
	// cloaked and confirmedClaudeCode are mutually exclusive: resolveClaudeWirePolicy
	// forces Cloak off for a confirmed native client.
	cpaOwnsCacheControl := shouldEnsureCacheControl(body, cloaked, confirmedClaudeCode)
	if cpaOwnsCacheControl {
		body = ensureCacheControl(body)
	}

	// Enforce Anthropic's cache_control block limit (max 4 breakpoints per request).
	// Cloaking and ensureCacheControl may push the total over 4 when the client
	// already sends multiple cache_control blocks.
	body = enforceCacheControlLimit(body, 4)

	// Native selects the 1h cache pool only for OAuth credentials and pairs it with
	// extended-cache-ttl-2025-04-11, which claudeCodeCLIBetas emits on exactly the
	// same credential condition. Upgrading after placement is settled mirrors the
	// native ttl helper.
	//
	// This runs only while CPA owns placement, and it then owns the ttl of every
	// breakpoint it can reach: a marker carrying no ttl is the wire default, not an
	// opt-in to 5m, so a cloaked caller's bare {"type":"ephemeral"} is upgraded too.
	// Only a ttl the caller wrote out explicitly survives, because
	// upgradeClaudeCacheControlTTL skips any block that already has one.
	// claude-code-cli fingerprint profiles emit extended-cache-ttl and must use the same 1h pool.
	// In native Claude Code 2.1.258, 1h cache and extended-cache-ttl are restricted to main
	// interaction queries (repl_main_thread*); subagents, side queries, and probes omit both.
	isSubagent := helps.IsClaudeSubagentRequest(incomingHeaders, body)
	if cpaOwnsCacheControl && fp.ProfileClaudeCodeCLI && !isSubagent && !isProbeOrHelper {
		body = upgradeClaudeCacheControlTTL(body, claudeCacheControlTTL1h)
	} else if isSubagent || isProbeOrHelper {
		body = stripClaudeCacheControlTTL(body)
	}

	// Normalize TTL values to prevent ordering violations under prompt-caching-scope-2026-01-05.
	// A 1h-TTL block must not appear after a 5m-TTL block in evaluation order (tools→system→messages).
	body = normalizeCacheControlTTL(body)
	// Payload rules and other request processing may rewrite stream. Keep the
	// upstream body, transport headers, and response parser on one authority.
	// Native non-stream Haiku helper requests omit stream rather than sending
	// false, so preserve that measured wire shape when the transport agrees.
	streamField := gjson.GetBytes(body, "stream")
	if !claudeCodeDetection.HelperProfile || streamField.Exists() || upstreamStream {
		body = helps.SetBoolIfDifferent(body, "stream", upstreamStream)
	}

	// Extract betas from body and convert to header
	var extraBetas []string
	extraBetas, body = extractAndRemoveBetas(body)
	bodyForTranslation := body
	bodyForUpstream := body
	var oauthToolNamesReverseMap map[string]string
	if fp.MCPAlias && cloaked {
		mcpAliases := resolveClaudeMCPAliasOptions(ctx)
		bodyForUpstream, oauthToolNamesReverseMap = prepareClaudeOAuthToolNamesForUpstream(bodyForUpstream, mcpAliases)
	}
	bodyForUpstream = sanitizeClaudeMessagesForClaudeUpstreamWithDebug(ctx, bodyForUpstream, baseModel, helps.APIKeyModelIsCompat(req))
	if fp.ApplyCLIIdentity {
		bodyForUpstream, err = applyClaudeCLIIdentity(bodyForUpstream, auth, apiKey, url, claudeSessionID, fp.SynthesizeIdentity)
		if err != nil {
			return resp, err
		}
	}
	if cloaked && len(wireSettings.sensitiveWords) > 0 {
		matcher := helps.BuildSensitiveWordMatcher(wireSettings.sensitiveWords)
		bodyForUpstream = helps.ObfuscateSensitiveWords(bodyForUpstream, matcher)
	}
	cchBilling := ""
	if cchSigning {
		if !claudeCodeDetection.HelperProfile || claudeBodyNeedsBillingFallback(bodyForUpstream) {
			cchBilling = claudeCCHFallbackBillingHeader(ctx, e.cfg, bodyForUpstream, claudeCodeDetection.Entrypoint)
		}
		bodyForUpstream, err = finalizeAnthropicMessagesBodyCCH(bodyForUpstream, cchBilling)
		if err != nil {
			return resp, fmt.Errorf("finalize Claude CCH: %w", err)
		}
	}
	bodyForUpstream = stripDefaultKimiClaudeCodeAttribution(auth, url, fp.ProfileClaudeCodeCLI, bodyForUpstream)
	// Runs on the finished body: payload rules can rewrite model and messages
	// long after translation, so an earlier check would not describe the request
	// that is about to be sent.
	if errMidSystem := validateClaudeMidSystemMessageModel(bodyForUpstream, confirmedClaudeCode, isAnthropicUpstreamBase(baseURL)); errMidSystem != nil {
		return resp, errMidSystem
	}
	reporter.SetTranslatedReasoningEffort(bodyForUpstream, to.String())
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyForUpstream))
	if err != nil {
		return resp, err
	}
	if errHeaders := applyClaudeHeadersWithNativeProfile(
		httpReq,
		auth,
		apiKey,
		upstreamStream,
		extraBetas,
		bodyForUpstream,
		e.cfg,
		incomingHeaders,
		confirmedClaudeCode && !cloaked,
		claudeCodeDetection.HelperProfile,
		claudeSessionID,
	); errHeaders != nil {
		return resp, errHeaders
	}
	fastRequest := isAnthropicUpstreamBase(baseURL) && claudeRequestIsFast(httpReq, bodyForUpstream)
	authID, authLabel, authType, authValue := claudeAuthLogIdentity(auth)
	helps.RecordAPIRequest(ctx, e.cfg, helps.UpstreamRequestLog{
		URL:       url,
		Method:    http.MethodPost,
		Headers:   httpReq.Header.Clone(),
		Body:      bodyForUpstream,
		Provider:  e.upstreamRequestLogProvider(),
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})

	httpClient := helps.NewUtlsHTTPClient(ctx, e.cfg, auth, 0)
	httpClient = reporter.TrackHTTPClient(httpClient)
	httpResp, err := doClaudeUpstreamRequest(httpClient, httpReq)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return resp, wrapClaudeFastRequestError(fastRequest, 0, err)
	}
	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		// Decompress error responses — pass the Content-Encoding value (may be empty)
		// and let decodeResponseBody handle both header-declared and magic-byte-detected
		// compression.  This keeps error-path behaviour consistent with the success path.
		errBody, decErr := decodeResponseBody(httpResp.Body, claudeResponseContentEncoding(httpResp.Header))
		if decErr != nil {
			helps.RecordAPIResponseError(ctx, e.cfg, decErr)
			msg := fmt.Sprintf("failed to decode error response body: %v", decErr)
			helps.LogWithRequestID(ctx).Warn(msg)
			errClassified := classifyClaudeUpstreamError(httpResp.StatusCode, httpResp.Header, []byte(msg))
			if fastRequest {
				return resp, wrapClaudeFastRequestError(fastRequest, httpResp.StatusCode, errClassified)
			}
			return resp, errClassified
		}
		b, readErr := io.ReadAll(errBody)
		if readErr != nil {
			helps.RecordAPIResponseError(ctx, e.cfg, readErr)
			msg := fmt.Sprintf("failed to read error response body: %v", readErr)
			helps.LogWithRequestID(ctx).Warn(msg)
			b = []byte(msg)
		}
		helps.AppendAPIResponseChunk(ctx, e.cfg, b)
		helps.LogWithRequestID(ctx).Debugf("request error, error status: %d, error message: %s", httpResp.StatusCode, helps.SummarizeErrorBody(httpResp.Header.Get("Content-Type"), b))
		if errClose := errBody.Close(); errClose != nil {
			log.Errorf("response body close error: %v", errClose)
		}
		if fastRequest {
			return resp, newClaudeFastDirectResponseError(httpResp, b)
		}
		return resp, classifyClaudeUpstreamError(httpResp.StatusCode, httpResp.Header, b)
	}
	decodedBody, err := decodeResponseBody(httpResp.Body, claudeResponseContentEncoding(httpResp.Header))
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("response body close error: %v", errClose)
		}
		return resp, wrapClaudeFastRequestError(fastRequest, httpResp.StatusCode, err)
	}
	defer func() {
		if errClose := decodedBody.Close(); errClose != nil {
			log.Errorf("response body close error: %v", errClose)
		}
	}()
	data, err := io.ReadAll(decodedBody)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return resp, wrapClaudeFastRequestError(fastRequest, httpResp.StatusCode, err)
	}
	helps.AppendAPIResponseChunk(ctx, e.cfg, data)
	if upstreamStream {
		if errValidate := validateClaudeStreamingResponse(data); errValidate != nil {
			helps.RecordAPIResponseError(ctx, e.cfg, errValidate)
			return resp, wrapClaudeFastRequestError(fastRequest, httpResp.StatusCode, errValidate)
		}
		if msgID := claudeMessageIDFromSSE(data); msgID != "" {
			commitClaudeContinuity(diagnosticsState, msgID, helps.HeaderValueCaseInsensitive(httpResp.Header, "request-id"))
		}
		lines := bytes.Split(data, []byte("\n"))
		for i, line := range lines {
			if detail, ok := helps.ParseClaudeStreamUsage(line); ok {
				reporter.Publish(ctx, detail)
			}
			restoredLine, errRestore := restoreClaudeOAuthToolNamesFromStreamLine(line, oauthToolNamesReverseMap)
			if errRestore != nil {
				errRestore = fmt.Errorf("restore Claude OAuth tool name from streaming response: %w", errRestore)
				helps.RecordAPIResponseError(ctx, e.cfg, errRestore)
				return resp, wrapClaudeFastRequestError(fastRequest, httpResp.StatusCode, errRestore)
			}
			lines[i] = restoredLine
		}
		data = bytes.Join(lines, []byte("\n"))
	} else {
		commitClaudeContinuity(diagnosticsState, claudeMessageIDFromResponse(data), helps.HeaderValueCaseInsensitive(httpResp.Header, "request-id"))
		reporter.Publish(ctx, helps.ParseClaudeUsage(data))
		var errRestore error
		data, errRestore = restoreClaudeOAuthToolNamesFromResponse(data, oauthToolNamesReverseMap)
		if errRestore != nil {
			errRestore = fmt.Errorf("restore Claude OAuth tool name from response: %w", errRestore)
			helps.RecordAPIResponseError(ctx, e.cfg, errRestore)
			return resp, wrapClaudeFastRequestError(fastRequest, httpResp.StatusCode, errRestore)
		}
	}
	data = e.restoreResponseModel(data, req.Model)
	cacheClaudeThinkingReplayResponse(ctx, replayScope, data)
	var param any
	out := sdktranslator.TranslateNonStream(
		ctx,
		to,
		responseFormat,
		req.Model,
		opts.OriginalRequest,
		bodyForTranslation,
		data,
		&param,
	)
	if responseFormat == sdktranslator.FormatOpenAIResponse {
		out = helps.EnsureResponsesUsageDetails(out)
	}
	resp = cliproxyexecutor.Response{Payload: out, Headers: httpResp.Header.Clone()}
	return resp, nil
}
