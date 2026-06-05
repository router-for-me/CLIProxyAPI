package deepseek

import (
	"encoding/json"
	"fmt"
	"strings"
)

type ContentPart struct {
	Text string
	Type string
}

type LineResult struct {
	Parsed            bool
	Stop              bool
	ContentFilter     bool
	ErrorMessage      string
	Parts             []ContentPart
	NextType          string
	ResponseMessageID int
}

func ParseSSELine(raw []byte, thinkingEnabled bool, currentType string) LineResult {
	line := strings.TrimSpace(string(raw))
	if line == "" || !strings.HasPrefix(line, "data:") {
		return LineResult{NextType: currentType}
	}
	dataStr := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
	if dataStr == "[DONE]" {
		return LineResult{Parsed: true, Stop: true, NextType: currentType}
	}
	var chunk map[string]any
	if err := json.Unmarshal([]byte(dataStr), &chunk); err != nil {
		return LineResult{NextType: currentType}
	}
	if errObj, hasErr := chunk["error"]; hasErr {
		return LineResult{Parsed: true, Stop: true, ErrorMessage: fmt.Sprintf("%v", errObj), NextType: currentType}
	}
	if code, _ := chunk["code"].(string); strings.EqualFold(code, "content_filter") {
		return LineResult{Parsed: true, Stop: true, ContentFilter: true, NextType: currentType}
	}
	nextType := currentType
	parts := make([]ContentPart, 0, 4)
	stop := collectPartsFromChunk(chunk, thinkingEnabled, &nextType, &parts)
	respID := intFromAny(chunk["response_message_id"])
	if respID == 0 {
		respID = responseMessageIDFromValue(chunk["v"])
	}
	return LineResult{
		Parsed:            true,
		Stop:              stop,
		Parts:             parts,
		NextType:          nextType,
		ResponseMessageID: respID,
	}
}

func collectPartsFromChunk(chunk map[string]any, thinkingEnabled bool, currentType *string, parts *[]ContentPart) bool {
	path := strings.TrimSpace(stringFromAny(chunk["p"]))
	if shouldSkipPath(path) {
		return false
	}
	value, hasValue := chunk["v"]
	if !hasValue {
		if responseID := intFromAny(chunk["response_message_id"]); responseID > 0 {
			_ = responseID
		}
		return false
	}
	if isFinishedValue(path, value) {
		return true
	}
	switch path {
	case "response/content":
		*currentType = "text"
		appendPart(parts, value, "text")
		return false
	case "response/thinking_content":
		if thinkingEnabled {
			*currentType = "thinking"
			appendPart(parts, value, "thinking")
		}
		return false
	case "response/fragments":
		collectFragments(value, thinkingEnabled, currentType, parts)
		return false
	case "response":
		return collectResponseValue(value, thinkingEnabled, currentType, parts)
	default:
		if strings.Contains(path, "response/fragments") && strings.Contains(path, "/content") {
			partType := *currentType
			if partType == "" {
				partType = "text"
			}
			if partType == "thinking" && !thinkingEnabled {
				return false
			}
			appendPart(parts, value, partType)
			return false
		}
		return collectResponseValue(value, thinkingEnabled, currentType, parts)
	}
}

func shouldSkipPath(path string) bool {
	if path == "" {
		return false
	}
	if path == "response/search_status" {
		return true
	}
	skipContains := []string{
		"quasi_status",
		"elapsed_secs",
		"token_usage",
		"pending_fragment",
		"conversation_mode",
		"fragments/-1/status",
		"fragments/-2/status",
		"fragments/-3/status",
	}
	for _, marker := range skipContains {
		if strings.Contains(path, marker) {
			return true
		}
	}
	if strings.HasPrefix(path, "response/fragments/") && strings.HasSuffix(path, "/status") {
		return true
	}
	return false
}

func isFinishedValue(path string, value any) bool {
	if strings.EqualFold(strings.TrimSpace(stringFromAny(value)), "FINISHED") {
		return path == "" || path == "status" || path == "response/status"
	}
	if m, ok := value.(map[string]any); ok {
		if response, okResp := m["response"].(map[string]any); okResp {
			return isFinishedStatus(response["status"])
		}
		return isFinishedStatus(m["status"])
	}
	return false
}

