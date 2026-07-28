package loguploader

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"
)

var (
	gateTimestampPattern = regexp.MustCompile(`(?m)^Timestamp:\s*([^\r\n]+)`)
	gateSectionPattern   = regexp.MustCompile(`(?m)^=== ([A-Z0-9 _]+) ===\s*$`)
	gateTitlePatterns    = []*regexp.Regexp{
		regexp.MustCompile(`(?i)\bgenerate (a )?title\b`),
		regexp.MustCompile(`(?i)\bcreate (a )?title\b`),
		regexp.MustCompile(`(?i)\bthread title\b`),
		regexp.MustCompile(`(?i)\bsession title\b`),
		regexp.MustCompile(`(?i)\bconversation title\b`),
		regexp.MustCompile(`(?i)\bsummarize (this|the) (conversation|chat|thread)\b`),
		regexp.MustCompile(`(?i)\bconversation summary\b`),
		regexp.MustCompile(`(?i)produced a summary of its thinking process`),
		regexp.MustCompile(`(?i)\bcompact(ion)?\b.*\bsummary\b`),
	}

	// toolCallTypes are model-initiated tool invocations (not outputs).
	toolCallTypes = map[string]struct{}{
		"function_call":         {},
		"custom_tool_call":      {},
		"computer_call":         {},
		"web_search_call":       {},
		"file_search_call":      {},
		"code_interpreter_call": {},
		"mcp_call":              {},
		"image_generation_call": {},
	}
	// toolOutputTypes are tool results paired by call_id.
	toolOutputTypes = map[string]struct{}{
		"function_call_output":    {},
		"custom_tool_call_output": {},
		"computer_call_output":    {},
	}
	// toolTypesAny counts toward "at least one tool call" (calls + outputs + name heuristic).
	toolTypesAny = map[string]struct{}{
		"function_call":           {},
		"function_call_output":    {},
		"custom_tool_call":        {},
		"custom_tool_call_output": {},
		"computer_call":           {},
		"computer_call_output":    {},
		"web_search_call":         {},
		"file_search_call":        {},
		"code_interpreter_call":   {},
		"mcp_call":                {},
		"image_generation_call":   {},
	}
	toolNames = map[string]struct{}{
		"exec":        {},
		"shell":       {},
		"apply_patch": {},
		"update_plan": {},
		"read_file":   {},
		"write_file":  {},
		"grep_files":  {},
		"list_dir":    {},
	}
)

// gateFileMetrics is the session-gate view of one settled .log file.
type gateFileMetrics struct {
	Source       sourceLog
	SessionID    string
	ThreadID     string
	RequestKind  string
	Timestamp    time.Time
	InputLen     int
	PromptRounds int
	ToolCalls    int
	// UnpairedToolCalls is true when call_id pairing fails on this file's input.
	UnpairedToolCalls bool
	// LastResponseType is the last completed RESPONSE output item type, if parsed.
	LastResponseType string
	// ResponseParsed is true when a RESPONSE tail type was recovered.
	ResponseParsed bool
	// ParseError is non-empty when REQUEST BODY could not be scored.
	ParseError string
}

