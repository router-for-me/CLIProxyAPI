package openai

import (
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
)

func shouldRetryResponsesWebsocketAfterUpstreamStateLoss(errMsg *interfaces.ErrorMessage, rawPayload []byte, lastRequest []byte, attempted bool) bool {
	return shouldRetryResponsesWebsocketAfterPreviousResponseNotFound(errMsg, rawPayload, lastRequest, attempted) ||
		shouldRetryResponsesWebsocketAfterMissingToolOutput(errMsg, rawPayload, lastRequest, attempted) ||
		shouldRetryResponsesWebsocketAfterMissingUpstreamSession(errMsg, rawPayload, lastRequest, attempted)
}

func shouldRetryResponsesWebsocketAfterMissingToolOutput(errMsg *interfaces.ErrorMessage, rawPayload []byte, lastRequest []byte, attempted bool) bool {
	if attempted || len(lastRequest) == 0 || !responsesWebsocketRequestRequiresUpstreamContext(rawPayload) {
		return false
	}
	if errMsg == nil || errMsg.Error == nil {
		return false
	}
	status := errMsg.StatusCode
	if status <= 0 {
		if statusErr, ok := errMsg.Error.(interface{ StatusCode() int }); ok && statusErr != nil {
			status = statusErr.StatusCode()
		}
	}
	if status != http.StatusBadRequest {
		return false
	}
	errText := strings.ToLower(strings.TrimSpace(errMsg.Error.Error()))
	return strings.Contains(errText, "no tool output found for function call") ||
		strings.Contains(errText, "no tool output found for custom tool call")
}

func shouldRetryResponsesWebsocketAfterMissingUpstreamSession(errMsg *interfaces.ErrorMessage, rawPayload []byte, lastRequest []byte, attempted bool) bool {
	if attempted || len(lastRequest) == 0 || !responsesWebsocketRequestRequiresUpstreamContext(rawPayload) {
		return false
	}
	if errMsg == nil || errMsg.Error == nil {
		return false
	}
	errText := strings.ToLower(strings.TrimSpace(errMsg.Error.Error()))
	return strings.Contains(errText, "codex websockets executor: request requires existing websocket session")
}
