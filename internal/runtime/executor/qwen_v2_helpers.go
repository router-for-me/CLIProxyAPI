package executor

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

// qwenChatID returns the chat identifier for Qwen V2 requests, preferring legacy headers
// before falling back to a generated UUID.
func qwenChatID(req *http.Request) string {
	if req == nil {
		return uuid.NewString()
	}
	if value := strings.TrimSpace(req.Header.Get("x-claude-code-session-id")); value != "" {
		return value
	}
	if value := strings.TrimSpace(req.Header.Get("x-client-request-id")); value != "" {
		return value
	}
	return uuid.NewString()
}

// qwenBuildQuery flattens the OpenAI-style message sequence into a single query string for Qwen V2.
// It keeps the last user message at the very end to avoid losing intent context.
func qwenBuildQuery(payload []byte) (string, error) {
	messages := gjson.GetBytes(payload, "messages")
	if !messages.Exists() || !messages.IsArray() {
		return "", fmt.Errorf("qwen: messages array missing")
	}
	msgs := messages.Array()
	lastUser := -1
	for idx, msg := range msgs {
		if strings.EqualFold(msg.Get("role").String(), "user") {
			lastUser = idx
		}
	}
	limit := len(msgs) - 1
	if lastUser >= 0 {
		limit = lastUser
	}
	var parts []string
	for i := 0; i <= limit; i++ {
		text := flattenMessageContent(msgs[i].Get("content"))
		if text == "" {
			continue
		}
		parts = append(parts, text)
	}
	if len(parts) == 0 {
		return "", fmt.Errorf("qwen: no usable messages")
	}
	if lastUser >= 0 {
		lastText := flattenMessageContent(msgs[lastUser].Get("content"))
		if lastText == "" {
			return "", fmt.Errorf("qwen: last user content empty")
		}
	}
	return strings.Join(parts, "\n"), nil
}

func flattenMessageContent(content gjson.Result) string {
	if !content.Exists() {
		return ""
	}
	if content.IsArray() {
		var pieces []string
		for _, entry := range content.Array() {
			if text := strings.TrimSpace(entry.Get("text").String()); text != "" {
				pieces = append(pieces, text)
			}
		}
		return strings.Join(pieces, " ")
	}
	if content.IsObject() {
		if text := strings.TrimSpace(content.Get("text").String()); text != "" {
			return text
		}
		return ""
	}
	return strings.TrimSpace(content.String())
}

