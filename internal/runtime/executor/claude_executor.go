package executor

import (
	"bufio"
	"bytes"
	"compress/flate"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/andybalholm/brotli"
	"github.com/google/uuid"
	"github.com/klauspost/compress/zstd"
	claudeauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/claude"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/misc"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"github.com/gin-gonic/gin"
)

// ClaudeExecutor is a stateless executor for Anthropic Claude over the messages API.
// If api_key is unavailable on auth, it falls back to legacy via ClientAdapter.
type ClaudeExecutor struct {
	cfg *config.Config
}

// claudeToolPrefix is empty to match real Claude Code behavior (no tool name prefix).
// Previously "proxy_" was used but this is a detectable fingerprint difference.
const claudeToolPrefix = ""

// oauthToolRenameMap maps OpenCode-style (lowercase) tool names to Claude Code-style
// (TitleCase) names. Anthropic uses tool name fingerprinting to detect third-party
// clients on OAuth traffic. Renaming to official names avoids extra-usage billing.
// All tools are mapped to TitleCase equivalents to match Claude Code naming patterns.
var oauthToolRenameMap = map[string]string{
	"bash":         "Bash",
	"read":         "Read",
	"write":        "Write",
	"edit":         "Edit",
	"glob":         "Glob",
	"grep":         "Grep",
	"task":         "Task",
	"webfetch":     "WebFetch",
	"todowrite":    "TodoWrite",
	"question":     "Question",
	"skill":        "Skill",
	"ls":           "LS",
	"todoread":     "TodoRead",
	"notebookedit": "NotebookEdit",
}

// The reverse map is now computed per-request in remapOAuthToolNames so that
// only names the client actually caused us to rewrite are restored on the
// response. A global reverse map — as used previously — corrupted responses
// for clients that sent mixed casing (e.g. Amp CLI sends `Bash` TitleCase
// alongside `glob` lowercase; the request flagged renames via `glob→Glob`,
// then the global reverse map incorrectly rewrote every `Bash` in the
// response to `bash`, causing Amp to reject the tool_use as unknown).

// oauthToolsToRemove lists tool names that must be stripped from OAuth requests
// even after remapping. Currently empty — all tools are mapped instead of removed.
var oauthToolsToRemove = map[string]bool{}

// Anthropic-compatible upstreams may reject or even crash when Claude models
// omit max_tokens. Prefer registered model metadata before using a fallback.
const defaultModelMaxTokens = 1024

func NewClaudeExecutor(cfg *config.Config) *ClaudeExecutor { return &ClaudeExecutor{cfg: cfg} }

func (e *ClaudeExecutor) Identifier() string { return "claude" }

// PrepareRequest injects Claude credentials into the outgoing HTTP request.
func (e *ClaudeExecutor) PrepareRequest(req *http.Request, auth *cliproxyauth.Auth) error {
	if req == nil {
		return nil
	}
	apiKey, _ := claudeCreds(auth)
	if strings.TrimSpace(apiKey) == "" {
		return nil
	}
	useAPIKey := auth != nil && auth.Attributes != nil && strings.TrimSpace(auth.Attributes["api_key"]) != ""
	isAnthropicBase := req.URL != nil && strings.EqualFold(req.URL.Scheme, "https") && strings.EqualFold(req.URL.Host, "api.anthropic.com")
	if isAnthropicBase && useAPIKey {
		req.Header.Del("Authorization")
		req.Header.Set("x-api-key", apiKey)
	} else {
		req.Header.Del("x-api-key")
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(req, attrs)
	return nil
}

// HttpRequest injects Claude credentials into the request and executes it.
func (e *ClaudeExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("claude executor: request is nil")
	}
	if ctx == nil {
		ctx = req.Context()
	}
	httpReq := req.WithContext(ctx)
	if err := e.PrepareRequest(httpReq, auth); err != nil {
		return nil, err
	}
	toolNameSanitization, errSanitize := sanitizeClaudeHTTPRequestToolNames(httpReq)
	if errSanitize != nil {
		return nil, errSanitize
	}
	httpClient := helps.NewUtlsHTTPClient(e.cfg, auth, 0)
	resp, errDo := httpClient.Do(httpReq)
	if errDo != nil {
		return nil, errDo
	}
	restoreClaudeHTTPResponseToolNames(resp, toolNameSanitization)
	return resp, nil
}

func (e *ClaudeExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	if opts.Alt == "responses/compact" {
		return resp, statusErr{code: http.StatusNotImplemented, msg: "/responses/compact not supported"}
	}
	baseModel := thinking.ParseSuffix(req.Model).ModelName

	apiKey, baseURL := claudeCreds(auth)
	if baseURL == "" {
		baseURL = "https://api.anthropic.com"
	}
	compatKind := claudeCompatKind(auth, baseURL)

	reporter := helps.NewUsageReporter(ctx, e.Identifier(), baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)
	from := opts.SourceFormat
	to := sdktranslator.FromString("claude")
	// Use streaming translation to preserve function calling, except for claude.
	stream := from != to
	originalPayloadSource := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayloadSource = opts.OriginalRequest
	}
	payloadSource := req.Payload
	if repaired, ok := helps.RepairInvalidJSONStringEscapes(originalPayloadSource); ok {
		originalPayloadSource = repaired
	}
	if repaired, ok := helps.RepairInvalidJSONStringEscapes(payloadSource); ok {
		payloadSource = repaired
	}
	originalPayload := originalPayloadSource
	originalTranslated := sdktranslator.TranslateRequest(from, to, baseModel, originalPayload, stream)
	body := sdktranslator.TranslateRequest(from, to, baseModel, payloadSource, stream)
	if repaired, ok := helps.RepairInvalidJSONStringEscapes(originalTranslated); ok {
		originalTranslated = repaired
	}
	if repaired, ok := helps.RepairInvalidJSONStringEscapes(body); ok {
		body = repaired
	}
	body, _ = sjson.SetBytes(body, "model", baseModel)

	body, err = thinking.ApplyThinking(body, req.Model, from.String(), to.String(), e.Identifier())
	if err != nil {
		return resp, err
	}

	// Apply cloaking (system prompt injection, fake user ID, sensitive word obfuscation)
	// based on client type and configuration.
	body = applyCloaking(ctx, e.cfg, auth, body, baseModel, apiKey)

	requestedModel := helps.PayloadRequestedModel(opts, req.Model)
	requestPath := helps.PayloadRequestPath(opts)
	repairMeta := newCompatRepairLogMeta(opts, requestedModel, baseModel, e.Identifier(), "ClaudeExecutor", requestPath, compatKind)
	body = helps.ApplyPayloadConfigWithRequest(e.cfg, baseModel, to.String(), from.String(), "", body, originalTranslated, requestedModel, requestPath, opts.Headers)
	body = scrubDeepSeekThinkingBudgetForCompat(body, baseModel, baseURL, compatKind)
	body = ensureModelMaxTokens(body, baseModel)
	body = normalizeClaudeSystemRoleMessages(body)

	// Disable thinking if tool_choice forces tool use (Anthropic API constraint)
	body = disableThinkingIfToolChoiceForced(body)
	body = normalizeClaudeTemperatureForThinking(body)
	body, _, _, err = normalizeThinkingHistoryForModel(body, "claude", baseModel)
	if err != nil {
		return resp, err
	}
	body, err = repairClaudeToolUseHistoryWithCompatLog(ctx, body, repairMeta)
	if err != nil {
		return resp, err
	}
	body, _, err = normalizeClaudeEmptyToolResults(body)
	if err != nil {
		return resp, err
	}
	body = downgradeClaudeToolSearchForCompatKind(compatKind, baseURL, body)

	// Apply cache_control phases sharing a single payload scan (byte-equivalent
	// to the legacy inject → enforce → normalize sequence):
	//   1. Auto-inject cache_control if missing (ClawdBot/clients without caching support).
	//   2. Enforce Anthropic's max-4-breakpoint limit. Cloaking and injection may
	//      push the total over 4 when the client (e.g. Amp CLI) already sends blocks.
	//   3. Normalize TTL ordering under prompt-caching-scope-2026-01-05: a 1h block
	//      must not appear after a 5m block in evaluation order (tools→system→messages).
	body = applyCacheControlPipeline(body, 4, true)
	body, err = repairMiniMaxClaudeToolAdjacencyForCompatWithLog(ctx, body, repairMeta)
	if err != nil {
		return resp, err
	}

	// Extract betas from body and convert to header
	var extraBetas []string
	extraBetas, body = extractAndRemoveBetas(body)
	bodyForTranslation := body
	bodyForUpstream := body
	bodyForUpstream = downgradeClaudeStructuredOutputForCompat(baseURL, bodyForUpstream)
	oauthToken := isClaudeOAuthToken(apiKey)
	var oauthToolNamesReverseMap map[string]string
	if oauthToken {
		bodyForUpstream, oauthToolNamesReverseMap = prepareClaudeOAuthToolNamesForUpstream(bodyForUpstream, claudeToolPrefix, auth.ToolPrefixDisabled())
	}
	var toolNameSanitization *claudeToolNameSanitization
	bodyForUpstream, toolNameSanitization = sanitizeClaudeToolNamesForUpstream(bodyForUpstream)
	// Enable cch signing by default for OAuth tokens (not just experimental flag).
	// Claude Code always computes cch; missing or invalid cch is a detectable fingerprint.
	if oauthToken || experimentalCCHSigningEnabled(e.cfg, auth) {
		bodyForUpstream = signAnthropicMessagesBody(bodyForUpstream)
	}
	if errValidate := validateClaudeUpstreamPayloadForCompat(compatKind, bodyForUpstream); errValidate != nil {
		return resp, errValidate
	}

	url := fmt.Sprintf("%s/v1/messages?beta=true", baseURL)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyForUpstream))
	if err != nil {
		return resp, err
	}
	applyClaudeHeaders(httpReq, auth, apiKey, false, extraBetas, e.cfg)
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
		Body:      bodyForUpstream,
		Provider:  e.Identifier(),
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})

	httpClient := helps.NewUtlsHTTPClient(e.cfg, auth, 0)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		// Decompress error responses — pass the Content-Encoding value (may be empty)
		// and let decodeResponseBody handle both header-declared and magic-byte-detected
		// compression.  This keeps error-path behaviour consistent with the success path.
		errBody, decErr := decodeResponseBody(httpResp.Body, httpResp.Header.Get("Content-Encoding"))
		if decErr != nil {
			helps.RecordAPIResponseError(ctx, e.cfg, decErr)
			msg := fmt.Sprintf("failed to decode error response body: %v", decErr)
			helps.LogWithRequestID(ctx).Warn(msg)
			return resp, statusErr{code: httpResp.StatusCode, msg: msg}
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
		err = statusErr{code: httpResp.StatusCode, msg: string(b)}
		if errClose := errBody.Close(); errClose != nil {
			log.Errorf("response body close error: %v", errClose)
		}
		return resp, err
	}
	decodedBody, err := decodeResponseBody(httpResp.Body, httpResp.Header.Get("Content-Encoding"))
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("response body close error: %v", errClose)
		}
		return resp, err
	}
	defer func() {
		if errClose := decodedBody.Close(); errClose != nil {
			log.Errorf("response body close error: %v", errClose)
		}
	}()
	data, err := io.ReadAll(decodedBody)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	helps.AppendAPIResponseChunk(ctx, e.cfg, data)
	if stream {
		if errValidate := validateClaudeStreamingResponse(data); errValidate != nil {
			helps.RecordAPIResponseError(ctx, e.cfg, errValidate)
			return resp, errValidate
		}
		lines := bytes.Split(data, []byte("\n"))
		for _, line := range lines {
			if detail, ok := helps.ParseClaudeStreamUsage(line); ok {
				reporter.Publish(ctx, detail)
			}
		}
	} else {
		reporter.Publish(ctx, helps.ParseClaudeUsage(data))
	}
	data = restoreClaudeToolNamesFromResponse(data, toolNameSanitization)
	if oauthToken {
		data = restoreClaudeOAuthToolNamesFromResponse(data, claudeToolPrefix, auth.ToolPrefixDisabled(), oauthToolNamesReverseMap)
	}
	var param any
	out := sdktranslator.TranslateNonStream(
		ctx,
		to,
		from,
		req.Model,
		opts.OriginalRequest,
		bodyForTranslation,
		data,
		&param,
	)
	resp = cliproxyexecutor.Response{Payload: out, Headers: httpResp.Header.Clone()}
	return resp, nil
}

func (e *ClaudeExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	if opts.Alt == "responses/compact" {
		return nil, statusErr{code: http.StatusNotImplemented, msg: "/responses/compact not supported"}
	}
	baseModel := thinking.ParseSuffix(req.Model).ModelName

	apiKey, baseURL := claudeCreds(auth)
	if baseURL == "" {
		baseURL = "https://api.anthropic.com"
	}
	compatKind := claudeCompatKind(auth, baseURL)

	reporter := helps.NewUsageReporter(ctx, e.Identifier(), baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)
	from := opts.SourceFormat
	to := sdktranslator.FromString("claude")
	originalPayloadSource := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayloadSource = opts.OriginalRequest
	}
	payloadSource := req.Payload
	if repaired, ok := helps.RepairInvalidJSONStringEscapes(originalPayloadSource); ok {
		originalPayloadSource = repaired
	}
	if repaired, ok := helps.RepairInvalidJSONStringEscapes(payloadSource); ok {
		payloadSource = repaired
	}
	originalPayload := originalPayloadSource
	originalTranslated := sdktranslator.TranslateRequest(from, to, baseModel, originalPayload, true)
	body := sdktranslator.TranslateRequest(from, to, baseModel, payloadSource, true)
	if repaired, ok := helps.RepairInvalidJSONStringEscapes(originalTranslated); ok {
		originalTranslated = repaired
	}
	if repaired, ok := helps.RepairInvalidJSONStringEscapes(body); ok {
		body = repaired
	}
	body, _ = sjson.SetBytes(body, "model", baseModel)

	body, err = thinking.ApplyThinking(body, req.Model, from.String(), to.String(), e.Identifier())
	if err != nil {
		return nil, err
	}

	// Apply cloaking (system prompt injection, fake user ID, sensitive word obfuscation)
	// based on client type and configuration.
	body = applyCloaking(ctx, e.cfg, auth, body, baseModel, apiKey)

	requestedModel := helps.PayloadRequestedModel(opts, req.Model)
	requestPath := helps.PayloadRequestPath(opts)
	repairMeta := newCompatRepairLogMeta(opts, requestedModel, baseModel, e.Identifier(), "ClaudeExecutor", requestPath, compatKind)
	body = helps.ApplyPayloadConfigWithRequest(e.cfg, baseModel, to.String(), from.String(), "", body, originalTranslated, requestedModel, requestPath, opts.Headers)
	body = scrubDeepSeekThinkingBudgetForCompat(body, baseModel, baseURL, compatKind)
	body = ensureModelMaxTokens(body, baseModel)
	body = applyMiniMaxStreamingThinkingDefaultForCompat(compatKind, body, true)
	body = normalizeClaudeSystemRoleMessages(body)

	// Disable thinking if tool_choice forces tool use (Anthropic API constraint)
	body = disableThinkingIfToolChoiceForced(body)
	body = normalizeClaudeTemperatureForThinking(body)
	body, _, _, err = normalizeThinkingHistoryForModel(body, "claude", baseModel)
	if err != nil {
		return nil, err
	}
	body, err = repairClaudeToolUseHistoryWithCompatLog(ctx, body, repairMeta)
	if err != nil {
		return nil, err
	}
	body, _, err = normalizeClaudeEmptyToolResults(body)
	if err != nil {
		return nil, err
	}
	body = downgradeClaudeToolSearchForCompatKind(compatKind, baseURL, body)

	// Apply cache_control phases sharing a single payload scan (byte-equivalent
	// to the legacy inject → enforce → normalize sequence): auto-inject when
	// missing, enforce the max-4-breakpoint limit, then normalize TTL ordering
	// under prompt-caching-scope-2026-01-05.
	body = applyCacheControlPipeline(body, 4, true)
	body, err = repairMiniMaxClaudeToolAdjacencyForCompatWithLog(ctx, body, repairMeta)
	if err != nil {
		return nil, err
	}

	// Extract betas from body and convert to header
	var extraBetas []string
	extraBetas, body = extractAndRemoveBetas(body)
	bodyForTranslation := body
	bodyForUpstream := body
	bodyForUpstream = downgradeClaudeStructuredOutputForCompat(baseURL, bodyForUpstream)
	oauthToken := isClaudeOAuthToken(apiKey)
	var oauthToolNamesReverseMap map[string]string
	if oauthToken {
		bodyForUpstream, oauthToolNamesReverseMap = prepareClaudeOAuthToolNamesForUpstream(bodyForUpstream, claudeToolPrefix, auth.ToolPrefixDisabled())
	}
	var toolNameSanitization *claudeToolNameSanitization
	bodyForUpstream, toolNameSanitization = sanitizeClaudeToolNamesForUpstream(bodyForUpstream)
	// Enable cch signing by default for OAuth tokens (not just experimental flag).
	if oauthToken || experimentalCCHSigningEnabled(e.cfg, auth) {
		bodyForUpstream = signAnthropicMessagesBody(bodyForUpstream)
	}
	if errValidate := validateClaudeUpstreamPayloadForCompat(compatKind, bodyForUpstream); errValidate != nil {
		return nil, errValidate
	}
	progressStartInputTokens := int64(0)
	if shouldPatchClaudeStartUsageForProgress(compatKind, baseURL) {
		progressStartInputTokens = estimateClaudeProgressInputTokens(baseModel, bodyForUpstream)
	}

	url := fmt.Sprintf("%s/v1/messages?beta=true", baseURL)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyForUpstream))
	if err != nil {
		return nil, err
	}
	applyClaudeHeaders(httpReq, auth, apiKey, true, extraBetas, e.cfg)
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
		Body:      bodyForUpstream,
		Provider:  e.Identifier(),
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})

	httpClient := helps.NewUtlsHTTPClient(e.cfg, auth, 0)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return nil, err
	}
	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		// Decompress error responses — pass the Content-Encoding value (may be empty)
		// and let decodeResponseBody handle both header-declared and magic-byte-detected
		// compression.  This keeps error-path behaviour consistent with the success path.
		errBody, decErr := decodeResponseBody(httpResp.Body, httpResp.Header.Get("Content-Encoding"))
		if decErr != nil {
			helps.RecordAPIResponseError(ctx, e.cfg, decErr)
			msg := fmt.Sprintf("failed to decode error response body: %v", decErr)
			helps.LogWithRequestID(ctx).Warn(msg)
			return nil, statusErr{code: httpResp.StatusCode, msg: msg}
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
		err = statusErr{code: httpResp.StatusCode, msg: string(b)}
		return nil, err
	}
	decodedBody, err := decodeResponseBody(httpResp.Body, httpResp.Header.Get("Content-Encoding"))
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("response body close error: %v", errClose)
		}
		return nil, err
	}
	out := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		defer close(out)
		defer func() {
			if errClose := decodedBody.Close(); errClose != nil {
				log.Errorf("response body close error: %v", errClose)
			}
		}()

		// If from == to (Claude → Claude), directly forward the SSE stream without translation
		if from == to {
			scanner := bufio.NewScanner(decodedBody)
			scanner.Buffer(nil, 52_428_800) // 50MB
			for scanner.Scan() {
				line := scanner.Bytes()
				helps.AppendAPIResponseChunk(ctx, e.cfg, line)
				if detail, ok := helps.ParseClaudeStreamUsage(line); ok {
					reporter.Publish(ctx, detail)
				}
				line = restoreClaudeToolNamesFromStreamLine(line, toolNameSanitization)
				if oauthToken {
					line = restoreClaudeOAuthToolNamesFromStreamLine(line, claudeToolPrefix, auth.ToolPrefixDisabled(), oauthToolNamesReverseMap)
				}
				if progressStartInputTokens > 0 {
					line = patchClaudeMessageStartUsageForProgress(line, progressStartInputTokens)
				}
				// Forward the line as-is to preserve SSE format
				cloned := make([]byte, len(line)+1)
				copy(cloned, line)
				cloned[len(line)] = '\n'
				select {
				case out <- cliproxyexecutor.StreamChunk{Payload: cloned}:
				case <-ctx.Done():
					return
				}
			}
			if errScan := scanner.Err(); errScan != nil {
				helps.RecordAPIResponseError(ctx, e.cfg, errScan)
				reporter.PublishFailure(ctx, errScan)
				select {
				case out <- cliproxyexecutor.StreamChunk{Err: errScan}:
				case <-ctx.Done():
				}
			}
			return
		}

		// For other formats, use translation
		scanner := bufio.NewScanner(decodedBody)
		scanner.Buffer(nil, 52_428_800) // 50MB
		var param any
		for scanner.Scan() {
			line := scanner.Bytes()
			helps.AppendAPIResponseChunk(ctx, e.cfg, line)
			if detail, ok := helps.ParseClaudeStreamUsage(line); ok {
				reporter.Publish(ctx, detail)
			}
			line = restoreClaudeToolNamesFromStreamLine(line, toolNameSanitization)
			if oauthToken {
				line = restoreClaudeOAuthToolNamesFromStreamLine(line, claudeToolPrefix, auth.ToolPrefixDisabled(), oauthToolNamesReverseMap)
			}
			chunks := sdktranslator.TranslateStream(
				ctx,
				to,
				from,
				req.Model,
				opts.OriginalRequest,
				bodyForTranslation,
				bytes.Clone(line),
				&param,
			)
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
		}
	}()
	return &cliproxyexecutor.StreamResult{Headers: httpResp.Header.Clone(), Chunks: out}, nil
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

