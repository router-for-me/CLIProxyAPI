package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	kiroauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/kiro"
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

// KiroExecutor proxies requests to Kiro runtime using SSO/OIDC credentials.
type KiroExecutor struct {
	cfg *config.Config
}

func NewKiroExecutor(cfg *config.Config) *KiroExecutor {
	return &KiroExecutor{cfg: cfg}
}

func (e *KiroExecutor) Identifier() string { return kiroauth.Provider }

func (e *KiroExecutor) PrepareRequest(req *http.Request, auth *cliproxyauth.Auth) error {
	if req == nil {
		return nil
	}
	token := kiroAccessToken(auth)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	applyKiroRuntimeHeaders(req, auth)
	if auth != nil {
		util.ApplyCustomHeadersFromAttrs(req, auth.Attributes)
	}
	return nil
}

func (e *KiroExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("kiro executor: request is nil")
	}
	httpReq := req.WithContext(ctx)
	if err := e.PrepareRequest(httpReq, auth); err != nil {
		return nil, err
	}
	return helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0).Do(httpReq)
}

func (e *KiroExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	reporter := helps.NewUsageReporter(ctx, e.Identifier(), baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	from := opts.SourceFormat
	to := sdktranslator.FromString("openai")
	translated, originalTranslated, err := e.buildOpenAIRequest(baseModel, req, opts, false)
	if err != nil {
		return resp, err
	}
	kiroPayload, err := buildKiroPayload(translated, kiroUpstreamModel(baseModel), kiroProfileARN(auth))
	if err != nil {
		return resp, err
	}

	httpResp, err := e.doKiroRequest(ctx, auth, kiroPayload)
	if err != nil {
		return resp, err
	}
	defer func() {
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("kiro executor: close response body error: %v", errClose)
		}
	}()
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		body, _ := io.ReadAll(httpResp.Body)
		helps.AppendAPIResponseChunk(ctx, e.cfg, body)
		err = statusErr{code: httpResp.StatusCode, msg: string(body)}
		return resp, err
	}
	body, err := io.ReadAll(httpResp.Body)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	helps.AppendAPIResponseChunk(ctx, e.cfg, body)
	content := parseKiroContent(body)
	openAIResp := buildKiroOpenAIResponse(req.Model, content)
	reporter.EnsurePublished(ctx)
	var param any
	out := sdktranslator.TranslateNonStream(ctx, to, from, req.Model, opts.OriginalRequest, originalTranslated, openAIResp, &param)
	return cliproxyexecutor.Response{Payload: out, Headers: httpResp.Header.Clone()}, nil
}

func (e *KiroExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	reporter := helps.NewUsageReporter(ctx, e.Identifier(), baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	from := opts.SourceFormat
	to := sdktranslator.FromString("openai")
	translated, originalTranslated, err := e.buildOpenAIRequest(baseModel, req, opts, true)
	if err != nil {
		return nil, err
	}
	kiroPayload, err := buildKiroPayload(translated, kiroUpstreamModel(baseModel), kiroProfileARN(auth))
	if err != nil {
		return nil, err
	}
	httpResp, err := e.doKiroRequest(ctx, auth, kiroPayload)
	if err != nil {
		return nil, err
	}
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		body, _ := io.ReadAll(httpResp.Body)
		_ = httpResp.Body.Close()
		helps.AppendAPIResponseChunk(ctx, e.cfg, body)
		err = statusErr{code: httpResp.StatusCode, msg: string(body)}
		return nil, err
	}

	out := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		defer close(out)
		defer func() {
			if errClose := httpResp.Body.Close(); errClose != nil {
				log.Errorf("kiro executor: close stream body error: %v", errClose)
			}
		}()
		var param any
		parser := newKiroEventParser()
		buf := make([]byte, 32*1024)
		for {
			n, readErr := httpResp.Body.Read(buf)
			if n > 0 {
				chunk := append([]byte(nil), buf[:n]...)
				helps.AppendAPIResponseChunk(ctx, e.cfg, chunk)
				for _, content := range parser.feed(chunk) {
					if content == "" {
						continue
					}
					openAIChunk := buildKiroOpenAIStreamChunk(req.Model, content, false)
					chunks := sdktranslator.TranslateStream(ctx, to, from, req.Model, opts.OriginalRequest, originalTranslated, openAIChunk, &param)
					for i := range chunks {
						select {
						case out <- cliproxyexecutor.StreamChunk{Payload: chunks[i]}:
						case <-ctx.Done():
							return
						}
					}
				}
			}
			if readErr != nil {
				if readErr != io.EOF {
					helps.RecordAPIResponseError(ctx, e.cfg, readErr)
					reporter.PublishFailure(ctx, readErr)
					select {
					case out <- cliproxyexecutor.StreamChunk{Err: readErr}:
					case <-ctx.Done():
					}
					return
				}
				done := []byte("data: [DONE]")
				chunks := sdktranslator.TranslateStream(ctx, to, from, req.Model, opts.OriginalRequest, originalTranslated, done, &param)
				for i := range chunks {
					select {
					case out <- cliproxyexecutor.StreamChunk{Payload: chunks[i]}:
					case <-ctx.Done():
						return
					}
				}
				reporter.EnsurePublished(ctx)
				return
			}
		}
	}()
	return &cliproxyexecutor.StreamResult{Headers: httpResp.Header.Clone(), Chunks: out}, nil
}

