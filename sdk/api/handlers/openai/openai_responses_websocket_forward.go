package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/clienterror"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type responsesWebsocketForwardOptions struct {
	toolCacheTurn *responsesWebsocketToolCacheTurn
	suppressError func(*interfaces.ErrorMessage) bool
}

func (h *OpenAIResponsesAPIHandler) forwardResponsesWebsocket(
	c *gin.Context,
	writer *responsesWebsocketWriter,
	cancel handlers.APIHandlerCancelFunc,
	data <-chan []byte,
	errs <-chan *interfaces.ErrorMessage,
	wsTimelineLog websocketTimelineAppender,
	sessionID string,
	options ...responsesWebsocketForwardOptions,
) ([]byte, string, []string, *interfaces.ErrorMessage, error) {
	var opts responsesWebsocketForwardOptions
	if len(options) > 0 {
		opts = options[0]
	}
	toolCacheTurn := opts.toolCacheTurn
	completed := false
	completedOutput := []byte("[]")
	completedResponseID := ""
	outputItemsByIndex := make(map[int64][]byte)
	var outputItemsFallback [][]byte
	pendingToolCallIDs := make(map[string]struct{})
	downstreamSessionKey := ""
	if c != nil && c.Request != nil {
		downstreamSessionKey = websocketDownstreamSessionKey(c.Request)
	}

	for {
		select {
		case <-c.Request.Context().Done():
			cancel(c.Request.Context().Err())
			return completedOutput, completedResponseID, sortedStringSet(pendingToolCallIDs), nil, c.Request.Context().Err()
		case errMsg, ok := <-errs:
			if !ok {
				errs = nil
				continue
			}
			if errMsg == nil {
				cancel(nil)
				return completedOutput, completedResponseID, sortedStringSet(pendingToolCallIDs), nil, nil
			}

			h.LoggingAPIResponseError(context.WithValue(context.Background(), "gin", c), errMsg)
			if opts.suppressError != nil && opts.suppressError(errMsg) {
				cancel(errMsg.Error)
				return completedOutput, completedResponseID, sortedStringSet(pendingToolCallIDs), errMsg, nil
			}
			markAPIResponseTimestamp(c)
			if matched, errClose := writer.closeForUpstreamError(errMsg.Error); matched {
				cancel(errMsg.Error)
				if errClose != nil {
					return completedOutput, completedResponseID, sortedStringSet(pendingToolCallIDs), errMsg, errClose
				}
				return completedOutput, completedResponseID, sortedStringSet(pendingToolCallIDs), errMsg, websocket.ErrCloseSent
			}

			errorPayload, wrote, errTerminate := writeResponsesWebsocketTerminalError(writer, wsTimelineLog, errMsg, nil)
			if wrote {
				log.Infof(
					"responses websocket: downstream_out id=%s type=%d event=%s payload=%s",
					sessionID,
					websocket.TextMessage,
					websocketPayloadEventType(errorPayload),
					websocketPayloadPreview(errorPayload),
				)
			}
			cancel(errMsg.Error)
			return completedOutput, completedResponseID, sortedStringSet(pendingToolCallIDs), errMsg, errTerminate
		case chunk, ok := <-data:
			if !ok {
				if !completed {
					errMsg := &interfaces.ErrorMessage{
						StatusCode: http.StatusRequestTimeout,
						Error:      fmt.Errorf("stream closed before response.completed"),
					}
					h.LoggingAPIResponseError(context.WithValue(context.Background(), "gin", c), errMsg)
					markAPIResponseTimestamp(c)
					_, errClose := writer.closeWithoutError()
					cancel(errMsg.Error)
					if errClose != nil {
						return completedOutput, completedResponseID, sortedStringSet(pendingToolCallIDs), errMsg, errClose
					}
					return completedOutput, completedResponseID, sortedStringSet(pendingToolCallIDs), errMsg, websocket.ErrCloseSent
				}
				cancel(nil)
				return completedOutput, completedResponseID, sortedStringSet(pendingToolCallIDs), nil, nil
			}

			payloads := websocketJSONPayloadsFromChunk(chunk)
			for i := range payloads {
				collectResponsesWebsocketOutputItem(payloads[i], outputItemsByIndex, &outputItemsFallback)
				eventType := gjson.GetBytes(payloads[i], "type").String()
				if isResponsesWebsocketCompletionEvent(eventType) {
					payloads[i] = restoreResponsesWebsocketCompletionOutput(payloads[i], outputItemsByIndex, outputItemsFallback)
				}
				if toolCacheTurn != nil {
					toolCacheTurn.recordResponse(payloads[i])
				} else {
					recordResponsesWebsocketToolCallsFromPayload(downstreamSessionKey, payloads[i])
				}
				recordPendingToolCallIDsFromPayload(pendingToolCallIDs, payloads[i])
				var payloadErrMsg *interfaces.ErrorMessage
				if eventType == wsEventTypeError {
					payloadErrMsg = responsesWebsocketErrorMessageFromPayload(payloads[i])
					if h != nil {
						h.LoggingAPIResponseError(context.WithValue(context.Background(), "gin", c), payloadErrMsg)
					}
					if opts.suppressError != nil && opts.suppressError(payloadErrMsg) {
						cancel(payloadErrMsg.Error)
						return completedOutput, completedResponseID, sortedStringSet(pendingToolCallIDs), payloadErrMsg, nil
					}
				} else if isResponsesWebsocketCompletionEvent(eventType) {
					completed = true
					completedOutput = responseCompletedOutputFromPayload(payloads[i], outputItemsByIndex, outputItemsFallback)
					completedResponseID = responseCompletedIDFromPayload(payloads[i])
				}
				markAPIResponseTimestamp(c)
				if payloadErrMsg != nil {
					if matched, errClose := writer.closeForUpstreamError(payloadErrMsg.Error); matched {
						cancel(payloadErrMsg.Error)
						if errClose != nil {
							return completedOutput, completedResponseID, sortedStringSet(pendingToolCallIDs), payloadErrMsg, errClose
						}
						return completedOutput, completedResponseID, sortedStringSet(pendingToolCallIDs), payloadErrMsg, websocket.ErrCloseSent
					}
					errorPayload, wrote, errTerminate := writeResponsesWebsocketTerminalError(writer, wsTimelineLog, payloadErrMsg, payloads[i])
					if wrote {
						log.Infof(
							"responses websocket: downstream_out id=%s type=%d event=%s payload=%s",
							sessionID,
							websocket.TextMessage,
							websocketPayloadEventType(errorPayload),
							websocketPayloadPreview(errorPayload),
						)
					}
					cancel(payloadErrMsg.Error)
					return completedOutput, completedResponseID, sortedStringSet(pendingToolCallIDs), payloadErrMsg, errTerminate
				}
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
					cancel(errWrite)
					return completedOutput, completedResponseID, sortedStringSet(pendingToolCallIDs), nil, errWrite
				}
			}
		}
	}
}

