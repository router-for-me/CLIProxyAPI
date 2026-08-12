package executor

import (
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

func (e *ClaudeExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	apiKey, baseURL := claudeCreds(auth)
	if baseURL == "" {
		baseURL = "https://api.anthropic.com"
	}
	// Only Anthropic's first-party origin has the measured native count_tokens
	// contract. Every custom/third-party base URL keeps local estimation,
	// regardless of whether the credential is OAuth or an API key.
	if shouldUseClaudeUpstreamTokenCount(apiKey, baseURL) {
		return e.countTokensUpstream(ctx, auth, req, opts)
	}

	baseModel := thinking.ParseSuffix(req.Model).ModelName
	from := opts.SourceFormat
	responseFormat := cliproxyexecutor.ResponseFormatOrSource(opts)
	to := sdktranslator.FromString("claude")

	// Use streaming translation to preserve function calling, except for claude.
	stream := from != to
	body := helps.TranslateRequestWithAPIKeyModelCompatibility(ctx, opts.Headers, e.cfg, from, to, baseModel, req.Payload, stream, helps.APIKeyModelIsCompat(req))
	var errThinking error
	body, errThinking = helps.ApplyRequestThinking(body, req, opts, from.String(), to.String(), e.Identifier())
	if errThinking != nil {
		return cliproxyexecutor.Response{}, errThinking
	}
	if rebuildMidSystemMessageEnabled(e.cfg, auth) {
		body = rebuildMidSystemMessagesToTopLevel(body)
	}
	body = sanitizeClaudeMessagesForClaudeUpstreamWithDebug(ctx, body, baseModel, helps.APIKeyModelIsCompat(req))
	if errValidate := validateClaudeTokenCountRequest(body); errValidate != nil {
		return cliproxyexecutor.Response{}, errValidate
	}

	// Custom API-key gateways without a native count_tokens contract continue to
	// use the local estimator without injecting generation-only CLI instructions.
	count, err := helps.CountClaudeInputTokens(body)
	if err != nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("claude executor: token counting failed: %w", err)
	}

	usageJSON := []byte(fmt.Sprintf(`{"input_tokens":%d}`, count))
	out := sdktranslator.TranslateTokenCount(ctx, to, responseFormat, count, usageJSON)
	return cliproxyexecutor.Response{Payload: out}, nil
}

type claudeTokenCountValidationError struct {
	statusErr
}

func (claudeTokenCountValidationError) IsRequestScoped() bool {
	return true
}

func newClaudeTokenCountValidationError(message string) error {
	return claudeTokenCountValidationError{statusErr{code: http.StatusBadRequest, msg: message}}
}

func validateClaudeTokenCountRequest(body []byte) error {
	if !gjson.ValidBytes(body) {
		return newClaudeTokenCountValidationError("invalid Claude token count request JSON")
	}
	root := gjson.ParseBytes(body)
	if !root.IsObject() {
		return newClaudeTokenCountValidationError("Claude token count request must be a JSON object")
	}
	messages := root.Get("messages")
	if !messages.IsArray() || len(messages.Array()) == 0 {
		return newClaudeTokenCountValidationError("Claude token count request messages must be a non-empty array")
	}
	for _, message := range messages.Array() {
		if !message.IsObject() {
			return newClaudeTokenCountValidationError("Claude token count request messages must contain objects")
		}
		role := message.Get("role").String()
		if role != "user" && role != "assistant" {
			return newClaudeTokenCountValidationError("Claude token count request message role must be user or assistant")
		}
		content := message.Get("content")
		if content.Type == gjson.String {
			continue
		}
		if !content.IsArray() {
			return newClaudeTokenCountValidationError("Claude token count request message content must be a string or array")
		}
		for _, block := range content.Array() {
			if !block.IsObject() || block.Get("type").Type != gjson.String || block.Get("type").String() == "" {
				return newClaudeTokenCountValidationError("Claude token count request content blocks must be typed objects")
			}
		}
	}
	return nil
}

