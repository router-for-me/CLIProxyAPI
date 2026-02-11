package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
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

// SequentialFillSelector selects credentials sequentially without jumping back.
// Unlike FillFirstSelector which always picks the first available (by ID),
// this selector "sticks" to the current credential until it becomes unavailable,
// then advances to the next one. When a previously used credential recovers,
// it won't jump back - ensuring balanced usage across all credentials.
//
// For mixed-provider requests, a two-level sticky selection is used:
// first stick to the current provider until all its credentials are
// exhausted (in cooldown/unavailable), then advance to the next provider.
// Within each provider, the same sticky sequential selection applies.
type SequentialFillSelector struct {
	mu              sync.Mutex
	current         map[string]string // actualProvider:model -> current auth ID
	stickyProvider  map[string]string // model -> current provider name (sticky)
}

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
func (s *RoundRobinSelector) Pick(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	_ = ctx
	_ = opts
	now := time.Now()
	available, err := getAvailableAuths(auths, provider, model, now)
	if err != nil {
		return nil, err
	}
	key := provider + ":" + canonicalModelKey(model)
	s.mu.Lock()
	if s.cursors == nil {
		s.cursors = make(map[string]int)
	}
	limit := s.maxKeys
	if limit <= 0 {
		limit = 4096
	}
	if _, ok := s.cursors[key]; !ok && len(s.cursors) >= limit {
		s.cursors = make(map[string]int)
	}
	index := s.cursors[key]

	if index >= 2_147_483_640 {
		index = 0
	}

	s.cursors[key] = index + 1
	s.mu.Unlock()
	// log.Debugf("available: %d, index: %d, key: %d", len(available), index, index%len(available))
	return available[index%len(available)], nil
}

// Pick selects the first available auth for the provider in a deterministic manner.
func (s *FillFirstSelector) Pick(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	_ = ctx
	_ = opts
	now := time.Now()
	available, err := getAvailableAuths(auths, provider, model, now)
	if err != nil {
		return nil, err
	}
	return available[0], nil
}

// Pick selects credentials sequentially without jumping back to earlier ones.
// For mixed-provider requests, it sticks to the current provider until all its
// credentials are exhausted, then advances to the next provider.
func (s *SequentialFillSelector) Pick(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	_ = ctx
	_ = opts
	now := time.Now()
	available, err := getAvailableAuths(auths, provider, model, now)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.current == nil {
		s.current = make(map[string]string)
	}

	// Single provider path: flat sticky selection.
	if provider != "mixed" {
		return s.pickSticky(provider, model, available), nil
	}

	// Mixed provider path: group by actual provider.
	groups := make(map[string][]*Auth)
	for _, auth := range available {
		groups[auth.Provider] = append(groups[auth.Provider], auth)
	}

	// Single actual provider in the mix: no rotation needed.
	if len(groups) == 1 {
		for p := range groups {
			return s.pickSticky(p, model, groups[p]), nil
		}
	}

	// Sticky provider selection: stick to the current provider as long as it
	// has available credentials. Only advance when the current provider is
	// exhausted (all its credentials are in cooldown/unavailable).
	if s.stickyProvider == nil {
		s.stickyProvider = make(map[string]string)
	}

	// Sort provider names for deterministic ordering.
	providers := make([]string, 0, len(groups))
	for p := range groups {
		providers = append(providers, p)
	}
	sort.Strings(providers)

	// If we have a sticky provider and it still has available credentials, use it.
	if cp := s.stickyProvider[model]; cp != "" {
		if auths, ok := groups[cp]; ok {
			return s.pickSticky(cp, model, auths), nil
		}
		// Current provider exhausted, advance to the next one.
		next := providers[0]
		for _, p := range providers {
			if p > cp {
				next = p
				break
			}
		}
		s.stickyProvider[model] = next
		return s.pickSticky(next, model, groups[next]), nil
	}

	// First access: start with the first provider.
	s.stickyProvider[model] = providers[0]
	return s.pickSticky(providers[0], model, groups[providers[0]]), nil
}

// pickSticky selects a credential from the given group with sticky sequential behavior.
// Must be called with s.mu held.
func (s *SequentialFillSelector) pickSticky(provider, model string, available []*Auth) *Auth {
	key := provider + ":" + model
	currentID := s.current[key]

	// First access: randomly select a starting credential.
	if currentID == "" {
		i := rand.Intn(len(available))
		s.current[key] = available[i].ID
		return available[i]
	}

	// Sticky: if current credential is still available, keep using it.
	for _, auth := range available {
		if auth.ID == currentID {
			return auth
		}
	}

	// Advance: find the first credential with ID > currentID.
	for _, auth := range available {
		if auth.ID > currentID {
			s.current[key] = auth.ID
			return auth
		}
	}

	// Wrap around: all subsequent credentials unavailable, start from beginning.
	s.current[key] = available[0].ID
	return available[0]
}

// MaxRetryAttempts implements RetryLimiter.
// Returns 2 to limit attempts to current credential + 1 retry,
// preserving stickiness and preventing credential pool exhaustion.
func (s *SequentialFillSelector) MaxRetryAttempts() int {
	return 2
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
