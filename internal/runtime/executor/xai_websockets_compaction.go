package executor

import (
	"bytes"
	"net/http"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// xaiWebsocketCompactionSource is the input sent to POST /responses/compact
// when a websocket compaction_trigger arrives.
type xaiWebsocketCompactionSource struct {
	input                  []byte
	keepPreviousResponseID bool
}

// resolveXAIWebsocketCompactionSource picks compact input when the in-memory
// websocket transcript is missing.
//
// Priority:
//  1. recorded transcript
//  2. payload input after dropping compaction_trigger items
//  3. previous_response_id (mapped upstream id wins)
//  4. empty-context error
func resolveXAIWebsocketCompactionSource(transcriptInput []byte, payload []byte, upstreamPreviousResponseID string) (xaiWebsocketCompactionSource, error) {
	if len(transcriptInput) > 0 {
		return xaiWebsocketCompactionSource{input: transcriptInput}, nil
	}

	remaining := compactionPayloadInputWithoutTrigger(payload)
	if len(remaining) > 0 {
		return xaiWebsocketCompactionSource{input: remaining}, nil
	}

	previousID := strings.TrimSpace(upstreamPreviousResponseID)
	if previousID == "" {
		previousID = strings.TrimSpace(gjson.GetBytes(payload, "previous_response_id").String())
	}
	if previousID == "" {
		return xaiWebsocketCompactionSource{}, statusErr{code: http.StatusBadRequest, msg: "xai websocket compaction context is empty"}
	}
	return xaiWebsocketCompactionSource{input: []byte("[]"), keepPreviousResponseID: true}, nil
}

func compactionPayloadInputWithoutTrigger(payload []byte) []byte {
	stripped := xaiRemoveInputItemsByType(payload, "compaction_trigger")
	input := gjson.GetBytes(stripped, "input")
	if !input.IsArray() || len(input.Array()) == 0 {
		return nil
	}
	return []byte(input.Raw)
}

func buildXAIWebsocketCompactionPayloadFromSource(payload []byte, source xaiWebsocketCompactionSource, upstreamPreviousResponseID string) ([]byte, error) {
	input := source.input
	if len(input) == 0 {
		input = []byte("[]")
	}
	if len(payload) == 0 {
		payload = []byte(`{}`)
	}
	out := bytes.Clone(payload)
	var err error
	out, err = sjson.SetRawBytes(out, "input", input)
	if err != nil {
		return nil, err
	}
	if source.keepPreviousResponseID {
		previousID := strings.TrimSpace(upstreamPreviousResponseID)
		if previousID == "" {
			previousID = strings.TrimSpace(gjson.GetBytes(payload, "previous_response_id").String())
		}
		if previousID != "" {
			out, err = sjson.SetBytes(out, "previous_response_id", previousID)
			if err != nil {
				return nil, err
			}
		}
		return out, nil
	}
	out, _ = sjson.DeleteBytes(out, "previous_response_id")
	return out, nil
}
