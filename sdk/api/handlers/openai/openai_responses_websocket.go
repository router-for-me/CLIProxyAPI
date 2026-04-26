package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	wsRequestTypeCreate  = "response.create"
	wsRequestTypeAppend  = "response.append"
	wsEventTypeError     = "error"
	wsEventTypeCompleted = "response.completed"
	wsDoneMarker         = "[DONE]"
	wsTurnStateHeader    = "x-codex-turn-state"
	wsTimelineBodyKey    = "WEBSOCKET_TIMELINE_OVERRIDE"

	maxResponsesWebsocketTimelineBytes      = 4 << 20
	maxResponsesWebsocketErrorTimelineBytes = 64 << 10
)

const responsesWebsocketTimelineTruncatedMarker = "\n...[websocket timeline truncated]...\n"

type websocketTimelineBuilder struct {
	strings.Builder
	maxBytes int
}

func newWebsocketTimelineBuilder(maxBytes int) websocketTimelineBuilder {
	if maxBytes <= 0 {
		maxBytes = maxResponsesWebsocketTimelineBytes
	}
	return websocketTimelineBuilder{maxBytes: maxBytes}
}

func responsesWebsocketTimelineLimit(h *OpenAIResponsesAPIHandler) int {
	if h == nil || h.Cfg == nil || h.Cfg.RequestLog {
		return maxResponsesWebsocketTimelineBytes
	}
	return maxResponsesWebsocketErrorTimelineBytes
}

var responsesWebsocketUpgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

