package executor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

const (
	codexDefaultConnectTimeout        = 30 * time.Second
	codexDefaultResponseHeaderTimeout = 60 * time.Second
	codexDefaultFirstEventTimeout     = 2 * time.Minute
	codexDefaultStreamIdleTimeout     = 15 * time.Minute
	codexDefaultWebsocketPingInterval = 30 * time.Second
)

type codexTimeouts struct {
	connect        time.Duration
	responseHeader time.Duration
	firstEvent     time.Duration
	streamIdle     time.Duration
	websocketPing  time.Duration
}

func codexTimeoutsFromConfig(cfg *config.Config) codexTimeouts {
	timeouts := codexTimeouts{
		connect:        codexDefaultConnectTimeout,
		responseHeader: codexDefaultResponseHeaderTimeout,
		firstEvent:     codexDefaultFirstEventTimeout,
		streamIdle:     codexDefaultStreamIdleTimeout,
		websocketPing:  codexDefaultWebsocketPingInterval,
	}
	if cfg == nil {
		return timeouts
	}
	timeouts.connect = positiveSecondsOrDefault(cfg.Codex.ConnectTimeoutSeconds, timeouts.connect)
	timeouts.responseHeader = positiveSecondsOrDefault(cfg.Codex.ResponseHeaderTimeoutSeconds, timeouts.responseHeader)
	timeouts.firstEvent = positiveSecondsOrDefault(cfg.Codex.FirstEventTimeoutSeconds, timeouts.firstEvent)
	timeouts.streamIdle = positiveSecondsOrDefault(cfg.Codex.StreamIdleTimeoutSeconds, timeouts.streamIdle)
	timeouts.websocketPing = positiveSecondsOrDefault(cfg.Codex.WebsocketPingIntervalSeconds, timeouts.websocketPing)
	if timeouts.websocketPing >= timeouts.streamIdle {
		timeouts.websocketPing = timeouts.streamIdle / 2
		if timeouts.websocketPing <= 0 {
			timeouts.websocketPing = time.Second
		}
	}
	return timeouts
}

func positiveSecondsOrDefault(seconds int, fallback time.Duration) time.Duration {
	if seconds <= 0 {
		return fallback
	}
	return time.Duration(seconds) * time.Second
}

type codexRequestTimeoutError struct {
	phase   string
	timeout time.Duration
}

func (e codexRequestTimeoutError) Error() string {
	return fmt.Sprintf("codex executor: upstream %s timeout after %s", e.phase, e.timeout)
}

func (codexRequestTimeoutError) StatusCode() int {
	return http.StatusGatewayTimeout
}

func (codexRequestTimeoutError) RetryAfter() *time.Duration {
	return nil
}

func (codexRequestTimeoutError) IsRequestScoped() bool {
	return true
}

type codexHTTPResult struct {
	response *http.Response
	err      error
}

// doCodexHTTPRequest bounds the wait for response headers without imposing a
// total request timeout on long-lived response streams. The request context is
// canceled when the returned response body is closed.
func doCodexHTTPRequest(ctx context.Context, client *http.Client, req *http.Request, timeout time.Duration) (*http.Response, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if client == nil {
		return nil, fmt.Errorf("codex executor: HTTP client is nil")
	}
	if req == nil {
		return nil, fmt.Errorf("codex executor: HTTP request is nil")
	}

	requestCtx, cancel := context.WithCancel(ctx)
	request := req.WithContext(requestCtx)
	resultCh := make(chan codexHTTPResult)
	abandoned := make(chan struct{})
	go func() {
		response, err := client.Do(request)
		select {
		case resultCh <- codexHTTPResult{response: response, err: err}:
		case <-abandoned:
			if response != nil && response.Body != nil {
				_ = response.Body.Close()
			}
		}
	}()

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case result := <-resultCh:
		if result.err != nil {
			cancel()
			return nil, result.err
		}
		if result.response == nil || result.response.Body == nil {
			cancel()
			return result.response, nil
		}
		result.response.Body = &cancelOnCloseReadCloser{ReadCloser: result.response.Body, cancel: cancel}
		return result.response, nil
	case <-ctx.Done():
		close(abandoned)
		cancel()
		return nil, ctx.Err()
	case <-timer.C:
		close(abandoned)
		cancel()
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return nil, codexRequestTimeoutError{phase: "response header", timeout: timeout}
	}
}

type cancelOnCloseReadCloser struct {
	io.ReadCloser
	cancel context.CancelFunc
	once   sync.Once
}

func (r *cancelOnCloseReadCloser) Close() error {
	err := r.ReadCloser.Close()
	r.once.Do(r.cancel)
	return err
}

type codexActivityTimeoutBody struct {
	body         io.ReadCloser
	ctx          context.Context
	firstTimeout time.Duration
	idleTimeout  time.Duration
	started      atomic.Bool
	timedOut     atomic.Bool
	closeOnce    sync.Once
}

func newCodexActivityTimeoutBody(ctx context.Context, body io.ReadCloser, firstTimeout, idleTimeout time.Duration) io.ReadCloser {
	if body == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return &codexActivityTimeoutBody{
		body:         body,
		ctx:          ctx,
		firstTimeout: firstTimeout,
		idleTimeout:  idleTimeout,
	}
}

func (r *codexActivityTimeoutBody) Read(p []byte) (int, error) {
	if r.timedOut.Load() {
		return 0, r.timeoutError()
	}
	timeout := r.firstTimeout
	phase := "first event"
	if r.started.Load() {
		timeout = r.idleTimeout
		phase = "stream idle"
	}
	timer := time.AfterFunc(timeout, func() {
		r.timedOut.Store(true)
		r.closeOnce.Do(func() { _ = r.body.Close() })
	})
	n, err := r.body.Read(p)
	if !timer.Stop() || r.timedOut.Load() {
		if ctxErr := r.ctx.Err(); ctxErr != nil {
			return 0, ctxErr
		}
		return 0, codexRequestTimeoutError{phase: phase, timeout: timeout}
	}
	if n > 0 {
		r.started.Store(true)
	}
	if err != nil {
		if ctxErr := r.ctx.Err(); ctxErr != nil {
			return n, ctxErr
		}
	}
	return n, err
}

func (r *codexActivityTimeoutBody) Close() error {
	var err error
	r.closeOnce.Do(func() { err = r.body.Close() })
	return err
}

func (r *codexActivityTimeoutBody) timeoutError() error {
	if err := r.ctx.Err(); err != nil {
		return err
	}
	phase := "first event"
	timeout := r.firstTimeout
	if r.started.Load() {
		phase = "stream idle"
		timeout = r.idleTimeout
	}
	return codexRequestTimeoutError{phase: phase, timeout: timeout}
}
