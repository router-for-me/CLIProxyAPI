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
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	dshelp "github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps/deepseek"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/sjson"
)

const (
	deepSeekCreateSessionPath = "/api/v0/chat_session/create"
	deepSeekCreatePowPath     = "/api/v0/chat/create_pow_challenge"
	deepSeekCompletionPath    = "/api/v0/chat/completion"
	deepSeekLoginPath         = "/api/v0/users/login"
)

type DeepSeekExecutor struct {
	cfg *config.Config
}

func NewDeepSeekExecutor(cfg *config.Config) *DeepSeekExecutor {
	return &DeepSeekExecutor{cfg: cfg}
}

func (e *DeepSeekExecutor) Identifier() string { return "deepseek" }

func (e *DeepSeekExecutor) PrepareRequest(req *http.Request, auth *cliproxyauth.Auth) error {
	if req == nil {
		return nil
	}
	token, _, _ := e.resolveCredentials(auth)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if auth != nil {
		util.ApplyCustomHeadersFromAttrs(req, auth.Attributes)
	}
	return nil
}

func (e *DeepSeekExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("deepseek executor: request is nil")
	}
	if ctx == nil {
		ctx = req.Context()
	}
	httpReq := req.WithContext(ctx)
	if err := e.PrepareRequest(httpReq, auth); err != nil {
		return nil, err
	}
	clients := dshelp.NewClients(e.cfg, auth)
	return clients.Regular.Do(httpReq)
}

func (e *DeepSeekExecutor) ShouldPrepareRequestAuth(auth *cliproxyauth.Auth) bool {
	if auth == nil {
		return false
	}
	token, _, _ := e.resolveCredentials(auth)
	if token != "" {
		return false
	}
	return hasDeepSeekLoginCredentials(auth)
}

func (e *DeepSeekExecutor) PrepareRequestAuth(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	return e.refreshWithLogin(ctx, auth)
}

func (e *DeepSeekExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	reporter := helps.NewUsageReporter(ctx, e.Identifier(), baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	prepared, err := e.prepareOpenAIRequest(ctx, req, opts, false)
	if err != nil {
		return resp, err
	}
	flow, err := e.startCompletion(ctx, auth, prepared.spec, prepared.completionPayload)
	if err != nil {
		return resp, err
	}
	defer func() {
		if flow.response != nil && flow.response.Body != nil {
			if errClose := flow.response.Body.Close(); errClose != nil {
				log.Errorf("deepseek executor: close response body error: %v", errClose)
			}
		}
	}()
	helps.RecordAPIResponseMetadata(ctx, e.cfg, flow.response.StatusCode, flow.response.Header.Clone())
	if flow.response.StatusCode < 200 || flow.response.StatusCode >= 300 {
		body, _ := dshelp.ReadResponseBody(flow.response)
		helps.AppendAPIResponseChunk(ctx, e.cfg, body)
		err = statusErr{code: flow.response.StatusCode, msg: string(body)}
		return resp, err
	}

	text, reasoning, err := e.collectDeepSeekResponse(ctx, flow.response.Body, prepared.spec.ThinkingEnabled)
	if err != nil {
		return resp, err
	}
	toolCalls, cleanedText := dshelp.ParseToolCalls(text, prepared.spec.ToolNames)
	if len(toolCalls) > 0 {
		text = cleanedText
	}
	body := buildOpenAIChatCompletion(prepared.responseModel, text, reasoning, toolCalls, usageEstimate(prepared.spec.Prompt, text+reasoning))
	helps.AppendAPIResponseChunk(ctx, e.cfg, body)
	reporter.Publish(ctx, helps.ParseOpenAIUsage(body))
	reporter.EnsurePublished(ctx)
	var param any
	out := sdktranslator.TranslateNonStream(ctx, prepared.to, prepared.from, req.Model, opts.OriginalRequest, prepared.translated, body, &param)
	resp = cliproxyexecutor.Response{Payload: out, Headers: flow.response.Header.Clone()}
	return resp, nil
}

func (e *DeepSeekExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	reporter := helps.NewUsageReporter(ctx, e.Identifier(), baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	prepared, err := e.prepareOpenAIRequest(ctx, req, opts, true)
	if err != nil {
		return nil, err
	}
	flow, err := e.startCompletion(ctx, auth, prepared.spec, prepared.completionPayload)
	if err != nil {
		return nil, err
	}
	helps.RecordAPIResponseMetadata(ctx, e.cfg, flow.response.StatusCode, flow.response.Header.Clone())
	if flow.response.StatusCode < 200 || flow.response.StatusCode >= 300 {
		body, _ := dshelp.ReadResponseBody(flow.response)
		if errClose := flow.response.Body.Close(); errClose != nil {
			log.Errorf("deepseek executor: close response body error: %v", errClose)
		}
		helps.AppendAPIResponseChunk(ctx, e.cfg, body)
		return nil, statusErr{code: flow.response.StatusCode, msg: string(body)}
	}

	out := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		defer close(out)
		defer reporter.EnsurePublished(ctx)
		defer func() {
			if errClose := flow.response.Body.Close(); errClose != nil {
				log.Errorf("deepseek executor: close response body error: %v", errClose)
			}
		}()
		e.forwardDeepSeekStream(ctx, flow.response.Body, prepared, reporter, out)
	}()
	return &cliproxyexecutor.StreamResult{Headers: flow.response.Header.Clone(), Chunks: out}, nil
}

func (e *DeepSeekExecutor) CountTokens(ctx context.Context, _ *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	from := opts.SourceFormat
	to := sdktranslator.FromString("openai")
	translated := sdktranslator.TranslateRequest(from, to, baseModel, req.Payload, false)
	enc, err := helps.TokenizerForModel(baseModel)
	if err != nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("deepseek executor: tokenizer init failed: %w", err)
	}
	count, err := helps.CountOpenAIChatTokens(enc, translated)
	if err != nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("deepseek executor: token counting failed: %w", err)
	}
	usageJSON := helps.BuildOpenAIUsageJSON(count)
	translatedUsage := sdktranslator.TranslateTokenCount(ctx, to, from, count, usageJSON)
	return cliproxyexecutor.Response{Payload: translatedUsage}, nil
}

