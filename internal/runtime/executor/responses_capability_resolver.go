package executor

import (
	"strings"
	"sync"

	log "github.com/sirupsen/logrus"
)

// ResponsesCapabilityResolver caches per-auth runtime capability detection results
// for the OpenAI Responses API. It uses lazy detection: the first request to an
// unknown upstream attempts native /responses and falls back on capability errors.
type ResponsesCapabilityResolver struct {
	mu    sync.RWMutex
	cache map[string]ResponsesMode // key: auth.ID
}

// NewResponsesCapabilityResolver creates a new resolver with an empty cache.
func NewResponsesCapabilityResolver() *ResponsesCapabilityResolver {
	return &ResponsesCapabilityResolver{
		cache: make(map[string]ResponsesMode),
	}
}

// Resolve returns the cached ResponsesMode for the given auth ID.
// If no cached result exists, it returns ResponsesModeUnknown.
func (r *ResponsesCapabilityResolver) Resolve(authID string) ResponsesMode {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if m, ok := r.cache[authID]; ok {
		return m
	}
	return ResponsesModeUnknown
}

// Set stores the resolved ResponsesMode for the given auth ID.
func (r *ResponsesCapabilityResolver) Set(authID string, mode ResponsesMode) {
	r.mu.Lock()
	defer r.mu.Unlock()
	prev := r.cache[authID]
	r.cache[authID] = mode
	if prev != mode {
		log.Debugf("responses capability resolver: auth=%s mode changed %s -> %s", authID, prev, mode)
	}
}

// Invalidate removes the cached result for the given auth ID,
// forcing re-detection on the next request.
func (r *ResponsesCapabilityResolver) Invalidate(authID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.cache, authID)
}

// isCapabilityError determines whether an upstream error response indicates
// that the /responses endpoint is not supported (vs. a real service error
// like auth failure or rate limiting).
//
// The check uses OR logic:
//   - HTTP status code in (404, 405, 501) → endpoint does not exist
//   - Response body contains capability-denial keywords → the upstream
//     explicitly signals it cannot handle the Responses format
//
// This handles providers that return 500 + "convert_request_failed" (e.g., new-api/one-api
// middleware) as well as standard 404/405 from bare HTTP servers.
func isCapabilityError(statusCode int, body []byte) bool {
	// Status-code whitelist: these clearly indicate the endpoint is not implemented.
	if statusCode == 404 || statusCode == 405 || statusCode == 501 {
		return true
	}

	// Body-keyword check: some providers return 500 with an explicit "not supported" marker.
	if len(body) > 0 {
		bodyStr := strings.ToLower(string(body))
		capabilityKeywords := []string{
			"not implemented",
			"convert_request_failed",
			"endpoint not found",
		}
		for _, kw := range capabilityKeywords {
			if strings.Contains(bodyStr, kw) {
				return true
			}
		}
	}

	return false
}

// globalResponsesCapabilityResolver is the process-wide singleton resolver instance.
var globalResponsesCapabilityResolver = NewResponsesCapabilityResolver()
