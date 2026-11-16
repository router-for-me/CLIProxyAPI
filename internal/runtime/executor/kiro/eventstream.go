// Package kiro provides AWS EventStream decoding and execution logic for Kiro AI.
package kiro

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"strconv"
	"strings"

	"github.com/google/uuid"
)

var (
	ErrInvalidPrelude          = errors.New("eventstream: invalid prelude CRC32 checksum")
	ErrInvalidMessageCRC       = errors.New("eventstream: invalid message CRC32 checksum")
	ErrMessageTooShort         = errors.New("eventstream: message length is less than minimum 16 bytes")
	ErrUnexpectedEOF           = errors.New("eventstream: unexpected EOF while reading frame")
	ErrMessageTooLarge         = errors.New("eventstream: message length exceeds maximum size")
	ErrEmptyKiroStream         = errors.New("kiro eventstream: stream ended before output")
	ErrIncompleteKiroToolInput = errors.New("kiro eventstream: incomplete tool input")
	ErrKiroEventStreamUpstream = errors.New("kiro eventstream: upstream error")
)

const maxEventStreamMessageSize = 16 * 1024 * 1024

// EventStreamFrame represents a decoded AWS EventStream binary message.
type EventStreamFrame struct {
	Headers map[string]string
	Payload []byte
}

// EventType returns the :event-type header value if present.
func (f *EventStreamFrame) EventType() string {
	if f.Headers == nil {
		return ""
	}
	return f.Headers[":event-type"]
}

// MessageType returns the :message-type header value if present.
func (f *EventStreamFrame) MessageType() string {
	if f.Headers == nil {
		return ""
	}
	return f.Headers[":message-type"]
}

// ExceptionType returns the :exception-type header value if present.
func (f *EventStreamFrame) ExceptionType() string {
	if f.Headers == nil {
		return ""
	}
	return f.Headers[":exception-type"]
}

// ReadEventStreamFrame attempts to read and decode one EventStream frame from reader.
func ReadEventStreamFrame(r io.Reader) (*EventStreamFrame, error) {
	// Read 8-byte prelude: TotalLength (4), HeadersLength (4)
	var prelude [8]byte
	if _, err := io.ReadFull(r, prelude[:]); err != nil {
		return nil, err
	}

	totalLen := binary.BigEndian.Uint32(prelude[0:4])
	headersLen := binary.BigEndian.Uint32(prelude[4:8])

	if totalLen < 16 {
		return nil, ErrMessageTooShort
	}
	if totalLen > maxEventStreamMessageSize {
		return nil, ErrMessageTooLarge
	}
	if headersLen > totalLen-16 {
		return nil, ErrInvalidPrelude
	}

	// Read Prelude CRC (4 bytes)
	var preludeCRCBytes [4]byte
	if _, err := io.ReadFull(r, preludeCRCBytes[:]); err != nil {
		return nil, ErrUnexpectedEOF
	}
	preludeCRC := binary.BigEndian.Uint32(preludeCRCBytes[:])

	// Validate Prelude CRC32 (IEEE)
	expectedPreludeCRC := crc32.ChecksumIEEE(prelude[:])
	if preludeCRC != expectedPreludeCRC {
		return nil, ErrInvalidPrelude
	}

	// Read remainder of message: headersLen + payloadLen + 4 bytes Message CRC
	remainderLen := totalLen - 12 // 8 (prelude) + 4 (preludeCRC)
	remainder := make([]byte, remainderLen)
	if _, err := io.ReadFull(r, remainder); err != nil {
		return nil, ErrUnexpectedEOF
	}

	// Message CRC is last 4 bytes of remainder
	messageCRC := binary.BigEndian.Uint32(remainder[remainderLen-4:])

	// Validate Message CRC32
	// Message CRC covers total message minus last 4 bytes (prelude + preludeCRC + remainder excluding last 4 bytes)
	fullData := append(append(prelude[:], preludeCRCBytes[:]...), remainder[:remainderLen-4]...)
	expectedMessageCRC := crc32.ChecksumIEEE(fullData)
	if messageCRC != expectedMessageCRC {
		return nil, ErrInvalidMessageCRC
	}

	// Extract Headers and Payload
	headersBytes := remainder[:headersLen]
	payloadBytes := remainder[headersLen : remainderLen-4]

	headers := parseEventStreamHeaders(headersBytes)

	return &EventStreamFrame{
		Headers: headers,
		Payload: payloadBytes,
	}, nil
}