func (e *DeepSeekExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	if refreshed, handled, err := helps.RefreshAuthViaHome(ctx, e.cfg, auth); handled {
		return refreshed, err
	}
	if hasDeepSeekLoginCredentials(auth) {
		return e.refreshWithLogin(ctx, auth)
	}
	return auth, nil
}

type deepSeekPreparedRequest struct {
	from              sdktranslator.Format
	to                sdktranslator.Format
	translated        []byte
	originalRequest   []byte
	responseModel     string
	spec              dshelp.RequestSpec
	completionPayload map[string]any
}

func (e *DeepSeekExecutor) prepareOpenAIRequest(ctx context.Context, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, stream bool) (deepSeekPreparedRequest, error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	from := opts.SourceFormat
	to := sdktranslator.FromString("openai")
	originalPayloadSource := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayloadSource = opts.OriginalRequest
	}
	originalTranslated := sdktranslator.TranslateRequest(from, to, baseModel, originalPayloadSource, stream)
	translated := sdktranslator.TranslateRequest(from, to, baseModel, req.Payload, stream)
	var err error
	translated, err = thinking.ApplyThinking(translated, req.Model, from.String(), to.String(), e.Identifier())
	if err != nil {
		return deepSeekPreparedRequest{}, err
	}
	requestedModel := helps.PayloadRequestedModel(opts, req.Model)
	requestPath := helps.PayloadRequestPath(opts)
	translated = helps.ApplyPayloadConfigWithRequest(e.cfg, baseModel, to.String(), from.String(), "", translated, originalTranslated, requestedModel, requestPath, opts.Headers)
	translated, _ = sjson.SetBytes(translated, "stream", stream)

	spec, err := dshelp.BuildRequestSpec(translated, baseModel)
	if err != nil {
		return deepSeekPreparedRequest{}, err
	}
	if strings.TrimSpace(spec.Prompt) == "" {
		return deepSeekPreparedRequest{}, statusErr{code: http.StatusBadRequest, msg: "deepseek executor: empty prompt"}
	}
	refFileIDs := make([]any, 0, len(spec.RefFileIDs))
	for _, fileID := range spec.RefFileIDs {
		if strings.TrimSpace(fileID) != "" {
			refFileIDs = append(refFileIDs, fileID)
		}
	}
	payload := map[string]any{
		"model_type":        spec.ModelType,
		"parent_message_id": nil,
		"prompt":            spec.Prompt,
		"ref_file_ids":      refFileIDs,
		"thinking_enabled":  spec.ThinkingEnabled,
		"search_enabled":    spec.SearchEnabled,
	}
	return deepSeekPreparedRequest{
		from:              from,
		to:                to,
		translated:        translated,
		originalRequest:   opts.OriginalRequest,
		responseModel:     helps.PayloadRequestedModel(opts, req.Model),
		spec:              spec,
		completionPayload: payload,
	}, nil
}

