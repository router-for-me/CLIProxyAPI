package helps

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"io"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	"github.com/tidwall/gjson"
)

// ReadKiroEvent reads complete AWS event-stream frames, not arbitrary JSON substrings.
func ReadKiroEvent(reader io.Reader) (string, []byte, error) {
	prelude := make([]byte, 12)
	if _, err := io.ReadFull(reader, prelude); err != nil {
		return "", nil, err
	}
	total, headerLength := binary.BigEndian.Uint32(prelude), binary.BigEndian.Uint32(prelude[4:])
	if total < 16 || total > 16<<20 || headerLength > total-16 {
		return "", nil, fmt.Errorf("kiro: invalid event-stream frame length")
	}
	if crc32.ChecksumIEEE(prelude[:8]) != binary.BigEndian.Uint32(prelude[8:]) {
		return "", nil, fmt.Errorf("kiro: invalid event-stream prelude checksum")
	}
	frame := make([]byte, total)
	copy(frame, prelude)
	if _, err := io.ReadFull(reader, frame[12:]); err != nil {
		return "", nil, err
	}
	if crc32.ChecksumIEEE(frame[:total-4]) != binary.BigEndian.Uint32(frame[total-4:]) {
		return "", nil, fmt.Errorf("kiro: invalid event-stream checksum")
	}
	headers := frame[12 : 12+headerLength]
	values := make(map[string]string)
	for len(headers) > 0 {
		length := int(headers[0])
		headers = headers[1:]
		if length == 0 || len(headers) < length+1 {
			return "", nil, fmt.Errorf("kiro: malformed event-stream header")
		}
		name := string(headers[:length])
		kind := headers[length]
		headers = headers[length+1:]
		var size int
		switch kind {
		case 0, 1:
			size = 0
		case 2:
			size = 1
		case 3:
			size = 2
		case 4:
			size = 4
		case 5, 8:
			size = 8
		case 9:
			size = 16
		case 6, 7:
			if len(headers) < 2 {
				return "", nil, fmt.Errorf("kiro: truncated event-stream header")
			}
			size = int(binary.BigEndian.Uint16(headers))
			headers = headers[2:]
		default:
			return "", nil, fmt.Errorf("kiro: invalid event-stream header type")
		}
		if len(headers) < size {
			return "", nil, fmt.Errorf("kiro: truncated event-stream header value")
		}
		if kind == 7 {
			values[name] = string(headers[:size])
		}
		headers = headers[size:]
	}
	payload := frame[12+headerLength : total-4]
	if !gjson.ValidBytes(payload) {
		return "", nil, fmt.Errorf("kiro: invalid event JSON")
	}
	if kind := values[":message-type"]; kind == "exception" || kind == "error" {
		name := values[":exception-type"]
		if name == "" {
			name = values[":error-code"]
		}
		return "", nil, &KiroStreamError{Kind: name}
	}
	return values[":event-type"], payload, nil
}

type KiroStreamError struct{ Kind string }

func (e *KiroStreamError) Error() string { return "kiro upstream: " + e.Kind }
func (e *KiroStreamError) StatusCode() int {
	switch strings.ToLower(e.Kind) {
	case "throttlingexception", "servicequotaexceededexception":
		return 429
	case "accessdeniedexception":
		return 403
	case "validationexception", "invalidstateevent":
		return 400
	case "serviceunavailableexception":
		return 503
	default:
		return 502
	}
}

type kiroToolCall struct {
	ID     string
	Name   string
	Input  strings.Builder
	Closed bool
}

type KiroResponse struct {
	ID         string
	Model      string
	Created    int64
	Text       strings.Builder
	Reasoning  strings.Builder
	Tools      []*kiroToolCall
	Usage      usage.Detail
	HasUsage   bool
	HasContent bool
	started    bool
}

func NewKiroResponse(model string) *KiroResponse {
	return &KiroResponse{ID: "chatcmpl-" + uuid.NewString(), Model: model, Created: time.Now().Unix()}
}

