// Package middleware provides HTTP middleware components for the CLI Proxy API server.
// This file contains the rate limiting middleware that protects upstream services
// from client floods by limiting requests per IP, auth key, and model.
package middleware

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

// LimiterStore defines the interface for rate limiter storage.
// This allows for future implementations using Redis or other distributed stores.
type LimiterStore interface {
	// TryConsume attempts to consume a token from the bucket identified by key.
	// Returns true if successful, false if rate limited.
	// Also returns the time until the next token is available.
	TryConsume(key string, capacity int, refillPerSecond float64) (allowed bool, retryAfter time.Duration)
}

// TokenBucket represents a single token bucket for rate limiting.
type TokenBucket struct {
	tokens     float64
	lastRefill time.Time
	mu         sync.Mutex
}

// InMemoryLimiterStore implements LimiterStore using in-memory token buckets.
// This is suitable for single-instance deployments.
type InMemoryLimiterStore struct {
	buckets sync.Map // map[string]*TokenBucket
}

// NewInMemoryLimiterStore creates a new in-memory rate limiter store.
func NewInMemoryLimiterStore() *InMemoryLimiterStore {
	return &InMemoryLimiterStore{}
}

// TryConsume attempts to consume a token from the bucket.
func (s *InMemoryLimiterStore) TryConsume(key string, capacity int, refillPerSecond float64) (bool, time.Duration) {
	if capacity <= 0 || refillPerSecond <= 0 {
		return true, 0
	}

	now := time.Now()
	bucketI, _ := s.buckets.LoadOrStore(key, &TokenBucket{
		tokens:     float64(capacity),
		lastRefill: now,
	})
	bucket := bucketI.(*TokenBucket)

	bucket.mu.Lock()
	defer bucket.mu.Unlock()

	elapsed := now.Sub(bucket.lastRefill).Seconds()
	bucket.tokens += elapsed * refillPerSecond
	if bucket.tokens > float64(capacity) {
		bucket.tokens = float64(capacity)
	}
	bucket.lastRefill = now

	if bucket.tokens >= 1 {
		bucket.tokens--
		return true, 0
	}

	tokensNeeded := 1 - bucket.tokens
	retryAfter := time.Duration(tokensNeeded/refillPerSecond*1000) * time.Millisecond
	return false, retryAfter
}

// RateLimitDimension indicates which rate limit dimension was exceeded.
type RateLimitDimension string

const (
	DimensionIP    RateLimitDimension = "ip"
	DimensionAuth  RateLimitDimension = "auth"
	DimensionModel RateLimitDimension = "model"
)

// RateLimitError represents a rate limit exceeded error response.
type RateLimitError struct {
	Error      string             `json:"error"`
	Message    string             `json:"message"`
	Dimension  RateLimitDimension `json:"dimension"`
	RetryAfter float64            `json:"retry_after_seconds"`
}

// RateLimiter holds the rate limiter configuration and store.
type RateLimiter struct {
	store   LimiterStore
	cfg     *config.RateLimitConfig
	enabled bool
	mu      sync.RWMutex
}

// NewRateLimiter creates a new rate limiter with the given configuration.
func NewRateLimiter(cfg *config.RateLimitConfig) *RateLimiter {
	rl := &RateLimiter{
		store: NewInMemoryLimiterStore(),
	}
	if cfg != nil {
		rl.cfg = cfg
		rl.enabled = cfg.Enabled
	}
	return rl
}

// UpdateConfig updates the rate limiter configuration.
func (rl *RateLimiter) UpdateConfig(cfg *config.RateLimitConfig) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	if cfg != nil {
		rl.cfg = cfg
		rl.enabled = cfg.Enabled
	} else {
		rl.enabled = false
	}
}

// IsEnabled returns whether rate limiting is enabled.
func (rl *RateLimiter) IsEnabled() bool {
	rl.mu.RLock()
	defer rl.mu.RUnlock()
	return rl.enabled
}

// getConfig returns a copy of the current configuration.
func (rl *RateLimiter) getConfig() *config.RateLimitConfig {
	rl.mu.RLock()
	defer rl.mu.RUnlock()
	return rl.cfg
}

