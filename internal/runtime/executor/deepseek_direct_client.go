package executor

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/andybalholm/brotli"
	utls "github.com/refraction-networking/utls"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

const (
	deepSeekLoginURL             = "https://chat.deepseek.com/api/v0/users/login"
	deepSeekCreateSessionURL     = "https://chat.deepseek.com/api/v0/chat_session/create"
	deepSeekCreatePowURL         = "https://chat.deepseek.com/api/v0/chat/create_pow_challenge"
	deepSeekCompletionURL        = "https://chat.deepseek.com/api/v0/chat/completion"
	deepSeekContinueURL          = "https://chat.deepseek.com/api/v0/chat/continue"
	deepSeekDeleteSessionURL     = "https://chat.deepseek.com/api/v0/chat_session/delete"
	deepSeekCompletionTargetPath = "/api/v0/chat/completion"
	deepSeekMaxContinueRounds    = 8
)

var deepSeekBaseHeaders = map[string]string{
	"Host":              "chat.deepseek.com",
	"Accept":            "application/json",
	"Content-Type":      "application/json",
	"accept-charset":    "UTF-8",
	"User-Agent":        "DeepSeek/2.0.1 Android/35",
	"x-client-platform": "android",
	"x-client-version":  "2.0.1",
	"x-client-locale":   "zh_CN",
}

func (e *DeepSeekProxyExecutor) openCompletion(ctx context.Context, auth *cliproxyauth.Auth, dsAuth *deepSeekAuth, request deepSeekRequest, originalBody []byte) (string, *http.Response, error) {
	sessionID, err := e.createSession(ctx, dsAuth.Token)
	if err != nil && isDeepSeekAuthError(err) {
		if refreshed, refreshErr := e.refreshAuthToken(ctx, auth, dsAuth); refreshErr == nil && refreshed {
			sessionID, err = e.createSession(ctx, dsAuth.Token)
		}
	}
	if err != nil {
		return "", nil, err
	}
	powHeader, err := e.getPowHeader(ctx, dsAuth.Token)
	if err != nil && isDeepSeekAuthError(err) {
		if refreshed, refreshErr := e.refreshAuthToken(ctx, auth, dsAuth); refreshErr == nil && refreshed {
			powHeader, err = e.getPowHeader(ctx, dsAuth.Token)
		}
	}
	if err != nil {
		return "", nil, err
	}
	payload := request.completionPayload(sessionID)
	headers := e.authHeaders(dsAuth.Token)
	headers["x-ds-pow-response"] = powHeader
	body, _ := json.Marshal(payload)
	helps.RecordAPIRequest(ctx, e.cfg, helps.UpstreamRequestLog{
		URL:       deepSeekCompletionURL,
		Method:    http.MethodPost,
		Headers:   headersToHTTPHeader(headers),
		Body:      originalBody,
		Provider:  e.Identifier(),
		AuthID:    authID(auth),
		AuthLabel: authLabel(auth),
		AuthType:  "deepseek",
		AuthValue: authLabel(auth),
	})
	resp, err := e.postRaw(ctx, e.stream, deepSeekCompletionURL, headers, body)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return "", nil, err
	}
	helps.RecordAPIResponseMetadata(ctx, e.cfg, resp.StatusCode, resp.Header.Clone())
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := readDeepSeekResponseBody(resp)
		_ = resp.Body.Close()
		helps.AppendAPIResponseChunk(ctx, e.cfg, b)
		return "", nil, statusErr{code: resp.StatusCode, msg: string(b)}
	}
	return sessionID, resp, nil
}