func qwenCookieHeader(tokenCookie string, sessionCookies map[string]string) string {
	var parts []string
	if tokenCookie != "" {
		parts = append(parts, fmt.Sprintf("token=%s", tokenCookie))
	}
	if len(sessionCookies) > 0 {
		keys := make([]string, 0, len(sessionCookies))
		for k := range sessionCookies {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, key := range keys {
			value := sessionCookies[key]
			if value == "" {
				continue
			}
			parts = append(parts, fmt.Sprintf("%s=%s", key, value))
		}
	}
	return strings.Join(parts, "; ")
}

type qwenChatFeatureConfig struct {
	ThinkingEnabled bool   `json:"thinking_enabled"`
	OutputSchema    string `json:"output_schema"`
	ResearchMode    string `json:"research_mode"`
	AutoThinking    bool   `json:"auto_thinking"`
	ThinkingMode    string `json:"thinking_mode"`
	ThinkingFormat  string `json:"thinking_format"`
	AutoSearch      bool   `json:"auto_search"`
}

type qwenChatMessageMeta struct {
	SubChatType string `json:"subChatType"`
}

type qwenChatMessageExtra struct {
	Meta qwenChatMessageMeta `json:"meta"`
}

type qwenChatMessage struct {
	FID           string                `json:"fid"`
	ParentID      *string               `json:"parentId"`
	ParentIDV2    *string               `json:"parent_id"`
	ChildrenIDs   []string              `json:"childrenIds"`
	Role          string                `json:"role"`
	Content       string                `json:"content"`
	UserAction    string                `json:"user_action"`
	Files         []any                 `json:"files"`
	Timestamp     int64                 `json:"timestamp"`
	Models        []string              `json:"models"`
	ChatType      string                `json:"chat_type"`
	FeatureConfig qwenChatFeatureConfig `json:"feature_config"`
	Extra         qwenChatMessageExtra  `json:"extra"`
	SubChatType   string                `json:"sub_chat_type"`
}

type qwenStreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type qwenChatCompletionRequest struct {
	Stream            bool               `json:"stream"`
	Version           string             `json:"version"`
	IncrementalOutput bool               `json:"incremental_output"`
	ChatID            string             `json:"chat_id"`
	ChatMode          string             `json:"chat_mode"`
	Model             string             `json:"model"`
	ParentID          *string            `json:"parent_id"`
	Messages          []qwenChatMessage  `json:"messages"`
	Timestamp         int64              `json:"timestamp"`
	StreamOptions     *qwenStreamOptions `json:"stream_options,omitempty"`
}

type qwenStreamState struct {
	ResponseID   string
	ChatID       string
	Created      int64
	Content      strings.Builder
	FinishReason string
	UsageRaw     string
}

const (
	qwenCoderTasksMetadataKey     = "qwen_coder_tasks"
	qwenCoderParentIDsMetadataKey = "qwen_chat_parent_ids"
	qwenCoderDefaultChatMode      = "normal"
	qwenCoderErrorType            = "invalid_request_error"
	qwenCoderGenericErrorCode     = "bad_request"
)

func qwenLastUserMessage(payload []byte) (string, error) {
	messages := gjson.GetBytes(payload, "messages")
	if !messages.Exists() || !messages.IsArray() {
		return "", fmt.Errorf("qwen: messages array missing")
	}
	msgs := messages.Array()
	for i := len(msgs) - 1; i >= 0; i-- {
		if !strings.EqualFold(msgs[i].Get("role").String(), "user") {
			continue
		}
		text := flattenMessageContent(msgs[i].Get("content"))
		if text == "" {
			return "", fmt.Errorf("qwen: last user content empty")
		}
		return text, nil
	}
	return "", fmt.Errorf("qwen: no user message found")
}

func qwenBuildCoderCompletionRequest(payload []byte, model string, chatID string, parentID *string, stream bool, now time.Time) ([]byte, error) {
	content, err := qwenLastUserMessage(payload)
	if err != nil {
		return nil, err
	}
	ts := now.Unix()
	body := qwenChatCompletionRequest{
		Stream:            stream,
		Version:           "2.1",
		IncrementalOutput: true,
		ChatID:            chatID,
		ChatMode:          qwenCoderDefaultChatMode,
		Model:             model,
		ParentID:          parentID,
		Messages: []qwenChatMessage{{
			FID:         uuid.NewString(),
			ParentID:    parentID,
			ParentIDV2:  parentID,
			ChildrenIDs: []string{},
			Role:        "user",
			Content:     content,
			UserAction:  "chat",
			Files:       []any{},
			Timestamp:   ts,
			Models:      []string{model},
			ChatType:    "t2t",
			FeatureConfig: qwenChatFeatureConfig{
				ThinkingEnabled: true,
				OutputSchema:    "phase",
				ResearchMode:    "normal",
				AutoThinking:    true,
				ThinkingMode:    "Auto",
				ThinkingFormat:  "summary",
				AutoSearch:      true,
			},
			Extra: qwenChatMessageExtra{
				Meta: qwenChatMessageMeta{SubChatType: "t2t"},
			},
			SubChatType: "t2t",
		}},
		Timestamp: ts,
	}
	if stream {
		body.StreamOptions = &qwenStreamOptions{IncludeUsage: true}
	}
	out, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("qwen: marshal coder request failed: %w", err)
	}
	return out, nil
}

