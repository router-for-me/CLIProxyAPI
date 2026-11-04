package helps

import (
	"bytes"
	"testing"
)

func TestRelaySSEBufferChunkBoundaries(t *testing.T) {
	for _, newline := range []string{"\n", "\r\n"} {
		first := "event: message" + newline + "data: {\"text\":\"first\"}" + newline + newline
		second := ": keep-alive" + newline + newline
		last := "data: {\"finishReason\":\"STOP\"}" + newline + newline
		wire := []byte(first + second + last)
		for split := 0; split <= len(wire); split++ {
			var buffer RelaySSEBuffer
			frames := buffer.Feed(wire[:split])
			frames = append(frames, buffer.Feed(wire[split:])...)
			if len(frames) != 3 || string(frames[0]) != first || string(frames[1]) != second || string(frames[2]) != last {
				t.Fatalf("newline %q, split %d: got frames %q", newline, split, frames)
			}
			if tail := buffer.Finish(); len(tail) != 0 {
				t.Fatalf("newline %q, split %d: unexpected tail %q", newline, split, tail)
			}
		}
		var buffer RelaySSEBuffer
		for i := range len(first) {
			frames := buffer.Feed([]byte{first[i]})
			if i < len(first)-1 && len(frames) != 0 {
				t.Fatalf("incomplete frame emitted at byte %d: %q", i, frames)
			}
			if i == len(first)-1 && (len(frames) != 1 || string(frames[0]) != first) {
				t.Fatalf("one-byte chunks lost frame: %q", frames)
			}
		}
	}
}

func TestRelaySSEBufferFinalFrameAndBareJSON(t *testing.T) {
	var buffer RelaySSEBuffer
	if frames := buffer.Feed([]byte("data: {\"text\":")); len(frames) != 0 {
		t.Fatalf("incomplete frame emitted: %q", frames)
	}
	if frames := buffer.Feed([]byte("\"tail\"}")); len(frames) != 0 {
		t.Fatalf("unterminated frame emitted: %q", frames)
	}
	if tail := buffer.Finish(); len(tail) != 1 || string(tail[0]) != "data: {\"text\":\"tail\"}" {
		t.Fatalf("lost unterminated final frame: %q", tail)
	}
	if tail := buffer.Finish(); len(tail) != 0 {
		t.Fatalf("final frame emitted twice: %q", tail)
	}
	for _, payload := range []string{"{\n  \"text\": \"value\"\n\n}", "[DONE]"} {
		var raw RelaySSEBuffer
		frames := raw.Feed([]byte(payload))
		if len(frames) != 1 || !bytes.Equal(frames[0], []byte(payload)) {
			t.Fatalf("bare payload changed: %q", frames)
		}
	}
}