func (e *KiroExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	translated, _, err := e.buildOpenAIRequest(baseModel, req, opts, false)
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}
	enc, err := helps.TokenizerForModel(baseModel)
	if err != nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("kiro executor: tokenizer init failed: %w", err)
	}
	count, err := helps.CountOpenAIChatTokens(enc, translated)
	if err != nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("kiro executor: token counting failed: %w", err)
	}
	usageJSON := helps.BuildOpenAIUsageJSON(count)
	translatedUsage := sdktranslator.TranslateTokenCount(ctx, sdktranslator.FromString("openai"), opts.SourceFormat, count, usageJSON)
	return cliproxyexecutor.Response{Payload: translatedUsage}, nil
}

func (e *KiroExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	log.Debugf("kiro executor: refresh called")
	if refreshed, handled, err := helps.RefreshAuthViaHome(ctx, e.cfg, auth); handled {
		return refreshed, err
	}
	if auth == nil {
		return nil, fmt.Errorf("kiro executor: auth is nil")
	}
	td := kiroauth.TokenDataFromMetadata(auth.Metadata)
	if strings.TrimSpace(td.RefreshToken) == "" {
		return auth, nil
	}
	refreshed, err := kiroauth.NewKiroAuth(e.cfg).Refresh(ctx, td)
	if err != nil {
		return nil, err
	}
	updated := auth.Clone()
	updated.Metadata = kiroauth.MetadataFromTokenData(refreshed)
	updated.Metadata["last_refresh"] = time.Now().UTC().Format(time.RFC3339)
	updated.UpdatedAt = time.Now().UTC()
	updated.LastRefreshedAt = updated.UpdatedAt
	if exp, ok := updated.ExpirationTime(); ok {
		updated.NextRefreshAfter = exp.Add(-20 * time.Minute)
	}
	return updated, nil
}

func (e *KiroExecutor) buildOpenAIRequest(baseModel string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, stream bool) ([]byte, []byte, error) {
	from := opts.SourceFormat
	to := sdktranslator.FromString("openai")
	originalPayloadSource := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayloadSource = opts.OriginalRequest
	}
	originalTranslated := sdktranslator.TranslateRequest(from, to, baseModel, bytes.Clone(originalPayloadSource), stream)
	translated := sdktranslator.TranslateRequest(from, to, baseModel, bytes.Clone(req.Payload), stream)
	var err error
	translated, err = thinking.ApplyThinking(translated, req.Model, from.String(), "kiro", e.Identifier())
	if err != nil {
		return nil, nil, err
	}
	translated = helps.ApplyPayloadConfigWithRequest(e.cfg, baseModel, "kiro", from.String(), "", translated, originalTranslated, helps.PayloadRequestedModel(opts, req.Model), helps.PayloadRequestPath(opts), opts.Headers)
	translated, _ = sjson.SetBytes(translated, "model", kiroUpstreamModel(baseModel))
	return translated, originalTranslated, nil
}

func (e *KiroExecutor) doKiroRequest(ctx context.Context, auth *cliproxyauth.Auth, payload []byte) (*http.Response, error) {
	token := kiroAccessToken(auth)
	if token == "" {
		return nil, statusErr{code: http.StatusUnauthorized, msg: "missing kiro access token"}
	}
	profileARN := kiroProfileARN(auth)
	if profileARN == "" {
		return nil, statusErr{code: http.StatusUnauthorized, msg: "missing kiro profile_arn"}
	}
	url := strings.TrimSuffix(kiroauth.RuntimeHost(kiroAPIRegion(auth)), "/") + "/generateAssistantResponse"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	applyKiroRuntimeHeaders(req, auth)
	if auth != nil {
		util.ApplyCustomHeadersFromAttrs(req, auth.Attributes)
	}
	helps.RecordAPIRequest(ctx, e.cfg, helps.UpstreamRequestLog{
		URL:      url,
		Method:   http.MethodPost,
		Headers:  req.Header.Clone(),
		Body:     payload,
		Provider: e.Identifier(),
	})
	resp, err := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0).Do(req)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return nil, err
	}
	helps.RecordAPIResponseMetadata(ctx, e.cfg, resp.StatusCode, resp.Header.Clone())
	return resp, nil
}

