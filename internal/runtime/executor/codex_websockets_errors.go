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

func parseCodexWebsocketErrorWithCooling(payload []byte, modelLevelCooling bool) (error, bool) {
	if len(payload) == 0 {
		return nil, false
	}
	if strings.TrimSpace(ojson.GetBytesNoCopy(payload, "type").String()) != "error" {
		return nil, false
	}
	status := int(ojson.GetBytesNoCopy(payload, "status").Int())
	if status == 0 {
		status = int(ojson.GetBytesNoCopy(payload, "status_code").Int())
	}
	if status <= 0 {
		return nil, false
	}

	out := buildCodexWebsocketErrorPayload(payload, status)
	headers := parseCodexWebsocketErrorHeaders(payload)
	isUsageLimit := isCodexUsageLimitError(out)
	transientCapacity := !isUsageLimit && isCodexTransientCapacityError(out)
	statusError := statusErr{
		code:               status,
		msg:                string(out),
		credentialScoped:   isUsageLimit && !modelLevelCooling,
		transientRateLimit: transientCapacity && status == http.StatusTooManyRequests,
	}
	// Hint precedence: the quota body reset wins, then the connection-limit
	// immediate-retry contract, then the generic Retry-After header.
	if retryAfter := parseCodexRetryAfter(status, out, time.Now()); retryAfter != nil {
		statusError.retryAfter = retryAfter
	} else if isCodexWebsocketConnectionLimitError(payload) {
		retryAfter := time.Duration(0)
		statusError.retryAfter = &retryAfter
	} else if status == http.StatusTooManyRequests {
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
	status := int(ojson.GetBytesNoCopy(payload, "status").Int())
	if status == 0 {
		status = int(ojson.GetBytesNoCopy(payload, "status_code").Int())
	}
	if status <= 0 {
		return nil
	}
	return clearCodexReasoningReplayOnInvalidSignature(ctx, scope, status, buildCodexWebsocketErrorPayload(payload, status))
}

func buildCodexWebsocketErrorPayload(payload []byte, status int) []byte {
	doc := ojson.NewDoc([]byte(`{}`))
	_ = doc.Set("status", status)

	if bodyNode := ojson.GetBytesNoCopy(payload, "body"); bodyNode.Exists() {
		_ = doc.SetRawBytes("body", []byte(bodyNode.Raw))
		if bodyErrorNode := bodyNode.Get("error"); bodyErrorNode.Exists() {
			_ = doc.SetRawBytes("error", []byte(bodyErrorNode.Raw))
			return doc.Bytes()
		}
	}

	if errNode := ojson.GetBytesNoCopy(payload, "error"); errNode.Exists() {
		_ = doc.SetRawBytes("error", []byte(errNode.Raw))
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

func parseCodexWebsocketErrorHeaders(payload []byte) http.Header {
	headersNode := ojson.GetBytesNoCopy(payload, "headers")
	if !headersNode.Exists() || !headersNode.IsObject() {
		return nil
	}
	mapped := make(http.Header)
	headersNode.ForEach(func(key, value ojson.Result) bool {
		name := strings.TrimSpace(key.String())
		if name == "" {
			return true
		}
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
	if resp == nil || resp.Body == nil {
		return nil
	}
	body, _ := io.ReadAll(resp.Body)
	closeHTTPResponseBody(resp, "codex websockets executor: close handshake response body error")
	if len(body) == 0 {
		return nil
	}
	return body
}

func closeHTTPResponseBody(resp *http.Response, logPrefix string) {
	if resp == nil || resp.Body == nil {
		return
	}
	if errClose := resp.Body.Close(); errClose != nil {
		log.Errorf("%s: %v", logPrefix, errClose)
	}
}
