package executor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/auth/codebuddy"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/thinking"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	codeBuddyChatPath = "/v2/chat/completions"
	codeBuddyAuthType = "codebuddy"
)

// CodeBuddyExecutor handles requests to the CodeBuddy API.
type CodeBuddyExecutor struct {
	cfg *config.Config
}

// NewCodeBuddyExecutor creates a new CodeBuddy executor instance.
func NewCodeBuddyExecutor(cfg *config.Config) *CodeBuddyExecutor {
	return &CodeBuddyExecutor{cfg: cfg}
}

// Identifier returns the unique identifier for this executor.
func (e *CodeBuddyExecutor) Identifier() string { return codeBuddyAuthType }

// codeBuddyCredentials extracts the access token and domain from auth metadata.
func codeBuddyCredentials(auth *cliproxyauth.Auth) (accessToken, userID, domain string) {
	if auth == nil {
		return "", "", ""
	}
	accessToken = metaStringValue(auth.Metadata, "access_token")
	userID = metaStringValue(auth.Metadata, "user_id")
	domain = metaStringValue(auth.Metadata, "domain")
	if domain == "" {
		domain = codebuddy.DefaultDomain
	}
	return
}

// PrepareRequest prepares the HTTP request before execution.
func (e *CodeBuddyExecutor) PrepareRequest(req *http.Request, auth *cliproxyauth.Auth) error {
	if req == nil {
		return nil
	}
	accessToken, userID, domain := codeBuddyCredentials(auth)
	if accessToken == "" {
		return fmt.Errorf("codebuddy: missing access token")
	}
	e.applyHeaders(req, accessToken, userID, domain)
	return nil
}

// HttpRequest executes a raw HTTP request.
func (e *CodeBuddyExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("codebuddy executor: request is nil")
	}
	if ctx == nil {
		ctx = req.Context()
	}
	httpReq := req.WithContext(ctx)
	if err := e.PrepareRequest(httpReq, auth); err != nil {
		return nil, err
	}
	httpClient := newProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	return httpClient.Do(httpReq)
}

