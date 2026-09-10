package helps

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"golang.org/x/time/rate"
)

const rateLimitWindowFallback = time.Minute

type providerRateState struct {
	limiter      *rate.Limiter
	rpm          int
	blockedUntil time.Time
}

// ProviderRateLimitRegistry owns limiters for one executor instance, allowing
// configuration reloads to retire stale provider state with the old executor.
type ProviderRateLimitRegistry struct {
	mu     sync.Mutex
	states map[string]*providerRateState
}

func NewProviderRateLimitRegistry() *ProviderRateLimitRegistry {
	return &ProviderRateLimitRegistry{states: make(map[string]*providerRateState)}
}

func providerRateKey(compat *config.OpenAICompatibility, providerKey, authID string) string {
	if compat == nil || compat.RequestsPerMinute <= 0 {
		return ""
	}
	key := strings.TrimSpace(providerKey)
	if key == "" {
		return ""
	}
	if authID != "" {
		key += "|" + authID
	}
	return key
}

// Wait blocks until the configured provider rate allows another request or the
// request context is canceled. It also honors a previous upstream Retry-After.
func (r *ProviderRateLimitRegistry) Wait(ctx context.Context, compat *config.OpenAICompatibility, providerKey, authID string) error {
	key := providerRateKey(compat, providerKey, authID)
	if key == "" || r == nil {
		return nil
	}
	rpm := compat.RequestsPerMinute

	r.mu.Lock()
	state := r.states[key]
	if state == nil || state.rpm != rpm {
		state = &providerRateState{
			limiter: rate.NewLimiter(rate.Every(time.Minute/time.Duration(rpm)), 1),
			rpm:     rpm,
		}
		r.states[key] = state
	}
	blockedUntil := state.blockedUntil
	limiter := state.limiter
	r.mu.Unlock()

	for {
		if errWait := limiter.Wait(ctx); errWait != nil {
			return errWait
		}

		// A queued caller may have started waiting before another request received
		// a 429. Re-read the shared pause after every limiter wake so already queued
		// calls also honor newly published Retry-After windows.
		r.mu.Lock()
		blockedUntil = state.blockedUntil
		r.mu.Unlock()
		pause := time.Until(blockedUntil)
		if pause <= 0 {
			return nil
		}
		timer := time.NewTimer(pause)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		}
	}
}

// NoteLimited pauses subsequent requests after an upstream 429 response.
func (r *ProviderRateLimitRegistry) NoteLimited(compat *config.OpenAICompatibility, providerKey, authID string, header http.Header) {
	key := providerRateKey(compat, providerKey, authID)
	if key == "" || r == nil {
		return
	}
	pause, validRetryAfter := parseRetryAfter(header)
	if !validRetryAfter {
		pause = rateLimitWindowFallback
	}
	until := time.Now().Add(pause)

	r.mu.Lock()
	state := r.states[key]
	if state == nil || state.rpm != compat.RequestsPerMinute {
		state = &providerRateState{
			limiter: rate.NewLimiter(rate.Every(time.Minute/time.Duration(compat.RequestsPerMinute)), 1),
			rpm:     compat.RequestsPerMinute,
		}
		r.states[key] = state
	}
	if until.After(state.blockedUntil) {
		state.blockedUntil = until
	}
	r.mu.Unlock()
}

func parseRetryAfter(header http.Header) (time.Duration, bool) {
	if header == nil {
		return 0, false
	}
	raw := strings.TrimSpace(header.Get("Retry-After"))
	if raw == "" {
		return 0, false
	}
	if seconds, errParse := strconv.Atoi(raw); errParse == nil {
		if seconds >= 0 {
			return time.Duration(seconds) * time.Second, true
		}
		return 0, false
	}
	if when, errParse := http.ParseTime(raw); errParse == nil {
		delay := time.Until(when)
		if delay < 0 {
			delay = 0
		}
		return delay, true
	}
	return 0, false
}
