package executor

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	kimiauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/kimi"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// KimiExecutor is a stateless executor for Kimi API using OpenAI-compatible chat completions.
type KimiExecutor struct {
	ClaudeExecutor
	cfg *config.Config
}

// NewKimiExecutor creates a new Kimi executor.
func NewKimiExecutor(cfg *config.Config) *KimiExecutor { return &KimiExecutor{cfg: cfg} }

// Identifier returns the executor identifier.
func (e *KimiExecutor) Identifier() string { return "kimi" }

// PrepareRequest injects Kimi credentials into the outgoing HTTP request.
func (e *KimiExecutor) PrepareRequest(req *http.Request, auth *cliproxyauth.Auth) error {
	if req == nil {
		return nil
	}
	token := kimiCreds(auth)
	if strings.TrimSpace(token) != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(req, attrs)
	return nil
}

// HttpRequest injects Kimi credentials into the request and executes it.
func (e *KimiExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("kimi executor: request is nil")
	}
	if ctx == nil {
		ctx = req.Context()
	}
	httpReq := req.WithContext(ctx)
	if err := sanitizeKimiHTTPRequestBody(httpReq); err != nil {
		return nil, err
	}
	if err := e.PrepareRequest(httpReq, auth); err != nil {
		return nil, err
	}
	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	return httpClient.Do(httpReq)
}

func sanitizeKimiHTTPRequestBody(req *http.Request) error {
	if req == nil || req.Body == nil || req.Body == http.NoBody {
		return nil
	}
	body, errRead := io.ReadAll(req.Body)
	if errRead != nil {
		return errRead
	}
	if errClose := req.Body.Close(); errClose != nil {
		log.Errorf("kimi executor: request body close error: %v", errClose)
	}
	updated, err := sanitizeKimiOpenAICompatibleRequestBody(body)
	if err != nil {
		return err
	}
	req.Body = io.NopCloser(bytes.NewReader(updated))
	req.ContentLength = int64(len(updated))
	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(updated)), nil
	}
	if req.Header != nil {
		req.Header.Set("Content-Length", fmt.Sprintf("%d", len(updated)))
	}
	return nil
}

func resolveKimiBaseURL(auth *cliproxyauth.Auth) string {
	baseURL := kimiauth.KimiAPIBaseURL
	if auth != nil && auth.Attributes != nil {
		if configured := strings.TrimSpace(auth.Attributes["base_url"]); configured != "" {
			baseURL = configured
		}
	}
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if strings.HasSuffix(strings.ToLower(baseURL), "/v1") {
		baseURL = baseURL[:len(baseURL)-3]
	}
	if baseURL == "" {
		return kimiauth.KimiAPIBaseURL
	}
	return baseURL
}

// Execute performs a non-streaming chat completion request to Kimi.
func (e *KimiExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	from := opts.SourceFormat
	if from.String() == "claude" {
		req, opts, err = repairKimiClaudeToolUseRequest(req, opts)
		if err != nil {
			return resp, err
		}
	}

	baseModel := thinking.ParseSuffix(req.Model).ModelName
	profile := openAICompatProfileForKind("kimi")
	baseURL := resolveKimiBaseURL(auth)

	token := kimiCreds(auth)

	reporter := helps.NewUsageReporter(ctx, e.Identifier(), baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	to := sdktranslator.FromString("openai")
	originalPayloadSource := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayloadSource = opts.OriginalRequest
	}
	if from.String() == "claude" {
		originalPayloadSource = downgradeClaudeToolSearchForCompat(baseURL, originalPayloadSource)
		req.Payload = downgradeClaudeToolSearchForCompat(baseURL, req.Payload)
	}
	originalPayload := bytes.Clone(originalPayloadSource)
	originalTranslated := sdktranslator.TranslateRequest(from, to, baseModel, originalPayload, false)
	body := sdktranslator.TranslateRequest(from, to, baseModel, bytes.Clone(req.Payload), false)

	// Strip kimi- prefix for upstream API
	upstreamModel := stripKimiPrefix(baseModel)
	body, err = sjson.SetBytes(body, "model", upstreamModel)
	if err != nil {
		return resp, fmt.Errorf("kimi executor: failed to set model in payload: %w", err)
	}

	body, err = thinking.ApplyThinking(body, req.Model, from.String(), "kimi", e.Identifier())
	if err != nil {
		return resp, err
	}

	requestedModel := helps.PayloadRequestedModel(opts, req.Model)
	requestPath := helps.PayloadRequestPath(opts)
	body = helps.ApplyPayloadConfigWithRequest(e.cfg, baseModel, to.String(), from.String(), "", body, originalTranslated, requestedModel, requestPath, opts.Headers)
	body, err = sanitizeKimiOpenAICompatibleRequestBody(body)
	if err != nil {
		return resp, err
	}

	url := strings.TrimSuffix(baseURL, "/") + "/v1/chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return resp, err
	}
	applyKimiHeadersWithAuth(httpReq, token, false, auth)
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(httpReq, attrs)
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

	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	defer func() {
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("kimi executor: close response body error: %v", errClose)
		}
	}()
	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		b, _ := io.ReadAll(httpResp.Body)
		helps.AppendAPIResponseChunk(ctx, e.cfg, b)
		helps.LogWithRequestID(ctx).Debugf("request error, error status: %d, error message: %s", httpResp.StatusCode, helps.SummarizeErrorBody(httpResp.Header.Get("Content-Type"), b))
		err = newOpenAICompatStatusErr(profile, auth, req.Model, httpResp.StatusCode, httpResp.Header, httpResp.Header.Get("Content-Type"), b)
		return resp, err
	}
	data, err := io.ReadAll(httpResp.Body)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	helps.AppendAPIResponseChunk(ctx, e.cfg, data)
	reporter.Publish(ctx, helps.ParseOpenAIUsage(data))
	var param any
	// Note: TranslateNonStream uses req.Model (original with suffix) to preserve
	// the original model name in the response for client compatibility.
	out := sdktranslator.TranslateNonStream(ctx, to, from, req.Model, opts.OriginalRequest, body, data, &param)
	resp = cliproxyexecutor.Response{Payload: out, Headers: httpResp.Header.Clone()}
	return resp, nil
}

