package executor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/misc"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

const defaultCodexConversationURL = "https://chatgpt.com/backend-api/conversation"

const (
	codexConversationDefaultTimezoneOffsetMin = -480
	codexConversationDefaultTimezoneName      = "Asia/Shanghai"
)

type codexConversationPayload struct {
	Action                     string                     `json:"action"`
	Messages                   []codexConversationMessage `json:"messages"`
	ParentMessageID            string                     `json:"parent_message_id"`
	Model                      string                     `json:"model"`
	ConversationID             string                     `json:"conversation_id,omitempty"`
	HistoryAndTrainingDisabled bool                       `json:"history_and_training_disabled"`
	TimezoneOffsetMin          int                        `json:"timezone_offset_min"`
	Timezone                   string                     `json:"timezone,omitempty"`
	Suggestions                []any                      `json:"suggestions"`
	ConversationMode           *codexConversationMode     `json:"conversation_mode,omitempty"`
	SupportsBuffering          bool                       `json:"supports_buffering"`
}

type codexConversationMessage struct {
	ID       string                       `json:"id"`
	Author   codexConversationAuthor      `json:"author"`
	Content  codexConversationMessageBody `json:"content"`
	Metadata map[string]any               `json:"metadata"`
}

type codexConversationAuthor struct {
	Role string `json:"role"`
}

type codexConversationMessageBody struct {
	ContentType string   `json:"content_type"`
	Parts       []string `json:"parts"`
}

type codexConversationMode struct {
	Kind string `json:"kind"`
}

type codexConversationStreamState struct {
	ResponseID     string
	MessageID      string
	ConversationID string
	Model          string
	CreatedAt      int64
	LastFullText   string
	Started        bool
	CreatedEmitted bool
	Completed      bool
}

type codexConversationRunConfig struct {
	From            sdktranslator.Format
	To              sdktranslator.Format
	BaseModel       string
	OriginalPayload []byte
	CodexBody       []byte
	Request         cliproxyexecutor.Request
	Options         cliproxyexecutor.Options
	Reporter        *usageReporter
}

func codexUsesConversationAPI(auth *cliproxyauth.Auth) bool {
	if auth == nil {
		return false
	}

	var apiKey, baseURL string
	if auth.Attributes != nil {
		apiKey = strings.TrimSpace(auth.Attributes["api_key"])
		baseURL = strings.TrimSpace(auth.Attributes["base_url"])
	}

	if baseURL != "" {
		lower := strings.ToLower(baseURL)
		if strings.Contains(lower, "/backend-api/conversation") {
			return true
		}
		if strings.Contains(lower, "/backend-api/codex") {
			return false
		}
	}

	if apiKey != "" {
		return false
	}

	if auth.Metadata == nil {
		return false
	}
	if token, _ := auth.Metadata["access_token"].(string); strings.TrimSpace(token) != "" {
		return true
	}
	return false
}

func resolveCodexConversationURL(auth *cliproxyauth.Auth) string {
	if auth == nil || auth.Attributes == nil {
		return defaultCodexConversationURL
	}

	baseURL := strings.TrimSpace(auth.Attributes["base_url"])
	if baseURL == "" {
		return defaultCodexConversationURL
	}

	parsed, err := neturl.Parse(baseURL)
	if err != nil {
		lower := strings.ToLower(baseURL)
		switch {
		case strings.Contains(lower, "/backend-api/conversation"):
			return strings.TrimRight(baseURL, "/")
		case strings.Contains(lower, "/backend-api/codex"):
			return strings.TrimRight(strings.Replace(baseURL, "/backend-api/codex", "/backend-api/conversation", 1), "/")
		default:
			return strings.TrimRight(baseURL, "/") + "/conversation"
		}
	}

	path := strings.TrimRight(parsed.Path, "/")
	switch {
	case path == "":
		parsed.Path = "/backend-api/conversation"
	case strings.HasSuffix(path, "/backend-api/conversation"):
		parsed.Path = path
	case strings.HasSuffix(path, "/backend-api/codex"):
		parsed.Path = strings.TrimSuffix(path, "/backend-api/codex") + "/backend-api/conversation"
	case strings.HasSuffix(path, "/backend-api"):
		parsed.Path = path + "/conversation"
	default:
		parsed.Path = path + "/conversation"
	}
	return strings.TrimRight(parsed.String(), "/")
}

