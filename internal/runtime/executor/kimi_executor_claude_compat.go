package executor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	kimiauth "github.com/router-for-me/CLIProxyAPI/v6/internal/auth/kimi"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Kimi Claude compatibility constants
const (
	// Thinking type constants
	thinkingTypeAdaptive = "adaptive"
	thinkingTypeAuto     = "auto"
	thinkingTypeEnabled  = "enabled"

	// Effort level constants
	effortMinimal = "minimal"
	effortNone    = "none"
	effortLow     = "low"
	effortMedium  = "medium"
	effortHigh    = "high"
	effortMax     = "max"
	effortXHigh   = "xhigh"

	// Thinking budget token constants
	budgetMinimal = 0
	budgetLow     = 1024
	budgetMedium  = 4096
	budgetHigh    = 8192

	// Kimi endpoint identifier
	kimiAPIHost = "api.kimi.com"

	// Beta header values
	betaHeaderBase = "claude-code-20250219"
	betaHeaderFull = "claude-code-20250219,interleaved-thinking-2025-05-14"
)

// isKimiClaudeCompatBaseURL checks if the given base URL points to Kimi's Anthropic-compatible endpoint.
func isKimiClaudeCompatBaseURL(baseURL string) bool {
	normalized := strings.ToLower(strings.TrimSpace(baseURL))
	parsed, err := url.Parse(normalized)
	if err != nil {
		return strings.Contains(normalized, kimiAPIHost)
	}
	return strings.Contains(parsed.Host, kimiAPIHost)
}

// applyKimiClaudeBetaHeader sets the Anthropic-Beta header appropriate for Kimi's compatibility layer.
func applyKimiClaudeBetaHeader(req *http.Request, strict bool) {
	if req == nil {
		return
	}
	if strict {
		req.Header.Set("Anthropic-Beta", betaHeaderBase)
		return
	}
	req.Header.Set("Anthropic-Beta", betaHeaderFull)
}

// isRetryableKimiInvalidRequest determines if a 400 response from Kimi indicates a retryable error.
func isRetryableKimiInvalidRequest(statusCode int, responseBody []byte) bool {
	if statusCode != http.StatusBadRequest {
		return false
	}
	lower := strings.ToLower(strings.TrimSpace(string(responseBody)))
	if lower == "" {
		return false
	}
	return strings.Contains(lower, "invalid_request_error") ||
		strings.Contains(lower, "invalid request error")
}

// applyKimiClaudeCompatibility applies all necessary transformations to make a Claude request
// compatible with Kimi's Anthropic-compatible endpoint.
func applyKimiClaudeCompatibility(body []byte, strict bool) []byte {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return body
	}
	out := stripKimiIncompatibleFields(body, strict)
	out = stripToolReferences(out)
	return out
}

// stripKimiIncompatibleFields removes or transforms fields that Kimi's endpoint cannot handle.
func stripKimiIncompatibleFields(body []byte, strict bool) []byte {
	out := body
	out, _ = sjson.DeleteBytes(out, "metadata")
	out, _ = sjson.DeleteBytes(out, "context_management")

	thinkingType := strings.ToLower(strings.TrimSpace(gjson.GetBytes(out, "thinking.type").String()))
	if thinkingType == thinkingTypeAdaptive || thinkingType == thinkingTypeAuto {
		effort := strings.ToLower(strings.TrimSpace(gjson.GetBytes(out, "output_config.effort").String()))
		budget := budgetMedium
		switch effort {
		case effortMinimal, effortNone:
			budget = budgetMinimal
		case effortLow:
			budget = budgetLow
		case effortHigh, effortMax, effortXHigh:
			budget = budgetHigh
		}
		if budget <= 0 {
			out, _ = sjson.DeleteBytes(out, "thinking")
		} else {
			out, _ = sjson.SetBytes(out, "thinking.type", thinkingTypeEnabled)
			out, _ = sjson.SetBytes(out, "thinking.budget_tokens", budget)
		}
		out, _ = sjson.DeleteBytes(out, "output_config.effort")
	}
	if strict {
		out, _ = sjson.DeleteBytes(out, "output_config.effort")
	}
	if oc := gjson.GetBytes(out, "output_config"); oc.Exists() && oc.IsObject() && len(oc.Map()) == 0 {
		out, _ = sjson.DeleteBytes(out, "output_config")
	}
	return out
}