func applyKiroRuntimeHeaders(req *http.Request, auth *cliproxyauth.Auth) {
	fingerprint := kiroauth.Fingerprint(kiroProfileARN(auth) + ":" + kiroEmail(auth))
	userAgent := "aws-sdk-js/1.0.27 ua/2.1 os/linux lang/js md/nodejs#22 api/codewhispererstreaming#1.0.27 m/E KiroIDE-0.7.45-" + fingerprint
	req.Header.Set("Content-Type", "application/x-amz-json-1.0")
	req.Header.Set("x-amz-target", "AmazonCodeWhispererStreamingService.GenerateAssistantResponse")
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("x-amz-user-agent", "aws-sdk-js/1.0.27 KiroIDE-0.7.45-"+fingerprint)
	req.Header.Set("x-amzn-codewhisperer-optout", "true")
	req.Header.Set("x-amzn-kiro-agent-mode", "vibe")
	req.Header.Set("amz-sdk-invocation-id", uuid.NewString())
	req.Header.Set("amz-sdk-request", "attempt=1; max=3")
}

func buildKiroPayload(openAIReq []byte, modelID string, profileARN string) ([]byte, error) {
	messages := gjson.GetBytes(openAIReq, "messages").Array()
	if len(messages) == 0 {
		return nil, fmt.Errorf("kiro executor: messages are required")
	}
	systemPrompt := collectKiroSystemPrompt(messages)
	chatMessages := filterKiroChatMessages(messages)
	if len(chatMessages) == 0 {
		chatMessages = []gjson.Result{messages[len(messages)-1]}
	}
	historyMessages := chatMessages
	current := historyMessages[len(historyMessages)-1]
	historyMessages = historyMessages[:len(historyMessages)-1]
	currentContent := kiroMessageText(current)
	if strings.TrimSpace(currentContent) == "" {
		currentContent = "(empty placeholder)"
	}
	if systemPrompt != "" && len(historyMessages) == 0 {
		currentContent = systemPrompt + "\n\n" + currentContent
	}

	history := make([]any, 0, len(historyMessages))
	for i, msg := range historyMessages {
		role := strings.ToLower(strings.TrimSpace(msg.Get("role").String()))
		content := kiroMessageText(msg)
		if i == 0 && systemPrompt != "" && role == "user" {
			content = systemPrompt + "\n\n" + content
		}
		if strings.TrimSpace(content) == "" {
			continue
		}
		if role == "assistant" {
			history = append(history, map[string]any{"assistantResponseMessage": map[string]any{"content": content}})
		} else {
			history = append(history, map[string]any{"userInputMessage": map[string]any{"content": content, "modelId": modelID, "origin": "AI_EDITOR"}})
		}
	}

	payload := map[string]any{
		"conversationState": map[string]any{
			"chatTriggerType": "MANUAL",
			"conversationId":  uuid.NewString(),
			"currentMessage": map[string]any{
				"userInputMessage": map[string]any{
					"content": currentContent,
					"modelId": modelID,
					"origin":  "AI_EDITOR",
				},
			},
		},
	}
	if len(history) > 0 {
		payload["conversationState"].(map[string]any)["history"] = history
	}
	if strings.TrimSpace(profileARN) != "" {
		payload["profileArn"] = strings.TrimSpace(profileARN)
	}
	return json.Marshal(payload)
}

func collectKiroSystemPrompt(messages []gjson.Result) string {
	parts := make([]string, 0)
	for _, msg := range messages {
		if strings.EqualFold(strings.TrimSpace(msg.Get("role").String()), "system") {
			if text := kiroMessageText(msg); text != "" {
				parts = append(parts, text)
			}
		}
	}
	return strings.Join(parts, "\n\n")
}

func filterKiroChatMessages(messages []gjson.Result) []gjson.Result {
	out := make([]gjson.Result, 0, len(messages))
	for _, msg := range messages {
		role := strings.ToLower(strings.TrimSpace(msg.Get("role").String()))
		if role == "system" || role == "tool" {
			continue
		}
		out = append(out, msg)
	}
	return out
}

