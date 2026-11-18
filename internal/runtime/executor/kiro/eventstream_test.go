package kiro

import (
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"strings"
	"testing"
)

func createTestEventStreamFrame(headers map[string]string, payload []byte) []byte {
	var headersBuf bytes.Buffer

	for k, v := range headers {
		headersBuf.WriteByte(byte(len(k)))
		headersBuf.WriteString(k)
		headersBuf.WriteByte(7) // String type
		binary.Write(&headersBuf, binary.BigEndian, uint16(len(v)))
		headersBuf.WriteString(v)
	}

	headersBytes := headersBuf.Bytes()
	headersLen := uint32(len(headersBytes))
	totalLen := uint32(16 + len(headersBytes) + len(payload))

	var prelude [8]byte
	binary.BigEndian.PutUint32(prelude[0:4], totalLen)
	binary.BigEndian.PutUint32(prelude[4:8], headersLen)
	preludeCRC := crc32.ChecksumIEEE(prelude[:])

	var fullData bytes.Buffer
	fullData.Write(prelude[:])
	binary.Write(&fullData, binary.BigEndian, preludeCRC)
	fullData.Write(headersBytes)
	fullData.Write(payload)

	msgCRC := crc32.ChecksumIEEE(fullData.Bytes())
	binary.Write(&fullData, binary.BigEndian, msgCRC)

	return fullData.Bytes()
}

func TestReadEventStreamFrame(t *testing.T) {
	headers := map[string]string{
		":event-type":   "assistantResponseEvent",
		":message-type": "event",
	}
	payload := []byte(`{"content":"Hello from Kiro!","reasoningContent":"Thinking..."}`)

	frameBytes := createTestEventStreamFrame(headers, payload)
	reader := bytes.NewReader(frameBytes)

	frame, err := ReadEventStreamFrame(reader)
	if err != nil {
		t.Fatalf("unexpected error decoding frame: %v", err)
	}

	if frame.EventType() != "assistantResponseEvent" {
		t.Errorf("expected event-type 'assistantResponseEvent', got '%s'", frame.EventType())
	}

	content, reasoning, err := frame.ParseAssistantEvent()
	if err != nil {
		t.Fatalf("failed to parse assistant event: %v", err)
	}

	if content != "Hello from Kiro!" {
		t.Errorf("expected content 'Hello from Kiro!', got '%s'", content)
	}

	if reasoning != "Thinking..." {
		t.Errorf("expected reasoning 'Thinking...', got '%s'", reasoning)
	}
}

func TestParseEventStreamTracked(t *testing.T) {
	headers := map[string]string{":message-type": "event"}
	var stream bytes.Buffer
	headers[":event-type"] = "reasoningContentEvent"
	stream.Write(createTestEventStreamFrame(headers, []byte(`{"text":"thinking"}`)))
	headers[":event-type"] = "assistantResponseEvent"
	stream.Write(createTestEventStreamFrame(headers, []byte(`{"content":"answer","usage":{"inputTokens":7,"outputTokens":3}}`)))
	headers[":event-type"] = "toolUseEvent"
	stream.Write(createTestEventStreamFrame(headers, []byte(`{"toolUseId":"toolu_1","name":"read_file","input":"{\"path\":\"a.go\"}","stop":true}`)))
	headers[":event-type"] = "meteringEvent"
	stream.Write(createTestEventStreamFrame(headers, []byte(`{"usage":1.25}`)))
	headers[":event-type"] = "contextUsageEvent"
	stream.Write(createTestEventStreamFrame(headers, []byte(`{"contextUsagePercentage":42}`)))

	var text string
	var reasoning string
	var toolUses []ToolUse
	var inputTokens int
	var outputTokens int
	var credits float64
	var contextUsage float64
	emitted, err := ParseEventStreamTracked(&stream, &StreamCallback{
		OnText: func(delta string, isThinking bool) {
			if isThinking {
				reasoning += delta
				return
			}
			text += delta
		},
		OnToolUse: func(toolUse ToolUse) {
			toolUses = append(toolUses, toolUse)
		},
		OnComplete: func(inTokens, outTokens int) {
			inputTokens = inTokens
			outputTokens = outTokens
		},
		OnCredits: func(v float64) {
			credits = v
		},
		OnContextUsage: func(v float64) {
			contextUsage = v
		},
	})
	if err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}
	if !emitted {
		t.Fatalf("expected emitted=true")
	}
	if text != "answer" || reasoning != "thinking" {
		t.Fatalf("unexpected text=%q reasoning=%q", text, reasoning)
	}
	if inputTokens != 7 || outputTokens != 3 {
		t.Fatalf("unexpected usage %d/%d", inputTokens, outputTokens)
	}
	if credits != 1.25 || contextUsage != 42 {
		t.Fatalf("unexpected credits/context usage %.2f/%.2f", credits, contextUsage)
	}
	if len(toolUses) != 1 || toolUses[0].Name != "read_file" || toolUses[0].Input["path"] != "a.go" {
		t.Fatalf("unexpected tool uses: %#v", toolUses)
	}
}

func TestParseEventStreamTrackedEmptyStream(t *testing.T) {
	emitted, err := ParseEventStreamTracked(bytes.NewReader(nil), nil)
	if err != ErrEmptyKiroStream {
		t.Fatalf("expected ErrEmptyKiroStream, got %v", err)
	}
	if emitted {
		t.Fatalf("expected emitted=false")
	}
}

func TestParseEventStreamTrackedInvalidStateEvent(t *testing.T) {
	headers := map[string]string{
		":message-type": "event",
		":event-type":   "invalidStateEvent",
	}
	frame := createTestEventStreamFrame(headers, []byte(`{"reason":"CONTENT_LENGTH_EXCEEDS_THRESHOLD","message":"Input is too long"}`))
	emitted, err := ParseEventStreamTracked(bytes.NewReader(frame), nil)
	if !errors.Is(err, ErrKiroEventStreamUpstream) {
		t.Fatalf("expected ErrKiroEventStreamUpstream, got %v", err)
	}
	if !strings.Contains(err.Error(), "CONTENT_LENGTH_EXCEEDS_THRESHOLD") {
		t.Fatalf("expected reason in error, got %v", err)
	}
	if emitted {
		t.Fatalf("expected emitted=false")
	}
}

func TestParseEventStreamTrackedMetadataOnlyDefaultsToRefusalStopReason(t *testing.T) {
	headers := map[string]string{
		":message-type": "event",
		":event-type":   "meteringEvent",
	}
	frame := createTestEventStreamFrame(headers, []byte(`{"usage":0.05}`))
	var stopReason string
	var credits float64
	emitted, err := ParseEventStreamTracked(bytes.NewReader(frame), &StreamCallback{
		OnStopReason: func(reason string) {
			stopReason = reason
		},
		OnCredits: func(v float64) {
			credits = v
		},
	})
	if err != nil {
		t.Fatalf("expected nil error for metadata-only refusal stream, got %v", err)
	}
	if emitted {
		t.Fatalf("expected emitted=false")
	}
	if stopReason != "refusal" {
		t.Fatalf("expected stopReason 'refusal', got %q", stopReason)
	}
	if credits != 0.05 {
		t.Fatalf("expected credits 0.05, got %v", credits)
	}
}
