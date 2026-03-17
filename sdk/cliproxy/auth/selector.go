package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"math/rand/v2"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/thinking"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

// RoundRobinSelector provides a simple provider scoped round-robin selection strategy.
type RoundRobinSelector struct {
	mu      sync.Mutex
	cursors map[string]int
	maxKeys int
}

// FillFirstSelector selects the first available credential (deterministic ordering).
// This "burns" one account before moving to the next, which can help stagger
// rolling-window subscription caps (e.g. chat message limits).
type FillFirstSelector struct{}

type blockReason int

const (
	blockReasonNone blockReason = iota
	blockReasonCooldown
	blockReasonDisabled
	blockReasonOther
)

type modelCooldownError struct {
	model    string
	resetIn  time.Duration
	provider string
}

func newModelCooldownError(model, provider string, resetIn time.Duration) *modelCooldownError {
	if resetIn < 0 {
		resetIn = 0
	}
	return &modelCooldownError{
		model:    model,
		provider: provider,
		resetIn:  resetIn,
	}
}

func (e *modelCooldownError) Error() string {
	modelName := e.model
	if modelName == "" {
		modelName = "requested model"
	}
	message := fmt.Sprintf("All credentials for model %s are cooling down", modelName)
	if e.provider != "" {
		message = fmt.Sprintf("%s via provider %s", message, e.provider)
	}
	resetSeconds := int(math.Ceil(e.resetIn.Seconds()))
	if resetSeconds < 0 {
		resetSeconds = 0
	}
	displayDuration := e.resetIn
	if displayDuration > 0 && displayDuration < time.Second {
		displayDuration = time.Second
	} else {
		displayDuration = displayDuration.Round(time.Second)
	}
	errorBody := map[string]any{
		"code":          "model_cooldown",
		"message":       message,
		"model":         e.model,
		"reset_time":    displayDuration.String(),
		"reset_seconds": resetSeconds,
	}
	if e.provider != "" {
		errorBody["provider"] = e.provider
	}
	payload := map[string]any{"error": errorBody}
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Sprintf(`{"error":{"code":"model_cooldown","message":"%s"}}`, message)
	}
	return string(data)
}

func (e *modelCooldownError) StatusCode() int {
	return http.StatusTooManyRequests
}

func (e *modelCooldownError) Headers() http.Header {
	headers := make(http.Header)
	headers.Set("Content-Type", "application/json")
	resetSeconds := int(math.Ceil(e.resetIn.Seconds()))
	if resetSeconds < 0 {
		resetSeconds = 0
	}
	headers.Set("Retry-After", strconv.Itoa(resetSeconds))
	return headers
}

func authPriority(auth *Auth) int {
	if auth == nil || auth.Attributes == nil {
		return 0
	}
	raw := strings.TrimSpace(auth.Attributes["priority"])
	if raw == "" {
		return 0
	}
	parsed, err := strconv.Atoi(raw)
	if err != nil {
		return 0
	}
	return parsed
}

func canonicalModelKey(model string) string {
	model = strings.TrimSpace(model)
	if model == "" {
		return ""
	}
	parsed := thinking.ParseSuffix(model)
	modelName := strings.TrimSpace(parsed.ModelName)
	if modelName == "" {
		return model
	}
	return modelName
}

func authWebsocketsEnabled(auth *Auth) bool {
	if auth == nil {
		return false
	}
	if len(auth.Attributes) > 0 {
		if raw := strings.TrimSpace(auth.Attributes["websockets"]); raw != "" {
			parsed, errParse := strconv.ParseBool(raw)
			if errParse == nil {
				return parsed
			}
		}
	}
	if len(auth.Metadata) == 0 {
		return false
	}
	raw, ok := auth.Metadata["websockets"]
	if !ok || raw == nil {
		return false
	}
	switch v := raw.(type) {
	case bool:
		return v
	case string:
		parsed, errParse := strconv.ParseBool(strings.TrimSpace(v))
		if errParse == nil {
			return parsed
		}
	default:
	}
	return false
}

