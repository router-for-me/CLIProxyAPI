package executor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
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
	ToolMode       bool
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
		ToolMode:       hasDeepSeekTools(raw["tools"]),
		PassThrough:    passThrough,
	}, nil
}

func hasDeepSeekTools(tools any) bool {
	items, ok := tools.([]any)
	return ok && len(items) > 0
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
			if toolCalls := formatDeepSeekAssistantToolCalls(msg["tool_calls"]); toolCalls != "" {
				if content != "" && !strings.HasSuffix(content, "\n") {
					b.WriteByte('\n')
				}
				b.WriteString(toolCalls)
			}
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
	b.WriteString("\nYou may call tools when needed. If a tool is needed, respond only with one or more tags exactly as <tool_call name=\"tool_name\">{\"arg\":\"value\"}</tool_call>. Do not use markdown or prose around tool calls. Available tools:\n")
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

func formatDeepSeekAssistantToolCalls(toolCalls any) string {
	items, ok := toolCalls.([]any)
	if !ok || len(items) == 0 {
		return ""
	}
	var b strings.Builder
	for _, item := range items {
		m, _ := item.(map[string]any)
		if m == nil {
			continue
		}
		fn, _ := m["function"].(map[string]any)
		name := firstNonEmpty(asString(fn["name"]), asString(m["name"]))
		if name == "" {
			continue
		}
		arguments := deepSeekArgumentsJSON(firstNonNil(fn["arguments"], m["arguments"], m["args"], map[string]any{}))
		b.WriteString(`<tool_call name="`)
		b.WriteString(escapeDeepSeekToolAttr(name))
		b.WriteString(`">`)
		b.WriteString(arguments)
		b.WriteString(`</tool_call>`)
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
	ToolCalls []deepSeekToolCall
}

type deepSeekSegment struct {
	Text     string
	Kind     string
	ToolCall *deepSeekToolCall
}

type deepSeekToolCall struct {
	ID        string
	Name      string
	Arguments string
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
	return consumeDeepSeekSSEWithToolMode(ctx, body, thinkingEnabled, false, state, observeRaw)
}

func consumeDeepSeekSSEWithToolMode(ctx context.Context, body ioReader, thinkingEnabled bool, toolMode bool, state *deepSeekContinueState, observeRaw func([]byte)) (deepSeekResult, error) {
	var result deepSeekResult
	contentParser := newDeepSeekToolCallParserWithHold(toolMode)
	reasoningParser := newDeepSeekToolCallParser()
	err := scanDeepSeekSSE(ctx, body, thinkingEnabled, state, observeRaw, func(segment deepSeekSegment) bool {
		if segment.Kind == "reasoning" {
			appendDeepSeekParsedSegments(&result, reasoningParser.PushKind(segment.Text, segment.Kind))
		} else {
			appendDeepSeekParsedSegments(&result, contentParser.PushKind(segment.Text, segment.Kind))
		}
		return true
	})
	if err != nil {
		return result, err
	}
	appendDeepSeekParsedSegments(&result, reasoningParser.Finish())
	appendDeepSeekParsedSegments(&result, contentParser.Finish())
	return result, nil
}

func appendDeepSeekParsedSegments(result *deepSeekResult, segments []deepSeekSegment) {
	for _, segment := range segments {
		if segment.Kind == "tool_call" && segment.ToolCall != nil {
			result.ToolCalls = append(result.ToolCalls, *segment.ToolCall)
			continue
		}
		if segment.Kind == "reasoning" {
			result.Reasoning += segment.Text
		} else {
			result.Content += segment.Text
		}
	}
}

const (
	deepSeekToolCallOpenTag  = "<tool_call"
	deepSeekToolCallCloseTag = "</tool_call>"
)

type deepSeekToolCallParser struct {
	buffer      string
	next        int
	holdText    bool
	heldText    []deepSeekSegment
	sawToolCall bool
	lastKind    string
}

func newDeepSeekToolCallParser() *deepSeekToolCallParser {
	return newDeepSeekToolCallParserWithHold(false)
}

func newDeepSeekToolCallParserWithHold(holdText bool) *deepSeekToolCallParser {
	return &deepSeekToolCallParser{holdText: holdText}
}

func (p *deepSeekToolCallParser) Push(text string) []deepSeekSegment {
	return p.PushKind(text, "content")
}

func (p *deepSeekToolCallParser) PushKind(text, kind string) []deepSeekSegment {
	if text == "" {
		return nil
	}
	if kind == "" {
		kind = "content"
	}
	p.lastKind = kind
	p.buffer += text
	return p.consume(false, kind)
}

func (p *deepSeekToolCallParser) Finish() []deepSeekSegment {
	kind := p.lastKind
	if kind == "" {
		kind = "content"
	}
	return p.consume(true, kind)
}

func (p *deepSeekToolCallParser) consume(flush bool, kind string) []deepSeekSegment {
	var out []deepSeekSegment
	for {
		start := indexDeepSeekToolTag(p.buffer, deepSeekToolCallOpenTag)
		if start < 0 {
			if flush {
				p.emitText(&out, p.buffer, kind)
				p.buffer = ""
				return p.finish(out)
			}
			safeLen := len(p.buffer) - deepSeekToolCallPrefixLen(p.buffer)
			if safeLen > 0 {
				p.emitText(&out, p.buffer[:safeLen], kind)
				p.buffer = p.buffer[safeLen:]
			}
			return out
		}
		if start > 0 {
			p.emitText(&out, sanitizeDeepSeekTextBeforeToolCall(p.buffer[:start]), kind)
			p.buffer = p.buffer[start:]
		}
		tagEnd := strings.IndexByte(p.buffer, '>')
		if tagEnd < 0 {
			if flush {
				p.emitText(&out, p.buffer, kind)
				p.buffer = ""
				return p.finish(out)
			}
			return out
		}
		closeRel := indexDeepSeekToolTag(p.buffer[tagEnd+1:], deepSeekToolCallCloseTag)
		if closeRel < 0 {
			if !flush {
				return out
			}
			if call, ok := parseDeepSeekToolCallBlock(p.buffer[:tagEnd+1], p.buffer[tagEnd+1:], p.next); ok {
				p.emitToolCall(&out, call)
				p.next++
			} else {
				p.emitText(&out, p.buffer, kind)
			}
			p.buffer = ""
			return p.finish(out)
		}
		closeStart := tagEnd + 1 + closeRel
		closeEnd := closeStart + len(deepSeekToolCallCloseTag)
		if call, ok := parseDeepSeekToolCallBlock(p.buffer[:tagEnd+1], p.buffer[tagEnd+1:closeStart], p.next); ok {
			p.emitToolCall(&out, call)
			p.next++
		} else {
			p.emitText(&out, p.buffer[:closeEnd], kind)
		}
		p.buffer = p.buffer[closeEnd:]
	}
}

func (p *deepSeekToolCallParser) finish(out []deepSeekSegment) []deepSeekSegment {
	if !p.holdText || p.sawToolCall || len(p.heldText) == 0 {
		return out
	}
	out = append(out, p.heldText...)
	p.heldText = nil
	return out
}

func (p *deepSeekToolCallParser) emitText(out *[]deepSeekSegment, text, kind string) {
	if text == "" {
		return
	}
	if p.holdText {
		if p.sawToolCall {
			return
		}
		p.heldText = append(p.heldText, deepSeekSegment{Text: text, Kind: kind})
		return
	}
	*out = append(*out, deepSeekSegment{Text: text, Kind: kind})
}

func (p *deepSeekToolCallParser) emitToolCall(out *[]deepSeekSegment, call deepSeekToolCall) {
	p.sawToolCall = true
	p.heldText = nil
	*out = append(*out, deepSeekSegment{Kind: "tool_call", ToolCall: &call})
}

func indexDeepSeekToolTag(text, tag string) int {
	return strings.Index(strings.ToLower(text), tag)
}

func deepSeekToolCallPrefixLen(text string) int {
	pattern := deepSeekToolCallOpenTag
	maxLen := len(pattern) - 1
	if len(text) < maxLen {
		maxLen = len(text)
	}
	for length := maxLen; length > 0; length-- {
		if strings.EqualFold(text[len(text)-length:], pattern[:length]) {
			return length
		}
	}
	return 0
}

func sanitizeDeepSeekTextBeforeToolCall(text string) string {
	trimmed := strings.ToLower(strings.TrimSpace(text))
	if trimmed == "" || trimmed == "user" || trimmed == "assistant" {
		return ""
	}
	lastNewline := strings.LastIndexAny(text, "\r\n")
	if lastNewline >= 0 {
		tail := strings.ToLower(strings.TrimSpace(text[lastNewline+1:]))
		if tail == "user" || tail == "assistant" {
			return text[:lastNewline+1]
		}
	}
	return text
}

func parseDeepSeekToolCallBlock(startTag, body string, index int) (deepSeekToolCall, bool) {
	attrs := parseDeepSeekToolCallAttrs(startTag)
	name := firstNonEmpty(attrs["name"], attrs["tool_name"], attrs["tool"], attrs["function"])
	id := firstNonEmpty(attrs["id"], attrs["call_id"])
	body = stripDeepSeekCodeFence(body)
	arguments := ""

	var payload map[string]any
	if body != "" && json.Unmarshal([]byte(body), &payload) == nil {
		fn, _ := payload["function"].(map[string]any)
		name = firstNonEmpty(name, asString(payload["name"]), asString(payload["tool_name"]), asString(payload["tool"]), asString(fn["name"]))
		id = firstNonEmpty(id, asString(payload["id"]), asString(payload["call_id"]))
		_, hasEnvelopeName := payload["name"]
		_, hasToolName := payload["tool_name"]
		_, hasTool := payload["tool"]
		if fn != nil || hasEnvelopeName || hasToolName || hasTool {
			arguments = deepSeekArgumentsJSON(firstNonNil(fn["arguments"], payload["arguments"], payload["args"], payload["parameters"], payload["input"], map[string]any{}))
		}
	}
	if childName, ok := firstDeepSeekChildTag(body, "tool_name", "name", "tool", "function"); ok {
		name = firstNonEmpty(name, childName)
	}
	if childID, ok := firstDeepSeekChildTag(body, "id", "call_id"); ok {
		id = firstNonEmpty(id, childID)
	}
	if childArgs, ok := firstDeepSeekChildTag(body, "arguments", "args", "parameters", "input", "tool_arguments", "tool_args", "tool_input"); ok {
		arguments = deepSeekArgumentsJSON(childArgs)
	} else if containsDeepSeekToolChildName(body) && arguments == "" {
		arguments = "{}"
	}
	if name == "" {
		return deepSeekToolCall{}, false
	}
	if arguments == "" {
		arguments = deepSeekArgumentsJSON(body)
	}
	if id == "" {
		id = fmt.Sprintf("call_deepseek_%d", index)
	}
	return deepSeekToolCall{ID: id, Name: name, Arguments: arguments}, true
}

func firstDeepSeekChildTag(body string, names ...string) (string, bool) {
	lower := strings.ToLower(body)
	for _, name := range names {
		name = strings.ToLower(strings.TrimSpace(name))
		if name == "" {
			continue
		}
		searchFrom := 0
		for searchFrom < len(lower) {
			openStartRel := strings.Index(lower[searchFrom:], "<"+name)
			if openStartRel < 0 {
				break
			}
			openStart := searchFrom + openStartRel
			afterName := openStart + len(name) + 1
			if afterName >= len(lower) || !isDeepSeekTagBoundary(lower[afterName]) {
				searchFrom = afterName
				continue
			}
			openEndRel := strings.IndexByte(lower[afterName:], '>')
			if openEndRel < 0 {
				break
			}
			contentStart := afterName + openEndRel + 1
			closeTag := "</" + name + ">"
			closeStartRel := strings.Index(lower[contentStart:], closeTag)
			if closeStartRel < 0 {
				break
			}
			value := strings.TrimSpace(body[contentStart : contentStart+closeStartRel])
			return html.UnescapeString(value), true
		}
	}
	return "", false
}

func containsDeepSeekToolChildName(body string) bool {
	_, ok := firstDeepSeekChildTag(body, "tool_name", "name", "tool", "function")
	return ok
}

func isDeepSeekTagBoundary(ch byte) bool {
	return ch == '>' || ch == '/' || ch == ' ' || ch == '\t' || ch == '\r' || ch == '\n'
}

func parseDeepSeekToolCallAttrs(tag string) map[string]string {
	attrs := map[string]string{}
	idx := strings.Index(strings.ToLower(tag), "tool_call")
	if idx < 0 {
		return attrs
	}
	text := strings.TrimSpace(tag[idx+len("tool_call"):])
	text = strings.TrimSuffix(text, ">")
	text = strings.TrimSuffix(strings.TrimSpace(text), "/")
	for len(text) > 0 {
		text = strings.TrimLeft(text, " \t\r\n")
		if text == "" {
			break
		}
		keyEnd := 0
		for keyEnd < len(text) {
			ch := text[keyEnd]
			if ch == '=' || ch == ' ' || ch == '\t' || ch == '\r' || ch == '\n' {
				break
			}
			keyEnd++
		}
		if keyEnd == 0 {
			break
		}
		key := strings.ToLower(strings.TrimSpace(text[:keyEnd]))
		text = strings.TrimLeft(text[keyEnd:], " \t\r\n")
		if !strings.HasPrefix(text, "=") {
			continue
		}
		text = strings.TrimLeft(text[1:], " \t\r\n")
		if text == "" {
			attrs[key] = ""
			break
		}
		var value string
		if text[0] == '"' || text[0] == '\'' {
			quote := text[0]
			end := 1
			for end < len(text) && text[end] != quote {
				end++
			}
			value = text[1:end]
			if end < len(text) {
				text = text[end+1:]
			} else {
				text = ""
			}
		} else {
			end := 0
			for end < len(text) && text[end] != ' ' && text[end] != '\t' && text[end] != '\r' && text[end] != '\n' {
				end++
			}
			value = text[:end]
			text = text[end:]
		}
		attrs[key] = html.UnescapeString(value)
	}
	return attrs
}

func deepSeekArgumentsJSON(value any) string {
	if value == nil {
		return "{}"
	}
	if text, ok := value.(string); ok {
		text = stripDeepSeekCodeFence(text)
		if text == "" {
			return "{}"
		}
		if compact, ok := compactDeepSeekJSON(text); ok {
			return compact
		}
		encoded, _ := json.Marshal(text)
		return string(encoded)
	}
	encoded, err := json.Marshal(value)
	if err != nil || len(encoded) == 0 {
		return "{}"
	}
	return string(encoded)
}

func compactDeepSeekJSON(text string) (string, bool) {
	var buf bytes.Buffer
	if err := json.Compact(&buf, []byte(text)); err != nil {
		return "", false
	}
	return buf.String(), true
}

func stripDeepSeekCodeFence(text string) string {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "```") {
		return text
	}
	if newline := strings.IndexByte(text, '\n'); newline >= 0 {
		text = text[newline+1:]
	}
	text = strings.TrimSpace(text)
	if strings.HasSuffix(text, "```") {
		text = strings.TrimSpace(strings.TrimSuffix(text, "```"))
	}
	return text
}

func escapeDeepSeekToolAttr(value string) string {
	return strings.NewReplacer("&", "&amp;", `"`, "&quot;", "<", "&lt;", ">", "&gt;").Replace(value)
}

func firstNonNil(values ...any) any {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
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