func parseGateFile(source sourceLog, location *time.Location, rules SessionGateConfig) gateFileMetrics {
	m := gateFileMetrics{
		Source:    source,
		Timestamp: source.Timestamp,
	}
	raw, errRead := os.ReadFile(source.Path)
	if errRead != nil {
		m.ParseError = "read failed: " + errRead.Error()
		return m
	}
	text := string(raw)
	if ts := extractGateTimestamp(text, location); !ts.IsZero() {
		m.Timestamp = ts
	}

	headers := parseGateHeaders(text)
	meta := parseGateTurnMetadata(headers["x-codex-turn-metadata"])
	m.SessionID = firstNonEmptyGate(meta["session_id"], headers["session-id"])
	m.ThreadID = firstNonEmptyGate(meta["thread_id"], headers["thread-id"])
	m.RequestKind = meta["request_kind"]
	// Do not invent session_id from path; empty stays empty for rule 2.

	bodyRaw := extractGateSection(text, "REQUEST BODY")
	if bodyRaw == "" {
		m.ParseError = "missing REQUEST BODY section"
		// Still try response tail for rule 3.
		if lastType, ok := lastResponseOutputType(text); ok {
			m.LastResponseType = lastType
			m.ResponseParsed = true
		}
		if m.ToolCalls == 0 && (strings.Contains(text, `"function_call"`) || strings.Contains(text, `"custom_tool_call"`)) {
			m.ToolCalls = 1
		}
		return m
	}
	body, errBody := decodeGateJSONObject(bodyRaw)
	if errBody != nil {
		m.ParseError = "invalid REQUEST BODY JSON: " + errBody.Error()
		if lastType, ok := lastResponseOutputType(text); ok {
			m.LastResponseType = lastType
			m.ResponseParsed = true
		}
		return m
	}
	input, _ := body["input"].([]any)
	m.InputLen = len(input)
	scored := scoreGateInput(input, m.RequestKind, rules)
	m.PromptRounds = scored.promptRounds
	m.ToolCalls = scored.toolCalls
	m.UnpairedToolCalls = scored.unpaired
	if m.ToolCalls == 0 && (strings.Contains(text, `"function_call"`) || strings.Contains(text, `"custom_tool_call"`)) {
		m.ToolCalls = 1
	}
	if lastType, ok := lastResponseOutputType(text); ok {
		m.LastResponseType = lastType
		m.ResponseParsed = true
	}
	return m
}

type scoredInput struct {
	promptRounds int
	toolCalls    int
	unpaired     bool
}

func scoreGateInput(input []any, requestKind string, rules SessionGateConfig) scoredInput {
	var out scoredInput
	calls := make(map[string]struct{})
	outputs := make(map[string]struct{})
	emptyCallOrOutput := false

	for _, rawItem := range input {
		item, ok := rawItem.(map[string]any)
		if !ok {
			continue
		}
		typeName := stringFieldGate(item, "type")
		role := stringFieldGate(item, "role")
		name := stringFieldGate(item, "name")
		text := contentTextGate(item)

		if _, isAny := toolTypesAny[typeName]; isAny {
			out.toolCalls++
		} else if _, isName := toolNames[name]; isName {
			out.toolCalls++
		}

		if _, isCall := toolCallTypes[typeName]; isCall {
			callID := strings.TrimSpace(stringFieldGate(item, "call_id"))
			if callID == "" {
				emptyCallOrOutput = true
			} else {
				calls[callID] = struct{}{}
			}
		}
		if _, isOut := toolOutputTypes[typeName]; isOut {
			callID := strings.TrimSpace(stringFieldGate(item, "call_id"))
			if callID == "" {
				emptyCallOrOutput = true
			} else {
				outputs[callID] = struct{}{}
			}
		}

		if isRealUserPromptGate(typeName, role, text, requestKind, rules) {
			out.promptRounds++
		}
	}

	if emptyCallOrOutput {
		out.unpaired = true
	}
	for id := range calls {
		if _, ok := outputs[id]; !ok {
			out.unpaired = true
			break
		}
	}
	if !out.unpaired {
		for id := range outputs {
			if _, ok := calls[id]; !ok {
				out.unpaired = true
				break
			}
		}
	}
	return out
}

func isRealUserPromptGate(typeName, role, text, requestKind string, rules SessionGateConfig) bool {
	if role != "user" {
		return false
	}
	if typeName != "" && typeName != "message" {
		return false
	}
	if strings.TrimSpace(text) == "" {
		return false
	}
	if rules.ExcludeIDEContext && isIDEContextGate(text) {
		return false
	}
	if rules.ExcludeEnvContext && isEnvContextGate(text) {
		return false
	}
	if rules.ExcludeTitleSummary && isTitleOrSummaryGate(text, requestKind) {
		return false
	}
	return true
}

