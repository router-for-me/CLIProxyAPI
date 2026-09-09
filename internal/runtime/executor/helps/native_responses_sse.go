package helps

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// FrameNativeResponsesChunk preserves the upstream event name on a translated
// identity chunk. Named frames supplied by plugin normalizers stay intact.
func FrameNativeResponsesChunk(eventName string, chunk []byte) []byte {
	payload := bytes.TrimSpace(chunk)
	if !bytes.HasPrefix(payload, []byte("data:")) {
		return chunk
	}
	payload = bytes.TrimSpace(payload[len("data:"):])
	if !json.Valid(payload) {
		return chunk
	}
	var frame bytes.Buffer
	if eventName != "" {
		_, _ = fmt.Fprintf(&frame, "event: %s\n", eventName)
	}
	frame.WriteString("data: ")
	frame.Write(bytes.ReplaceAll(payload, []byte("\n"), []byte("\ndata: ")))
	frame.WriteString("\n\n")
	return frame.Bytes()
}