func applyCodexConversationHeaders(r *http.Request, auth *cliproxyauth.Auth, token string, cfg *config.Config) {
	if r == nil {
		return
	}

	applyCodexHeaders(r, auth, token, true, cfg)

	var ginHeaders http.Header
	if ginCtx, ok := r.Context().Value("gin").(*gin.Context); ok && ginCtx != nil && ginCtx.Request != nil {
		ginHeaders = ginCtx.Request.Header
	}

	origin := codexConversationRequestOrigin(r.URL)
	misc.EnsureHeader(r.Header, ginHeaders, "Accept-Language", codexConversationDefaultAcceptLanguage)
	misc.EnsureHeader(r.Header, ginHeaders, "Oai-Language", codexConversationDefaultLanguage)
	misc.EnsureHeader(r.Header, ginHeaders, "Oai-Device-Id", codexConversationDeviceID(auth))
	misc.EnsureHeader(r.Header, ginHeaders, "Openai-Sentinel-Chat-Requirements-Token", "")
	misc.EnsureHeader(r.Header, ginHeaders, "Openai-Sentinel-Proof-Token", "")
	misc.EnsureHeader(r.Header, ginHeaders, "Origin", origin)
	misc.EnsureHeader(r.Header, ginHeaders, "Referer", origin+"/")
	misc.EnsureHeader(r.Header, ginHeaders, "Sec-Fetch-Dest", "empty")
	misc.EnsureHeader(r.Header, ginHeaders, "Sec-Fetch-Mode", "cors")
	misc.EnsureHeader(r.Header, ginHeaders, "Sec-Fetch-Site", "same-origin")
	misc.EnsureHeader(r.Header, ginHeaders, "Sec-Ch-Ua", codexConversationSecCHUA)
	misc.EnsureHeader(r.Header, ginHeaders, "Sec-Ch-Ua-Mobile", "?0")
	misc.EnsureHeader(r.Header, ginHeaders, "Sec-Ch-Ua-Platform", `"Windows"`)
	ensureHeaderWithConfigPrecedence(r.Header, ginHeaders, "User-Agent", "", codexConversationBrowserUserAgent)
}

func buildCodexConversationRequest(body []byte, auth *cliproxyauth.Auth) ([]byte, error) {
	model := strings.TrimSpace(gjson.GetBytes(body, "model").String())
	if model == "" {
		return nil, statusErr{code: http.StatusBadRequest, msg: "codex conversation bridge: missing model"}
	}

	prompt, err := buildCodexConversationPrompt(body)
	if err != nil {
		return nil, err
	}

	payload := codexConversationPayload{
		Action:            "next",
		Messages:          []codexConversationMessage{buildCodexConversationUserMessage(prompt)},
		ParentMessageID:   codexConversationParentMessageID(body),
		Model:             model,
		ConversationID:    strings.TrimSpace(gjson.GetBytes(body, "conversation_id").String()),
		TimezoneOffsetMin: codexConversationTimezoneOffsetMinutes(body),
		Timezone:          codexConversationTimezoneName(body),
		Suggestions:       []any{},
		ConversationMode:  &codexConversationMode{Kind: "primary_assistant"},
		SupportsBuffering: true,
	}

	if disabled := codexConversationHistoryAndTrainingDisabled(body, auth); disabled {
		payload.HistoryAndTrainingDisabled = true
	}

	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("codex conversation bridge: marshal request: %w", err)
	}
	return raw, nil
}