func responsesWebsocketErrorStatus(errMsg *interfaces.ErrorMessage) int {
	if errMsg == nil {
		return 0
	}
	status := errMsg.StatusCode
	if status <= 0 && errMsg.Error != nil {
		if se, ok := errMsg.Error.(interface{ StatusCode() int }); ok && se != nil {
			status = se.StatusCode()
		}
	}
	return status
}

// shouldExposeResponsesUpstreamError reports whether a terminal upstream error
// must reach the downstream client.
//
// Only request-shape failures are exposed: the client can act on them and no
// credential rotation or retry can make the request succeed. Credential, quota
// and transport failures stay silent so the client simply reconnects and retries;
// a fresh connection carries no server-side transcript, so reconnecting already
// implies a full context resend.
func shouldExposeResponsesUpstreamError(errMsg *interfaces.ErrorMessage) bool {
	if errMsg == nil {
		return false
	}
	return clienterror.IsRequestFault(responsesWebsocketErrorStatus(errMsg), errMsg.Error)
}

func writeResponsesWebsocketTerminalError(
	writer *responsesWebsocketWriter,
	wsTimelineLog websocketTimelineAppender,
	errMsg *interfaces.ErrorMessage,
	payload []byte,
) ([]byte, bool, error) {
	if !shouldExposeResponsesUpstreamError(errMsg) {
		// Keep the upstream reason in the request-log timeline even though the client
		// only observes a closed connection, otherwise silent failures are
		// undiagnosable after the fact.
		if wsTimelineLog != nil && errMsg != nil {
			appendWebsocketTimelineDisconnect(wsTimelineLog, errMsg.Error, time.Now())
		}
		_, errClose := writer.closeWithoutError()
		if errClose != nil {
			return nil, false, errClose
		}
		return nil, false, websocket.ErrCloseSent
	}

	if len(payload) == 0 {
		var errBuild error
		payload, errBuild = buildResponsesWebsocketErrorPayload(errMsg)
		if errBuild != nil {
			_, _ = writer.closeWithoutError()
			return nil, false, errBuild
		}
	}

	wrote, errClose := writer.closeWithPayload(payload)
	if wrote && wsTimelineLog != nil {
		wsTimelineLog.Append("response", payload, time.Now())
	}
	if errClose != nil {
		return payload, wrote, errClose
	}
	return payload, wrote, websocket.ErrCloseSent
}