func (e *ClaudeExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName

	apiKey, baseURL := claudeCreds(auth)
	if baseURL == "" {
		baseURL = "https://api.anthropic.com"
	}

	from := opts.SourceFormat
	to := sdktranslator.FromString("claude")
	// Use streaming translation to preserve function calling, except for claude.
	stream := from != to
	body := sdktranslator.TranslateRequest(from, to, baseModel, req.Payload, stream)
	if repaired, ok := helps.RepairInvalidJSONStringEscapes(body); ok {
		body = repaired
	}
	body, _ = sjson.SetBytes(body, "model", baseModel)

	if !strings.HasPrefix(baseModel, "claude-3-5-haiku") {
		body = checkSystemInstructions(body)
	}
	body = normalizeClaudeSystemRoleMessages(body)

	// Keep count_tokens requests compatible with Anthropic cache-control constraints too.
	body = downgradeClaudeToolSearchForCompat(baseURL, body)
	// count_tokens does NOT inject cache_control; only enforce the limit and
	// normalize TTL ordering (shares a single payload scan, byte-equivalent to
	// the legacy enforce → normalize sequence).
	body = applyCacheControlPipeline(body, 4, false)

	// Extract betas from body and convert to header (for count_tokens too)
	var extraBetas []string
	extraBetas, body = extractAndRemoveBetas(body)
	if isClaudeOAuthToken(apiKey) {
		body, _ = prepareClaudeOAuthToolNamesForUpstream(body, claudeToolPrefix, auth.ToolPrefixDisabled())
	}
	body, _ = sanitizeClaudeToolNamesForUpstream(body)
	if errValidate := validateClaudeUpstreamPayload(baseURL, body); errValidate != nil {
		return cliproxyexecutor.Response{}, errValidate
	}

	url := fmt.Sprintf("%s/v1/messages/count_tokens?beta=true", baseURL)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}
	applyClaudeHeaders(httpReq, auth, apiKey, false, extraBetas, e.cfg)
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
		Provider:  e.Identifier(),
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})

	httpClient := helps.NewUtlsHTTPClient(e.cfg, auth, 0)
	resp, err := httpClient.Do(httpReq)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return cliproxyexecutor.Response{}, err
	}
	helps.RecordAPIResponseMetadata(ctx, e.cfg, resp.StatusCode, resp.Header.Clone())
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Decompress error responses — pass the Content-Encoding value (may be empty)
		// and let decodeResponseBody handle both header-declared and magic-byte-detected
		// compression.  This keeps error-path behaviour consistent with the success path.
		errBody, decErr := decodeResponseBody(resp.Body, resp.Header.Get("Content-Encoding"))
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
	decodedBody, err := decodeResponseBody(resp.Body, resp.Header.Get("Content-Encoding"))
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
	out := sdktranslator.TranslateTokenCount(ctx, to, from, count, data)
	return cliproxyexecutor.Response{Payload: out, Headers: resp.Header.Clone()}, nil
}

func (e *ClaudeExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	log.Debugf("claude executor: refresh called")
	if refreshed, handled, err := helps.RefreshAuthViaHome(ctx, e.cfg, auth); handled {
		return refreshed, err
	}
	if auth == nil {
		return nil, fmt.Errorf("claude executor: auth is nil")
	}
	var refreshToken string
	if auth.Metadata != nil {
		if v, ok := auth.Metadata["refresh_token"].(string); ok && v != "" {
			refreshToken = v
		}
	}
	if refreshToken == "" {
		return auth, nil
	}
	svc := claudeauth.NewClaudeAuthWithProxyURL(e.cfg, auth.ProxyURL)
	td, err := svc.RefreshTokensWithRetry(ctx, refreshToken, 3)
	if err != nil {
		return nil, err
	}
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	auth.Metadata["access_token"] = td.AccessToken
	if td.RefreshToken != "" {
		auth.Metadata["refresh_token"] = td.RefreshToken
	}
	auth.Metadata["email"] = td.Email
	auth.Metadata["expired"] = td.Expire
	auth.Metadata["type"] = "claude"
	now := time.Now().Format(time.RFC3339)
	auth.Metadata["last_refresh"] = now
	return auth, nil
}

// extractAndRemoveBetas extracts the "betas" array from the body and removes it.
// Returns the extracted betas as a string slice and the modified body.
func extractAndRemoveBetas(body []byte) ([]string, []byte) {
	betasResult := gjson.GetBytes(body, "betas")
	if !betasResult.Exists() {
		return nil, body
	}
	var betas []string
	if betasResult.IsArray() {
		for _, item := range betasResult.Array() {
			if s := strings.TrimSpace(item.String()); s != "" {
				betas = append(betas, s)
			}
		}
	} else if s := strings.TrimSpace(betasResult.String()); s != "" {
		betas = append(betas, s)
	}
	body, _ = sjson.DeleteBytes(body, "betas")
	return betas, body
}

// disableThinkingIfToolChoiceForced checks if tool_choice forces tool use and disables thinking.
// Anthropic API does not allow thinking when tool_choice is set to "any" or a specific tool.
// See: https://docs.anthropic.com/en/docs/build-with-claude/extended-thinking#important-considerations
func disableThinkingIfToolChoiceForced(body []byte) []byte {
	toolChoiceType := gjson.GetBytes(body, "tool_choice.type").String()
	// "auto" is allowed with thinking, but "any" or "tool" (specific tool) are not
	if toolChoiceType == "any" || toolChoiceType == "tool" {
		// Remove thinking configuration entirely to avoid API error
		body, _ = sjson.DeleteBytes(body, "thinking")
		// Adaptive thinking may also set output_config.effort; remove it to avoid
		// leaking thinking controls when tool_choice forces tool use.
		body, _ = sjson.DeleteBytes(body, "output_config.effort")
		if oc := gjson.GetBytes(body, "output_config"); oc.Exists() && oc.IsObject() && len(oc.Map()) == 0 {
			body, _ = sjson.DeleteBytes(body, "output_config")
		}
	}
	return body
}

// normalizeClaudeTemperatureForThinking keeps Anthropic message requests valid when
// thinking is enabled. Anthropic rejects temperatures other than 1 when
// thinking.type is enabled/adaptive/auto.
func normalizeClaudeTemperatureForThinking(body []byte) []byte {
	if !gjson.GetBytes(body, "temperature").Exists() {
		return body
	}

	thinkingType := strings.ToLower(strings.TrimSpace(gjson.GetBytes(body, "thinking.type").String()))
	switch thinkingType {
	case "enabled", "adaptive", "auto":
		if temp := gjson.GetBytes(body, "temperature"); temp.Exists() && temp.Type == gjson.Number && temp.Float() == 1 {
			return body
		}
		body, _ = sjson.SetBytes(body, "temperature", 1)
	}
	return body
}

func normalizeClaudeSystemRoleMessages(body []byte) []byte {
	if len(body) == 0 || !gjson.GetBytes(body, "messages").IsArray() {
		return body
	}

	var root map[string]any
	if err := json.Unmarshal(body, &root); err != nil {
		return body
	}
	messages, ok := root["messages"].([]any)
	if !ok || len(messages) == 0 {
		return body
	}

	systemBlocks := claudeSystemBlocksFromValue(root["system"])
	cleanedMessages := make([]any, 0, len(messages))
	changed := false
	for _, rawMessage := range messages {
		message, ok := rawMessage.(map[string]any)
		if !ok {
			cleanedMessages = append(cleanedMessages, rawMessage)
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(compatStringValue(message["role"])), "system") {
			cleanedMessages = append(cleanedMessages, message)
			continue
		}
		systemBlocks = append(systemBlocks, claudeSystemBlocksFromValue(message["content"])...)
		changed = true
	}
	if !changed {
		return body
	}

	root["messages"] = cleanedMessages
	if len(systemBlocks) > 0 {
		root["system"] = systemBlocks
	} else {
		delete(root, "system")
	}
	out, err := json.Marshal(root)
	if err != nil || !gjson.ValidBytes(out) {
		return body
	}
	return out
}

func claudeSystemBlocksFromValue(value any) []any {
	switch typed := value.(type) {
	case string:
		return claudeSystemBlockFromText(typed)
	case []any:
		blocks := make([]any, 0, len(typed))
		for _, item := range typed {
			blocks = append(blocks, claudeSystemBlocksFromValue(item)...)
		}
		return blocks
	case map[string]any:
		text := strings.TrimSpace(compatStringValue(typed["text"]))
		if text == "" || util.IsClaudeCodeAttributionSystemText(text) {
			return nil
		}
		block := make(map[string]any, len(typed)+1)
		for key, val := range typed {
			block[key] = val
		}
		if strings.TrimSpace(compatStringValue(block["type"])) == "" {
			block["type"] = "text"
		}
		block["text"] = text
		return []any{block}
	default:
		return nil
	}
}

func claudeSystemBlockFromText(text string) []any {
	text = strings.TrimSpace(text)
	if text == "" || util.IsClaudeCodeAttributionSystemText(text) {
		return nil
	}
	return []any{map[string]any{
		"type": "text",
		"text": text,
	}}
}

type compositeReadCloser struct {
	io.Reader
	closers []func() error
}