// ExecuteStream performs a streaming chat completion request to Kimi.
func (e *KimiExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	from := opts.SourceFormat
	if from.String() == "claude" {
		req, opts, err = repairKimiClaudeToolUseRequest(req, opts)
		if err != nil {
			return nil, err
		}
	}

	baseModel := thinking.ParseSuffix(req.Model).ModelName
	profile := openAICompatProfileForKind("kimi")
	baseURL := resolveKimiBaseURL(auth)
	token := kimiCreds(auth)

	reporter := helps.NewUsageReporter(ctx, e.Identifier(), baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	to := sdktranslator.FromString("openai")
	originalPayloadSource := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayloadSource = opts.OriginalRequest
	}
	originalPayload := bytes.Clone(originalPayloadSource)
	if from.String() == "claude" {
		originalPayload = downgradeClaudeToolSearchForCompat(baseURL, originalPayload)
		req.Payload = downgradeClaudeToolSearchForCompat(baseURL, req.Payload)
	}
	originalTranslated := sdktranslator.TranslateRequest(from, to, baseModel, originalPayload, true)
	body := sdktranslator.TranslateRequest(from, to, baseModel, bytes.Clone(req.Payload), true)

	// Strip kimi- prefix for upstream API
	upstreamModel := stripKimiPrefix(baseModel)
	body, err = sjson.SetBytes(body, "model", upstreamModel)
	if err != nil {
		return nil, fmt.Errorf("kimi executor: failed to set model in payload: %w", err)
	}

	body, err = thinking.ApplyThinking(body, req.Model, from.String(), "kimi", e.Identifier())
	if err != nil {
		return nil, err
	}

	body, err = sjson.SetBytes(body, "stream_options.include_usage", true)
	if err != nil {
		return nil, fmt.Errorf("kimi executor: failed to set stream_options in payload: %w", err)
	}
	requestedModel := helps.PayloadRequestedModel(opts, req.Model)
	requestPath := helps.PayloadRequestPath(opts)
	body = helps.ApplyPayloadConfigWithRequest(e.cfg, baseModel, to.String(), from.String(), "", body, originalTranslated, requestedModel, requestPath, opts.Headers)
	body, err = sanitizeKimiOpenAICompatibleRequestBody(body)
	if err != nil {
		return nil, err
	}

	url := strings.TrimSuffix(baseURL, "/") + "/v1/chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	applyKimiHeadersWithAuth(httpReq, token, true, auth)
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(httpReq, attrs)
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

	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return nil, err
	}
	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		b, _ := io.ReadAll(httpResp.Body)
		helps.AppendAPIResponseChunk(ctx, e.cfg, b)
		helps.LogWithRequestID(ctx).Debugf("request error, error status: %d, error message: %s", httpResp.StatusCode, helps.SummarizeErrorBody(httpResp.Header.Get("Content-Type"), b))
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("kimi executor: close response body error: %v", errClose)
		}
		err = newOpenAICompatStatusErr(profile, auth, req.Model, httpResp.StatusCode, httpResp.Header, httpResp.Header.Get("Content-Type"), b)
		return nil, err
	}
	out := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		defer close(out)
		defer func() {
			if errClose := httpResp.Body.Close(); errClose != nil {
				log.Errorf("kimi executor: close response body error: %v", errClose)
			}
		}()
		scanner := bufio.NewScanner(httpResp.Body)
		scanner.Buffer(nil, 1_048_576) // 1MB
		var param any
		for scanner.Scan() {
			line := scanner.Bytes()
			helps.AppendAPIResponseChunk(ctx, e.cfg, line)
			if detail, ok := helps.ParseOpenAIStreamUsage(line); ok {
				reporter.Publish(ctx, detail)
			}
			chunks := sdktranslator.TranslateStream(ctx, to, from, req.Model, opts.OriginalRequest, body, bytes.Clone(line), &param)
			for i := range chunks {
				select {
				case out <- cliproxyexecutor.StreamChunk{Payload: chunks[i]}:
				case <-ctx.Done():
					return
				}
			}
		}
		doneChunks := sdktranslator.TranslateStream(ctx, to, from, req.Model, opts.OriginalRequest, body, []byte("[DONE]"), &param)
		for i := range doneChunks {
			select {
			case out <- cliproxyexecutor.StreamChunk{Payload: doneChunks[i]}:
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
	}()
	return &cliproxyexecutor.StreamResult{Headers: httpResp.Header.Clone(), Chunks: out}, nil
}