// stripToolReferences removes tool_reference content blocks from tool_result messages.
func stripToolReferences(body []byte) []byte {
	messages := gjson.GetBytes(body, "messages")
	if !messages.IsArray() {
		return body
	}

	out := body
	messages.ForEach(func(mi, msg gjson.Result) bool {
		content := msg.Get("content")
		if !content.IsArray() {
			return true
		}
		var modified bool
		newBlocks := make([]interface{}, 0, len(content.Array()))
		for _, block := range content.Array() {
			if block.Get("type").String() != "tool_result" {
				newBlocks = append(newBlocks, block.Value())
				continue
			}
			inner := block.Get("content")
			if !inner.IsArray() {
				newBlocks = append(newBlocks, block.Value())
				continue
			}
			innerArr := inner.Array()
			filtered := make([]interface{}, 0, len(innerArr))
			innerModified := false
			for _, ib := range innerArr {
				if ib.Get("type").String() == "tool_reference" {
					innerModified = true
					continue
				}
				filtered = append(filtered, ib.Value())
			}
			if !innerModified {
				newBlocks = append(newBlocks, block.Value())
				continue
			}
			modified = true
			bm, ok := block.Value().(map[string]interface{})
			if !ok {
				newBlocks = append(newBlocks, block.Value())
				continue
			}
			bm["content"] = filtered
			newBlocks = append(newBlocks, bm)
		}
		if modified {
			if b, err := json.Marshal(newBlocks); err == nil {
				out, _ = sjson.SetRawBytes(out, fmt.Sprintf("messages.%d.content", mi.Int()), b)
			} else {
				log.Warnf("failed to strip tool_reference from message %d: %v", mi.Int(), err)
			}
		}
		return true
	})
	return out
}

// buildKimiClaudeMessagesBody constructs the request bodies for Kimi's Claude-compatible endpoint.
// It wraps ClaudeExecutor.buildMessagesBody and then applies Kimi-specific compatibility transforms.
func buildKimiClaudeMessagesBody(
	e *ClaudeExecutor,
	ctx context.Context,
	auth *cliproxyauth.Auth,
	req cliproxyexecutor.Request,
	opts cliproxyexecutor.Options,
	baseModel, apiKey string,
) (bodyForTranslation, bodyForUpstream []byte, extraBetas []string, oauthToolNamesRemapped bool, err error) {
	bodyForTranslation, bodyForUpstream, extraBetas, oauthToolNamesRemapped, err = e.buildMessagesBody(ctx, auth, req, opts, baseModel, apiKey)
	if err != nil {
		return nil, nil, nil, false, err
	}
	bodyForUpstream = applyKimiClaudeCompatibility(bodyForUpstream, false)
	bodyForTranslation = applyKimiClaudeCompatibility(bodyForTranslation, false)
	return bodyForTranslation, bodyForUpstream, extraBetas, oauthToolNamesRemapped, nil
}

// tryKimiCompatRetry encapsulates the Kimi compatibility retry logic.
func tryKimiCompatRetry(
	ctx context.Context,
	statusCode int,
	respBody []byte,
	sendUpstream func([]byte) (*http.Response, error),
	bodyForUpstream, bodyForTranslation *[]byte,
) (*http.Response, error) {
	if !isRetryableKimiInvalidRequest(statusCode, respBody) {
		return nil, nil
	}

	log.Debugf("Kimi returned retryable 400, attempting strict-mode retry")
	strictBody := applyKimiClaudeCompatibility(*bodyForUpstream, true)
	*bodyForUpstream = strictBody
	*bodyForTranslation = strictBody

	retryResp, retryErr := sendUpstream(strictBody)
	if retryErr != nil {
		log.Warnf("Kimi strict-mode retry failed: %v", retryErr)
		return nil, retryErr
	}
	return retryResp, nil
}

