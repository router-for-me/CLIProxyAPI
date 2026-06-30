package openai

import (
	"bytes"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func recordResponsesWebsocketCompactTranscriptState(c *gin.Context, requestPayload []byte, responsePayload []byte) {
	if c == nil || c.Request == nil {
		return
	}
	sessionKey := websocketScopedDownstreamSessionKey(c, websocketDownstreamSessionKey(c.Request))
	if sessionKey == "" {
		return
	}
	state, ok := responsesWebsocketCompactTranscriptState(requestPayload, responsePayload)
	if !ok {
		return
	}
	recordResponsesWebsocketTranscriptState(sessionKey, state)
}

func responsesWebsocketCompactTranscriptState(requestPayload []byte, responsePayload []byte) (responsesWebsocketTranscriptState, bool) {
	output := gjson.GetBytes(responsePayload, "output")
	if !output.IsArray() || len(output.Array()) == 0 {
		return responsesWebsocketTranscriptState{}, false
	}

	lastRequest := bytes.Clone(requestPayload)
	if len(lastRequest) == 0 || !jsonValidObject(lastRequest) {
		lastRequest = []byte(`{}`)
	}
	lastRequest, _ = sjson.DeleteBytes(lastRequest, "type")
	lastRequest, _ = sjson.DeleteBytes(lastRequest, "previous_response_id")
	lastRequest, _ = sjson.DeleteBytes(lastRequest, "stream")
	lastRequest, _ = sjson.SetBytes(lastRequest, "stream", true)
	lastRequest, _ = sjson.SetRawBytes(lastRequest, "input", []byte(output.Raw))
	lastRequest = sanitizeResponsesInputForHTTP(lastRequest)

	modelName := strings.TrimSpace(gjson.GetBytes(lastRequest, "model").String())
	return responsesWebsocketTranscriptState{
		lastRequest:          lastRequest,
		lastResponseOutput:   []byte("[]"),
		lastResponseID:       strings.TrimSpace(gjson.GetBytes(responsePayload, "id").String()),
		passthroughModelName: modelName,
	}, true
}

func jsonValidObject(raw []byte) bool {
	parsed := gjson.ParseBytes(raw)
	return parsed.IsObject()
}
