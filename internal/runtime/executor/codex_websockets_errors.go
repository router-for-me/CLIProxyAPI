package executor

import (
	"context"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	log "github.com/sirupsen/logrus"
)

type statusErrWithHeaders struct {
	statusErr
	headers http.Header
}

func (e statusErrWithHeaders) Headers() http.Header {
	if e.headers == nil {
		return nil
	}
	return e.headers.Clone()
}

func parseCodexWebsocketError(payload []byte) (error, bool) {
	return parseCodexWebsocketErrorWithCooling(payload, false)
}

func codexWebsocketErrorStatus(payload []byte) int {
	for _, path := range []string{"status", "status_code"} {
		if status := int(ojson.GetBytesNoCopy(payload, path).Int()); status > 0 {
			return status
		}
	}
	for _, path := range []string{
		"body.status", "body.status_code",
		"error.status", "error.status_code",
		"body.error.status", "body.error.status_code",
		"response.error.status", "response.error.status_code",
	} {
		if status := int(ojson.GetBytesNoCopy(payload, path).Int()); status >= 400 && status <= 599 {
			return status
		}
	}
	return 0
}

func codexWebsocketErrorStatusWithHeaders(payload []byte, headers http.Header) int {
	if status := codexWebsocketErrorStatus(payload); status > 0 {
		return status
	}
	if len(headers) == 0 {
		return 0
	}
	return inferCodexWebsocketErrorStatus(payload)
}

func inferCodexWebsocketErrorStatus(payload []byte) int {
	if isCodexWebsocketConnectionLimitError(payload) {
		return http.StatusTooManyRequests
	}
	body, ok := codexTerminalFailureBody(payload)
	if !ok {
		return 0
	}
	if isCodexUsageLimitError(body) || isCodexBillingQuotaError(body) || isCodexTransientCapacityError(body) {
		return http.StatusTooManyRequests
	}
	if codexTerminalErrorIsContextLength(body) {
		return http.StatusBadRequest
	}
	if code, _, classified := codexStatusErrorClassification(http.StatusBadRequest, body); classified {
		if code == "auth_unavailable" {
			return http.StatusUnauthorized
		}
		return http.StatusBadRequest
	}
	errorType := strings.ToLower(strings.TrimSpace(ojson.GetBytesNoCopy(body, "error.type").String()))
	errorCode := strings.ToLower(strings.TrimSpace(ojson.GetBytesNoCopy(body, "error.code").String()))
	if errorType == "service_unavailable_error" || errorCode == "server_is_overloaded" {
		return http.StatusServiceUnavailable
	}
	return codexTerminalFailureStatus(body)
}

func parseCodexWebsocketErrorWithCooling(payload []byte, modelLevelCooling bool) (error, bool) {
	if len(payload) == 0 {
		return nil, false
	}
	if helps.CodexResponseEventType(payload) != "error" {
		return nil, false
	}
	headers := parseCodexWebsocketErrorHeaders(payload)
	status := codexWebsocketErrorStatusWithHeaders(payload, headers)
	if status <= 0 {
		return nil, false
	}

	out := buildCodexWebsocketErrorPayload(payload, status)
	isUsageLimit := isCodexUsageLimitError(out)
	isBillingQuota := isCodexBillingQuotaError(out)
	transientCapacity := !isUsageLimit && !isBillingQuota && isCodexTransientCapacityError(out)
	errCode := status
	if isBillingQuota {
		errCode = http.StatusTooManyRequests
	}
	statusError := statusErr{
		code:               errCode,
		msg:                string(out),
		credentialScoped:   isBillingQuota || (isUsageLimit && !modelLevelCooling),
		transientRateLimit: transientCapacity && errCode == http.StatusTooManyRequests,
	}
	// Hint precedence: the quota body reset wins, then the connection-limit
	// immediate-retry contract, then the generic Retry-After header.
	if retryAfter := parseCodexRetryAfter(errCode, out, time.Now()); retryAfter != nil {
		statusError.retryAfter = retryAfter
	} else if isCodexWebsocketConnectionLimitError(payload) {
		retryAfter := time.Duration(0)
		statusError.retryAfter = &retryAfter
	} else if shouldParseCodexRetryAfterHeader(errCode) {
		if retryAfter := parseRetryAfterHeader(headers, time.Now()); retryAfter != nil {
			statusError.retryAfter = retryAfter
		}
	}
	return statusErrWithHeaders{
		statusErr: statusError,
		headers:   headers,
	}, true
}

