package helps

import (

	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	log "github.com/sirupsen/logrus"
)

// NewProxyAwareHTTPClient creates an HTTP client with proper proxy configuration priority:
// 1. Use auth.ProxyURL if configured (highest priority)
// 2. Use cfg.ProxyURL if auth proxy is not configured
// 3. Use RoundTripper from context if neither are configured
//
// Parameters:
//   - ctx: The context containing optional RoundTripper
//   - cfg: The application configuration
//   - auth: The authentication information
//   - timeout: The client timeout (0 means no timeout)
//
// Returns:
//   - *http.Client: An HTTP client with configured proxy or transport
func NewProxyAwareHTTPClient(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth, timeout time.Duration) *http.Client {
	httpClient := &http.Client{}
	if timeout > 0 {
		httpClient.Timeout = timeout
	}

	// Priority 1: Use auth.ProxyURL if configured
	var proxyURL string
	if auth != nil {
		proxyURL = strings.TrimSpace(auth.ProxyURL)
	}

	// Priority 2: Use cfg.ProxyURL if auth proxy is not configured
	if proxyURL == "" && cfg != nil {
		proxyURL = strings.TrimSpace(cfg.ProxyURL)
	}

	// Priority 2.5: Use cfg.TorProxy if no other proxy is configured and TOR is enabled
	if proxyURL == "" && cfg != nil && cfg.TorProxy != "" {
		proxyURL = strings.TrimSpace(cfg.TorProxy)
	}

	// If we have a proxy URL configured, set up the transport
	if proxyURL != "" {
		transport := buildProxyTransport(proxyURL)
		if transport != nil {
			httpClient.Transport = transport
			// Wrap with TOR auto-rotate if configured and enabled
			if cfg != nil && cfg.TorControl != "" && len(cfg.TorRetryableCodes) > 0 {
				httpClient.Transport = &torRetryRoundTripper{
					base:   transport,
					cfg:    cfg,
				}
			}
			return httpClient
		}
		// If proxy setup failed, log and fall through to context RoundTripper
		log.Debugf("failed to setup proxy from URL: %s, falling back to context transport", proxyutil.Redact(proxyURL))
	}

	return httpClient
}

// torRetryRoundTripper wraps an http.RoundTripper to auto-rotate TOR IP
// when upstream responses match configured retryable status codes.
type torRetryRoundTripper struct {
	base http.RoundTripper
	cfg  *config.Config
}

const defaultTorMaxRetries = 0

func (t *torRetryRoundTripper) maxRetries() int {
	if t.cfg != nil && t.cfg.TorMaxRetries > 0 {
		return t.cfg.TorMaxRetries
	}
	if t.cfg != nil && t.cfg.TorMaxRetries == 0 {
		return 0 // unlimited
	}
	return defaultTorMaxRetries
}


func (t *torRetryRoundTripper) shouldRetry(code int) bool {
	for _, c := range t.cfg.TorRetryableCodes {
		if code == c {
			return true
		}
	}
	return false
}

func (t *torRetryRoundTripper) consumeAndClose(resp *http.Response) {
	if resp == nil || resp.Body == nil {
		return
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
}



func (t *torRetryRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	ctrl := strings.TrimSpace(t.cfg.TorControl)
	if len(t.cfg.TorRetryableCodes) == 0 || ctrl == "" {
		return t.base.RoundTrip(req)
	}

	// Buffer request body for replay on retry
	var bodyBuf []byte
	if req.Body != nil {
		var err error
		bodyBuf, err = io.ReadAll(req.Body)
		if err != nil {
			return nil, fmt.Errorf("tor retry: read body: %w", err)
		}
		req.Body.Close()
	}

	setBody := func(r *http.Request) {
		if bodyBuf != nil {
			r.Body = io.NopCloser(bytes.NewReader(bodyBuf))
			r.ContentLength = int64(len(bodyBuf))
			r.GetBody = func() (io.ReadCloser, error) {
				return io.NopCloser(bytes.NewReader(bodyBuf)), nil
			}
		}
	}

	// Set GetBody on original request for safety
	if bodyBuf != nil && req.GetBody == nil {
		bodyCopy := make([]byte, len(bodyBuf))
		copy(bodyCopy, bodyBuf)
		req.GetBody = func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(bodyCopy)), nil
		}
	}

	maxRetries := t.maxRetries()
	unlimited := maxRetries == 0
	var lastResp *http.Response
	for attempt := 0; unlimited || attempt <= maxRetries; attempt++ {
		setBody(req)
		resp, err := t.base.RoundTrip(req)
		if err != nil {
			t.consumeAndClose(lastResp)
			return resp, err
		}

		if !t.shouldRetry(resp.StatusCode) {
			t.consumeAndClose(lastResp)
			return resp, nil
		}

		// Retryable code — consume, rotate, loop
		log.WithFields(log.Fields{"attempt": attempt + 1, "status": resp.StatusCode}).Info("TOR: retryable status, rotating IP")
		t.consumeAndClose(lastResp)
		lastResp = resp

		if err := util.TorSendCommand(ctrl, t.cfg.TorPassword, "SIGNAL NEWNYM"); err != nil {
			log.WithError(err).Error("TOR: rotation failed, returning last error to client")
			return resp, nil
		}

		// Wait for new circuit, but abort immediately if client disconnected
		select {
		case <-time.After(2 * time.Second):
		case <-req.Context().Done():
			log.Warn("TOR: retry cancelled by client disconnect")
			return nil, req.Context().Err()
		}
	}

	log.Warn("TOR: max retries reached, returning last error to client")
	return lastResp, nil
}

// buildProxyTransport creates an HTTP transport configured for the given proxy URL.
// It supports SOCKS5, HTTP, and HTTPS proxy protocols.
//
// Parameters:
//   - proxyURL: The proxy URL string (e.g., "socks5://user:pass@host:port", "http://host:port")
//
// Returns:
//   - *http.Transport: A configured transport, or nil if the proxy URL is invalid
func buildProxyTransport(proxyURL string) *http.Transport {
	transport, _, errBuild := proxyutil.BuildHTTPTransport(proxyURL)
	if errBuild != nil {
		log.Errorf("%v", errBuild)
		return nil
	}
	return transport
}