type deepSeekFlow struct {
	response *http.Response
}

func (e *DeepSeekExecutor) startCompletion(ctx context.Context, auth *cliproxyauth.Auth, spec dshelp.RequestSpec, completionPayload map[string]any) (deepSeekFlow, error) {
	token, baseURL, useFingerprint := e.resolveCredentials(auth)
	if token == "" {
		return deepSeekFlow{}, statusErr{code: http.StatusUnauthorized, msg: "missing DeepSeek token"}
	}
	clients := dshelp.NewClients(e.cfg, auth)
	sessionID, err := e.createSession(ctx, auth, clients, baseURL, token, useFingerprint)
	if err != nil {
		return deepSeekFlow{}, err
	}
	powHeader, err := e.createPow(ctx, auth, clients, baseURL, token, useFingerprint)
	if err != nil {
		return deepSeekFlow{}, err
	}
	completionPayload["chat_session_id"] = sessionID
	resp, err := e.callCompletion(ctx, auth, clients, baseURL, token, powHeader, useFingerprint, completionPayload)
	if err != nil {
		return deepSeekFlow{}, err
	}
	_ = spec
	return deepSeekFlow{response: resp}, nil
}

func (e *DeepSeekExecutor) createSession(ctx context.Context, auth *cliproxyauth.Auth, clients dshelp.Clients, baseURL, token string, useFingerprint bool) (string, error) {
	body, status, _, err := e.postJSON(ctx, auth, clients, baseURL, deepSeekCreateSessionPath, token, "", useFingerprint, map[string]any{"agent": "chat"})
	if err != nil {
		return "", err
	}
	code, bizCode, msg := deepSeekStatus(body)
	if status == http.StatusOK && code == 0 && bizCode == 0 {
		if sessionID := extractDeepSeekSessionID(body); sessionID != "" {
			return sessionID, nil
		}
	}
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return "", statusErr{code: status, msg: msg}
	}
	return "", statusErr{code: statusCodeOrBadGateway(status), msg: nonEmpty(msg, "create session failed")}
}

func (e *DeepSeekExecutor) createPow(ctx context.Context, auth *cliproxyauth.Auth, clients dshelp.Clients, baseURL, token string, useFingerprint bool) (string, error) {
	body, status, _, err := e.postJSON(ctx, auth, clients, baseURL, deepSeekCreatePowPath, token, "", useFingerprint, map[string]any{"target_path": deepSeekCompletionPath})
	if err != nil {
		return "", err
	}
	code, bizCode, msg := deepSeekStatus(body)
	if status == http.StatusOK && code == 0 && bizCode == 0 {
		challenge, err := extractPowChallenge(body)
		if err != nil {
			return "", err
		}
		return dshelp.SolveAndBuildPowHeader(ctx, challenge)
	}
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return "", statusErr{code: status, msg: msg}
	}
	return "", statusErr{code: statusCodeOrBadGateway(status), msg: nonEmpty(msg, "create pow failed")}
}