// CountTokens estimates token count for Kimi requests.
func (e *KimiExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	var err error
	if opts.SourceFormat.String() == "claude" {
		req, opts, err = repairKimiClaudeToolUseRequest(req, opts)
		if err != nil {
			return cliproxyexecutor.Response{}, err
		}
	}
	authForCount := auth
	if auth != nil {
		authForCount = auth.Clone()
		if authForCount.Attributes == nil {
			authForCount.Attributes = make(map[string]string)
		}
		authForCount.Attributes["base_url"] = resolveKimiBaseURL(auth)
	}
	return e.ClaudeExecutor.CountTokens(ctx, authForCount, req, opts)
}

func repairKimiClaudeToolUseRequest(req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Request, cliproxyexecutor.Options, error) {
	repaired, err := repairClaudeToolUseHistory(req.Payload, "kimi")
	if err != nil {
		return req, opts, err
	}
	req.Payload = repaired

	if len(opts.OriginalRequest) > 0 {
		original, _, errOriginal := repairClaudeToolUseHistoryWithStats(opts.OriginalRequest)
		if errOriginal != nil {
			return req, opts, errOriginal
		}
		opts.OriginalRequest = original
	}

	return req, opts, nil
}

type claudeToolHistoryRepairStats struct {
	mergedToolResultMessages int
	dedupedToolResults       int
	reorderedToolResults     int
	removedToolUses          int
	removedToolResults       int
}

func (s claudeToolHistoryRepairStats) changed() bool {
	return s.mergedToolResultMessages > 0 ||
		s.dedupedToolResults > 0 ||
		s.reorderedToolResults > 0 ||
		s.removedToolUses > 0 ||
		s.removedToolResults > 0
}

func repairClaudeToolUseHistory(body []byte, executorName string) ([]byte, error) {
	repaired, stats, err := repairClaudeToolUseHistoryWithStats(body)
	if err != nil {
		return body, err
	}
	if stats.changed() {
		log.WithFields(log.Fields{
			"executor":                    executorName,
			"merged_tool_result_messages": stats.mergedToolResultMessages,
			"deduped_tool_results":        stats.dedupedToolResults,
			"reordered_tool_results":      stats.reorderedToolResults,
			"removed_tool_uses":           stats.removedToolUses,
			"removed_tool_results":        stats.removedToolResults,
		}).Warn("repaired Claude tool_use history")
	}
	return repaired, nil
}

func repairClaudeToolUseHistoryWithStats(body []byte) ([]byte, claudeToolHistoryRepairStats, error) {
	var stats claudeToolHistoryRepairStats
	if len(body) == 0 || !helps.HasClaudeToolUseOrResultMarkers(body) {
		return body, stats, nil
	}

	repaired, merged, err := coalesceAdjacentClaudeToolResultMessages(body)
	if err != nil {
		return body, stats, err
	}
	stats.mergedToolResultMessages = merged

	repaired, dedupedToolResults, err := helps.DedupeClaudeToolResultParts(repaired)
	if err != nil {
		return body, stats, err
	}
	stats.dedupedToolResults = dedupedToolResults

	repaired, reorderedToolResults, err := helps.MoveClaudeToolResultsBeforeUserContent(repaired)
	if err != nil {
		return body, stats, err
	}
	stats.reorderedToolResults = reorderedToolResults

	repaired, removedToolUses, err := dropUnansweredClaudeToolUses(repaired)
	if err != nil {
		return body, stats, err
	}
	stats.removedToolUses = removedToolUses

	repaired, removedToolResults, err := dropOrphanClaudeToolResults(repaired)
	if err != nil {
		return body, stats, err
	}
	stats.removedToolResults = removedToolResults

	return repaired, stats, nil
}