func (c *compositeReadCloser) Close() error {
	var firstErr error
	for i := range c.closers {
		if c.closers[i] == nil {
			continue
		}
		if err := c.closers[i](); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// peekableBody wraps a bufio.Reader around the original ReadCloser so that
// magic bytes can be inspected without consuming them from the stream.
type peekableBody struct {
	*bufio.Reader
	closer io.Closer
}

func (p *peekableBody) Close() error {
	return p.closer.Close()
}

func decodeResponseBody(body io.ReadCloser, contentEncoding string) (io.ReadCloser, error) {
	if body == nil {
		return nil, fmt.Errorf("response body is nil")
	}
	if contentEncoding == "" {
		// No Content-Encoding header.  Attempt best-effort magic-byte detection to
		// handle misbehaving upstreams that compress without setting the header.
		// Only gzip (1f 8b) and zstd (28 b5 2f fd) have reliable magic sequences;
		// br and deflate have none and are left as-is.
		// The bufio wrapper preserves unread bytes so callers always see the full
		// stream regardless of whether decompression was applied.
		pb := &peekableBody{Reader: bufio.NewReader(body), closer: body}
		magic, peekErr := pb.Peek(4)
		if peekErr == nil || (peekErr == io.EOF && len(magic) >= 2) {
			switch {
			case len(magic) >= 2 && magic[0] == 0x1f && magic[1] == 0x8b:
				gzipReader, gzErr := gzip.NewReader(pb)
				if gzErr != nil {
					_ = pb.Close()
					return nil, fmt.Errorf("magic-byte gzip: failed to create reader: %w", gzErr)
				}
				return &compositeReadCloser{
					Reader: gzipReader,
					closers: []func() error{
						gzipReader.Close,
						pb.Close,
					},
				}, nil
			case len(magic) >= 4 && magic[0] == 0x28 && magic[1] == 0xb5 && magic[2] == 0x2f && magic[3] == 0xfd:
				decoder, zdErr := zstd.NewReader(pb)
				if zdErr != nil {
					_ = pb.Close()
					return nil, fmt.Errorf("magic-byte zstd: failed to create reader: %w", zdErr)
				}
				return &compositeReadCloser{
					Reader: decoder,
					closers: []func() error{
						func() error { decoder.Close(); return nil },
						pb.Close,
					},
				}, nil
			}
		}
		return pb, nil
	}
	encodings := strings.Split(contentEncoding, ",")
	for _, raw := range encodings {
		encoding := strings.TrimSpace(strings.ToLower(raw))
		switch encoding {
		case "", "identity":
			continue
		case "gzip":
			gzipReader, err := gzip.NewReader(body)
			if err != nil {
				_ = body.Close()
				return nil, fmt.Errorf("failed to create gzip reader: %w", err)
			}
			return &compositeReadCloser{
				Reader: gzipReader,
				closers: []func() error{
					gzipReader.Close,
					func() error { return body.Close() },
				},
			}, nil
		case "deflate":
			deflateReader := flate.NewReader(body)
			return &compositeReadCloser{
				Reader: deflateReader,
				closers: []func() error{
					deflateReader.Close,
					func() error { return body.Close() },
				},
			}, nil
		case "br":
			return &compositeReadCloser{
				Reader: brotli.NewReader(body),
				closers: []func() error{
					func() error { return body.Close() },
				},
			}, nil
		case "zstd":
			decoder, err := zstd.NewReader(body)
			if err != nil {
				_ = body.Close()
				return nil, fmt.Errorf("failed to create zstd reader: %w", err)
			}
			return &compositeReadCloser{
				Reader: decoder,
				closers: []func() error{
					func() error { decoder.Close(); return nil },
					func() error { return body.Close() },
				},
			}, nil
		default:
			continue
		}
	}
	return body, nil
}

func applyClaudeHeaders(r *http.Request, auth *cliproxyauth.Auth, apiKey string, stream bool, extraBetas []string, cfg *config.Config) {
	hdrDefault := func(cfgVal, fallback string) string {
		if cfgVal != "" {
			return cfgVal
		}
		return fallback
	}

	var hd config.ClaudeHeaderDefaults
	if cfg != nil {
		hd = cfg.ClaudeHeaderDefaults
	}

	useAPIKey := auth != nil && auth.Attributes != nil && strings.TrimSpace(auth.Attributes["api_key"]) != ""
	isAnthropicBase := r.URL != nil && strings.EqualFold(r.URL.Scheme, "https") && strings.EqualFold(r.URL.Host, "api.anthropic.com")
	if isAnthropicBase && useAPIKey {
		r.Header.Del("Authorization")
		r.Header.Set("x-api-key", apiKey)
	} else {
		r.Header.Set("Authorization", "Bearer "+apiKey)
	}
	r.Header.Set("Content-Type", "application/json")

	var ginHeaders http.Header
	if ginCtx, ok := r.Context().Value("gin").(*gin.Context); ok && ginCtx != nil && ginCtx.Request != nil {
		ginHeaders = ginCtx.Request.Header
	}
	stabilizeDeviceProfile := helps.ClaudeDeviceProfileStabilizationEnabled(cfg)
	var deviceProfile helps.ClaudeDeviceProfile
	if stabilizeDeviceProfile {
		deviceProfile = helps.ResolveClaudeDeviceProfile(auth, apiKey, ginHeaders, cfg)
	}

	baseBetas := "claude-code-20250219,oauth-2025-04-20,interleaved-thinking-2025-05-14,context-management-2025-06-27,prompt-caching-scope-2026-01-05,structured-outputs-2025-12-15,fast-mode-2026-02-01,redact-thinking-2026-02-12,token-efficient-tools-2026-03-28"
	if val := strings.TrimSpace(ginHeaders.Get("Anthropic-Beta")); val != "" {
		baseBetas = val
		if !strings.Contains(val, "oauth") {
			baseBetas += ",oauth-2025-04-20"
		}
	}
	if !strings.Contains(baseBetas, "interleaved-thinking") {
		baseBetas += ",interleaved-thinking-2025-05-14"
	}

	// Merge extra betas from request body and request flags.
	if len(extraBetas) > 0 {
		existingSet := make(map[string]bool)
		for _, b := range strings.Split(baseBetas, ",") {
			betaName := strings.TrimSpace(b)
			if betaName != "" {
				existingSet[betaName] = true
			}
		}
		for _, beta := range extraBetas {
			beta = strings.TrimSpace(beta)
			if beta != "" && !existingSet[beta] {
				baseBetas += "," + beta
				existingSet[beta] = true
			}
		}
	}
	if !isAnthropicBase {
		baseBetas = filterClaudeBetasForCompat(baseBetas)
	}
	if strings.TrimSpace(baseBetas) != "" {
		r.Header.Set("Anthropic-Beta", baseBetas)
	} else {
		r.Header.Del("Anthropic-Beta")
	}

	misc.EnsureHeader(r.Header, ginHeaders, "Anthropic-Version", "2023-06-01")
	// Only set browser access header for API key mode; real Claude Code CLI does not send it.
	if useAPIKey {
		misc.EnsureHeader(r.Header, ginHeaders, "Anthropic-Dangerous-Direct-Browser-Access", "true")
	}
	misc.EnsureHeader(r.Header, ginHeaders, "X-App", "cli")
	// Values below match Claude Code 2.1.63 / @anthropic-ai/sdk 0.74.0 (updated 2026-02-28).
	misc.EnsureHeader(r.Header, ginHeaders, "X-Stainless-Retry-Count", "0")
	misc.EnsureHeader(r.Header, ginHeaders, "X-Stainless-Runtime", "node")
	misc.EnsureHeader(r.Header, ginHeaders, "X-Stainless-Lang", "js")
	misc.EnsureHeader(r.Header, ginHeaders, "X-Stainless-Timeout", hdrDefault(hd.Timeout, "600"))
	// Session ID: stable per auth/apiKey, matches Claude Code's X-Claude-Code-Session-Id header.
	misc.EnsureHeader(r.Header, ginHeaders, "X-Claude-Code-Session-Id", helps.CachedSessionID(apiKey))
	// Per-request UUID, matches Claude Code's x-client-request-id for first-party API.
	if isAnthropicBase {
		misc.EnsureHeader(r.Header, ginHeaders, "x-client-request-id", uuid.New().String())
	}
	r.Header.Set("Connection", "keep-alive")
	if stream {
		r.Header.Set("Accept", "text/event-stream")
		// SSE streams must not be compressed: the downstream scanner reads
		// line-delimited text and cannot parse compressed bytes.  Using
		// "identity" tells the upstream to send an uncompressed stream.
		r.Header.Set("Accept-Encoding", "identity")
	} else {
		r.Header.Set("Accept", "application/json")
		r.Header.Set("Accept-Encoding", "gzip, deflate, br, zstd")
	}
	// Legacy mode keeps OS/Arch runtime-derived; stabilized mode pins OS/Arch
	// to the configured baseline while still allowing newer official
	// User-Agent/package/runtime tuples to upgrade the software fingerprint.
	if stabilizeDeviceProfile {
		helps.ApplyClaudeDeviceProfileHeaders(r, deviceProfile)
	} else {
		helps.ApplyClaudeLegacyDeviceHeaders(r, ginHeaders, cfg)
	}
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(r, attrs)
	// Re-enforce Accept-Encoding: identity after ApplyCustomHeadersFromAttrs, which
	// may override it with a user-configured value.  Compressed SSE breaks the line
	// scanner regardless of user preference, so this is non-negotiable for streams.
	if stream {
		r.Header.Set("Accept-Encoding", "identity")
	}
}

func filterClaudeBetasForCompat(raw string) string {
	parts := strings.Split(raw, ",")
	filtered := make([]string, 0, len(parts))
	seen := make(map[string]bool, len(parts))
	for _, part := range parts {
		beta := strings.TrimSpace(part)
		if beta == "" {
			continue
		}
		normalized := strings.ToLower(beta)
		if isNativeAnthropicBetaForCompat(normalized) {
			continue
		}
		if seen[beta] {
			continue
		}
		filtered = append(filtered, beta)
		seen[beta] = true
	}
	return strings.Join(filtered, ",")
}

func isNativeAnthropicBetaForCompat(normalized string) bool {
	if strings.Contains(normalized, "tool-search") || strings.Contains(normalized, "tool_search") {
		return true
	}
	switch normalized {
	case "claude-code-20250219",
		"oauth-2025-04-20",
		"interleaved-thinking-2025-05-14",
		"context-management-2025-06-27",
		"prompt-caching-scope-2026-01-05",
		"structured-outputs-2025-12-15",
		"fast-mode-2026-02-01",
		"redact-thinking-2026-02-12",
		"token-efficient-tools-2026-03-28":
		return true
	default:
		return false
	}
}

func claudeCreds(a *cliproxyauth.Auth) (apiKey, baseURL string) {
	if a == nil {
		return "", ""
	}
	if a.Attributes != nil {
		apiKey = a.Attributes["api_key"]
		baseURL = a.Attributes["base_url"]
	}
	if apiKey == "" && a.Metadata != nil {
		if v, ok := a.Metadata["access_token"].(string); ok {
			apiKey = v
		}
	}
	return
}

func checkSystemInstructions(payload []byte) []byte {
	return checkSystemInstructionsWithSigningMode(payload, false, false, false, "2.1.63", "", "")
}

func applyMiniMaxStreamingThinkingDefaultForCompat(compatKind string, body []byte, stream bool) []byte {
	if compatKind != "minimax" || !stream || len(body) == 0 || !gjson.ValidBytes(body) {
		return body
	}
	if gjson.GetBytes(body, "thinking").Exists() || gjson.GetBytes(body, "output_config.effort").Exists() {
		return body
	}
	switch gjson.GetBytes(body, "tool_choice.type").String() {
	case "any", "tool":
		return body
	}
	out, err := sjson.SetBytes(body, "thinking.type", "disabled")
	if err != nil {
		return body
	}
	return out
}

func isClaudeOAuthToken(apiKey string) bool {
	return strings.Contains(apiKey, "sk-ant-oat")
}

func claudeCompatKind(auth *cliproxyauth.Auth, baseURL string) string {
	if auth != nil {
		for _, key := range []string{"compat_kind", "compat-kind"} {
			if value := config.NormalizeOpenAICompatibilityKind(auth.Attributes[key]); value != "" {
				return value
			}
		}
	}
	return config.InferCompatKindFromBaseURL(baseURL)
}

func shouldPatchClaudeStartUsageForProgress(compatKind, baseURL string) bool {
	if strings.EqualFold(strings.TrimSpace(compatKind), "qianfan") {
		return true
	}
	return strings.EqualFold(config.InferCompatKindFromBaseURL(baseURL), "qianfan")
}

func estimateClaudeProgressInputTokens(model string, body []byte) int64 {
	body = bytes.TrimSpace(body)
	if len(body) == 0 {
		return 0
	}
	text := string(body)
	if enc, err := helps.TokenizerForModel(model); err == nil {
		if count, errCount := enc.Count(text); errCount == nil && count > 0 {
			return int64(count)
		}
	}
	runes := len([]rune(text))
	count := (runes + 3) / 4
	if count < 1 {
		return 1
	}
	return int64(count)
}

func patchClaudeMessageStartUsageForProgress(line []byte, inputTokens int64) []byte {
	if inputTokens <= 0 {
		return line
	}
	payload, ok := sseDataPayload(line)
	if !ok || len(payload) == 0 || !gjson.ValidBytes(payload) {
		return line
	}
	if gjson.GetBytes(payload, "type").String() != "message_start" {
		return line
	}
	if existing := gjson.GetBytes(payload, "message.usage.input_tokens"); existing.Exists() && existing.Int() > 0 {
		return line
	}
	updated, err := sjson.SetBytes(payload, "message.usage.input_tokens", inputTokens)
	if err != nil {
		return line
	}
	return replaceSSEDataPayload(line, updated)
}

func sseDataPayload(line []byte) ([]byte, bool) {
	trimmed := bytes.TrimSpace(line)
	if !bytes.HasPrefix(trimmed, []byte("data:")) {
		return nil, false
	}
	return bytes.TrimSpace(trimmed[len("data:"):]), true
}

func replaceSSEDataPayload(line, payload []byte) []byte {
	leadingLen := len(line) - len(bytes.TrimLeft(line, " \t"))
	out := make([]byte, 0, leadingLen+len("data: ")+len(payload))
	out = append(out, line[:leadingLen]...)
	out = append(out, "data: "...)
	out = append(out, payload...)
	return out
}

func downgradeClaudeStructuredOutputForCompat(baseURL string, body []byte) []byte {
	if isOfficialAnthropicBaseURL(baseURL) || len(body) == 0 || !gjson.ValidBytes(body) {
		return body
	}
	format := gjson.GetBytes(body, "output_config.format")
	if !format.Exists() {
		return body
	}
	if format.IsObject() && len(format.Map()) == 0 {
		return body
	}
	instruction := buildStructuredOutputCompatInstruction(format.Raw)
	if instruction != "" {
		body = appendClaudeSystemText(body, instruction)
	}
	body, _ = sjson.DeleteBytes(body, "output_config.format")
	if oc := gjson.GetBytes(body, "output_config"); oc.Exists() && oc.IsObject() && len(oc.Map()) == 0 {
		body, _ = sjson.DeleteBytes(body, "output_config")
	}
	return body
}

func downgradeClaudeToolSearchForCompat(baseURL string, body []byte) []byte {
	return downgradeClaudeToolSearchForCompatKind("", baseURL, body)
}

func downgradeClaudeToolSearchForCompatKind(compatKind, baseURL string, body []byte) []byte {
	if repaired, ok := helps.RepairInvalidJSONStringEscapes(body); ok {
		body = repaired
	}
	if isOfficialAnthropicBaseURL(baseURL) || len(body) == 0 || !gjson.ValidBytes(body) {
		return body
	}

	if compatKind == "" {
		compatKind = config.InferCompatKindFromBaseURL(baseURL)
	}
	var root map[string]any
	if err := json.Unmarshal(body, &root); err != nil {
		return body
	}
	modelID := strings.TrimSpace(compatStringValue(root["model"]))

	changed := false
	removedToolNames := make(map[string]bool)
	if tools, ok := root["tools"].([]any); ok {
		cleanedTools := make([]any, 0, len(tools))
		for _, rawTool := range tools {
			tool, okTool := rawTool.(map[string]any)
			if !okTool {
				cleanedTools = append(cleanedTools, rawTool)
				continue
			}
			toolType := strings.TrimSpace(compatStringValue(tool["type"]))
			toolName := strings.TrimSpace(compatStringValue(tool["name"]))
			if isClaudeToolSearchTool(toolType, toolName) || isUnsupportedClaudeServerToolForCompat(compatKind, toolType, tool) {
				if toolName != "" {
					removedToolNames[toolName] = true
				}
				changed = true
				continue
			}
			if _, hasDeferLoading := tool["defer_loading"]; hasDeferLoading {
				delete(tool, "defer_loading")
				changed = true
			}
			if sanitizeClaudeToolInputSchemaForCompat(compatKind, tool) {
				changed = true
			}
			cleanedTools = append(cleanedTools, tool)
		}
		if len(cleanedTools) == 0 {
			delete(root, "tools")
		} else {
			root["tools"] = cleanedTools
		}
	}

	if len(removedToolNames) > 0 {
		if toolChoice, ok := root["tool_choice"].(map[string]any); ok {
			choiceName := strings.TrimSpace(compatStringValue(toolChoice["name"]))
			if strings.TrimSpace(compatStringValue(toolChoice["type"])) == "tool" && removedToolNames[choiceName] {
				delete(root, "tool_choice")
				changed = true
			}
		}
	}

	if messages, ok := root["messages"].([]any); ok {
		cleanedMessages := make([]any, 0, len(messages))
		for _, rawMessage := range messages {
			message, okMessage := rawMessage.(map[string]any)
			if !okMessage {
				cleanedMessages = append(cleanedMessages, rawMessage)
				continue
			}
			content, okContent := message["content"].([]any)
			if !okContent {
				cleanedMessages = append(cleanedMessages, message)
				continue
			}
			cleanedContent, contentChanged := downgradeClaudeToolSearchContentForCompat(compatKind, modelID, content)
			if contentChanged {
				changed = true
				if len(cleanedContent) == 0 {
					continue
				}
				message["content"] = cleanedContent
			}
			cleanedMessages = append(cleanedMessages, message)
		}
		root["messages"] = cleanedMessages
	}

	if !changed {
		return body
	}
	out, err := json.Marshal(root)
	if err != nil || !gjson.ValidBytes(out) {
		return body
	}
	log.WithField("compat_kind", compatKind).Debug("downgraded Claude tool search payload for upstream compatibility")
	return out
}

func sanitizeClaudeToolInputSchemaForCompat(compatKind string, tool map[string]any) bool {
	if !requiresClaudeToolSchemaCleanupForCompat(compatKind) {
		return false
	}
	inputSchema, ok := tool["input_schema"]
	if !ok {
		return false
	}
	raw, err := json.Marshal(inputSchema)
	if err != nil || !gjson.ValidBytes(raw) {
		return false
	}
	cleanedRaw := util.CleanJSONSchemaForStrictUpstream(string(raw))
	var cleaned any
	if err = json.Unmarshal([]byte(cleanedRaw), &cleaned); err != nil {
		return false
	}
	if jsonValuesEqual(inputSchema, cleaned) {
		return false
	}
	tool["input_schema"] = cleaned
	return true
}

func requiresClaudeToolSchemaCleanupForCompat(compatKind string) bool {
	switch compatKind {
	case "deepseek":
		return true
	default:
		return false
	}
}

func isClaudeToolSearchTool(toolType string, toolName string) bool {
	return strings.HasPrefix(toolType, "tool_search_tool_") || strings.HasPrefix(toolName, "tool_search_tool_")
}

func isUnsupportedClaudeServerToolForCompat(compatKind string, toolType string, tool map[string]any) bool {
	if toolType == "" {
		return false
	}
	if !requiresClaudeContentBlockDowngradeForCompat(compatKind) {
		return false
	}
	_, hasInputSchema := tool["input_schema"]
	return !hasInputSchema
}

func downgradeClaudeToolSearchContent(content []any) ([]any, bool) {
	return downgradeClaudeToolSearchContentForCompat("", "", content)
}

func downgradeClaudeToolSearchContentForCompat(compatKind, modelID string, content []any) ([]any, bool) {
	cleaned := make([]any, 0, len(content))
	changed := false
	for _, rawPart := range content {
		part, okPart := rawPart.(map[string]any)
		if !okPart {
			cleaned = append(cleaned, rawPart)
			continue
		}
		partType := strings.TrimSpace(compatStringValue(part["type"]))
		switch {
		case isUnsupportedClaudeContentPartForCompat(compatKind, modelID, partType):
			changed = true
			if text := claudeUnsupportedContentText(part); text != "" {
				cleaned = append(cleaned, map[string]any{"type": "text", "text": text})
			}
			continue
		case partType == "server_tool_use":
			changed = true
			continue
		case partType == "tool_search_tool_result":
			changed = true
			if text := claudeToolSearchResultText(part); text != "" {
				cleaned = append(cleaned, map[string]any{"type": "text", "text": text})
			}
			continue
		case isClaudeServerToolResultPart(partType):
			changed = true
			continue
		case partType == "tool_reference":
			changed = true
			if text := claudeToolReferenceText(part); text != "" {
				cleaned = append(cleaned, map[string]any{"type": "text", "text": text})
			}
			continue
		case partType == "tool_result":
			updated := part
			updatedChanged := false
			if next, nextChanged := downgradeClaudeToolResultContentForCompat(compatKind, modelID, updated); nextChanged {
				updated = next
				updatedChanged = true
			}
			if next, nextChanged := downgradeClaudeToolResultReferences(updated); nextChanged {
				updated = next
				updatedChanged = true
			}
			if updatedChanged {
				cleaned = append(cleaned, updated)
				changed = true
				continue
			}
		}
		cleaned = append(cleaned, part)
	}
	return cleaned, changed
}

func isUnsupportedClaudeContentPartForCompat(compatKind, modelID, partType string) bool {
	if !requiresClaudeContentBlockDowngradeForCompat(compatKind) {
		return false
	}
	if supportsMiniMaxM3ClaudeMultimodalPart(compatKind, modelID, partType) {
		return false
	}
	if compatKind == "doubao" {
		switch partType {
		case "image":
			return false
		case "image_url", "video", "video_url", "document", "search_result", "redacted_thinking", "server_tool_use",
			"web_search_tool_result", "code_execution_tool_result", "mcp_tool_use", "mcp_tool_result", "container_upload":
			return true
		default:
			return false
		}
	}
	switch partType {
	case "image", "image_url", "video", "video_url", "document", "search_result", "redacted_thinking", "server_tool_use",
		"web_search_tool_result", "code_execution_tool_result", "mcp_tool_use", "mcp_tool_result", "container_upload":
		return true
	default:
		return false
	}
}

func supportsMiniMaxM3ClaudeMultimodalPart(compatKind, modelID, partType string) bool {
	if compatKind != "minimax" || !isMiniMaxM3SeriesModel(modelID) {
		return false
	}
	switch partType {
	case "image", "video":
		return true
	default:
		return false
	}
}

func isMiniMaxM3SeriesModel(modelID string) bool {
	modelID = strings.ToLower(strings.TrimSpace(thinking.ParseSuffix(modelID).ModelName))
	return modelID == "minimax-m3" || strings.HasPrefix(modelID, "minimax-m3-")
}

func requiresClaudeContentBlockDowngradeForCompat(compatKind string) bool {
	switch compatKind {
	case "deepseek", "doubao", "minimax", "qianfan", "step", "xiaomi":
		return true
	default:
		return false
	}
}

func isClaudeServerToolResultPart(partType string) bool {
	return partType != "" && partType != "tool_result" && strings.HasSuffix(partType, "_tool_result")
}

func downgradeClaudeToolResultContentForCompat(compatKind, modelID string, part map[string]any) (map[string]any, bool) {
	if !requiresClaudeContentBlockDowngradeForCompat(compatKind) {
		return part, false
	}
	content, ok := part["content"]
	if !ok {
		return part, false
	}

	switch typed := content.(type) {
	case []any:
		cleaned := make([]any, 0, len(typed))
		changed := false
		for _, rawNested := range typed {
			nested, okNested := rawNested.(map[string]any)
			if !okNested {
				cleaned = append(cleaned, rawNested)
				continue
			}
			nestedType := strings.TrimSpace(compatStringValue(nested["type"]))
			if !isUnsupportedClaudeContentPartForCompat(compatKind, modelID, nestedType) {
				cleaned = append(cleaned, rawNested)
				continue
			}
			changed = true
			if text := claudeUnsupportedContentText(nested); text != "" {
				cleaned = append(cleaned, map[string]any{"type": "text", "text": text})
			}
		}
		if !changed {
			return part, false
		}
		part["content"] = cleaned
		return part, true
	case map[string]any:
		nestedType := strings.TrimSpace(compatStringValue(typed["type"]))
		if !isUnsupportedClaudeContentPartForCompat(compatKind, modelID, nestedType) {
			return part, false
		}
		if text := claudeUnsupportedContentText(typed); text != "" {
			part["content"] = []any{map[string]any{"type": "text", "text": text}}
		} else {
			part["content"] = []any{}
		}
		return part, true
	default:
		return part, false
	}
}

func claudeUnsupportedContentText(part map[string]any) string {
	if part == nil {
		return ""
	}
	fragments := make([]string, 0, 2)
	collectClaudeTextFragments(part["text"], &fragments)
	collectClaudeTextFragments(part["content"], &fragments)
	collectClaudeTextFragments(part["error_message"], &fragments)
	return strings.Join(dedupeNonEmptyStrings(fragments), "\n\n")
}

func collectClaudeTextFragments(value any, fragments *[]string) {
	switch typed := value.(type) {
	case string:
		if text := strings.TrimSpace(typed); text != "" {
			*fragments = append(*fragments, text)
		}
	case []any:
		for _, item := range typed {
			collectClaudeTextFragments(item, fragments)
		}
	case map[string]any:
		if strings.TrimSpace(compatStringValue(typed["type"])) == "text" {
			collectClaudeTextFragments(typed["text"], fragments)
			return
		}
		collectClaudeTextFragments(typed["text"], fragments)
		collectClaudeTextFragments(typed["content"], fragments)
		collectClaudeTextFragments(typed["error_message"], fragments)
	}
}

func dedupeNonEmptyStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func downgradeClaudeToolResultReferences(part map[string]any) (map[string]any, bool) {
	content, ok := part["content"]
	if !ok {
		return part, false
	}
	if nested, okNested := content.([]any); okNested {
		cleaned := make([]any, 0, len(nested))
		changed := false
		for _, rawNested := range nested {
			nestedPart, okPart := rawNested.(map[string]any)
			if !okPart || strings.TrimSpace(compatStringValue(nestedPart["type"])) != "tool_reference" {
				cleaned = append(cleaned, rawNested)
				continue
			}
			changed = true
			if text := claudeToolReferenceText(nestedPart); text != "" {
				cleaned = append(cleaned, map[string]any{"type": "text", "text": text})
			}
		}
		if !changed {
			return part, false
		}
		part["content"] = cleaned
		return part, true
	}
	if nestedPart, okNested := content.(map[string]any); okNested && strings.TrimSpace(compatStringValue(nestedPart["type"])) == "tool_reference" {
		if text := claudeToolReferenceText(nestedPart); text != "" {
			part["content"] = []any{map[string]any{"type": "text", "text": text}}
		} else {
			part["content"] = []any{}
		}
		return part, true
	}
	return part, false
}

func claudeToolSearchResultText(part map[string]any) string {
	names := make([]string, 0)
	collectToolReferenceNames(part["content"], &names)
	if len(names) == 0 {
		return ""
	}
	return "Tool search discovered: " + strings.Join(names, ", ")
}

func claudeToolReferenceText(part map[string]any) string {
	name := strings.TrimSpace(compatStringValue(part["tool_name"]))
	if name == "" {
		return ""
	}
	return "Tool reference: " + name
}

func collectToolReferenceNames(value any, names *[]string) {
	switch v := value.(type) {
	case map[string]any:
		if strings.TrimSpace(compatStringValue(v["type"])) == "tool_reference" {
			if name := strings.TrimSpace(compatStringValue(v["tool_name"])); name != "" {
				*names = append(*names, name)
			}
		}
		for _, child := range v {
			collectToolReferenceNames(child, names)
		}
	case []any:
		for _, child := range v {
			collectToolReferenceNames(child, names)
		}
	}
}

func isOfficialAnthropicBaseURL(rawBaseURL string) bool {
	parsed, err := url.Parse(strings.TrimSpace(rawBaseURL))
	if err != nil {
		return false
	}
	return strings.EqualFold(parsed.Hostname(), "api.anthropic.com")
}

func buildStructuredOutputCompatInstruction(formatRaw string) string {
	formatRaw = strings.TrimSpace(formatRaw)
	if formatRaw == "" {
		return ""
	}
	return "Structured output compatibility mode: the original request included Anthropic output_config.format, but this upstream does not support that field natively. Return only a valid JSON value that conforms to the requested format/schema below. Do not include Markdown fences, explanations, or any text outside the JSON.\n\nRequested output_config.format:\n" + formatRaw
}

func appendClaudeSystemText(body []byte, text string) []byte {
	text = strings.TrimSpace(text)
	if text == "" || len(body) == 0 || !gjson.ValidBytes(body) {
		return body
	}
	system := gjson.GetBytes(body, "system")
	if !system.Exists() {
		if next, err := sjson.SetBytes(body, "system", text); err == nil {
			return next
		}
		return body
	}
	if system.IsArray() {
		block := fmt.Sprintf(`{"type":"text","text":%s}`, strconv.Quote(text))
		if next, err := sjson.SetRawBytes(body, fmt.Sprintf("system.%d", len(system.Array())), []byte(block)); err == nil {
			return next
		}
		return body
	}
	if system.Type == gjson.String {
		combined := strings.TrimSpace(system.String())
		if combined != "" {
			combined += "\n\n"
		}
		combined += text
		if next, err := sjson.SetBytes(body, "system", combined); err == nil {
			return next
		}
		return body
	}
	combined := fmt.Sprintf("Existing system value: %s\n\n%s", strings.TrimSpace(system.Raw), text)
	if next, err := sjson.SetBytes(body, "system", combined); err == nil {
		return next
	}
	return body
}

func validateClaudeUpstreamPayload(baseURL string, body []byte) error {
	return validateClaudeUpstreamPayloadForCompat(config.InferCompatKindFromBaseURL(baseURL), body)
}

func validateClaudeUpstreamPayloadForCompat(compatKind string, body []byte) error {
	if compatKind != "minimax" {
		return nil
	}
	if err := validateMiniMaxStructuredOutputCompatibility(body); err != nil {
		return err
	}
	if err := validateMiniMaxServerToolCompatibility(body); err != nil {
		return err
	}
	return validateMiniMaxToolResultAdjacency(body)
}

func normalizeClaudeEmptyToolResults(body []byte) ([]byte, int, error) {
	if len(body) == 0 || !helps.HasClaudeToolResultMarker(body) || !gjson.ValidBytes(body) {
		return body, 0, nil
	}
	messages := gjson.GetBytes(body, "messages")
	if !messages.Exists() || !messages.IsArray() {
		return body, 0, nil
	}

	placeholder := []byte(`[{"type":"text","text":" "}]`)
	out := body
	repairs := 0
	for msgIdx, msg := range messages.Array() {
		content := msg.Get("content")
		if !content.Exists() || !content.IsArray() {
			continue
		}
		for partIdx, part := range content.Array() {
			if strings.TrimSpace(part.Get("type").String()) != "tool_result" {
				continue
			}
			toolContent := part.Get("content")
			needsRepair := !toolContent.Exists() ||
				toolContent.Type == gjson.Null ||
				(toolContent.Type == gjson.String && toolContent.String() == "") ||
				(toolContent.IsArray() && len(toolContent.Array()) == 0)
			if !needsRepair {
				continue
			}
			var err error
			out, err = sjson.SetRawBytes(out, fmt.Sprintf("messages.%d.content.%d.content", msgIdx, partIdx), placeholder)
			if err != nil {
				return body, repairs, fmt.Errorf("failed to normalize empty Claude tool_result content: %w", err)
			}
			repairs++
		}
	}
	return out, repairs, nil
}

func repairMiniMaxClaudeToolAdjacency(baseURL string, body []byte) ([]byte, error) {
	return repairMiniMaxClaudeToolAdjacencyForCompat(config.InferCompatKindFromBaseURL(baseURL), body)
}

type compatRepairLogMeta struct {
	requestedModel string
	upstreamModel  string
	provider       string
	executor       string
	requestPath    string
	compatKind     string
	messageCount   int
	toolCount      int
}

func newCompatRepairLogMeta(opts cliproxyexecutor.Options, requestedModel, upstreamModel, provider, executor, requestPath, compatKind string) compatRepairLogMeta {
	return compatRepairLogMeta{
		requestedModel: requestedModel,
		upstreamModel:  upstreamModel,
		provider:       provider,
		executor:       executor,
		requestPath:    requestPath,
		compatKind:     compatKind,
		messageCount:   executorIntMetadataValue(opts.Metadata[cliproxyexecutor.MessageCountMetadataKey]),
		toolCount:      executorIntMetadataValue(opts.Metadata[cliproxyexecutor.ToolCountMetadataKey]),
	}
}

func repairMiniMaxClaudeToolAdjacencyForCompatWithLog(ctx context.Context, body []byte, meta compatRepairLogMeta) ([]byte, error) {
	if !requiresClaudeToolAdjacencyRepair(meta.compatKind) {
		return body, nil
	}
	started := time.Now()
	beforeBytes := len(body)
	repaired, count, err := repairMiniMaxToolResultAdjacency(body)
	if err != nil {
		return body, err
	}
	if count > 0 {
		compatRepairLogEntry(ctx, meta, "claude_tool_result_adjacency", count, beforeBytes, len(repaired), time.Since(started), nil).
			Warn("compat repair applied")
	}
	return repaired, nil
}

func repairClaudeToolUseHistoryWithCompatLog(ctx context.Context, body []byte, meta compatRepairLogMeta) ([]byte, error) {
	started := time.Now()
	beforeBytes := len(body)
	repaired, stats, err := repairClaudeToolUseHistoryWithStats(body)
	if err != nil {
		return body, err
	}
	if stats.changed() {
		extra := log.Fields{
			"merged_tool_result_messages": stats.mergedToolResultMessages,
			"deduped_tool_results":        stats.dedupedToolResults,
			"reordered_tool_results":      stats.reorderedToolResults,
			"removed_tool_uses":           stats.removedToolUses,
			"removed_tool_results":        stats.removedToolResults,
		}
		compatRepairLogEntry(ctx, meta, "claude_tool_use_history", compatRepairStatsTotal(stats), beforeBytes, len(repaired), time.Since(started), extra).
			Warn("compat repair applied")
	}
	return repaired, nil
}

func compatRepairStatsTotal(stats claudeToolHistoryRepairStats) int {
	return stats.mergedToolResultMessages +
		stats.dedupedToolResults +
		stats.reorderedToolResults +
		stats.removedToolUses +
		stats.removedToolResults
}

func compatRepairLogEntry(ctx context.Context, meta compatRepairLogMeta, repairType string, repairsCount, beforeBytes, afterBytes int, duration time.Duration, extra log.Fields) *log.Entry {
	fields := log.Fields{
		"event":                "compat_repair",
		"requested_model":      meta.requestedModel,
		"upstream_model":       meta.upstreamModel,
		"provider":             meta.provider,
		"executor":             meta.executor,
		"request_path":         meta.requestPath,
		"compat_kind":          meta.compatKind,
		"repair_type":          repairType,
		"repairs_count":        repairsCount,
		"message_count":        meta.messageCount,
		"tool_count":           meta.toolCount,
		"payload_bytes_before": beforeBytes,
		"payload_bytes_after":  afterBytes,
		"repair_duration_ms":   duration.Milliseconds(),
	}
	for key, value := range extra {
		fields[key] = value
	}
	return helps.LogWithRequestID(ctx).WithFields(fields)
}

func executorIntMetadataValue(raw any) int {
	switch value := raw.(type) {
	case int:
		if value > 0 {
			return value
		}
	case int32:
		if value > 0 {
			return int(value)
		}
	case int64:
		if value > 0 {
			return int(value)
		}
	case float32:
		if value > 0 {
			return int(value)
		}
	case float64:
		if value > 0 {
			return int(value)
		}
	case string:
		parsed, errParse := strconv.Atoi(strings.TrimSpace(value))
		if errParse == nil && parsed > 0 {
			return parsed
		}
	case []byte:
		parsed, errParse := strconv.Atoi(strings.TrimSpace(string(value)))
		if errParse == nil && parsed > 0 {
			return parsed
		}
	}
	return 0
}

func repairMiniMaxClaudeToolAdjacencyForCompat(compatKind string, body []byte) ([]byte, error) {
	if !requiresClaudeToolAdjacencyRepair(compatKind) {
		return body, nil
	}
	repaired, count, err := repairMiniMaxToolResultAdjacency(body)
	if err != nil {
		return body, err
	}
	if count > 0 {
		log.WithFields(log.Fields{
			"compat_kind": compatKind,
			"repairs":     count,
		}).Warn("repaired Claude tool_result adjacency")
	}
	return repaired, nil
}

func requiresClaudeToolAdjacencyRepair(compatKind string) bool {
	switch strings.ToLower(strings.TrimSpace(compatKind)) {
	case "minimax", "deepseek", "doubao", "qianfan", "step":
		return true
	default:
		return false
	}
}

func validateMiniMaxStructuredOutputCompatibility(body []byte) error {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return nil
	}
	format := gjson.GetBytes(body, "output_config.format")
	if !format.Exists() {
		return nil
	}
	if format.IsObject() && len(format.Map()) == 0 {
		return nil
	}
	return statusErr{
		code: http.StatusBadRequest,
		msg:  "request_feature_unsupported: minimax anthropic compatibility does not support output_config.format",
	}
}

func validateMiniMaxServerToolCompatibility(body []byte) error {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return nil
	}
	tools := gjson.GetBytes(body, "tools")
	if !tools.IsArray() {
		return nil
	}
	var unsupportedType string
	tools.ForEach(func(_, tool gjson.Result) bool {
		if !tool.IsObject() {
			return true
		}
		toolType := strings.TrimSpace(tool.Get("type").String())
		if toolType == "" {
			return true
		}
		if tool.Get("input_schema").Exists() {
			return true
		}
		unsupportedType = toolType
		return false
	})
	if unsupportedType == "" {
		return nil
	}
	return statusErr{
		code: http.StatusBadRequest,
		msg:  fmt.Sprintf("request_feature_unsupported: minimax anthropic compatibility does not support server tool type %q", unsupportedType),
	}
}

