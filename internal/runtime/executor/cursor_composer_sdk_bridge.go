package executor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
)

const defaultCursorSDKBridgeURL = "http://127.0.0.1:8792/sdk"

type cursorSDKBridgeOutput struct {
	Text    string `json:"text"`
	AgentID string `json:"agentID"`
	RunID   string `json:"runID"`
	Status  string `json:"status"`
}

func cursorComposerSDKBridgeURL(auth *cliproxyauth.Auth) string {
	if auth != nil && auth.Attributes != nil {
		if v := strings.TrimSpace(auth.Attributes["sdk_bridge_url"]); v != "" {
			return normalizeSDKBridgeURL(v)
		}
	}
	return normalizeSDKBridgeURL(defaultCursorSDKBridgeURL)
}

func normalizeSDKBridgeURL(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	trimmed = strings.TrimRight(trimmed, "/")
	if !strings.HasSuffix(trimmed, "/sdk") {
		trimmed += "/sdk"
	}
	return trimmed
}

func (e *CursorComposerExecutor) executeViaSDKBridge(ctx context.Context, auth *cliproxyauth.Auth, prepared cursorPreparedRequest, stream bool) (text string, err error) {
	apiKey, _, _, _, _ := cursorComposerCredentials(auth)
	if apiKey == "" {
		return "", statusErr{code: http.StatusUnauthorized, msg: "missing Cursor API key"}
	}
	bridgeURL := cursorComposerSDKBridgeURL(auth)
	if bridgeURL == "" {
		return "", statusErr{code: http.StatusInternalServerError, msg: "missing Cursor SDK bridge URL"}
	}
	payload := map[string]any{
		"apiKey":     apiKey,
		"model":      prepared.model,
		"prompt":     prepared.prompt,
		"requestId":  uuid.NewString(),
		"sessionKey": uuid.NewString(),
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	if stream {
		return e.executeViaSDKBridgeStream(ctx, auth, bridgeURL, body)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, bridgeURL, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	helps.RecordAPIRequest(ctx, e.cfg, helps.UpstreamRequestLog{URL: bridgeURL, Method: http.MethodPost, Headers: req.Header.Clone(), Body: body, Provider: e.Identifier()})
	resp, err := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0).Do(req)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return "", statusErr{code: http.StatusBadGateway, msg: fmt.Sprintf("Cursor SDK bridge unreachable at %s: %v", bridgeURL, err)}
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	helps.RecordAPIResponseMetadata(ctx, e.cfg, resp.StatusCode, resp.Header.Clone())
	helps.AppendAPIResponseChunk(ctx, e.cfg, raw)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", statusErr{code: cursorHTTPStatus(resp.StatusCode), msg: cursorSDKBridgeErrorMessage(raw, resp.StatusCode)}
	}
	var out cursorSDKBridgeOutput
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", statusErr{code: http.StatusBadGateway, msg: "Cursor SDK bridge returned invalid JSON"}
	}
	return strings.TrimSpace(out.Text), nil
}

func (e *CursorComposerExecutor) executeViaSDKBridgeStream(ctx context.Context, auth *cliproxyauth.Auth, bridgeURL string, body []byte) (string, error) {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", err
	}
	payload["streamEvents"] = true
	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, bridgeURL, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0).Do(req)
	if err != nil {
		return "", statusErr{code: http.StatusBadGateway, msg: fmt.Sprintf("Cursor SDK bridge unreachable at %s: %v", bridgeURL, err)}
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(resp.Body)
		return "", statusErr{code: cursorHTTPStatus(resp.StatusCode), msg: cursorSDKBridgeErrorMessage(raw, resp.StatusCode)}
	}
	var text strings.Builder
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		switch gjson.Get(line, "type").String() {
		case "text":
			if delta := gjson.Get(line, "text").String(); delta != "" {
				text.WriteString(delta)
			}
		case "error":
			msg := gjson.Get(line, "error.message").String()
			if msg == "" {
				msg = line
			}
			return "", statusErr{code: http.StatusBadGateway, msg: msg}
		case "done":
			if final := gjson.Get(line, "output.text").String(); final != "" && text.Len() == 0 {
				text.WriteString(final)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	return strings.TrimSpace(text.String()), nil
}

func cursorSDKBridgeErrorMessage(body []byte, status int) string {
	if msg := gjson.GetBytes(body, "error.message").String(); msg != "" {
		return msg
	}
	if len(bytes.TrimSpace(body)) > 0 {
		return string(body)
	}
	return fmt.Sprintf("Cursor SDK bridge request failed with status %d", status)
}