func qwenExecutionBaseURL(baseURL string) string {
	if strings.TrimSpace(baseURL) == "" {
		return "https://chat.qwen.ai/api/v2"
	}
	trimmed := strings.TrimSuffix(baseURL, "/")
	if strings.HasSuffix(trimmed, "/api/v2") {
		return trimmed
	}
	return trimmed + "/api/v2"
}

func qwenBuildTaskCompletionsURL(baseURL string, chatID string) string {
	target := strings.TrimSuffix(qwenExecutionBaseURL(baseURL), "/") + "/chat/completions"
	if strings.TrimSpace(chatID) == "" {
		return target
	}
	return target + "?chat_id=" + chatID
}

func qwenBuildTaskNewURL(baseURL string) string {
	return strings.TrimSuffix(qwenExecutionBaseURL(baseURL), "/") + "/chats/new"
}

func qwenCoderErrorStatus(code int, message string) statusErr {
	return statusErr{
		code: code,
		msg:  fmt.Sprintf(`{"error":{"code":"%s","message":%q,"type":"%s"}}`, qwenCoderGenericErrorCode, message, qwenCoderErrorType),
	}
}

func qwenTaskCache(auth *cliproxyauth.Auth) map[string]string {
	if auth == nil {
		return nil
	}
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	raw, ok := auth.Metadata[qwenCoderTasksMetadataKey]
	if !ok || raw == nil {
		cache := make(map[string]string)
		auth.Metadata[qwenCoderTasksMetadataKey] = cache
		return cache
	}
	switch v := raw.(type) {
	case map[string]string:
		return v
	case map[string]any:
		cache := make(map[string]string, len(v))
		for key, value := range v {
			if s, ok := value.(string); ok && strings.TrimSpace(s) != "" {
				cache[key] = s
			}
		}
		auth.Metadata[qwenCoderTasksMetadataKey] = cache
		return cache
	default:
		cache := make(map[string]string)
		auth.Metadata[qwenCoderTasksMetadataKey] = cache
		return cache
	}
}

func qwenCachedTaskID(auth *cliproxyauth.Auth, sessionKey string) string {
	if strings.TrimSpace(sessionKey) == "" {
		return ""
	}
	cache := qwenTaskCache(auth)
	if cache == nil {
		return ""
	}
	return strings.TrimSpace(cache[sessionKey])
}

func qwenStoreTaskID(auth *cliproxyauth.Auth, sessionKey string, taskID string) {
	if strings.TrimSpace(sessionKey) == "" || strings.TrimSpace(taskID) == "" {
		return
	}
	cache := qwenTaskCache(auth)
	if cache == nil {
		return
	}
	cache[sessionKey] = taskID
}

func qwenParentIDCache(auth *cliproxyauth.Auth) map[string]string {
	if auth == nil {
		return nil
	}
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	raw, ok := auth.Metadata[qwenCoderParentIDsMetadataKey]
	if !ok || raw == nil {
		cache := make(map[string]string)
		auth.Metadata[qwenCoderParentIDsMetadataKey] = cache
		return cache
	}
	switch v := raw.(type) {
	case map[string]string:
		return v
	case map[string]any:
		cache := make(map[string]string, len(v))
		for key, value := range v {
			if s, ok := value.(string); ok && strings.TrimSpace(s) != "" {
				cache[key] = s
			}
		}
		auth.Metadata[qwenCoderParentIDsMetadataKey] = cache
		return cache
	default:
		cache := make(map[string]string)
		auth.Metadata[qwenCoderParentIDsMetadataKey] = cache
		return cache
	}
}

func qwenCachedParentID(auth *cliproxyauth.Auth, sessionKey string) string {
	if strings.TrimSpace(sessionKey) == "" {
		return ""
	}
	cache := qwenParentIDCache(auth)
	if cache == nil {
		return ""
	}
	return strings.TrimSpace(cache[sessionKey])
}