func repairMiniMaxToolResultAdjacency(body []byte) ([]byte, int, error) {
	if len(body) == 0 || !helps.HasClaudeToolUseOrResultMarkers(body) || !gjson.ValidBytes(body) {
		return body, 0, nil
	}
	messages := gjson.GetBytes(body, "messages")
	if !messages.Exists() || !messages.IsArray() {
		return body, 0, nil
	}

	outMessages := []byte(`[]`)
	pending := map[string]bool{}
	changed := false
	repairs := 0

	for _, msg := range messages.Array() {
		role := strings.TrimSpace(msg.Get("role").String())
		msgRaw := []byte(msg.Raw)
		if role == "assistant" {
			var moved bool
			var err error
			msgRaw, moved, err = moveClaudeToolUseBlocksToEnd(msg)
			if err != nil {
				return body, 0, err
			}
			if moved {
				changed = true
				repairs++
			}
			pending = claudeToolUseIDsInMessage(gjson.ParseBytes(msgRaw))
			outMessages, _ = sjson.SetRawBytes(outMessages, "-1", msgRaw)
			continue
		}

		if role != "user" || len(pending) == 0 {
			if role != "user" {
				pending = map[string]bool{}
			}
			outMessages, _ = sjson.SetRawBytes(outMessages, "-1", msgRaw)
			continue
		}

		toolResultParts, otherParts := splitPendingClaudeToolResultParts(msg, pending)
		if len(toolResultParts) == 0 || len(otherParts) == 0 {
			outMessages, _ = sjson.SetRawBytes(outMessages, "-1", msgRaw)
			pending = map[string]bool{}
			continue
		}

		toolResultMsg, err := setClaudeMessageContent(msg, toolResultParts)
		if err != nil {
			return body, 0, err
		}
		otherMsg, err := setClaudeMessageContent(msg, otherParts)
		if err != nil {
			return body, 0, err
		}
		outMessages, _ = sjson.SetRawBytes(outMessages, "-1", toolResultMsg)
		outMessages, _ = sjson.SetRawBytes(outMessages, "-1", otherMsg)
		pending = map[string]bool{}
		changed = true
		repairs++
	}

	if !changed {
		return body, 0, nil
	}

	out, err := sjson.SetRawBytes(body, "messages", outMessages)
	if err != nil {
		return body, 0, fmt.Errorf("failed to update MiniMax Claude tool_result adjacency: %w", err)
	}
	return out, repairs, nil
}