func isIDEContextGate(text string) bool {
	return strings.HasPrefix(strings.TrimLeft(text, " \t\r\n"), "# Context from my IDE setup")
}

func isEnvContextGate(text string) bool {
	trimmed := strings.TrimLeft(text, " \t\r\n")
	if strings.HasPrefix(trimmed, "<environment_context>") {
		return true
	}
	limit := 200
	if len(trimmed) < limit {
		limit = len(trimmed)
	}
	return strings.Contains(trimmed[:limit], "<environment_context>")
}

func isTitleOrSummaryGate(text, requestKind string) bool {
	rk := strings.ToLower(requestKind)
	if strings.Contains(rk, "title") || strings.Contains(rk, "summary") || strings.Contains(rk, "compact") {
		return true
	}
	for _, pat := range gateTitlePatterns {
		if pat.MatchString(text) {
			return true
		}
	}
	return false
}

func isTitleOrSummaryRequestKind(requestKind string) bool {
	rk := strings.ToLower(strings.TrimSpace(requestKind))
	return strings.Contains(rk, "title") || strings.Contains(rk, "summary")
}

func isCompactionRequestKind(requestKind string) bool {
	rk := strings.ToLower(strings.TrimSpace(requestKind))
	return rk != "" && strings.Contains(rk, "compact")
}

func isToolCallType(typeName string) bool {
	_, ok := toolCallTypes[strings.ToLower(strings.TrimSpace(typeName))]
	return ok
}

// lastResponseOutputType recovers the last completed output item type from RESPONSE/SSE.
func lastResponseOutputType(logText string) (string, bool) {
	sections := []string{
		extractGateSection(logText, "API RESPONSE 1"),
		extractGateSection(logText, "API RESPONSE"),
		extractGateSection(logText, "RESPONSE"),
	}
	var lastType string
	found := false
	for _, section := range sections {
		if strings.TrimSpace(section) == "" {
			continue
		}
		// Prefer response.completed output array.
		for _, line := range strings.Split(section, "\n") {
			payload := ssePayload(line)
			if payload == "" {
				continue
			}
			var obj map[string]any
			if err := json.Unmarshal([]byte(payload), &obj); err != nil {
				continue
			}
			switch stringFieldGate(obj, "type") {
			case "response.output_item.done":
				if item, ok := obj["item"].(map[string]any); ok {
					if t := stringFieldGate(item, "type"); t != "" {
						lastType = t
						found = true
					}
				}
			case "response.completed":
				if t := lastTypeFromCompleted(obj); t != "" {
					lastType = t
					found = true
				}
			}
		}
		// Non-SSE JSON body fallback.
		if !found {
			if body, err := decodeGateJSONObject(section); err == nil {
				if t := lastTypeFromCompleted(body); t != "" {
					lastType = t
					found = true
				} else if output, ok := body["output"].([]any); ok && len(output) > 0 {
					if item, ok := output[len(output)-1].(map[string]any); ok {
						if t := stringFieldGate(item, "type"); t != "" {
							lastType = t
							found = true
						}
					}
				}
			}
		}
	}
	return lastType, found
}

func lastTypeFromCompleted(obj map[string]any) string {
	resp, ok := obj["response"].(map[string]any)
	if !ok {
		// completed event sometimes is the response itself
		if output, ok := obj["output"].([]any); ok && len(output) > 0 {
			if item, ok := output[len(output)-1].(map[string]any); ok {
				return stringFieldGate(item, "type")
			}
		}
		return ""
	}
	output, ok := resp["output"].([]any)
	if !ok || len(output) == 0 {
		return ""
	}
	if item, ok := output[len(output)-1].(map[string]any); ok {
		return stringFieldGate(item, "type")
	}
	return ""
}

