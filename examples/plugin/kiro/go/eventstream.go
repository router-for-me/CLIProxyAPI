package main

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"strconv"
	"strings"
)

const maxEventStreamMessageSize = 16 * 1024 * 1024

type eventStreamDecoder struct {
	buffer []byte
}

func (d *eventStreamDecoder) Feed(chunk []byte) ([]kiroEvent, error) {
	if len(chunk) > 0 {
		d.buffer = append(d.buffer, chunk...)
	}
	var events []kiroEvent
	for {
		if len(d.buffer) < 12 {
			return events, nil
		}
		totalLength := int(binary.BigEndian.Uint32(d.buffer[0:4]))
		headersLength := int(binary.BigEndian.Uint32(d.buffer[4:8]))
		if totalLength < 16 || totalLength > maxEventStreamMessageSize {
			return nil, fmt.Errorf("invalid Kiro event frame length %d", totalLength)
		}
		if headersLength < 0 || headersLength > totalLength-16 {
			return nil, fmt.Errorf("invalid Kiro event header length %d", headersLength)
		}
		if crc32.ChecksumIEEE(d.buffer[:8]) != binary.BigEndian.Uint32(d.buffer[8:12]) {
			return nil, errors.New("Kiro event prelude CRC mismatch")
		}
		if len(d.buffer) < totalLength {
			return events, nil
		}
		frame := d.buffer[:totalLength]
		if crc32.ChecksumIEEE(frame[:totalLength-4]) != binary.BigEndian.Uint32(frame[totalLength-4:]) {
			return nil, errors.New("Kiro event message CRC mismatch")
		}
		headers, errHeaders := parseEventHeaders(frame[12 : 12+headersLength])
		if errHeaders != nil {
			return nil, fmt.Errorf("decode Kiro event headers: %w", errHeaders)
		}
		payloadRaw := frame[12+headersLength : totalLength-4]
		payload := make(map[string]any)
		if len(payloadRaw) > 0 {
			if errUnmarshal := json.Unmarshal(payloadRaw, &payload); errUnmarshal != nil {
				return nil, fmt.Errorf("decode Kiro event payload: %w", errUnmarshal)
			}
		}
		messageType := headers[":message-type"]
		if messageType == "error" || messageType == "exception" {
			message := firstStringField(payload, "message", "errorMessage")
			if message == "" {
				message = messageType
			}
			return nil, fmt.Errorf("Kiro upstream stream error: %s", message)
		}
		events = append(events, kiroEvent{Type: headers[":event-type"], Payload: payload})
		d.buffer = append(d.buffer[:0], d.buffer[totalLength:]...)
	}
}

func (d *eventStreamDecoder) Finish() error {
	if len(d.buffer) != 0 {
		return fmt.Errorf("incomplete Kiro event frame: %d buffered bytes", len(d.buffer))
	}
	return nil
}

func parseEventHeaders(data []byte) (map[string]string, error) {
	headers := make(map[string]string)
	for offset := 0; offset < len(data); {
		nameLength := int(data[offset])
		offset++
		if nameLength == 0 || offset+nameLength >= len(data) {
			return nil, errors.New("malformed header name")
		}
		name := string(data[offset : offset+nameLength])
		offset += nameLength
		valueType := data[offset]
		offset++
		var valueLength int
		switch valueType {
		case 0, 1:
			continue
		case 2:
			valueLength = 1
		case 3:
			valueLength = 2
		case 4:
			valueLength = 4
		case 5, 8:
			valueLength = 8
		case 9:
			valueLength = 16
		case 6, 7:
			if offset+2 > len(data) {
				return nil, errors.New("truncated variable header length")
			}
			valueLength = int(binary.BigEndian.Uint16(data[offset : offset+2]))
			offset += 2
		default:
			return nil, fmt.Errorf("unsupported header value type %d", valueType)
		}
		if offset+valueLength > len(data) {
			return nil, errors.New("truncated header value")
		}
		if valueType == 7 {
			headers[name] = string(data[offset : offset+valueLength])
		}
		offset += valueLength
	}
	return headers, nil
}

type pendingToolUse struct {
	ID        string
	Name      string
	InputJSON strings.Builder
	Generated bool
}

type pendingToolUses struct {
	byID   map[string]*pendingToolUse
	order  []string
	lastID string
}