func moveClaudeToolUseBlocksToEnd(msg gjson.Result) ([]byte, bool, error) {
	content := msg.Get("content")
	if !content.IsArray() {
		return []byte(msg.Raw), false, nil
	}

	regularParts := make([]gjson.Result, 0)
	toolUseParts := make([]gjson.Result, 0)
	seenToolUse := false
	moved := false
	for _, part := range content.Array() {
		if part.Get("type").String() == "tool_use" {
			seenToolUse = true
			toolUseParts = append(toolUseParts, part)
			continue
		}
		if seenToolUse {
			moved = true
		}
		regularParts = append(regularParts, part)
	}
	if !moved || len(toolUseParts) == 0 {
		return []byte(msg.Raw), false, nil
	}

	newContent := []byte(`[]`)
	for _, part := range regularParts {
		newContent, _ = sjson.SetRawBytes(newContent, "-1", []byte(part.Raw))
	}
	for _, part := range toolUseParts {
		newContent, _ = sjson.SetRawBytes(newContent, "-1", []byte(part.Raw))
	}
	msgOut, err := sjson.SetRawBytes([]byte(msg.Raw), "content", newContent)
	if err != nil {
		return nil, false, fmt.Errorf("failed to move MiniMax Claude tool_use blocks: %w", err)
	}
	return msgOut, true, nil
}

func splitPendingClaudeToolResultParts(msg gjson.Result, pending map[string]bool) ([]gjson.Result, []gjson.Result) {
	content := msg.Get("content")
	if !content.IsArray() {
		return nil, nil
	}

	toolResultParts := make([]gjson.Result, 0)
	otherParts := make([]gjson.Result, 0)
	for _, part := range content.Array() {
		if part.Get("type").String() == "tool_result" {
			toolUseID := strings.TrimSpace(part.Get("tool_use_id").String())
			if toolUseID != "" && pending[toolUseID] {
				toolResultParts = append(toolResultParts, part)
				delete(pending, toolUseID)
				continue
			}
		}
		otherParts = append(otherParts, part)
	}
	return toolResultParts, otherParts
}

func setClaudeMessageContent(msg gjson.Result, parts []gjson.Result) ([]byte, error) {
	content := []byte(`[]`)
	for _, part := range parts {
		content, _ = sjson.SetRawBytes(content, "-1", []byte(part.Raw))
	}
	out, err := sjson.SetRawBytes([]byte(msg.Raw), "content", content)
	if err != nil {
		return nil, fmt.Errorf("failed to update Claude message content: %w", err)
	}
	return out, nil
}

func validateMiniMaxToolResultAdjacency(body []byte) error {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return nil
	}
	messages := gjson.GetBytes(body, "messages")
	if !messages.Exists() || !messages.IsArray() {
		return nil
	}

	pending := make([]string, 0)
	removePending := func(id string) bool {
		for idx := range pending {
			if pending[idx] != id {
				continue
			}
			pending = append(pending[:idx], pending[idx+1:]...)
			return true
		}
		return false
	}

	for msgIdx, msg := range messages.Array() {
		role := strings.TrimSpace(msg.Get("role").String())
		switch role {
		case "assistant":
			if len(pending) > 0 {
				return statusErr{code: http.StatusBadRequest, msg: fmt.Sprintf("minimax invalid tool_result sequence: pending tool results must be completed before assistant message %d", msgIdx)}
			}
			content := msg.Get("content")
			if !content.Exists() || !content.IsArray() {
				continue
			}
			for _, part := range content.Array() {
				if strings.TrimSpace(part.Get("type").String()) != "tool_use" {
					continue
				}
				id := strings.TrimSpace(part.Get("id").String())
				if id != "" {
					pending = append(pending, id)
				}
			}
		case "user":
			if len(pending) == 0 {
				continue
			}
			content := msg.Get("content")
			if !content.Exists() || !content.IsArray() {
				return statusErr{code: http.StatusBadRequest, msg: fmt.Sprintf("minimax invalid tool_result sequence: pending tool results must be completed before non-tool user message %d", msgIdx)}
			}
			for partIdx, part := range content.Array() {
				if strings.TrimSpace(part.Get("type").String()) != "tool_result" {
					if len(pending) > 0 {
						return statusErr{code: http.StatusBadRequest, msg: fmt.Sprintf("minimax invalid tool_result sequence: tool_result must immediately follow tool_use before user content at message %d part %d", msgIdx, partIdx)}
					}
					continue
				}
				toolUseID := strings.TrimSpace(part.Get("tool_use_id").String())
				if toolUseID == "" {
					return statusErr{code: http.StatusBadRequest, msg: fmt.Sprintf("minimax invalid tool_result sequence: missing tool_use_id at message %d part %d", msgIdx, partIdx)}
				}
				if !removePending(toolUseID) {
					return statusErr{code: http.StatusBadRequest, msg: fmt.Sprintf("minimax invalid tool_result sequence: unexpected tool_use_id %q at message %d part %d", toolUseID, msgIdx, partIdx)}
				}
			}
		default:
			if len(pending) > 0 {
				return statusErr{code: http.StatusBadRequest, msg: fmt.Sprintf("minimax invalid tool_result sequence: pending tool results must be completed before role %q at message %d", role, msgIdx)}
			}
		}
	}

	if len(pending) > 0 {
		return statusErr{code: http.StatusBadRequest, msg: fmt.Sprintf("minimax invalid tool_result sequence: %d tool result(s) missing for preceding tool_use", len(pending))}
	}
	return nil
}

// prepareClaudeOAuthToolNamesForUpstream applies the Claude OAuth tool-name
// transforms in the same order across request paths. Remap runs before prefixing
// so any future non-empty prefix still composes correctly with the per-request
// reverse map.
func prepareClaudeOAuthToolNamesForUpstream(body []byte, prefix string, prefixDisabled bool) ([]byte, map[string]string) {
	body, reverseMap := remapOAuthToolNames(body)
	if !prefixDisabled {
		body = applyClaudeToolPrefix(body, prefix)
	}
	return body, reverseMap
}

// restoreClaudeOAuthToolNamesFromResponse undoes the Claude OAuth tool-name
// transforms for non-stream responses in reverse order.
func restoreClaudeOAuthToolNamesFromResponse(body []byte, prefix string, prefixDisabled bool, reverseMap map[string]string) []byte {
	if !prefixDisabled {
		body = stripClaudeToolPrefixFromResponse(body, prefix)
	}
	return reverseRemapOAuthToolNames(body, reverseMap)
}

// restoreClaudeOAuthToolNamesFromStreamLine undoes the Claude OAuth tool-name
// transforms for SSE lines in reverse order.
func restoreClaudeOAuthToolNamesFromStreamLine(line []byte, prefix string, prefixDisabled bool, reverseMap map[string]string) []byte {
	if !prefixDisabled {
		line = stripClaudeToolPrefixFromStreamLine(line, prefix)
	}
	return reverseRemapOAuthToolNamesFromStreamLine(line, reverseMap)
}

// remapOAuthToolNames renames third-party tool names to Claude Code equivalents
// and removes tools without an official counterpart. This prevents Anthropic from
// fingerprinting the request as a third-party client via tool naming patterns.
//
// It operates on: tools[].name, tool_choice.name, and all tool_use/tool_reference
// references in messages. Removed tools' corresponding tool_result blocks are preserved
// (they just become orphaned, which is safe for Claude).
//
// The returned map is keyed on the upstream (TitleCase) name and maps to the
// client-supplied original name. Callers MUST pass this map to the reverse
// functions so only names the client actually caused us to rewrite are restored
// on the response. A global reverse map (the previous implementation) incorrectly
// rewrote names the client originally sent in TitleCase (e.g. Amp CLI's `Bash`)
// when any OTHER tool in the same request triggered a forward rename (e.g.
// Amp's `glob`→`Glob`), because the global reverse map contained `Bash`→`bash`
// regardless of what the client originally sent.
func remapOAuthToolNames(body []byte) ([]byte, map[string]string) {
	reverseMap := make(map[string]string, len(oauthToolRenameMap))
	recordRename := func(original, renamed string) {
		// Preserve the first-seen original name if the same upstream name is
		// produced from multiple call sites; they all map back identically.
		if _, exists := reverseMap[renamed]; !exists {
			reverseMap[renamed] = original
		}
	}

	// 1. Rewrite tools array in a single pass (if present).
	// IMPORTANT: do not mutate names first and then rebuild from an older gjson
	// snapshot. gjson results are snapshots of the original bytes; rebuilding from a
	// stale snapshot will preserve removals but overwrite renamed names back to their
	// original lowercase values.
	tools := gjson.GetBytes(body, "tools")
	if tools.Exists() && tools.IsArray() {

		var toolsJSON strings.Builder
		toolsJSON.WriteByte('[')
		toolCount := 0
		tools.ForEach(func(_, tool gjson.Result) bool {
			// Keep Anthropic built-in tools (web_search, code_execution, etc.) unchanged.
			if tool.Get("type").Exists() && tool.Get("type").String() != "" {
				if toolCount > 0 {
					toolsJSON.WriteByte(',')
				}
				toolsJSON.WriteString(tool.Raw)
				toolCount++
				return true
			}

			name := tool.Get("name").String()
			if oauthToolsToRemove[name] {
				return true
			}

			toolJSON := tool.Raw
			if newName, ok := oauthToolRenameMap[name]; ok && newName != name {
				updatedTool, err := sjson.Set(toolJSON, "name", newName)
				if err == nil {
					toolJSON = updatedTool
					recordRename(name, newName)
				}
			}

			if toolCount > 0 {
				toolsJSON.WriteByte(',')
			}
			toolsJSON.WriteString(toolJSON)
			toolCount++
			return true
		})
		toolsJSON.WriteByte(']')
		body, _ = sjson.SetRawBytes(body, "tools", []byte(toolsJSON.String()))
	}

	// 2. Rename tool_choice if it references a known tool
	toolChoiceType := gjson.GetBytes(body, "tool_choice.type").String()
	if toolChoiceType == "tool" {
		tcName := gjson.GetBytes(body, "tool_choice.name").String()
		if oauthToolsToRemove[tcName] {
			// The chosen tool was removed from the tools array, so drop tool_choice to
			// keep the payload internally consistent and fall back to normal auto tool use.
			body, _ = sjson.DeleteBytes(body, "tool_choice")
		} else if newName, ok := oauthToolRenameMap[tcName]; ok && newName != tcName {
			body, _ = sjson.SetBytes(body, "tool_choice.name", newName)
			recordRename(tcName, newName)
		}
	}

	// 3. Rename tool references in messages
	messages := gjson.GetBytes(body, "messages")
	if messages.Exists() && messages.IsArray() {
		messages.ForEach(func(msgIndex, msg gjson.Result) bool {
			content := msg.Get("content")
			if !content.Exists() || !content.IsArray() {
				return true
			}
			content.ForEach(func(contentIndex, part gjson.Result) bool {
				partType := part.Get("type").String()
				switch partType {
				case "tool_use":
					name := part.Get("name").String()
					if newName, ok := oauthToolRenameMap[name]; ok && newName != name {
						path := fmt.Sprintf("messages.%d.content.%d.name", msgIndex.Int(), contentIndex.Int())
						body, _ = sjson.SetBytes(body, path, newName)
						recordRename(name, newName)
					}
				case "tool_reference":
					toolName := part.Get("tool_name").String()
					if newName, ok := oauthToolRenameMap[toolName]; ok && newName != toolName {
						path := fmt.Sprintf("messages.%d.content.%d.tool_name", msgIndex.Int(), contentIndex.Int())
						body, _ = sjson.SetBytes(body, path, newName)
						recordRename(toolName, newName)
					}
				case "tool_result":
					// Handle nested tool_reference blocks inside tool_result.content[]
					toolID := part.Get("tool_use_id").String()
					_ = toolID // tool_use_id stays as-is
					nestedContent := part.Get("content")
					if nestedContent.Exists() && nestedContent.IsArray() {
						nestedContent.ForEach(func(nestedIndex, nestedPart gjson.Result) bool {
							if nestedPart.Get("type").String() == "tool_reference" {
								nestedToolName := nestedPart.Get("tool_name").String()
								if newName, ok := oauthToolRenameMap[nestedToolName]; ok && newName != nestedToolName {
									nestedPath := fmt.Sprintf("messages.%d.content.%d.content.%d.tool_name", msgIndex.Int(), contentIndex.Int(), nestedIndex.Int())
									body, _ = sjson.SetBytes(body, nestedPath, newName)
									recordRename(nestedToolName, newName)
								}
							}
							return true
						})
					}
				}
				return true
			})
			return true
		})
	}

	return body, reverseMap
}

// reverseRemapOAuthToolNames reverses the tool name mapping for non-stream responses
// using the per-request map produced by remapOAuthToolNames. Names the client sent
// that were NOT forward-renamed are passed through unchanged.
func reverseRemapOAuthToolNames(body []byte, reverseMap map[string]string) []byte {
	if len(reverseMap) == 0 {
		return body
	}
	content := gjson.GetBytes(body, "content")
	if !content.Exists() || !content.IsArray() {
		return body
	}
	content.ForEach(func(index, part gjson.Result) bool {
		partType := part.Get("type").String()
		switch partType {
		case "tool_use":
			name := part.Get("name").String()
			if origName, ok := reverseMap[name]; ok {
				path := fmt.Sprintf("content.%d.name", index.Int())
				body, _ = sjson.SetBytes(body, path, origName)
			}
		case "tool_reference":
			toolName := part.Get("tool_name").String()
			if origName, ok := reverseMap[toolName]; ok {
				path := fmt.Sprintf("content.%d.tool_name", index.Int())
				body, _ = sjson.SetBytes(body, path, origName)
			}
		}
		return true
	})
	return body
}

// reverseRemapOAuthToolNamesFromStreamLine reverses the tool name mapping for SSE
// stream lines, using the per-request reverseMap produced by remapOAuthToolNames.
func reverseRemapOAuthToolNamesFromStreamLine(line []byte, reverseMap map[string]string) []byte {
	if len(reverseMap) == 0 {
		return line
	}
	payload := helps.JSONPayload(line)
	if len(payload) == 0 || !gjson.ValidBytes(payload) {
		return line
	}

	contentBlock := gjson.GetBytes(payload, "content_block")
	if !contentBlock.Exists() {
		return line
	}

	blockType := contentBlock.Get("type").String()
	var updated []byte
	var err error

	switch blockType {
	case "tool_use":
		name := contentBlock.Get("name").String()
		if origName, ok := reverseMap[name]; ok {
			updated, err = sjson.SetBytes(payload, "content_block.name", origName)
			if err != nil {
				return line
			}
		} else {
			return line
		}
	case "tool_reference":
		toolName := contentBlock.Get("tool_name").String()
		if origName, ok := reverseMap[toolName]; ok {
			updated, err = sjson.SetBytes(payload, "content_block.tool_name", origName)
			if err != nil {
				return line
			}
		} else {
			return line
		}
	default:
		return line
	}

	trimmed := bytes.TrimSpace(line)
	if bytes.HasPrefix(trimmed, []byte("data:")) {
		return append([]byte("data: "), updated...)
	}
	return updated
}

type claudeToolNameSanitization struct {
	originalToUpstream map[string]string
	upstreamToOriginal map[string]string
}

func sanitizeClaudeToolNamesForUpstream(body []byte) ([]byte, *claudeToolNameSanitization) {
	names := collectClaudeCustomToolNames(body)
	mapping := buildClaudeToolNameSanitization(names)
	if mapping == nil {
		return body, nil
	}
	return rewriteClaudeRequestToolNames(body, func(name string) string {
		if upstream, ok := mapping.originalToUpstream[name]; ok {
			return upstream
		}
		return name
	}), mapping
}

func sanitizeClaudeHTTPRequestToolNames(req *http.Request) (*claudeToolNameSanitization, error) {
	if req == nil || req.Body == nil || req.Body == http.NoBody {
		return nil, nil
	}
	body, errRead := io.ReadAll(req.Body)
	if errRead != nil {
		return nil, errRead
	}
	if errClose := req.Body.Close(); errClose != nil {
		log.Errorf("request body close error: %v", errClose)
	}
	compatKind := ""
	if req.URL != nil {
		compatKind = config.InferCompatKindFromBaseURL(req.URL.String())
	}
	if repaired, ok := helps.RepairInvalidJSONStringEscapes(body); ok {
		body = repaired
	}
	body = downgradeClaudeToolSearchForCompatKind(compatKind, requestURLString(req), body)
	body = scrubDeepSeekThinkingBudgetForCompat(body, gjson.GetBytes(body, "model").String(), requestURLString(req), compatKind)
	body = applyMiniMaxStreamingThinkingDefaultForCompat(compatKind, body, gjson.GetBytes(body, "stream").Bool())
	body = normalizeClaudeSystemRoleMessages(body)
	updated, mapping := sanitizeClaudeToolNamesForUpstream(body)
	req.Body = io.NopCloser(bytes.NewReader(updated))
	req.ContentLength = int64(len(updated))
	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(updated)), nil
	}
	if req.Header != nil {
		req.Header.Set("Content-Length", strconv.Itoa(len(updated)))
	}
	return mapping, nil
}

func requestURLString(req *http.Request) string {
	if req == nil || req.URL == nil {
		return ""
	}
	return req.URL.String()
}

func restoreClaudeHTTPResponseToolNames(resp *http.Response, mapping *claudeToolNameSanitization) {
	if resp == nil || resp.Body == nil || mapping == nil || len(mapping.upstreamToOriginal) == 0 {
		return
	}
	contentType := strings.ToLower(resp.Header.Get("Content-Type"))
	if strings.Contains(contentType, "text/event-stream") {
		resp.Body = newClaudeToolNameRestoringStream(resp.Body, mapping)
		resp.ContentLength = -1
		resp.Header.Del("Content-Length")
		return
	}
	if strings.TrimSpace(resp.Header.Get("Content-Encoding")) != "" {
		return
	}
	data, errRead := io.ReadAll(resp.Body)
	if errRead != nil {
		log.Errorf("response body read error: %v", errRead)
		return
	}
	if errClose := resp.Body.Close(); errClose != nil {
		log.Errorf("response body close error: %v", errClose)
	}
	data = restoreClaudeToolNamesFromResponse(data, mapping)
	resp.Body = io.NopCloser(bytes.NewReader(data))
	resp.ContentLength = int64(len(data))
	resp.Header.Set("Content-Length", strconv.Itoa(len(data)))
}