func (e *DeepSeekExecutor) callCompletion(ctx context.Context, auth *cliproxyauth.Auth, clients dshelp.Clients, baseURL, token, powHeader string, useFingerprint bool, payload map[string]any) (*http.Response, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	urlStr := deepSeekEndpoint(baseURL, deepSeekCompletionPath)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, urlStr, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	applyDeepSeekHeaders(req, baseURL, token)
	req.Header.Set("x-ds-pow-response", powHeader)
	if auth != nil {
		util.ApplyCustomHeadersFromAttrs(req, auth.Attributes)
	}
	recordDeepSeekAPIRequest(ctx, e.cfg, auth, urlStr, raw, req.Header.Clone())
	doer := clients.FallbackStream
	if useFingerprint {
		doer = clients.Stream
	}
	resp, err := doer.Do(req)
	if err == nil {
		return resp, nil
	}
	if !useFingerprint {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return nil, err
	}
	log.Debugf("deepseek executor: fingerprint completion request failed, falling back: %v", err)
	fallbackReq, reqErr := http.NewRequestWithContext(ctx, http.MethodPost, urlStr, bytes.NewReader(raw))
	if reqErr != nil {
		return nil, reqErr
	}
	applyDeepSeekHeaders(fallbackReq, baseURL, token)
	fallbackReq.Header.Set("x-ds-pow-response", powHeader)
	if auth != nil {
		util.ApplyCustomHeadersFromAttrs(fallbackReq, auth.Attributes)
	}
	resp, err = clients.FallbackStream.Do(fallbackReq)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
	}
	return resp, err
}

func (e *DeepSeekExecutor) postJSON(ctx context.Context, auth *cliproxyauth.Auth, clients dshelp.Clients, baseURL, path, token, powHeader string, useFingerprint bool, payload any) (map[string]any, int, http.Header, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, 0, nil, err
	}
	urlStr := deepSeekEndpoint(baseURL, path)
	buildReq := func() (*http.Request, error) {
		req, errReq := http.NewRequestWithContext(ctx, http.MethodPost, urlStr, bytes.NewReader(raw))
		if errReq != nil {
			return nil, errReq
		}
		applyDeepSeekHeaders(req, baseURL, token)
		if powHeader != "" {
			req.Header.Set("x-ds-pow-response", powHeader)
		}
		if auth != nil {
			util.ApplyCustomHeadersFromAttrs(req, auth.Attributes)
		}
		return req, nil
	}
	req, err := buildReq()
	if err != nil {
		return nil, 0, nil, err
	}
	doer := clients.Fallback
	if useFingerprint {
		doer = clients.Regular
	}
	resp, err := doer.Do(req)
	if err != nil && useFingerprint {
		log.Debugf("deepseek executor: fingerprint request failed, falling back: %v", err)
		req, errReq := buildReq()
		if errReq != nil {
			return nil, 0, nil, errReq
		}
		resp, err = clients.Fallback.Do(req)
	}
	if err != nil {
		return nil, 0, nil, err
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("deepseek executor: close response body error: %v", errClose)
		}
	}()
	bodyBytes, err := dshelp.ReadResponseBody(resp)
	if err != nil {
		return nil, resp.StatusCode, resp.Header.Clone(), err
	}
	out := map[string]any{}
	if len(bytes.TrimSpace(bodyBytes)) > 0 {
		if err := json.Unmarshal(bodyBytes, &out); err != nil {
			return nil, resp.StatusCode, resp.Header.Clone(), fmt.Errorf("deepseek executor: decode JSON response: %w", err)
		}
	}
	return out, resp.StatusCode, resp.Header.Clone(), nil
}

func (e *DeepSeekExecutor) collectDeepSeekResponse(ctx context.Context, body io.Reader, thinkingEnabled bool) (string, string, error) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(nil, 52_428_800)
	currentType := ""
	var text strings.Builder
	var reasoning strings.Builder
	for scanner.Scan() {
		line := bytes.Clone(scanner.Bytes())
		helps.AppendAPIResponseChunk(ctx, e.cfg, line)
		result := dshelp.ParseSSELine(line, thinkingEnabled, currentType)
		currentType = result.NextType
		if !result.Parsed {
			continue
		}
		if result.ErrorMessage != "" {
			return "", "", statusErr{code: http.StatusBadGateway, msg: result.ErrorMessage}
		}
		if result.ContentFilter {
			return "", "", statusErr{code: http.StatusBadGateway, msg: "deepseek content filter triggered"}
		}
		for _, part := range result.Parts {
			if part.Type == "thinking" {
				reasoning.WriteString(part.Text)
			} else {
				text.WriteString(part.Text)
			}
		}
		if result.Stop {
			break
		}
	}
	if errScan := scanner.Err(); errScan != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, errScan)
		return "", "", errScan
	}
	return text.String(), reasoning.String(), nil
}

