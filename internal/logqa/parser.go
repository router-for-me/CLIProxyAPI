package logqa

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var (
	timestampPattern = regexp.MustCompile(`(?m)^Timestamp:\s*([^\r\n]+)`)
	sectionPattern   = regexp.MustCompile(`(?m)^=== ([A-Z0-9 _]+) ===\s*$`)
)

// parseLogFile reads a source log and produces a RequestRecord.
func parseLogFile(logsRoot, path string, info os.FileInfo, location *time.Location, rules RulesConfig) (RequestRecord, error) {
	relative, errRel := filepath.Rel(logsRoot, path)
	if errRel != nil {
		return RequestRecord{}, fmt.Errorf("relative path: %w", errRel)
	}
	relative = filepath.ToSlash(relative)
	parts := strings.Split(relative, "/")
	keyName := "unknown"
	if len(parts) >= 1 {
		keyName = parts[0]
	}

	raw, errRead := os.ReadFile(path)
	if errRead != nil {
		return RequestRecord{}, errRead
	}
	text := string(raw)

	rec := RequestRecord{
		SourceFile:  relative,
		KeyName:     keyName,
		Fingerprint: fingerprint(relative, info.Size(), info.ModTime()),
		ModTime:     info.ModTime(),
		SizeBytes:   info.Size(),
		Timestamp:   extractTimestamp(text, info.ModTime(), location),
	}

	headers := parseHeadersSection(text)
	meta := parseTurnMetadata(headers["x-codex-turn-metadata"])
	rec.SessionID = firstNonEmpty(meta["session_id"], headers["session-id"])
	rec.ThreadID = firstNonEmpty(meta["thread_id"], headers["thread-id"])
	rec.RequestKind = meta["request_kind"]

	bodyRaw := extractSection(text, "REQUEST BODY")
	if bodyRaw == "" {
		rec.ParseError = "missing REQUEST BODY section"
		return rec, nil
	}
	body, errBody := decodeJSONObject(bodyRaw)
	if errBody != nil {
		rec.ParseError = "invalid REQUEST BODY JSON: " + errBody.Error()
		return rec, nil
	}

	input, _ := body["input"].([]any)
	rec.InputLen = len(input)
	metrics := scoreInput(input, rec.RequestKind, rules)
	// Response fallback for tools if none in input
	if metrics.ToolCalls == 0 && (strings.Contains(text, `"function_call"`) || strings.Contains(text, `"custom_tool_call"`)) {
		metrics.ToolCalls = 1
	}
	rec.PromptRounds = metrics.PromptRounds
	rec.ToolCalls = metrics.ToolCalls
	rec.DupAssistant = metrics.DupAssistant
	rec.UnpairedToolCalls = metrics.UnpairedToolCalls
	rec.SamplePrompts = metrics.SamplePrompts
	rec.AssistantTexts = metrics.AssistantTexts
	if lastType, ok := lastResponseOutputType(text); ok {
		rec.LastResponseType = lastType
		rec.ResponseParsed = true
	}
	if title, source := resolveRequestTitle(rec.RequestKind, input, text); title != "" {
		rec.Title = title
		rec.TitleSource = source
	}

	// Preserve whether a real session_id existed before synthetic fallback for display keys.
	rec.HasRealSessionID = rec.SessionID != ""
	if rec.SessionID == "" {
		if rec.ThreadID != "" {
			// Thread-only ids still lack a real session_id for upload gate rule 2.
			rec.SessionID = rec.ThreadID
		} else {
			rec.SessionID = "unknown:" + relative
		}
	}
	return rec, nil
}

// lastResponseOutputType recovers the last completed output item type from RESPONSE/SSE.
func lastResponseOutputType(logText string) (string, bool) {
	sections := []string{
		extractSection(logText, "API RESPONSE 1"),
		extractSection(logText, "API RESPONSE"),
		extractSection(logText, "RESPONSE"),
	}
	var lastType string
	found := false
	for _, section := range sections {
		if strings.TrimSpace(section) == "" {
			continue
		}
		for _, line := range strings.Split(section, "\n") {
			payload := sseJSONPayload(line)
			if payload == "" {
				continue
			}
			var obj map[string]any
			if err := json.Unmarshal([]byte(payload), &obj); err != nil {
				continue
			}
			switch stringField(obj, "type") {
			case "response.output_item.done":
				if item, ok := obj["item"].(map[string]any); ok {
					if t := stringField(item, "type"); t != "" {
						lastType = t
						found = true
					}
				}
			case "response.completed":
				if t := lastTypeFromCompletedResponse(obj); t != "" {
					lastType = t
					found = true
				}
			}
		}
		if !found {
			if body, err := decodeJSONObject(section); err == nil {
				if t := lastTypeFromCompletedResponse(body); t != "" {
					lastType = t
					found = true
				}
			}
		}
	}
	return lastType, found
}

func lastTypeFromCompletedResponse(obj map[string]any) string {
	if resp, ok := obj["response"].(map[string]any); ok {
		if output, ok := resp["output"].([]any); ok && len(output) > 0 {
			if item, ok := output[len(output)-1].(map[string]any); ok {
				return stringField(item, "type")
			}
		}
		return ""
	}
	if output, ok := obj["output"].([]any); ok && len(output) > 0 {
		if item, ok := output[len(output)-1].(map[string]any); ok {
			return stringField(item, "type")
		}
	}
	return ""
}

func sseJSONPayload(line string) string {
	line = strings.TrimSpace(line)
	if line == "" {
		return ""
	}
	if strings.HasPrefix(strings.ToLower(line), "data:") {
		line = strings.TrimSpace(line[5:])
	}
	if line == "" || line == "[DONE]" || !strings.HasPrefix(line, "{") {
		return ""
	}
	return line
}

func fingerprint(relative string, size int64, modTime time.Time) string {
	return fmt.Sprintf("%s|%d|%d", relative, size, modTime.UnixNano())
}

func extractTimestamp(text string, fallback time.Time, location *time.Location) time.Time {
	if match := timestampPattern.FindStringSubmatch(text); len(match) == 2 {
		if parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(match[1])); err == nil {
			return parsed.In(location)
		}
		if parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(match[1])); err == nil {
			return parsed.In(location)
		}
	}
	return fallback.In(location)
}

func parseHeadersSection(text string) map[string]string {
	section := extractSection(text, "HEADERS")
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

func parseTurnMetadata(raw string) map[string]string {
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

func extractSection(text, name string) string {
	// Prefer regex split on section headers
	indices := sectionPattern.FindAllStringSubmatchIndex(text, -1)
	if len(indices) == 0 {
		// fallback legacy search
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
		// loc: full start, full end, name start, name end
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

func decodeJSONObject(raw string) (map[string]any, error) {
	raw = strings.TrimSpace(raw)
	var obj map[string]any
	if err := json.Unmarshal([]byte(raw), &obj); err == nil {
		return obj, nil
	}
	// trim to outermost object
	i := strings.Index(raw, "{")
	j := strings.LastIndex(raw, "}")
	if i >= 0 && j > i {
		if err := json.Unmarshal([]byte(raw[i:j+1]), &obj); err == nil {
			return obj, nil
		}
	}
	return nil, fmt.Errorf("unable to decode JSON object")
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