func (e *DeepSeekProxyExecutor) collectDeepSeekResponse(ctx context.Context, auth *cliproxyauth.Auth, dsAuth *deepSeekAuth, initial *http.Response, sessionID string, request deepSeekRequest) (deepSeekResult, error) {
	var result deepSeekResult
	state := deepSeekContinueState{SessionID: sessionID}
	current := initial
	for round := 0; ; round++ {
		partial, err := consumeDeepSeekSSE(ctx, current.Body, request.Thinking, &state, func(line []byte) {
			helps.AppendAPIResponseChunk(ctx, e.cfg, line)
		})
		_ = current.Body.Close()
		result.Content += partial.Content
		result.Reasoning += partial.Reasoning
		if err != nil {
			return result, err
		}
		if !state.shouldContinue() || round >= deepSeekMaxContinueRounds {
			return result, nil
		}
		next, err := e.callContinue(ctx, auth, dsAuth, sessionID, state.ResponseMessageID)
		if err != nil {
			return result, err
		}
		current = next
		state.prepareNext()
	}
}

func (e *DeepSeekProxyExecutor) resolveDeepSeekAuth(ctx context.Context, auth *cliproxyauth.Auth) (*deepSeekAuth, error) {
	if auth == nil {
		return nil, statusErr{code: http.StatusUnauthorized, msg: "deepseek auth is missing"}
	}
	if token := extractDeepSeekToken(auth); token != "" {
		return &deepSeekAuth{Token: token, AccountID: auth.ID}, nil
	}
	token, err := e.login(ctx, auth)
	if err != nil {
		return nil, err
	}
	setDeepSeekAuthToken(auth, token)
	return &deepSeekAuth{Token: token, AccountID: auth.ID}, nil
}

func (e *DeepSeekProxyExecutor) refreshAuthToken(ctx context.Context, auth *cliproxyauth.Auth, dsAuth *deepSeekAuth) (bool, error) {
	token, err := e.login(ctx, auth)
	if err != nil {
		return false, err
	}
	setDeepSeekAuthToken(auth, token)
	dsAuth.Token = token
	return true, nil
}

func (e *DeepSeekProxyExecutor) login(ctx context.Context, auth *cliproxyauth.Auth) (string, error) {
	payload := map[string]any{
		"password":  strings.TrimSpace(stringFromAuth(auth, "password")),
		"device_id": "deepseek_to_api",
		"os":        "android",
	}
	if payload["password"] == "" {
		return "", statusErr{code: http.StatusUnauthorized, msg: "deepseek password is missing"}
	}
	if email := strings.TrimSpace(stringFromAuth(auth, "email")); email != "" {
		payload["email"] = email
	} else if mobile := strings.TrimSpace(stringFromAuth(auth, "mobile")); mobile != "" {
		loginMobile, areaCode := normalizeDeepSeekMobile(mobile)
		payload["mobile"] = loginMobile
		if areaCode != nil {
			payload["area_code"] = areaCode
		}
	} else {
		return "", statusErr{code: http.StatusUnauthorized, msg: "deepseek email/mobile is missing"}
	}
	body, status, err := e.postJSON(ctx, e.regular, deepSeekLoginURL, deepSeekBaseHeaders, payload)
	if err != nil {
		return "", err
	}
	code, bizCode, msg, bizMsg := deepSeekResponseStatus(body)
	if status != http.StatusOK || code != 0 || bizCode != 0 {
		return "", statusErr{code: statusCodeOr(status, http.StatusUnauthorized), msg: failureMessage(msg, bizMsg, "deepseek login failed")}
	}
	token := jsonPointerString(body, "data", "biz_data", "user", "token")
	if token == "" {
		return "", statusErr{code: http.StatusUnauthorized, msg: "deepseek login token is missing"}
	}
	return token, nil
}