// parseEventStreamHeaders parses the AWS EventStream header section into a key-value map.
func parseEventStreamHeaders(b []byte) map[string]string {
	headers := make(map[string]string)
	buf := bytes.NewReader(b)

	for buf.Len() > 0 {
		nameLenByte, err := buf.ReadByte()
		if err != nil {
			break
		}
		nameLen := int(nameLenByte)
		if buf.Len() < nameLen {
			break
		}

		nameBytes := make([]byte, nameLen)
		if _, err := buf.Read(nameBytes); err != nil {
			break
		}
		headerName := string(nameBytes)

		valueTypeByte, err := buf.ReadByte()
		if err != nil {
			break
		}
		valueType := int(valueTypeByte)

		// Type 7 = String (2 bytes length + string value)
		switch valueType {
		case 7: // String
			var strLen uint16
			if err := binary.Read(buf, binary.BigEndian, &strLen); err != nil {
				break
			}
			strBytes := make([]byte, strLen)
			if _, err := buf.Read(strBytes); err != nil {
				break
			}
			headers[headerName] = string(strBytes)
		case 0, 1: // True, False
			headers[headerName] = fmt.Sprintf("%t", valueType == 0)
		case 2: // Byte
			v, _ := buf.ReadByte()
			headers[headerName] = fmt.Sprintf("%d", v)
		case 3: // Short
			var v int16
			_ = binary.Read(buf, binary.BigEndian, &v)
			headers[headerName] = fmt.Sprintf("%d", v)
		case 4: // Integer
			var v int32
			_ = binary.Read(buf, binary.BigEndian, &v)
			headers[headerName] = fmt.Sprintf("%d", v)
		case 5, 8: // Long, Timestamp
			var v int64
			_ = binary.Read(buf, binary.BigEndian, &v)
			headers[headerName] = fmt.Sprintf("%d", v)
		case 6: // Byte Array
			var arrLen uint16
			_ = binary.Read(buf, binary.BigEndian, &arrLen)
			arr := make([]byte, arrLen)
			_, _ = buf.Read(arr)
			headers[headerName] = string(arr)
		case 9: // UUID (16 bytes)
			uuidBytes := make([]byte, 16)
			_, _ = buf.Read(uuidBytes)
			headers[headerName] = fmt.Sprintf("%x", uuidBytes)
		default:
			// Unknown type, stop parsing headers
			return headers
		}
	}

	return headers
}

// AssistantResponseEvent structure parsed from JSON payload of assistantResponseEvent
type AssistantResponseEvent struct {
	Content          string `json:"content,omitempty"`
	ReasoningContent string `json:"reasoningContent,omitempty"`
}

// ParseAssistantEvent attempts to unmarshal payload as AssistantResponseEvent or raw event JSON.
func (f *EventStreamFrame) ParseAssistantEvent() (content string, reasoning string, err error) {
	if excType := f.ExceptionType(); excType != "" {
		return "", "", fmt.Errorf("Kiro EventStream Exception [%s]: %s", excType, string(f.Payload))
	}
	if f.MessageType() == "exception" || f.MessageType() == "error" {
		return "", "", fmt.Errorf("Kiro EventStream Exception: %s", string(f.Payload))
	}

	if len(f.Payload) == 0 {
		return "", "", nil
	}

	var raw map[string]any
	if err := json.Unmarshal(f.Payload, &raw); err != nil {
		return "", "", err
	}

	if errMsg, ok := raw["message"].(string); ok && (f.EventType() == "exception" || strings.Contains(strings.ToLower(string(f.Payload)), "exception")) {
		return "", "", fmt.Errorf("Kiro EventStream Error: %s", errMsg)
	}

	if c, ok := raw["content"].(string); ok {
		content = c
	} else if c, ok := raw["code"].(string); ok {
		content = c
	}

	if f.EventType() == "reasoningContentEvent" {
		if r, ok := raw["text"].(string); ok {
			reasoning = r
		}
	}
	if reasoning == "" {
		if r, ok := raw["reasoningContent"].(string); ok {
			reasoning = r
		} else if r, ok := raw["reasoning"].(string); ok {
			reasoning = r
		}
	}

	return content, reasoning, nil
}

// ToolUse describes a completed Kiro tool-use event.
type ToolUse struct {
	ToolUseID string         `json:"toolUseId"`
	Name      string         `json:"name"`
	Input     map[string]any `json:"input"`
}