func (e *DeepSeekExecutor) forwardDeepSeekStream(ctx context.Context, body io.Reader, prepared deepSeekPreparedRequest, reporter *helps.UsageReporter, out chan<- cliproxyexecutor.StreamChunk) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(nil, 52_428_800)
	currentType := ""
	var param any
	var textBuffer strings.Builder
	var reasoningBuffer strings.Builder
	bufferForTools := len(prepared.spec.ToolNames) > 0
	emit := func(payload []byte) bool {
		chunks := sdktranslator.TranslateStream(ctx, prepared.to, prepared.from, prepared.responseModel, prepared.originalRequest, prepared.translated, payload, &param)
		for i := range chunks {
			select {
			case out <- cliproxyexecutor.StreamChunk{Payload: chunks[i]}:
			case <-ctx.Done():
				return false
			}
		}
		return true
	}
	_ = emit(openAIStreamChunk(prepared.responseModel, map[string]any{"role": "assistant"}, nil, nil))
	for scanner.Scan() {
		line := bytes.Clone(scanner.Bytes())
		helps.AppendAPIResponseChunk(ctx, e.cfg, line)
		result := dshelp.ParseSSELine(line, prepared.spec.ThinkingEnabled, currentType)
		currentType = result.NextType
		if !result.Parsed {
			continue
		}
		if result.ErrorMessage != "" {
			err := statusErr{code: http.StatusBadGateway, msg: result.ErrorMessage}
			helps.RecordAPIResponseError(ctx, e.cfg, err)
			reporter.PublishFailure(ctx, err)
			select {
			case out <- cliproxyexecutor.StreamChunk{Err: err}:
			case <-ctx.Done():
			}
			return
		}
		if result.ContentFilter {
			err := statusErr{code: http.StatusBadGateway, msg: "deepseek content filter triggered"}
			helps.RecordAPIResponseError(ctx, e.cfg, err)
			reporter.PublishFailure(ctx, err)
			select {
			case out <- cliproxyexecutor.StreamChunk{Err: err}:
			case <-ctx.Done():
			}
			return
		}
		for _, part := range result.Parts {
			if part.Type == "thinking" {
				reasoningBuffer.WriteString(part.Text)
			} else {
				textBuffer.WriteString(part.Text)
			}
			if bufferForTools {
				continue
			}
			delta := map[string]any{}
			if part.Type == "thinking" {
				delta["reasoning_content"] = part.Text
			} else {
				delta["content"] = part.Text
			}
			if !emit(openAIStreamChunk(prepared.responseModel, delta, nil, nil)) {
				return
			}
		}
		if result.Stop {
			break
		}
	}
	if errScan := scanner.Err(); errScan != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, errScan)
		reporter.PublishFailure(ctx, errScan)
		select {
		case out <- cliproxyexecutor.StreamChunk{Err: errScan}:
		case <-ctx.Done():
		}
		return
	}
	fullText := textBuffer.String()
	fullReasoning := reasoningBuffer.String()
	toolCalls, cleanedText := dshelp.ParseToolCalls(fullText, prepared.spec.ToolNames)
	finishReason := "stop"
	if len(toolCalls) > 0 {
		finishReason = "tool_calls"
		if cleanedText != "" {
			_ = emit(openAIStreamChunk(prepared.responseModel, map[string]any{"content": cleanedText}, nil, nil))
		}
		for i, call := range toolCalls {
			toolDelta := streamToolCallDelta(i, call)
			if !emit(openAIStreamChunk(prepared.responseModel, map[string]any{"tool_calls": []any{toolDelta}}, nil, nil)) {
				return
			}
		}
	} else if bufferForTools {
		if fullReasoning != "" {
			_ = emit(openAIStreamChunk(prepared.responseModel, map[string]any{"reasoning_content": fullReasoning}, nil, nil))
		}
		if fullText != "" {
			_ = emit(openAIStreamChunk(prepared.responseModel, map[string]any{"content": fullText}, nil, nil))
		}
	}
	usage := usageEstimate(prepared.spec.Prompt, fullText+fullReasoning)
	usageChunk := openAIStreamChunk(prepared.responseModel, map[string]any{}, &finishReason, usage)
	_ = emit(usageChunk)
	_ = emit([]byte("data: [DONE]"))
	reporter.Publish(ctx, openAIUsageDetail(usage))
}