func kiroMessageText(msg gjson.Result) string {
	content := msg.Get("content")
	if content.Type == gjson.String {
		return content.String()
	}
	if content.IsArray() {
		parts := make([]string, 0)
		for _, item := range content.Array() {
			itemType := strings.ToLower(strings.TrimSpace(item.Get("type").String()))
			switch itemType {
			case "", "text", "input_text":
				if text := strings.TrimSpace(item.Get("text").String()); text != "" {
					parts = append(parts, text)
				}
			}
		}
		return strings.Join(parts, "\n")
	}
	return strings.TrimSpace(content.String())
}

func parseKiroContent(raw []byte) string {
	parser := newKiroEventParser()
	return strings.Join(parser.feed(raw), "")
}

type kiroEventParser struct {
	buffer      string
	lastContent string
}

func newKiroEventParser() *kiroEventParser { return &kiroEventParser{} }

func (p *kiroEventParser) feed(chunk []byte) []string {
	p.buffer += string(chunk)
	var out []string
	for {
		idx := strings.Index(p.buffer, `{"content":`)
		if idx < 0 {
			if len(p.buffer) > 4096 {
				p.buffer = p.buffer[len(p.buffer)-4096:]
			}
			return out
		}
		end := findJSONEnd(p.buffer, idx)
		if end < 0 {
			if idx > 0 {
				p.buffer = p.buffer[idx:]
			}
			return out
		}
		raw := p.buffer[idx : end+1]
		p.buffer = p.buffer[end+1:]
		value := gjson.Get(raw, "content").String()
		if value == "" || value == p.lastContent {
			continue
		}
		p.lastContent = value
		out = append(out, value)
	}
}

func findJSONEnd(s string, start int) int {
	depth := 0
	inString := false
	escaped := false
	for i := start; i < len(s); i++ {
		ch := s[i]
		if escaped {
			escaped = false
			continue
		}
		if inString && ch == '\\' {
			escaped = true
			continue
		}
		if ch == '"' {
			inString = !inString
			continue
		}
		if inString {
			continue
		}
		switch ch {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

func buildKiroOpenAIResponse(model string, content string) []byte {
	payload := map[string]any{
		"id":      "chatcmpl-" + uuid.NewString(),
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []any{map[string]any{
			"index": 0,
			"message": map[string]any{
				"role":    "assistant",
				"content": content,
			},
			"finish_reason": "stop",
		}},
	}
	raw, _ := json.Marshal(payload)
	return raw
}

func buildKiroOpenAIStreamChunk(model string, content string, done bool) []byte {
	if done {
		return []byte("data: [DONE]")
	}
	payload := map[string]any{
		"id":      "chatcmpl-" + uuid.NewString(),
		"object":  "chat.completion.chunk",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []any{map[string]any{
			"index":         0,
			"delta":         map[string]any{"role": "assistant", "content": content},
			"finish_reason": nil,
		}},
	}
	raw, _ := json.Marshal(payload)
	return append([]byte("data: "), raw...)
}

func kiroAccessToken(auth *cliproxyauth.Auth) string {
	return kiroMetaString(auth, "access_token", "accessToken")
}

func kiroProfileARN(auth *cliproxyauth.Auth) string {
	if auth != nil && auth.Attributes != nil {
		if value := strings.TrimSpace(auth.Attributes["profile_arn"]); value != "" {
			return value
		}
	}
	return kiroMetaString(auth, "profile_arn", "profileArn")
}

func kiroAPIRegion(auth *cliproxyauth.Auth) string {
	return firstKiroNonEmpty(kiroMetaString(auth, "api_region", "apiRegion"), kiroMetaString(auth, "region"), kiroauth.DefaultAPIRegion)
}

func kiroEmail(auth *cliproxyauth.Auth) string {
	return kiroMetaString(auth, "email")
}

func kiroMetaString(auth *cliproxyauth.Auth, keys ...string) string {
	if auth == nil || auth.Metadata == nil {
		return ""
	}
	for _, key := range keys {
		if value, ok := auth.Metadata[key].(string); ok {
			if trimmed := strings.TrimSpace(value); trimmed != "" {
				return trimmed
			}
		}
	}
	return ""
}

func kiroUpstreamModel(model string) string {
	model = strings.TrimSpace(model)
	if model == "auto-kiro" {
		return "auto"
	}
	return strings.TrimPrefix(model, "kiro-")
}

func firstKiroNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