func buildCodexConversationPrompt(body []byte) (string, error) {
	if tools := gjson.GetBytes(body, "tools"); tools.IsArray() && len(tools.Array()) > 0 {
		return "", statusErr{code: http.StatusBadRequest, msg: "codex conversation bridge: tool calls are not supported for OAuth auth-file credentials"}
	}

	input := gjson.GetBytes(body, "input")
	if input.Exists() && !input.IsArray() {
		if input.Type == gjson.String {
			prompt := strings.TrimSpace(input.String())
			if prompt != "" {
				return prompt, nil
			}
		}
		return "", statusErr{code: http.StatusBadRequest, msg: "codex conversation bridge: unsupported input payload"}
	}

	type promptSegment struct {
		role  string
		parts []string
	}

	var segments []promptSegment
	if instructions := strings.TrimSpace(gjson.GetBytes(body, "instructions").String()); instructions != "" {
		segments = append(segments, promptSegment{
			role:  "system",
			parts: []string{instructions},
		})
	}

	if input.IsArray() {
		items := input.Array()
		for i := range items {
			item := items[i]
			switch strings.TrimSpace(item.Get("type").String()) {
			case "", "message":
				role := normalizeCodexConversationPromptRole(item.Get("role").String())
				parts, err := extractCodexConversationTextParts(item.Get("content"))
				if err != nil {
					return "", err
				}
				if len(parts) == 0 {
					continue
				}
				segments = append(segments, promptSegment{role: role, parts: parts})
			case "function_call", "function_call_output":
				return "", statusErr{code: http.StatusBadRequest, msg: "codex conversation bridge: function tools are not supported for OAuth auth-file credentials"}
			default:
				return "", statusErr{code: http.StatusBadRequest, msg: fmt.Sprintf("codex conversation bridge: unsupported input item type %q", item.Get("type").String())}
			}
		}
	}

	if len(segments) == 0 {
		return "", statusErr{code: http.StatusBadRequest, msg: "codex conversation bridge: request has no usable text content"}
	}

	if len(segments) == 1 && segments[0].role == "user" {
		return strings.Join(segments[0].parts, "\n\n"), nil
	}

	var builder strings.Builder
	for i := range segments {
		if i > 0 {
			builder.WriteString("\n\n")
		}
		builder.WriteString(codexConversationRoleLabel(segments[i].role))
		builder.WriteString(":\n")
		builder.WriteString(strings.Join(segments[i].parts, "\n\n"))
	}
	return strings.TrimSpace(builder.String()), nil
}

func normalizeCodexConversationPromptRole(role string) string {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "developer", "system":
		return "system"
	case "assistant":
		return "assistant"
	default:
		return "user"
	}
}

func extractCodexConversationTextParts(content gjson.Result) ([]string, error) {
	if !content.Exists() {
		return nil, nil
	}
	if content.Type == gjson.String {
		if text := strings.TrimSpace(content.String()); text != "" {
			return []string{text}, nil
		}
		return nil, nil
	}
	if !content.IsArray() {
		return nil, statusErr{code: http.StatusBadRequest, msg: "codex conversation bridge: unsupported message content payload"}
	}

	var parts []string
	items := content.Array()
	for i := range items {
		item := items[i]
		itemType := strings.TrimSpace(item.Get("type").String())
		switch itemType {
		case "", "text", "input_text", "output_text":
			if text := strings.TrimSpace(item.Get("text").String()); text != "" {
				parts = append(parts, text)
			}
		case "input_image", "input_file":
			return nil, statusErr{code: http.StatusBadRequest, msg: fmt.Sprintf("codex conversation bridge: content type %q is not supported for OAuth auth-file credentials", itemType)}
		default:
			if text := strings.TrimSpace(item.Get("text").String()); text != "" {
				parts = append(parts, text)
				continue
			}
			return nil, statusErr{code: http.StatusBadRequest, msg: fmt.Sprintf("codex conversation bridge: content type %q is not supported", itemType)}
		}
	}
	return parts, nil
}

func codexConversationRoleLabel(role string) string {
	switch role {
	case "system":
		return "System"
	case "assistant":
		return "Assistant"
	default:
		return "User"
	}
}

func buildCodexConversationUserMessage(prompt string) codexConversationMessage {
	return codexConversationMessage{
		ID: uuid.NewString(),
		Author: codexConversationAuthor{
			Role: "user",
		},
		Content: codexConversationMessageBody{
			ContentType: "text",
			Parts:       []string{prompt},
		},
		Metadata: map[string]any{},
	}
}

func codexConversationParentMessageID(body []byte) string {
	if parentMessageID := strings.TrimSpace(gjson.GetBytes(body, "parent_message_id").String()); parentMessageID != "" {
		return parentMessageID
	}
	return uuid.NewString()
}