func (e *DeepSeekExecutor) refreshWithLogin(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	if auth == nil {
		return auth, nil
	}
	password := strings.TrimSpace(metadataString(auth, "password"))
	email := strings.TrimSpace(metadataString(auth, "email"))
	mobile := strings.TrimSpace(metadataString(auth, "mobile"))
	if password == "" || (email == "" && mobile == "") {
		return auth, nil
	}
	_, baseURL, useFingerprint := e.resolveCredentials(auth)
	clients := dshelp.NewClients(e.cfg, auth)
	payload := map[string]any{
		"password":  password,
		"device_id": "cli_proxy_api",
		"os":        "android",
	}
	if email != "" {
		payload["email"] = email
	} else {
		loginMobile, areaCode := normalizeDeepSeekMobile(mobile)
		payload["mobile"] = loginMobile
		payload["area_code"] = areaCode
	}
	body, status, _, err := e.postJSON(ctx, auth, clients, baseURL, deepSeekLoginPath, "", "", useFingerprint, payload)
	if err != nil {
		return auth, err
	}
	code, bizCode, msg := deepSeekStatus(body)
	if status != http.StatusOK || code != 0 || bizCode != 0 {
		return auth, statusErr{code: statusCodeOrUnauthorized(status), msg: nonEmpty(msg, "deepseek login failed")}
	}
	token := extractLoginToken(body)
	if token == "" {
		return auth, statusErr{code: http.StatusUnauthorized, msg: "deepseek login response missing token"}
	}
	updated := auth.Clone()
	if updated.Metadata == nil {
		updated.Metadata = map[string]any{}
	}
	updated.Metadata["token"] = token
	updated.Metadata["access_token"] = token
	updated.Metadata["updated_at"] = time.Now().Format(time.RFC3339)
	return updated, nil
}

func (e *DeepSeekExecutor) resolveCredentials(auth *cliproxyauth.Auth) (token string, baseURL string, useFingerprint bool) {
	baseURL = dshelp.DefaultBaseURL
	if auth != nil {
		if auth.Attributes != nil {
			token = strings.TrimSpace(auth.Attributes["api_key"])
			if base := strings.TrimSpace(auth.Attributes["base_url"]); base != "" {
				baseURL = base
			}
		}
		if token == "" {
			for _, key := range []string{"token", "access_token", "deepseek_token"} {
				if v := strings.TrimSpace(metadataString(auth, key)); v != "" {
					token = v
					break
				}
			}
		}
		if base := strings.TrimSpace(metadataString(auth, "base_url")); base != "" && (auth.Attributes == nil || strings.TrimSpace(auth.Attributes["base_url"]) == "") {
			baseURL = base
		}
	}
	return token, strings.TrimRight(baseURL, "/"), isDefaultDeepSeekBaseURL(baseURL)
}

func deepSeekEndpoint(baseURL, path string) string {
	return strings.TrimRight(baseURL, "/") + path
}

func applyDeepSeekHeaders(req *http.Request, baseURL, token string) {
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("accept-charset", "UTF-8")
	req.Header.Set("User-Agent", "DeepSeek/2.0.4 Android/35")
	req.Header.Set("x-client-platform", "android")
	req.Header.Set("x-client-version", "2.0.4")
	req.Header.Set("x-client-locale", "zh_CN")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if isDefaultDeepSeekBaseURL(baseURL) {
		req.Host = "chat.deepseek.com"
	}
}

