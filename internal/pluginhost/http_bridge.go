package pluginhost

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	log "github.com/sirupsen/logrus"
)

const hostHTTPMaxAttempts = 3

func isTransientHostHTTPError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, needle := range []string{
		"service unavailable",
		"bad gateway",
		"gateway timeout",
		"unexpected eof",
		"connection reset",
		"connection refused",
		"broken pipe",
		"i/o timeout",
		"tls handshake timeout",
		"timeout awaiting response headers",
		"server closed idle connection",
		"http2: client connection force closed",
		"http2: client connection lost",
		"eof",
	} {
		if strings.Contains(msg, needle) {
			return true
		}
	}
	return false
}

func closeIdleHTTPClient(client *http.Client) {
	if client == nil || client.Transport == nil {
		return
	}
	type idleCloser interface {
		CloseIdleConnections()
	}
	if closer, ok := client.Transport.(idleCloser); ok {
		closer.CloseIdleConnections()
	}
}

type hostHTTPClient struct {
	host     *Host
	auth     *coreauth.Auth
	provider string
}

func (h *Host) newHTTPClient(auth *coreauth.Auth, providers ...string) pluginapi.HostHTTPClient {
	provider := ""
	if len(providers) > 0 {
		provider = providers[0]
	}
	return &hostHTTPClient{host: h, auth: auth, provider: provider}
}

func (c *hostHTTPClient) Do(ctx context.Context, req pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	resp, cfg, errDo := c.doHTTP(ctx, req)
	if errDo != nil {
		return pluginapi.HTTPResponse{}, errDo
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Warnf("pluginhost: response body close error: %v", errClose)
		}
	}()
	helps.RecordAPIResponseMetadata(ctx, cfg, resp.StatusCode, resp.Header.Clone())
	body, errReadAll := io.ReadAll(resp.Body)
	if len(body) > 0 {
		helps.AppendAPIResponseChunk(ctx, cfg, body)
	}
	if errReadAll != nil {
		helps.RecordAPIResponseError(ctx, cfg, errReadAll)
		return pluginapi.HTTPResponse{}, fmt.Errorf("read host http response: %w", errReadAll)
	}
	return pluginapi.HTTPResponse{
		StatusCode: resp.StatusCode,
		Headers:    cloneHeader(resp.Header),
		Body:       body,
	}, nil
}

func (c *hostHTTPClient) DoStream(ctx context.Context, req pluginapi.HTTPRequest) (pluginapi.HTTPStreamResponse, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	resp, cfg, errDo := c.doHTTP(ctx, req)
	if errDo != nil {
		return pluginapi.HTTPStreamResponse{}, errDo
	}
	helps.RecordAPIResponseMetadata(ctx, cfg, resp.StatusCode, resp.Header.Clone())
	chunks := make(chan pluginapi.HTTPStreamChunk)
	go func() {
		defer close(chunks)
		defer func() {
			if errClose := resp.Body.Close(); errClose != nil {
				log.Warnf("pluginhost: stream response body close error: %v", errClose)
			}
		}()
		buf := make([]byte, 32*1024)
		for {
			n, errRead := resp.Body.Read(buf)
			if n > 0 {
				payload := bytes.Clone(buf[:n])
				helps.AppendAPIResponseChunk(ctx, cfg, payload)
				select {
				case <-ctx.Done():
					return
				case chunks <- pluginapi.HTTPStreamChunk{Payload: payload}:
				}
			}
			if errRead != nil {
				if errRead != io.EOF {
					helps.RecordAPIResponseError(ctx, cfg, errRead)
					select {
					case <-ctx.Done():
					case chunks <- pluginapi.HTTPStreamChunk{Err: errRead}:
					}
				}
				return
			}
		}
	}()
	return pluginapi.HTTPStreamResponse{
		StatusCode: resp.StatusCode,
		Headers:    cloneHeader(resp.Header),
		Chunks:     chunks,
	}, nil
}

func (c *hostHTTPClient) doHTTP(ctx context.Context, req pluginapi.HTTPRequest) (*http.Response, *config.Config, error) {
	if c == nil || c.host == nil {
		return nil, nil, fmt.Errorf("host http client is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	cfg := c.host.currentRuntimeConfig()
	method := req.Method
	if method == "" {
		method = http.MethodGet
	}
	body := bytes.Clone(req.Body)
	helps.PrepareUpstreamForProxy(ctx, cfg, c.auth)
	client := helps.NewProxyAwareHTTPClient(ctx, cfg, c.auth, 0)
	if client == nil {
		client = &http.Client{}
	}

	var lastErr error
	for attempt := 1; attempt <= hostHTTPMaxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, cfg, fmt.Errorf("execute host http request: %w", err)
		}
		httpReq, errNewRequest := http.NewRequestWithContext(ctx, method, req.URL, bytes.NewReader(body))
		if errNewRequest != nil {
			return nil, cfg, fmt.Errorf("create host http request: %w", errNewRequest)
		}
		httpReq.Header = cloneHeader(req.Headers)
		if attempt == 1 {
			c.recordHTTPRequest(ctx, cfg, httpReq, body)
		}
		resp, errDo := client.Do(httpReq)
		if errDo == nil {
			return resp, cfg, nil
		}
		lastErr = errDo
		helps.RecordAPIResponseError(ctx, cfg, errDo)
		closeIdleHTTPClient(client)
		if attempt >= hostHTTPMaxAttempts || !isTransientHostHTTPError(errDo) {
			break
		}
		backoff := time.Duration(attempt) * 400 * time.Millisecond
		log.Debugf("pluginhost: host http retry %d/%d after transient error: %v", attempt, hostHTTPMaxAttempts, errDo)
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, cfg, fmt.Errorf("execute host http request: %w", ctx.Err())
		case <-timer.C:
		}
	}
	return nil, cfg, fmt.Errorf("execute host http request: %w", lastErr)
}

func (c *hostHTTPClient) recordHTTPRequest(ctx context.Context, cfg *config.Config, req *http.Request, body []byte) {
	if req == nil {
		return
	}
	provider := c.provider
	var authID, authLabel, authType, authValue string
	if c.auth != nil {
		authID = c.auth.ID
		authLabel = c.auth.Label
		authType, authValue = c.auth.AccountInfo()
		if provider == "" {
			provider = c.auth.Provider
		}
	}
	helps.RecordAPIRequest(ctx, cfg, helps.UpstreamRequestLog{
		URL:       req.URL.String(),
		Method:    req.Method,
		Headers:   req.Header.Clone(),
		Body:      bytes.Clone(body),
		Provider:  provider,
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})
}

func (h *Host) currentRuntimeConfig() *config.Config {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.runtimeConfig
}
