package helps

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
	"testing/iotest"
)

const (
	bootstrapMessageStart = "event: message_start\n" + `data: {"type":"message_start","message":{"id":"msg_1","model":"claude-opus-5"}}` + "\n\n"
	bootstrapPing         = "event: ping\n" + `data: {"type":"ping"}` + "\n\n"
	bootstrapEmptyText    = "event: content_block_start\n" + `data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\n"
	bootstrapEmptyThink   = "event: content_block_start\n" + `data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}` + "\n\n"
	bootstrapToolUse      = "event: content_block_start\n" + `data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"t1","name":"f","input":{}}}` + "\n\n"
	bootstrapServerTool   = "event: content_block_start\n" + `data: {"type":"content_block_start","index":0,"content_block":{"type":"server_tool_use","id":"s1","name":"web_search","input":{}}}` + "\n\n"
	bootstrapTextDelta    = "event: content_block_delta\n" + `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}` + "\n\n"
	bootstrapRateLimit    = "event: error\n" + `data: {"type":"error","error":{"type":"rate_limit_error","message":"slow down"}}` + "\n\n"
	bootstrapOverloaded   = "event: error\n" + `data: {"type":"error","error":{"type":"overloaded_error","message":"busy"}}` + "\n\n"
	bootstrapRest         = "event: message_stop\n" + `data: {"type":"message_stop"}` + "\n\n"
)

func TestReadClaudeStreamBootstrap(t *testing.T) {
	rateLimitPayload := `{"type":"error","error":{"type":"rate_limit_error","message":"slow down"}}`
	for _, tc := range []struct {
		name      string
		stream    string
		held      string
		rateLimit string
	}{
		{"rate limit after bootstrap", bootstrapMessageStart + bootstrapPing + bootstrapEmptyText + bootstrapRateLimit, bootstrapMessageStart + bootstrapPing + bootstrapEmptyText + bootstrapRateLimit, rateLimitPayload},
		{"rate limit after empty thinking block", bootstrapMessageStart + bootstrapEmptyThink + bootstrapRateLimit, bootstrapMessageStart + bootstrapEmptyThink + bootstrapRateLimit, rateLimitPayload},
		{"rate limit without final blank line", bootstrapMessageStart + strings.TrimSuffix(bootstrapRateLimit, "\n\n"), bootstrapMessageStart + strings.TrimSuffix(bootstrapRateLimit, "\n\n"), rateLimitPayload},
		{"rate limit after comment", ": keepalive\n\n" + bootstrapMessageStart + bootstrapRateLimit, ": keepalive\n\n" + bootstrapMessageStart + bootstrapRateLimit, rateLimitPayload},
		{"text delta releases", bootstrapMessageStart + bootstrapEmptyText + bootstrapTextDelta + bootstrapRateLimit, bootstrapMessageStart + bootstrapEmptyText + bootstrapTextDelta, ""},
		{"tool use releases", bootstrapMessageStart + bootstrapToolUse + bootstrapRateLimit, bootstrapMessageStart + bootstrapToolUse, ""},
		{"server tool use releases", bootstrapMessageStart + bootstrapServerTool + bootstrapRateLimit, bootstrapMessageStart + bootstrapServerTool, ""},
		{"other error releases", bootstrapMessageStart + bootstrapOverloaded + bootstrapRest, bootstrapMessageStart + bootstrapOverloaded, ""},
		{"end of stream releases", bootstrapMessageStart + bootstrapPing, bootstrapMessageStart + bootstrapPing, ""},
		{"rate limit with CRLF", strings.ReplaceAll(bootstrapMessageStart+bootstrapRateLimit, "\n", "\r\n"), strings.ReplaceAll(bootstrapMessageStart+bootstrapRateLimit, "\n", "\r\n"), rateLimitPayload},
		{"line budget releases", strings.Repeat(bootstrapPing, 20) + bootstrapRateLimit, strings.Repeat(bootstrapPing, 16), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bootstrap := ReadClaudeStreamBootstrap(strings.NewReader(tc.stream))
			if string(bootstrap.Held) != tc.held {
				t.Fatalf("Held = %q, want %q", bootstrap.Held, tc.held)
			}
			if string(bootstrap.RateLimit) != tc.rateLimit {
				t.Fatalf("RateLimit = %q, want %q", bootstrap.RateLimit, tc.rateLimit)
			}
			replayed, errRead := io.ReadAll(bootstrap.Stream)
			if errRead != nil {
				t.Fatal(errRead)
			}
			if !bytes.Equal(replayed, []byte(tc.stream)) {
				t.Fatalf("Stream = %q, want the upstream bytes %q", replayed, tc.stream)
			}
		})
	}
}

func TestReadClaudeStreamBootstrapReleasesOversizedLine(t *testing.T) {
	huge := "event: ping\ndata: " + strings.Repeat("x", 2*claudeBootstrapReaderSize) + "\n\n"
	stream := bootstrapMessageStart + huge + bootstrapRateLimit
	bootstrap := ReadClaudeStreamBootstrap(strings.NewReader(stream))
	if bootstrap.RateLimit != nil {
		t.Fatalf("RateLimit = %q after an oversized line, want nil", bootstrap.RateLimit)
	}
	if len(bootstrap.Held) > len(bootstrapMessageStart)+claudeBootstrapReaderSize+len("event: ping\n") {
		t.Fatalf("held %d bytes, want the oversized line cut at the reader size", len(bootstrap.Held))
	}
	replayed, errRead := io.ReadAll(bootstrap.Stream)
	if errRead != nil {
		t.Fatal(errRead)
	}
	if string(replayed) != stream {
		t.Fatal("Stream did not replay the upstream bytes")
	}
}

func TestReadClaudeStreamBootstrapReplaysReadError(t *testing.T) {
	errUpstream := errors.New("upstream reset")
	body := io.MultiReader(strings.NewReader(bootstrapMessageStart), iotest.ErrReader(errUpstream))
	bootstrap := ReadClaudeStreamBootstrap(iotest.OneByteReader(body))
	if bootstrap.RateLimit != nil {
		t.Fatalf("RateLimit = %q, want nil", bootstrap.RateLimit)
	}
	replayed, errRead := io.ReadAll(bootstrap.Stream)
	if string(replayed) != bootstrapMessageStart || !errors.Is(errRead, errUpstream) {
		t.Fatalf("Stream = %q, %v; want the held bytes then the upstream error", replayed, errRead)
	}
	if _, errAgain := bootstrap.Stream.Read(make([]byte, 1)); !errors.Is(errAgain, errUpstream) {
		t.Fatalf("second Read error = %v, want the upstream error again", errAgain)
	}
}