func codexConversationTimezoneOffsetMinutes(body []byte) int {
	offset := gjson.GetBytes(body, "timezone_offset_min")
	if offset.Exists() {
		switch offset.Type {
		case gjson.Number:
			return int(offset.Int())
		case gjson.String:
			if strings.TrimSpace(offset.String()) != "" {
				return int(offset.Int())
			}
		}
	}
	return codexConversationDefaultTimezoneOffsetMin
}

func codexConversationTimezoneName(body []byte) string {
	if timezone := strings.TrimSpace(gjson.GetBytes(body, "timezone").String()); timezone != "" {
		return timezone
	}
	return codexConversationDefaultTimezoneName
}

func codexConversationHistoryAndTrainingDisabled(body []byte, auth *cliproxyauth.Auth) bool {
	if value := gjson.GetBytes(body, "history_and_training_disabled"); value.Exists() {
		switch value.Type {
		case gjson.True:
			return true
		case gjson.False:
			return false
		case gjson.String:
			return strings.EqualFold(strings.TrimSpace(value.String()), "true")
		}
	}
	if auth == nil {
		return false
	}
	if auth.Attributes != nil {
		if raw := strings.TrimSpace(auth.Attributes["history_and_training_disabled"]); raw != "" {
			return strings.EqualFold(raw, "true")
		}
	}
	if auth.Metadata == nil {
		return false
	}
	switch value := auth.Metadata["history_and_training_disabled"].(type) {
	case bool:
		return value
	case string:
		return strings.EqualFold(strings.TrimSpace(value), "true")
	default:
		return false
	}
}

func newCodexConversationStreamState(model string) *codexConversationStreamState {
	return &codexConversationStreamState{
		Model: strings.TrimSpace(model),
	}
}

func (s *codexConversationStreamState) consumeLine(line []byte) ([][]byte, bool, error) {
	if len(bytes.TrimSpace(line)) == 0 {
		return nil, false, nil
	}
	if !bytes.HasPrefix(line, dataTag) {
		return nil, false, nil
	}
	payload := bytes.TrimSpace(line[len(dataTag):])
	return s.consumePayload(payload)
}

func (s *codexConversationStreamState) consumePayload(payload []byte) ([][]byte, bool, error) {
	if s == nil {
		return nil, false, fmt.Errorf("codex conversation bridge: state is nil")
	}

	if bytes.Equal(payload, []byte("[DONE]")) {
		events := make([][]byte, 0, 2)
		if !s.Completed && s.Started {
			completed, err := s.emitCompleted()
			if err != nil {
				return nil, false, err
			}
			events = append(events, completed...)
		}
		events = append(events, codexConversationDoneEvent())
		return events, true, nil
	}

	root := gjson.ParseBytes(payload)
	if errResult := root.Get("error"); errResult.Exists() && errResult.Type != gjson.Null {
		message := strings.TrimSpace(errResult.String())
		if message == "" {
			message = strings.TrimSpace(errResult.Raw)
		}
		if message == "" {
			message = "unknown conversation upstream error"
		}
		return nil, false, fmt.Errorf("codex conversation bridge: upstream error: %s", message)
	}

	message := root.Get("message")
	if conversationID := strings.TrimSpace(root.Get("conversation_id").String()); conversationID != "" {
		s.ConversationID = conversationID
	}
	if messageID := extractCodexConversationResponseMessageID(root); messageID != "" {
		s.MessageID = messageID
	}
	appendDelta := extractCodexConversationAppendDelta(root)
	fullText := extractCodexConversationResponseText(root)
	hasAssistantMessage := message.Exists() && strings.TrimSpace(message.Get("author.role").String()) == "assistant"
	if !hasAssistantMessage && appendDelta == "" && fullText == "" {
		return nil, false, nil
	}
	if model := strings.TrimSpace(message.Get("metadata.model_slug").String()); model != "" {
		s.Model = model
	}
	if s.Model == "" {
		if model := strings.TrimSpace(root.Get("model").String()); model != "" {
			s.Model = model
		}
	}
	if s.CreatedAt == 0 {
		s.CreatedAt = normalizeCodexConversationCreateTime(message.Get("create_time"))
	}
	if s.ResponseID == "" {
		if s.MessageID != "" {
			s.ResponseID = "resp_" + s.MessageID
		} else {
			s.ResponseID = "resp_" + uuid.NewString()
		}
	}

	if !s.Started {
		s.Started = true
	}

	var events [][]byte
	if created, err := s.emitCreated(); err != nil {
		return nil, false, err
	} else if len(created) > 0 {
		events = append(events, created...)
	}

	delta := appendDelta
	if delta != "" {
		s.LastFullText += delta
	} else {
		delta = s.nextDelta(fullText)
	}
	if delta != "" {
		payload, err := marshalCodexConversationEvent(map[string]any{
			"type":          "response.output_text.delta",
			"item_id":       s.MessageID,
			"output_index":  0,
			"content_index": 0,
			"delta":         delta,
		})
		if err != nil {
			return nil, false, err
		}
		events = append(events, payload)
	}

	status := strings.TrimSpace(message.Get("status").String())
	if status == "finished_successfully" || status == "finished" || status == "completed" {
		completed, err := s.emitCompleted()
		if err != nil {
			return nil, false, err
		}
		events = append(events, completed...)
		return events, true, nil
	}

	return events, false, nil
}