// StreamCallback receives decoded Kiro EventStream output.
type StreamCallback struct {
	OnText         func(text string, isThinking bool)
	OnToolUse      func(toolUse ToolUse)
	OnComplete     func(inputTokens, outputTokens int)
	OnCredits      func(credits float64)
	OnContextUsage func(percentage float64)
	OnStopReason   func(reason string)
}

// ParseEventStream decodes all Kiro events and invokes callback.
func ParseEventStream(body io.Reader, callback *StreamCallback) error {
	_, err := ParseEventStreamTracked(body, callback)
	return err
}

// ParseEventStreamTracked reports whether any downstream-visible output was emitted before an error.
func ParseEventStreamTracked(body io.Reader, callback *StreamCallback) (emitted bool, err error) {
	if callback == nil {
		callback = &StreamCallback{}
	}

	var inputTokens, outputTokens int
	var totalCredits float64
	var contextUsagePercentages []float64
	var sawOutput bool
	pending := &pendingToolUses{}
	trackedCallback := *callback
	originalOnToolUse := trackedCallback.OnToolUse
	trackedCallback.OnToolUse = func(toolUse ToolUse) {
		sawOutput = true
		if originalOnToolUse != nil {
			emitted = true
			originalOnToolUse(toolUse)
		}
	}
	callback = &trackedCallback

	for {
		frame, errFrame := ReadEventStreamFrame(body)
		if errFrame != nil {
			if errFrame == io.EOF {
				break
			}
			return emitted, errFrame
		}

		event := make(map[string]any)
		if len(frame.Payload) > 0 {
			if err := json.Unmarshal(frame.Payload, &event); err != nil {
				return emitted, fmt.Errorf("kiro eventstream: decode payload: %w", err)
			}
		}

		if isUpstreamErrorFrame(frame) {
			detail := formatUpstreamErrorDetail(frame, event)
			return emitted, fmt.Errorf("%w: %s", ErrKiroEventStreamUpstream, detail)
		}

		inputTokens, outputTokens = updateTokensFromEvent(event, inputTokens, outputTokens)

		switch frame.EventType() {
		case "assistantResponseEvent", "codeEvent":
			if reasoning := firstStringField(event, "reasoningContent", "reasoning"); reasoning != "" {
				sawOutput = true
				if callback.OnText != nil {
					emitted = true
					callback.OnText(reasoning, true)
				}
			}
			if content := firstStringField(event, "content", "code"); content != "" {
				sawOutput = true
				if callback.OnText != nil {
					emitted = true
					callback.OnText(content, false)
				}
			}
			if toolUseObj, ok := event["toolUse"].(map[string]any); ok {
				if toolErr := handleToolUseEvent(toolUseObj, pending, callback); toolErr != nil {
					return emitted, toolErr
				}
			}
		case "reasoningContentEvent":
			if text := firstStringField(event, "text", "reasoningContent", "reasoning", "content"); text != "" {
				sawOutput = true
				if callback.OnText != nil {
					emitted = true
					callback.OnText(text, true)
				}
			}
		case "toolUseEvent":
			if toolErr := handleToolUseEvent(event, pending, callback); toolErr != nil {
				return emitted, toolErr
			}
		case "meteringEvent":
			if usage, ok := numberAsFloat(event["usage"]); ok {
				totalCredits += usage
			}
		case "contextUsageEvent":
			if pct, ok := numberAsFloat(event["contextUsagePercentage"]); ok {
				contextUsagePercentages = append(contextUsagePercentages, pct)
			}
		case "metadataEvent":
			if reason := firstStringField(event, "stopReason", "stop_reason"); reason != "" && callback.OnStopReason != nil {
				callback.OnStopReason(reason)
			}
		case "messageStopEvent":
			if reason := firstStringField(event, "stopReason", "stop_reason"); reason != "" && callback.OnStopReason != nil {
				callback.OnStopReason(reason)
			}
		}
	}

	if err := pending.flushAll(callback); err != nil {
		return emitted, err
	}
	if !sawOutput {
		return emitted, ErrEmptyKiroStream
	}
	if callback.OnCredits != nil && totalCredits > 0 {
		callback.OnCredits(totalCredits)
	}
	if callback.OnContextUsage != nil {
		for _, percentage := range contextUsagePercentages {
			callback.OnContextUsage(percentage)
		}
	}
	if callback.OnComplete != nil {
		callback.OnComplete(inputTokens, outputTokens)
	}
	return emitted, nil
}