func (e *DeepSeekProxyExecutor) createSession(ctx context.Context, token string) (string, error) {
	body, status, err := e.postJSON(ctx, e.regular, deepSeekCreateSessionURL, e.authHeaders(token), map[string]any{"agent": "chat"})
	if err != nil {
		return "", err
	}
	code, bizCode, msg, bizMsg := deepSeekResponseStatus(body)
	if status == http.StatusOK && code == 0 && bizCode == 0 {
		if sessionID := jsonPointerString(body, "data", "biz_data", "id"); sessionID != "" {
			return sessionID, nil
		}
		if sessionID := jsonPointerString(body, "data", "biz_data", "chat_session", "id"); sessionID != "" {
			return sessionID, nil
		}
	}
	return "", deepSeekAPIError{status: status, code: code, bizCode: bizCode, msg: failureMessage(msg, bizMsg, "create session failed")}
}

func (e *DeepSeekProxyExecutor) getPowHeader(ctx context.Context, token string) (string, error) {
	body, status, err := e.postJSON(ctx, e.regular, deepSeekCreatePowURL, e.authHeaders(token), map[string]any{"target_path": deepSeekCompletionTargetPath})
	if err != nil {
		return "", err
	}
	code, bizCode, msg, bizMsg := deepSeekResponseStatus(body)
	if status != http.StatusOK || code != 0 || bizCode != 0 {
		return "", deepSeekAPIError{status: status, code: code, bizCode: bizCode, msg: failureMessage(msg, bizMsg, "get pow failed")}
	}
	challenge, _ := jsonPointer(body, "data", "biz_data", "challenge").(map[string]any)
	if challenge == nil {
		return "", errors.New("deepseek pow challenge is missing")
	}
	answer, err := solveDeepSeekPow(ctx, challenge)
	if err != nil {
		return "", err
	}
	return buildDeepSeekPowHeader(challenge, answer)
}

func (e *DeepSeekProxyExecutor) callContinue(ctx context.Context, auth *cliproxyauth.Auth, dsAuth *deepSeekAuth, sessionID string, messageID int) (*http.Response, error) {
	if sessionID == "" || messageID <= 0 {
		return nil, errors.New("missing deepseek continue identifiers")
	}
	payload := map[string]any{"chat_session_id": sessionID, "message_id": messageID, "fallback_to_resume": true}
	powHeader, err := e.getPowHeader(ctx, dsAuth.Token)
	if err != nil {
		return nil, err
	}
	headers := e.authHeaders(dsAuth.Token)
	headers["x-ds-pow-response"] = powHeader
	body, _ := json.Marshal(payload)
	helps.RecordAPIRequest(ctx, e.cfg, helps.UpstreamRequestLog{
		URL:       deepSeekContinueURL,
		Method:    http.MethodPost,
		Headers:   headersToHTTPHeader(headers),
		Body:      body,
		Provider:  e.Identifier(),
		AuthID:    authID(auth),
		AuthLabel: authLabel(auth),
		AuthType:  "deepseek",
		AuthValue: authLabel(auth),
	})
	resp, err := e.postRaw(ctx, e.stream, deepSeekContinueURL, headers, body)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return nil, err
	}
	helps.RecordAPIResponseMetadata(ctx, e.cfg, resp.StatusCode, resp.Header.Clone())
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := readDeepSeekResponseBody(resp)
		_ = resp.Body.Close()
		helps.AppendAPIResponseChunk(ctx, e.cfg, b)
		return nil, statusErr{code: resp.StatusCode, msg: string(b)}
	}
	return resp, nil
}

func (e *DeepSeekProxyExecutor) deleteSession(ctx context.Context, token, sessionID string) {
	if token == "" || sessionID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	_, _, err := e.postJSON(ctx, e.regular, deepSeekDeleteSessionURL, e.authHeaders(token), map[string]any{"chat_session_id": sessionID})
	if err != nil {
		log.Debugf("deepseek delete session failed: %v", err)
	}
}

func (e *DeepSeekProxyExecutor) authHeaders(token string) map[string]string {
	headers := make(map[string]string, len(deepSeekBaseHeaders)+1)
	for key, value := range deepSeekBaseHeaders {
		headers[key] = value
	}
	headers["authorization"] = "Bearer " + token
	return headers
}