func isDefaultDeepSeekBaseURL(baseURL string) bool {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return false
	}
	return strings.EqualFold(parsed.Hostname(), "chat.deepseek.com")
}

func deepSeekStatus(body map[string]any) (int, int, string) {
	code := intFromAny(body["code"])
	msg := strings.TrimSpace(stringFromAny(body["msg"]))
	data, _ := body["data"].(map[string]any)
	bizCode := intFromAny(data["biz_code"])
	bizMsg := strings.TrimSpace(stringFromAny(data["biz_msg"]))
	if bizMsg != "" {
		msg = bizMsg
	}
	return code, bizCode, msg
}

func extractDeepSeekSessionID(body map[string]any) string {
	data, _ := body["data"].(map[string]any)
	bizData, _ := data["biz_data"].(map[string]any)
	for _, key := range []string{"id", "chat_session_id", "session_id"} {
		if v := strings.TrimSpace(stringFromAny(bizData[key])); v != "" {
			return v
		}
	}
	if session, ok := bizData["chat_session"].(map[string]any); ok {
		for _, key := range []string{"id", "chat_session_id", "session_id"} {
			if v := strings.TrimSpace(stringFromAny(session[key])); v != "" {
				return v
			}
		}
	}
	return ""
}

func extractPowChallenge(body map[string]any) (dshelp.PowChallenge, error) {
	data, _ := body["data"].(map[string]any)
	bizData, _ := data["biz_data"].(map[string]any)
	challengeMap, _ := bizData["challenge"].(map[string]any)
	if len(challengeMap) == 0 {
		return dshelp.PowChallenge{}, fmt.Errorf("deepseek pow challenge missing")
	}
	challenge := dshelp.PowChallenge{
		Algorithm:  strings.TrimSpace(stringFromAny(challengeMap["algorithm"])),
		Challenge:  strings.TrimSpace(stringFromAny(challengeMap["challenge"])),
		Salt:       strings.TrimSpace(stringFromAny(challengeMap["salt"])),
		ExpireAt:   int64FromAny(challengeMap["expire_at"]),
		Difficulty: int64FromAny(challengeMap["difficulty"]),
		Signature:  strings.TrimSpace(stringFromAny(challengeMap["signature"])),
		TargetPath: strings.TrimSpace(stringFromAny(challengeMap["target_path"])),
	}
	if challenge.TargetPath == "" {
		challenge.TargetPath = deepSeekCompletionPath
	}
	return challenge, nil
}

func extractLoginToken(body map[string]any) string {
	data, _ := body["data"].(map[string]any)
	bizData, _ := data["biz_data"].(map[string]any)
	user, _ := bizData["user"].(map[string]any)
	for _, key := range []string{"token", "access_token", "deepseek_token"} {
		if v := strings.TrimSpace(stringFromAny(user[key])); v != "" {
			return v
		}
		if v := strings.TrimSpace(stringFromAny(bizData[key])); v != "" {
			return v
		}
	}
	return ""
}

func hasDeepSeekLoginCredentials(auth *cliproxyauth.Auth) bool {
	if auth == nil || auth.Metadata == nil {
		return false
	}
	password := strings.TrimSpace(metadataString(auth, "password"))
	email := strings.TrimSpace(metadataString(auth, "email"))
	mobile := strings.TrimSpace(metadataString(auth, "mobile"))
	return password != "" && (email != "" || mobile != "")
}

func metadataString(auth *cliproxyauth.Auth, key string) string {
	if auth == nil || auth.Metadata == nil {
		return ""
	}
	value, ok := auth.Metadata[key]
	if !ok {
		return ""
	}
	return stringFromAny(value)
}

func normalizeDeepSeekMobile(mobile string) (string, string) {
	mobile = strings.TrimSpace(mobile)
	mobile = strings.TrimPrefix(mobile, "+")
	if strings.HasPrefix(mobile, "86") && len(mobile) > 11 {
		return mobile[2:], "86"
	}
	return mobile, "86"
}