// ResponsesWebsocket handles websocket requests for /v1/responses.
// It accepts `response.create` and `response.append` requests and streams
// response events back as JSON websocket text messages.
func (h *OpenAIResponsesAPIHandler) ResponsesWebsocket(c *gin.Context) {
	conn, err := responsesWebsocketUpgrader.Upgrade(c.Writer, c.Request, websocketUpgradeHeaders(c.Request))
	if err != nil {
		return
	}
	connectionID := uuid.NewString()
	fallbackExecutionSessionID := uuid.NewString()
	activeExecutionSessionID := ""
	headerExecutionSessionID := responsesExplicitExecutionSessionID(c.Request, nil)
	downstreamSessionKey := websocketDownstreamSessionKey(c.Request)
	if downstreamSessionKey == "" {
		downstreamSessionKey = connectionID
	}
	retainResponsesWebsocketToolCaches(downstreamSessionKey)
	clientIP := websocketClientAddress(c)
	log.Infof("responses websocket: client connected id=%s remote=%s", connectionID, clientIP)
	var wsTerminateErr error
	wsTimelineLog := newWebsocketTimelineBuilder(responsesWebsocketTimelineLimit(h))
	defer func() {
		releaseResponsesWebsocketToolCaches(downstreamSessionKey)
		if wsTerminateErr != nil {
			appendWebsocketTimelineDisconnect(&wsTimelineLog, wsTerminateErr, time.Now())
			// log.Infof("responses websocket: session closing id=%s reason=%v", connectionID, wsTerminateErr)
		} else {
			log.Infof("responses websocket: session closing id=%s", connectionID)
		}
		if h != nil && h.AuthManager != nil {
			closeExecutionSessionID := strings.TrimSpace(activeExecutionSessionID)
			if closeExecutionSessionID == "" {
				if headerExecutionSessionID != "" {
					closeExecutionSessionID = headerExecutionSessionID
				} else {
					closeExecutionSessionID = fallbackExecutionSessionID
				}
			}
			h.AuthManager.CloseExecutionSession(closeExecutionSessionID)
			log.Infof("responses websocket: upstream execution session closed id=%s", closeExecutionSessionID)
		}
		setWebsocketTimelineBody(c, wsTimelineLog.String())
		if errClose := conn.Close(); errClose != nil {
			log.Warnf("responses websocket: close connection error: %v", errClose)
		}
	}()

	var lastRequest []byte
	lastResponseOutput := []byte("[]")
	pinnedAuthID := ""
	incrementalInputSupportByModel := make(map[string]bool)

	for {
		msgType, payload, errReadMessage := conn.ReadMessage()
		if errReadMessage != nil {
			wsTerminateErr = errReadMessage
			if websocket.IsCloseError(errReadMessage, websocket.CloseNormalClosure, websocket.CloseGoingAway, websocket.CloseNoStatusReceived) {
				log.Infof("responses websocket: client disconnected id=%s error=%v", connectionID, errReadMessage)
			} else {
				// log.Warnf("responses websocket: read message failed id=%s error=%v", connectionID, errReadMessage)
			}
			return
		}
		if msgType != websocket.TextMessage && msgType != websocket.BinaryMessage {
			continue
		}
		// log.Infof(
		// 	"responses websocket: downstream_in id=%s type=%d event=%s payload=%s",
		// 	passthroughSessionID,
		// 	msgType,
		// 	websocketPayloadEventType(payload),
		// 	websocketPayloadPreview(payload),
		// )
		appendWebsocketTimelineEvent(&wsTimelineLog, "request", payload, time.Now())
		replacesTranscript := shouldReplaceWebsocketTranscript(payload, gjson.GetBytes(payload, "input"))

		currentExecutionSessionID := headerExecutionSessionID
		if currentExecutionSessionID == "" {
			currentExecutionSessionID = responsesExplicitExecutionSessionID(c.Request, payload)
		}
		if currentExecutionSessionID == "" {
			if activeExecutionSessionID != "" {
				currentExecutionSessionID = activeExecutionSessionID
			} else {
				currentExecutionSessionID = fallbackExecutionSessionID
			}
		}
		sessionChanged := activeExecutionSessionID != "" && activeExecutionSessionID != currentExecutionSessionID
		normalizationLastRequest := lastRequest
		normalizationLastResponseOutput := lastResponseOutput
		if sessionChanged {
			normalizationLastRequest = nil
			normalizationLastResponseOutput = []byte("[]")
		}

		allowIncrementalInputWithPreviousResponseID := false
		if pinnedAuthID != "" {
			allowIncrementalInputWithPreviousResponseID = true
		} else {
			requestModelName := strings.TrimSpace(gjson.GetBytes(payload, "model").String())
			if requestModelName == "" {
				requestModelName = strings.TrimSpace(gjson.GetBytes(normalizationLastRequest, "model").String())
			}
			allowIncrementalInputWithPreviousResponseID = cachedResponsesWebsocketIncrementalInputSupport(
				incrementalInputSupportByModel,
				requestModelName,
				h.websocketUpstreamSupportsIncrementalInputForModel,
			)
		}

		var requestJSON []byte
		var updatedLastRequest []byte
		var errMsg *interfaces.ErrorMessage
		requestJSON, updatedLastRequest, errMsg = normalizeResponsesWebsocketRequestWithMode(
			payload,
			normalizationLastRequest,
			normalizationLastResponseOutput,
			allowIncrementalInputWithPreviousResponseID,
		)
		if errMsg != nil {
			h.LoggingAPIResponseError(context.WithValue(context.Background(), "gin", c), errMsg)
			markAPIResponseTimestamp(c)
			errorPayload, errWrite := writeResponsesWebsocketError(conn, &wsTimelineLog, errMsg)
			log.Infof(
				"responses websocket: downstream_out id=%s type=%d event=%s payload=%s",
				connectionID,
				websocket.TextMessage,
				websocketPayloadEventType(errorPayload),
				websocketPayloadPreview(errorPayload),
			)
			if errWrite != nil {
				log.Warnf(
					"responses websocket: downstream_out write failed id=%s event=%s error=%v",
					connectionID,
					websocketPayloadEventType(errorPayload),
					errWrite,
				)
				return
			}
			continue
		}

		toolCachesReset := false
		if sessionChanged {
			if h != nil && h.AuthManager != nil {
				h.AuthManager.ResetExecutionSession(activeExecutionSessionID)
			}
			resetResponsesWebsocketToolCaches(downstreamSessionKey)
			toolCachesReset = true
		}
		activeExecutionSessionID = currentExecutionSessionID

		if shouldHandleResponsesWebsocketPrewarmLocally(payload, normalizationLastRequest, allowIncrementalInputWithPreviousResponseID) {
			if updated, errDelete := sjson.DeleteBytes(requestJSON, "generate"); errDelete == nil {
				requestJSON = updated
			}
			if updated, errDelete := sjson.DeleteBytes(updatedLastRequest, "generate"); errDelete == nil {
				updatedLastRequest = updated
			}
			lastRequest = updatedLastRequest
			lastResponseOutput = []byte("[]")
			if errWrite := writeResponsesWebsocketSyntheticPrewarm(c, conn, requestJSON, &wsTimelineLog, connectionID); errWrite != nil {
				wsTerminateErr = errWrite
				return
			}
			continue
		}

		if replacesTranscript {
			if !toolCachesReset {
				resetResponsesWebsocketToolCaches(downstreamSessionKey)
			}
			requestJSON = repairResponsesWebsocketToolCalls(downstreamSessionKey, requestJSON)
		} else {
			requestJSON = repairResponsesWebsocketToolCalls(downstreamSessionKey, requestJSON)
		}
		updatedLastRequest = requestJSON
		lastRequest = requestJSON

		modelName := gjson.GetBytes(requestJSON, "model").String()
		cliCtx, cliCancel := h.GetContextWithCancel(h, c, context.Background())
		cliCtx = cliproxyexecutor.WithDownstreamWebsocket(cliCtx)
		cliCtx = handlers.WithExecutionSessionID(cliCtx, currentExecutionSessionID)
		if pinnedAuthID != "" {
			cliCtx = handlers.WithPinnedAuthID(cliCtx, pinnedAuthID)
		} else {
			cliCtx = handlers.WithSelectedAuthIDCallback(cliCtx, func(authID string) {
				authID = strings.TrimSpace(authID)
				if authID == "" || h == nil || h.AuthManager == nil {
					return
				}
				selectedAuth, ok := h.AuthManager.GetByID(authID)
				if !ok || selectedAuth == nil {
					return
				}
				if websocketUpstreamSupportsIncrementalInput(selectedAuth.Attributes, selectedAuth.Metadata) {
					pinnedAuthID = authID
				}
			})
		}
		dataChan, _, errChan := h.ExecuteStreamWithAuthManager(cliCtx, h.HandlerType(), modelName, requestJSON, "")

		completedOutput, errForward := h.forwardResponsesWebsocket(c, conn, cliCancel, dataChan, errChan, &wsTimelineLog, connectionID, downstreamSessionKey)
		if errForward != nil {
			wsTerminateErr = errForward
			log.Warnf("responses websocket: forward failed id=%s error=%v", connectionID, errForward)
			return
		}
		lastResponseOutput = completedOutput
	}
}

