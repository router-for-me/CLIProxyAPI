package client

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/deepseek/protocol"
)

// CallCompletion sends a chat completion request and returns the raw streaming
// response. The caller (executor) is responsible for consuming the SSE stream.
//
// Decoupled from ds2api's auth model: token/locale/proxyURL are passed
// explicitly. Single attempt — the conductor handles retries.
func (c *Client) CallCompletion(ctx context.Context, token, locale, proxyURL string, payload map[string]any, powResp string) (*http.Response, error) {
	clients := c.requestClientsForProxy(proxyURL)
	headers := c.authHeaders(token, locale)
	headers["x-ds-pow-response"] = powResp
	applySessionReferer(headers, payload)
	resp, err := c.streamPostOnce(ctx, clients, protocol.DeepSeekCompletionURL, headers, payload)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusOK {
		newBody, muted, _, err := detectMutedCompletion(resp.Body)
		if err != nil {
			_ = resp.Body.Close()
			return nil, err
		}
		if muted {
			_ = resp.Body.Close()
			return nil, &RequestFailure{Op: "completion", Kind: FailureMuted, Message: "user is muted"}
		}
		if newBody != nil {
			resp.Body = newBody
		}
	}
	return resp, nil
}

func (c *Client) streamPost(ctx context.Context, clients requestClients, url string, headers map[string]string, payload any) (*http.Response, error) {
	return c.streamPostWithFallback(ctx, clients, url, headers, payload, true)
}

func (c *Client) streamPostOnce(ctx context.Context, clients requestClients, url string, headers map[string]string, payload any) (*http.Response, error) {
	return c.streamPostWithFallback(ctx, clients, url, headers, payload, false)
}

func (c *Client) streamPostWithFallback(ctx context.Context, clients requestClients, url string, headers map[string]string, payload any, allowFallback bool) (*http.Response, error) {
	b, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	headers = c.jsonHeaders(headers)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := clients.stream.Do(req)
	if err != nil {
		if allowFallback {
			req2, reqErr := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(b))
			if reqErr != nil {
				return nil, reqErr
			}
			for k, v := range headers {
				req2.Header.Set(k, v)
			}
			return clients.fallbackS.Do(req2)
		}
		return nil, err
	}
	return resp, nil
}

// detectMutedCompletion peeks the response body to determine whether the
// upstream returned a muted-account JSON error instead of an SSE stream.
// DeepSeek returns HTTP 200 with a plain JSON body (not text/event-stream)
// when the account is muted: {"code":0,"data":{"biz_code":5,"biz_msg":"user is muted",...}}.
// Returns (restoredBody, muted, muteUntil, error).
func detectMutedCompletion(body io.ReadCloser) (io.ReadCloser, bool, float64, error) {
	if body == nil {
		return nil, false, 0, nil
	}
	br := bufio.NewReader(body)
	b, err := br.Peek(1)
	if err != nil && err != io.EOF {
		return io.NopCloser(br), false, 0, nil
	}
	if len(b) == 0 || b[0] != '{' {
		return io.NopCloser(br), false, 0, nil
	}
	all, err := io.ReadAll(br)
	if err != nil {
		return io.NopCloser(bytes.NewReader(nil)), false, 0, err
	}
	var parsed map[string]any
	if err := json.Unmarshal(all, &parsed); err != nil {
		return io.NopCloser(bytes.NewReader(all)), false, 0, nil
	}
	if isMutedJSONResponse(parsed) {
		return nil, true, extractMuteUntil(parsed), nil
	}
	return io.NopCloser(bytes.NewReader(all)), false, 0, nil
}

func isMutedJSONResponse(resp map[string]any) bool {
	if resp == nil {
		return false
	}
	data, _ := resp["data"].(map[string]any)
	if data == nil {
		return false
	}
	bizCode := intFrom(data["biz_code"])
	bizMsg := strings.ToLower(strings.TrimSpace(getStringFromMap(data, "biz_msg")))
	if bizCode == 5 || strings.Contains(bizMsg, "muted") {
		return true
	}
	bizData, _ := data["biz_data"].(map[string]any)
	if bizData != nil {
		if isMuted, _ := bizData["is_muted"].(float64); isMuted == 1 {
			return true
		}
	}
	return false
}

func extractMuteUntil(resp map[string]any) float64 {
	if resp == nil {
		return 0
	}
	data, _ := resp["data"].(map[string]any)
	if data == nil {
		return 0
	}
	bizData, _ := data["biz_data"].(map[string]any)
	if bizData != nil {
		if muteUntil, ok := bizData["mute_until"].(float64); ok {
			return muteUntil
		}
	}
	return 0
}

func getStringFromMap(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return s
}

// applySessionReferer points Referer at the conversation page the message
// belongs to. A browser sending a message is sitting on /a/chat/s/<id>, so a
// bare site root on every request is a mismatch between the header and the
// action it accompanies.
func applySessionReferer(headers map[string]string, payload map[string]any) {
	if headers == nil || payload == nil {
		return
	}
	sessionID := getStringFromMap(payload, "chat_session_id")
	if strings.TrimSpace(sessionID) == "" {
		return
	}
	headers["Referer"] = protocol.ChatSessionReferer(sessionID)
}
