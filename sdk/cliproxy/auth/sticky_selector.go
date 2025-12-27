package auth

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tidwall/gjson"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

const stickySessionTTL = time.Hour

var claudeSessionRegex = regexp.MustCompile(`session_([a-f0-9-]{36})`)

type stickyBinding struct {
	authID    string
	expiresAt time.Time
	lastUsedAt time.Time
}

const (
	defaultAuthPriority = 50
	stickyGCInterval    = 10 * time.Minute
	stickyGCMinEntries  = 1024
)

// StickySelector provides sticky-session routing using a session key extracted from
// request headers and/or the original request payload.
//
// It falls back to round-robin when no session key is available.
type StickySelector struct {
	mu       sync.Mutex
	bindings map[string]stickyBinding
	lastGC   time.Time
	rr       RoundRobinSelector
}

func priorityFromAny(v any) (int, bool) {
	switch val := v.(type) {
	case int:
		return val, true
	case int64:
		return int(val), true
	case float64:
		return int(val), true
	case float32:
		return int(val), true
	case string:
		val = strings.TrimSpace(val)
		if val == "" {
			return 0, false
		}
		if parsed, err := strconv.Atoi(val); err == nil {
			return parsed, true
		}
		return 0, false
	default:
		return 0, false
	}
}

func authPriority(a *Auth) int {
	if a == nil {
		return defaultAuthPriority
	}
	if a.Metadata != nil {
		if v, ok := a.Metadata["priority"]; ok {
			if p, ok2 := priorityFromAny(v); ok2 {
				return p
			}
		}
	}
	if a.Attributes != nil {
		if v := strings.TrimSpace(a.Attributes["priority"]); v != "" {
			if p, err := strconv.Atoi(v); err == nil {
				return p
			}
		}
	}
	return defaultAuthPriority
}

func (s *StickySelector) gcLocked(now time.Time) {
	if s == nil || len(s.bindings) == 0 {
		s.lastGC = now
		return
	}
	for k, v := range s.bindings {
		if v.authID == "" || now.After(v.expiresAt) {
			delete(s.bindings, k)
		}
	}
	s.lastGC = now
}

type SessionBindingStatus struct {
	AuthID       string    `json:"auth_id"`
	SessionCount int       `json:"session_count"`
	LastUsedAt   time.Time `json:"last_used_at"`
}

func (s *StickySelector) SessionBindingStatuses() []SessionBindingStatus {
	now := time.Now()

	s.mu.Lock()
	if s.bindings == nil || len(s.bindings) == 0 {
		s.mu.Unlock()
		return nil
	}
	if len(s.bindings) >= stickyGCMinEntries || s.lastGC.IsZero() || now.Sub(s.lastGC) >= stickyGCInterval {
		s.gcLocked(now)
	}

	stats := make(map[string]SessionBindingStatus, len(s.bindings))
	for _, binding := range s.bindings {
		if binding.authID == "" || now.After(binding.expiresAt) {
			continue
		}
		entry := stats[binding.authID]
		entry.AuthID = binding.authID
		entry.SessionCount++
		if entry.LastUsedAt.IsZero() || binding.lastUsedAt.After(entry.LastUsedAt) {
			entry.LastUsedAt = binding.lastUsedAt
		}
		stats[binding.authID] = entry
	}
	s.mu.Unlock()

	out := make([]SessionBindingStatus, 0, len(stats))
	for _, v := range stats {
		out = append(out, v)
	}
	return out
}

func extractBearerToken(header string) string {
	header = strings.TrimSpace(header)
	if header == "" {
		return ""
	}
	parts := strings.SplitN(header, " ", 2)
	if len(parts) != 2 {
		return header
	}
	if strings.ToLower(parts[0]) != "bearer" {
		return header
	}
	return strings.TrimSpace(parts[1])
}

func stableHash(input string) string {
	input = strings.TrimSpace(input)
	if input == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(input))
	return hex.EncodeToString(sum[:16])
}

