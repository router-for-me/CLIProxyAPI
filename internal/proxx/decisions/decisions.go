// Package decisions serves decision-model vendors (TypeSafe Jev, OpenAI Decisions) as native
// passthrough routes with key rotation, cooldowns, usage records and request logging (ADR 0008).
package decisions

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"mime"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/MoeForge/moego/json"
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/proxyutil"
	log "github.com/sirupsen/logrus"
)

// ExecutorType marks usage records produced by these routes.
const ExecutorType = "proxx-decisions"

const (
	defaultRateLimitCooldown = 60 * time.Second
	maxRateLimitCooldown     = time.Hour
	authFailureCooldown      = 10 * time.Minute
)

// maxBodyBytes caps request and response bodies; larger requests get 413 (never truncated).
var maxBodyBytes int64 = 64 << 20

var (
	nowFunc   = time.Now
	publishFn = usage.PublishRecord
	pools     = newPool()
)

// Vendor describes one decision-model upstream.
type Vendor struct {
	// Name is the config key under proxx.decisions.
	Name string
	// Route is the client path served by proxx.
	Route string
	// Provider is the usage/pricing provider key.
	Provider       string
	DefaultBaseURL string
	// UpstreamPath is appended to the base URL.
	UpstreamPath string
	// HeaderPrefixes are client header prefixes forwarded upstream (canonical form).
	HeaderPrefixes []string
	// BlockedHeaders are never forwarded even when they match a prefix.
	BlockedHeaders []string
	settings       func(*config.Config) config.ProxxDecisionsVendor
}

var (
	// TypeSafe is POST /v1/systemone → https://api.typesafe.ai/v1/systemone.
	TypeSafe = &Vendor{
		Name:           "typesafe",
		Route:          "/v1/systemone",
		Provider:       "typesafe",
		DefaultBaseURL: "https://api.typesafe.ai",
		UpstreamPath:   "/v1/systemone",
		HeaderPrefixes: []string{"Typesafe-"},
		settings:       func(c *config.Config) config.ProxxDecisionsVendor { return c.Proxx.Decisions.TypeSafe },
	}
	// OpenAI is POST /v1/decisions → https://api.openai.com/v1/decisions.
	OpenAI = &Vendor{
		Name:           "openai",
		Route:          "/v1/decisions",
		Provider:       "openai",
		DefaultBaseURL: "https://api.openai.com/v1",
		UpstreamPath:   "/decisions",
		HeaderPrefixes: []string{"Openai-"},
		BlockedHeaders: []string{"Openai-Organization", "Openai-Project"},
		settings:       func(c *config.Config) config.ProxxDecisionsVendor { return c.Proxx.Decisions.OpenAI },
	}
)

// pool rotates keys per vendor and tracks cooldowns by key hash.
type pool struct {
	mu       sync.Mutex
	next     map[string]int
	cooldown map[string]time.Time
}

func newPool() *pool {
	return &pool{next: map[string]int{}, cooldown: map[string]time.Time{}}
}

func keyHash(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])[:12]
}

// order returns the indexes of keys that are not cooling down, starting at the round-robin cursor,
// and the earliest cooldown end when none is available.
func (p *pool) order(vendor string, keys []string) ([]int, time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := nowFunc()
	start := p.next[vendor] % len(keys)
	p.next[vendor] = start + 1
	var out []int
	var earliest time.Time
	for i := 0; i < len(keys); i++ {
		idx := (start + i) % len(keys)
		id := vendor + ":" + keyHash(keys[idx])
		if until, ok := p.cooldown[id]; ok {
			if now.Before(until) {
				if earliest.IsZero() || until.Before(earliest) {
					earliest = until
				}
				continue
			}
			delete(p.cooldown, id)
		}
		out = append(out, idx)
	}
	return out, earliest
}

func (p *pool) cool(vendor, key string, until time.Time) {
	p.mu.Lock()
	p.cooldown[vendor+":"+keyHash(key)] = until
	p.mu.Unlock()
}