type claudeToolNameRestoringStream struct {
	*io.PipeReader
	upstream io.Closer
}

func newClaudeToolNameRestoringStream(upstream io.ReadCloser, mapping *claudeToolNameSanitization) io.ReadCloser {
	reader, writer := io.Pipe()
	go func() {
		defer func() {
			if errClose := upstream.Close(); errClose != nil {
				log.Errorf("response body close error: %v", errClose)
			}
		}()
		scanner := bufio.NewScanner(upstream)
		scanner.Buffer(nil, 52_428_800)
		for scanner.Scan() {
			line := restoreClaudeToolNamesFromStreamLine(scanner.Bytes(), mapping)
			if _, errWrite := writer.Write(line); errWrite != nil {
				_ = writer.CloseWithError(errWrite)
				return
			}
			if _, errWrite := writer.Write([]byte("\n")); errWrite != nil {
				_ = writer.CloseWithError(errWrite)
				return
			}
		}
		if errScan := scanner.Err(); errScan != nil {
			_ = writer.CloseWithError(errScan)
			return
		}
		_ = writer.Close()
	}()
	return &claudeToolNameRestoringStream{PipeReader: reader, upstream: upstream}
}

func (s *claudeToolNameRestoringStream) Close() error {
	errReader := s.PipeReader.Close()
	if s.upstream == nil {
		return errReader
	}
	errUpstream := s.upstream.Close()
	if errReader != nil {
		return errReader
	}
	return errUpstream
}

func collectClaudeCustomToolNames(body []byte) map[string]bool {
	builtinTools := helps.AugmentClaudeBuiltinToolRegistry(body, nil)
	names := make(map[string]bool)
	addName := func(name string) {
		if name == "" || builtinTools[name] {
			return
		}
		names[name] = true
	}

	if tools := gjson.GetBytes(body, "tools"); tools.Exists() && tools.IsArray() {
		tools.ForEach(func(_, tool gjson.Result) bool {
			if tool.Get("type").String() != "" {
				return true
			}
			addName(tool.Get("name").String())
			return true
		})
	}

	if gjson.GetBytes(body, "tool_choice.type").String() == "tool" {
		addName(gjson.GetBytes(body, "tool_choice.name").String())
	}

	if messages := gjson.GetBytes(body, "messages"); messages.Exists() && messages.IsArray() {
		messages.ForEach(func(_, msg gjson.Result) bool {
			content := msg.Get("content")
			if !content.Exists() || !content.IsArray() {
				return true
			}
			content.ForEach(func(_, part gjson.Result) bool {
				switch part.Get("type").String() {
				case "tool_use":
					addName(part.Get("name").String())
				case "tool_reference":
					addName(part.Get("tool_name").String())
				case "tool_result":
					nestedContent := part.Get("content")
					if nestedContent.Exists() && nestedContent.IsArray() {
						nestedContent.ForEach(func(_, nestedPart gjson.Result) bool {
							if nestedPart.Get("type").String() == "tool_reference" {
								addName(nestedPart.Get("tool_name").String())
							}
							return true
						})
					}
				}
				return true
			})
			return true
		})
	}

	return names
}

func buildClaudeToolNameSanitization(names map[string]bool) *claudeToolNameSanitization {
	if len(names) == 0 {
		return nil
	}
	ordered := make([]string, 0, len(names))
	for name := range names {
		ordered = append(ordered, name)
	}
	sort.Strings(ordered)

	occupied := make(map[string]bool, len(ordered))
	for _, name := range ordered {
		if isValidClaudeToolName(name) {
			occupied[name] = true
		}
	}

	mapping := &claudeToolNameSanitization{
		originalToUpstream: make(map[string]string),
		upstreamToOriginal: make(map[string]string),
	}
	for _, name := range ordered {
		if isValidClaudeToolName(name) {
			continue
		}
		candidate := makeClaudeToolNameCandidate(name)
		candidate = uniqueClaudeToolName(candidate, name, occupied)
		occupied[candidate] = true
		mapping.originalToUpstream[name] = candidate
		mapping.upstreamToOriginal[candidate] = name
	}
	if len(mapping.originalToUpstream) == 0 {
		return nil
	}
	return mapping
}

func rewriteClaudeRequestToolNames(body []byte, replace func(string) string) []byte {
	if replace == nil {
		return body
	}
	if tools := gjson.GetBytes(body, "tools"); tools.Exists() && tools.IsArray() {
		tools.ForEach(func(index, tool gjson.Result) bool {
			if tool.Get("type").String() != "" {
				return true
			}
			name := tool.Get("name").String()
			if newName := replace(name); newName != name {
				path := fmt.Sprintf("tools.%d.name", index.Int())
				body, _ = sjson.SetBytes(body, path, newName)
			}
			return true
		})
	}

	if gjson.GetBytes(body, "tool_choice.type").String() == "tool" {
		name := gjson.GetBytes(body, "tool_choice.name").String()
		if newName := replace(name); newName != name {
			body, _ = sjson.SetBytes(body, "tool_choice.name", newName)
		}
	}

	if messages := gjson.GetBytes(body, "messages"); messages.Exists() && messages.IsArray() {
		messages.ForEach(func(msgIndex, msg gjson.Result) bool {
			content := msg.Get("content")
			if !content.Exists() || !content.IsArray() {
				return true
			}
			content.ForEach(func(contentIndex, part gjson.Result) bool {
				switch part.Get("type").String() {
				case "tool_use":
					name := part.Get("name").String()
					if newName := replace(name); newName != name {
						path := fmt.Sprintf("messages.%d.content.%d.name", msgIndex.Int(), contentIndex.Int())
						body, _ = sjson.SetBytes(body, path, newName)
					}
				case "tool_reference":
					toolName := part.Get("tool_name").String()
					if newName := replace(toolName); newName != toolName {
						path := fmt.Sprintf("messages.%d.content.%d.tool_name", msgIndex.Int(), contentIndex.Int())
						body, _ = sjson.SetBytes(body, path, newName)
					}
				case "tool_result":
					nestedContent := part.Get("content")
					if nestedContent.Exists() && nestedContent.IsArray() {
						nestedContent.ForEach(func(nestedIndex, nestedPart gjson.Result) bool {
							if nestedPart.Get("type").String() != "tool_reference" {
								return true
							}
							nestedToolName := nestedPart.Get("tool_name").String()
							if newName := replace(nestedToolName); newName != nestedToolName {
								nestedPath := fmt.Sprintf("messages.%d.content.%d.content.%d.tool_name", msgIndex.Int(), contentIndex.Int(), nestedIndex.Int())
								body, _ = sjson.SetBytes(body, nestedPath, newName)
							}
							return true
						})
					}
				}
				return true
			})
			return true
		})
	}
	return body
}

func restoreClaudeToolNamesFromResponse(body []byte, mapping *claudeToolNameSanitization) []byte {
	if mapping == nil || len(mapping.upstreamToOriginal) == 0 {
		return body
	}
	content := gjson.GetBytes(body, "content")
	if !content.Exists() || !content.IsArray() {
		return body
	}
	content.ForEach(func(index, part gjson.Result) bool {
		switch part.Get("type").String() {
		case "tool_use":
			name := part.Get("name").String()
			if original, ok := mapping.upstreamToOriginal[name]; ok {
				path := fmt.Sprintf("content.%d.name", index.Int())
				body, _ = sjson.SetBytes(body, path, original)
			}
		case "tool_reference":
			toolName := part.Get("tool_name").String()
			if original, ok := mapping.upstreamToOriginal[toolName]; ok {
				path := fmt.Sprintf("content.%d.tool_name", index.Int())
				body, _ = sjson.SetBytes(body, path, original)
			}
		case "tool_result":
			nestedContent := part.Get("content")
			if nestedContent.Exists() && nestedContent.IsArray() {
				nestedContent.ForEach(func(nestedIndex, nestedPart gjson.Result) bool {
					if nestedPart.Get("type").String() != "tool_reference" {
						return true
					}
					nestedToolName := nestedPart.Get("tool_name").String()
					if original, ok := mapping.upstreamToOriginal[nestedToolName]; ok {
						nestedPath := fmt.Sprintf("content.%d.content.%d.tool_name", index.Int(), nestedIndex.Int())
						body, _ = sjson.SetBytes(body, nestedPath, original)
					}
					return true
				})
			}
		}
		return true
	})
	return body
}

func restoreClaudeToolNamesFromStreamLine(line []byte, mapping *claudeToolNameSanitization) []byte {
	if mapping == nil || len(mapping.upstreamToOriginal) == 0 {
		return line
	}
	payload := helps.JSONPayload(line)
	if len(payload) == 0 || !gjson.ValidBytes(payload) {
		return line
	}
	contentBlock := gjson.GetBytes(payload, "content_block")
	if !contentBlock.Exists() {
		return line
	}

	var updated []byte
	var err error
	switch contentBlock.Get("type").String() {
	case "tool_use":
		name := contentBlock.Get("name").String()
		original, ok := mapping.upstreamToOriginal[name]
		if !ok {
			return line
		}
		updated, err = sjson.SetBytes(payload, "content_block.name", original)
	case "tool_reference":
		toolName := contentBlock.Get("tool_name").String()
		original, ok := mapping.upstreamToOriginal[toolName]
		if !ok {
			return line
		}
		updated, err = sjson.SetBytes(payload, "content_block.tool_name", original)
	default:
		return line
	}
	if err != nil {
		return line
	}

	trimmed := bytes.TrimSpace(line)
	if bytes.HasPrefix(trimmed, []byte("data:")) {
		return append([]byte("data: "), updated...)
	}
	return updated
}

func isValidClaudeToolName(name string) bool {
	if len(name) == 0 || len(name) > 64 || !isClaudeToolNameStart(name[0]) {
		return false
	}
	for i := 1; i < len(name); i++ {
		if !isClaudeToolNameChar(name[i]) {
			return false
		}
	}
	return true
}

