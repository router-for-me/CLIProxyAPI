package websearch

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
)

// MaxSearchBodySize caps a single provider response body.
const MaxSearchBodySize = 4 << 20

// browserHeaders mirrors the shared Chromium navigation headers OMP uses
// for scrape providers.
func browserHeaders() map[string]string {
	return map[string]string{
		"User-Agent":      "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
		"Accept":          "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
		"Accept-Language": "en-US,en;q=0.9",
		"Cache-Control":   "no-cache",
	}
}

// httpClient builds a transport honoring ProxyURL, else the environment.
func httpClient(cfg Config) *http.Client {
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		MaxIdleConnsPerHost:   4,
		ResponseHeaderTimeout: 15 * time.Second,
	}
	if proxyURL := strings.TrimSpace(cfg.ProxyURL); proxyURL != "" {
		if parsed, errParse := url.Parse(proxyURL); errParse == nil {
			transport.Proxy = http.ProxyURL(parsed)
		} else {
			log.Warnf("websearch: invalid proxy URL: %v", errParse)
		}
	}
	return &http.Client{Transport: transport}
}

func doerFor(cfg Config) Doer {
	if cfg.doer != nil {
		return cfg.doer
	}
	return httpClient(cfg)
}

// fetchJSON executes req and returns the body for 2xx responses. Error
// bodies are summarized without leaking credentials.
func fetchJSON(ctx context.Context, doer Doer, req *http.Request) ([]byte, int, error) {
	req = req.WithContext(ctx)
	resp, errDo := doer.Do(req)
	if errDo != nil {
		return nil, 0, errDo
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.WithError(errClose).Debug("websearch: close response body")
		}
	}()
	body, errRead := io.ReadAll(io.LimitReader(resp.Body, MaxSearchBodySize+1))
	if errRead != nil {
		return nil, resp.StatusCode, fmt.Errorf("read response: %w", errRead)
	}
	if int64(len(body)) > MaxSearchBodySize {
		return nil, resp.StatusCode, fmt.Errorf("response exceeds %d bytes", MaxSearchBodySize)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, resp.StatusCode, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, summarizeErrorBody(body))
	}
	return body, resp.StatusCode, nil
}

// fetchJSONCapture is fetchJSON for callers that also need the response
// headers, e.g. to read an upstream request id that the JSON body omits.
// The captured header is nil when the call failed before a response.
func fetchJSONCapture(ctx context.Context, doer Doer, req *http.Request, header *http.Header) ([]byte, int, error) {
	req = req.WithContext(ctx)
	resp, errDo := doer.Do(req)
	if errDo != nil {
		return nil, 0, errDo
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.WithError(errClose).Debug("websearch: close response body")
		}
	}()
	if header != nil {
		*header = resp.Header
	}
	body, errRead := io.ReadAll(io.LimitReader(resp.Body, MaxSearchBodySize+1))
	if errRead != nil {
		return nil, resp.StatusCode, fmt.Errorf("read response: %w", errRead)
	}
	if int64(len(body)) > MaxSearchBodySize {
		return nil, resp.StatusCode, fmt.Errorf("response exceeds %d bytes", MaxSearchBodySize)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, resp.StatusCode, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, summarizeErrorBody(body))
	}
	return body, resp.StatusCode, nil
}

func summarizeErrorBody(body []byte) string {
	trimmed := strings.TrimSpace(string(body))
	if len(trimmed) > 512 {
		trimmed = trimmed[:512] + "..."
	}
	if trimmed == "" {
		return "empty error body"
	}
	return trimmed
}

func newJSONRequest(ctx context.Context, method, requestURL string, body []byte, headers map[string]string) (*http.Request, error) {
	var reader *bytes.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	} else {
		reader = bytes.NewReader(nil)
	}
	req, errRequest := http.NewRequestWithContext(ctx, method, requestURL, reader)
	if errRequest != nil {
		return nil, errRequest
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for key, value := range headers {
		if value != "" {
			req.Header.Set(key, value)
		}
	}
	return req, nil
}