func extractStickySessionKey(opts cliproxyexecutor.Options) string {
	var headers http.Header
	if opts.Headers != nil {
		headers = opts.Headers
	}

	if headers != nil {
		if sid := strings.TrimSpace(headers.Get("session_id")); sid != "" {
			if hashed := stableHash(sid); hashed != "" {
				return "codex:" + hashed
			}
		}
	}

	if len(opts.OriginalRequest) > 0 {
		userID := strings.TrimSpace(gjson.GetBytes(opts.OriginalRequest, "metadata.user_id").String())
		if userID != "" {
			if match := claudeSessionRegex.FindStringSubmatch(strings.ToLower(userID)); len(match) == 2 {
				return "claude:" + match[1]
			}
		}
	}

	if headers != nil {
		if tok := extractBearerToken(headers.Get("authorization")); tok != "" {
			if hashed := stableHash(tok); hashed != "" {
				return "apikey:" + hashed
			}
		}
		if tok := strings.TrimSpace(headers.Get("x-api-key")); tok != "" {
			if hashed := stableHash(tok); hashed != "" {
				return "apikey:" + hashed
			}
		}
		if tok := strings.TrimSpace(headers.Get("x-goog-api-key")); tok != "" {
			if hashed := stableHash(tok); hashed != "" {
				return "apikey:" + hashed
			}
		}

		if ua := strings.TrimSpace(headers.Get("user-agent")); ua != "" {
			if hashed := stableHash(ua); hashed != "" {
				return "ua:" + hashed
			}
		}
	}

	return ""
}

func rendezvousScore(sessionKey, authID string) uint64 {
	h := sha256.New()
	_, _ = h.Write([]byte(sessionKey))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(authID))
	sum := h.Sum(nil)
	return binary.BigEndian.Uint64(sum[:8])
}

func pickRendezvous(sessionKey string, available []*Auth) *Auth {
	if sessionKey == "" || len(available) == 0 {
		return nil
	}
	var best *Auth
	var bestScore uint64
	for _, candidate := range available {
		if candidate == nil || candidate.ID == "" {
			continue
		}
		score := rendezvousScore(sessionKey, candidate.ID)
		if best == nil || score > bestScore || (score == bestScore && candidate.ID < best.ID) {
			best = candidate
			bestScore = score
		}
	}
	return best
}

func (s *StickySelector) Pick(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	_ = ctx
	now := time.Now()

	available, err := getAvailableAuths(auths, provider, model, now)
	if err != nil {
		return nil, err
	}

	sessionKey := extractStickySessionKey(opts)
	if sessionKey == "" {
		return s.rr.Pick(ctx, provider, model, opts, auths)
	}

	bindingKey := provider + ":" + sessionKey

	s.mu.Lock()
	if s.bindings == nil {
		s.bindings = make(map[string]stickyBinding)
	}
	if len(s.bindings) > 0 && (len(s.bindings) >= stickyGCMinEntries || s.lastGC.IsZero() || now.Sub(s.lastGC) >= stickyGCInterval) {
		s.gcLocked(now)
	}

	if existing, ok := s.bindings[bindingKey]; ok {
		if existing.authID != "" && now.Before(existing.expiresAt) {
			for _, candidate := range available {
				if candidate != nil && candidate.ID == existing.authID {
					s.bindings[bindingKey] = stickyBinding{
						authID:     candidate.ID,
						expiresAt:  now.Add(stickySessionTTL),
						lastUsedAt: now,
					}
					s.mu.Unlock()
					return candidate, nil
				}
			}
		} else {
			delete(s.bindings, bindingKey)
		}
	}

	minPriority := defaultAuthPriority
	for _, candidate := range available {
		p := authPriority(candidate)
		if p < minPriority {
			minPriority = p
		}
	}
	filtered := available
	if minPriority != defaultAuthPriority {
		filtered = make([]*Auth, 0, len(available))
		for _, candidate := range available {
			if authPriority(candidate) == minPriority {
				filtered = append(filtered, candidate)
			}
		}
	}

	selected := pickRendezvous(sessionKey, filtered)
	if selected == nil {
		s.mu.Unlock()
		return nil, &Error{Code: "auth_not_found", Message: "no auth available"}
	}
	s.bindings[bindingKey] = stickyBinding{
		authID:     selected.ID,
		expiresAt:  now.Add(stickySessionTTL),
		lastUsedAt: now,
	}
	s.mu.Unlock()
	return selected, nil
}