func preferCodexWebsocketAuths(ctx context.Context, provider string, available []*Auth) []*Auth {
	if len(available) == 0 {
		return available
	}
	if !cliproxyexecutor.DownstreamWebsocket(ctx) {
		return available
	}
	if !strings.EqualFold(strings.TrimSpace(provider), "codex") {
		return available
	}

	wsEnabled := make([]*Auth, 0, len(available))
	for i := 0; i < len(available); i++ {
		candidate := available[i]
		if authWebsocketsEnabled(candidate) {
			wsEnabled = append(wsEnabled, candidate)
		}
	}
	if len(wsEnabled) > 0 {
		return wsEnabled
	}
	return available
}

func collectAvailableByPriority(auths []*Auth, model string, now time.Time) (available map[int][]*Auth, cooldownCount int, earliest time.Time) {
	available = make(map[int][]*Auth)
	for i := 0; i < len(auths); i++ {
		candidate := auths[i]
		blocked, reason, next := isAuthBlockedForModel(candidate, model, now)
		if !blocked {
			priority := authPriority(candidate)
			available[priority] = append(available[priority], candidate)
			continue
		}
		if reason == blockReasonCooldown {
			cooldownCount++
			if !next.IsZero() && (earliest.IsZero() || next.Before(earliest)) {
				earliest = next
			}
		}
	}
	return available, cooldownCount, earliest
}

func getAvailableAuths(auths []*Auth, provider, model string, now time.Time) ([]*Auth, error) {
	if len(auths) == 0 {
		return nil, &Error{Code: "auth_not_found", Message: "no auth candidates"}
	}

	availableByPriority, cooldownCount, earliest := collectAvailableByPriority(auths, model, now)
	if len(availableByPriority) == 0 {
		if cooldownCount == len(auths) && !earliest.IsZero() {
			providerForError := provider
			if providerForError == "mixed" {
				providerForError = ""
			}
			resetIn := earliest.Sub(now)
			if resetIn < 0 {
				resetIn = 0
			}
			return nil, newModelCooldownError(model, providerForError, resetIn)
		}
		return nil, &Error{Code: "auth_unavailable", Message: "no auth available"}
	}

	bestPriority := 0
	found := false
	for priority := range availableByPriority {
		if !found || priority > bestPriority {
			bestPriority = priority
			found = true
		}
	}

	available := availableByPriority[bestPriority]
	if len(available) > 1 {
		sort.Slice(available, func(i, j int) bool { return available[i].ID < available[j].ID })
	}
	return available, nil
}

// Pick selects the next available auth for the provider in a round-robin manner.
// For gemini-cli virtual auths (identified by the gemini_virtual_parent attribute),
// a two-level round-robin is used: first cycling across credential groups (parent
// accounts), then cycling within each group's project auths.
func (s *RoundRobinSelector) Pick(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	_ = opts
	now := time.Now()
	available, err := getAvailableAuths(auths, provider, model, now)
	if err != nil {
		return nil, err
	}
	available = preferCodexWebsocketAuths(ctx, provider, available)
	key := provider + ":" + canonicalModelKey(model)
	s.mu.Lock()
	if s.cursors == nil {
		s.cursors = make(map[string]int)
	}
	limit := s.maxKeys
	if limit <= 0 {
		limit = 4096
	}

	// Check if any available auth has gemini_virtual_parent attribute,
	// indicating gemini-cli virtual auths that should use credential-level polling.
	groups, parentOrder := groupByVirtualParent(available)
	if len(parentOrder) > 1 {
		// Two-level round-robin: first select a credential group, then pick within it.
		groupKey := key + "::group"
		s.ensureCursorKey(groupKey, limit)
		if _, exists := s.cursors[groupKey]; !exists {
			// Seed with a random initial offset so the starting credential is randomized.
			s.cursors[groupKey] = rand.IntN(len(parentOrder))
		}
		groupIndex := s.cursors[groupKey]
		if groupIndex >= 2_147_483_640 {
			groupIndex = 0
		}
		s.cursors[groupKey] = groupIndex + 1

		selectedParent := parentOrder[groupIndex%len(parentOrder)]
		group := groups[selectedParent]

		// Second level: round-robin within the selected credential group.
		innerKey := key + "::cred:" + selectedParent
		s.ensureCursorKey(innerKey, limit)
		innerIndex := s.cursors[innerKey]
		if innerIndex >= 2_147_483_640 {
			innerIndex = 0
		}
		s.cursors[innerKey] = innerIndex + 1
		s.mu.Unlock()
		return group[innerIndex%len(group)], nil
	}

	// Flat round-robin for non-grouped auths (original behavior).
	s.ensureCursorKey(key, limit)
	index := s.cursors[key]
	if index >= 2_147_483_640 {
		index = 0
	}
	s.cursors[key] = index + 1
	s.mu.Unlock()
	return available[index%len(available)], nil
}