func (e *DeepSeekProxyExecutor) postJSON(ctx context.Context, client *http.Client, url string, headers map[string]string, payload any) (map[string]any, int, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, 0, err
	}
	resp, err := e.postRaw(ctx, client, url, headers, body)
	if err != nil {
		return nil, 0, err
	}
	defer closeDeepSeekBody(resp.Body)
	payloadBytes, err := readDeepSeekResponseBody(resp)
	if err != nil {
		return nil, resp.StatusCode, err
	}
	out := map[string]any{}
	if len(payloadBytes) > 0 {
		if err := json.Unmarshal(payloadBytes, &out); err != nil {
			return nil, resp.StatusCode, fmt.Errorf("deepseek json parse failed: %w", err)
		}
	}
	return out, resp.StatusCode, nil
}

func (e *DeepSeekProxyExecutor) postRaw(ctx context.Context, client *http.Client, url string, headers map[string]string, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	return client.Do(req)
}

func newDeepSeekHTTPClient(timeout time.Duration) *http.Client {
	dialContext := (&net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	transport := &http.Transport{
		ForceAttemptHTTP2:   false,
		MaxIdleConns:        200,
		MaxIdleConnsPerHost: 100,
		IdleConnTimeout:     90 * time.Second,
		Proxy:               http.ProxyFromEnvironment,
		DialContext:         dialContext,
		DialTLSContext:      safariTLSDialer(dialContext),
		TLSClientConfig:     &tls.Config{MinVersion: tls.VersionTLS12},
	}
	return &http.Client{Timeout: timeout, Transport: transport}
}

func safariTLSDialer(dialContext func(context.Context, string, string) (net.Conn, error)) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		plainConn, err := dialContext(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		host, _, _ := net.SplitHostPort(addr)
		uConn := utls.UClient(plainConn, &utls.Config{ServerName: host}, utls.HelloSafari_Auto)
		if err := forceHTTP11ALPN(uConn); err != nil {
			_ = plainConn.Close()
			return nil, err
		}
		if err := uConn.HandshakeContext(ctx); err != nil {
			_ = plainConn.Close()
			return nil, err
		}
		if negotiated := uConn.ConnectionState().NegotiatedProtocol; negotiated != "" && negotiated != "http/1.1" {
			_ = uConn.Close()
			return nil, fmt.Errorf("unexpected ALPN protocol negotiated: %s", negotiated)
		}
		return uConn, nil
	}
}

func forceHTTP11ALPN(uConn *utls.UConn) error {
	if err := uConn.BuildHandshakeState(); err != nil {
		return err
	}
	for _, ext := range uConn.Extensions {
		alpnExt, ok := ext.(*utls.ALPNExtension)
		if !ok {
			continue
		}
		alpnExt.AlpnProtocols = []string{"http/1.1"}
		return nil
	}
	return nil
}

func readDeepSeekResponseBody(resp *http.Response) ([]byte, error) {
	encoding := strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Encoding")))
	var reader io.Reader = resp.Body
	switch encoding {
	case "gzip":
		gz, err := gzip.NewReader(resp.Body)
		if err != nil {
			return nil, err
		}
		defer func() { _ = gz.Close() }()
		reader = gz
	case "br":
		reader = brotli.NewReader(resp.Body)
	}
	return io.ReadAll(reader)
}

func closeDeepSeekBody(body io.Closer) {
	if body == nil {
		return
	}
	if err := body.Close(); err != nil {
		log.Errorf("deepseek executor: close response body error: %v", err)
	}
}

func normalizeDeepSeekMobile(raw string) (string, any) {
	s := strings.TrimSpace(raw)
	hasPlus := strings.HasPrefix(s, "+")
	var b strings.Builder
	for _, r := range s {
		if unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	digits := b.String()
	if (hasPlus || strings.HasPrefix(digits, "86")) && strings.HasPrefix(digits, "86") && len(digits) == 13 {
		return digits[2:], nil
	}
	return digits, nil
}