func websocketClientAddress(c *gin.Context) string {
	if c == nil || c.Request == nil {
		return ""
	}
	return strings.TrimSpace(c.ClientIP())
}

func cachedResponsesWebsocketIncrementalInputSupport(cache map[string]bool, modelName string, resolve func(string) bool) bool {
	if resolve == nil {
		return false
	}

	modelName = strings.TrimSpace(modelName)
	if modelName == "" || cache == nil {
		return resolve(modelName)
	}

	if supported, ok := cache[modelName]; ok {
		return supported
	}

	supported := resolve(modelName)
	cache[modelName] = supported
	return supported
}

func websocketUpgradeHeaders(req *http.Request) http.Header {
	headers := http.Header{}
	if req == nil {
		return headers
	}

	// Keep the same sticky turn-state across reconnects when provided by the client.
	turnState := strings.TrimSpace(req.Header.Get(wsTurnStateHeader))
	if turnState != "" {
		headers.Set(wsTurnStateHeader, turnState)
	}
	return headers
}

func normalizeResponsesWebsocketRequest(rawJSON []byte, lastRequest []byte, lastResponseOutput []byte) ([]byte, []byte, *interfaces.ErrorMessage) {
	return normalizeResponsesWebsocketRequestWithMode(rawJSON, lastRequest, lastResponseOutput, true)
}

func normalizeResponsesWebsocketRequestWithMode(rawJSON []byte, lastRequest []byte, lastResponseOutput []byte, allowIncrementalInputWithPreviousResponseID bool) ([]byte, []byte, *interfaces.ErrorMessage) {
	requestType := strings.TrimSpace(gjson.GetBytes(rawJSON, "type").String())
	switch requestType {
	case wsRequestTypeCreate:
		// log.Infof("responses websocket: response.create request")
		if len(lastRequest) == 0 {
			return normalizeResponseCreateRequest(rawJSON)
		}
		return normalizeResponseSubsequentRequest(rawJSON, lastRequest, lastResponseOutput, allowIncrementalInputWithPreviousResponseID)
	case wsRequestTypeAppend:
		// log.Infof("responses websocket: response.append request")
		return normalizeResponseSubsequentRequest(rawJSON, lastRequest, lastResponseOutput, allowIncrementalInputWithPreviousResponseID)
	default:
		return nil, lastRequest, &interfaces.ErrorMessage{
			StatusCode: http.StatusBadRequest,
			Error:      fmt.Errorf("unsupported websocket request type: %s", requestType),
		}
	}
}

func normalizeResponseCreateRequest(rawJSON []byte) ([]byte, []byte, *interfaces.ErrorMessage) {
	normalized, errDelete := sjson.DeleteBytes(rawJSON, "type")
	if errDelete != nil {
		normalized = rawJSON
	}
	normalized, _ = sjson.SetBytes(normalized, "stream", true)
	if !gjson.GetBytes(normalized, "input").Exists() {
		normalized, _ = sjson.SetRawBytes(normalized, "input", []byte("[]"))
	}

	modelName := strings.TrimSpace(gjson.GetBytes(normalized, "model").String())
	if modelName == "" {
		return nil, nil, &interfaces.ErrorMessage{
			StatusCode: http.StatusBadRequest,
			Error:      fmt.Errorf("missing model in response.create request"),
		}
	}
	return normalized, normalized, nil
}

