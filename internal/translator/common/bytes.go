package common

import (
	"bytes"
	"encoding/json"
	"strconv"

	"github.com/MoeForge/moego/ojson"
)

// WrapGeminiCLIResponse embeds a Gemini response in the envelope expected by Gemini CLI clients.
func WrapGeminiCLIResponse(response []byte) []byte {
	out, err := ojson.SetRawBytes([]byte(`{"response":{}}`), "response", response)
	if err != nil {
		return response
	}
	return out
}

func GeminiTokenCountJSON(count int64) []byte {
	out := make([]byte, 0, 96)
	out = append(out, `{"totalTokens":`...)
	out = strconv.AppendInt(out, count, 10)
	out = append(out, `,"promptTokensDetails":[{"modality":"TEXT","tokenCount":`...)
	out = strconv.AppendInt(out, count, 10)
	out = append(out, `}]}`...)
	return out
}

func ClaudeInputTokensJSON(count int64) []byte {
	out := make([]byte, 0, 32)
	out = append(out, `{"input_tokens":`...)
	out = strconv.AppendInt(out, count, 10)
	out = append(out, '}')
	return out
}

// NewRawArrayItems creates a raw item slice sized for the expected input.
func NewRawArrayItems(capacity int64) [][]byte {
	if capacity <= 0 {
		return nil
	}
	return make([][]byte, 0, int(capacity))
}

func JoinRawArray(items [][]byte) []byte {
	if len(items) == 0 {
		return []byte("[]")
	}
	size := len(items) + 1
	for _, item := range items {
		size += len(item)
	}
	out := make([]byte, 0, size)
	out = append(out, '[')
	for i, item := range items {
		if i > 0 {
			out = append(out, ',')
		}
		out = append(out, item...)
	}
	return append(out, ']')
}

// SetRawArrayItems replaces an empty JSON array at path with raw items.
// The single-item path avoids allocating an intermediate joined array.
func SetRawArrayItems(data []byte, path string, items [][]byte) []byte {
	if len(items) == 0 {
		return data
	}
	if len(items) == 1 {
		array := ojson.GetBytesNoCopy(data, path)
		if array.Raw == "[]" && array.Index >= 0 && array.Index+len(array.Raw) <= len(data) {
			out := make([]byte, 0, len(data)+len(items[0]))
			out = append(out, data[:array.Index]...)
			out = append(out, '[')
			out = append(out, items[0]...)
			out = append(out, ']')
			return append(out, data[array.Index+len(array.Raw):]...)
		}
	}
	data, _ = ojson.SetRawBytes(data, path, JoinRawArray(items))
	return data
}

// SetRawArrayItemsDoc replaces a JSON array at path in doc with raw items.
func SetRawArrayItemsDoc(doc *ojson.Doc, path string, items [][]byte) {
	if doc == nil || len(items) == 0 {
		return
	}
	_ = doc.SetRawBytes(path, JoinRawArray(items))
}

// SSEEventData builds one complete SSE frame. Each frame carries its own
// blank-line terminator so concatenated frames stay separable downstream.
func SSEEventData(event string, payload []byte) []byte {
	out := make([]byte, 0, len(event)+len(payload)+16)
	out = append(out, "event: "...)
	out = append(out, event...)
	out = append(out, '\n')
	out = append(out, "data: "...)
	out = append(out, payload...)
	return append(out, '\n', '\n')
}

func AppendSSEEventString(out []byte, event, payload string, trailingNewlines int) []byte {
	out = append(out, "event: "...)
	out = append(out, event...)
	out = append(out, '\n')
	out = append(out, "data: "...)
	out = append(out, payload...)
	for i := 0; i < trailingNewlines; i++ {
		out = append(out, '\n')
	}
	return out
}

func AppendSSEEventBytes(out []byte, event string, payload []byte, trailingNewlines int) []byte {
	out = append(out, "event: "...)
	out = append(out, event...)
	out = append(out, '\n')
	out = append(out, "data: "...)
	out = append(out, payload...)
	for i := 0; i < trailingNewlines; i++ {
		out = append(out, '\n')
	}
	return out
}

// SetStringWithoutHTMLEscape sets a string field in a JSON payload without escaping
// HTML characters (<, >, &). Standard ojson.Set/SetBytes delegates string values
// containing quotes or special characters to encoding/json.Marshal, which unconditionally
// escapes <, >, & to \u003c, \u003e, \u0026.
func SetStringWithoutHTMLEscape(data []byte, path, value string) ([]byte, error) {
	buf := &bytes.Buffer{}
	enc := json.NewEncoder(buf)
	enc.SetEscapeHTML(false)
	if errEncode := enc.Encode(value); errEncode != nil {
		return ojson.SetBytes(data, path, value)
	}
	raw := bytes.TrimRight(buf.Bytes(), "\n")
	return ojson.SetRawBytes(data, path, raw)
}

// SetStringWithoutHTMLEscapeDoc sets a string field in an ojson.Doc without escaping
// HTML characters (<, >, &).
func SetStringWithoutHTMLEscapeDoc(doc *ojson.Doc, path, value string) error {
	if doc == nil {
		return nil
	}
	buf := &bytes.Buffer{}
	enc := json.NewEncoder(buf)
	enc.SetEscapeHTML(false)
	if errEncode := enc.Encode(value); errEncode != nil {
		return doc.Set(path, value)
	}
	raw := bytes.TrimRight(buf.Bytes(), "\n")
	return doc.SetRawBytes(path, raw)
}