// RateLimitMiddleware creates a Gin middleware that enforces rate limits.
// It checks per-IP, per-auth, and per-model limits based on configuration.
func RateLimitMiddleware(rl *RateLimiter) gin.HandlerFunc {
	return func(c *gin.Context) {
		if rl == nil || !rl.IsEnabled() {
			c.Next()
			return
		}

		cfg := rl.getConfig()
		if cfg == nil {
			c.Next()
			return
		}

		ip := c.ClientIP()
		authID := extractAuthID(c)
		model := extractModelFromRequest(c)

		msgCfg := cfg.Messages

		if msgCfg.PerIP.Capacity > 0 && msgCfg.PerIP.RefillPerSecond > 0 {
			key := "ip:" + ip
			allowed, retryAfter := rl.store.TryConsume(key, msgCfg.PerIP.Capacity, msgCfg.PerIP.RefillPerSecond)
			if !allowed {
				logRateLimitBlock(ip, authID, model, DimensionIP)
				rejectWithRateLimit(c, DimensionIP, retryAfter)
				return
			}
		}

		if authID != "" && msgCfg.PerAuth.Capacity > 0 && msgCfg.PerAuth.RefillPerSecond > 0 {
			key := "auth:" + authID
			allowed, retryAfter := rl.store.TryConsume(key, msgCfg.PerAuth.Capacity, msgCfg.PerAuth.RefillPerSecond)
			if !allowed {
				logRateLimitBlock(ip, authID, model, DimensionAuth)
				rejectWithRateLimit(c, DimensionAuth, retryAfter)
				return
			}
		}

		if model != "" && msgCfg.PerModel.Capacity > 0 && msgCfg.PerModel.RefillPerSecond > 0 {
			key := "model:" + model
			allowed, retryAfter := rl.store.TryConsume(key, msgCfg.PerModel.Capacity, msgCfg.PerModel.RefillPerSecond)
			if !allowed {
				logRateLimitBlock(ip, authID, model, DimensionModel)
				rejectWithRateLimit(c, DimensionModel, retryAfter)
				return
			}
		}

		c.Next()
	}
}

// extractAuthID extracts the auth identifier from the Gin context.
// It looks for auth_id set by AuthMiddleware, or falls back to API key hash.
func extractAuthID(c *gin.Context) string {
	if authID, exists := c.Get("auth_id"); exists {
		if id, ok := authID.(string); ok && id != "" {
			return id
		}
	}

	if authKey := c.GetHeader("Authorization"); authKey != "" {
		authKey = strings.TrimPrefix(authKey, "Bearer ")
		if len(authKey) > 8 {
			return "key:" + authKey[:8]
		}
		return "key:" + authKey
	}

	if apiKey := c.GetHeader("x-api-key"); apiKey != "" {
		if len(apiKey) > 8 {
			return "key:" + apiKey[:8]
		}
		return "key:" + apiKey
	}

	return ""
}

// extractModelFromRequest extracts the model name from the request body.
// For Claude/Anthropic format, it looks for the "model" field.
func extractModelFromRequest(c *gin.Context) string {
	if c.Request.Body == nil {
		return "unknown"
	}

	bodyBytes, exists := c.Get("request_body")
	if !exists {
		return "unknown"
	}

	body, ok := bodyBytes.([]byte)
	if !ok || len(body) == 0 {
		return "unknown"
	}

	model := gjson.GetBytes(body, "model").String()
	if model == "" {
		return "unknown"
	}
	return model
}

// rejectWithRateLimit sends a 429 response with rate limit details.
func rejectWithRateLimit(c *gin.Context, dimension RateLimitDimension, retryAfter time.Duration) {
	retrySeconds := retryAfter.Seconds()
	if retrySeconds < 1 {
		retrySeconds = 1
	}

	c.Header("Retry-After", formatRetryAfter(retryAfter))

	errResp := RateLimitError{
		Error:      "rate_limit_exceeded",
		Message:    "Rate limit exceeded for " + string(dimension),
		Dimension:  dimension,
		RetryAfter: retrySeconds,
	}

	c.AbortWithStatusJSON(http.StatusTooManyRequests, errResp)
}

// formatRetryAfter formats the retry-after duration as seconds.
func formatRetryAfter(d time.Duration) string {
	secs := int(d.Seconds())
	if secs < 1 {
		secs = 1
	}
	return strconv.Itoa(secs)
}

// logRateLimitBlock logs a rate limit block event.
func logRateLimitBlock(ip, authID, model string, dimension RateLimitDimension) {
	log.WithFields(log.Fields{
		"ip":        ip,
		"auth_id":   authID,
		"model":     model,
		"dimension": string(dimension),
	}).Warn("rate limit exceeded")
}

// RequestBodyCaptureMiddleware captures the request body for later use by rate limiting.
// This must be applied before RateLimitMiddleware to extract the model from the body.
func RequestBodyCaptureMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Body == nil {
			c.Next()
			return
		}

		if c.Request.Method != http.MethodPost {
			c.Next()
			return
		}

		body, err := c.GetRawData()
		if err != nil {
			log.WithError(err).Debug("failed to read request body for rate limiting")
			c.Next()
			return
		}

		c.Set("request_body", body)
		c.Request.Body = nopCloser{strings.NewReader(string(body))}
		c.Next()
	}
}

type nopCloser struct {
	*strings.Reader
}

func (nopCloser) Close() error { return nil }

// MarshalJSON implements json.Marshaler for RateLimitError.
func (e RateLimitError) MarshalJSON() ([]byte, error) {
	type Alias RateLimitError
	return json.Marshal(struct {
		Type string `json:"type"`
		Alias
	}{
		Type:  "error",
		Alias: Alias(e),
	})
}
