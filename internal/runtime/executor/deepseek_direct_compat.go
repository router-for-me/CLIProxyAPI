package executor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
)

type deepSeekAuth struct {
	Token     string
	AccountID string
}

type deepSeekRequest struct {
	RequestedModel string
	ResolvedModel  string
	Prompt         string
	Stream         bool
	Thinking       bool
	Search         bool
	PassThrough    map[string]any
}

func (r deepSeekRequest) completionPayload(sessionID string) map[string]any {
	payload := map[string]any{
		"chat_session_id":   sessionID,
		"model_type":        deepSeekModelType(r.ResolvedModel),
		"parent_message_id": nil,
		"prompt":            r.Prompt,
		"ref_file_ids":      []any{},
		"thinking_enabled":  r.Thinking,
		"search_enabled":    r.Search,
	}
	for key, value := range r.PassThrough {
		payload[key] = value
	}
	return payload
}

func prepareDeepSeekRequest(body []byte, fallbackModel string) (deepSeekRequest, error) {
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return deepSeekRequest{}, err
	}
	model := strings.TrimSpace(asString(raw["model"]))
	if model == "" {
		model = fallbackModel
	}
	resolved := resolveDeepSeekModel(model)
	if resolved == "" {
		return deepSeekRequest{}, statusErr{code: http.StatusBadRequest, msg: "unsupported deepseek model: " + model}
	}
	thinkingEnabled, searchEnabled := deepSeekModelConfig(resolved)
	if val, ok := raw["thinking"].(bool); ok {
		thinkingEnabled = val
	}
	prompt, err := buildDeepSeekPrompt(raw)
	if err != nil {
		return deepSeekRequest{}, err
	}
	passThrough := map[string]any{}
	for _, key := range []string{"temperature", "top_p", "max_tokens", "max_completion_tokens", "presence_penalty", "frequency_penalty", "stop"} {
		if value, ok := raw[key]; ok {
			passThrough[key] = value
		}
	}
	return deepSeekRequest{
		RequestedModel: model,
		ResolvedModel:  resolved,
		Prompt:         prompt,
		Stream:         boolValue(raw["stream"]),
		Thinking:       thinkingEnabled,
		Search:         searchEnabled,
		PassThrough:    passThrough,
	}, nil
}

func buildDeepSeekPrompt(req map[string]any) (string, error) {
	messages, ok := req["messages"].([]any)
	if !ok || len(messages) == 0 {
		return "", statusErr{code: http.StatusBadRequest, msg: "OpenAI messages array is required"}
	}
	var b strings.Builder
	b.WriteString("<｜begin▁of▁sentence｜>")
	for _, item := range messages {
		msg, _ := item.(map[string]any)
		role := asString(msg["role"])
		content := normalizeDeepSeekMessageContent(msg["content"])
		switch role {
		case "system", "developer":
			b.WriteString("<｜System｜>")
			b.WriteString(content)
			b.WriteString("<｜end▁of▁instructions｜>")
		case "assistant":
			b.WriteString("<｜Assistant｜>")
			if reasoning := asString(msg["reasoning_content"]); reasoning != "" {
				b.WriteString("[reasoning_content]\n")
				b.WriteString(reasoning)
				b.WriteString("\n[/reasoning_content]\n")
			}
			b.WriteString(content)
			b.WriteString("<｜end▁of▁sentence｜>")
		case "tool":
			b.WriteString("<｜Tool｜>")
			b.WriteString(content)
			b.WriteString("<｜end▁of▁toolresults｜>")
		default:
			b.WriteString("<｜User｜>")
			b.WriteString(content)
		}
	}
	if tools, ok := req["tools"]; ok {
		if toolPrompt := buildDeepSeekToolPrompt(tools); toolPrompt != "" {
			b.WriteString("<｜System｜>")
			b.WriteString(toolPrompt)
			b.WriteString("<｜end▁of▁instructions｜>")
		}
	}
	b.WriteString("<｜Assistant｜>")
	return b.String(), nil
}

func buildDeepSeekToolPrompt(tools any) string {
	items, ok := tools.([]any)
	if !ok || len(items) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\nYou may call tools when needed. Return tool calls as JSON inside <tool_call>...</tool_call>. Available tools:\n")
	for _, item := range items {
		m, _ := item.(map[string]any)
		fn, _ := m["function"].(map[string]any)
		name := asString(fn["name"])
		if name == "" {
			continue
		}
		b.WriteString("- ")
		b.WriteString(name)
		if desc := asString(fn["description"]); desc != "" {
			b.WriteString(": ")
			b.WriteString(desc)
		}
		if params, ok := fn["parameters"]; ok {
			encoded, _ := json.Marshal(params)
			if len(encoded) > 0 {
				b.WriteString(" Parameters: ")
				b.Write(encoded)
			}
		}
		b.WriteByte('\n')
	}
	return b.String()
}

func normalizeDeepSeekMessageContent(content any) string {
	switch typed := content.(type) {
	case string:
		return typed
	case []any:
		parts := make([]string, 0, len(typed))
		for _, part := range typed {
			m, _ := part.(map[string]any)
			if text := asString(m["text"]); text != "" {
				parts = append(parts, text)
				continue
			}
			if image, _ := m["image_url"].(map[string]any); image != nil {
				if url := asString(image["url"]); url != "" {
					parts = append(parts, "[image: "+url+"]")
				}
			}
		}
		return strings.Join(parts, "\n")
	case nil:
		return ""
	default:
		b, _ := json.Marshal(typed)
		return string(b)
	}
}