// requestModel reads "model" from a JSON body or from the JSON "request" part of a multipart body.
func requestModel(contentType string, body []byte) string {
	mediaType, params, _ := mime.ParseMediaType(contentType)
	if strings.HasPrefix(mediaType, "multipart/") && params["boundary"] != "" {
		mr := multipart.NewReader(bytes.NewReader(body), params["boundary"])
		for {
			part, err := mr.NextPart()
			if err != nil {
				return ""
			}
			if part.FormName() == "request" {
				data, errRead := io.ReadAll(part)
				if errRead != nil {
					return ""
				}
				return jsonModel(data)
			}
		}
	}
	return jsonModel(body)
}

func jsonModel(data []byte) string {
	var v struct {
		Model string `json:"model"`
	}
	if json.Unmarshal(data, &v) != nil {
		return ""
	}
	return strings.TrimSpace(v.Model)
}

// parseResponse extracts the served model and token usage. TypeSafe and OpenAI share the
// usage field names; OpenAI's input_tokens already includes cached and cache-write tokens.
func parseResponse(body []byte) (string, usage.Detail) {
	var v struct {
		Model string `json:"model"`
		Usage *struct {
			InputTokens        int64 `json:"input_tokens"`
			OutputTokens       int64 `json:"output_tokens"`
			TotalTokens        int64 `json:"total_tokens"`
			InputTokensDetails struct {
				CachedTokens     int64 `json:"cached_tokens"`
				CacheWriteTokens int64 `json:"cache_write_tokens"`
			} `json:"input_tokens_details"`
			OutputTokensDetails struct {
				ReasoningTokens int64 `json:"reasoning_tokens"`
			} `json:"output_tokens_details"`
		} `json:"usage"`
	}
	if json.Unmarshal(body, &v) != nil || v.Usage == nil {
		return strings.TrimSpace(v.Model), usage.Detail{}
	}
	u := v.Usage
	total := u.TotalTokens
	if total == 0 {
		total = u.InputTokens + u.OutputTokens
	}
	cached, write := u.InputTokensDetails.CachedTokens, u.InputTokensDetails.CacheWriteTokens
	reasoning := u.OutputTokensDetails.ReasoningTokens
	return strings.TrimSpace(v.Model), usage.Detail{
		InputTokens:         u.InputTokens,
		OutputTokens:        u.OutputTokens,
		ReasoningTokens:     reasoning,
		CachedTokens:        cached,
		CacheReadTokens:     cached,
		CacheCreationTokens: write,
		TotalTokens:         total,
		TokenBreakdown:      usage.NewSubsetTokenBreakdown(u.InputTokens, cached, write, u.OutputTokens, reasoning, total),
	}
}

var (
	clientsMu sync.Mutex
	clients   = map[string]*http.Client{}
)

// httpClient returns a cached client for the proxy URL. There is no overall timeout (AGENTS.md).
func httpClient(proxyURL string) *http.Client {
	clientsMu.Lock()
	defer clientsMu.Unlock()
	if c, ok := clients[proxyURL]; ok {
		return c
	}
	c := &http.Client{}
	transport, _, err := proxyutil.BuildHTTPTransport(proxyURL)
	if err != nil {
		log.WithError(err).Warn("proxx decisions: invalid proxy-url, using the default transport")
	}
	if transport != nil {
		c.Transport = transport
	}
	clients[proxyURL] = c
	return c
}

// skippedResponseHeaders are never copied to the client.
var skippedResponseHeaders = map[string]bool{
	"Connection": true, "Keep-Alive": true, "Proxy-Authenticate": true, "Proxy-Authorization": true,
	"Te": true, "Trailer": true, "Transfer-Encoding": true, "Upgrade": true,
	"Content-Length": true, "Content-Encoding": true, "Set-Cookie": true,
}

func (v *Vendor) upstreamHeaders(client http.Header, contentType, key string) http.Header {
	h := make(http.Header)
	for name, values := range client {
		canonical := http.CanonicalHeaderKey(name)
		if !hasAnyPrefix(canonical, v.HeaderPrefixes) || containsFold(v.BlockedHeaders, canonical) {
			continue
		}
		h[canonical] = append([]string(nil), values...)
	}
	h.Set("Content-Type", contentType)
	h.Set("Accept", "application/json")
	h.Set("Authorization", "Bearer "+key)
	return h
}

