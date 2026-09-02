package helps

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps/cursorproto"
	"google.golang.org/protobuf/proto"
)

func TestApplyCursorRunHeadersRequestsStreaming(t *testing.T) {
	request, errRequest := http.NewRequest(http.MethodPost, "https://example.com/run", nil)
	if errRequest != nil {
		t.Fatalf("create request: %v", errRequest)
	}

	applyCursorRunHeaders(request, "token")

	if got := request.Header.Get("X-Cursor-Streaming"); got != "true" {
		t.Fatalf("X-Cursor-Streaming = %q, want %q", got, "true")
	}
}

func TestRunCursorResponseLoopEmitsTextFramesIncrementally(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	responseReader, responseWriter := io.Pipe()
	requestReader, requestWriter := io.Pipe()
	defer requestReader.Close()
	writer := &cursorRequestWriter{pipe: requestWriter}
	events := make(chan CursorStreamEvent)
	run := &CursorRunPayload{Blobs: make(map[string][]byte)}
	go runCursorResponseLoop(ctx, responseReader, writer, run, events)

	firstFrame := marshalCursorTestTextFrame(t, "first")
	secondFrame := marshalCursorTestTextFrame(t, "second")
	firstWritten := make(chan struct{})
	releaseSecond := make(chan struct{})
	writeDone := make(chan error, 1)
	go func() {
		if _, errWrite := responseWriter.Write(firstFrame); errWrite != nil {
			writeDone <- errWrite
			return
		}
		close(firstWritten)
		select {
		case <-releaseSecond:
		case <-ctx.Done():
			writeDone <- ctx.Err()
			return
		}
		if _, errWrite := responseWriter.Write(secondFrame); errWrite != nil {
			writeDone <- errWrite
			return
		}
		writeDone <- responseWriter.Close()
	}()

	select {
	case <-firstWritten:
	case <-time.After(time.Second):
		t.Fatal("response loop did not consume the first frame")
	}
	select {
	case event := <-events:
		if event.Text != "first" {
			t.Fatalf("first event text = %q, want %q", event.Text, "first")
		}
	case <-time.After(time.Second):
		t.Fatal("first text frame was buffered instead of emitted immediately")
	}

	close(releaseSecond)
	select {
	case event := <-events:
		if event.Text != "second" {
			t.Fatalf("second event text = %q, want %q", event.Text, "second")
		}
	case <-time.After(time.Second):
		t.Fatal("second text frame was not emitted")
	}

	if errWrite := <-writeDone; errWrite != nil {
		t.Fatalf("write response frames: %v", errWrite)
	}
}

func marshalCursorTestTextFrame(t *testing.T, text string) []byte {
	t.Helper()
	message, errMarshal := proto.Marshal(&cursorproto.AgentServerMessage{
		Message: &cursorproto.AgentServerMessage_InteractionUpdate{
			InteractionUpdate: &cursorproto.InteractionUpdate{
				Message: &cursorproto.InteractionUpdate_TextDelta{
					TextDelta: &cursorproto.TextDeltaUpdate{Text: text},
				},
			},
		},
	})
	if errMarshal != nil {
		t.Fatalf("marshal text frame: %v", errMarshal)
	}
	frame := bytes.NewBuffer(make([]byte, 0, len(message)+5))
	frame.WriteByte(0)
	if errLength := binary.Write(frame, binary.BigEndian, uint32(len(message))); errLength != nil {
		t.Fatalf("write frame length: %v", errLength)
	}
	frame.Write(message)
	return frame.Bytes()
}