func isClaudeToolNameStart(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isClaudeToolNameChar(c byte) bool {
	return isClaudeToolNameStart(c) || (c >= '0' && c <= '9') || c == '_' || c == '-'
}

func makeClaudeToolNameCandidate(name string) string {
	var b strings.Builder
	b.Grow(len(name) + len("tool_"))
	for i := 0; i < len(name); i++ {
		c := name[i]
		if isClaudeToolNameChar(c) {
			b.WriteByte(c)
		} else {
			b.WriteByte('_')
		}
	}
	candidate := b.String()
	if candidate == "" {
		candidate = "tool"
	}
	if !isClaudeToolNameStart(candidate[0]) {
		candidate = "tool_" + candidate
	}
	return truncateClaudeToolName(candidate, "_"+shortClaudeToolNameHash(name, 8))
}

func uniqueClaudeToolName(candidate, original string, occupied map[string]bool) string {
	if candidate == "" {
		candidate = "tool_" + shortClaudeToolNameHash(original, 8)
	}
	if !occupied[candidate] {
		return candidate
	}
	hash := shortClaudeToolNameHash(original, 8)
	for i := 0; ; i++ {
		suffix := "_" + hash
		if i > 0 {
			suffix = fmt.Sprintf("_%s_%d", hash[:6], i)
		}
		unique := withClaudeToolNameSuffix(candidate, suffix)
		if !occupied[unique] {
			return unique
		}
	}
}

func withClaudeToolNameSuffix(name, suffix string) string {
	maxPrefixLen := 64 - len(suffix)
	if maxPrefixLen < 1 {
		maxPrefixLen = 1
	}
	if len(name) > maxPrefixLen {
		name = name[:maxPrefixLen]
	}
	name += suffix
	if !isClaudeToolNameStart(name[0]) {
		name = "tool_" + name
		if len(name) > 64 {
			name = name[:64]
		}
	}
	return name
}

func truncateClaudeToolName(name, suffix string) string {
	if len(name) <= 64 {
		return name
	}
	maxPrefixLen := 64 - len(suffix)
	if maxPrefixLen < 1 {
		maxPrefixLen = 1
	}
	name = name[:maxPrefixLen] + suffix
	if !isClaudeToolNameStart(name[0]) {
		name = "tool_" + name
		if len(name) > 64 {
			name = name[:64]
		}
	}
	return name
}

func shortClaudeToolNameHash(input string, length int) string {
	h := sha256.Sum256([]byte(input))
	out := hex.EncodeToString(h[:])
	if length > len(out) {
		length = len(out)
	}
	return out[:length]
}

func applyClaudeToolPrefix(body []byte, prefix string) []byte {
	if prefix == "" {
		return body
	}

	// Collect built-in tool names from the authoritative fallback seed list and
	// augment it with any typed built-ins present in the current request body.
	builtinTools := helps.AugmentClaudeBuiltinToolRegistry(body, nil)

	if tools := gjson.GetBytes(body, "tools"); tools.Exists() && tools.IsArray() {
		tools.ForEach(func(index, tool gjson.Result) bool {
			// Skip built-in tools (web_search, code_execution, etc.) which have
			// a "type" field and require their name to remain unchanged.
			if tool.Get("type").Exists() && tool.Get("type").String() != "" {
				if n := tool.Get("name").String(); n != "" {
					builtinTools[n] = true
				}
				return true
			}
			name := tool.Get("name").String()
			if name == "" || strings.HasPrefix(name, prefix) {
				return true
			}
			path := fmt.Sprintf("tools.%d.name", index.Int())
			body, _ = sjson.SetBytes(body, path, prefix+name)
			return true
		})
	}

	if gjson.GetBytes(body, "tool_choice.type").String() == "tool" {
		name := gjson.GetBytes(body, "tool_choice.name").String()
		if name != "" && !strings.HasPrefix(name, prefix) && !builtinTools[name] {
			body, _ = sjson.SetBytes(body, "tool_choice.name", prefix+name)
		}
	}

	if messages := gjson.GetBytes(body, "messages"); messages.Exists() && messages.IsArray() {
		messages.ForEach(func(msgIndex, msg gjson.Result) bool {
			content := msg.Get("content")
			if !content.Exists() || !content.IsArray() {
				return true
			}
			content.ForEach(func(contentIndex, part gjson.Result) bool {
				partType := part.Get("type").String()
				switch partType {
				case "tool_use":
					name := part.Get("name").String()
					if name == "" || strings.HasPrefix(name, prefix) || builtinTools[name] {
						return true
					}
					path := fmt.Sprintf("messages.%d.content.%d.name", msgIndex.Int(), contentIndex.Int())
					body, _ = sjson.SetBytes(body, path, prefix+name)
				case "tool_reference":
					toolName := part.Get("tool_name").String()
					if toolName == "" || strings.HasPrefix(toolName, prefix) || builtinTools[toolName] {
						return true
					}
					path := fmt.Sprintf("messages.%d.content.%d.tool_name", msgIndex.Int(), contentIndex.Int())
					body, _ = sjson.SetBytes(body, path, prefix+toolName)
				case "tool_result":
					// Handle nested tool_reference blocks inside tool_result.content[]
					nestedContent := part.Get("content")
					if nestedContent.Exists() && nestedContent.IsArray() {
						nestedContent.ForEach(func(nestedIndex, nestedPart gjson.Result) bool {
							if nestedPart.Get("type").String() == "tool_reference" {
								nestedToolName := nestedPart.Get("tool_name").String()
								if nestedToolName != "" && !strings.HasPrefix(nestedToolName, prefix) && !builtinTools[nestedToolName] {
									nestedPath := fmt.Sprintf("messages.%d.content.%d.content.%d.tool_name", msgIndex.Int(), contentIndex.Int(), nestedIndex.Int())
									body, _ = sjson.SetBytes(body, nestedPath, prefix+nestedToolName)
								}
							}
							return true
						})
					}
				}
				return true
			})
			return true
		})
	}

	return body
}

func stripClaudeToolPrefixFromResponse(body []byte, prefix string) []byte {
	if prefix == "" {
		return body
	}
	content := gjson.GetBytes(body, "content")
	if !content.Exists() || !content.IsArray() {
		return body
	}
	content.ForEach(func(index, part gjson.Result) bool {
		partType := part.Get("type").String()
		switch partType {
		case "tool_use":
			name := part.Get("name").String()
			if !strings.HasPrefix(name, prefix) {
				return true
			}
			path := fmt.Sprintf("content.%d.name", index.Int())
			body, _ = sjson.SetBytes(body, path, strings.TrimPrefix(name, prefix))
		case "tool_reference":
			toolName := part.Get("tool_name").String()
			if !strings.HasPrefix(toolName, prefix) {
				return true
			}
			path := fmt.Sprintf("content.%d.tool_name", index.Int())
			body, _ = sjson.SetBytes(body, path, strings.TrimPrefix(toolName, prefix))
		case "tool_result":
			// Handle nested tool_reference blocks inside tool_result.content[]
			nestedContent := part.Get("content")
			if nestedContent.Exists() && nestedContent.IsArray() {
				nestedContent.ForEach(func(nestedIndex, nestedPart gjson.Result) bool {
					if nestedPart.Get("type").String() == "tool_reference" {
						nestedToolName := nestedPart.Get("tool_name").String()
						if strings.HasPrefix(nestedToolName, prefix) {
							nestedPath := fmt.Sprintf("content.%d.content.%d.tool_name", index.Int(), nestedIndex.Int())
							body, _ = sjson.SetBytes(body, nestedPath, strings.TrimPrefix(nestedToolName, prefix))
						}
					}
					return true
				})
			}
		}
		return true
	})
	return body
}

func stripClaudeToolPrefixFromStreamLine(line []byte, prefix string) []byte {
	if prefix == "" {
		return line
	}
	payload := helps.JSONPayload(line)
	if len(payload) == 0 || !gjson.ValidBytes(payload) {
		return line
	}
	contentBlock := gjson.GetBytes(payload, "content_block")
	if !contentBlock.Exists() {
		return line
	}

	blockType := contentBlock.Get("type").String()
	var updated []byte
	var err error

	switch blockType {
	case "tool_use":
		name := contentBlock.Get("name").String()
		if !strings.HasPrefix(name, prefix) {
			return line
		}
		updated, err = sjson.SetBytes(payload, "content_block.name", strings.TrimPrefix(name, prefix))
		if err != nil {
			return line
		}
	case "tool_reference":
		toolName := contentBlock.Get("tool_name").String()
		if !strings.HasPrefix(toolName, prefix) {
			return line
		}
		updated, err = sjson.SetBytes(payload, "content_block.tool_name", strings.TrimPrefix(toolName, prefix))
		if err != nil {
			return line
		}
	default:
		return line
	}

	trimmed := bytes.TrimSpace(line)
	if bytes.HasPrefix(trimmed, []byte("data:")) {
		return append([]byte("data: "), updated...)
	}
	return updated
}

// getClientUserAgent extracts the client User-Agent from the gin context.
func getClientUserAgent(ctx context.Context) string {
	if ginCtx, ok := ctx.Value("gin").(*gin.Context); ok && ginCtx != nil && ginCtx.Request != nil {
		return ginCtx.GetHeader("User-Agent")
	}
	return ""
}

// parseEntrypointFromUA extracts the entrypoint from a Claude Code User-Agent.
// Format: "claude-cli/x.y.z (external, cli)" → "cli"
// Format: "claude-cli/x.y.z (external, vscode)" → "vscode"
// Returns "cli" if parsing fails or UA is not Claude Code.
func parseEntrypointFromUA(userAgent string) string {
	// Find content inside parentheses
	start := strings.Index(userAgent, "(")
	end := strings.LastIndex(userAgent, ")")
	if start < 0 || end <= start {
		return "cli"
	}
	inner := userAgent[start+1 : end]
	// Split by comma, take the second part (entrypoint is at index 1, after USER_TYPE)
	// Format: "(USER_TYPE, ENTRYPOINT[, extra...])"
	parts := strings.Split(inner, ",")
	if len(parts) >= 2 {
		ep := strings.TrimSpace(parts[1])
		if ep != "" {
			return ep
		}
	}
	return "cli"
}

// getWorkloadFromContext extracts workload identifier from the gin request headers.
func getWorkloadFromContext(ctx context.Context) string {
	if ginCtx, ok := ctx.Value("gin").(*gin.Context); ok && ginCtx != nil && ginCtx.Request != nil {
		return strings.TrimSpace(ginCtx.GetHeader("X-CPA-Claude-Workload"))
	}
	return ""
}

// getCloakConfigFromAuth extracts cloak configuration from auth attributes.
// Returns (cloakMode, strictMode, sensitiveWords, cacheUserID).
func getCloakConfigFromAuth(auth *cliproxyauth.Auth) (string, bool, []string, bool) {
	if auth == nil || auth.Attributes == nil {
		return "auto", false, nil, false
	}

	cloakMode := auth.Attributes["cloak_mode"]
	if cloakMode == "" {
		cloakMode = "auto"
	}

	strictMode := strings.ToLower(auth.Attributes["cloak_strict_mode"]) == "true"

	var sensitiveWords []string
	if wordsStr := auth.Attributes["cloak_sensitive_words"]; wordsStr != "" {
		sensitiveWords = strings.Split(wordsStr, ",")
		for i := range sensitiveWords {
			sensitiveWords[i] = strings.TrimSpace(sensitiveWords[i])
		}
	}

	cacheUserID := strings.EqualFold(strings.TrimSpace(auth.Attributes["cloak_cache_user_id"]), "true")

	return cloakMode, strictMode, sensitiveWords, cacheUserID
}

// injectFakeUserID generates and injects a fake user ID into the request metadata.
// When useCache is false, a new user ID is generated for every call.
func injectFakeUserID(payload []byte, apiKey string, useCache bool) []byte {
	generateID := func() string {
		if useCache {
			return helps.CachedUserID(apiKey)
		}
		return helps.GenerateFakeUserID()
	}

	metadata := gjson.GetBytes(payload, "metadata")
	if !metadata.Exists() {
		payload, _ = sjson.SetBytes(payload, "metadata.user_id", generateID())
		return payload
	}

	existingUserID := gjson.GetBytes(payload, "metadata.user_id").String()
	if existingUserID == "" || !helps.IsValidUserID(existingUserID) {
		payload, _ = sjson.SetBytes(payload, "metadata.user_id", generateID())
	}
	return payload
}

// fingerprintSalt is the salt used by Claude Code to compute the 3-char build fingerprint.
const fingerprintSalt = "59cf53e54c78"

// computeFingerprint computes the 3-char build fingerprint that Claude Code embeds in cc_version.
// Algorithm: SHA256(salt + messageText[4] + messageText[7] + messageText[20] + version)[:3]
func computeFingerprint(messageText, version string) string {
	indices := [3]int{4, 7, 20}
	runes := []rune(messageText)
	var sb strings.Builder
	for _, idx := range indices {
		if idx < len(runes) {
			sb.WriteRune(runes[idx])
		} else {
			sb.WriteRune('0')
		}
	}
	input := fingerprintSalt + sb.String() + version
	h := sha256.Sum256([]byte(input))
	return hex.EncodeToString(h[:])[:3]
}

// generateBillingHeader creates the x-anthropic-billing-header text block that
// real Claude Code prepends to every system prompt array.
// Format: x-anthropic-billing-header: cc_version=<ver>.<build>; cc_entrypoint=<ep>; cch=<hash>; [cc_workload=<wl>;]
func generateBillingHeader(payload []byte, experimentalCCHSigning bool, version, messageText, entrypoint, workload string) string {
	if entrypoint == "" {
		entrypoint = "cli"
	}
	buildHash := computeFingerprint(messageText, version)
	workloadPart := ""
	if workload != "" {
		workloadPart = fmt.Sprintf(" cc_workload=%s;", workload)
	}

	if experimentalCCHSigning {
		return fmt.Sprintf("x-anthropic-billing-header: cc_version=%s.%s; cc_entrypoint=%s; cch=00000;%s", version, buildHash, entrypoint, workloadPart)
	}

	// Generate a deterministic cch hash from the payload content (system + messages + tools).
	h := sha256.Sum256(payload)
	cch := hex.EncodeToString(h[:])[:5]
	return fmt.Sprintf("x-anthropic-billing-header: cc_version=%s.%s; cc_entrypoint=%s; cch=%s;%s", version, buildHash, entrypoint, cch, workloadPart)
}

func checkSystemInstructionsWithMode(payload []byte, strictMode bool) []byte {
	return checkSystemInstructionsWithSigningMode(payload, strictMode, false, false, "2.1.63", "", "")
}

// checkSystemInstructionsWithSigningMode injects Claude Code-style system blocks:
//
//	system[0]: billing header (no cache_control)
//	system[1]: agent identifier (cache_control ephemeral, scope=org)
//	system[2]: core intro prompt (cache_control ephemeral, scope=global)
//	system[3]: system instructions (no cache_control)
//	system[4]: doing tasks (no cache_control)
//	system[5]: user system messages moved to first user message
func checkSystemInstructionsWithSigningMode(payload []byte, strictMode bool, experimentalCCHSigning bool, oauthMode bool, version, entrypoint, workload string) []byte {
	system := gjson.GetBytes(payload, "system")

	// Extract original message text for fingerprint computation (before billing injection).
	// Use the first system text block's content as the fingerprint source.
	messageText := ""
	if system.IsArray() {
		system.ForEach(func(_, part gjson.Result) bool {
			if part.Get("type").String() == "text" {
				messageText = part.Get("text").String()
				return false
			}
			return true
		})
	} else if system.Type == gjson.String {
		messageText = system.String()
	}

	// Skip if already injected
	firstText := gjson.GetBytes(payload, "system.0.text").String()
	if strings.HasPrefix(firstText, "x-anthropic-billing-header:") {
		return payload
	}

	billingText := generateBillingHeader(payload, experimentalCCHSigning, version, messageText, entrypoint, workload)
	billingBlock := buildTextBlock(billingText, nil)

	// Build system blocks matching real Claude Code structure.
	// Important: Claude Code's internal cacheScope='org' does NOT serialize to
	// scope='org' in the API request. Only scope='global' is sent explicitly.
	// The system prompt prefix block is sent without cache_control.
	agentBlock := buildTextBlock("You are Claude Code, Anthropic's official CLI for Claude.", nil)
	staticPrompt := strings.Join([]string{
		helps.ClaudeCodeIntro,
		helps.ClaudeCodeSystem,
		helps.ClaudeCodeDoingTasks,
		helps.ClaudeCodeToneAndStyle,
		helps.ClaudeCodeOutputEfficiency,
	}, "\n\n")
	staticBlock := buildTextBlock(staticPrompt, nil)

	systemResult := "[" + billingBlock + "," + agentBlock + "," + staticBlock + "]"
	payload, _ = sjson.SetRawBytes(payload, "system", []byte(systemResult))

	// Collect user system instructions and prepend to first user message
	if !strictMode {
		var userSystemParts []string
		if system.IsArray() {
			system.ForEach(func(_, part gjson.Result) bool {
				if part.Get("type").String() == "text" {
					txt := strings.TrimSpace(part.Get("text").String())
					if txt != "" {
						userSystemParts = append(userSystemParts, txt)
					}
				}
				return true
			})
		} else if system.Type == gjson.String && strings.TrimSpace(system.String()) != "" {
			userSystemParts = append(userSystemParts, strings.TrimSpace(system.String()))
		}

		if len(userSystemParts) > 0 {
			combined := strings.Join(userSystemParts, "\n\n")
			if oauthMode {
				combined = sanitizeForwardedSystemPrompt(combined)
			}
			if strings.TrimSpace(combined) != "" {
				payload = prependToFirstUserMessage(payload, combined)
			}
		}
	}

	return payload
}

// sanitizeForwardedSystemPrompt reduces forwarded third-party system context to a
// tiny neutral reminder for Claude OAuth cloaking. The goal is to preserve only
// the minimum tool/task guidance while removing virtually all client-specific
// prompt structure that Anthropic may classify as third-party agent traffic.
func sanitizeForwardedSystemPrompt(text string) string {
	if strings.TrimSpace(text) == "" {
		return ""
	}
	return strings.TrimSpace(`Use the available tools when needed to help with software engineering tasks.
Keep responses concise and focused on the user's request.
Prefer acting on the user's task over describing product-specific workflows.`)
}

// buildTextBlock constructs a JSON text block object with proper escaping.
// Uses sjson.SetBytes to handle multi-line text, quotes, and control characters.
// cacheControl is optional; pass nil to omit cache_control.
func buildTextBlock(text string, cacheControl map[string]string) string {
	block := []byte(`{"type":"text"}`)
	block, _ = sjson.SetBytes(block, "text", text)
	if cacheControl != nil && len(cacheControl) > 0 {
		// Build cache_control JSON manually to avoid sjson map marshaling issues.
		// sjson.SetBytes with map[string]string may not produce expected structure.
		cc := `{"type":"ephemeral"`
		if t, ok := cacheControl["ttl"]; ok {
			cc += fmt.Sprintf(`,"ttl":"%s"`, t)
		}
		cc += "}"
		block, _ = sjson.SetRawBytes(block, "cache_control", []byte(cc))
	}
	return string(block)
}

// prependToFirstUserMessage prepends text content to the first user message.
// This avoids putting non-Claude-Code system instructions in system[] which
// triggers Anthropic's extra usage billing for OAuth-proxied requests.
func prependToFirstUserMessage(payload []byte, text string) []byte {
	messages := gjson.GetBytes(payload, "messages")
	if !messages.Exists() || !messages.IsArray() {
		return payload
	}

	// Find the first user message index
	firstUserIdx := -1
	messages.ForEach(func(idx, msg gjson.Result) bool {
		if msg.Get("role").String() == "user" {
			firstUserIdx = int(idx.Int())
			return false
		}
		return true
	})

	if firstUserIdx < 0 {
		return payload
	}

	prefixBlock := fmt.Sprintf(`<system-reminder>
As you answer the user's questions, you can use the following context from the system:
%s

IMPORTANT: this context may or may not be relevant to your tasks. You should not respond to this context unless it is highly relevant to your task.
</system-reminder>
`, text)

	contentPath := fmt.Sprintf("messages.%d.content", firstUserIdx)
	content := gjson.GetBytes(payload, contentPath)

	if content.IsArray() {
		newBlock := fmt.Sprintf(`{"type":"text","text":%q}`, prefixBlock)
		var newArray string
		if content.Raw == "[]" || content.Raw == "" {
			newArray = "[" + newBlock + "]"
		} else {
			newArray = "[" + newBlock + "," + content.Raw[1:]
		}
		payload, _ = sjson.SetRawBytes(payload, contentPath, []byte(newArray))
	} else if content.Type == gjson.String {
		newText := prefixBlock + content.String()
		payload, _ = sjson.SetBytes(payload, contentPath, newText)
	}

	return payload
}

// applyCloaking applies cloaking transformations to the payload based on config and client.
// Cloaking includes: system prompt injection, fake user ID, and sensitive word obfuscation.
func applyCloaking(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth, payload []byte, model string, apiKey string) []byte {
	clientUserAgent := getClientUserAgent(ctx)
	// Enable cch signing for OAuth tokens by default (not just experimental flag).
	oauthToken := isClaudeOAuthToken(apiKey)
	useCCHSigning := oauthToken || experimentalCCHSigningEnabled(cfg, auth)

	// Get cloak config from ClaudeKey configuration
	cloakCfg := resolveClaudeKeyCloakConfig(cfg, auth)
	attrMode, attrStrict, attrWords, attrCache := getCloakConfigFromAuth(auth)

	// Determine cloak settings
	cloakMode := attrMode
	strictMode := attrStrict
	sensitiveWords := attrWords
	cacheUserID := attrCache

	if cloakCfg != nil {
		if mode := strings.TrimSpace(cloakCfg.Mode); mode != "" {
			cloakMode = mode
		}
		if cloakCfg.StrictMode {
			strictMode = true
		}
		if len(cloakCfg.SensitiveWords) > 0 {
			sensitiveWords = cloakCfg.SensitiveWords
		}
		if cloakCfg.CacheUserID != nil {
			cacheUserID = *cloakCfg.CacheUserID
		}
	}

	// Determine if cloaking should be applied
	if !helps.ShouldCloak(cloakMode, clientUserAgent) {
		return payload
	}

	// Skip system instructions for claude-3-5-haiku models
	if !strings.HasPrefix(model, "claude-3-5-haiku") {
		billingVersion := helps.DefaultClaudeVersion(cfg)
		entrypoint := parseEntrypointFromUA(clientUserAgent)
		workload := getWorkloadFromContext(ctx)
		payload = checkSystemInstructionsWithSigningMode(payload, strictMode, useCCHSigning, oauthToken, billingVersion, entrypoint, workload)
	}

	// Inject fake user ID
	payload = injectFakeUserID(payload, apiKey, cacheUserID)

	// Apply sensitive word obfuscation
	if len(sensitiveWords) > 0 {
		matcher := helps.BuildSensitiveWordMatcher(sensitiveWords)
		payload = helps.ObfuscateSensitiveWords(payload, matcher)
	}

	return payload
}

// ensureCacheControl injects cache_control breakpoints into the payload for optimal prompt caching.
// According to Anthropic's documentation, cache prefixes are created in order: tools -> system -> messages.
// This function adds cache_control to:
// 1. The LAST tool in the tools array (caches all tool definitions)
// 2. The LAST system prompt element
// 3. The SECOND-TO-LAST user turn (caches conversation history for multi-turn)
//
// Up to 4 cache breakpoints are allowed per request. Tools, System, and Messages are INDEPENDENT breakpoints.
// This enables up to 90% cost reduction on cached tokens (cache read = 0.1x base price).
// See: https://docs.anthropic.com/en/docs/build-with-claude/prompt-caching
func ensureCacheControl(payload []byte) []byte {
	// 1. Inject cache_control into the LAST tool (caches all tool definitions)
	// Tools are cached first in the hierarchy, so this is the most important breakpoint.
	payload = injectToolsCacheControl(payload)

	// 2. Inject cache_control into the LAST system prompt element
	// System is the second level in the cache hierarchy.
	payload = injectSystemCacheControl(payload)

	// 3. Inject cache_control into messages for multi-turn conversation caching
	// This caches the conversation history up to the second-to-last user turn.
	payload = injectMessagesCacheControl(payload)

	return payload
}

func countCacheControls(payload []byte) int {
	count := 0

	// Check system
	system := gjson.GetBytes(payload, "system")
	if system.IsArray() {
		system.ForEach(func(_, item gjson.Result) bool {
			if item.Get("cache_control").Exists() {
				count++
			}
			return true
		})
	}

	// Check tools
	tools := gjson.GetBytes(payload, "tools")
	if tools.IsArray() {
		tools.ForEach(func(_, item gjson.Result) bool {
			if item.Get("cache_control").Exists() {
				count++
			}
			return true
		})
	}

	// Check messages
	messages := gjson.GetBytes(payload, "messages")
	if messages.IsArray() {
		messages.ForEach(func(_, msg gjson.Result) bool {
			content := msg.Get("content")
			if content.IsArray() {
				content.ForEach(func(_, item gjson.Result) bool {
					if item.Get("cache_control").Exists() {
						count++
					}
					return true
				})
			}
			return true
		})
	}

	return count
}

// applyCacheControlPipeline runs the three cache_control phases sharing a single
// initial scan of the payload. It is byte-for-byte equivalent to the legacy
// sequence used at the call sites:
//
//	if countCacheControls(body) == 0 { body = ensureCacheControl(body) } // when doInject
//	body = enforceCacheControlLimit(body, maxBlocks)
//	body = normalizeCacheControlTTL(body)
//
// Only the read side is merged: a one-pass model drives the common path
// (payload already within the limit) so normalization runs without re-walking
// the document. Writes stay pointwise via sjson. The rarer paths (injection
// needed, or block count over the limit) defer to the legacy functions, which
// reshape the body and are re-scanned freshly. When doInject is false the
// injection phase is skipped entirely (CountTokens path).
func applyCacheControlPipeline(payload []byte, maxBlocks int, doInject bool) []byte {
	model := collectCacheControlModel(payload)

	if doInject && model.total == 0 {
		// Client sent no cache_control: inject breakpoints, then enforce and
		// normalize on the mutated body (the model above is now stale).
		payload = ensureCacheControl(payload)
		payload = enforceCacheControlLimit(payload, maxBlocks)
		return normalizeCacheControlTTL(payload)
	}

	if model.total > maxBlocks {
		// Over the breakpoint limit: legacy enforce reshapes the body, then
		// normalize runs on the fresh result.
		payload = enforceCacheControlLimit(payload, maxBlocks)
		return normalizeCacheControlTTL(payload)
	}

	// Common path: within the limit and no injection. enforce would be a no-op,
	// so normalize directly from the model already built (single scan total).
	return normalizeFromModel(payload, model)
}

// cacheControlBlock is a lightweight descriptor of one cache_control breakpoint,
// captured in a single modelling pass so the normalize phase need not re-walk
// the document. path is the gjson/sjson path to the OWNING element (e.g.
// "tools.0", "system.1", "messages.2.content.0"); the ".cache_control" suffix is
// appended by consumers.
type cacheControlBlock struct {
	path       string
	ccIsObject bool
	ttlIs1h    bool
}

// cacheControlModel holds every cache_control breakpoint in Anthropic evaluation
// order (tools → system → messages). total mirrors countCacheControls(payload):
// the block set is identical, so total == len(blocks).
type cacheControlModel struct {
	total  int
	blocks []cacheControlBlock
}

// collectCacheControlModel walks tools, system, and messages once, recording each
// cache_control breakpoint in evaluation order. The captured (ccIsObject, ttlIs1h)
// flags carry exactly the information normalizeFromModel needs, replacing the
// repeated independent walks of countCacheControls + normalizeCacheControlTTL.
func collectCacheControlModel(payload []byte) cacheControlModel {
	model := cacheControlModel{}
	addBlock := func(path string, item gjson.Result) {
		cc := item.Get("cache_control")
		if !cc.Exists() {
			return
		}
		ttl := cc.Get("ttl")
		model.blocks = append(model.blocks, cacheControlBlock{
			path:       path,
			ccIsObject: cc.IsObject(),
			ttlIs1h:    ttl.Type == gjson.String && ttl.String() == "1h",
		})
	}

	if tools := gjson.GetBytes(payload, "tools"); tools.IsArray() {
		tools.ForEach(func(idx, item gjson.Result) bool {
			addBlock(fmt.Sprintf("tools.%d", int(idx.Int())), item)
			return true
		})
	}
	if system := gjson.GetBytes(payload, "system"); system.IsArray() {
		system.ForEach(func(idx, item gjson.Result) bool {
			addBlock(fmt.Sprintf("system.%d", int(idx.Int())), item)
			return true
		})
	}
	if messages := gjson.GetBytes(payload, "messages"); messages.IsArray() {
		messages.ForEach(func(msgIdx, msg gjson.Result) bool {
			content := msg.Get("content")
			if !content.IsArray() {
				return true
			}
			content.ForEach(func(itemIdx, item gjson.Result) bool {
				addBlock(fmt.Sprintf("messages.%d.content.%d", int(msgIdx.Int()), int(itemIdx.Int())), item)
				return true
			})
			return true
		})
	}

	model.total = len(model.blocks)
	return model
}

// normalizeFromModel applies the TTL ordering normalization using the prebuilt
// model, equivalent to normalizeCacheControlTTL but without re-walking the
// document. Anthropic evaluates blocks in order tools → system → messages; once
// a 5m (default) block is seen, every later 1h block must drop its ttl. The
// model's blocks are already in evaluation order, so a non-object cache_control
// or a non-"1h" ttl marks a 5m block. Bytes are returned unchanged when no
// deletion occurs, preserving the no-op identity guarantee.
func normalizeFromModel(payload []byte, model cacheControlModel) []byte {
	// Match the guard in normalizeCacheControlTTL/enforceCacheControlLimit so the
	// common path stays byte-identical to the legacy sequence on empty or invalid
	// JSON (both leave the payload untouched).
	if len(payload) == 0 || !gjson.ValidBytes(payload) {
		return payload
	}
	seen5m := false
	for _, b := range model.blocks {
		if !b.ccIsObject || !b.ttlIs1h {
			seen5m = true
			continue
		}
		if !seen5m {
			continue
		}
		if updated, errDel := sjson.DeleteBytes(payload, b.path+".cache_control.ttl"); errDel == nil {
			payload = updated
		}
	}
	return payload
}

// normalizeCacheControlTTL ensures cache_control TTL values don't violate the
// prompt-caching-scope-2026-01-05 ordering constraint: a 1h-TTL block must not
// appear after a 5m-TTL block anywhere in the evaluation order.
//
// Anthropic evaluates blocks in order: tools → system (index 0..N) → messages.
// Within each section, blocks are evaluated in array order. A 5m (default) block
// followed by a 1h block at ANY later position is an error — including within
// the same section (e.g. system[1]=5m then system[3]=1h).
//
// Strategy: walk all cache_control blocks in evaluation order. Once a 5m block
// is seen, strip ttl from ALL subsequent 1h blocks (downgrading them to 5m).
func normalizeCacheControlTTL(payload []byte) []byte {
	if len(payload) == 0 || !gjson.ValidBytes(payload) {
		return payload
	}

	original := payload
	seen5m := false
	modified := false

	processBlock := func(path string, obj gjson.Result) {
		cc := obj.Get("cache_control")
		if !cc.Exists() {
			return
		}
		if !cc.IsObject() {
			seen5m = true
			return
		}
		ttl := cc.Get("ttl")
		if ttl.Type != gjson.String || ttl.String() != "1h" {
			seen5m = true
			return
		}
		if !seen5m {
			return
		}
		ttlPath := path + ".cache_control.ttl"
		updated, errDel := sjson.DeleteBytes(payload, ttlPath)
		if errDel != nil {
			return
		}
		payload = updated
		modified = true
	}

	tools := gjson.GetBytes(payload, "tools")
	if tools.IsArray() {
		tools.ForEach(func(idx, item gjson.Result) bool {
			processBlock(fmt.Sprintf("tools.%d", int(idx.Int())), item)
			return true
		})
	}

	system := gjson.GetBytes(payload, "system")
	if system.IsArray() {
		system.ForEach(func(idx, item gjson.Result) bool {
			processBlock(fmt.Sprintf("system.%d", int(idx.Int())), item)
			return true
		})
	}

	messages := gjson.GetBytes(payload, "messages")
	if messages.IsArray() {
		messages.ForEach(func(msgIdx, msg gjson.Result) bool {
			content := msg.Get("content")
			if !content.IsArray() {
				return true
			}
			content.ForEach(func(itemIdx, item gjson.Result) bool {
				processBlock(fmt.Sprintf("messages.%d.content.%d", int(msgIdx.Int()), int(itemIdx.Int())), item)
				return true
			})
			return true
		})
	}

	if !modified {
		return original
	}
	return payload
}

// enforceCacheControlLimit removes excess cache_control blocks from a payload
// so the total does not exceed the Anthropic API limit (currently 4).
//
// Anthropic evaluates cache breakpoints in order: tools → system → messages.
// The most valuable breakpoints are:
//  1. Last tool         — caches ALL tool definitions
//  2. Last system block — caches ALL system content
//  3. Recent messages   — cache conversation context
//
// Removal priority (strip lowest-value first):
//
//	Phase 1: system blocks earliest-first, preserving the last one.
//	Phase 2: tool blocks earliest-first, preserving the last one.
//	Phase 3: message content blocks earliest-first.
//	Phase 4: remaining system blocks (last system).
//	Phase 5: remaining tool blocks (last tool).
func enforceCacheControlLimit(payload []byte, maxBlocks int) []byte {
	if len(payload) == 0 || !gjson.ValidBytes(payload) {
		return payload
	}

	total := countCacheControls(payload)
	if total <= maxBlocks {
		return payload
	}

	excess := total - maxBlocks

	system := gjson.GetBytes(payload, "system")
	if system.IsArray() {
		lastIdx := -1
		system.ForEach(func(idx, item gjson.Result) bool {
			if item.Get("cache_control").Exists() {
				lastIdx = int(idx.Int())
			}
			return true
		})
		if lastIdx >= 0 {
			system.ForEach(func(idx, item gjson.Result) bool {
				if excess <= 0 {
					return false
				}
				i := int(idx.Int())
				if i == lastIdx {
					return true
				}
				if !item.Get("cache_control").Exists() {
					return true
				}
				path := fmt.Sprintf("system.%d.cache_control", i)
				updated, errDel := sjson.DeleteBytes(payload, path)
				if errDel != nil {
					return true
				}
				payload = updated
				excess--
				return true
			})
		}
	}
	if excess <= 0 {
		return payload
	}

	tools := gjson.GetBytes(payload, "tools")
	if tools.IsArray() {
		lastIdx := -1
		tools.ForEach(func(idx, item gjson.Result) bool {
			if item.Get("cache_control").Exists() {
				lastIdx = int(idx.Int())
			}
			return true
		})
		if lastIdx >= 0 {
			tools.ForEach(func(idx, item gjson.Result) bool {
				if excess <= 0 {
					return false
				}
				i := int(idx.Int())
				if i == lastIdx {
					return true
				}
				if !item.Get("cache_control").Exists() {
					return true
				}
				path := fmt.Sprintf("tools.%d.cache_control", i)
				updated, errDel := sjson.DeleteBytes(payload, path)
				if errDel != nil {
					return true
				}
				payload = updated
				excess--
				return true
			})
		}
	}
	if excess <= 0 {
		return payload
	}

	messages := gjson.GetBytes(payload, "messages")
	if messages.IsArray() {
		messages.ForEach(func(msgIdx, msg gjson.Result) bool {
			if excess <= 0 {
				return false
			}
			content := msg.Get("content")
			if !content.IsArray() {
				return true
			}
			content.ForEach(func(itemIdx, item gjson.Result) bool {
				if excess <= 0 {
					return false
				}
				if !item.Get("cache_control").Exists() {
					return true
				}
				path := fmt.Sprintf("messages.%d.content.%d.cache_control", int(msgIdx.Int()), int(itemIdx.Int()))
				updated, errDel := sjson.DeleteBytes(payload, path)
				if errDel != nil {
					return true
				}
				payload = updated
				excess--
				return true
			})
			return true
		})
	}
	if excess <= 0 {
		return payload
	}

	system = gjson.GetBytes(payload, "system")
	if system.IsArray() {
		system.ForEach(func(idx, item gjson.Result) bool {
			if excess <= 0 {
				return false
			}
			if !item.Get("cache_control").Exists() {
				return true
			}
			path := fmt.Sprintf("system.%d.cache_control", int(idx.Int()))
			updated, errDel := sjson.DeleteBytes(payload, path)
			if errDel != nil {
				return true
			}
			payload = updated
			excess--
			return true
		})
	}
	if excess <= 0 {
		return payload
	}

	tools = gjson.GetBytes(payload, "tools")
	if tools.IsArray() {
		tools.ForEach(func(idx, item gjson.Result) bool {
			if excess <= 0 {
				return false
			}
			if !item.Get("cache_control").Exists() {
				return true
			}
			path := fmt.Sprintf("tools.%d.cache_control", int(idx.Int()))
			updated, errDel := sjson.DeleteBytes(payload, path)
			if errDel != nil {
				return true
			}
			payload = updated
			excess--
			return true
		})
	}

	return payload
}

// injectMessagesCacheControl adds cache_control to the second-to-last user turn for multi-turn caching.
// Per Anthropic docs: "Place cache_control on the second-to-last User message to let the model reuse the earlier cache."
// This enables caching of conversation history, which is especially beneficial for long multi-turn conversations.
// Only adds cache_control if:
// - There are at least 2 user turns in the conversation
// - No message content already has cache_control
func injectMessagesCacheControl(payload []byte) []byte {
	messages := gjson.GetBytes(payload, "messages")
	if !messages.Exists() || !messages.IsArray() {
		return payload
	}

	// Check if ANY message content already has cache_control
	hasCacheControlInMessages := false
	messages.ForEach(func(_, msg gjson.Result) bool {
		content := msg.Get("content")
		if content.IsArray() {
			content.ForEach(func(_, item gjson.Result) bool {
				if item.Get("cache_control").Exists() {
					hasCacheControlInMessages = true
					return false
				}
				return true
			})
		}
		return !hasCacheControlInMessages
	})
	if hasCacheControlInMessages {
		return payload
	}

	// Find all user message indices
	var userMsgIndices []int
	messages.ForEach(func(index gjson.Result, msg gjson.Result) bool {
		if msg.Get("role").String() == "user" {
			userMsgIndices = append(userMsgIndices, int(index.Int()))
		}
		return true
	})

	// Need at least 2 user turns to cache the second-to-last
	if len(userMsgIndices) < 2 {
		return payload
	}

	// Get the second-to-last user message index
	secondToLastUserIdx := userMsgIndices[len(userMsgIndices)-2]

	// Get the content of this message
	contentPath := fmt.Sprintf("messages.%d.content", secondToLastUserIdx)
	content := gjson.GetBytes(payload, contentPath)

	if content.IsArray() {
		// Add cache_control to the last content block of this message
		contentCount := int(content.Get("#").Int())
		if contentCount > 0 {
			cacheControlPath := fmt.Sprintf("messages.%d.content.%d.cache_control", secondToLastUserIdx, contentCount-1)
			result, err := sjson.SetBytes(payload, cacheControlPath, map[string]string{"type": "ephemeral"})
			if err != nil {
				log.Warnf("failed to inject cache_control into messages: %v", err)
				return payload
			}
			payload = result
		}
	} else if content.Type == gjson.String {
		// Convert string content to array with cache_control
		text := content.String()
		newContent := []map[string]interface{}{
			{
				"type": "text",
				"text": text,
				"cache_control": map[string]string{
					"type": "ephemeral",
				},
			},
		}
		result, err := sjson.SetBytes(payload, contentPath, newContent)
		if err != nil {
			log.Warnf("failed to inject cache_control into message string content: %v", err)
			return payload
		}
		payload = result
	}

	return payload
}

// injectToolsCacheControl adds cache_control to the last tool in the tools array.
// Per Anthropic docs: "The cache_control parameter on the last tool definition caches all tool definitions."
// This only adds cache_control if NO tool in the array already has it.
func injectToolsCacheControl(payload []byte) []byte {
	tools := gjson.GetBytes(payload, "tools")
	if !tools.Exists() || !tools.IsArray() {
		return payload
	}

	toolCount := int(tools.Get("#").Int())
	if toolCount == 0 {
		return payload
	}

	// Check if ANY tool already has cache_control - if so, don't modify tools
	hasCacheControlInTools := false
	tools.ForEach(func(_, tool gjson.Result) bool {
		if tool.Get("cache_control").Exists() {
			hasCacheControlInTools = true
			return false
		}
		return true
	})
	if hasCacheControlInTools {
		return payload
	}

	// Add cache_control to the last tool
	lastToolPath := fmt.Sprintf("tools.%d.cache_control", toolCount-1)
	result, err := sjson.SetBytes(payload, lastToolPath, map[string]string{"type": "ephemeral"})
	if err != nil {
		log.Warnf("failed to inject cache_control into tools array: %v", err)
		return payload
	}

	return result
}

// injectSystemCacheControl adds cache_control to the last element in the system prompt.
// Converts string system prompts to array format if needed.
// This only adds cache_control if NO system element already has it.
func injectSystemCacheControl(payload []byte) []byte {
	system := gjson.GetBytes(payload, "system")
	if !system.Exists() {
		return payload
	}

	if system.IsArray() {
		count := int(system.Get("#").Int())
		if count == 0 {
			return payload
		}

		// Check if ANY system element already has cache_control
		hasCacheControlInSystem := false
		system.ForEach(func(_, item gjson.Result) bool {
			if item.Get("cache_control").Exists() {
				hasCacheControlInSystem = true
				return false
			}
			return true
		})
		if hasCacheControlInSystem {
			return payload
		}

		// Add cache_control to the last system element
		lastSystemPath := fmt.Sprintf("system.%d.cache_control", count-1)
		result, err := sjson.SetBytes(payload, lastSystemPath, map[string]string{"type": "ephemeral"})
		if err != nil {
			log.Warnf("failed to inject cache_control into system array: %v", err)
			return payload
		}
		payload = result
	} else if system.Type == gjson.String {
		// Convert string system prompt to array with cache_control
		// "system": "text" -> "system": [{"type": "text", "text": "text", "cache_control": {"type": "ephemeral"}}]
		text := system.String()
		newSystem := []map[string]interface{}{
			{
				"type": "text",
				"text": text,
				"cache_control": map[string]string{
					"type": "ephemeral",
				},
			},
		}
		result, err := sjson.SetBytes(payload, "system", newSystem)
		if err != nil {
			log.Warnf("failed to inject cache_control into system string: %v", err)
			return payload
		}
		payload = result
	}

	return payload
}

func ensureModelMaxTokens(body []byte, modelID string) []byte {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return body
	}

	if maxTokens := gjson.GetBytes(body, "max_tokens"); maxTokens.Exists() {
		return body
	}

	for _, provider := range registry.GetGlobalRegistry().GetModelProviders(strings.TrimSpace(modelID)) {
		if strings.EqualFold(provider, "claude") {
			maxTokens := defaultModelMaxTokens
			if info := registry.GetGlobalRegistry().GetModelInfo(strings.TrimSpace(modelID), "claude"); info != nil && info.MaxCompletionTokens > 0 {
				maxTokens = info.MaxCompletionTokens
			}
			body, _ = sjson.SetBytes(body, "max_tokens", maxTokens)
			return body
		}
	}

	return body
}