func ssePayload(line string) string {
	line = strings.TrimSpace(line)
	if line == "" {
		return ""
	}
	if strings.HasPrefix(strings.ToLower(line), "data:") {
		line = strings.TrimSpace(line[5:])
	}
	if line == "" || line == "[DONE]" {
		return ""
	}
	if !strings.HasPrefix(line, "{") {
		return ""
	}
	return line
}

func extractGateTimestamp(text string, location *time.Location) time.Time {
	if match := gateTimestampPattern.FindStringSubmatch(text); len(match) == 2 {
		if parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(match[1])); err == nil {
			return parsed.In(location)
		}
		if parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(match[1])); err == nil {
			return parsed.In(location)
		}
	}
	return time.Time{}
}

func parseGateHeaders(text string) map[string]string {
	section := extractGateSection(text, "HEADERS")
	out := make(map[string]string)
	for _, line := range strings.Split(section, "\n") {
		line = strings.TrimRight(line, "\r")
		if idx := strings.Index(line, ": "); idx > 0 {
			key := strings.ToLower(strings.TrimSpace(line[:idx]))
			val := strings.TrimSpace(line[idx+2:])
			out[key] = val
		}
	}
	return out
}

func parseGateTurnMetadata(raw string) map[string]string {
	out := make(map[string]string)
	if strings.TrimSpace(raw) == "" {
		return out
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(raw), &obj); err != nil {
		return out
	}
	for _, key := range []string{"session_id", "thread_id", "request_kind", "turn_id"} {
		if v, ok := obj[key]; ok {
			out[key] = fmt.Sprint(v)
		}
	}
	return out
}

func extractGateSection(text, name string) string {
	indices := gateSectionPattern.FindAllStringSubmatchIndex(text, -1)
	if len(indices) == 0 {
		marker := "=== " + name + " ===\n"
		start := strings.Index(text, marker)
		if start < 0 {
			marker = "=== " + name + " ===\r\n"
			start = strings.Index(text, marker)
			if start < 0 {
				return ""
			}
		}
		start += len(marker)
		rest := text[start:]
		if next := strings.Index(rest, "\n=== "); next >= 0 {
			return strings.TrimSpace(rest[:next])
		}
		return strings.TrimSpace(rest)
	}
	for i, loc := range indices {
		secName := strings.TrimSpace(text[loc[2]:loc[3]])
		if !strings.EqualFold(secName, name) {
			continue
		}
		bodyStart := loc[1]
		bodyEnd := len(text)
		if i+1 < len(indices) {
			bodyEnd = indices[i+1][0]
		}
		return strings.TrimSpace(text[bodyStart:bodyEnd])
	}
	return ""
}

func decodeGateJSONObject(raw string) (map[string]any, error) {
	raw = strings.TrimSpace(raw)
	var obj map[string]any
	if err := json.Unmarshal([]byte(raw), &obj); err == nil {
		return obj, nil
	}
	i := strings.Index(raw, "{")
	j := strings.LastIndex(raw, "}")
	if i >= 0 && j > i {
		if err := json.Unmarshal([]byte(raw[i:j+1]), &obj); err == nil {
			return obj, nil
		}
	}
	return nil, fmt.Errorf("unable to decode JSON object")
}

func contentTextGate(item map[string]any) string {
	if v, ok := item["content"]; ok {
		switch content := v.(type) {
		case string:
			return content
		case []any:
			var parts []string
			for _, part := range content {
				switch p := part.(type) {
				case string:
					parts = append(parts, p)
				case map[string]any:
					if t := stringFieldGate(p, "text"); t != "" {
						parts = append(parts, t)
					} else if t := stringFieldGate(p, "input"); t != "" {
						parts = append(parts, t)
					}
				}
			}
			return strings.Join(parts, "\n")
		}
	}
	return stringFieldGate(item, "text")
}

func stringFieldGate(obj map[string]any, key string) string {
	v, ok := obj[key]
	if !ok || v == nil {
		return ""
	}
	switch t := v.(type) {
	case string:
		return t
	default:
		return fmt.Sprint(t)
	}
}

func firstNonEmptyGate(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