func hasAnyPrefix(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

func containsFold(list []string, s string) bool {
	for _, item := range list {
		if strings.EqualFold(item, s) {
			return true
		}
	}
	return false
}

func errorJSON(c *gin.Context, status int, message string) {
	c.JSON(status, gin.H{"error": gin.H{"message": message, "type": "proxx_error"}})
}

// retryAfter parses a Retry-After header in seconds (or HTTP date), bounded to [1s, 1h].
func retryAfter(h http.Header) time.Duration {
	value := strings.TrimSpace(h.Get("Retry-After"))
	if value == "" {
		return defaultRateLimitCooldown
	}
	d := defaultRateLimitCooldown
	if secs, err := strconv.ParseFloat(value, 64); err == nil {
		d = time.Duration(secs * float64(time.Second))
	} else if t, errDate := http.ParseTime(value); errDate == nil {
		d = t.Sub(nowFunc())
	}
	if d < time.Second {
		d = time.Second
	}
	if d > maxRateLimitCooldown {
		d = maxRateLimitCooldown
	}
	return d
}

type upstreamResult struct {
	status int
	header http.Header
	body   []byte
}

// NewHandler serves one vendor. cfg is read per request, so key changes hot-reload.
func NewHandler(v *Vendor, cfg func() *config.Config) gin.HandlerFunc {
	return func(c *gin.Context) {
		current := cfg()
		if current == nil {
			errorJSON(c, http.StatusServiceUnavailable, "proxx: configuration unavailable")
			return
		}
		settings := v.settings(current)
		keys := make([]string, 0, len(settings.APIKeys))
		for _, k := range settings.APIKeys {
			if k = strings.TrimSpace(k); k != "" {
				keys = append(keys, k)
			}
		}
		if len(keys) == 0 {
			errorJSON(c, http.StatusServiceUnavailable, fmt.Sprintf("proxx: no %s API keys configured (proxx.decisions.%s.api-keys)", v.Name, v.Name))
			return
		}
		body, err := io.ReadAll(io.LimitReader(c.Request.Body, maxBodyBytes+1))
		if err != nil {
			errorJSON(c, http.StatusBadRequest, "proxx: failed to read request body")
			return
		}
		if int64(len(body)) > maxBodyBytes {
			errorJSON(c, http.StatusRequestEntityTooLarge, fmt.Sprintf("proxx: request body exceeds %d bytes", maxBodyBytes))
			return
		}
		contentType := c.GetHeader("Content-Type")
		if contentType == "" {
			contentType = "application/json"
		}
		alias := requestModel(contentType, body)

		order, earliest := pools.order(v.Name, keys)
		if len(order) == 0 {
			wait := int(math.Ceil(earliest.Sub(nowFunc()).Seconds()))
			if wait < 1 {
				wait = 1
			}
			c.Header("Retry-After", strconv.Itoa(wait))
			errorJSON(c, http.StatusTooManyRequests, fmt.Sprintf("proxx: every %s API key is cooling down", v.Name))
			return
		}

		baseURL := strings.TrimSpace(settings.BaseURL)
		if baseURL == "" {
			baseURL = v.DefaultBaseURL
		}
		upstreamURL := strings.TrimRight(baseURL, "/") + v.UpstreamPath
		proxyURL := strings.TrimSpace(settings.ProxyURL)
		if proxyURL == "" {
			proxyURL = current.ProxyURL
		}
		client := httpClient(proxyURL)
		ctx := context.WithValue(c.Request.Context(), "gin", c)

		var last *upstreamResult
		var lastErr error
		for _, idx := range order {
			key := keys[idx]
			res, errAttempt := v.attempt(ctx, current, client, upstreamURL, contentType, body, key, idx, alias, c.Request.Header)
			if errAttempt != nil {
				if ctx.Err() != nil {
					return
				}
				lastErr = errAttempt
				continue
			}
			last = res
			switch {
			case res.status == http.StatusTooManyRequests:
				pools.cool(v.Name, key, nowFunc().Add(retryAfter(res.header)))
			case res.status == http.StatusUnauthorized || res.status == http.StatusForbidden:
				pools.cool(v.Name, key, nowFunc().Add(authFailureCooldown))
			case res.status == http.StatusInternalServerError || res.status == http.StatusBadGateway ||
				res.status == http.StatusServiceUnavailable || res.status == http.StatusGatewayTimeout:
			default:
				writeResult(c, res)
				return
			}
		}
		if last != nil {
			writeResult(c, last)
			return
		}
		msg := "proxx: upstream request failed"
		if lastErr != nil {
			msg = "proxx: upstream request failed: " + lastErr.Error()
		}
		errorJSON(c, http.StatusBadGateway, msg)
	}
}

func writeResult(c *gin.Context, res *upstreamResult) {
	for name, values := range res.header {
		if skippedResponseHeaders[http.CanonicalHeaderKey(name)] {
			continue
		}
		for _, value := range values {
			c.Writer.Header().Add(name, value)
		}
	}
	c.Status(res.status)
	if _, err := c.Writer.Write(res.body); err != nil {
		log.WithError(err).Debug("proxx decisions: write response")
	}
}

// attempt performs one upstream call, logs it and publishes its usage record.
func (v *Vendor) attempt(ctx context.Context, cfg *config.Config, client *http.Client, upstreamURL, contentType string,
	body []byte, key string, idx int, alias string, clientHeaders http.Header) (*upstreamResult, error) {
	headers := v.upstreamHeaders(clientHeaders, contentType, key)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, upstreamURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header = headers
	authID := "decisions:" + v.Name + ":" + keyHash(key)
	helps.RecordAPIRequest(ctx, cfg, helps.UpstreamRequestLog{
		URL:       upstreamURL,
		Method:    http.MethodPost,
		Headers:   headers.Clone(),
		Body:      body,
		Provider:  v.Provider,
		AuthID:    authID,
		AuthLabel: fmt.Sprintf("%s#%d", v.Name, idx+1),
		AuthType:  "api_key",
		AuthValue: key,
	})
	record := usage.Record{
		Provider:     v.Provider,
		BaseURL:      strings.TrimSuffix(upstreamURL, v.UpstreamPath),
		ExecutorType: ExecutorType,
		Model:        alias,
		Alias:        alias,
		AuthID:       authID,
		AuthType:     "api_key",
		RequestedAt:  nowFunc(),
	}
	started := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		helps.RecordAPIResponseError(ctx, cfg, err)
		record.Latency = time.Since(started)
		record.Failed = true
		record.Fail = usage.Failure{StatusCode: http.StatusBadGateway, Body: err.Error()}
		publishFn(ctx, record)
		return nil, err
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.WithError(errClose).Debug("proxx decisions: close upstream body")
		}
	}()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes+1))
	if err == nil && int64(len(respBody)) > maxBodyBytes {
		err = errors.New("upstream response too large")
	}
	helps.RecordAPIResponseMetadata(ctx, cfg, resp.StatusCode, resp.Header.Clone())
	if err != nil {
		helps.RecordAPIResponseError(ctx, cfg, err)
		record.Latency = time.Since(started)
		record.Failed = true
		record.Fail = usage.Failure{StatusCode: http.StatusBadGateway, Body: err.Error()}
		publishFn(ctx, record)
		return nil, err
	}
	helps.AppendAPIResponseChunk(ctx, cfg, respBody)
	record.Latency = time.Since(started)
	record.ResponseHeaders = resp.Header.Clone()
	if resp.StatusCode >= http.StatusBadRequest {
		record.Failed = true
		record.Fail = usage.Failure{StatusCode: resp.StatusCode, Body: string(respBody)}
	} else {
		served, detail := parseResponse(respBody)
		if served != "" {
			record.Model = served
			record.ResponseModel = served
		}
		record.Detail = detail
	}
	publishFn(ctx, record)
	return &upstreamResult{status: resp.StatusCode, header: resp.Header.Clone(), body: respBody}, nil
}