func normalizeResponseSubsequentRequest(rawJSON []byte, lastRequest []byte, lastResponseOutput []byte, allowIncrementalInputWithPreviousResponseID bool) ([]byte, []byte, *interfaces.ErrorMessage) {
	if len(lastRequest) == 0 {
		return nil, lastRequest, &interfaces.ErrorMessage{
			StatusCode: http.StatusBadRequest,
			Error:      fmt.Errorf("websocket request received before response.create"),
		}
	}

	nextInput := gjson.GetBytes(rawJSON, "input")
	if !nextInput.Exists() || !nextInput.IsArray() {
		return nil, lastRequest, &interfaces.ErrorMessage{
			StatusCode: http.StatusBadRequest,
			Error:      fmt.Errorf("websocket request requires array field: input"),
		}
	}

	// Compaction can cause clients to replace local websocket history with a new
	// compact transcript on the next `response.create`. When the input already
	// contains historical model output items, treating it as an incremental append
	// duplicates stale turn-state and can leave late orphaned function_call items.
	if shouldReplaceWebsocketTranscript(rawJSON, nextInput) {
		normalized := normalizeResponseTranscriptReplacement(rawJSON, lastRequest)
		return normalized, normalized, nil
	}

	// Websocket v2 mode uses response.create with previous_response_id + incremental input.
	// Do not expand it into a full input transcript; upstream expects the incremental payload.
	if allowIncrementalInputWithPreviousResponseID {
		if prev := strings.TrimSpace(gjson.GetBytes(rawJSON, "previous_response_id").String()); prev != "" {
			normalized, errDelete := sjson.DeleteBytes(rawJSON, "type")
			if errDelete != nil {
				normalized = rawJSON
			}
			if !gjson.GetBytes(normalized, "model").Exists() {
				modelName := strings.TrimSpace(gjson.GetBytes(lastRequest, "model").String())
				if modelName != "" {
					normalized, _ = sjson.SetBytes(normalized, "model", modelName)
				}
			}
			if !gjson.GetBytes(normalized, "instructions").Exists() {
				instructions := gjson.GetBytes(lastRequest, "instructions")
				if instructions.Exists() {
					normalized, _ = sjson.SetRawBytes(normalized, "instructions", []byte(instructions.Raw))
				}
			}
			normalized, _ = sjson.SetBytes(normalized, "stream", true)
			return normalized, normalized, nil
		}
	}

	existingInput := gjson.GetBytes(lastRequest, "input")
	mergedInput, errMerge := mergeJSONArrayRaw(existingInput.Raw, normalizeJSONArrayRaw(lastResponseOutput))
	if errMerge != nil {
		return nil, lastRequest, &interfaces.ErrorMessage{
			StatusCode: http.StatusBadRequest,
			Error:      fmt.Errorf("invalid previous response output: %w", errMerge),
		}
	}

	mergedInput, errMerge = mergeJSONArrayRaw(mergedInput, nextInput.Raw)
	if errMerge != nil {
		return nil, lastRequest, &interfaces.ErrorMessage{
			StatusCode: http.StatusBadRequest,
			Error:      fmt.Errorf("invalid request input: %w", errMerge),
		}
	}
	dedupedInput, errDedupeFunctionCalls := dedupeFunctionCallsByCallID(mergedInput)
	if errDedupeFunctionCalls == nil {
		mergedInput = dedupedInput
	}

	normalized, errDelete := sjson.DeleteBytes(rawJSON, "type")
	if errDelete != nil {
		normalized = rawJSON
	}
	normalized, _ = sjson.DeleteBytes(normalized, "previous_response_id")
	var errSet error
	normalized, errSet = sjson.SetRawBytes(normalized, "input", []byte(mergedInput))
	if errSet != nil {
		return nil, lastRequest, &interfaces.ErrorMessage{
			StatusCode: http.StatusBadRequest,
			Error:      fmt.Errorf("failed to merge websocket input: %w", errSet),
		}
	}
	if !gjson.GetBytes(normalized, "model").Exists() {
		modelName := strings.TrimSpace(gjson.GetBytes(lastRequest, "model").String())
		if modelName != "" {
			normalized, _ = sjson.SetBytes(normalized, "model", modelName)
		}
	}
	if !gjson.GetBytes(normalized, "instructions").Exists() {
		instructions := gjson.GetBytes(lastRequest, "instructions")
		if instructions.Exists() {
			normalized, _ = sjson.SetRawBytes(normalized, "instructions", []byte(instructions.Raw))
		}
	}
	normalized, _ = sjson.SetBytes(normalized, "stream", true)
	return normalized, normalized, nil
}

func shouldReplaceWebsocketTranscript(rawJSON []byte, nextInput gjson.Result) bool {
	requestType := strings.TrimSpace(gjson.GetBytes(rawJSON, "type").String())
	if requestType != wsRequestTypeCreate && requestType != wsRequestTypeAppend {
		return false
	}
	if strings.TrimSpace(gjson.GetBytes(rawJSON, "previous_response_id").String()) != "" {
		return false
	}
	if !nextInput.Exists() || !nextInput.IsArray() {
		return false
	}

	replace := false
	nextInput.ForEach(func(_, item gjson.Result) bool {
		switch strings.TrimSpace(item.Get("type").String()) {
		case "function_call", "custom_tool_call":
			replace = true
			return false
		case "message":
			role := strings.TrimSpace(item.Get("role").String())
			if role == "assistant" {
				replace = true
				return false
			}
		}
		return true
	})

	return replace
}

func normalizeResponseTranscriptReplacement(rawJSON []byte, lastRequest []byte) []byte {
	normalized, errDelete := sjson.DeleteBytes(rawJSON, "type")
	if errDelete != nil {
		normalized = rawJSON
	}
	normalized, _ = sjson.DeleteBytes(normalized, "previous_response_id")
	if !gjson.GetBytes(normalized, "model").Exists() {
		modelName := strings.TrimSpace(gjson.GetBytes(lastRequest, "model").String())
		if modelName != "" {
			normalized, _ = sjson.SetBytes(normalized, "model", modelName)
		}
	}
	if !gjson.GetBytes(normalized, "instructions").Exists() {
		instructions := gjson.GetBytes(lastRequest, "instructions")
		if instructions.Exists() {
			normalized, _ = sjson.SetRawBytes(normalized, "instructions", []byte(instructions.Raw))
		}
	}
	normalized, _ = sjson.SetBytes(normalized, "stream", true)
	return normalized
}