// urlQueryEscape escapes one query parameter value.
func urlQueryEscape(value string) string {
	return url.QueryEscape(value)
}

// clampCount bounds a result count to [min,max], defaulting non-positive
// values to fallback.
func clampCount(value, min, max, fallback int) int {
	if value <= 0 {
		return fallback
	}
	if value < min {
		return min
	}
	if value > max {
		return max
	}
	return value
}

// Gemini retry policy, mirroring OMP: four attempts with an exponential
// base delay, honoring a server-supplied Retry-After, and a hard ceiling
// so a rate-limit budget cannot stall the chain indefinitely.
// Retry pacing is a var so tests can shrink the backoff without waiting.
var (
	geminiBaseDelay    = 1000 * time.Millisecond
	geminiMaxRateDelay = 5 * time.Minute
)

const geminiMaxRetries = 3

// retryableStatus reports whether a status is worth another attempt.
func retryableStatus(status int) bool {
	return status == http.StatusTooManyRequests || status >= 500
}

// fetchWithRetry issues a request, retrying transport errors and
// rate-limit / server statuses with exponential backoff. A non-retryable
// status is returned to the caller immediately so its body can be read.
func fetchWithRetry(ctx context.Context, cfg Config, label, endpoint string, payload []byte, headers map[string]string) (*http.Response, error) {
	var lastErr error
	var lastResp *http.Response
	for attempt := 0; attempt <= geminiMaxRetries; attempt++ {
		if attempt > 0 {
			if errWait := waitForRetry(ctx, attempt, lastRetryAfter(lastResp)); errWait != nil {
				return nil, &ProviderError{Provider: label, Message: errWait.Error()}
			}
		}
		httpReq, errRequest := newJSONRequest(ctx, http.MethodPost, endpoint, payload, headers)
		if errRequest != nil {
			return nil, &ProviderError{Provider: label, Message: errRequest.Error()}
		}
		httpReq.Header.Set("Content-Type", "application/json")
		resp, errDo := doerFor(cfg).Do(httpReq)
		if errDo != nil {
			lastErr = errDo
			lastResp = nil
			if ctx.Err() != nil {
				return nil, &ProviderError{Provider: label, Message: errDo.Error()}
			}
			continue
		}
		if !retryableStatus(resp.StatusCode) {
			return resp, nil
		}
		// Drain and close before retrying so the connection can be reused.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		_ = resp.Body.Close()
		lastResp = resp
		lastErr = &ProviderError{Provider: label, Message: "retryable upstream status", Status: resp.StatusCode}
	}
	if lastResp != nil {
		// Hand back the last response so the caller can report its body.
		return lastResp, nil
	}
	if lastErr == nil {
		lastErr = errors.New("request failed")
	}
	return nil, &ProviderError{Provider: label, Message: lastErr.Error()}
}

// lastRetryAfter extracts a server-supplied delay in seconds.
func lastRetryAfter(resp *http.Response) time.Duration {
	if resp == nil {
		return 0
	}
	value := strings.TrimSpace(resp.Header.Get("Retry-After"))
	if value == "" {
		return 0
	}
	if seconds, errParse := strconv.Atoi(value); errParse == nil && seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	if when, errParse := http.ParseTime(value); errParse == nil {
		if delay := time.Until(when); delay > 0 {
			return delay
		}
	}
	return 0
}

// waitForRetry sleeps for the exponential delay, or the server-supplied
// delay when longer, capped by the rate-limit budget.
func waitForRetry(ctx context.Context, attempt int, serverDelay time.Duration) error {
	delay := geminiBaseDelay << attempt
	if serverDelay > delay {
		delay = serverDelay
	}
	if delay > geminiMaxRateDelay {
		delay = geminiMaxRateDelay
	}
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
