package ratelimit

import (
	"fmt"
	"math"
	"math/rand"
	"net/http"
	"strconv"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

const (
	DefaultBaseBackoff = 1 * time.Second
	DefaultMaxBackoff  = 60 * time.Second
	DefaultMaxJitter   = 1 * time.Second
)

type Status string

const (
	StatusHealthy     Status = "healthy"
	StatusRateLimited Status = "rate_limited"
	StatusUnknown     Status = "unknown"
)

type ProviderStatus struct {
	ProviderID  string    `json:"provider_id"`
	Status      Status    `json:"status"`
	RateLimited bool      `json:"rate_limited"`
	RetryAfter  time.Time `json:"retry_after,omitempty"`
	LastUpdated time.Time `json:"last_updated"`
}

type RateLimitHandler struct {
	providerStatuses map[string]*ProviderStatus
	mu               sync.RWMutex
	baseBackoff      time.Duration
	maxBackoff       time.Duration
	maxJitter        time.Duration
}

func NewRateLimitHandler() *RateLimitHandler {
	return &RateLimitHandler{
		providerStatuses: make(map[string]*ProviderStatus),
		baseBackoff:      DefaultBaseBackoff,
		maxBackoff:       DefaultMaxBackoff,
		maxJitter:        DefaultMaxJitter,
	}
}

func (h *RateLimitHandler) HandleRateLimit(resp *http.Response) (retryAfter time.Duration, shouldRetry bool) {
	if resp.StatusCode != http.StatusTooManyRequests {
		return 0, false
	}

	retryAfterHeader := resp.Header.Get("Retry-After")
	if retryAfterHeader != "" {
		if parsed := h.ParseRetryAfter(retryAfterHeader); parsed > 0 {
			return parsed, true
		}
	}

	return h.CalculateBackoff(0), true
}

func (h *RateLimitHandler) HandleRateLimitForProvider(providerID string, resp *http.Response) (retryAfter time.Duration, shouldRetry bool) {
	retryAfter, shouldRetry = h.HandleRateLimit(resp)
	if !shouldRetry {
		return 0, false
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	status := &ProviderStatus{
		ProviderID:  providerID,
		Status:      StatusRateLimited,
		RateLimited: true,
		RetryAfter:  time.Now().Add(retryAfter),
		LastUpdated: time.Now(),
	}
	h.providerStatuses[providerID] = status

	log.Warnf("ratelimit: provider %s rate limited, retry after %v", providerID, retryAfter)
	return retryAfter, true
}

func (h *RateLimitHandler) HandleRateLimitWithAttempt(resp *http.Response, attempt int) (retryAfter time.Duration, shouldRetry bool) {
	if resp.StatusCode != http.StatusTooManyRequests {
		return 0, false
	}

	retryAfterHeader := resp.Header.Get("Retry-After")
	if retryAfterHeader != "" {
		if parsed := h.ParseRetryAfter(retryAfterHeader); parsed > 0 {
			return parsed, true
		}
	}

	return h.CalculateBackoff(attempt), true
}

func (h *RateLimitHandler) CalculateBackoff(attempt int) time.Duration {
	backoffSeconds := math.Min(
		float64(h.baseBackoff.Seconds())*math.Pow(2, float64(attempt)),
		h.maxBackoff.Seconds(),
	)

	backoff := time.Duration(backoffSeconds) * time.Second

	jitter := time.Duration(rand.Float64() * float64(h.maxJitter))
	backoff += jitter

	if backoff > h.maxBackoff+h.maxJitter {
		backoff = h.maxBackoff + jitter
	}

	return backoff
}

func (h *RateLimitHandler) ParseRetryAfter(header string) time.Duration {
	if header == "" {
		return 0
	}

	if seconds, err := strconv.Atoi(header); err == nil {
		if seconds < 0 {
			return 0
		}
		return time.Duration(seconds) * time.Second
	}

	if t, err := http.ParseTime(header); err == nil {
		duration := time.Until(t)
		if duration > 0 {
			return duration
		}
	}

	return 0
}

func (h *RateLimitHandler) FormatUserMessage(retryAfter time.Duration) string {
	seconds := int(retryAfter.Seconds())
	return fmt.Sprintf("Provider rate limited. Retrying in %ds...", seconds)
}

func (h *RateLimitHandler) GetProviderStatus(providerID string) ProviderStatus {
	h.mu.RLock()
	defer h.mu.RUnlock()

	if status, exists := h.providerStatuses[providerID]; exists {
		if status.RateLimited && time.Now().After(status.RetryAfter) {
			return ProviderStatus{
				ProviderID:  providerID,
				Status:      StatusHealthy,
				RateLimited: false,
				LastUpdated: status.LastUpdated,
			}
		}
		return *status
	}

	return ProviderStatus{
		ProviderID:  providerID,
		Status:      StatusUnknown,
		RateLimited: false,
	}
}

func (h *RateLimitHandler) GetAllProviderStatuses() map[string]ProviderStatus {
	h.mu.RLock()
	defer h.mu.RUnlock()

	result := make(map[string]ProviderStatus, len(h.providerStatuses))
	for id, status := range h.providerStatuses {
		if status.RateLimited && time.Now().After(status.RetryAfter) {
			result[id] = ProviderStatus{
				ProviderID:  id,
				Status:      StatusHealthy,
				RateLimited: false,
				LastUpdated: status.LastUpdated,
			}
		} else {
			result[id] = *status
		}
	}
	return result
}

func (h *RateLimitHandler) ClearRateLimit(providerID string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if status, exists := h.providerStatuses[providerID]; exists {
		status.Status = StatusHealthy
		status.RateLimited = false
		status.RetryAfter = time.Time{}
		status.LastUpdated = time.Now()
		log.Debugf("ratelimit: cleared rate limit for provider %s", providerID)
	} else {
		h.providerStatuses[providerID] = &ProviderStatus{
			ProviderID:  providerID,
			Status:      StatusHealthy,
			RateLimited: false,
			LastUpdated: time.Now(),
		}
	}
}

func (h *RateLimitHandler) IsRateLimited(providerID string) bool {
	status := h.GetProviderStatus(providerID)
	return status.RateLimited
}

func (h *RateLimitHandler) GetRetryAfter(providerID string) time.Duration {
	h.mu.RLock()
	defer h.mu.RUnlock()

	if status, exists := h.providerStatuses[providerID]; exists && status.RateLimited {
		remaining := time.Until(status.RetryAfter)
		if remaining > 0 {
			return remaining
		}
	}
	return 0
}
