package executor

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func (e *ClaudeExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	if opts.Alt == "responses/compact" {
		return nil, statusErr{code: http.StatusNotImplemented, msg: "/responses/compact not supported"}
	}
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	upstreamModel := e.upstreamModel(baseModel)

	apiKey, baseURL := claudeCreds(auth)
	if baseURL == "" {
		baseURL = "https://api.anthropic.com"
	}
	url := fmt.Sprintf("%s/v1/messages?beta=true", baseURL)
	fp := resolveClaudeFingerprintPolicy(e.cfg, auth, apiKey)
	defer func() {
		if cancelErr := newClaudeOAuthCancellationError(ctx, fp.OAuthCancellation, err); cancelErr != nil {
			err = cancelErr
		}
	}()
	// Same split as Execute: OAuth signs everywhere, an opted-in API key only on
	// upstreams the native gate accepts.
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

	originalTranslated := helps.TranslateRequestWithAPIKeyModelCompatibility(ctx, opts.Headers, e.cfg, from, to, baseModel, originalPayload, true, helps.APIKeyModelIsCompat(req))
	body := helps.TranslateRequestWithAPIKeyModelCompatibility(ctx, opts.Headers, e.cfg, from, to, baseModel, req.Payload, true, helps.APIKeyModelIsCompat(req))
	body = helps.SetStringIfDifferent(body, "model", upstreamModel)

	body, err = helps.ApplyRequestThinking(body, req, opts, from.String(), to.String(), e.Identifier())
	if err != nil {
		return nil, err
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
		return nil, err
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
	body = normalizeCacheControlTTL(body)

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
			return nil, err
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
			return nil, fmt.Errorf("finalize Claude CCH: %w", err)
		}
	}
	bodyForUpstream = stripDefaultKimiClaudeCodeAttribution(auth, url, fp.ProfileClaudeCodeCLI, bodyForUpstream)
	// Runs on the finished body: payload rules can rewrite model and messages
	// long after translation, so an earlier check would not describe the request
	// that is about to be sent.
	if errMidSystem := validateClaudeMidSystemMessageModel(bodyForUpstream, confirmedClaudeCode, isAnthropicUpstreamBase(baseURL)); errMidSystem != nil {
		return nil, errMidSystem
	}
	reporter.SetTranslatedReasoningEffort(bodyForUpstream, to.String())
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyForUpstream))
	if err != nil {
		return nil, err
	}
	if errHeaders := applyClaudeHeadersWithNativeProfile(
		httpReq,
		auth,
		apiKey,
		true,
		extraBetas,
		bodyForUpstream,
		e.cfg,
		incomingHeaders,
		confirmedClaudeCode && !cloaked,
		claudeCodeDetection.HelperProfile,
		claudeSessionID,
	); errHeaders != nil {
		return nil, errHeaders
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
		return nil, wrapClaudeFastRequestError(fastRequest, 0, err)
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
				return nil, wrapClaudeFastRequestError(fastRequest, httpResp.StatusCode, errClassified)
			}
			return nil, errClassified
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
			return nil, newClaudeFastDirectResponseError(httpResp, b)
		}
		return nil, classifyClaudeUpstreamError(httpResp.StatusCode, httpResp.Header, b)
	}
	decodedBody, err := decodeResponseBody(httpResp.Body, claudeResponseContentEncoding(httpResp.Header))
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("response body close error: %v", errClose)
		}
		return nil, wrapClaudeFastRequestError(fastRequest, httpResp.StatusCode, err)
	}
	out := make(chan cliproxyexecutor.StreamChunk, 1)
	go func() {
		defer close(out)
		defer func() {
			if errClose := decodedBody.Close(); errClose != nil {
				log.Errorf("response body close error: %v", errClose)
			}
		}()
		emitCancellation := func(cause error) bool {
			cancelErr := newClaudeOAuthCancellationError(ctx, fp.OAuthCancellation, cause)
			if cancelErr == nil {
				return false
			}
			helps.RecordAPIResponseError(ctx, e.cfg, cancelErr)
			reporter.PublishFailure(ctx, cancelErr)
			select {
			case out <- cliproxyexecutor.StreamChunk{Err: cancelErr}:
			default:
			}
			return true
		}
		emitResponseError := func(errResponse error) {
			errResponse = wrapClaudeFastRequestError(fastRequest, httpResp.StatusCode, errResponse)
			helps.RecordAPIResponseError(ctx, e.cfg, errResponse)
			reporter.PublishFailure(ctx, errResponse)
			select {
			case out <- cliproxyexecutor.StreamChunk{Err: errResponse}:
			case <-ctx.Done():
			}
		}

		// If the response target is Claude, directly forward complete SSE events without translation.
		if responseFormat == to {
			scanner := bufio.NewScanner(decodedBody)
			scanner.Buffer(nil, 52_428_800) // 50MB
			var event bytes.Buffer
			var upstreamMessageID string
			upstreamCompleted := false
			flushEvent := func() bool {
				if event.Len() == 0 {
					return true
				}
				cloned := bytes.Clone(event.Bytes())
				event.Reset()
				select {
				case out <- cliproxyexecutor.StreamChunk{Payload: cloned}:
					return true
				case <-ctx.Done():
					return false
				}
			}
			for scanner.Scan() {
				line := scanner.Bytes()
				observeClaudeStreamLine(line, &upstreamMessageID, &upstreamCompleted)
				helps.AppendAPIResponseChunk(ctx, e.cfg, line)
				if detail, ok := helps.ParseClaudeStreamUsage(line); ok {
					reporter.Publish(ctx, detail)
				}
				restoredLine, errRestore := restoreClaudeOAuthToolNamesFromStreamLine(line, oauthToolNamesReverseMap)
				if errRestore != nil {
					emitResponseError(fmt.Errorf("restore Claude OAuth tool name from streaming response: %w", errRestore))
					return
				}
				line = e.restoreResponseModel(restoredLine, req.Model)
				event.Write(line)
				event.WriteByte('\n')
				if len(bytes.TrimSpace(line)) == 0 && !flushEvent() {
					emitCancellation(ctx.Err())
					return
				}
			}
			if !flushEvent() {
				emitCancellation(ctx.Err())
				return
			}
			if emitCancellation(scanner.Err()) {
				return
			}
			if errScan := scanner.Err(); errScan != nil {
				errScan = wrapClaudeFastRequestError(fastRequest, httpResp.StatusCode, errScan)
				helps.RecordAPIResponseError(ctx, e.cfg, errScan)
				reporter.PublishFailure(ctx, errScan)
				select {
				case out <- cliproxyexecutor.StreamChunk{Err: errScan}:
				case <-ctx.Done():
				}
				return
			}
			if upstreamCompleted {
				commitClaudeContinuity(diagnosticsState, upstreamMessageID, helps.HeaderValueCaseInsensitive(httpResp.Header, "request-id"))
			}
			return
		}

		// For other formats, use translation
		scanner := bufio.NewScanner(decodedBody)
		scanner.Buffer(nil, 52_428_800) // 50MB
		var param any
		var upstreamMessageID string
		upstreamCompleted := false
		for scanner.Scan() {
			line := scanner.Bytes()
			observeClaudeStreamLine(line, &upstreamMessageID, &upstreamCompleted)
			helps.AppendAPIResponseChunk(ctx, e.cfg, line)
			if detail, ok := helps.ParseClaudeStreamUsage(line); ok {
				reporter.Publish(ctx, detail)
			}
			restoredLine, errRestore := restoreClaudeOAuthToolNamesFromStreamLine(line, oauthToolNamesReverseMap)
			if errRestore != nil {
				emitResponseError(fmt.Errorf("restore Claude OAuth tool name from streaming response: %w", errRestore))
				return
			}
			line = e.restoreResponseModel(restoredLine, req.Model)
			chunks := sdktranslator.TranslateStream(
				ctx,
				to,
				responseFormat,
				req.Model,
				opts.OriginalRequest,
				bodyForTranslation,
				bytes.Clone(line),
				&param,
			)
			if responseFormat == sdktranslator.FormatOpenAIResponse {
				for i, chunk := range chunks {
					chunks[i] = helps.EnsureResponsesUsageDetails(chunk)
				}
			}
			for i := range chunks {
				select {
				case out <- cliproxyexecutor.StreamChunk{Payload: chunks[i]}:
				case <-ctx.Done():
					emitCancellation(ctx.Err())
					return
				}
			}
		}
		if emitCancellation(scanner.Err()) {
			return
		}
		if errScan := scanner.Err(); errScan != nil {
			errScan = wrapClaudeFastRequestError(fastRequest, httpResp.StatusCode, errScan)
			helps.RecordAPIResponseError(ctx, e.cfg, errScan)
			reporter.PublishFailure(ctx, errScan)
			select {
			case out <- cliproxyexecutor.StreamChunk{Err: errScan}:
			case <-ctx.Done():
			}
			return
		}
		if upstreamCompleted {
			commitClaudeContinuity(diagnosticsState, upstreamMessageID, helps.HeaderValueCaseInsensitive(httpResp.Header, "request-id"))
		}
	}()
	result := &cliproxyexecutor.StreamResult{Headers: httpResp.Header.Clone(), Chunks: out}
	if replayScope.valid() {
		result = wrapClaudeThinkingReplayStream(ctx, result, replayScope)
	}
	return result, nil
}