func shouldUseClaudeUpstreamTokenCount(apiKey, baseURL string) bool {
	return strings.TrimSpace(apiKey) != "" && isAnthropicUpstreamBase(baseURL)
}

// countTokensUpstream preserves Anthropic's native token-counting contract.
func (e *ClaudeExecutor) countTokensUpstream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	upstreamModel := e.upstreamModel(baseModel)

	apiKey, baseURL := claudeCreds(auth)
	if baseURL == "" {
		baseURL = "https://api.anthropic.com"
	}
	url := fmt.Sprintf("%s/v1/messages/count_tokens?beta=true", baseURL)
	oauthToken := isClaudeOAuthToken(apiKey)

	from := opts.SourceFormat
	responseFormat := cliproxyexecutor.ResponseFormatOrSource(opts)
	to := sdktranslator.FromString("claude")
	originalPayload := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayload = opts.OriginalRequest
	}
	incomingHeaders, claudeCodeDetection := detectIncomingClaudeCodeRequest(ctx, opts.Headers, originalPayload, true, e.cfg)
	confirmedClaudeCode := claudeCodeDetection.Confirmed
	claudeSessionID := ""
	if oauthToken {
		claudeSessionID = helps.ClaudeAgentSessionUUIDForRequest(incomingHeaders, originalPayload, req.Payload, confirmedClaudeCode, opts.Metadata, req.Metadata)
	}
	// Use streaming translation to preserve function calling, except for claude.
	stream := from != to
	body := helps.TranslateRequestWithAPIKeyModelCompatibility(ctx, opts.Headers, e.cfg, from, to, baseModel, req.Payload, stream, helps.APIKeyModelIsCompat(req))
	body = helps.SetStringIfDifferent(body, "model", upstreamModel)
	var errThinking error
	body, errThinking = helps.ApplyRequestThinking(body, req, opts, from.String(), to.String(), e.Identifier())
	if errThinking != nil {
		return cliproxyexecutor.Response{}, errThinking
	}
	if rebuildMidSystemMessageEnabled(e.cfg, auth) {
		body = rebuildMidSystemMessagesToTopLevel(body)
	}

	directAnthropic := isAnthropicUpstreamBase(baseURL)
	var cloaked bool
	if directAnthropic {
		// Claude Code's count_tokens carries only model, messages and tools, so the
		// full Messages cloaking must not run here. Apply the parts that still have
		// to hold: relocate the caller's system prompt into messages so its tokens
		// stay counted, and obfuscate sensitive words exactly like the Messages path.
		policy, settings := resolveClaudeWirePolicy(e.cfg, auth, apiKey, confirmedClaudeCode)
		cloaked = policy.Cloak
		if cloaked {
			if !settings.strictMode {
				if errSystem := validateClaudeCallerSystemBlocks(gjson.GetBytes(body, "system")); errSystem != nil {
					return cliproxyexecutor.Response{}, errSystem
				}
			}
			body = relocateClaudeSystemPromptForCountTokens(body, settings.strictMode)
			if len(settings.sensitiveWords) > 0 {
				body = helps.ObfuscateSensitiveWords(body, helps.BuildSensitiveWordMatcher(settings.sensitiveWords))
			}
		}
	} else {
		var errCloaking error
		body, cloaked, errCloaking = applyCloaking(
			ctx,
			e.cfg,
			auth,
			body,
			apiKey,
			confirmedClaudeCode,
			false,
		)
		if errCloaking != nil {
			return cliproxyexecutor.Response{}, errCloaking
		}
	}

	// Keep count_tokens requests compatible with Anthropic cache-control constraints too.
	body = enforceCacheControlLimit(body, 4)
	body = normalizeCacheControlTTL(body)

	// Extract betas from body and convert to header (for count_tokens too)
	var extraBetas []string
	extraBetas, body = extractAndRemoveBetas(body)
	// Claude Code 2.1.220's beta.messages.countTokens() always appends this beta.
	extraBetas = append(extraBetas, claudeTokenCountingBeta)
	if oauthToken && cloaked {
		mcpAliases := resolveClaudeMCPAliasOptions(ctx)
		body, _ = prepareClaudeOAuthToolNamesForUpstream(body, mcpAliases)
	}
	body = sanitizeClaudeMessagesForClaudeUpstreamWithDebug(ctx, body, baseModel, helps.APIKeyModelIsCompat(req))
	// Claude Code never sends metadata on count_tokens, and Anthropic rejects the
	// field outright there ("metadata: Extra inputs are not permitted"). The
	// Messages path still carries the credential identity; this endpoint must not.
	if directAnthropic {
		body, _ = sjson.DeleteBytes(body, "metadata")
		body, _ = sjson.DeleteBytes(body, "context_management")
		body, _ = sjson.DeleteBytes(body, "diagnostics")
	}
	// Runs on the finished body: payload rules can rewrite model and messages
	// long after translation, so an earlier check would not describe the request
	// that is about to be sent.
	if errMidSystem := validateClaudeMidSystemMessageModel(body, confirmedClaudeCode, directAnthropic); errMidSystem != nil {
		return cliproxyexecutor.Response{}, errMidSystem
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}
	if errHeaders := applyClaudeHeaders(httpReq, auth, apiKey, false, extraBetas, body, e.cfg, incomingHeaders, confirmedClaudeCode && !cloaked, claudeSessionID); errHeaders != nil {
		return cliproxyexecutor.Response{}, errHeaders
	}
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
		Body:      body,
		Provider:  e.upstreamRequestLogProvider(),
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})

	httpClient := helps.NewUtlsHTTPClient(ctx, e.cfg, auth, 0)
	resp, err := doClaudeUpstreamRequest(httpClient, httpReq)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return cliproxyexecutor.Response{}, err
	}
	helps.RecordAPIResponseMetadata(ctx, e.cfg, resp.StatusCode, resp.Header.Clone())
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Decompress error responses — pass the Content-Encoding value (may be empty)
		// and let decodeResponseBody handle both header-declared and magic-byte-detected
		// compression.  This keeps error-path behaviour consistent with the success path.
		errBody, decErr := decodeResponseBody(resp.Body, claudeResponseContentEncoding(resp.Header))
		if decErr != nil {
			helps.RecordAPIResponseError(ctx, e.cfg, decErr)
			msg := fmt.Sprintf("failed to decode error response body: %v", decErr)
			helps.LogWithRequestID(ctx).Warn(msg)
			return cliproxyexecutor.Response{}, statusErr{code: resp.StatusCode, msg: msg}
		}
		b, readErr := io.ReadAll(errBody)
		if readErr != nil {
			helps.RecordAPIResponseError(ctx, e.cfg, readErr)
			msg := fmt.Sprintf("failed to read error response body: %v", readErr)
			helps.LogWithRequestID(ctx).Warn(msg)
			b = []byte(msg)
		}
		helps.AppendAPIResponseChunk(ctx, e.cfg, b)
		if errClose := errBody.Close(); errClose != nil {
			log.Errorf("response body close error: %v", errClose)
		}
		return cliproxyexecutor.Response{}, statusErr{code: resp.StatusCode, msg: string(b)}
	}
	decodedBody, err := decodeResponseBody(resp.Body, claudeResponseContentEncoding(resp.Header))
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("response body close error: %v", errClose)
		}
		return cliproxyexecutor.Response{}, err
	}
	defer func() {
		if errClose := decodedBody.Close(); errClose != nil {
			log.Errorf("response body close error: %v", errClose)
		}
	}()
	data, err := io.ReadAll(decodedBody)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return cliproxyexecutor.Response{}, err
	}
	helps.AppendAPIResponseChunk(ctx, e.cfg, data)
	count := gjson.GetBytes(data, "input_tokens").Int()
	out := sdktranslator.TranslateTokenCount(ctx, to, responseFormat, count, data)
	return cliproxyexecutor.Response{Payload: out, Headers: resp.Header.Clone()}, nil
}