// Execute performs a non-streaming request.
func (e *CodeBuddyExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName

	reporter := newUsageReporter(ctx, e.Identifier(), baseModel, auth)
	defer reporter.trackFailure(ctx, &err)

	accessToken, userID, domain := codeBuddyCredentials(auth)
	if accessToken == "" {
		return resp, fmt.Errorf("codebuddy: missing access token")
	}

	from := opts.SourceFormat
	to := sdktranslator.FromString("openai")

	originalPayloadSource := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayloadSource = opts.OriginalRequest
	}
	originalTranslated := sdktranslator.TranslateRequest(from, to, baseModel, originalPayloadSource, true)
	translated := sdktranslator.TranslateRequest(from, to, baseModel, req.Payload, true)
	requestedModel := payloadRequestedModel(opts, req.Model)
	translated = applyPayloadConfigWithRoot(e.cfg, baseModel, to.String(), "", translated, originalTranslated, requestedModel)
	translated, _ = sjson.SetBytes(translated, "stream", true)
	translated, _ = sjson.SetBytes(translated, "stream_options.include_usage", true)

	translated, err = thinking.ApplyThinking(translated, req.Model, from.String(), to.String(), e.Identifier())
	if err != nil {
		return resp, err
	}

	url := codebuddy.BaseURL + codeBuddyChatPath
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(translated))
	if err != nil {
		return resp, err
	}
	e.applyHeaders(httpReq, accessToken, userID, domain)
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("Cache-Control", "no-cache")

	var authID, authLabel, authType, authValue string
	if auth != nil {
		authID = auth.ID
		authLabel = auth.Label
		authType, authValue = auth.AccountInfo()
	}
	recordAPIRequest(ctx, e.cfg, upstreamRequestLog{
		URL:       url,
		Method:    http.MethodPost,
		Headers:   httpReq.Header.Clone(),
		Body:      translated,
		Provider:  e.Identifier(),
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})

	httpClient := newProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		recordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	defer func() {
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("codebuddy executor: close response body error: %v", errClose)
		}
	}()

	recordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if !isHTTPSuccess(httpResp.StatusCode) {
		b, _ := io.ReadAll(httpResp.Body)
		appendAPIResponseChunk(ctx, e.cfg, b)
		log.Debugf("codebuddy executor: upstream error status: %d, body: %s", httpResp.StatusCode, summarizeErrorBody(httpResp.Header.Get("Content-Type"), b))
		err = statusErr{code: httpResp.StatusCode, msg: string(b)}
		return resp, err
	}

	body, err := io.ReadAll(httpResp.Body)
	if err != nil {
		recordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	appendAPIResponseChunk(ctx, e.cfg, body)
	aggregatedBody, usageDetail, err := aggregateOpenAIChatCompletionStream(body)
	if err != nil {
		recordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	reporter.publish(ctx, usageDetail)
	reporter.ensurePublished(ctx)

	var param any
	out := sdktranslator.TranslateNonStream(ctx, to, from, req.Model, opts.OriginalRequest, translated, aggregatedBody, &param)
	resp = cliproxyexecutor.Response{Payload: []byte(out), Headers: httpResp.Header.Clone()}
	return resp, nil
}

// ExecuteStream performs a streaming request.
func (e *CodeBuddyExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName

	reporter := newUsageReporter(ctx, e.Identifier(), baseModel, auth)
	defer reporter.trackFailure(ctx, &err)

	accessToken, userID, domain := codeBuddyCredentials(auth)
	if accessToken == "" {
		return nil, fmt.Errorf("codebuddy: missing access token")
	}

	from := opts.SourceFormat
	to := sdktranslator.FromString("openai")

	originalPayloadSource := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayloadSource = opts.OriginalRequest
	}
	originalTranslated := sdktranslator.TranslateRequest(from, to, baseModel, originalPayloadSource, true)
	translated := sdktranslator.TranslateRequest(from, to, baseModel, req.Payload, true)
	requestedModel := payloadRequestedModel(opts, req.Model)
	translated = applyPayloadConfigWithRoot(e.cfg, baseModel, to.String(), "", translated, originalTranslated, requestedModel)

	translated, err = thinking.ApplyThinking(translated, req.Model, from.String(), to.String(), e.Identifier())
	if err != nil {
		return nil, err
	}

	url := codebuddy.BaseURL + codeBuddyChatPath
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(translated))
	if err != nil {
		return nil, err
	}
	e.applyHeaders(httpReq, accessToken, userID, domain)
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("Cache-Control", "no-cache")

	var authID, authLabel, authType, authValue string
	if auth != nil {
		authID = auth.ID
		authLabel = auth.Label
		authType, authValue = auth.AccountInfo()
	}
	recordAPIRequest(ctx, e.cfg, upstreamRequestLog{
		URL:       url,
		Method:    http.MethodPost,
		Headers:   httpReq.Header.Clone(),
		Body:      translated,
		Provider:  e.Identifier(),
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})

	httpClient := newProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		recordAPIResponseError(ctx, e.cfg, err)
		return nil, err
	}

	recordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if !isHTTPSuccess(httpResp.StatusCode) {
		b, _ := io.ReadAll(httpResp.Body)
		appendAPIResponseChunk(ctx, e.cfg, b)
		httpResp.Body.Close()
		log.Debugf("codebuddy executor: upstream error status: %d, body: %s", httpResp.StatusCode, summarizeErrorBody(httpResp.Header.Get("Content-Type"), b))
		err = statusErr{code: httpResp.StatusCode, msg: string(b)}
		return nil, err
	}

	out := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		defer close(out)
		defer func() {
			if errClose := httpResp.Body.Close(); errClose != nil {
				log.Errorf("codebuddy executor: close stream body error: %v", errClose)
			}
		}()

		scanner := bufio.NewScanner(httpResp.Body)
		scanner.Buffer(nil, maxScannerBufferSize)
		var param any
		for scanner.Scan() {
			line := scanner.Bytes()
			appendAPIResponseChunk(ctx, e.cfg, line)
			if detail, ok := parseOpenAIStreamUsage(line); ok {
				reporter.publish(ctx, detail)
			}
			if len(line) == 0 {
				continue
			}
			if !bytes.HasPrefix(line, []byte("data:")) {
				continue
			}
			chunks := sdktranslator.TranslateStream(ctx, to, from, req.Model, opts.OriginalRequest, translated, bytes.Clone(line), &param)
			for i := range chunks {
				out <- cliproxyexecutor.StreamChunk{Payload: []byte(chunks[i])}
			}
		}
		if errScan := scanner.Err(); errScan != nil {
			recordAPIResponseError(ctx, e.cfg, errScan)
			reporter.publishFailure(ctx)
			out <- cliproxyexecutor.StreamChunk{Err: errScan}
		}
		reporter.ensurePublished(ctx)
	}()

	return &cliproxyexecutor.StreamResult{
		Headers: httpResp.Header.Clone(),
		Chunks:  out,
	}, nil
}