func (s *KiroResponse) chunk(delta map[string]any, finish any, withUsage bool) []byte {
	body := map[string]any{"id": s.ID, "object": "chat.completion.chunk", "created": s.Created, "model": s.Model,
		"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}}
	if withUsage {
		body["usage"] = s.usageJSON()
	}
	data, _ := json.Marshal(body)
	return append(append([]byte("data: "), data...), '\n', '\n')
}

func (s *KiroResponse) Consume(event string, payload []byte) ([][]byte, error) {
	root := gjson.ParseBytes(payload)
	if nested := root.Get(event); nested.IsObject() {
		root = nested
	}
	var delta map[string]any
	switch event {
	case "assistantResponseEvent":
		if text := root.Get("content").String(); text != "" {
			s.Text.WriteString(text)
			s.HasContent = true
			delta = map[string]any{"content": text}
		}
	case "reasoningContentEvent":
		if text := root.Get("text").String(); text != "" {
			s.Reasoning.WriteString(text)
			s.HasContent = true
			delta = map[string]any{"reasoning_content": text}
		}
	case "toolUseEvent":
		id := root.Get("toolUseId").String()
		index := -1
		for i, tool := range s.Tools {
			if tool.ID == id {
				index = i
				break
			}
		}
		newTool := index < 0
		if newTool {
			if id == "" || root.Get("name").String() == "" {
				return nil, fmt.Errorf("kiro: tool event is missing its ID or name")
			}
			s.Tools = append(s.Tools, &kiroToolCall{ID: id, Name: root.Get("name").String()})
			index = len(s.Tools) - 1
		}
		tool := s.Tools[index]
		if tool.Closed {
			return nil, fmt.Errorf("kiro: tool event arrived after stop")
		}
		input := root.Get("input")
		fragment := input.String()
		if input.IsObject() {
			fragment = input.Raw
		}
		tool.Input.WriteString(fragment)
		if root.Get("stop").Bool() {
			tool.Closed = true
		}
		function := map[string]any{"arguments": fragment}
		call := map[string]any{"index": index, "function": function}
		if newTool {
			call["id"], call["type"], function["name"] = id, "function", tool.Name
		}
		delta = map[string]any{"tool_calls": []any{call}}
		s.HasContent = true
	case "metadataEvent", "messageMetadataEvent":
		tokens := root.Get("tokenUsage")
		if tokens.Exists() {
			read, write := tokens.Get("cacheReadInputTokens").Int(), tokens.Get("cacheWriteInputTokens").Int()
			input := tokens.Get("uncachedInputTokens").Int() + read + write
			if direct := tokens.Get("inputTokens"); direct.Exists() {
				input = direct.Int()
			}
			output := tokens.Get("outputTokens").Int()
			s.Usage = usage.Detail{InputTokens: input, OutputTokens: output, TotalTokens: input + output, CachedTokens: read, CacheReadTokens: read, CacheCreationTokens: write}
			s.HasUsage = true
		}
	case "invalidStateEvent":
		return nil, &KiroStreamError{Kind: event}
	}
	if delta == nil {
		return nil, nil
	}
	chunks := make([][]byte, 0, 2)
	if !s.started {
		chunks = append(chunks, s.chunk(map[string]any{"role": "assistant", "content": ""}, nil, false))
		s.started = true
	}
	chunks = append(chunks, s.chunk(delta, nil, false))
	return chunks, nil
}

func (s *KiroResponse) Validate() error {
	if !s.HasContent {
		return fmt.Errorf("kiro: upstream returned no assistant content")
	}
	for _, tool := range s.Tools {
		if !tool.Closed {
			return fmt.Errorf("kiro: upstream ended before tool use completed")
		}
		if input := tool.Input.String(); input != "" && (!gjson.Valid(input) || !gjson.Parse(input).IsObject()) {
			return fmt.Errorf("kiro: upstream returned invalid tool arguments")
		}
	}
	return nil
}

func (s *KiroResponse) finishReason() string {
	if len(s.Tools) > 0 {
		return "tool_calls"
	}
	return "stop"
}

func (s *KiroResponse) usageJSON() map[string]any {
	return map[string]any{"prompt_tokens": s.Usage.InputTokens, "completion_tokens": s.Usage.OutputTokens, "total_tokens": s.Usage.TotalTokens,
		"prompt_tokens_details": map[string]any{"cached_tokens": s.Usage.CacheReadTokens, "cache_creation_tokens": s.Usage.CacheCreationTokens}}
}

func (s *KiroResponse) FinishStream() ([][]byte, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	return [][]byte{s.chunk(map[string]any{}, s.finishReason(), s.HasUsage), []byte("data: [DONE]\n\n")}, nil
}

func (s *KiroResponse) JSON() ([]byte, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	message := map[string]any{"role": "assistant", "content": s.Text.String()}
	if reasoning := s.Reasoning.String(); reasoning != "" {
		message["reasoning_content"] = reasoning
	}
	if len(s.Tools) > 0 {
		calls := make([]any, 0, len(s.Tools))
		for _, tool := range s.Tools {
			input := tool.Input.String()
			if input == "" {
				input = "{}"
			}
			calls = append(calls, map[string]any{"id": tool.ID, "type": "function", "function": map[string]any{"name": tool.Name, "arguments": input}})
		}
		message["tool_calls"] = calls
	}
	body := map[string]any{"id": s.ID, "object": "chat.completion", "created": s.Created, "model": s.Model,
		"choices": []any{map[string]any{"index": 0, "message": message, "finish_reason": s.finishReason()}}}
	if s.HasUsage {
		body["usage"] = s.usageJSON()
	}
	return json.Marshal(body)
}