func (s *codexConversationStreamState) emitCreated() ([][]byte, error) {
	if s == nil || s.Completed || s.CreatedEmitted || !s.Started {
		return nil, nil
	}
	if s.CreatedAt == 0 {
		s.CreatedAt = time.Now().Unix()
	}
	if s.ResponseID == "" {
		s.ResponseID = "resp_" + uuid.NewString()
	}
	if s.Model == "" {
		s.Model = "gpt-5"
	}
	if s.MessageID == "" {
		s.MessageID = "msg_" + uuid.NewString()
	}
	payload, err := marshalCodexConversationEvent(map[string]any{
		"type": "response.created",
		"response": map[string]any{
			"id":         s.ResponseID,
			"object":     "response",
			"created_at": s.CreatedAt,
			"model":      s.Model,
			"status":     "in_progress",
		},
	})
	if err != nil {
		return nil, err
	}
	s.CreatedEmitted = true
	return [][]byte{payload}, nil
}

func (s *codexConversationStreamState) emitCompleted() ([][]byte, error) {
	if s == nil {
		return nil, fmt.Errorf("codex conversation bridge: state is nil")
	}
	if s.Completed {
		return nil, nil
	}
	if s.CreatedAt == 0 {
		s.CreatedAt = time.Now().Unix()
	}
	if s.ResponseID == "" {
		s.ResponseID = "resp_" + uuid.NewString()
	}
	if s.MessageID == "" {
		s.MessageID = "msg_" + uuid.NewString()
	}
	if s.Model == "" {
		s.Model = "gpt-5"
	}

	s.Completed = true
	payload, err := marshalCodexConversationEvent(map[string]any{
		"type": "response.completed",
		"response": map[string]any{
			"id":         s.ResponseID,
			"object":     "response",
			"created_at": s.CreatedAt,
			"model":      s.Model,
			"status":     "completed",
			"background": false,
			"error":      nil,
			"output": []map[string]any{{
				"type": "message",
				"id":   s.MessageID,
				"role": "assistant",
				"content": []map[string]any{{
					"type": "output_text",
					"text": s.LastFullText,
				}},
			}},
			"usage": map[string]any{
				"input_tokens":  0,
				"output_tokens": 0,
				"total_tokens":  0,
			},
		},
	})
	if err != nil {
		return nil, err
	}
	return [][]byte{payload}, nil
}

func (s *codexConversationStreamState) nextDelta(text string) string {
	if text == "" {
		return ""
	}
	delta, merged := mergeCodexConversationDelta(s.LastFullText, text)
	s.LastFullText = merged
	return delta
}

func mergeCodexConversationDelta(currentText, nextText string) (string, string) {
	current := currentText
	candidate := nextText
	if candidate == "" {
		return "", current
	}
	if current == "" {
		return candidate, candidate
	}
	if strings.HasPrefix(candidate, current) {
		return candidate[len(current):], candidate
	}
	if strings.HasSuffix(current, candidate) {
		return "", current
	}
	return candidate, candidate
}