func qwenStoreParentID(auth *cliproxyauth.Auth, sessionKey string, parentID string) {
	if strings.TrimSpace(sessionKey) == "" || strings.TrimSpace(parentID) == "" {
		return
	}
	cache := qwenParentIDCache(auth)
	if cache == nil {
		return
	}
	cache[sessionKey] = parentID
}

func qwenReadJSONBody(resp *http.Response) ([]byte, error) {
	if resp == nil || resp.Body == nil {
		return nil, nil
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	return body, nil
}

func qwenNormalizeStreamChunk(line []byte, model string, state *qwenStreamState) ([]byte, bool, error) {
	raw := bytesTrimDataPrefix(line)
	if len(raw) == 0 {
		return nil, false, nil
	}
	if string(raw) == "[DONE]" {
		return nil, true, nil
	}
	root := gjson.ParseBytes(raw)
	if !root.Exists() {
		return nil, false, nil
	}

	if created := root.Get("response.created"); created.Exists() {
		if state != nil {
			if chatID := strings.TrimSpace(created.Get("chat_id").String()); chatID != "" {
				state.ChatID = chatID
			}
			if responseID := strings.TrimSpace(created.Get("response_id").String()); responseID != "" {
				state.ResponseID = responseID
			}
			if state.Created == 0 {
				state.Created = time.Now().Unix()
			}
		}
		return nil, false, nil
	}
	if root.Get("response.stopped").Exists() || root.Get("sources").Exists() || root.Get("selected_model_id").Exists() {
		return nil, false, nil
	}
	if errNode := root.Get("error"); errNode.Exists() {
		msg := strings.TrimSpace(errNode.Get("message").String())
		if msg == "" {
			msg = strings.TrimSpace(errNode.Get("details").String())
		}
		code := strings.TrimSpace(errNode.Get("code").String())
		errJSON := []byte(`{"error":{"message":"unknown qwen stream error","type":"provider_error"}}`)
		var err error
		if msg != "" {
			errJSON, err = sjson.SetBytes(errJSON, "error.message", msg)
			if err != nil {
				return nil, false, err
			}
		}
		if code != "" {
			errJSON, err = sjson.SetBytes(errJSON, "error.code", code)
			if err != nil {
				return nil, false, err
			}
		}
		return errJSON, false, nil
	}
	if !root.Get("choices").Exists() {
		return nil, false, nil
	}

	out := append([]byte(nil), raw...)
	if state != nil && state.Created == 0 {
		state.Created = time.Now().Unix()
	}
	var err error
	if state != nil && strings.TrimSpace(state.ResponseID) != "" {
		out, err = sjson.SetBytes(out, "id", state.ResponseID)
		if err != nil {
			return nil, false, err
		}
	}
	out, err = sjson.SetBytes(out, "object", "chat.completion.chunk")
	if err != nil {
		return nil, false, err
	}
	out, err = sjson.SetBytes(out, "model", model)
	if err != nil {
		return nil, false, err
	}
	if state != nil && state.Created != 0 {
		out, err = sjson.SetBytes(out, "created", state.Created)
		if err != nil {
			return nil, false, err
		}
	}

	if state != nil {
		if delta := root.Get("choices.0.delta.content"); delta.Exists() {
			state.Content.WriteString(delta.String())
		}
		if message := root.Get("choices.0.message.content"); message.Exists() && state.Content.Len() == 0 {
			state.Content.WriteString(message.String())
		}
		if finish := strings.TrimSpace(root.Get("choices.0.finish_reason").String()); finish != "" && finish != "null" {
			state.FinishReason = finish
		}
		if usage := root.Get("usage"); usage.Exists() {
			state.UsageRaw = usage.Raw
		}
	}
	return out, false, nil
}

func qwenBuildOpenAICompletionResponse(model string, state *qwenStreamState) ([]byte, error) {
	if state == nil {
		state = &qwenStreamState{}
	}
	created := state.Created
	if created == 0 {
		created = time.Now().Unix()
	}
	responseID := state.ResponseID
	if responseID == "" {
		responseID = "chatcmpl-" + uuid.NewString()
	}
	finishReason := state.FinishReason
	if finishReason == "" {
		finishReason = "stop"
	}

	resp := []byte(`{"id":"","object":"chat.completion","created":0,"model":"","choices":[{"index":0,"message":{"role":"assistant","content":""},"finish_reason":"stop"}]}`)
	var err error
	resp, err = sjson.SetBytes(resp, "id", responseID)
	if err != nil {
		return nil, err
	}
	resp, err = sjson.SetBytes(resp, "created", created)
	if err != nil {
		return nil, err
	}
	resp, err = sjson.SetBytes(resp, "model", model)
	if err != nil {
		return nil, err
	}
	resp, err = sjson.SetBytes(resp, "choices.0.message.content", state.Content.String())
	if err != nil {
		return nil, err
	}
	resp, err = sjson.SetBytes(resp, "choices.0.finish_reason", finishReason)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(state.UsageRaw) != "" {
		resp, err = sjson.SetRawBytes(resp, "usage", []byte(state.UsageRaw))
		if err != nil {
			return nil, err
		}
	}
	return resp, nil
}

func bytesTrimDataPrefix(line []byte) []byte {
	trimmed := strings.TrimSpace(string(line))
	if trimmed == "" {
		return nil
	}
	if strings.HasPrefix(trimmed, "data:") {
		trimmed = strings.TrimSpace(strings.TrimPrefix(trimmed, "data:"))
	}
	if trimmed == "" {
		return nil
	}
	return []byte(trimmed)
}

func qwenCoderJSONErrorStatus(body []byte) (int, bool) {
	if len(body) == 0 {
		return 0, false
	}
	root := gjson.ParseBytes(body)
	if !root.Exists() {
		return 0, false
	}
	success := root.Get("success")
	data := root.Get("data")
	if !success.Exists() || !data.Exists() || success.Bool() {
		return 0, false
	}
	code := strings.ToLower(strings.TrimSpace(data.Get("code").String()))
	details := strings.ToLower(strings.TrimSpace(data.Get("details").String()))
	switch code {
	case "unauthorized", "forbidden", "session_expired", "session_invalid", "need_login":
		return http.StatusUnauthorized, true
	case "requestvalidationerror", "bad_request":
		return http.StatusBadRequest, true
	}
	if strings.Contains(details, "field required") || strings.Contains(details, "not exist") || strings.Contains(details, "invalid input") {
		return http.StatusBadRequest, true
	}
	if strings.Contains(details, "session") || strings.Contains(details, "login") || strings.Contains(details, "token") {
		return http.StatusUnauthorized, true
	}
	return http.StatusBadGateway, true
}

func qwenParseModelsResponse(payload []byte) ([]*registry.ModelInfo, error) {
	root := gjson.ParseBytes(payload)
	if !root.Get("success").Bool() {
		return nil, fmt.Errorf("qwen: response not successful")
	}
	entries := root.Get("data.data")
	if !entries.Exists() || !entries.IsArray() {
		return nil, fmt.Errorf("qwen: models list missing")
	}
	var models []*registry.ModelInfo
	entries.ForEach(func(_, entry gjson.Result) bool {
		if id := strings.TrimSpace(entry.Get("id").String()); id != "" {
			info := &registry.ModelInfo{
				ID:          id,
				Name:        entry.Get("name").String(),
				Object:      entry.Get("object").String(),
				OwnedBy:     entry.Get("owned_by").String(),
				Description: entry.Get("info.meta.description").String(),
			}
			if maxCtx := entry.Get("info.meta.max_context_length"); maxCtx.Exists() {
				if length := int(maxCtx.Int()); length > 0 {
					info.ContextLength = length
				}
			}
			models = append(models, info)
		}
		return true
	})

	return models, nil
}