func dedupeFunctionCallsByCallID(rawArray string) (string, error) {
	rawArray, err := validatedJSONArrayRawString(rawArray)
	if err != nil {
		return "", err
	}

	result := gjson.Parse(rawArray)
	itemCount := int(result.Get("#").Int())
	var seenCallIDs map[string]struct{}
	filtered := make([]string, 0, itemCount)
	duplicated := false
	result.ForEach(func(_, item gjson.Result) bool {
		itemRaw := strings.TrimSpace(item.Raw)
		if itemRaw == "" {
			return true
		}
		itemType := strings.TrimSpace(item.Get("type").String())
		if isResponsesToolCallType(itemType) {
			callID := strings.TrimSpace(item.Get("call_id").String())
			if callID != "" {
				if seenCallIDs == nil {
					seenCallIDs = make(map[string]struct{}, itemCount)
				}
				if _, ok := seenCallIDs[callID]; ok {
					duplicated = true
					return true
				}
				seenCallIDs[callID] = struct{}{}
			}
		}
		filtered = append(filtered, itemRaw)
		return true
	})
	if !duplicated {
		return rawArray, nil
	}

	return joinJSONArrayRaw(filtered), nil
}

func websocketUpstreamSupportsIncrementalInput(attributes map[string]string, metadata map[string]any) bool {
	if len(attributes) > 0 {
		if raw := strings.TrimSpace(attributes["websockets"]); raw != "" {
			parsed, errParse := strconv.ParseBool(raw)
			if errParse == nil {
				return parsed
			}
		}
	}
	if len(metadata) == 0 {
		return false
	}
	raw, ok := metadata["websockets"]
	if !ok || raw == nil {
		return false
	}
	switch value := raw.(type) {
	case bool:
		return value
	case string:
		parsed, errParse := strconv.ParseBool(strings.TrimSpace(value))
		if errParse == nil {
			return parsed
		}
	default:
	}
	return false
}

func (h *OpenAIResponsesAPIHandler) websocketUpstreamSupportsIncrementalInputForModel(modelName string) bool {
	if h == nil || h.AuthManager == nil {
		return false
	}

	resolvedModelName := modelName
	initialSuffix := thinking.ParseSuffix(modelName)
	if initialSuffix.ModelName == "auto" {
		resolvedBase := util.ResolveAutoModel(initialSuffix.ModelName)
		if initialSuffix.HasSuffix {
			resolvedModelName = fmt.Sprintf("%s(%s)", resolvedBase, initialSuffix.RawSuffix)
		} else {
			resolvedModelName = resolvedBase
		}
	} else {
		resolvedModelName = util.ResolveAutoModel(modelName)
	}

	parsed := thinking.ParseSuffix(resolvedModelName)
	baseModel := strings.TrimSpace(parsed.ModelName)
	providers := util.GetProviderName(baseModel)
	if len(providers) == 0 && baseModel != resolvedModelName {
		providers = util.GetProviderName(resolvedModelName)
	}
	if len(providers) == 0 {
		return false
	}

	modelKey := baseModel
	if modelKey == "" {
		modelKey = strings.TrimSpace(resolvedModelName)
	}
	return h.AuthManager.AnyAvailableAuthForModel(providers, modelKey, func(auth *coreauth.Auth) bool {
		return websocketUpstreamSupportsIncrementalInput(auth.Attributes, auth.Metadata)
	})
}

func shouldHandleResponsesWebsocketPrewarmLocally(rawJSON []byte, lastRequest []byte, allowIncrementalInputWithPreviousResponseID bool) bool {
	if allowIncrementalInputWithPreviousResponseID || len(lastRequest) != 0 {
		return false
	}
	if strings.TrimSpace(gjson.GetBytes(rawJSON, "type").String()) != wsRequestTypeCreate {
		return false
	}
	generateResult := gjson.GetBytes(rawJSON, "generate")
	return generateResult.Exists() && !generateResult.Bool()
}

func writeResponsesWebsocketSyntheticPrewarm(
	c *gin.Context,
	conn *websocket.Conn,
	requestJSON []byte,
	wsTimelineLog *websocketTimelineBuilder,
	sessionID string,
) error {
	payloads, errPayloads := syntheticResponsesWebsocketPrewarmPayloads(requestJSON)
	if errPayloads != nil {
		return errPayloads
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
		if errWrite := writeResponsesWebsocketPayload(conn, wsTimelineLog, payloads[i], time.Now()); errWrite != nil {
			log.Warnf(
				"responses websocket: downstream_out write failed id=%s event=%s error=%v",
				sessionID,
				websocketPayloadEventType(payloads[i]),
				errWrite,
			)
			return errWrite
		}
	}
	return nil
}

func syntheticResponsesWebsocketPrewarmPayloads(requestJSON []byte) ([][]byte, error) {
	responseID := "resp_prewarm_" + uuid.NewString()
	createdAt := time.Now().Unix()
	modelName := strings.TrimSpace(gjson.GetBytes(requestJSON, "model").String())

	createdPayload := []byte(`{"type":"response.created","sequence_number":0,"response":{"id":"","object":"response","created_at":0,"status":"in_progress","background":false,"error":null,"output":[]}}`)
	var errSet error
	createdPayload, errSet = sjson.SetBytes(createdPayload, "response.id", responseID)
	if errSet != nil {
		return nil, errSet
	}
	createdPayload, errSet = sjson.SetBytes(createdPayload, "response.created_at", createdAt)
	if errSet != nil {
		return nil, errSet
	}
	if modelName != "" {
		createdPayload, errSet = sjson.SetBytes(createdPayload, "response.model", modelName)
		if errSet != nil {
			return nil, errSet
		}
	}

	completedPayload := []byte(`{"type":"response.completed","sequence_number":1,"response":{"id":"","object":"response","created_at":0,"status":"completed","background":false,"error":null,"output":[],"usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0}}}`)
	completedPayload, errSet = sjson.SetBytes(completedPayload, "response.id", responseID)
	if errSet != nil {
		return nil, errSet
	}
	completedPayload, errSet = sjson.SetBytes(completedPayload, "response.created_at", createdAt)
	if errSet != nil {
		return nil, errSet
	}
	if modelName != "" {
		completedPayload, errSet = sjson.SetBytes(completedPayload, "response.model", modelName)
		if errSet != nil {
			return nil, errSet
		}
	}

	return [][]byte{createdPayload, completedPayload}, nil
}