func shouldReplayResponsesWebsocketPinnedAuthFailure(errMsg *interfaces.ErrorMessage) bool {
	switch responsesWebsocketErrorStatus(errMsg) {
	case http.StatusUnauthorized, http.StatusTooManyRequests:
		return true
	default:
		return false
	}
}

func shouldReleaseResponsesWebsocketPinnedAuth(errMsg *interfaces.ErrorMessage) bool {
	if errMsg == nil {
		return false
	}
	switch responsesWebsocketErrorStatus(errMsg) {
	case http.StatusUnauthorized,
		http.StatusPaymentRequired,
		http.StatusForbidden,
		http.StatusTooManyRequests,
		http.StatusRequestTimeout,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout:
		return true
	default:
	}
	if errMsg.Error != nil {
		msg := strings.ToLower(errMsg.Error.Error())
		switch {
		case strings.Contains(msg, "stream closed before response.completed"),
			strings.Contains(msg, "previous_response_not_found"),
			strings.Contains(msg, "ws_failed"),
			strings.Contains(msg, "upstream stream closed before first payload"),
			strings.Contains(msg, "empty_stream"):
			return true
		}
	}
	return false
}

func collectResponsesWebsocketOutputItem(payload []byte, outputItemsByIndex map[int64][]byte, outputItemsFallback *[][]byte) {
	if gjson.GetBytes(payload, "type").String() != "response.output_item.done" {
		return
	}
	item := gjson.GetBytes(payload, "item")
	if !item.Exists() || !item.IsObject() {
		return
	}
	outputIndex := gjson.GetBytes(payload, "output_index")
	if outputIndex.Exists() {
		outputItemsByIndex[outputIndex.Int()] = bytes.Clone([]byte(item.Raw))
		return
	}
	*outputItemsFallback = append(*outputItemsFallback, bytes.Clone([]byte(item.Raw)))
}

func restoreResponsesWebsocketCompletionOutput(payload []byte, outputItemsByIndex map[int64][]byte, outputItemsFallback [][]byte) []byte {
	output := gjson.GetBytes(payload, "response.output")
	if output.Exists() && output.IsArray() && len(output.Array()) > 0 {
		reconciledOutput, changed := reconcileResponsesWebsocketCompletionToolCalls(output, outputItemsByIndex, outputItemsFallback)
		if !changed {
			return payload
		}
		restored, errSet := sjson.SetRawBytes(payload, "response.output", reconciledOutput)
		if errSet != nil {
			return payload
		}
		return restored
	}
	if len(outputItemsByIndex) == 0 && len(outputItemsFallback) == 0 {
		return payload
	}

	restored, errSet := sjson.SetRawBytes(payload, "response.output", responseCompletedOutputFromPayload(payload, outputItemsByIndex, outputItemsFallback))
	if errSet != nil {
		return payload
	}
	return restored
}

func reconcileResponsesWebsocketCompletionToolCalls(output gjson.Result, outputItemsByIndex map[int64][]byte, outputItemsFallback [][]byte) ([]byte, bool) {
	collectedToolCalls := make(map[string]json.RawMessage)
	recordCollectedToolCall := func(raw []byte) {
		item := gjson.ParseBytes(raw)
		if !isCompleteResponsesWebsocketToolCall(item) {
			return
		}
		callID := strings.TrimSpace(item.Get("call_id").String())
		collectedToolCalls[callID] = append(json.RawMessage(nil), raw...)
	}

	indexes := make([]int64, 0, len(outputItemsByIndex))
	for index := range outputItemsByIndex {
		indexes = append(indexes, index)
	}
	sort.Slice(indexes, func(i, j int) bool {
		return indexes[i] < indexes[j]
	})
	for _, index := range indexes {
		recordCollectedToolCall(outputItemsByIndex[index])
	}
	for _, item := range outputItemsFallback {
		recordCollectedToolCall(item)
	}
	if len(collectedToolCalls) == 0 {
		return nil, false
	}

	items := output.Array()
	reconciled := make([]json.RawMessage, 0, len(items))
	changed := false
	for _, item := range items {
		raw := json.RawMessage(item.Raw)
		if isResponsesToolCallType(item.Get("type").String()) {
			callID := strings.TrimSpace(item.Get("call_id").String())
			if collected, ok := collectedToolCalls[callID]; ok && !bytes.Equal(raw, collected) {
				raw = collected
				changed = true
			}
		}
		reconciled = append(reconciled, raw)
	}
	if !changed {
		return nil, false
	}

	marshaledOutput, errMarshal := json.Marshal(reconciled)
	if errMarshal != nil {
		return nil, false
	}
	return marshaledOutput, true
}