func (p *pendingToolUses) accept(event map[string]any) ([]kiroToolUse, error) {
	id := firstStringField(event, "toolUseId", "toolUseID", "tool_use_id", "id")
	name := firstStringField(event, "name", "toolName", "tool_name")
	stop := firstBoolField(event, "stop", "isStop", "done")
	if p.byID == nil {
		p.byID = make(map[string]*pendingToolUse)
	}
	if id == "" {
		id = p.lastID
	}
	generated := false
	if id == "" && name != "" {
		id = "toolu_" + randomUUID()
		generated = true
	}
	if id == "" {
		return nil, nil
	}
	state := p.byID[id]
	if state == nil && !generated && p.lastID != "" {
		previous := p.byID[p.lastID]
		if previous != nil && previous.Generated && (name == "" || previous.Name == name) {
			oldID := previous.ID
			delete(p.byID, oldID)
			previous.ID = id
			previous.Generated = false
			p.byID[id] = previous
			for index, existing := range p.order {
				if existing == oldID {
					p.order[index] = id
					break
				}
			}
			state = previous
		}
	}
	if state == nil {
		state = &pendingToolUse{ID: id, Name: name, Generated: generated}
		p.byID[id] = state
		p.order = append(p.order, id)
	}
	if state.Name == "" {
		state.Name = name
	}
	p.lastID = id
	if fragment, ok := event["input"].(string); ok {
		state.InputJSON.WriteString(fragment)
	} else if input, ok := event["input"].(map[string]any); ok {
		raw, _ := json.Marshal(input)
		state.InputJSON.Reset()
		state.InputJSON.Write(raw)
	}
	if !stop {
		return nil, nil
	}
	tool, errFinish := finishPendingTool(state)
	if errFinish != nil {
		return nil, errFinish
	}
	p.remove(id)
	return []kiroToolUse{tool}, nil
}

func (p *pendingToolUses) flush() ([]kiroToolUse, error) {
	tools := make([]kiroToolUse, 0, len(p.order))
	for _, id := range append([]string(nil), p.order...) {
		state := p.byID[id]
		if state == nil || state.Name == "" {
			continue
		}
		tool, errFinish := finishPendingTool(state)
		if errFinish != nil {
			return nil, errFinish
		}
		tools = append(tools, tool)
	}
	p.byID = nil
	p.order = nil
	p.lastID = ""
	return tools, nil
}

func (p *pendingToolUses) remove(id string) {
	delete(p.byID, id)
	for index, existing := range p.order {
		if existing == id {
			p.order = append(p.order[:index], p.order[index+1:]...)
			break
		}
	}
	if p.lastID == id {
		p.lastID = ""
	}
}

func finishPendingTool(state *pendingToolUse) (kiroToolUse, error) {
	input := make(map[string]any)
	if state.InputJSON.Len() > 0 {
		if errUnmarshal := json.Unmarshal([]byte(state.InputJSON.String()), &input); errUnmarshal != nil {
			return kiroToolUse{}, fmt.Errorf("decode Kiro tool input: %w", errUnmarshal)
		}
	}
	return kiroToolUse{ToolUseID: state.ID, Name: state.Name, Input: input}, nil
}

func firstStringField(values map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := values[key].(string); ok && value != "" {
			return value
		}
	}
	return ""
}

func firstBoolField(values map[string]any, keys ...string) bool {
	for _, key := range keys {
		if value, ok := values[key].(bool); ok {
			return value
		}
	}
	return false
}

func readNumber(values map[string]any, keys ...string) (int, bool) {
	value, ok := readFloat(values, keys...)
	return int(value), ok
}

func readFloat(values map[string]any, keys ...string) (float64, bool) {
	for _, key := range keys {
		switch value := values[key].(type) {
		case float64:
			return value, true
		case int:
			return float64(value), true
		case int64:
			return float64(value), true
		case json.Number:
			parsed, errParse := value.Float64()
			if errParse == nil {
				return parsed, true
			}
		case string:
			parsed, errParse := strconv.ParseFloat(value, 64)
			if errParse == nil {
				return parsed, true
			}
		}
	}
	return 0, false
}

func updateUsage(event map[string]any, inputTokens, outputTokens int) (int, int) {
	candidates := []map[string]any{event}
	collectUsageMaps(event, &candidates)
	for _, candidate := range candidates {
		if value, ok := readNumber(candidate, "outputTokens", "completionTokens", "totalOutputTokens", "output_tokens", "completion_tokens", "total_output_tokens"); ok {
			outputTokens = value
		}
		if value, ok := readNumber(candidate, "inputTokens", "promptTokens", "totalInputTokens", "input_tokens", "prompt_tokens", "total_input_tokens"); ok {
			inputTokens = value
			continue
		}
		uncached, _ := readNumber(candidate, "uncachedInputTokens", "uncached_input_tokens")
		cacheRead, _ := readNumber(candidate, "cacheReadInputTokens", "cache_read_input_tokens")
		cacheWrite, _ := readNumber(candidate, "cacheWriteInputTokens", "cache_write_input_tokens", "cacheCreationInputTokens", "cache_creation_input_tokens")
		if uncached+cacheRead+cacheWrite > 0 {
			inputTokens = uncached + cacheRead + cacheWrite
			continue
		}
		if total, ok := readNumber(candidate, "totalTokens", "total_tokens"); ok && total > outputTokens {
			inputTokens = total - outputTokens
		}
	}
	return inputTokens, outputTokens
}

func collectUsageMaps(value any, candidates *[]map[string]any) {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			normalized := strings.ToLower(key)
			if normalized == "usage" || normalized == "tokenusage" || normalized == "token_usage" {
				if nested, ok := child.(map[string]any); ok {
					*candidates = append(*candidates, nested)
				}
			}
			collectUsageMaps(child, candidates)
		}
	case []any:
		for _, child := range typed {
			collectUsageMaps(child, candidates)
		}
	}
}