func coalesceAdjacentClaudeToolResultMessages(body []byte) ([]byte, int, error) {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return body, 0, nil
	}

	messages := gjson.GetBytes(body, "messages")
	if !messages.Exists() || !messages.IsArray() {
		return body, 0, nil
	}

	msgs := messages.Array()
	outMessages := []byte(`[]`)
	changed := false
	merged := 0

	for msgIdx := 0; msgIdx < len(msgs); msgIdx++ {
		msg := msgs[msgIdx]
		if strings.TrimSpace(msg.Get("role").String()) != "assistant" || !claudeMessageHasToolUse(msg) || msgIdx+1 >= len(msgs) {
			outMessages, _ = sjson.SetRawBytes(outMessages, "-1", []byte(msg.Raw))
			continue
		}

		firstResults, ok := claudeToolResultOnlyContent(msgs[msgIdx+1])
		if !ok {
			outMessages, _ = sjson.SetRawBytes(outMessages, "-1", []byte(msg.Raw))
			continue
		}

		mergedContent := []byte(`[]`)
		for _, part := range firstResults {
			mergedContent, _ = sjson.SetRawBytes(mergedContent, "-1", []byte(part.Raw))
		}

		lastResultIdx := msgIdx + 1
		for nextIdx := msgIdx + 2; nextIdx < len(msgs); nextIdx++ {
			nextResults, nextOK := claudeToolResultOnlyContent(msgs[nextIdx])
			if !nextOK {
				break
			}
			for _, part := range nextResults {
				mergedContent, _ = sjson.SetRawBytes(mergedContent, "-1", []byte(part.Raw))
			}
			lastResultIdx = nextIdx
			merged++
			changed = true
		}

		outMessages, _ = sjson.SetRawBytes(outMessages, "-1", []byte(msg.Raw))
		firstMsg, err := sjson.SetRawBytes([]byte(msgs[msgIdx+1].Raw), "content", mergedContent)
		if err != nil {
			return body, 0, fmt.Errorf("failed to merge Claude tool_result messages: %w", err)
		}
		outMessages, _ = sjson.SetRawBytes(outMessages, "-1", firstMsg)
		msgIdx = lastResultIdx
	}

	if !changed {
		return body, 0, nil
	}

	out, err := sjson.SetRawBytes(body, "messages", outMessages)
	if err != nil {
		return body, 0, fmt.Errorf("failed to update Claude messages: %w", err)
	}
	return out, merged, nil
}

func claudeMessageHasToolUse(msg gjson.Result) bool {
	content := msg.Get("content")
	if !content.IsArray() {
		return false
	}
	for _, part := range content.Array() {
		if part.Get("type").String() == "tool_use" {
			return true
		}
	}
	return false
}

func claudeToolResultOnlyContent(msg gjson.Result) ([]gjson.Result, bool) {
	if strings.TrimSpace(msg.Get("role").String()) != "user" {
		return nil, false
	}
	content := msg.Get("content")
	if !content.IsArray() {
		return nil, false
	}
	parts := content.Array()
	if len(parts) == 0 {
		return nil, false
	}
	for _, part := range parts {
		if part.Get("type").String() != "tool_result" {
			return nil, false
		}
	}
	return parts, true
}

func dropUnansweredClaudeToolUses(body []byte) ([]byte, int, error) {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return body, 0, nil
	}

	messages := gjson.GetBytes(body, "messages")
	if !messages.Exists() || !messages.IsArray() {
		return body, 0, nil
	}

	msgs := messages.Array()
	outMessages := []byte(`[]`)
	changed := false
	removed := 0

	for msgIdx, msg := range msgs {
		role := strings.TrimSpace(msg.Get("role").String())
		content := msg.Get("content")
		if role != "assistant" || !content.IsArray() {
			outMessages, _ = sjson.SetRawBytes(outMessages, "-1", []byte(msg.Raw))
			continue
		}

		nextToolResults := claudeToolResultIDsInNextUserMessage(msgs, msgIdx)
		contentOut := []byte(`[]`)
		contentChanged := false
		keptParts := 0

		for _, part := range content.Array() {
			if part.Get("type").String() == "tool_use" {
				toolUseID := strings.TrimSpace(part.Get("id").String())
				if toolUseID == "" || !nextToolResults[toolUseID] {
					contentChanged = true
					removed++
					continue
				}
			}

			contentOut, _ = sjson.SetRawBytes(contentOut, "-1", []byte(part.Raw))
			keptParts++
		}

		if !contentChanged {
			outMessages, _ = sjson.SetRawBytes(outMessages, "-1", []byte(msg.Raw))
			continue
		}

		changed = true
		if keptParts == 0 {
			continue
		}

		msgOut, err := sjson.SetRawBytes([]byte(msg.Raw), "content", contentOut)
		if err != nil {
			return body, 0, fmt.Errorf("failed to drop unanswered Claude tool_use: %w", err)
		}
		outMessages, _ = sjson.SetRawBytes(outMessages, "-1", msgOut)
	}

	if !changed {
		return body, 0, nil
	}

	out, err := sjson.SetRawBytes(body, "messages", outMessages)
	if err != nil {
		return body, 0, fmt.Errorf("failed to update Claude messages: %w", err)
	}
	return out, removed, nil
}

