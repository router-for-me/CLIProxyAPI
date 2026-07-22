package helps

import (
	"context"
	"net/http"
	"strings"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
)

// CodexWebsocketShouldNotifyUpstreamDisconnect reports whether disconnect subscribers should receive the error.
func CodexWebsocketShouldNotifyUpstreamDisconnect(ctx context.Context, err error) bool {
	return !cliproxyexecutor.DownstreamWebsocket(ctx) ||
		!isCodexWebsocketReplayableRequestError(err)
}

func isCodexWebsocketReplayableRequestError(err error) bool {
	return isCodexWebsocketPreviousResponseNotFoundError(err) ||
		isCodexWebsocketMissingToolOutputError(err)
}

func isCodexWebsocketMissingToolOutputError(err error) bool {
	if err == nil {
		return false
	}
	if statusErr, ok := err.(interface{ StatusCode() int }); ok && statusErr != nil {
		status := statusErr.StatusCode()
		if status > 0 && status != http.StatusBadRequest {
			return false
		}
	}

	errText := strings.ToLower(strings.TrimSpace(err.Error()))
	return strings.Contains(errText, "no tool output found for function call") ||
		strings.Contains(errText, "no tool output found for custom tool call")
}

func isCodexWebsocketPreviousResponseNotFoundError(err error) bool {
	if err == nil {
		return false
	}
	if statusErr, ok := err.(interface{ StatusCode() int }); ok && statusErr != nil {
		status := statusErr.StatusCode()
		if status > 0 && status != http.StatusBadRequest {
			return false
		}
	}

	errText := strings.TrimSpace(err.Error())
	if errText == "" {
		return false
	}
	code := strings.TrimSpace(gjson.Get(errText, "error.code").String())
	if code == "" {
		code = strings.TrimSpace(gjson.Get(errText, "body.error.code").String())
	}
	if code == "" {
		code = strings.TrimSpace(gjson.Get(errText, "code").String())
	}
	if strings.EqualFold(code, "previous_response_not_found") {
		return true
	}

	lower := strings.ToLower(errText)
	return strings.Contains(lower, "previous_response_not_found") ||
		strings.Contains(lower, "previous_response_id") && strings.Contains(lower, "not found")
}