func mergeJSONArrayRaw(existingRaw, appendRaw string) (string, error) {
	existingRaw, err := validatedJSONArrayRawString(existingRaw)
	if err != nil {
		return "", err
	}
	appendRaw, err = validatedJSONArrayRawString(appendRaw)
	if err != nil {
		return "", err
	}
	if existingRaw == "[]" {
		return appendRaw, nil
	}
	if appendRaw == "[]" {
		return existingRaw, nil
	}

	existingBody := strings.TrimSpace(existingRaw[1 : len(existingRaw)-1])
	appendBody := strings.TrimSpace(appendRaw[1 : len(appendRaw)-1])
	if existingBody == "" {
		return appendRaw, nil
	}
	if appendBody == "" {
		return existingRaw, nil
	}

	var builder strings.Builder
	builder.Grow(len(existingBody) + len(appendBody) + 3)
	builder.WriteByte('[')
	builder.WriteString(existingBody)
	builder.WriteByte(',')
	builder.WriteString(appendBody)
	builder.WriteByte(']')
	return builder.String(), nil
}

func normalizeJSONArrayRaw(raw []byte) string {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return "[]"
	}
	if trimmed[0] != '[' || trimmed[len(trimmed)-1] != ']' {
		return "[]"
	}
	if !gjson.ValidBytes(trimmed) {
		return "[]"
	}
	return string(trimmed)
}

func validatedJSONArrayRawString(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "[]", nil
	}
	if trimmed[0] != '[' || trimmed[len(trimmed)-1] != ']' {
		return "", fmt.Errorf("expected JSON array")
	}
	if !gjson.Valid(trimmed) {
		return "", fmt.Errorf("invalid JSON array")
	}
	return trimmed, nil
}

func joinJSONArrayRaw(items []string) string {
	if len(items) == 0 {
		return "[]"
	}
	size := 2
	for _, item := range items {
		size += len(item) + 1
	}
	var builder strings.Builder
	builder.Grow(size)
	builder.WriteByte('[')
	for i, item := range items {
		if i > 0 {
			builder.WriteByte(',')
		}
		builder.WriteString(item)
	}
	builder.WriteByte(']')
	return builder.String()
}

func (h *OpenAIResponsesAPIHandler) forwardResponsesWebsocket(
	c *gin.Context,
	conn *websocket.Conn,
	cancel handlers.APIHandlerCancelFunc,
	data <-chan []byte,
	errs <-chan *interfaces.ErrorMessage,
	wsTimelineLog *websocketTimelineBuilder,
	sessionID string,
	downstreamSessionKey string,
) ([]byte, error) {
	completed := false
	completedOutput := []byte("[]")
	noticeFilter := newResponsesNoticeFilter()

	for {
		select {
		case <-c.Request.Context().Done():
			cancel(c.Request.Context().Err())
			return completedOutput, c.Request.Context().Err()
		case errMsg, ok := <-errs:
			if !ok {
				errs = nil
				continue
			}
			if errMsg != nil {
				h.LoggingAPIResponseError(context.WithValue(context.Background(), "gin", c), errMsg)
				markAPIResponseTimestamp(c)
				errorPayload, errWrite := writeResponsesWebsocketError(conn, wsTimelineLog, errMsg)
				log.Infof(
					"responses websocket: downstream_out id=%s type=%d event=%s payload=%s",
					sessionID,
					websocket.TextMessage,
					websocketPayloadEventType(errorPayload),
					websocketPayloadPreview(errorPayload),
				)
				if errWrite != nil {
					// log.Warnf(
					// 	"responses websocket: downstream_out write failed id=%s event=%s error=%v",
					// 	sessionID,
					// 	websocketPayloadEventType(errorPayload),
					// 	errWrite,
					// )
					cancel(errMsg.Error)
					return completedOutput, errWrite
				}
			}
			if errMsg != nil {
				cancel(errMsg.Error)
			} else {
				cancel(nil)
			}
			return completedOutput, nil
		case chunk, ok := <-data:
			if !ok {
				if !completed {
					errMsg := &interfaces.ErrorMessage{
						StatusCode: http.StatusRequestTimeout,
						Error:      fmt.Errorf("stream closed before response.completed"),
					}
					h.LoggingAPIResponseError(context.WithValue(context.Background(), "gin", c), errMsg)
					markAPIResponseTimestamp(c)
					errorPayload, errWrite := writeResponsesWebsocketError(conn, wsTimelineLog, errMsg)
					log.Infof(
						"responses websocket: downstream_out id=%s type=%d event=%s payload=%s",
						sessionID,
						websocket.TextMessage,
						websocketPayloadEventType(errorPayload),
						websocketPayloadPreview(errorPayload),
					)
					if errWrite != nil {
						log.Warnf(
							"responses websocket: downstream_out write failed id=%s event=%s error=%v",
							sessionID,
							websocketPayloadEventType(errorPayload),
							errWrite,
						)
						cancel(errMsg.Error)
						return completedOutput, errWrite
					}
					cancel(errMsg.Error)
					return completedOutput, nil
				}
				cancel(nil)
				return completedOutput, nil
			}

			payloads := websocketJSONPayloadsFromChunk(chunk)
			for i := range payloads {
				filteredPayload := payloads[i]
				if noticeFilter != nil {
					filteredPayload = noticeFilter.FilterPayload(filteredPayload)
				}
				if len(filteredPayload) == 0 {
					continue
				}
				recordResponsesWebsocketToolCallsFromPayload(downstreamSessionKey, filteredPayload)
				eventType := gjson.GetBytes(filteredPayload, "type").String()
				if eventType == wsEventTypeCompleted {
					completed = true
					completedOutput = responseCompletedOutputFromPayload(filteredPayload)
				}
				markAPIResponseTimestamp(c)
				// log.Infof(
				// 	"responses websocket: downstream_out id=%s type=%d event=%s payload=%s",
				// 	sessionID,
				// 	websocket.TextMessage,
				// 	websocketPayloadEventType(payloads[i]),
				// 	websocketPayloadPreview(payloads[i]),
				// )
				if errWrite := writeResponsesWebsocketPayload(conn, wsTimelineLog, filteredPayload, time.Now()); errWrite != nil {
					log.Warnf(
						"responses websocket: downstream_out write failed id=%s event=%s error=%v",
						sessionID,
						websocketPayloadEventType(filteredPayload),
						errWrite,
					)
					cancel(errWrite)
					return completedOutput, errWrite
				}
			}
		}
	}
}