func dropOrphanClaudeToolResults(body []byte) ([]byte, int, error) {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return body, 0, nil
	}

	messages := gjson.GetBytes(body, "messages")
	if !messages.Exists() || !messages.IsArray() {
		return body, 0, nil
	}

	msgs := messages.Array()
	outMessages := []byte(`[]`)
	pending := map[string]bool{}
	changed := false
	removed := 0

	for _, msg := range msgs {
		role := strings.TrimSpace(msg.Get("role").String())
		switch role {
		case "assistant":
			pending = claudeToolUseIDsInMessage(msg)
			outMessages, _ = sjson.SetRawBytes(outMessages, "-1", []byte(msg.Raw))
		case "user":
			content := msg.Get("content")
			if !content.IsArray() {
				pending = map[string]bool{}
				outMessages, _ = sjson.SetRawBytes(outMessages, "-1", []byte(msg.Raw))
				continue
			}

			contentOut := []byte(`[]`)
			contentChanged := false
			keptParts := 0
			for _, part := range content.Array() {
				if part.Get("type").String() == "tool_result" {
					toolUseID := strings.TrimSpace(part.Get("tool_use_id").String())
					if toolUseID == "" || !pending[toolUseID] {
						contentChanged = true
						changed = true
						removed++
						continue
					}
					delete(pending, toolUseID)
				}
				contentOut, _ = sjson.SetRawBytes(contentOut, "-1", []byte(part.Raw))
				keptParts++
			}
			if !contentChanged {
				outMessages, _ = sjson.SetRawBytes(outMessages, "-1", []byte(msg.Raw))
				pending = map[string]bool{}
				continue
			}
			if keptParts == 0 {
				pending = map[string]bool{}
				continue
			}
			msgOut, err := sjson.SetRawBytes([]byte(msg.Raw), "content", contentOut)
			if err != nil {
				return body, 0, fmt.Errorf("failed to drop orphan Claude tool_result: %w", err)
			}
			outMessages, _ = sjson.SetRawBytes(outMessages, "-1", msgOut)
			pending = map[string]bool{}
		default:
			pending = map[string]bool{}
			outMessages, _ = sjson.SetRawBytes(outMessages, "-1", []byte(msg.Raw))
		}
	}

	if !changed {
		return body, 0, nil
	}

	out, err := sjson.SetRawBytes(body, "messages", outMessages)
	if err != nil {
		return body, 0, fmt.Errorf("failed to update Claude messages: %w", err)
	}
	return out, removed, nil
}

func claudeToolUseIDsInMessage(msg gjson.Result) map[string]bool {
	ids := make(map[string]bool)
	for _, toolUseID := range claudeToolUseIDOrderInMessage(msg) {
		ids[toolUseID] = true
	}
	return ids
}

func claudeToolUseIDOrderInMessage(msg gjson.Result) []string {
	ids := make([]string, 0)
	seen := make(map[string]bool)
	content := msg.Get("content")
	if !content.IsArray() {
		return ids
	}
	for _, part := range content.Array() {
		if part.Get("type").String() != "tool_use" {
			continue
		}
		toolUseID := strings.TrimSpace(part.Get("id").String())
		if toolUseID != "" && !seen[toolUseID] {
			ids = append(ids, toolUseID)
			seen[toolUseID] = true
		}
	}
	return ids
}

func claudeToolResultIDsInNextUserMessage(messages []gjson.Result, assistantIdx int) map[string]bool {
	result := make(map[string]bool)
	nextIdx := assistantIdx + 1
	if nextIdx >= len(messages) {
		return result
	}

	next := messages[nextIdx]
	if strings.TrimSpace(next.Get("role").String()) != "user" {
		return result
	}

	content := next.Get("content")
	if !content.IsArray() {
		return result
	}

	for _, part := range content.Array() {
		if part.Get("type").String() != "tool_result" {
			continue
		}
		toolUseID := strings.TrimSpace(part.Get("tool_use_id").String())
		if toolUseID != "" {
			result[toolUseID] = true
		}
	}
	return result
}

