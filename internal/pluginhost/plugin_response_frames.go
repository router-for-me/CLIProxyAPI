package pluginhost

import (
	"bytes"
	"encoding/json"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
)

// Plugin chunks need not coincide with JSON/SSE boundaries. Feed complete lines
// to the Responses translator so a split usage snapshot cannot become zero.
type pluginResponseFrames struct{ pending []byte }

func (f *pluginResponseFrames) feed(payload []byte) [][]byte {
	if len(f.pending) == 0 && json.Valid(bytes.TrimSpace(payload)) {
		return [][]byte{payload}
	}
	f.pending = append(f.pending, payload...)
	var frames [][]byte
	for {
		index := bytes.IndexByte(f.pending, '\n')
		if index < 0 {
			break
		}
		frames = append(frames, bytes.Clone(f.pending[:index+1]))
		f.pending = f.pending[index+1:]
	}
	if raw := helps.ExtractStreamJSONPayload(f.pending); json.Valid(raw) || bytes.Equal(bytes.TrimSpace(raw), []byte("[DONE]")) {
		frames = append(frames, bytes.Clone(f.pending))
		f.pending = nil
	}
	return frames
}
