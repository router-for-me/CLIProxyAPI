package helps

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/MoeForge/moego/ojson"
)

const (
	antigravity429RefusalBaseBlock    = 2 * time.Minute
	antigravity429RefusalMaxBlock     = 30 * time.Minute
	antigravity429RefusalStrikeWindow = 1 * time.Hour
	antigravity429RefusalMaxEntries   = 500
)

type antigravity429RefusalEntry struct {
	blockedUntil time.Time
	strikes      int
	lastUpdated  time.Time
}

// Antigravity429RefusalError represents a locally synthesized 429 response for a
// request whose exact content was recently refused by Antigravity. It implements
// StatusError and RequestScopedError so auth managers do not retry across
// credentials or cool down the account.
type Antigravity429RefusalError struct {
	Code       int
	Msg        string
	RetryDelay *time.Duration
}

func (e *Antigravity429RefusalError) Error() string {
	return e.Msg
}

func (e *Antigravity429RefusalError) StatusCode() int {
	return e.Code
}

func (e *Antigravity429RefusalError) RetryAfter() *time.Duration {
	return e.RetryDelay
}

func (e *Antigravity429RefusalError) IsRequestScoped() bool {
	return true
}

// Antigravity429Guard caches content-level 429 refusals per (auth, model, request content)
// to prevent hammering Antigravity with identical requests that lack a quota/rate-limit reason.
type Antigravity429Guard struct {
	mu      sync.Mutex
	entries map[string]antigravity429RefusalEntry
	nowFunc func() time.Time
}

// NewAntigravity429Guard creates a new 429 refusal guard with an optional clock function.
func NewAntigravity429Guard(nowFunc func() time.Time) *Antigravity429Guard {
	if nowFunc == nil {
		nowFunc = time.Now
	}
	return &Antigravity429Guard{
		entries: make(map[string]antigravity429RefusalEntry),
		nowFunc: nowFunc,
	}
}

// DefaultAntigravity429Guard is the shared process-wide guard used by AntigravityExecutor.
var DefaultAntigravity429Guard = NewAntigravity429Guard(nil)

// FingerprintAntigravityRequest computes a stable SHA-256 key over (authID, model)
// and the semantic request fields (systemInstruction, contents, tools, toolConfig,
// generationConfig), ignoring per-call IDs like requestId, sessionId, and project.
func FingerprintAntigravityRequest(authID, model string, payload []byte) string {
	h := sha256.New()
	writePart := func(label string, val []byte) {
		_, _ = h.Write([]byte(label))
		_, _ = h.Write([]byte{':'})
		_, _ = h.Write(val)
		_, _ = h.Write([]byte{0})
	}

	writePart("auth", []byte(strings.TrimSpace(authID)))
	writePart("model", []byte(strings.TrimSpace(model)))

	for _, field := range []string{
		"enabledCreditTypes",
		"request.systemInstruction",
		"request.contents",
		"request.tools",
		"request.toolConfig",
		"request.generationConfig",
	} {
		res := ojson.GetBytesNoCopy(payload, field)
		if res.Exists() {
			writePart(field, []byte(res.Raw))
		} else {
			writePart(field, nil)
		}
	}

	return hex.EncodeToString(h.Sum(nil))
}

// Check returns the remaining block duration and strike count if key is currently blocked.
func (g *Antigravity429Guard) Check(key string) (time.Duration, int, bool) {
	if g == nil || key == "" {
		return 0, 0, false
	}
	now := g.nowFunc()
	g.mu.Lock()
	defer g.mu.Unlock()

	entry, ok := g.entries[key]
	if !ok {
		return 0, 0, false
	}
	if !entry.blockedUntil.After(now) {
		return 0, 0, false
	}
	return entry.blockedUntil.Sub(now), entry.strikes, true
}

// Remember records a content-level 429 refusal for key and returns the applied
// block duration and cumulative strike count. The first refusal (strike 1) is
// recorded without blocking so a single retry can still proceed; the second and
// subsequent identical refusals within the strike window arm exponential backoff.
func (g *Antigravity429Guard) Remember(key string, retryAfter *time.Duration) (time.Duration, int) {
	if g == nil || key == "" {
		return 0, 0
	}
	now := g.nowFunc()
	g.mu.Lock()
	defer g.mu.Unlock()

	strikes := 1
	if prev, ok := g.entries[key]; ok && !prev.lastUpdated.Add(antigravity429RefusalStrikeWindow).Before(now) {
		strikes = prev.strikes + 1
	}

	var blockDuration time.Duration
	var blockedUntil time.Time
	if strikes >= 2 {
		if retryAfter != nil && *retryAfter > 0 {
			blockDuration = *retryAfter
			if blockDuration > antigravity429RefusalMaxBlock {
				blockDuration = antigravity429RefusalMaxBlock
			}
		} else {
			blockDuration = antigravity429RefusalBaseBlock << min(strikes-2, 4)
			if blockDuration > antigravity429RefusalMaxBlock {
				blockDuration = antigravity429RefusalMaxBlock
			}
		}
		blockedUntil = now.Add(blockDuration)
	}

	g.entries[key] = antigravity429RefusalEntry{
		blockedUntil: blockedUntil,
		strikes:      strikes,
		lastUpdated:  now,
	}
	g.pruneLocked(now)
	return blockDuration, strikes
}

func (g *Antigravity429Guard) pruneLocked(now time.Time) {
	for k, entry := range g.entries {
		ref := entry.blockedUntil
		if entry.lastUpdated.After(ref) {
			ref = entry.lastUpdated
		}
		if ref.Add(antigravity429RefusalStrikeWindow).Before(now) {
			delete(g.entries, k)
		}
	}
	for len(g.entries) > antigravity429RefusalMaxEntries {
		var oldestKey string
		var oldestTime time.Time
		first := true
		for k, entry := range g.entries {
			if first || entry.lastUpdated.Before(oldestTime) {
				oldestKey = k
				oldestTime = entry.lastUpdated
				first = false
			}
		}
		if oldestKey == "" {
			break
		}
		delete(g.entries, oldestKey)
	}
}

// BuildAntigravityRefusalReplayError constructs a local 429 RequestScopedError for a blocked re-send.
func BuildAntigravityRefusalReplayError(remaining time.Duration, strikes int) error {
	seconds := max(1, int((remaining+time.Second-1)/time.Second))
	retryDelay := time.Duration(seconds) * time.Second
	msg := fmt.Sprintf(
		"Antigravity refused this exact request with 429 RESOURCE_EXHAUSTED; suppressed locally for %ds (repeat %d) to avoid hammering upstream",
		seconds, strikes,
	)
	return &Antigravity429RefusalError{
		Code:       http.StatusTooManyRequests,
		Msg:        msg,
		RetryDelay: &retryDelay,
	}
}