func recordDeepSeekAPIRequest(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth, urlStr string, body []byte, headers http.Header) {
	var authID, authLabel, authType, authValue string
	if auth != nil {
		authID = auth.ID
		authLabel = auth.Label
		authType, authValue = auth.AccountInfo()
	}
	helps.RecordAPIRequest(ctx, cfg, helps.UpstreamRequestLog{
		URL:       urlStr,
		Method:    http.MethodPost,
		Headers:   headers,
		Body:      body,
		Provider:  "deepseek",
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})
}

func buildOpenAIChatCompletion(model, content, reasoning string, toolCalls []map[string]any, usage map[string]any) []byte {
	message := map[string]any{"role": "assistant"}
	if content != "" || len(toolCalls) == 0 {
		message["content"] = content
	}
	if reasoning != "" {
		message["reasoning_content"] = reasoning
	}
	finishReason := "stop"
	if len(toolCalls) > 0 {
		message["tool_calls"] = toolCalls
		finishReason = "tool_calls"
		if _, exists := message["content"]; !exists {
			message["content"] = nil
		}
	}
	body := map[string]any{
		"id":      "chatcmpl-" + strings.ReplaceAll(uuid.NewString(), "-", ""),
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []any{map[string]any{
			"index":         0,
			"message":       message,
			"finish_reason": finishReason,
		}},
		"usage": usage,
	}
	raw, _ := json.Marshal(body)
	return raw
}

func openAIStreamChunk(model string, delta map[string]any, finishReason *string, usage map[string]any) []byte {
	choice := map[string]any{
		"index": 0,
		"delta": delta,
	}
	if finishReason != nil {
		choice["finish_reason"] = *finishReason
	} else {
		choice["finish_reason"] = nil
	}
	body := map[string]any{
		"id":      "chatcmpl-" + strings.ReplaceAll(uuid.NewString(), "-", ""),
		"object":  "chat.completion.chunk",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []any{choice},
	}
	if usage != nil {
		body["usage"] = usage
	}
	raw, _ := json.Marshal(body)
	return append([]byte("data: "), raw...)
}

func streamToolCallDelta(index int, call map[string]any) map[string]any {
	fn, _ := call["function"].(map[string]any)
	return map[string]any{
		"index": index,
		"id":    stringFromAny(call["id"]),
		"type":  "function",
		"function": map[string]any{
			"name":      stringFromAny(fn["name"]),
			"arguments": stringFromAny(fn["arguments"]),
		},
	}
}

func usageEstimate(prompt, completion string) map[string]any {
	promptTokens := roughTokenCount(prompt)
	completionTokens := roughTokenCount(completion)
	return map[string]any{
		"prompt_tokens":     promptTokens,
		"completion_tokens": completionTokens,
		"total_tokens":      promptTokens + completionTokens,
	}
}

func openAIUsageDetail(usage map[string]any) usage.Detail {
	body, _ := json.Marshal(map[string]any{"usage": usage})
	return helps.ParseOpenAIUsage(body)
}

func roughTokenCount(text string) int {
	text = strings.TrimSpace(text)
	if text == "" {
		return 0
	}
	runes := []rune(text)
	count := len(runes) / 4
	if count < 1 {
		count = 1
	}
	return count
}

func statusCodeOrBadGateway(status int) int {
	if status > 0 {
		return status
	}
	return http.StatusBadGateway
}

func statusCodeOrUnauthorized(status int) int {
	if status > 0 {
		return status
	}
	return http.StatusUnauthorized
}

func nonEmpty(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value != "" {
		return value
	}
	return fallback
}

func intFromAny(v any) int {
	switch value := v.(type) {
	case int:
		return value
	case int64:
		return int(value)
	case float64:
		return int(value)
	case json.Number:
		n, _ := value.Int64()
		return int(n)
	default:
		return 0
	}
}

func int64FromAny(v any) int64 {
	switch value := v.(type) {
	case int:
		return int64(value)
	case int64:
		return value
	case float64:
		return int64(value)
	case json.Number:
		n, _ := value.Int64()
		return n
	default:
		return 0
	}
}

func stringFromAny(v any) string {
	switch value := v.(type) {
	case nil:
		return ""
	case string:
		return value
	case fmt.Stringer:
		return value.String()
	default:
		return fmt.Sprintf("%v", value)
	}
}
