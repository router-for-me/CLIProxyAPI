package openai

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	log "github.com/sirupsen/logrus"
)

func shouldHandleResponsesWebsocketPrewarmLocally(rawJSON []byte, allowIncrementalInputWithPreviousResponseID bool) bool {
	if allowIncrementalInputWithPreviousResponseID {
		return false
	}
	var fieldsBuf [2]ojson.Result
	fields := ojson.GetManyBytesNoCopy(fieldsBuf[:0], rawJSON, "type", "generate")
	if strings.TrimSpace(fields[0].String()) != wsRequestTypeCreate {
		return false
	}
	generateResult := fields[1]
	return generateResult.Exists() && !generateResult.Bool()
}

func writeResponsesWebsocketSyntheticPrewarm(
	c *gin.Context,
	writer *responsesWebsocketWriter,
	requestJSON []byte,
	wsTimelineLog websocketTimelineAppender,
	sessionID string,
) (string, error) {
	payloads, errPayloads := syntheticResponsesWebsocketPrewarmPayloads(requestJSON)
	if errPayloads != nil {
		return "", errPayloads
	}
	for i := 0; i < len(payloads); i++ {
		markAPIResponseTimestamp(c)
		// log.Infof(
		// 	"responses websocket: downstream_out id=%s type=%d event=%s payload=%s",
		// 	sessionID,
		// 	websocket.TextMessage,
		// 	websocketPayloadEventType(payloads[i]),
		// 	websocketPayloadPreview(payloads[i]),
		// )
		if errWrite := writeResponsesWebsocketPayload(writer, wsTimelineLog, payloads[i], time.Now()); errWrite != nil {
			log.Warnf(
				"responses websocket: downstream_out write failed id=%s event=%s error=%v",
				sessionID,
				websocketPayloadEventType(payloads[i]),
				errWrite,
			)
			return "", errWrite
		}
	}
	return strings.Clone(ojson.GetBytesNoCopy(payloads[0], "response.id").String()), nil
}

// A synthetic warm-up acknowledges input that never reached the upstream.
// Materialize that input before compacted-history detection can mistake the
// client's remaining delta for a complete replacement transcript.
func normalizeResponsesWebsocketPrewarmFollowup(rawJSON, warmupRequest []byte) ([]byte, []byte, *interfaces.ErrorMessage) {
	var fieldsBuf [2]ojson.Result
	fields := ojson.GetManyBytesNoCopy(fieldsBuf[:0], rawJSON, "type", "input")
	requestType := strings.TrimSpace(fields[0].String())
	if requestType != wsRequestTypeCreate && requestType != wsRequestTypeAppend {
		return nil, warmupRequest, &interfaces.ErrorMessage{StatusCode: http.StatusBadRequest, Error: fmt.Errorf("unsupported websocket request type: %s", requestType)}
	}
	input := fields[1]
	if !input.IsArray() {
		return nil, warmupRequest, &interfaces.ErrorMessage{StatusCode: http.StatusBadRequest, Error: fmt.Errorf("websocket request requires array field: input")}
	}
	merged, errMerge := mergeResponsesWebsocketInput(warmupRequest, []byte("[]"), input.Raw)
	if errMerge != nil {
		return nil, warmupRequest, &interfaces.ErrorMessage{StatusCode: http.StatusBadRequest, Error: errMerge}
	}
	doc := ojson.NewDoc(rawJSON)
	normalizeResponseTranscriptReplacementDoc(doc, warmupRequest)
	if errSet := doc.SetRawBytes("input", merged); errSet != nil {
		return nil, warmupRequest, &interfaces.ErrorMessage{StatusCode: http.StatusBadRequest, Error: errSet}
	}
	normalized := doc.Bytes()
	return normalized, normalized, nil
}

func syntheticResponsesWebsocketPrewarmPayloads(requestJSON []byte) ([][]byte, error) {
	responseID := "resp_prewarm_" + uuid.NewString()
	createdAt := time.Now().Unix()
	modelName := strings.TrimSpace(ojson.GetBytesNoCopy(requestJSON, "model").String())

	createdDoc := ojson.NewDoc([]byte(`{"type":"response.created","sequence_number":0,"response":{"id":"","object":"response","created_at":0,"status":"in_progress","background":false,"error":null,"output":[]}}`))
	if errSet := createdDoc.Set("response.id", responseID); errSet != nil {
		return nil, errSet
	}
	if errSet := createdDoc.Set("response.created_at", createdAt); errSet != nil {
		return nil, errSet
	}
	if modelName != "" {
		if errSet := createdDoc.Set("response.model", modelName); errSet != nil {
			return nil, errSet
		}
	}

	completedDoc := ojson.NewDoc([]byte(`{"type":"response.completed","sequence_number":1,"response":{"id":"","object":"response","created_at":0,"status":"completed","background":false,"error":null,"output":[],"usage":{"input_tokens":0,"input_tokens_details":{"cached_tokens":0},"output_tokens":0,"output_tokens_details":{"reasoning_tokens":0},"total_tokens":0}}}`))
	if errSet := completedDoc.Set("response.id", responseID); errSet != nil {
		return nil, errSet
	}
	if errSet := completedDoc.Set("response.created_at", createdAt); errSet != nil {
		return nil, errSet
	}
	if modelName != "" {
		if errSet := completedDoc.Set("response.model", modelName); errSet != nil {
			return nil, errSet
		}
	}

	return [][]byte{createdDoc.Bytes(), completedDoc.Bytes()}, nil
}

// inputContainsFullTranscript returns true when the input array carries compact
// replay markers that indicate the client already sent the full conversation
// transcript. Merging that input with stale lastRequest/lastResponseOutput
// would duplicate or break function_call/function_call_output pairings, so the
// caller should use the input as-is.
//
// Assistant messages alone are not enough to classify the payload as a replay:
// incremental websocket requests may legitimately append assistant items.
func inputContainsFullTranscript(input ojson.Result) bool {
	if !input.IsArray() {
		return false
	}
	for _, item := range input.Array() {
		t := item.Get("type").String()
		if t == "compaction" || t == "compaction_summary" {
			return true
		}
	}
	return false
}

func inputWithoutCompactionItems(input ojson.Result) string {
	if !input.IsArray() {
		return normalizeJSONArrayRaw([]byte(input.Raw))
	}
	filtered := make([]string, 0, len(input.Array()))
	for _, item := range input.Array() {
		t := item.Get("type").String()
		if t == "compaction" || t == "compaction_summary" {
			continue
		}
		filtered = append(filtered, item.Raw)
	}
	return "[" + strings.Join(filtered, ",") + "]"
}

func normalizeJSONArrayRaw(raw []byte) string {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return "[]"
	}
	result := ojson.Parse(trimmed)
	if result.Type == ojson.JSON && result.IsArray() {
		return trimmed
	}
	return "[]"
}