func isUpstreamErrorFrame(frame *EventStreamFrame) bool {
	if frame == nil {
		return false
	}
	if frame.ExceptionType() != "" {
		return true
	}
	switch frame.MessageType() {
	case "error", "exception":
		return true
	}
	switch frame.EventType() {
	case "error", "exception", "invalidStateEvent":
		return true
	}
	return false
}

func formatUpstreamErrorDetail(frame *EventStreamFrame, event map[string]any) string {
	reason := firstStringField(event, "reason", "code", "errorCode", "error_code")
	message := firstStringField(event, "message", "errorMessage", "error_message", "detail")
	if reason != "" && message != "" && !strings.Contains(message, reason) {
		return reason + ": " + message
	}
	if message != "" {
		return message
	}
	if reason != "" {
		return reason
	}
	if frame != nil {
		if raw := strings.TrimSpace(string(frame.Payload)); raw != "" {
			return raw
		}
		if exc := strings.TrimSpace(frame.ExceptionType()); exc != "" {
			return exc
		}
		if ev := strings.TrimSpace(frame.EventType()); ev != "" {
			return ev
		}
	}
	return "unknown error"
}

type toolUseState struct {
	ToolUseID   string
	Name        string
	InputBuffer strings.Builder
	GeneratedID bool
}

type pendingToolUses struct {
	byID   map[string]*toolUseState
	order  []string
	lastID string
}

func (p *pendingToolUses) get(id string) *toolUseState {
	if p.byID == nil {
		return nil
	}
	return p.byID[id]
}

func (p *pendingToolUses) add(state *toolUseState) {
	if p.byID == nil {
		p.byID = make(map[string]*toolUseState)
	}
	p.byID[state.ToolUseID] = state
	p.order = append(p.order, state.ToolUseID)
	p.lastID = state.ToolUseID
}

func (p *pendingToolUses) rekey(state *toolUseState, newID string) {
	oldID := state.ToolUseID
	delete(p.byID, oldID)
	for i, id := range p.order {
		if id == oldID {
			p.order[i] = newID
			break
		}
	}
	state.ToolUseID = newID
	state.GeneratedID = false
	p.byID[newID] = state
	if p.lastID == oldID {
		p.lastID = newID
	}
}

func (p *pendingToolUses) remove(id string) {
	delete(p.byID, id)
	for i, existing := range p.order {
		if existing == id {
			p.order = append(p.order[:i], p.order[i+1:]...)
			break
		}
	}
	if p.lastID == id {
		p.lastID = ""
	}
}

func (p *pendingToolUses) flushAll(callback *StreamCallback) error {
	order := p.order
	byID := p.byID
	p.byID = nil
	p.order = nil
	p.lastID = ""
	for _, id := range order {
		state := byID[id]
		if state == nil {
			continue
		}
		if err := finishToolUse(state, callback); err != nil {
			return err
		}
	}
	return nil
}

func handleToolUseEvent(event map[string]any, pending *pendingToolUses, callback *StreamCallback) error {
	toolUseID := firstStringField(event, "toolUseId", "toolUseID", "tool_use_id", "id")
	name := firstStringField(event, "name", "toolName", "tool_name")
	isStop := firstBoolField(event, "stop", "isStop", "done")

	var state *toolUseState
	switch {
	case toolUseID != "":
		state = pending.get(toolUseID)
		if state == nil && pending.lastID != "" {
			if prev := pending.get(pending.lastID); prev != nil && prev.GeneratedID && (name == "" || prev.Name == name) {
				pending.rekey(prev, toolUseID)
				state = prev
			}
		}
		if state == nil {
			if name == "" {
				return nil
			}
			state = &toolUseState{ToolUseID: toolUseID, Name: name}
			pending.add(state)
		} else {
			if name != "" && state.Name == "" {
				state.Name = name
			}
			pending.lastID = state.ToolUseID
		}
	case pending.lastID != "" && pending.get(pending.lastID) != nil:
		state = pending.get(pending.lastID)
		if name != "" && state.Name != name {
			if err := finishToolUse(state, callback); err != nil {
				return err
			}
			pending.remove(state.ToolUseID)
			state = &toolUseState{ToolUseID: "toolu_" + uuid.New().String(), Name: name, GeneratedID: true}
			pending.add(state)
		}
	case name != "":
		state = &toolUseState{ToolUseID: "toolu_" + uuid.New().String(), Name: name, GeneratedID: true}
		pending.add(state)
	default:
		return nil
	}

	if input, ok := event["input"].(string); ok {
		state.InputBuffer.WriteString(input)
	} else if inputObj, ok := event["input"].(map[string]any); ok {
		data, _ := json.Marshal(inputObj)
		state.InputBuffer.Reset()
		state.InputBuffer.Write(data)
	}

	if isStop {
		if err := finishToolUse(state, callback); err != nil {
			return err
		}
		pending.remove(state.ToolUseID)
	}
	return nil
}