// ensureCursorKey ensures the cursor map has capacity for the given key.
// Must be called with s.mu held.
func (s *RoundRobinSelector) ensureCursorKey(key string, limit int) {
	if _, ok := s.cursors[key]; !ok && len(s.cursors) >= limit {
		s.cursors = make(map[string]int)
	}
}

// groupByVirtualParent groups auths by their gemini_virtual_parent attribute.
// Returns a map of parentID -> auths and a sorted slice of parent IDs for stable iteration.
// Only auths with a non-empty gemini_virtual_parent are grouped; if any auth lacks
// this attribute, nil/nil is returned so the caller falls back to flat round-robin.
func groupByVirtualParent(auths []*Auth) (map[string][]*Auth, []string) {
	if len(auths) == 0 {
		return nil, nil
	}
	groups := make(map[string][]*Auth)
	for _, a := range auths {
		parent := ""
		if a.Attributes != nil {
			parent = strings.TrimSpace(a.Attributes["gemini_virtual_parent"])
		}
		if parent == "" {
			// Non-virtual auth present; fall back to flat round-robin.
			return nil, nil
		}
		groups[parent] = append(groups[parent], a)
	}
	// Collect parent IDs in sorted order for stable cursor indexing.
	parentOrder := make([]string, 0, len(groups))
	for p := range groups {
		parentOrder = append(parentOrder, p)
	}
	sort.Strings(parentOrder)
	return groups, parentOrder
}

// Pick selects the first available auth for the provider in a deterministic manner.
func (s *FillFirstSelector) Pick(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	_ = opts
	now := time.Now()
	available, err := getAvailableAuths(auths, provider, model, now)
	if err != nil {
		return nil, err
	}
	available = preferCodexWebsocketAuths(ctx, provider, available)
	return available[0], nil
}

func isAuthBlockedForModel(auth *Auth, model string, now time.Time) (bool, blockReason, time.Time) {
	if auth == nil {
		return true, blockReasonOther, time.Time{}
	}
	if auth.Disabled || auth.Status == StatusDisabled {
		return true, blockReasonDisabled, time.Time{}
	}
	if model != "" {
		if len(auth.ModelStates) > 0 {
			state, ok := auth.ModelStates[model]
			if (!ok || state == nil) && model != "" {
				baseModel := canonicalModelKey(model)
				if baseModel != "" && baseModel != model {
					state, ok = auth.ModelStates[baseModel]
				}
			}
			if ok && state != nil {
				if state.Status == StatusDisabled {
					return true, blockReasonDisabled, time.Time{}
				}
				if state.Unavailable {
					if state.NextRetryAfter.IsZero() {
						return false, blockReasonNone, time.Time{}
					}
					if state.NextRetryAfter.After(now) {
						next := state.NextRetryAfter
						if !state.Quota.NextRecoverAt.IsZero() && state.Quota.NextRecoverAt.After(now) {
							next = state.Quota.NextRecoverAt
						}
						if next.Before(now) {
							next = now
						}
						if state.Quota.Exceeded {
							return true, blockReasonCooldown, next
						}
						return true, blockReasonOther, next
					}
				}
				return false, blockReasonNone, time.Time{}
			}
		}
		return false, blockReasonNone, time.Time{}
	}
	if auth.Unavailable && auth.NextRetryAfter.After(now) {
		next := auth.NextRetryAfter
		if !auth.Quota.NextRecoverAt.IsZero() && auth.Quota.NextRecoverAt.After(now) {
			next = auth.Quota.NextRecoverAt
		}
		if next.Before(now) {
			next = now
		}
		if auth.Quota.Exceeded {
			return true, blockReasonCooldown, next
		}
		return true, blockReasonOther, next
	}
	return false, blockReasonNone, time.Time{}
}