func sanitizeKimiOpenAICompatibleRequestBody(body []byte) ([]byte, error) {
	body = repairOpenAICompatToolCallHistory(body)
	profile := openAICompatProfileForKind("kimi")
	body = sanitizeOpenAICompatToolSchemas(body)
	body = scrubOpenAICompatProviderToolPayload(body, profile)
	return normalizeKimiToolMessageLinks(body)
}

func normalizeKimiToolMessageLinks(body []byte) ([]byte, error) {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return body, nil
	}

	messages := gjson.GetBytes(body, "messages")
	if !messages.Exists() || !messages.IsArray() {
		return body, nil
	}

	msgs := messages.Array()
	out, dropped, err := filterKimiEmptyAssistantMessages(body, msgs)
	if err != nil {
		return body, err
	}
	if dropped > 0 {
		log.WithField("dropped_assistant_messages", dropped).Debug("kimi executor: dropped empty assistant messages")
	}

	messages = gjson.GetBytes(out, "messages")
	msgs = messages.Array()
	pending := make([]string, 0)
	patched := 0
	patchedReasoning := 0
	ambiguous := 0
	latestReasoning := ""
	hasLatestReasoning := false

	removePending := func(id string) {
		for idx := range pending {
			if pending[idx] != id {
				continue
			}
			pending = append(pending[:idx], pending[idx+1:]...)
			return
		}
	}

	for msgIdx := range msgs {
		msg := msgs[msgIdx]
		role := strings.TrimSpace(msg.Get("role").String())
		switch role {
		case "assistant":
			reasoning := msg.Get("reasoning_content")
			if reasoning.Exists() {
				reasoningText := reasoning.String()
				if strings.TrimSpace(reasoningText) != "" {
					latestReasoning = reasoningText
					hasLatestReasoning = true
				}
			}

			toolCalls := msg.Get("tool_calls")
			if !toolCalls.Exists() || !toolCalls.IsArray() || len(toolCalls.Array()) == 0 {
				continue
			}

			if !reasoning.Exists() || strings.TrimSpace(reasoning.String()) == "" {
				reasoningText := fallbackAssistantReasoning(msg, hasLatestReasoning, latestReasoning)
				path := fmt.Sprintf("messages.%d.reasoning_content", msgIdx)
				next, err := sjson.SetBytes(out, path, reasoningText)
				if err != nil {
					return body, fmt.Errorf("kimi executor: failed to set assistant reasoning_content: %w", err)
				}
				out = next
				patchedReasoning++
			}

			for _, tc := range toolCalls.Array() {
				id := strings.TrimSpace(tc.Get("id").String())
				if id == "" {
					continue
				}
				pending = append(pending, id)
			}
		case "tool":
			toolCallID := strings.TrimSpace(msg.Get("tool_call_id").String())
			if toolCallID == "" {
				toolCallID = strings.TrimSpace(msg.Get("call_id").String())
				if toolCallID != "" {
					path := fmt.Sprintf("messages.%d.tool_call_id", msgIdx)
					next, err := sjson.SetBytes(out, path, toolCallID)
					if err != nil {
						return body, fmt.Errorf("kimi executor: failed to set tool_call_id from call_id: %w", err)
					}
					out = next
					patched++
				}
			}
			if toolCallID == "" {
				if len(pending) == 1 {
					toolCallID = pending[0]
					path := fmt.Sprintf("messages.%d.tool_call_id", msgIdx)
					next, err := sjson.SetBytes(out, path, toolCallID)
					if err != nil {
						return body, fmt.Errorf("kimi executor: failed to infer tool_call_id: %w", err)
					}
					out = next
					patched++
				} else if len(pending) > 1 {
					ambiguous++
				}
			}
			if toolCallID != "" {
				removePending(toolCallID)
			}
		}
	}

	if patched > 0 || patchedReasoning > 0 {
		log.WithFields(log.Fields{
			"patched_tool_messages":      patched,
			"patched_reasoning_messages": patchedReasoning,
		}).Debug("kimi executor: normalized tool message fields")
	}
	if ambiguous > 0 {
		log.WithFields(log.Fields{
			"ambiguous_tool_messages": ambiguous,
			"pending_tool_calls":      len(pending),
		}).Warn("kimi executor: tool messages missing tool_call_id with ambiguous candidates")
	}

	return out, nil
}