func responseCompletedOutputFromPayload(payload []byte) []byte {
	output := gjson.GetBytes(payload, "response.output")
	if output.Exists() && output.IsArray() {
		return bytes.Clone([]byte(output.Raw))
	}
	return []byte("[]")
}

func websocketJSONPayloadsFromChunk(chunk []byte) [][]byte {
	payloads := make([][]byte, 0, 2)
	lines := bytes.Split(chunk, []byte("\n"))
	for i := range lines {
		line := bytes.TrimSpace(lines[i])
		if len(line) == 0 || bytes.HasPrefix(line, []byte("event:")) {
			continue
		}
		if bytes.HasPrefix(line, []byte("data:")) {
			line = bytes.TrimSpace(line[len("data:"):])
		}
		if len(line) == 0 || bytes.Equal(line, []byte(wsDoneMarker)) {
			continue
		}
		if json.Valid(line) {
			payloads = append(payloads, bytes.Clone(line))
		}
	}

	if len(payloads) > 0 {
		return payloads
	}

	trimmed := bytes.TrimSpace(chunk)
	if bytes.HasPrefix(trimmed, []byte("data:")) {
		trimmed = bytes.TrimSpace(trimmed[len("data:"):])
	}
	if len(trimmed) > 0 && !bytes.Equal(trimmed, []byte(wsDoneMarker)) && json.Valid(trimmed) {
		payloads = append(payloads, bytes.Clone(trimmed))
	}
	return payloads
}

func writeResponsesWebsocketError(conn *websocket.Conn, wsTimelineLog *websocketTimelineBuilder, errMsg *interfaces.ErrorMessage) ([]byte, error) {
	status := http.StatusInternalServerError
	errText := http.StatusText(status)
	if errMsg != nil {
		if errMsg.StatusCode > 0 {
			status = errMsg.StatusCode
			errText = http.StatusText(status)
		}
		if errMsg.Error != nil && strings.TrimSpace(errMsg.Error.Error()) != "" {
			errText = errMsg.Error.Error()
		}
	}

	body := handlers.BuildErrorResponseBody(status, errText)
	payload := []byte(`{}`)
	var errSet error
	payload, errSet = sjson.SetBytes(payload, "type", wsEventTypeError)
	if errSet != nil {
		return nil, errSet
	}
	payload, errSet = sjson.SetBytes(payload, "status", status)
	if errSet != nil {
		return nil, errSet
	}

	if errMsg != nil && errMsg.Addon != nil {
		headers := []byte(`{}`)
		hasHeaders := false
		for key, values := range errMsg.Addon {
			if len(values) == 0 {
				continue
			}
			headerPath := strings.ReplaceAll(strings.ReplaceAll(key, `\\`, `\\\\`), ".", `\\.`)
			headers, errSet = sjson.SetBytes(headers, headerPath, values[0])
			if errSet != nil {
				return nil, errSet
			}
			hasHeaders = true
		}
		if hasHeaders {
			payload, errSet = sjson.SetRawBytes(payload, "headers", headers)
			if errSet != nil {
				return nil, errSet
			}
		}
	}

	if len(body) > 0 && json.Valid(body) {
		errorNode := gjson.GetBytes(body, "error")
		if errorNode.Exists() {
			payload, errSet = sjson.SetRawBytes(payload, "error", []byte(errorNode.Raw))
		} else {
			payload, errSet = sjson.SetRawBytes(payload, "error", body)
		}
		if errSet != nil {
			return nil, errSet
		}
	}

	if !gjson.GetBytes(payload, "error").Exists() {
		payload, errSet = sjson.SetBytes(payload, "error.type", "server_error")
		if errSet != nil {
			return nil, errSet
		}
		payload, errSet = sjson.SetBytes(payload, "error.message", errText)
		if errSet != nil {
			return nil, errSet
		}
	}

	return payload, writeResponsesWebsocketPayload(conn, wsTimelineLog, payload, time.Now())
}

