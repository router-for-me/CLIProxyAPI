package executor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/clienterror"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	log "github.com/sirupsen/logrus"
)

func (e *ClaudeExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	ctx = helps.EnsureSessionContext(ctx, opts, req.Payload)
	if errExpand := expandClaudeResponsesCompaction(&req, &opts); errExpand != nil {
		return resp, statusErr{code: http.StatusBadRequest, msg: errExpand.Error()}
	}
	if claudeResponsesCompactionRequested(req, opts) {
		return e.executeClaudeCompaction(ctx, auth, req, opts)
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
	if upstreamModel != baseModel {
		reporter.SetUpstreamModel(upstreamModel)
	}
	from := opts.SourceFormat
	ctx, req, opts = e.applyNativeToolSearchFallback(ctx, auth, from, upstreamModel, req, opts)
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
	wirePolicy, wireSettings := resolveClaudeWirePolicy(e.cfg, auth, apiKey, claudeCodeDetection.Confirmed)
	confirmedClaudeCode := isEffectiveConfirmedClaudeCode(claudeCodeDetection, wirePolicy)
	ctx = e.pinResponsesSystemItemPlacement(ctx, auth, from, upstreamModel, wirePolicy, wireSettings)
	claudeSessionID := ""
	if fp.ProfileClaudeCodeCLI {
		claudeSessionID = helps.ClaudeAgentSessionUUIDForRequest(incomingHeaders, originalPayload, req.Payload, confirmedClaudeCode, opts.Metadata, req.Metadata)
	}

	continuityCtx := &helps.ClaudeContinuityContext{}
	ctx = helps.WithClaudeContinuityContext(ctx, continuityCtx)
	ctx = helps.WithIncomingHeaders(ctx, incomingHeaders)
	ctx = helps.WithClaudeExecutionMetadata(ctx, helps.ClaudeRequestHasExecutionMetadata(opts.Metadata, req.Metadata))
	if claudeSessionID != "" {
		ctx = helps.WithClaudeSessionID(ctx, claudeSessionID)
	}

	webSearchSessionKey, webSearchTTL, webSearchLimit := e.claudeWebSearchResultCacheScope(ctx, auth, from, req, opts, originalPayload)
	ctx = helps.WithClaudeWebSearchResultRestore(ctx, webSearchSessionKey, webSearchTTL)

	isCompat := helps.APIKeyModelIsCompat(req)
	originalTranslated, body, err := helps.TranslateRequestPairReturningError(ctx, opts.Headers, e.cfg, from, to, baseModel, originalPayload, req.Payload, upstreamStream, isCompat)
	if err != nil {
		return resp, err
	}
	doc := ojson.NewDoc(body)
	helps.SetStringIfDifferentDoc(doc, "model", upstreamModel)
	helps.NormalizeClaudeToolChoiceDoc(e.upstreamRequestLogProvider(), upstreamModel, doc)

	if err = helps.ApplyRequestThinkingDoc(doc, req, opts, from.String(), to.String(), e.Identifier()); err != nil {
		return resp, err
	}
	helps.RestoreClaudeWebSearchResultsContextDoc(ctx, doc, webSearchSessionKey, webSearchTTL)
	if rebuildMidSystemMessageEnabled(e.cfg, auth) {
		rebuildMidSystemMessagesToTopLevelDoc(doc)
	}

	// Apply cloaking (system prompt injection, fake user ID, sensitive word obfuscation)
	// based on client type and configuration.
	isProbeOrHelper := isClaudeProbeOrHelperRequestDoc(doc)
	explicitCacheMode := isExplicitPromptCacheModeDoc(doc, originalPayload, req.Payload)
	if explicitCacheMode {
		ctx = withExplicitPromptCacheMode(ctx, true)
	}
	var cloaked bool
	cloaked, _, err = applyCloakingInternalDoc(
		ctx,
		e.cfg,
		auth,
		doc,
		apiKey,
		confirmedClaudeCode,
		cchSigning,
		false,
		explicitCacheMode,
	)
	if err != nil {
		return resp, err
	}
	// Only the Messages endpoint on Anthropic itself was captured; count_tokens
	// keeps its own shape and other gateways never see this field.
	diagnosticsState := claudeDiagnosticsRequestState{}
	if !isProbeOrHelper {
		isProbeOrHelper = isClaudeProbeOrHelperRequestDoc(doc)
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
		callerOwned: doc.Get("context_management").Exists(),
	}
	if contextManagementState.eligible {
		contextManagementState.automaticallyInjected = injectClaudeCodeContextManagementDoc(doc)
		if fp.InjectDiagnostics && !isProbeOrHelper {
			if continuityCtx.Initialized {
				diagnosticsState = injectClaudeDiagnosticsWithStateDoc(doc, continuityCtx.Key, continuityCtx.Sequence, continuityCtx.PreviousMessageID, continuityCtx.PromptID)
			} else {
				diagnosticsState = injectClaudeDiagnosticsDoc(doc, auth, claudeSessionID)
			}
		}
	}

	ensureModelMaxTokensDoc(doc, baseModel)

	// Disable thinking if tool_choice forces tool use (Anthropic API constraint)
	disableThinkingIfToolChoiceForcedDoc(doc)
	reconcileClaudeCodeContextManagementDoc(doc, contextManagementState)
	normalizeClaudeSamplingForUpstreamDoc(doc, confirmedClaudeCode)

	// Default cache_control for translated entrypoints (Responses/Chat/Gemini) and other
	// non-native callers. Confirmed native Claude Code owns its marker placement and must
	// not be rewritten. Cloaked requests always run section-independent ensure so cloaking's
	// first-user marker cannot suppress system/latest-user breakpoints.
	// cloaked and confirmedClaudeCode are mutually exclusive: resolveClaudeWirePolicy
	// forces Cloak off for a confirmed native client.
	cpaOwnsCacheControl := shouldEnsureCacheControlDoc(doc, cloaked, confirmedClaudeCode, originalPayload, req.Payload)
	if cpaOwnsCacheControl {
		ensureCacheControlDoc(doc)
	}

	// Enforce Anthropic's cache_control block limit (max 4 breakpoints per request).
	// Cloaking and ensureCacheControl may push the total over 4 when the client
	// already sends multiple cache_control blocks.
	enforceCacheControlLimitDoc(doc, 4)
	stripPromptCacheOptionsDoc(doc)

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
	// In native Claude Code, subagents default to 5m unless 1h is explicitly configured;
	// probes omit both 1h cache and extended-cache-ttl.
	isSubagent := isClaudeSubagentRequestDoc(incomingHeaders, doc)
	subagent1h := isSubagent && claudeSubagentRequests1hDoc(incomingHeaders, doc)
	if cpaOwnsCacheControl && fp.ProfileClaudeCodeCLI && (!isSubagent || subagent1h) && !isProbeOrHelper {
		upgradeClaudeCacheControlTTLDoc(doc, claudeCacheControlTTL1h)
	} else if (isProbeOrHelper || (isSubagent && !subagent1h)) && !explicitCacheMode {
		stripClaudeCacheControlTTLDoc(doc)
	}

	// Normalize TTL values to prevent ordering violations under prompt-caching-scope-2026-01-05.
	// A 1h-TTL block must not appear after a 5m-TTL block in evaluation order (tools→system→messages).
	if !explicitCacheMode {
		normalizeCacheControlTTLDoc(doc)
	}
	// Set the built-in transport default before the explicit payload barrier.
	// Native non-stream Haiku helper requests omit stream rather than sending
	// false, so preserve that measured wire shape when the transport agrees.
	streamField := doc.Get("stream")
	if !claudeCodeDetection.HelperProfile || streamField.Exists() || upstreamStream {
		helps.SetBoolIfDifferentDoc(doc, "stream", upstreamStream)
	}

	// Extract betas from body and convert to header
	var extraBetas []string
	var oauthToolNamesReverseMap map[string]string
	if fp.MCPAlias && cloaked {
		mcpAliases := resolveClaudeMCPAliasOptions(ctx, opts.Metadata, req.Metadata)
		oauthToolNamesReverseMap, err = e.prepareClaudeOAuthToolNamesForRequestDoc(doc, mcpAliases)
		if err != nil {
			return resp, err
		}
	}
	helps.NormalizeClaudeToolPropertyNamesDoc(doc)
	sanitizeClaudeMessagesForClaudeUpstreamWithDebugDoc(ctx, doc, baseModel, helps.APIKeyModelIsCompat(req))
	if fp.ApplyCLIIdentity {
		err = applyClaudeCLIIdentityDoc(doc, auth, apiKey, url, claudeSessionID, fp.SynthesizeIdentity)
		if err != nil {
			return resp, err
		}
	}
	if cloaked {
		if matcher := wireSettings.buildSensitiveWordMatcher(); matcher != nil {
			helps.ObfuscateSensitiveWordsDoc(doc, matcher)
		}
	}
	cchBilling := ""
	if cchSigning {
		if !claudeCodeDetection.HelperProfile || claudeBodyNeedsBillingFallbackDoc(doc) {
			cchBilling = claudeCCHFallbackBillingHeaderDoc(ctx, e.cfg, doc, claudeCodeDetection.Entrypoint)
		}
		err = ensureClaudeBillingHeaderCCHPlaceholderDoc(doc, cchBilling)
		if err != nil {
			return resp, fmt.Errorf("finalize Claude CCH: %w", err)
		}
	}
	stripDefaultKimiClaudeCodeAttributionDoc(auth, url, fp.ProfileClaudeCodeCLI, doc)
	if claudeCompactionSummaryFromContext(ctx) {
		finalizeClaudeCompactionSummaryBodyDoc(doc)
	}
	applyClaudePreservedThinkingBindingDoc(doc)
	// User rules match the fully prepared business body and are applied only once.
	touchedPayloadPaths := helps.ApplyPayloadConfigWithTrackedPathsDoc(
		e.cfg, baseModel, to.String(), from.String(), "", doc, stripTranslatedDefaultMaxTokens(from, originalPayload, originalTranslated),
		helps.PayloadRequestedModel(opts, req.Model), helps.PayloadRequestPath(opts), opts.Headers,
		"diagnostics", "betas",
	)
	if touchedPayloadPaths["diagnostics"] {
		diagnosticsState = claudeDiagnosticsRequestState{}
	}
	extraBetas, _ = extractAndRemoveBetasDoc(doc)
	stripPromptCacheOptionsDoc(doc)
	bodyForUpstream := doc.Bytes()
	body = bodyForUpstream
	bodyForTranslation := bodyForUpstream
	if cchSigning {
		bodyForUpstream, err = signAnthropicMessagesBody(bodyForUpstream)
		if err != nil {
			return resp, fmt.Errorf("sign Claude CCH: %w", err)
		}
	}
	// Read-only validation must observe the final configured model and messages.
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
	threadBetas, errThread := e.prepareClaudeThreadBetas(ctx, httpReq, auth, bodyForUpstream, extraBetas, touchedPayloadPaths["betas"], incomingHeaders, wirePolicy.Uncloak, opts.Metadata, req.Metadata)
	if errThread != nil {
		return resp, errThread
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
			errClassified := classifyClaudeUpstreamErrorWithCooling(httpResp.StatusCode, httpResp.Header, []byte(msg), e.modelLevelCooling())
			if fastRequest {
				return resp, wrapClaudeFastRequestError(fastRequest, httpResp.StatusCode, errClassified)
			}
			return resp, errClassified
		}
		b, readErr := io.ReadAll(errBody)
		helps.AppendAPIResponseChunk(ctx, e.cfg, b)
		if readErr != nil {
			helps.RecordAPIResponseError(ctx, e.cfg, readErr)
			msg := fmt.Sprintf("failed to read error response body: %v", readErr)
			helps.LogWithRequestID(ctx).Warn(msg)
			if len(b) == 0 {
				b = []byte(msg)
			}
		}
		helps.LogWithRequestID(ctx).Debugf("request error, error status: %d, error message: %s", httpResp.StatusCode, helps.SummarizeErrorBody(httpResp.Header.Get("Content-Type"), b))
		if errClose := errBody.Close(); errClose != nil {
			log.Errorf("response body close error: %v", errClose)
		}
		if fastRequest {
			return resp, newClaudeFastDirectResponseError(httpResp, b)
		}
		return resp, classifyClaudeUpstreamErrorWithCooling(httpResp.StatusCode, httpResp.Header, b, e.modelLevelCooling())
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
	helps.AppendAPIResponseChunk(ctx, e.cfg, data)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return resp, wrapClaudeFastRequestError(fastRequest, httpResp.StatusCode, err)
	}
	var streamUsage helps.StreamUsageBuffer
	threadMessageID := ""
	if upstreamStream {
		if errValidate := validateClaudeStreamingResponse(data); errValidate != nil {
			helps.RecordAPIResponseError(ctx, e.cfg, errValidate)
			return resp, wrapClaudeFastRequestError(fastRequest, httpResp.StatusCode, errValidate)
		}
		threadMessageID = claudeMessageIDFromSSE(data)
		if msgID := threadMessageID; msgID != "" {
			commitClaudeContinuity(diagnosticsState, msgID, helps.HeaderValueCaseInsensitive(httpResp.Header, "request-id"))
			e.rememberClaudeOAuthToolAliases(body, oauthToolNamesReverseMap, msgID)
		}
		lines := bytes.Split(data, []byte("\n"))
		var eventBuf bytes.Buffer
		var upstreamMessageID string
		var upstreamStreamErr error
		upstreamCompleted := false
		for i, line := range lines {
			eventBuf.Write(line)
			eventBuf.WriteByte('\n')
			if len(bytes.TrimSpace(line)) == 0 {
				observeClaudeSSEEvent(eventBuf.Bytes(), &upstreamMessageID, &upstreamCompleted, &upstreamStreamErr)
				streamUsage.ObserveClaudeSSEEvent(eventBuf.Bytes())
				eventBuf.Reset()
			}
			reporter.ObserveResponseModel(line)
			streamUsage.ObserveClaudeStream(line)
			restoredLine, errRestore := restoreClaudeOAuthToolNamesFromStreamLine(line, oauthToolNamesReverseMap)
			if errRestore != nil {
				errRestore = fmt.Errorf("restore Claude OAuth tool name from streaming response: %w", errRestore)
				helps.RecordAPIResponseError(ctx, e.cfg, errRestore)
				streamUsage.PublishFailure(ctx, reporter, errRestore)
				return resp, wrapClaudeFastRequestError(fastRequest, httpResp.StatusCode, errRestore)
			}
			lines[i] = restoredLine
		}
		if eventBuf.Len() > 0 {
			observeClaudeSSEEvent(eventBuf.Bytes(), &upstreamMessageID, &upstreamCompleted, &upstreamStreamErr)
			streamUsage.ObserveClaudeSSEEvent(eventBuf.Bytes())
			eventBuf.Reset()
		}
		if upstreamStreamErr != nil {
			upstreamStreamErr = wrapClaudeFastRequestError(fastRequest, httpResp.StatusCode, upstreamStreamErr)
			helps.RecordAPIResponseError(ctx, e.cfg, upstreamStreamErr)
			streamUsage.PublishFailure(ctx, reporter, upstreamStreamErr)
			return resp, upstreamStreamErr
		}
		data = bytes.Join(lines, []byte("\n"))
	} else {
		messageID := claudeMessageIDFromResponse(data)
		threadMessageID = messageID
		commitClaudeContinuity(diagnosticsState, messageID, helps.HeaderValueCaseInsensitive(httpResp.Header, "request-id"))
		e.rememberClaudeOAuthToolAliases(body, oauthToolNamesReverseMap, messageID)
		reporter.ObserveResponseModel(data)
		var errRestore error
		data, errRestore = restoreClaudeOAuthToolNamesFromResponse(data, oauthToolNamesReverseMap)
		if errRestore != nil {
			errRestore = fmt.Errorf("restore Claude OAuth tool name from response: %w", errRestore)
			helps.RecordAPIResponseError(ctx, e.cfg, errRestore)
			return resp, wrapClaudeFastRequestError(fastRequest, httpResp.StatusCode, errRestore)
		}
		if refusalMsg, blocked := clienterror.ClaudePolicyRefusalMessage(data); blocked {
			errRefusal := wrapClaudeFastRequestError(fastRequest, httpResp.StatusCode, statusErr{code: http.StatusBadRequest, msg: refusalMsg})
			helps.RecordAPIResponseError(ctx, e.cfg, errRefusal)
			reporter.PublishFailureWithDetail(ctx, helps.ParseClaudeUsage(data), errRefusal)
			return resp, errRefusal
		}
	}
	data = e.restoreResponseModel(data, req.Model)
	helps.RecordClaudeWebSearchResultsContext(ctx, webSearchSessionKey, data, webSearchTTL, webSearchLimit)
	cacheClaudeThinkingReplayResponse(ctx, replayScope, data)
	var param any
	out := sdktranslator.TranslateNonStream(
		ctx,
		to,
		responseFormat,
		req.Model,
		helps.ApplyPatchOriginalRequest(req, opts),
		bodyForTranslation,
		data,
		&param,
	)
	if helps.ApplyPatchTranslationError(param) != nil || len(out) == 0 {
		return cliproxyexecutor.Response{}, statusErr{code: http.StatusBadGateway, msg: helps.ApplyPatchUpstreamErrorMessage}
	}
	if upstreamStream {
		streamUsage.Publish(ctx, reporter)
	} else {
		reporter.Publish(ctx, helps.ParseClaudeUsage(data))
	}
	if responseFormat == sdktranslator.FormatOpenAIResponse {
		out = helps.EnsureResponsesUsageDetails(out)
	}
	threadBetas.Commit(threadMessageID)
	resp = cliproxyexecutor.Response{Payload: out, Headers: httpResp.Header.Clone()}
	return resp, nil
}
