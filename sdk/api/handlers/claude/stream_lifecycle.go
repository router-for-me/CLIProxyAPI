package claude

import (
	"bytes"
	"encoding/json"
	"strings"
	"time"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type claudeStreamLifecycle struct {
	messageID       string
	model           string
	sawMessageStart bool
	sawMessageDelta bool
	sawMessageStop  bool
}

func newClaudeStreamLifecycle() *claudeStreamLifecycle {
	return &claudeStreamLifecycle{}
}

func (l *claudeStreamLifecycle) ObserveChunk(chunk []byte) {
	if l == nil || len(chunk) == 0 {
		return
	}

	var eventType string
	for _, line := range bytes.Split(chunk, []byte("\n")) {
		line = bytes.TrimSpace(bytes.TrimRight(line, "\r"))
		if len(line) == 0 {
			eventType = ""
			continue
		}
		switch {
		case bytes.HasPrefix(line, []byte("event:")):
			eventType = strings.TrimSpace(string(bytes.TrimSpace(line[len("event:"):])))
			l.observeEventType(eventType)
		case bytes.HasPrefix(line, []byte("data:")):
			payload := bytes.TrimSpace(line[len("data:"):])
			l.observePayload(eventType, payload)
		}
	}
}

func (l *claudeStreamLifecycle) BuildTerminalErrorFrames(errResp claudeErrorResponse) []byte {
	if l == nil {
		l = newClaudeStreamLifecycle()
	}

	out := make([]byte, 0, 512)
	if !l.sawMessageStart {
		out = append(out, buildClaudeSSEEvent("message_start", l.syntheticMessageStartPayload())...)
	}
	if !l.sawMessageDelta {
		out = append(out, buildClaudeSSEEvent("message_delta", []byte(`{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"input_tokens":0,"output_tokens":0}}`))...)
	}
	errorJSON, _ := json.Marshal(errResp)
	out = append(out, buildClaudeSSEEvent("error", errorJSON)...)
	if !l.sawMessageStop {
		out = append(out, buildClaudeSSEEvent("message_stop", []byte(`{"type":"message_stop"}`))...)
	}
	return out
}

func (l *claudeStreamLifecycle) observeEventType(eventType string) {
	switch strings.TrimSpace(eventType) {
	case "message_start":
		l.sawMessageStart = true
	case "message_delta":
		l.sawMessageDelta = true
	case "message_stop":
		l.sawMessageStop = true
	}
}

func (l *claudeStreamLifecycle) observePayload(eventType string, payload []byte) {
	payload = bytes.TrimSpace(payload)
	if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) || !json.Valid(payload) {
		return
	}

	root := gjson.ParseBytes(payload)
	payloadType := strings.TrimSpace(root.Get("type").String())
	if payloadType != "" {
		l.observeEventType(payloadType)
	} else {
		l.observeEventType(eventType)
	}

	if message := root.Get("message"); message.Exists() {
		if id := strings.TrimSpace(message.Get("id").String()); id != "" {
			l.messageID = id
		}
		if model := strings.TrimSpace(message.Get("model").String()); model != "" {
			l.model = model
		}
	}
}

func (l *claudeStreamLifecycle) syntheticMessageStartPayload() []byte {
	messageID := strings.TrimSpace(l.messageID)
	if messageID == "" {
		messageID = "msg_terminal_error_" + time.Now().UTC().Format("20060102150405.000000000")
	}

	model := strings.TrimSpace(l.model)
	if model == "" {
		model = "unknown"
	}

	payload := []byte(`{"type":"message_start","message":{"id":"","type":"message","role":"assistant","content":[],"model":"","stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":0,"output_tokens":0}}}`)
	payload, _ = sjson.SetBytes(payload, "message.id", messageID)
	payload, _ = sjson.SetBytes(payload, "message.model", model)
	return payload
}

func buildClaudeSSEEvent(event string, payload []byte) []byte {
	chunk := make([]byte, 0, len(event)+len(payload)+20)
	chunk = append(chunk, "event: "...)
	chunk = append(chunk, event...)
	chunk = append(chunk, '\n')
	chunk = append(chunk, "data: "...)
	chunk = append(chunk, payload...)
	chunk = append(chunk, '\n', '\n')
	return chunk
}