func appendWebsocketEvent(builder *websocketTimelineBuilder, eventType string, payload []byte) {
	if builder == nil {
		return
	}
	trimmedPayload := bytes.TrimSpace(payload)
	if len(trimmedPayload) == 0 {
		return
	}
	if websocketTimelineTruncated(builder) {
		return
	}
	if builder.Len() > 0 {
		appendWebsocketTimelineText(builder, "\n")
	}
	appendWebsocketTimelineText(builder, "websocket.")
	appendWebsocketTimelineText(builder, eventType)
	appendWebsocketTimelineText(builder, "\n")
	appendWebsocketTimelineBytes(builder, trimmedPayload)
	appendWebsocketTimelineText(builder, "\n")
}

func websocketPayloadEventType(payload []byte) string {
	eventType := strings.TrimSpace(gjson.GetBytes(payload, "type").String())
	if eventType == "" {
		return "-"
	}
	return eventType
}

func websocketPayloadPreview(payload []byte) string {
	trimmedPayload := bytes.TrimSpace(payload)
	if len(trimmedPayload) == 0 {
		return "<empty>"
	}
	previewText := strings.ReplaceAll(string(trimmedPayload), "\n", "\\n")
	previewText = strings.ReplaceAll(previewText, "\r", "\\r")
	return previewText
}

func setWebsocketTimelineBody(c *gin.Context, body string) {
	setWebsocketBody(c, wsTimelineBodyKey, body)
}

func setWebsocketBody(c *gin.Context, key string, body string) {
	if c == nil {
		return
	}
	trimmedBody := strings.TrimSpace(body)
	if trimmedBody == "" {
		return
	}
	c.Set(key, []byte(trimmedBody))
}

func writeResponsesWebsocketPayload(conn *websocket.Conn, wsTimelineLog *websocketTimelineBuilder, payload []byte, timestamp time.Time) error {
	appendWebsocketTimelineEvent(wsTimelineLog, "response", payload, timestamp)
	return conn.WriteMessage(websocket.TextMessage, payload)
}

func appendWebsocketTimelineDisconnect(builder *websocketTimelineBuilder, err error, timestamp time.Time) {
	if err == nil {
		return
	}
	appendWebsocketTimelineEvent(builder, "disconnect", []byte(err.Error()), timestamp)
}

func appendWebsocketTimelineEvent(builder *websocketTimelineBuilder, eventType string, payload []byte, timestamp time.Time) {
	if builder == nil {
		return
	}
	trimmedPayload := bytes.TrimSpace(payload)
	if len(trimmedPayload) == 0 {
		return
	}
	if websocketTimelineTruncated(builder) {
		return
	}
	if builder.Len() > 0 {
		appendWebsocketTimelineText(builder, "\n")
	}
	appendWebsocketTimelineText(builder, "Timestamp: ")
	appendWebsocketTimelineText(builder, timestamp.Format(time.RFC3339Nano))
	appendWebsocketTimelineText(builder, "\n")
	appendWebsocketTimelineText(builder, "Event: websocket.")
	appendWebsocketTimelineText(builder, eventType)
	appendWebsocketTimelineText(builder, "\n")
	appendWebsocketTimelineBytes(builder, trimmedPayload)
	appendWebsocketTimelineText(builder, "\n")
}

func appendWebsocketTimelineText(builder *websocketTimelineBuilder, text string) {
	if builder == nil || text == "" || websocketTimelineTruncated(builder) {
		return
	}
	remaining := builder.maxBytes - builder.Len()
	if remaining <= 0 {
		builder.WriteString(responsesWebsocketTimelineTruncatedMarker)
		return
	}
	if len(text) <= remaining {
		builder.WriteString(text)
		return
	}
	builder.WriteString(text[:remaining])
	builder.WriteString(responsesWebsocketTimelineTruncatedMarker)
}

func appendWebsocketTimelineBytes(builder *websocketTimelineBuilder, data []byte) {
	if builder == nil || len(data) == 0 || websocketTimelineTruncated(builder) {
		return
	}
	remaining := builder.maxBytes - builder.Len()
	if remaining <= 0 {
		builder.WriteString(responsesWebsocketTimelineTruncatedMarker)
		return
	}
	if len(data) <= remaining {
		builder.Write(data)
		return
	}
	builder.Write(data[:remaining])
	builder.WriteString(responsesWebsocketTimelineTruncatedMarker)
}

func websocketTimelineTruncated(builder *websocketTimelineBuilder) bool {
	if builder == nil {
		return false
	}
	return builder.Len() > builder.maxBytes
}

func markAPIResponseTimestamp(c *gin.Context) {
	if c == nil {
		return
	}
	if _, exists := c.Get("API_RESPONSE_TIMESTAMP"); exists {
		return
	}
	c.Set("API_RESPONSE_TIMESTAMP", time.Now())
}
