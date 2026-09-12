package management

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
	log "github.com/sirupsen/logrus"
	"golang.org/x/net/proxy"
)

const (
	proxyFallbackDialTimeout = 5 * time.Second
	proxyFallbackRememberFor = 30 * time.Second
)

var proxyFallbackStates sync.Map // map[string]*proxyFallbackState

type proxyFallbackState struct {
	mu        sync.Mutex
	downUntil time.Time
}

type proxyDirectFallbackTransport struct {
	proxyKey string
	proxy    http.RoundTripper
	direct   http.RoundTripper
}

func wrapProxyDirectFallback(proxyStr string, fallback http.RoundTripper) http.RoundTripper {
	proxyTransport := buildTimeoutAwareProxyTransport(proxyStr)
	if proxyTransport == nil {
		if fallback != nil {
			return fallback
		}
		return directAPICallTransport()
	}
	return &proxyDirectFallbackTransport{
		proxyKey: strings.TrimSpace(proxyStr),
		proxy:    proxyTransport,
		direct:   directAPICallTransport(),
	}
}

func buildTimeoutAwareProxyTransport(proxyStr string) *http.Transport {
	dialer, mode, errBuild := proxyutil.BuildDialer(proxyStr)
	if errBuild != nil || mode != proxyutil.ModeProxy || dialer == nil {
		return buildProxyTransport(proxyStr)
	}

	transport := proxyutil.NewDirectTransport()
	transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		if ctx == nil {
			ctx = context.Background()
		}
		dialCtx, cancel := context.WithTimeout(ctx, proxyFallbackDialTimeout)
		defer cancel()
		if contextAware, ok := dialer.(proxy.ContextDialer); ok {
			return contextAware.DialContext(dialCtx, network, addr)
		}
		return dialer.Dial(network, addr)
	}
	return transport
}

func (t *proxyDirectFallbackTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t == nil {
		return nil, errors.New("proxy fallback transport is nil")
	}
	// A remembered proxy outage may bypass the proxy only for replay-safe
	// methods. Non-idempotent requests must continue through the proxy so a
	// transport error cannot turn into an unapproved direct retry.
	if isProxyFallbackRetrySafe(req) && proxyFallbackUnavailable(t.proxyKey) {
		return t.direct.RoundTrip(req)
	}

	retryAllowed := isProxyFallbackRetrySafe(req)
	var retryReq *http.Request
	var errClone error
	if retryAllowed {
		retryReq, errClone = cloneAPICallRequest(req)
	}
	resp, errDo := t.proxy.RoundTrip(req)
	if errDo == nil {
		if retryReq != nil && retryReq.Body != nil && retryReq.Body != http.NoBody {
			_ = retryReq.Body.Close()
		}
		return resp, nil
	}
	if !isProxyFallbackError(errDo) {
		return nil, errDo
	}
	markProxyFallbackDown(t.proxyKey)
	if !retryAllowed {
		log.WithError(errDo).
			WithField("proxy", proxyutil.Redact(t.proxyKey)).
			Warn("management APICall proxy failed; direct fallback suppressed for non-replay-safe method")
		return nil, errDo
	}
	log.WithError(errDo).WithField("proxy", proxyutil.Redact(t.proxyKey)).Warn("management APICall proxy failed; falling back to direct")
	if retryReq == nil {
		if errClone != nil {
			return nil, errDo
		}
		return t.direct.RoundTrip(req)
	}
	return t.direct.RoundTrip(retryReq)
}

// Proxy fallback retries only methods whose HTTP semantics are safe to repeat.
// Retrying a POST or PATCH after a transport error can duplicate a side effect
// when the proxy already forwarded the request before the connection failed.
func isProxyFallbackRetrySafe(req *http.Request) bool {
	if req == nil {
		return false
	}
	switch strings.ToUpper(strings.TrimSpace(req.Method)) {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	default:
		return false
	}
}

func cloneAPICallRequest(req *http.Request) (*http.Request, error) {
	if req == nil {
		return nil, errors.New("request is nil")
	}
	clone := req.Clone(req.Context())
	if req.Body == nil || req.Body == http.NoBody {
		return clone, nil
	}
	if req.GetBody == nil {
		return nil, errors.New("request body cannot be replayed")
	}
	body, errBody := req.GetBody()
	if errBody != nil {
		return nil, errBody
	}
	clone.Body = body
	return clone, nil
}

func proxyFallbackUnavailable(proxyKey string) bool {
	state := proxyFallbackStateFor(proxyKey)
	if state == nil {
		return false
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	return time.Now().Before(state.downUntil)
}

func markProxyFallbackDown(proxyKey string) {
	state := proxyFallbackStateFor(proxyKey)
	if state == nil {
		return
	}
	state.mu.Lock()
	state.downUntil = time.Now().Add(proxyFallbackRememberFor)
	state.mu.Unlock()
}

func resetProxyFallbackState(proxyKey string) {
	proxyKey = strings.TrimSpace(proxyKey)
	if proxyKey == "" {
		return
	}
	proxyFallbackStates.Delete(proxyKey)
}

func proxyFallbackStateFor(proxyKey string) *proxyFallbackState {
	proxyKey = strings.TrimSpace(proxyKey)
	if proxyKey == "" {
		return nil
	}
	if existing, ok := proxyFallbackStates.Load(proxyKey); ok {
		if state, okState := existing.(*proxyFallbackState); okState {
			return state
		}
	}
	state := &proxyFallbackState{}
	actual, _ := proxyFallbackStates.LoadOrStore(proxyKey, state)
	if stored, ok := actual.(*proxyFallbackState); ok {
		return stored
	}
	return state
}

func isProxyFallbackError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	msg := strings.ToLower(err.Error())
	for _, needle := range []string{
		"connection refused",
		"network is unreachable",
		"no such host",
		"i/o timeout",
		"dial http proxy failed",
		"proxy connect",
		"connection reset",
	} {
		if strings.Contains(msg, needle) {
			return true
		}
	}
	return false
}

func apiCallRequestFailedMessage(err error) string {
	if err == nil {
		return "request failed"
	}
	if errors.Is(err, context.Canceled) {
		return "request canceled"
	}
	if errors.Is(err, context.DeadlineExceeded) || isTimeoutError(err) {
		return "request timed out"
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "connection refused"):
		return "proxy connection refused"
	case strings.Contains(msg, "proxy connect"), strings.Contains(msg, "dial http proxy failed"):
		return "proxy connect failed"
	default:
		return "request failed"
	}
}

func isTimeoutError(err error) bool {
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	return strings.Contains(strings.ToLower(err.Error()), "timeout")
}