func isFinishedStatus(value any) bool {
	status := strings.ToUpper(strings.TrimSpace(stringFromAny(value)))
	return status == "FINISHED" || status == "DONE"
}

func collectResponseValue(value any, thinkingEnabled bool, currentType *string, parts *[]ContentPart) bool {
	switch v := value.(type) {
	case map[string]any:
		if response, ok := v["response"].(map[string]any); ok {
			if isFinishedStatus(response["status"]) {
				collectResponseMap(response, thinkingEnabled, currentType, parts)
				return true
			}
			collectResponseMap(response, thinkingEnabled, currentType, parts)
			return false
		}
		collectResponseMap(v, thinkingEnabled, currentType, parts)
	case []any:
		for _, item := range v {
			switch m := item.(type) {
			case map[string]any:
				if p := strings.TrimSpace(stringFromAny(m["p"])); p != "" {
					if collectPartsFromChunk(m, thinkingEnabled, currentType, parts) {
						return true
					}
					continue
				}
				collectResponseMap(m, thinkingEnabled, currentType, parts)
			case string:
				appendTypedString(parts, m, currentType, thinkingEnabled)
			}
		}
	case string:
		appendTypedString(parts, v, currentType, thinkingEnabled)
	}
	return false
}

func collectResponseMap(response map[string]any, thinkingEnabled bool, currentType *string, parts *[]ContentPart) {
	if fragments, ok := response["fragments"]; ok {
		collectFragments(fragments, thinkingEnabled, currentType, parts)
	}
	if thinking := stringFromAny(response["thinking_content"]); thinking != "" && thinkingEnabled {
		*currentType = "thinking"
		*parts = append(*parts, ContentPart{Text: thinking, Type: "thinking"})
	}
	if content := stringFromAny(response["content"]); content != "" {
		*currentType = "text"
		*parts = append(*parts, ContentPart{Text: content, Type: "text"})
	}
}

func collectFragments(value any, thinkingEnabled bool, currentType *string, parts *[]ContentPart) {
	fragments, ok := value.([]any)
	if !ok {
		return
	}
	for _, item := range fragments {
		fragment, ok := item.(map[string]any)
		if !ok {
			continue
		}
		fragType := strings.ToUpper(strings.TrimSpace(stringFromAny(fragment["type"])))
		if fragType == "" {
			fragType = strings.ToUpper(strings.TrimSpace(stringFromAny(fragment["fragment_type"])))
		}
		content := stringFromAny(fragment["content"])
		if content == "" {
			continue
		}
		switch fragType {
		case "THINK", "THINKING":
			*currentType = "thinking"
			if thinkingEnabled {
				*parts = append(*parts, ContentPart{Text: content, Type: "thinking"})
			}
		case "RESPONSE":
			*currentType = "text"
			*parts = append(*parts, ContentPart{Text: content, Type: "text"})
		default:
			partType := *currentType
			if partType == "" || partType == "thinking" && !thinkingEnabled {
				partType = "text"
			}
			*parts = append(*parts, ContentPart{Text: content, Type: partType})
		}
	}
}

func appendPart(parts *[]ContentPart, value any, partType string) {
	text := stringFromAny(value)
	if text == "" {
		return
	}
	*parts = append(*parts, ContentPart{Text: text, Type: partType})
}

func appendTypedString(parts *[]ContentPart, text string, currentType *string, thinkingEnabled bool) {
	if text == "" || isFinishedStatus(text) {
		return
	}
	partType := *currentType
	if partType == "" {
		partType = "text"
	}
	if partType == "thinking" && !thinkingEnabled {
		return
	}
	*parts = append(*parts, ContentPart{Text: text, Type: partType})
}

func responseMessageIDFromValue(value any) int {
	switch v := value.(type) {
	case map[string]any:
		if response, ok := v["response"].(map[string]any); ok {
			if id := intFromAny(response["message_id"]); id > 0 {
				return id
			}
		}
		return intFromAny(v["message_id"])
	case []any:
		for _, item := range v {
			if id := responseMessageIDFromValue(item); id > 0 {
				return id
			}
		}
	}
	return 0
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