func finishToolUse(state *toolUseState, callback *StreamCallback) error {
	if state == nil || state.Name == "" {
		return nil
	}
	if state.ToolUseID == "" {
		state.ToolUseID = "toolu_" + uuid.New().String()
	}
	input := make(map[string]any)
	if state.InputBuffer.Len() > 0 {
		if err := json.Unmarshal([]byte(state.InputBuffer.String()), &input); err != nil {
			return fmt.Errorf("%w: %v", ErrIncompleteKiroToolInput, err)
		}
	}
	if callback != nil && callback.OnToolUse != nil {
		callback.OnToolUse(ToolUse{ToolUseID: state.ToolUseID, Name: state.Name, Input: input})
	}
	return nil
}

func updateTokensFromEvent(event map[string]any, currentInputTokens, currentOutputTokens int) (int, int) {
	candidates := []map[string]any{event}
	collectUsageMaps(event, &candidates)
	inputTokens := currentInputTokens
	outputTokens := currentOutputTokens
	for _, usage := range candidates {
		if usage == nil {
			continue
		}
		if v, ok := readTokenNumber(usage, "outputTokens", "completionTokens", "totalOutputTokens", "output_tokens", "completion_tokens", "total_output_tokens"); ok {
			outputTokens = v
		}
		if v, ok := readTokenNumber(usage, "inputTokens", "promptTokens", "totalInputTokens", "input_tokens", "prompt_tokens", "total_input_tokens"); ok {
			inputTokens = v
			continue
		}
		uncached, _ := readTokenNumber(usage, "uncachedInputTokens", "uncached_input_tokens")
		cacheRead, _ := readTokenNumber(usage, "cacheReadInputTokens", "cache_read_input_tokens")
		cacheWrite, _ := readTokenNumber(usage, "cacheWriteInputTokens", "cache_write_input_tokens", "cacheCreationInputTokens", "cache_creation_input_tokens")
		if uncached+cacheRead+cacheWrite > 0 {
			inputTokens = uncached + cacheRead + cacheWrite
			continue
		}
		total, ok := readTokenNumber(usage, "totalTokens", "total_tokens")
		if ok && total > 0 && total-outputTokens > 0 {
			inputTokens = total - outputTokens
		}
	}
	return inputTokens, outputTokens
}

func collectUsageMaps(v any, out *[]map[string]any) {
	switch t := v.(type) {
	case map[string]any:
		for k, child := range t {
			lk := strings.ToLower(k)
			if lk == "usage" || lk == "tokenusage" || lk == "token_usage" {
				if m, ok := child.(map[string]any); ok {
					*out = append(*out, m)
				}
			}
			collectUsageMaps(child, out)
		}
	case []any:
		for _, child := range t {
			collectUsageMaps(child, out)
		}
	}
}

func readTokenNumber(m map[string]any, keys ...string) (int, bool) {
	for _, key := range keys {
		v, ok := m[key]
		if !ok {
			continue
		}
		switch n := v.(type) {
		case float64:
			return int(n), true
		case int:
			return n, true
		case int64:
			return int(n), true
		case json.Number:
			if parsed, err := n.Int64(); err == nil {
				return int(parsed), true
			}
		case string:
			if parsed, err := strconv.Atoi(n); err == nil {
				return parsed, true
			}
			if parsed, err := strconv.ParseFloat(n, 64); err == nil {
				return int(parsed), true
			}
		}
	}
	return 0, false
}

func firstStringField(m map[string]any, keys ...string) string {
	for _, key := range keys {
		if v, ok := m[key].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

func firstBoolField(m map[string]any, keys ...string) bool {
	for _, key := range keys {
		if v, ok := m[key].(bool); ok {
			return v
		}
	}
	return false
}

func numberAsFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case json.Number:
		parsed, err := n.Float64()
		return parsed, err == nil
	case string:
		parsed, err := strconv.ParseFloat(n, 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}