func filterKimiEmptyAssistantMessages(body []byte, msgs []gjson.Result) ([]byte, int, error) {
	kept := make([]string, 0, len(msgs))
	dropped := 0
	for _, msg := range msgs {
		if shouldDropKimiAssistantMessage(msg) {
			dropped++
			continue
		}
		kept = append(kept, msg.Raw)
	}
	if dropped == 0 {
		return body, 0, nil
	}

	rawMessages := []byte("[" + strings.Join(kept, ",") + "]")
	out, err := sjson.SetRawBytes(body, "messages", rawMessages)
	if err != nil {
		return body, 0, fmt.Errorf("kimi executor: failed to drop empty assistant messages: %w", err)
	}
	return out, dropped, nil
}

func shouldDropKimiAssistantMessage(msg gjson.Result) bool {
	if strings.TrimSpace(msg.Get("role").String()) != "assistant" {
		return false
	}
	if hasKimiToolCalls(msg) || hasKimiLegacyFunctionCall(msg) || hasKimiAssistantReasoning(msg) {
		return false
	}
	return isKimiAssistantContentEmpty(msg.Get("content"))
}

func hasKimiToolCalls(msg gjson.Result) bool {
	toolCalls := msg.Get("tool_calls")
	return toolCalls.Exists() && toolCalls.IsArray() && len(toolCalls.Array()) > 0
}

func hasKimiLegacyFunctionCall(msg gjson.Result) bool {
	functionCall := msg.Get("function_call")
	if !functionCall.Exists() || functionCall.Type == gjson.Null {
		return false
	}
	if functionCall.IsObject() && strings.TrimSpace(functionCall.Raw) == "{}" {
		return false
	}
	return strings.TrimSpace(functionCall.Raw) != ""
}

func hasKimiAssistantReasoning(msg gjson.Result) bool {
	reasoning := msg.Get("reasoning_content")
	return reasoning.Exists() && strings.TrimSpace(reasoning.String()) != ""
}

func isKimiAssistantContentEmpty(content gjson.Result) bool {
	if !content.Exists() || content.Type == gjson.Null {
		return true
	}
	if content.Type == gjson.String {
		return strings.TrimSpace(content.String()) == ""
	}
	if !content.IsArray() {
		return false
	}
	for _, part := range content.Array() {
		if !isKimiAssistantContentPartEmpty(part) {
			return false
		}
	}
	return true
}

func isKimiAssistantContentPartEmpty(part gjson.Result) bool {
	if !part.Exists() || part.Type == gjson.Null {
		return true
	}
	if part.Type == gjson.String {
		return strings.TrimSpace(part.String()) == ""
	}
	if !part.IsObject() {
		return false
	}
	if text := part.Get("text"); text.Exists() {
		return strings.TrimSpace(text.String()) == ""
	}
	if strings.TrimSpace(part.Get("type").String()) == "text" {
		return true
	}
	return strings.TrimSpace(part.Raw) == "{}"
}

func fallbackAssistantReasoning(msg gjson.Result, hasLatest bool, latest string) string {
	if hasLatest && strings.TrimSpace(latest) != "" {
		return latest
	}

	content := msg.Get("content")
	if content.Type == gjson.String {
		if text := strings.TrimSpace(content.String()); text != "" {
			return text
		}
	}
	if content.IsArray() {
		parts := make([]string, 0, len(content.Array()))
		for _, item := range content.Array() {
			text := strings.TrimSpace(item.Get("text").String())
			if text == "" {
				continue
			}
			parts = append(parts, text)
		}
		if len(parts) > 0 {
			return strings.Join(parts, "\n")
		}
	}

	return "[reasoning unavailable]"
}

// Refresh refreshes the Kimi token using the refresh token.
func (e *KimiExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	log.Debugf("kimi executor: refresh called")
	if refreshed, handled, err := helps.RefreshAuthViaHome(ctx, e.cfg, auth); handled {
		return refreshed, err
	}
	if auth == nil {
		return nil, fmt.Errorf("kimi executor: auth is nil")
	}
	// Expect refresh_token in metadata for OAuth-based accounts
	var refreshToken string
	if auth.Metadata != nil {
		if v, ok := auth.Metadata["refresh_token"].(string); ok && strings.TrimSpace(v) != "" {
			refreshToken = v
		}
	}
	if strings.TrimSpace(refreshToken) == "" {
		// Nothing to refresh
		return auth, nil
	}

	client := kimiauth.NewDeviceFlowClientWithDeviceIDAndProxyURL(e.cfg, resolveKimiDeviceID(auth), auth.ProxyURL)
	td, err := client.RefreshToken(ctx, refreshToken)
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
	if td.ExpiresAt > 0 {
		exp := time.Unix(td.ExpiresAt, 0).UTC().Format(time.RFC3339)
		auth.Metadata["expired"] = exp
	}
	auth.Metadata["type"] = "kimi"
	now := time.Now().Format(time.RFC3339)
	auth.Metadata["last_refresh"] = now
	return auth, nil
}