func normalizeCodexConversationCreateTime(value gjson.Result) int64 {
	if !value.Exists() {
		return time.Now().Unix()
	}
	createTime := value.Float()
	if createTime <= 0 {
		return time.Now().Unix()
	}
	if createTime > 1_000_000_000_000 {
		return int64(createTime / 1000)
	}
	return int64(createTime)
}

func extractCodexConversationMessageText(content gjson.Result) string {
	if !content.Exists() {
		return ""
	}
	parts := content.Get("parts")
	if !parts.IsArray() {
		return strings.TrimSpace(content.Get("text").String())
	}
	values := parts.Array()
	textParts := make([]string, 0, len(values))
	for i := range values {
		part := values[i]
		if part.Type == gjson.String {
			if text := strings.TrimSpace(part.String()); text != "" {
				textParts = append(textParts, text)
			}
			continue
		}
		if text := strings.TrimSpace(part.Get("text").String()); text != "" {
			textParts = append(textParts, text)
		}
	}
	return strings.Join(textParts, "\n\n")
}

func extractCodexConversationResponseText(root gjson.Result) string {
	if !root.Exists() {
		return ""
	}
	if messageText := extractCodexConversationMessageText(root.Get("message.content")); messageText != "" {
		return messageText
	}
	value := root.Get("v")
	if value.Type == gjson.String {
		return strings.TrimSpace(value.String())
	}
	return ""
}

func extractCodexConversationAppendDelta(root gjson.Result) string {
	operations := root.Get("v")
	if !operations.IsArray() {
		return ""
	}
	var chunks []string
	for _, operation := range operations.Array() {
		if operation.Get("o").String() != "append" {
			continue
		}
		if operation.Get("p").String() != "/message/content/parts/0" {
			continue
		}
		if value := operation.Get("v"); value.Type == gjson.String {
			if text := value.String(); text != "" {
				chunks = append(chunks, text)
			}
		}
	}
	return strings.Join(chunks, "")
}

func extractCodexConversationResponseMessageID(root gjson.Result) string {
	if messageID := strings.TrimSpace(root.Get("message.id").String()); messageID != "" {
		return messageID
	}
	operations := root.Get("v")
	if !operations.IsArray() {
		return ""
	}
	for _, operation := range operations.Array() {
		if messageID := strings.TrimSpace(operation.Get("message_id").String()); messageID != "" {
			return messageID
		}
		if messageID := strings.TrimSpace(operation.Get("id").String()); messageID != "" {
			return messageID
		}
	}
	return ""
}

func marshalCodexConversationEvent(payload map[string]any) ([]byte, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return append([]byte("data: "), raw...), nil
}

func codexConversationDoneEvent() []byte {
	return []byte("data: [DONE]")
}