func validateClaudeStreamingResponse(data []byte) error {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(nil, 52_428_800)

	hasData := false
	hasMessageStart := false
	hasMessageDelta := false

	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 || !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		payload := bytes.TrimSpace(line[len("data:"):])
		if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) {
			continue
		}
		hasData = true
		if !gjson.ValidBytes(payload) {
			return statusErr{code: http.StatusBadGateway, msg: "claude executor: upstream returned malformed stream data"}
		}

		root := gjson.ParseBytes(payload)
		switch root.Get("type").String() {
		case "error":
			message := strings.TrimSpace(root.Get("error.message").String())
			if message == "" {
				message = strings.TrimSpace(root.Get("error.type").String())
			}
			if message == "" {
				message = "unknown upstream error"
			}
			return statusErr{code: http.StatusBadGateway, msg: "claude executor: upstream returned error event: " + message}
		case "message_start":
			message := root.Get("message")
			if strings.TrimSpace(message.Get("id").String()) == "" || strings.TrimSpace(message.Get("model").String()) == "" {
				return statusErr{code: http.StatusBadGateway, msg: "claude executor: upstream stream message_start is missing id or model"}
			}
			hasMessageStart = true
		case "message_delta":
			hasMessageDelta = true
		}
	}
	if errScan := scanner.Err(); errScan != nil {
		return errScan
	}
	if !hasData {
		return statusErr{code: http.StatusBadGateway, msg: "claude executor: upstream returned empty stream response"}
	}
	if !hasMessageStart {
		return statusErr{code: http.StatusBadGateway, msg: "claude executor: upstream stream response is missing message_start"}
	}
	if !hasMessageDelta {
		return statusErr{code: http.StatusBadGateway, msg: "claude executor: upstream stream response ended before message completion"}
	}
	return nil
}
