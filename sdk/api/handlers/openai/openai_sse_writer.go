package openai

import (
	"bytes"
	"encoding/json"
	"io"

	log "github.com/sirupsen/logrus"
)

func writeOpenAIChatSSEChunk(w io.Writer, chunk []byte) {
	if w == nil || len(chunk) == 0 {
		return
	}

	trimmed := bytes.TrimSpace(chunk)
	if len(trimmed) == 0 {
		return
	}

	if isOpenAIChatSSEEncodedChunk(trimmed) {
		normalized, repaired := normalizeOpenAIChatSSEChunk(trimmed)
		if _, err := w.Write(normalized); err != nil {
			return
		}
		writeOpenAIChatSSEDelimiterIfMissing(w, normalized)
		if repaired {
			log.WithFields(log.Fields{
				"event":     "openai_stream_chunk_normalized",
				"shape":     "nested_data_repaired",
				"chunk_len": len(trimmed),
			}).Debug("normalized nested OpenAI SSE data line")
		}
		return
	}

	if _, err := w.Write([]byte("data: ")); err != nil {
		return
	}
	if _, err := w.Write(trimmed); err != nil {
		return
	}
	_, _ = w.Write([]byte("\n\n"))
}

func isOpenAIChatSSEEncodedChunk(chunk []byte) bool {
	chunk = bytes.TrimLeft(chunk, " \t\r\n")
	return bytes.HasPrefix(chunk, []byte("data:")) ||
		bytes.HasPrefix(chunk, []byte("event:")) ||
		bytes.HasPrefix(chunk, []byte("id:")) ||
		bytes.HasPrefix(chunk, []byte("retry:")) ||
		bytes.HasPrefix(chunk, []byte(":"))
}

func normalizeOpenAIChatSSEChunk(chunk []byte) ([]byte, bool) {
	trimmed := bytes.TrimSpace(chunk)
	if len(trimmed) == 0 || bytes.ContainsAny(trimmed, "\r\n") || !bytes.HasPrefix(trimmed, []byte("data:")) {
		return trimmed, false
	}

	payload := bytes.TrimSpace(trimmed[len("data:"):])
	if !bytes.HasPrefix(payload, []byte("data:")) {
		return trimmed, false
	}

	nested := bytes.TrimSpace(payload[len("data:"):])
	if !bytes.Equal(nested, []byte("[DONE]")) && !json.Valid(nested) {
		return trimmed, false
	}

	repaired := make([]byte, 0, len(nested)+len("data: "))
	repaired = append(repaired, []byte("data: ")...)
	repaired = append(repaired, nested...)
	return repaired, true
}

func writeOpenAIChatSSEDelimiterIfMissing(w io.Writer, chunk []byte) {
	if bytes.HasSuffix(chunk, []byte("\n\n")) || bytes.HasSuffix(chunk, []byte("\r\n\r\n")) {
		return
	}
	if bytes.HasSuffix(chunk, []byte("\r\n")) {
		_, _ = w.Write([]byte("\r\n"))
		return
	}
	if bytes.HasSuffix(chunk, []byte("\n")) {
		_, _ = w.Write([]byte("\n"))
		return
	}
	_, _ = w.Write([]byte("\n\n"))
}