func (e *CodexExecutor) executeConversationNonStream(ctx context.Context, auth *cliproxyauth.Auth, apiKey string, run codexConversationRunConfig) (resp cliproxyexecutor.Response, err error) {
	targetURL := resolveCodexConversationURL(auth)
	apiKey, err = e.resolveCodexConversationBearerToken(ctx, auth, targetURL)
	if err != nil {
		return resp, err
	}
	conversationBody, err := buildCodexConversationRequest(run.CodexBody, auth)
	if err != nil {
		return resp, err
	}

	httpClient := newCodexConversationHTTPClient(ctx, e.cfg, auth, targetURL)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, bytes.NewReader(conversationBody))
	if err != nil {
		return resp, err
	}
	applyCodexConversationHeaders(httpReq, auth, apiKey, e.cfg)
	if err = ensureCodexConversationSession(ctx, httpClient, auth, httpReq, apiKey); err != nil {
		return resp, err
	}
	logCodexRequestDiagnostics(ctx, auth, run.Request, run.Options, httpReq.Header, conversationBody, codexContinuity{})

	var authID, authLabel, authType, authValue string
	if auth != nil {
		authID = auth.ID
		authLabel = auth.Label
		authType, authValue = auth.AccountInfo()
	}
	recordAPIRequest(ctx, e.cfg, upstreamRequestLog{
		URL:       targetURL,
		Method:    http.MethodPost,
		Headers:   httpReq.Header.Clone(),
		Body:      conversationBody,
		Provider:  e.Identifier(),
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})

	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		recordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	defer func() {
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("codex conversation bridge: close response body error: %v", errClose)
		}
	}()

	recordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		b, _ := io.ReadAll(httpResp.Body)
		appendAPIResponseChunk(ctx, e.cfg, b)
		err = newCodexStatusErr(httpResp.StatusCode, b)
		return resp, err
	}

	scanner := bufio.NewScanner(httpResp.Body)
	scanner.Buffer(nil, 52_428_800)
	state := newCodexConversationStreamState(run.BaseModel)
	var completedData []byte
	for scanner.Scan() {
		line := bytes.Clone(scanner.Bytes())
		appendAPIResponseChunk(ctx, e.cfg, line)

		events, _, consumeErr := state.consumeLine(line)
		if consumeErr != nil {
			recordAPIResponseError(ctx, e.cfg, consumeErr)
			return resp, consumeErr
		}
		for i := range events {
			event := events[i]
			if !bytes.HasPrefix(event, dataTag) {
				continue
			}
			data := bytes.TrimSpace(event[len(dataTag):])
			if gjson.GetBytes(data, "type").String() == "response.completed" {
				completedData = bytes.Clone(data)
				if run.Reporter != nil {
					run.Reporter.ensurePublished(ctx)
				}
			}
		}
	}
	if errScan := scanner.Err(); errScan != nil {
		recordAPIResponseError(ctx, e.cfg, errScan)
		return resp, errScan
	}
	if len(completedData) == 0 && state.Started {
		events, completeErr := state.emitCompleted()
		if completeErr != nil {
			return resp, completeErr
		}
		for i := range events {
			event := events[i]
			if !bytes.HasPrefix(event, dataTag) {
				continue
			}
			data := bytes.TrimSpace(event[len(dataTag):])
			if gjson.GetBytes(data, "type").String() == "response.completed" {
				completedData = bytes.Clone(data)
				if run.Reporter != nil {
					run.Reporter.ensurePublished(ctx)
				}
				break
			}
		}
	}
	if len(completedData) == 0 {
		return resp, statusErr{code: http.StatusRequestTimeout, msg: "codex conversation bridge: stream closed before response.completed"}
	}

	translatedData := completedData
	if run.To == sdktranslator.FromString("openai-response") {
		if response := gjson.GetBytes(completedData, "response"); response.Exists() && strings.TrimSpace(response.Raw) != "" {
			translatedData = []byte(response.Raw)
		}
	}

	var param any
	out := sdktranslator.TranslateNonStream(ctx, run.To, run.From, run.Request.Model, run.OriginalPayload, run.CodexBody, translatedData, &param)
	return cliproxyexecutor.Response{Payload: out, Headers: httpResp.Header.Clone()}, nil
}