// Refresh exchanges the CodeBuddy refresh token for a new access token.
func (e *CodeBuddyExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	if auth == nil {
		return nil, fmt.Errorf("codebuddy: missing auth")
	}

	refreshToken := metaStringValue(auth.Metadata, "refresh_token")
	if refreshToken == "" {
		log.Debugf("codebuddy executor: no refresh token available, skipping refresh")
		return auth, nil
	}

	accessToken, userID, domain := codeBuddyCredentials(auth)

	authSvc := codebuddy.NewCodeBuddyAuth(e.cfg)
	storage, err := authSvc.RefreshToken(ctx, accessToken, refreshToken, userID, domain)
	if err != nil {
		return nil, fmt.Errorf("codebuddy: token refresh failed: %w", err)
	}

	updated := auth.Clone()
	updated.Metadata["access_token"] = storage.AccessToken
	if storage.RefreshToken != "" {
		updated.Metadata["refresh_token"] = storage.RefreshToken
	}
	updated.Metadata["expires_in"] = storage.ExpiresIn
	updated.Metadata["domain"] = storage.Domain
	if storage.UserID != "" {
		updated.Metadata["user_id"] = storage.UserID
	}
	now := time.Now()
	updated.UpdatedAt = now
	updated.LastRefreshedAt = now

	return updated, nil
}

// CountTokens is not supported for CodeBuddy.
func (e *CodeBuddyExecutor) CountTokens(_ context.Context, _ *cliproxyauth.Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, fmt.Errorf("codebuddy: count tokens not supported")
}

// applyHeaders sets required headers for CodeBuddy API requests.
func (e *CodeBuddyExecutor) applyHeaders(req *http.Request, accessToken, userID, domain string) {
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", codebuddy.UserAgent)
	req.Header.Set("X-User-Id", userID)
	req.Header.Set("X-Domain", domain)
	req.Header.Set("X-Product", "SaaS")
	req.Header.Set("X-IDE-Type", "CLI")
	req.Header.Set("X-IDE-Name", "CLI")
	req.Header.Set("X-IDE-Version", "2.63.2")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
}

type openAIChatStreamChoiceAccumulator struct {
	Role               string
	ContentParts       []string
	ReasoningParts     []string
	FinishReason       string
	ToolCalls          map[int]*openAIChatStreamToolCallAccumulator
	ToolCallOrder      []int
	NativeFinishReason any
}

type openAIChatStreamToolCallAccumulator struct {
	ID        string
	Type      string
	Name      string
	Arguments strings.Builder
}