func clearCodexReasoningReplayOnWebsocketError(ctx context.Context, scope codexReasoningReplayScope, payload []byte) error {
	headers := parseCodexWebsocketErrorHeaders(payload)
	status := codexWebsocketErrorStatusWithHeaders(payload, headers)
	if status <= 0 {
		return nil
	}
	return clearCodexReasoningReplayOnInvalidSignature(ctx, scope, status, buildCodexWebsocketErrorPayload(payload, status))
}

func buildCodexWebsocketErrorPayload(payload []byte, status int) []byte {
	doc := ojson.NewDocString(`{"type":"error"}`)
	_ = doc.Set("status", status)
	if seq := ojson.GetBytesNoCopy(payload, "sequence_number"); seq.Exists() {
		_ = doc.Set("sequence_number", seq.Int())
	}

	fillBlankErrorMessage := func() {
		if node := doc.Get("error"); !node.IsObject() {
			scalar := ""
			if node.Type != ojson.Null && strings.TrimSpace(node.String()) != "" {
				scalar = node.String()
			}
			_ = doc.SetRawBytes("error", []byte(`{}`))
			if scalar != "" {
				_ = doc.Set("error.message", scalar)
				return
			}
		}
		if strings.TrimSpace(doc.Get("error.message").String()) != "" {
			return
		}
		if code := strings.TrimSpace(doc.Get("error.code").String()); code != "" {
			_ = doc.Set("error.message", codexPolicyFallbackMessage(code))
		} else if errType := strings.TrimSpace(doc.Get("error.type").String()); errType != "" {
			_ = doc.Set("error.message", errType)
		} else {
			_ = doc.Set("error.message", http.StatusText(status))
		}
	}

	if bodyNode := ojson.GetBytesNoCopy(payload, "body"); bodyNode.Exists() {
		_ = doc.SetRawBytes("body", []byte(bodyNode.Raw))
		if bodyErrorNode := bodyNode.Get("error"); bodyErrorNode.Exists() && bodyErrorNode.Type != ojson.Null {
			_ = doc.SetRawBytes("error", []byte(bodyErrorNode.Raw))
			fillBlankErrorMessage()
			return doc.Bytes()
		}
	}

	for _, path := range []string{"error", "response.error"} {
		if errNode := ojson.GetBytesNoCopy(payload, path); errNode.Exists() && errNode.Type != ojson.Null {
			_ = doc.SetRawBytes("error", []byte(errNode.Raw))
			fillBlankErrorMessage()
			return doc.Bytes()
		}
	}
	if topLevel := codexTerminalTopLevelErrorBody(payload); topLevel != nil {
		_ = doc.SetRawBytes("error", []byte(topLevel.Get("error").Raw))
		fillBlankErrorMessage()
		return doc.Bytes()
	}

	_ = doc.Set("error.type", "server_error")
	_ = doc.Set("error.message", http.StatusText(status))
	return doc.Bytes()
}

func isCodexWebsocketConnectionLimitError(payload []byte) bool {
	if len(payload) == 0 {
		return false
	}
	for _, path := range []string{"error.code", "error.type", "body.error.code", "body.error.type", "code", "error"} {
		if strings.TrimSpace(ojson.GetBytesNoCopy(payload, path).String()) == "websocket_connection_limit_reached" {
			return true
		}
	}
	return false
}

func shouldParseCodexRetryAfterHeader(statusCode int) bool {
	switch statusCode {
	case http.StatusTooManyRequests,
		http.StatusRequestTimeout,
		http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout,
		520, 521, 522, 523, 524, 525, 526:
		return true
	default:
		return false
	}
}

func parseCodexWebsocketErrorHeaders(payload []byte) http.Header {
	if len(payload) == 0 {
		return nil
	}
	mapped := make(http.Header)
	seen := make(map[string]struct{})
	for _, path := range []string{"headers", "body.headers", "error.headers", "body.error.headers", "response.error.headers"} {
		headersNode := ojson.GetBytesNoCopy(payload, path)
		if !headersNode.Exists() || !headersNode.IsObject() {
			continue
		}
		headersNode.ForEach(func(key, value ojson.Result) bool {
			name := strings.TrimSpace(key.String())
			if name == "" {
				return true
			}
			canonical := http.CanonicalHeaderKey(name)
			if _, exists := seen[canonical]; exists {
				return true
			}
			// Mark the header as seen before validating its value so an explicit outer
			// blank or unsupported entry still shadows any nested fallback value.
			seen[canonical] = struct{}{}
			switch value.Type {
			case ojson.String:
				if v := strings.TrimSpace(value.String()); v != "" {
					mapped.Set(strings.Clone(name), strings.Clone(v))
				}
			case ojson.Number, ojson.True, ojson.False:
				if v := strings.TrimSpace(value.Raw); v != "" {
					mapped.Set(strings.Clone(name), strings.Clone(v))
				}
			default:
			}
			return true
		})
	}
	if len(mapped) == 0 {
		return nil
	}
	return mapped
}