// executeKimiClaudeNonStream performs a non-streaming Claude-format request through Kimi's compatible endpoint.
func executeKimiClaudeNonStream(
	ctx context.Context,
	cfg *config.Config,
	auth *cliproxyauth.Auth,
	req cliproxyexecutor.Request,
	opts cliproxyexecutor.Options,
	claudeExec *ClaudeExecutor,
) (cliproxyexecutor.Response, error) {
	var resp cliproxyexecutor.Response
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	apiKey, _ := claudeCreds(auth)
	baseURL := kimiauth.KimiAPIBaseURL

	reporter := helps.NewUsageReporter(ctx, "kimi", baseModel, auth)
	var err error
	defer reporter.TrackFailure(ctx, &err)

	from := opts.SourceFormat
	to := sdktranslator.FromString("claude")
	stream := from != to

	bodyForTranslation, bodyForUpstream, extraBetas, oauthToolNamesRemapped, err := buildKimiClaudeMessagesBody(claudeExec, ctx, auth, req, opts, baseModel, apiKey)
	if err != nil {
		return resp, err
	}

	url := fmt.Sprintf("%s/v1/messages?beta=true", baseURL)
	var authID, authLabel, authType, authValue string
	if auth != nil {
		authID = auth.ID
		authLabel = auth.Label
		authType, authValue = auth.AccountInfo()
	}
	httpClient := helps.NewUtlsHTTPClient(cfg, auth, 0)

	sendUpstream := func(body []byte) (*http.Response, error) {
		httpReq, reqErr := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if reqErr != nil {
			return nil, reqErr
		}
		applyClaudeHeaders(httpReq, auth, apiKey, false, extraBetas, cfg)
		applyKimiClaudeBetaHeader(httpReq, false)
		helps.RecordAPIRequest(ctx, cfg, helps.UpstreamRequestLog{
			URL:       url,
			Method:    http.MethodPost,
			Headers:   httpReq.Header.Clone(),
			Body:      body,
			Provider:  "kimi",
			AuthID:    authID,
			AuthLabel: authLabel,
			AuthType:  authType,
			AuthValue: authValue,
		})
		return httpClient.Do(httpReq)
	}

	httpResp, err := sendUpstream(bodyForUpstream)
	if err != nil {
		helps.RecordAPIResponseError(ctx, cfg, err)
		return resp, err
	}
	helps.RecordAPIResponseMetadata(ctx, cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		errBody, decErr := decodeResponseBody(httpResp.Body, httpResp.Header.Get("Content-Encoding"))
		if decErr != nil {
			helps.RecordAPIResponseError(ctx, cfg, decErr)
			msg := fmt.Sprintf("failed to decode error response body: %v", decErr)
			helps.LogWithRequestID(ctx).Warn(msg)
			return resp, statusErr{code: httpResp.StatusCode, msg: msg}
		}
		b, readErr := io.ReadAll(errBody)
		if readErr != nil {
			helps.RecordAPIResponseError(ctx, cfg, readErr)
			msg := fmt.Sprintf("failed to read error response body: %v", readErr)
			helps.LogWithRequestID(ctx).Warn(msg)
			b = []byte(msg)
		}
		helps.AppendAPIResponseChunk(ctx, cfg, b)
		helps.LogWithRequestID(ctx).Debugf("request error, error status: %d, error message: %s", httpResp.StatusCode, helps.SummarizeErrorBody(httpResp.Header.Get("Content-Type"), b))
		if errClose := errBody.Close(); errClose != nil {
			log.Errorf("response body close error: %v", errClose)
		}

		retryResp, retryErr := tryKimiCompatRetry(ctx, httpResp.StatusCode, b, sendUpstream, &bodyForUpstream, &bodyForTranslation)
		if retryErr != nil {
			return resp, retryErr
		}
		if retryResp == nil {
			return resp, statusErr{code: httpResp.StatusCode, msg: string(b)}
		}
		httpResp = retryResp
	}
	decodedBody, err := decodeResponseBody(httpResp.Body, httpResp.Header.Get("Content-Encoding"))
	if err != nil {
		helps.RecordAPIResponseError(ctx, cfg, err)
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
		helps.RecordAPIResponseError(ctx, cfg, err)
		return resp, err
	}
	helps.AppendAPIResponseChunk(ctx, cfg, data)
	if stream {
		lines := bytes.Split(data, []byte("\n"))
		for _, line := range lines {
			if detail, ok := helps.ParseClaudeStreamUsage(line); ok {
				reporter.Publish(ctx, detail)
			}
		}
	} else {
		reporter.Publish(ctx, helps.ParseClaudeUsage(data))
	}
	if isClaudeOAuthToken(apiKey) && !auth.ToolPrefixDisabled() {
		data = stripClaudeToolPrefixFromResponse(data, claudeToolPrefix)
	}
	if isClaudeOAuthToken(apiKey) && oauthToolNamesRemapped {
		data = reverseRemapOAuthToolNames(data)
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

// executeKimiClaudeStream performs a streaming Claude-format request through Kimi's compatible endpoint.
func executeKimiClaudeStream(
	ctx context.Context,
	cfg *config.Config,
	auth *cliproxyauth.Auth,
	req cliproxyexecutor.Request,
	opts cliproxyexecutor.Options,
	claudeExec *ClaudeExecutor,
) (*cliproxyexecutor.StreamResult, error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	apiKey, _ := claudeCreds(auth)
	baseURL := kimiauth.KimiAPIBaseURL

	reporter := helps.NewUsageReporter(ctx, "kimi", baseModel, auth)
	var err error
	defer reporter.TrackFailure(ctx, &err)

	from := opts.SourceFormat
	to := sdktranslator.FromString("claude")

	bodyForTranslation, bodyForUpstream, extraBetas, oauthToolNamesRemapped, err := buildKimiClaudeMessagesBody(claudeExec, ctx, auth, req, opts, baseModel, apiKey)
	if err != nil {
		return nil, err
	}

	url := fmt.Sprintf("%s/v1/messages?beta=true", baseURL)
	var authID, authLabel, authType, authValue string
	if auth != nil {
		authID = auth.ID
		authLabel = auth.Label
		authType, authValue = auth.AccountInfo()
	}
	httpClient := helps.NewUtlsHTTPClient(cfg, auth, 0)

	sendUpstream := func(body []byte) (*http.Response, error) {
		httpReq, reqErr := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if reqErr != nil {
			return nil, reqErr
		}
		applyClaudeHeaders(httpReq, auth, apiKey, true, extraBetas, cfg)
		applyKimiClaudeBetaHeader(httpReq, false)
		helps.RecordAPIRequest(ctx, cfg, helps.UpstreamRequestLog{
			URL:       url,
			Method:    http.MethodPost,
			Headers:   httpReq.Header.Clone(),
			Body:      body,
			Provider:  "kimi",
			AuthID:    authID,
			AuthLabel: authLabel,
			AuthType:  authType,
			AuthValue: authValue,
		})
		return httpClient.Do(httpReq)
	}

	httpResp, err := sendUpstream(bodyForUpstream)
	if err != nil {
		helps.RecordAPIResponseError(ctx, cfg, err)
		return nil, err
	}
	helps.RecordAPIResponseMetadata(ctx, cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		errBody, decErr := decodeResponseBody(httpResp.Body, httpResp.Header.Get("Content-Encoding"))
		if decErr != nil {
			helps.RecordAPIResponseError(ctx, cfg, decErr)
			msg := fmt.Sprintf("failed to decode error response body: %v", decErr)
			helps.LogWithRequestID(ctx).Warn(msg)
			return nil, statusErr{code: httpResp.StatusCode, msg: msg}
		}
		b, readErr := io.ReadAll(errBody)
		if readErr != nil {
			helps.RecordAPIResponseError(ctx, cfg, readErr)
			msg := fmt.Sprintf("failed to read error response body: %v", readErr)
			helps.LogWithRequestID(ctx).Warn(msg)
			b = []byte(msg)
		}
		helps.AppendAPIResponseChunk(ctx, cfg, b)
		helps.LogWithRequestID(ctx).Debugf("request error, error status: %d, error message: %s", httpResp.StatusCode, helps.SummarizeErrorBody(httpResp.Header.Get("Content-Type"), b))
		if errClose := errBody.Close(); errClose != nil {
			log.Errorf("response body close error: %v", errClose)
		}

		retryResp, retryErr := tryKimiCompatRetry(ctx, httpResp.StatusCode, b, sendUpstream, &bodyForUpstream, &bodyForTranslation)
		if retryErr != nil {
			return nil, retryErr
		}
		if retryResp == nil {
			return nil, statusErr{code: httpResp.StatusCode, msg: string(b)}
		}
		httpResp = retryResp
	}
	decodedBody, err := decodeResponseBody(httpResp.Body, httpResp.Header.Get("Content-Encoding"))
	if err != nil {
		helps.RecordAPIResponseError(ctx, cfg, err)
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

		if from == to {
			scanner := bufio.NewScanner(decodedBody)
			scanner.Buffer(nil, 52_428_800)
			for scanner.Scan() {
				line := scanner.Bytes()
				helps.AppendAPIResponseChunk(ctx, cfg, line)
				if detail, ok := helps.ParseClaudeStreamUsage(line); ok {
					reporter.Publish(ctx, detail)
				}
				if isClaudeOAuthToken(apiKey) && !auth.ToolPrefixDisabled() {
					line = stripClaudeToolPrefixFromStreamLine(line, claudeToolPrefix)
				}
				if isClaudeOAuthToken(apiKey) && oauthToolNamesRemapped {
					line = reverseRemapOAuthToolNamesFromStreamLine(line)
				}
				cloned := make([]byte, len(line)+1)
				copy(cloned, line)
				cloned[len(line)] = '\n'
				out <- cliproxyexecutor.StreamChunk{Payload: cloned}
			}
			if errScan := scanner.Err(); errScan != nil {
				helps.RecordAPIResponseError(ctx, cfg, errScan)
				reporter.PublishFailure(ctx)
				out <- cliproxyexecutor.StreamChunk{Err: errScan}
			}
			return
		}

		scanner := bufio.NewScanner(decodedBody)
		scanner.Buffer(nil, 52_428_800)
		var param any
		for scanner.Scan() {
			line := scanner.Bytes()
			helps.AppendAPIResponseChunk(ctx, cfg, line)
			if detail, ok := helps.ParseClaudeStreamUsage(line); ok {
				reporter.Publish(ctx, detail)
			}
			if isClaudeOAuthToken(apiKey) && !auth.ToolPrefixDisabled() {
				line = stripClaudeToolPrefixFromStreamLine(line, claudeToolPrefix)
			}
			if isClaudeOAuthToken(apiKey) && oauthToolNamesRemapped {
				line = reverseRemapOAuthToolNamesFromStreamLine(line)
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
				out <- cliproxyexecutor.StreamChunk{Payload: chunks[i]}
			}
		}
		if errScan := scanner.Err(); errScan != nil {
			helps.RecordAPIResponseError(ctx, cfg, errScan)
			reporter.PublishFailure(ctx)
			out <- cliproxyexecutor.StreamChunk{Err: errScan}
		}
	}()
	return &cliproxyexecutor.StreamResult{Headers: httpResp.Header.Clone(), Chunks: out}, nil
}