func isCompleteResponsesWebsocketToolCall(item gjson.Result) bool {
	if !item.Exists() || !item.IsObject() {
		return false
	}
	callID := item.Get("call_id")
	name := item.Get("name")
	if callID.Type != gjson.String || strings.TrimSpace(callID.String()) == "" || name.Type != gjson.String || strings.TrimSpace(name.String()) == "" {
		return false
	}

	switch strings.TrimSpace(item.Get("type").String()) {
	case "function_call":
		arguments := item.Get("arguments")
		return arguments.Exists() && arguments.Type == gjson.String
	case "custom_tool_call":
		input := item.Get("input")
		return input.Exists() && input.Type == gjson.String
	default:
		return false
	}
}

func responseCompletedOutputFromPayload(payload []byte, outputItemsByIndex map[int64][]byte, outputItemsFallback [][]byte) []byte {
	output := gjson.GetBytes(payload, "response.output")
	if output.Exists() && output.IsArray() && len(output.Array()) > 0 {
		return bytes.Clone([]byte(output.Raw))
	}
	if len(outputItemsByIndex) == 0 && len(outputItemsFallback) == 0 {
		return []byte("[]")
	}

	indexes := make([]int64, 0, len(outputItemsByIndex))
	for index := range outputItemsByIndex {
		indexes = append(indexes, index)
	}
	sort.Slice(indexes, func(i, j int) bool {
		return indexes[i] < indexes[j]
	})

	items := make([]json.RawMessage, 0, len(outputItemsByIndex)+len(outputItemsFallback))
	appendCollectedItem := func(raw []byte) {
		item := gjson.ParseBytes(raw)
		if isResponsesToolCallType(item.Get("type").String()) && !isCompleteResponsesWebsocketToolCall(item) {
			return
		}
		items = append(items, append(json.RawMessage(nil), raw...))
	}
	for _, index := range indexes {
		appendCollectedItem(outputItemsByIndex[index])
	}
	for _, item := range outputItemsFallback {
		appendCollectedItem(item)
	}

	marshaledOutput, errMarshal := json.Marshal(items)
	if errMarshal != nil {
		return []byte("[]")
	}
	return marshaledOutput
}

func responseCompletedIDFromPayload(payload []byte) string {
	return strings.TrimSpace(gjson.GetBytes(payload, "response.id").String())
}

func recordPendingToolCallIDsFromPayload(pending map[string]struct{}, payload []byte) {
	if pending == nil || len(payload) == 0 {
		return
	}
	updatePendingToolCallIDsFromItem(pending, gjson.GetBytes(payload, "item"))
	output := gjson.GetBytes(payload, "response.output")
	if output.IsArray() {
		for _, item := range output.Array() {
			updatePendingToolCallIDsFromItem(pending, item)
		}
	}
}

func updatePendingToolCallIDsFromItem(pending map[string]struct{}, item gjson.Result) {
	if pending == nil || !item.Exists() {
		return
	}
	switch strings.TrimSpace(item.Get("type").String()) {
	case "function_call", "custom_tool_call":
		if !isCompleteResponsesWebsocketToolCall(item) {
			return
		}
		callID := strings.TrimSpace(item.Get("call_id").String())
		pending[callID] = struct{}{}
	case "function_call_output", "custom_tool_call_output":
		callID := strings.TrimSpace(item.Get("call_id").String())
		if callID != "" {
			delete(pending, callID)
		}
	}
}

func sortedStringSet(values map[string]struct{}) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	for value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			out = append(out, value)
		}
	}
	sort.Strings(out)
	return out
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

func buildResponsesWebsocketErrorPayload(errMsg *interfaces.ErrorMessage) ([]byte, error) {
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

	return payload, nil
}

func writeResponsesWebsocketError(writer *responsesWebsocketWriter, wsTimelineLog websocketTimelineAppender, errMsg *interfaces.ErrorMessage) ([]byte, error) {
	payload, errBuild := buildResponsesWebsocketErrorPayload(errMsg)
	if errBuild != nil {
		return nil, errBuild
	}
	return payload, writeResponsesWebsocketPayload(writer, wsTimelineLog, payload, time.Now())
}