func normalizeCodexWebsocketCompletion(payload []byte) []byte {
	if strings.TrimSpace(ojson.GetBytesNoCopy(payload, "type").String()) == "response.done" {
		updated, err := ojson.SetBytes(payload, "type", "response.completed")
		if err == nil && len(updated) > 0 {
			return updated
		}
	}
	return payload
}

func encodeCodexWebsocketAsSSE(payload []byte) []byte {
	if len(payload) == 0 {
		return nil
	}
	line := make([]byte, 0, len("data: ")+len(payload))
	line = append(line, []byte("data: ")...)
	line = append(line, payload...)
	return line
}

func websocketUpgradeRequestLog(info helps.UpstreamRequestLog) helps.UpstreamRequestLog {
	upgradeInfo := info
	upgradeInfo.URL = helps.WebsocketUpgradeRequestURL(info.URL)
	upgradeInfo.Method = http.MethodGet
	upgradeInfo.Body = nil
	upgradeInfo.Headers = info.Headers.Clone()
	if upgradeInfo.Headers == nil {
		upgradeInfo.Headers = make(http.Header)
	}
	if strings.TrimSpace(upgradeInfo.Headers.Get("Connection")) == "" {
		upgradeInfo.Headers.Set("Connection", "Upgrade")
	}
	if strings.TrimSpace(upgradeInfo.Headers.Get("Upgrade")) == "" {
		upgradeInfo.Headers.Set("Upgrade", "websocket")
	}
	return upgradeInfo
}

func recordAPIWebsocketHandshake(ctx context.Context, cfg *config.Config, resp *http.Response) {
	if resp == nil {
		return
	}
	helps.RecordAPIWebsocketHandshake(ctx, cfg, resp.StatusCode, resp.Header.Clone())
	closeHTTPResponseBody(resp, "codex websockets executor: close handshake response body error")
}

func websocketHandshakeBody(resp *http.Response) []byte {
	body, _ := websocketHandshakeBodyWithReadErr(resp)
	return body
}

func websocketHandshakeBodyWithReadErr(resp *http.Response) ([]byte, error) {
	if resp == nil || resp.Body == nil {
		return nil, nil
	}
	body, errRead := io.ReadAll(resp.Body)
	closeHTTPResponseBody(resp, "codex websockets executor: close handshake response body error")
	if len(body) == 0 {
		return nil, errRead
	}
	return body, errRead
}

func recordWebsocketUpgradeRejection(ctx context.Context, cfg *config.Config, wsReqLog helps.UpstreamRequestLog, resp *http.Response) []byte {
	bodyErr, errRead := websocketHandshakeBodyWithReadErr(resp)
	if resp != nil {
		helps.RecordAPIWebsocketUpgradeRejection(ctx, cfg, websocketUpgradeRequestLog(wsReqLog), resp.StatusCode, resp.Header.Clone(), bodyErr)
		if errRead != nil {
			helps.RecordAPIResponseError(ctx, cfg, errRead)
		}
	}
	return bodyErr
}

func recordWebsocketDialFailure(ctx context.Context, cfg *config.Config, wsReqLog helps.UpstreamRequestLog, resp *http.Response, errDial error) []byte {
	bodyErr := recordWebsocketUpgradeRejection(ctx, cfg, wsReqLog, resp)
	helps.RecordAPIWebsocketError(ctx, cfg, "dial", errDial)
	return bodyErr
}

func recordWebsocketDialRetryFailure(ctx context.Context, cfg *config.Config, wsReqLog helps.UpstreamRequestLog, resp *http.Response, errDial error) []byte {
	if resp != nil {
		bodyErr := recordWebsocketUpgradeRejection(ctx, cfg, wsReqLog, resp)
		helps.RecordAPIWebsocketError(ctx, cfg, "dial_retry", errDial)
		return bodyErr
	}
	if cfg != nil && !cfg.CommercialMode && !cfg.RequestLog {
		helps.RecordAPIRequest(ctx, cfg, websocketUpgradeRequestLog(wsReqLog))
	}
	helps.RecordAPIWebsocketError(ctx, cfg, "dial_retry", errDial)
	return nil
}

func closeHTTPResponseBody(resp *http.Response, logPrefix string) {
	if resp == nil || resp.Body == nil {
		return
	}
	if errClose := resp.Body.Close(); errClose != nil {
		log.Errorf("%s: %v", logPrefix, errClose)
	}
}