func (e *CodexExecutor) executeConversationStream(ctx context.Context, auth *cliproxyauth.Auth, apiKey string, run codexConversationRunConfig) (_ *cliproxyexecutor.StreamResult, err error) {
	targetURL := resolveCodexConversationURL(auth)
	apiKey, err = e.resolveCodexConversationBearerToken(ctx, auth, targetURL)
	if err != nil {
		return nil, err
	}
	conversationBody, err := buildCodexConversationRequest(run.CodexBody, auth)
	if err != nil {
		return nil, err
	}

	httpClient := newCodexConversationHTTPClient(ctx, e.cfg, auth, targetURL)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, bytes.NewReader(conversationBody))
	if err != nil {
		return nil, err
	}
	applyCodexConversationHeaders(httpReq, auth, apiKey, e.cfg)
	if err = ensureCodexConversationSession(ctx, httpClient, auth, httpReq, apiKey); err != nil {
		return nil, err
	}
	logCodexRequestDiagnostics(ctx, auth, run.Request, run.Options, httpReq.Header, conversationBody, codexContinuity{})

	var authID, authLabel, authType, authValue string
	if auth != nil {
		authID = auth.ID
		authLabel = auth.Label
		authType, authValue = auth.AccountInfo()
	}
	recordAPIRequest(ctx, e.cfg, upstreamRequestLog{
		URL:       targetURL,
		Method:    http.MethodPost,
		Headers:   httpReq.Header.Clone(),
		Body:      conversationBody,
		Provider:  e.Identifier(),
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})

	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		recordAPIResponseError(ctx, e.cfg, err)
		return nil, err
	}

	recordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		data, readErr := io.ReadAll(httpResp.Body)
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("codex conversation bridge: close response body error: %v", errClose)
		}
		if readErr != nil {
			recordAPIResponseError(ctx, e.cfg, readErr)
			return nil, readErr
		}
		appendAPIResponseChunk(ctx, e.cfg, data)
		return nil, newCodexStatusErr(httpResp.StatusCode, data)
	}

	out := make(chan cliproxyexecutor.StreamChunk, 16)
	go func() {
		defer close(out)
		defer func() {
			if errClose := httpResp.Body.Close(); errClose != nil {
				log.Errorf("codex conversation bridge: close response body error: %v", errClose)
			}
		}()

		scanner := bufio.NewScanner(httpResp.Body)
		scanner.Buffer(nil, 52_428_800)
		state := newCodexConversationStreamState(run.BaseModel)
		var param any
		for scanner.Scan() {
			line := bytes.Clone(scanner.Bytes())
			appendAPIResponseChunk(ctx, e.cfg, line)

			events, _, consumeErr := state.consumeLine(line)
			if consumeErr != nil {
				recordAPIResponseError(ctx, e.cfg, consumeErr)
				if run.Reporter != nil {
					run.Reporter.publishFailure(ctx)
				}
				out <- cliproxyexecutor.StreamChunk{Err: consumeErr}
				return
			}
			for i := range events {
				event := events[i]
				if bytes.HasPrefix(event, dataTag) {
					data := bytes.TrimSpace(event[len(dataTag):])
					if gjson.GetBytes(data, "type").String() == "response.completed" && run.Reporter != nil {
						run.Reporter.ensurePublished(ctx)
					}
				}

				chunks := sdktranslator.TranslateStream(ctx, run.To, run.From, run.Request.Model, run.OriginalPayload, run.CodexBody, event, &param)
				for j := range chunks {
					out <- cliproxyexecutor.StreamChunk{Payload: chunks[j]}
				}
			}
		}

		if errScan := scanner.Err(); errScan != nil {
			recordAPIResponseError(ctx, e.cfg, errScan)
			if run.Reporter != nil {
				run.Reporter.publishFailure(ctx)
			}
			out <- cliproxyexecutor.StreamChunk{Err: errScan}
			return
		}

		if state.Started && !state.Completed {
			events, completeErr := state.emitCompleted()
			if completeErr != nil {
				if run.Reporter != nil {
					run.Reporter.publishFailure(ctx)
				}
				out <- cliproxyexecutor.StreamChunk{Err: completeErr}
				return
			}
			for i := range events {
				event := events[i]
				if bytes.HasPrefix(event, dataTag) {
					data := bytes.TrimSpace(event[len(dataTag):])
					if gjson.GetBytes(data, "type").String() == "response.completed" && run.Reporter != nil {
						run.Reporter.ensurePublished(ctx)
					}
				}
				chunks := sdktranslator.TranslateStream(ctx, run.To, run.From, run.Request.Model, run.OriginalPayload, run.CodexBody, event, &param)
				for j := range chunks {
					out <- cliproxyexecutor.StreamChunk{Payload: chunks[j]}
				}
			}
			doneChunks := sdktranslator.TranslateStream(ctx, run.To, run.From, run.Request.Model, run.OriginalPayload, run.CodexBody, codexConversationDoneEvent(), &param)
			for j := range doneChunks {
				out <- cliproxyexecutor.StreamChunk{Payload: doneChunks[j]}
			}
			return
		}

		if !state.Completed {
			errClosed := statusErr{code: http.StatusRequestTimeout, msg: "codex conversation bridge: stream closed before assistant response"}
			if run.Reporter != nil {
				run.Reporter.publishFailure(ctx)
			}
			out <- cliproxyexecutor.StreamChunk{Err: errClosed}
		}
	}()

	return &cliproxyexecutor.StreamResult{Headers: httpResp.Header.Clone(), Chunks: out}, nil
}