func aggregateOpenAIChatCompletionStream(raw []byte) ([]byte, usage.Detail, error) {
	lines := bytes.Split(raw, []byte("\n"))
	var (
		responseID  string
		model       string
		created     int64
		serviceTier string
		systemFP    string
		usageDetail usage.Detail
		choices     = map[int]*openAIChatStreamChoiceAccumulator{}
		choiceOrder []int
	)

	for _, line := range lines {
		line = bytes.TrimSpace(line)
		if len(line) == 0 || !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		payload := bytes.TrimSpace(line[5:])
		if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) {
			continue
		}
		if !gjson.ValidBytes(payload) {
			continue
		}

		root := gjson.ParseBytes(payload)
		if responseID == "" {
			responseID = root.Get("id").String()
		}
		if model == "" {
			model = root.Get("model").String()
		}
		if created == 0 {
			created = root.Get("created").Int()
		}
		if serviceTier == "" {
			serviceTier = root.Get("service_tier").String()
		}
		if systemFP == "" {
			systemFP = root.Get("system_fingerprint").String()
		}
		if detail, ok := parseOpenAIStreamUsage(line); ok {
			usageDetail = detail
		}

		for _, choiceResult := range root.Get("choices").Array() {
			idx := int(choiceResult.Get("index").Int())
			choice := choices[idx]
			if choice == nil {
				choice = &openAIChatStreamChoiceAccumulator{ToolCalls: map[int]*openAIChatStreamToolCallAccumulator{}}
				choices[idx] = choice
				choiceOrder = append(choiceOrder, idx)
			}

			delta := choiceResult.Get("delta")
			if role := delta.Get("role").String(); role != "" {
				choice.Role = role
			}
			if content := delta.Get("content").String(); content != "" {
				choice.ContentParts = append(choice.ContentParts, content)
			}
			if reasoning := delta.Get("reasoning_content").String(); reasoning != "" {
				choice.ReasoningParts = append(choice.ReasoningParts, reasoning)
			}
			if finishReason := choiceResult.Get("finish_reason").String(); finishReason != "" {
				choice.FinishReason = finishReason
			}
			if nativeFinishReason := choiceResult.Get("native_finish_reason"); nativeFinishReason.Exists() {
				choice.NativeFinishReason = nativeFinishReason.Value()
			}

			for _, toolCallResult := range delta.Get("tool_calls").Array() {
				toolIdx := int(toolCallResult.Get("index").Int())
				toolCall := choice.ToolCalls[toolIdx]
				if toolCall == nil {
					toolCall = &openAIChatStreamToolCallAccumulator{}
					choice.ToolCalls[toolIdx] = toolCall
					choice.ToolCallOrder = append(choice.ToolCallOrder, toolIdx)
				}
				if id := toolCallResult.Get("id").String(); id != "" {
					toolCall.ID = id
				}
				if typ := toolCallResult.Get("type").String(); typ != "" {
					toolCall.Type = typ
				}
				if name := toolCallResult.Get("function.name").String(); name != "" {
					toolCall.Name = name
				}
				if args := toolCallResult.Get("function.arguments").String(); args != "" {
					toolCall.Arguments.WriteString(args)
				}
			}
		}
	}

	if responseID == "" && model == "" && len(choiceOrder) == 0 {
		return nil, usageDetail, fmt.Errorf("codebuddy: streaming response did not contain any chat completion chunks")
	}

	response := map[string]any{
		"id":      responseID,
		"object":  "chat.completion",
		"created": created,
		"model":   model,
		"choices": make([]map[string]any, 0, len(choiceOrder)),
		"usage": map[string]any{
			"prompt_tokens":     usageDetail.InputTokens,
			"completion_tokens": usageDetail.OutputTokens,
			"total_tokens":      usageDetail.TotalTokens,
		},
	}
	if serviceTier != "" {
		response["service_tier"] = serviceTier
	}
	if systemFP != "" {
		response["system_fingerprint"] = systemFP
	}

	for _, idx := range choiceOrder {
		choice := choices[idx]
		message := map[string]any{
			"role":    choice.Role,
			"content": strings.Join(choice.ContentParts, ""),
		}
		if message["role"] == "" {
			message["role"] = "assistant"
		}
		if len(choice.ReasoningParts) > 0 {
			message["reasoning_content"] = strings.Join(choice.ReasoningParts, "")
		}
		if len(choice.ToolCallOrder) > 0 {
			toolCalls := make([]map[string]any, 0, len(choice.ToolCallOrder))
			for _, toolIdx := range choice.ToolCallOrder {
				toolCall := choice.ToolCalls[toolIdx]
				toolCallType := toolCall.Type
				if toolCallType == "" {
					toolCallType = "function"
				}
				arguments := toolCall.Arguments.String()
				if arguments == "" {
					arguments = "{}"
				}
				toolCalls = append(toolCalls, map[string]any{
					"id":   toolCall.ID,
					"type": toolCallType,
					"function": map[string]any{
						"name":      toolCall.Name,
						"arguments": arguments,
					},
				})
			}
			message["tool_calls"] = toolCalls
		}

		finishReason := choice.FinishReason
		if finishReason == "" {
			finishReason = "stop"
		}
		choicePayload := map[string]any{
			"index":         idx,
			"message":       message,
			"finish_reason": finishReason,
		}
		if choice.NativeFinishReason != nil {
			choicePayload["native_finish_reason"] = choice.NativeFinishReason
		}
		response["choices"] = append(response["choices"].([]map[string]any), choicePayload)
	}

	out, err := json.Marshal(response)
	if err != nil {
		return nil, usageDetail, fmt.Errorf("codebuddy: failed to encode aggregated response: %w", err)
	}
	return out, usageDetail, nil
}
