package common

import (
	"strconv"

	"github.com/tidwall/sjson"
)

func WrapGeminiCLIResponse(response []byte) []byte {
	out, err := sjson.SetRawBytes([]byte(`{"response":{}}`), "response", response)
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

func SSEEventData(event string, payload []byte) []byte {
	out := make([]byte, 0, len(event)+len(payload)+14)
	out = append(out, "event: "...)
	out = append(out, event...)
	out = append(out, '\n')
	out = append(out, "data: "...)
	out = append(out, payload...)
	return out
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