type deepSeekResult struct {
	Content   string
	Reasoning string
}

type deepSeekSegment struct {
	Text string
	Kind string
}

type deepSeekContinueState struct {
	SessionID         string
	ResponseMessageID int
	LastStatus        string
	Finished          bool
}

func (s *deepSeekContinueState) shouldContinue() bool {
	if s == nil || s.Finished || s.SessionID == "" || s.ResponseMessageID <= 0 {
		return false
	}
	switch strings.ToUpper(strings.TrimSpace(s.LastStatus)) {
	case "WIP", "INCOMPLETE", "AUTO_CONTINUE":
		return true
	default:
		return false
	}
}

func (s *deepSeekContinueState) prepareNext() {
	s.Finished = false
	s.LastStatus = ""
}

func consumeDeepSeekSSE(ctx context.Context, body ioReader, thinkingEnabled bool, state *deepSeekContinueState, observeRaw func([]byte)) (deepSeekResult, error) {
	var result deepSeekResult
	err := scanDeepSeekSSE(ctx, body, thinkingEnabled, state, observeRaw, func(segment deepSeekSegment) bool {
		if segment.Kind == "reasoning" {
			result.Reasoning += segment.Text
		} else {
			result.Content += segment.Text
		}
		return true
	})
	return result, err
}

type ioReader interface {
	Read([]byte) (int, error)
}

func scanDeepSeekSSE(ctx context.Context, body ioReader, thinkingEnabled bool, state *deepSeekContinueState, observeRaw func([]byte), emit func(deepSeekSegment) bool) error {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 52_428_800)
	currentType := "text"
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		if observeRaw != nil {
			observeRaw(bytes.Clone(line))
		}
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		data := strings.TrimSpace(string(bytes.TrimPrefix(line, []byte("data:"))))
		if data == "[DONE]" {
			continue
		}
		segments, nextType := parseDeepSeekSSEPayload(data, thinkingEnabled, currentType, state)
		currentType = nextType
		for _, segment := range segments {
			if !emit(segment) {
				return ctx.Err()
			}
		}
	}
	return scanner.Err()
}

func parseDeepSeekSSEPayload(data string, thinkingEnabled bool, currentType string, state *deepSeekContinueState) ([]deepSeekSegment, string) {
	var chunk map[string]any
	if err := json.Unmarshal([]byte(data), &chunk); err != nil {
		return nil, currentType
	}
	observeDeepSeekContinue(chunk, state)
	if asString(chunk["code"]) == "content_filter" {
		return []deepSeekSegment{{Text: "[content filtered]", Kind: "content"}}, currentType
	}
	path := asString(chunk["p"])
	if shouldSkipDeepSeekPath(path) {
		return nil, currentType
	}
	if isDeepSeekStatusPath(path) {
		if strings.EqualFold(asString(chunk["v"]), "FINISHED") && state != nil {
			state.Finished = true
		}
		return nil, currentType
	}
	segments := make([]deepSeekSegment, 0, 4)
	nextType := currentType
	appendDeepSeekValueSegments(chunk["v"], path, thinkingEnabled, &nextType, &segments)
	return segments, nextType
}

func appendDeepSeekValueSegments(value any, path string, thinkingEnabled bool, currentType *string, out *[]deepSeekSegment) {
	switch typed := value.(type) {
	case string:
		if typed == "" || typed == "FINISHED" {
			return
		}
		kind := kindForDeepSeekPath(path, *currentType)
		if kind == "reasoning" && !thinkingEnabled {
			*currentType = "text"
			return
		}
		*out = append(*out, deepSeekSegment{Text: typed, Kind: kind})
	case []any:
		for _, item := range typed {
			appendDeepSeekItemSegments(item, path, thinkingEnabled, currentType, out)
		}
	case map[string]any:
		if response, ok := typed["response"]; ok {
			appendDeepSeekValueSegments(response, "response", thinkingEnabled, currentType, out)
			return
		}
		if fragments, ok := typed["fragments"]; ok {
			appendDeepSeekValueSegments(fragments, "response/fragments", thinkingEnabled, currentType, out)
		}
	}
}

func appendDeepSeekItemSegments(item any, parentPath string, thinkingEnabled bool, currentType *string, out *[]deepSeekSegment) {
	m, ok := item.(map[string]any)
	if !ok {
		appendDeepSeekValueSegments(item, parentPath, thinkingEnabled, currentType, out)
		return
	}
	path := asString(m["p"])
	if path == "" {
		path = parentPath
	}
	if shouldSkipDeepSeekPath(path) || isDeepSeekStatusPath(path) {
		return
	}
	if content := asString(m["content"]); content != "" {
		kind := "content"
		typeName := strings.ToUpper(firstNonEmpty(asString(m["type"]), asString(m["fragment_type"])))
		switch typeName {
		case "THINK", "THINKING":
			kind = "reasoning"
			*currentType = "reasoning"
		case "RESPONSE":
			kind = "content"
			*currentType = "text"
		default:
			kind = kindForDeepSeekPath(path, *currentType)
		}
		if kind == "reasoning" && !thinkingEnabled {
			*currentType = "text"
			return
		}
		*out = append(*out, deepSeekSegment{Text: content, Kind: kind})
		return
	}
	if value, ok := m["v"]; ok {
		appendDeepSeekValueSegments(value, path, thinkingEnabled, currentType, out)
	}
}