// applyKimiHeaders sets required headers for Kimi API requests.
// Headers match kimi-cli client for compatibility.
func applyKimiHeaders(r *http.Request, token string, stream bool) {
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+token)
	// Match kimi-cli headers exactly
	r.Header.Set("User-Agent", "KimiCLI/1.10.6")
	r.Header.Set("X-Msh-Platform", "kimi_cli")
	r.Header.Set("X-Msh-Version", "1.10.6")
	r.Header.Set("X-Msh-Device-Name", getKimiHostname())
	r.Header.Set("X-Msh-Device-Model", getKimiDeviceModel())
	r.Header.Set("X-Msh-Device-Id", getKimiDeviceID())
	if stream {
		r.Header.Set("Accept", "text/event-stream")
		return
	}
	r.Header.Set("Accept", "application/json")
}

func resolveKimiDeviceIDFromAuth(auth *cliproxyauth.Auth) string {
	if auth == nil || auth.Metadata == nil {
		return ""
	}

	deviceIDRaw, ok := auth.Metadata["device_id"]
	if !ok {
		return ""
	}

	deviceID, ok := deviceIDRaw.(string)
	if !ok {
		return ""
	}

	return strings.TrimSpace(deviceID)
}

func resolveKimiDeviceIDFromStorage(auth *cliproxyauth.Auth) string {
	if auth == nil {
		return ""
	}

	storage, ok := auth.Storage.(*kimiauth.KimiTokenStorage)
	if !ok || storage == nil {
		return ""
	}

	return strings.TrimSpace(storage.DeviceID)
}

func resolveKimiDeviceID(auth *cliproxyauth.Auth) string {
	deviceID := resolveKimiDeviceIDFromAuth(auth)
	if deviceID != "" {
		return deviceID
	}
	return resolveKimiDeviceIDFromStorage(auth)
}

func applyKimiHeadersWithAuth(r *http.Request, token string, stream bool, auth *cliproxyauth.Auth) {
	applyKimiHeaders(r, token, stream)

	if deviceID := resolveKimiDeviceID(auth); deviceID != "" {
		r.Header.Set("X-Msh-Device-Id", deviceID)
	}
}

// getKimiHostname returns the machine hostname.
func getKimiHostname() string {
	hostname, err := os.Hostname()
	if err != nil {
		return "unknown"
	}
	return hostname
}

// getKimiDeviceModel returns a device model string matching kimi-cli format.
func getKimiDeviceModel() string {
	return fmt.Sprintf("%s %s", runtime.GOOS, runtime.GOARCH)
}

// getKimiDeviceID returns a stable device ID, matching kimi-cli storage location.
func getKimiDeviceID() string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "cli-proxy-api-device"
	}
	// Check kimi-cli's device_id location first (platform-specific)
	var kimiShareDir string
	switch runtime.GOOS {
	case "darwin":
		kimiShareDir = filepath.Join(homeDir, "Library", "Application Support", "kimi")
	case "windows":
		appData := os.Getenv("APPDATA")
		if appData == "" {
			appData = filepath.Join(homeDir, "AppData", "Roaming")
		}
		kimiShareDir = filepath.Join(appData, "kimi")
	default: // linux and other unix-like
		kimiShareDir = filepath.Join(homeDir, ".local", "share", "kimi")
	}
	deviceIDPath := filepath.Join(kimiShareDir, "device_id")
	if data, err := os.ReadFile(deviceIDPath); err == nil {
		return strings.TrimSpace(string(data))
	}
	return "cli-proxy-api-device"
}

// kimiCreds extracts the access token from auth.
func kimiCreds(a *cliproxyauth.Auth) (token string) {
	if a == nil {
		return ""
	}
	// Check metadata first (OAuth flow stores tokens here)
	if a.Metadata != nil {
		if v, ok := a.Metadata["access_token"].(string); ok && strings.TrimSpace(v) != "" {
			return v
		}
	}
	// Fallback to attributes (API key style)
	if a.Attributes != nil {
		if v := a.Attributes["access_token"]; v != "" {
			return v
		}
		if v := a.Attributes["api_key"]; v != "" {
			return v
		}
	}
	return ""
}

// stripKimiPrefix removes the "kimi-" prefix from model names for the upstream API.
func stripKimiPrefix(model string) string {
	model = strings.TrimSpace(model)
	if strings.HasPrefix(strings.ToLower(model), "kimi-") {
		return model[5:]
	}
	return model
}
