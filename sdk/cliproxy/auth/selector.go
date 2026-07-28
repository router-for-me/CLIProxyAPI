package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/credentialweight"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	cliproxysession "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/session"
)

// RoundRobinSelector provides a simple provider scoped round-robin selection strategy.
type RoundRobinSelector struct {
	mu      sync.Mutex
	cursors map[string]int
	maxKeys int
}

// WeightedRoundRobinSelector provides smooth weighted round-robin selection.
type WeightedRoundRobinSelector struct {
	mu      sync.Mutex
	states  map[string]*smoothWeightedState
	maxKeys int
}

type smoothWeightedState struct {
	current map[string]int64
	weights map[string]int64
}

type weightedSelectorStateModelKey struct{}

func withWeightedSelectorStateModel(ctx context.Context, selector Selector, routeModel string) context.Context {
	if _, ok := selector.(*WeightedRoundRobinSelector); !ok || strings.TrimSpace(routeModel) == "" {
		return ctx
	}
	return context.WithValue(ctx, weightedSelectorStateModelKey{}, routeModel)
}

func weightedSelectorStateModel(ctx context.Context, availabilityModel string) string {
	if ctx != nil {
		if routeModel, ok := ctx.Value(weightedSelectorStateModelKey{}).(string); ok && strings.TrimSpace(routeModel) != "" {
			return routeModel
		}
	}
	return availabilityModel
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

func authWeight(auth *Auth) int64 {
	if auth == nil {
		return credentialweight.Default
	}
	if rawWeight, ok := auth.Attributes[AttributeWeight]; ok && strings.TrimSpace(rawWeight) != "" {
		weight, errParse := credentialweight.ParseString(rawWeight)
		if errParse != nil {
			return 0
		}
		return weight
	}
	if rawWeight, ok := auth.Metadata[AttributeWeight]; ok {
		weight, errParse := credentialweight.ParseValue(rawWeight)
		if errParse != nil {
			return 0
		}
		return weight
	}
	return credentialweight.Default
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

func positiveWeightAuths(auths []*Auth) []*Auth {
	weightedCandidates := make([]*Auth, 0, len(auths))
	for _, auth := range auths {
		if authWeight(auth) > 0 {
			weightedCandidates = append(weightedCandidates, auth)
		}
	}
	return weightedCandidates
}

// Pick selects the next available auth using smooth weighted round-robin.
func (s *WeightedRoundRobinSelector) Pick(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	_ = opts
	available, errAvailable := getAvailableAuths(positiveWeightAuths(auths), provider, model, time.Now())
	if errAvailable != nil {
		return nil, errAvailable
	}
	available = preferCodexWebsocketAuths(ctx, provider, available)
	stateModel := weightedSelectorStateModel(ctx, model)
	key := provider + ":" + canonicalModelKey(stateModel)

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.states == nil {
		s.states = make(map[string]*smoothWeightedState)
	}
	limit := s.maxKeys
	if limit <= 0 {
		limit = 4096
	}
	if _, ok := s.states[key]; !ok && len(s.states) >= limit {
		s.states = make(map[string]*smoothWeightedState)
	}
	state := s.states[key]
	if state == nil {
		state = &smoothWeightedState{}
		s.states[key] = state
	}
	weights := authWeightVector(available)
	state.prepare(weights)
	picked := pickSmoothWeightedAuth(available, state.current)
	if picked == nil {
		return nil, &Error{Code: "auth_unavailable", Message: "no auth available with positive weight"}
	}
	return picked, nil
}

func (s *smoothWeightedState) prepare(weights map[string]int64) {
	if s.current == nil || !weightVectorsEqual(s.weights, weights) {
		s.current = make(map[string]int64)
	}
	s.weights = weights
}

func weightVectorsEqual(left, right map[string]int64) bool {
	if len(left) != len(right) {
		return false
	}
	for authID, weight := range left {
		if right[authID] != weight {
			return false
		}
	}
	return true
}

func authWeightVector(auths []*Auth) map[string]int64 {
	weights := make(map[string]int64, len(auths))
	for _, auth := range auths {
		if auth == nil {
			continue
		}
		if weight := authWeight(auth); weight > 0 {
			weights[auth.ID] = weight
		}
	}
	return weights
}

func pickSmoothWeightedAuth(auths []*Auth, current map[string]int64) *Auth {
	active := make(map[string]struct{}, len(auths))
	var picked *Auth
	var pickedCurrent int64
	var totalWeight int64
	for _, auth := range auths {
		weight := authWeight(auth)
		if auth == nil || weight <= 0 {
			continue
		}
		active[auth.ID] = struct{}{}
		current[auth.ID] = saturatingAddInt64(current[auth.ID], weight)
		totalWeight = saturatingAddInt64(totalWeight, weight)
		if picked == nil || current[auth.ID] > pickedCurrent {
			picked = auth
			pickedCurrent = current[auth.ID]
		}
	}
	for authID := range current {
		if _, ok := active[authID]; !ok {
			delete(current, authID)
		}
	}
	if picked == nil {
		return nil
	}
	current[picked.ID] = saturatingAddInt64(current[picked.ID], -totalWeight)
	return picked
}

func saturatingAddInt64(value, delta int64) int64 {
	if delta > 0 && value > math.MaxInt64-delta {
		return math.MaxInt64
	}
	if delta < 0 && value < math.MinInt64-delta {
		return math.MinInt64
	}
	return value + delta
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
				return availabilityBlock(state.Unavailable, state.Quota.Exceeded, state.NextRetryAfter, state.Quota.NextRecoverAt, now)
			}
			// Auth-level availability can aggregate failures from other models.
			return false, blockReasonNone, time.Time{}
		}
		return availabilityBlock(auth.Unavailable, auth.Quota.Exceeded, auth.NextRetryAfter, auth.Quota.NextRecoverAt, now)
	}
	return availabilityBlock(auth.Unavailable, auth.Quota.Exceeded, auth.NextRetryAfter, auth.Quota.NextRecoverAt, now)
}

func availabilityBlock(unavailable, quotaExceeded bool, nextRetryAfter, nextRecoverAt, now time.Time) (bool, blockReason, time.Time) {
	if !unavailable && !quotaExceeded {
		return false, blockReasonNone, time.Time{}
	}

	hasRecoveryTime := !nextRetryAfter.IsZero() || !nextRecoverAt.IsZero()
	var next time.Time
	for _, candidate := range []time.Time{nextRetryAfter, nextRecoverAt} {
		if candidate.After(now) && (next.IsZero() || candidate.After(next)) {
			next = candidate
		}
	}
	if !next.IsZero() {
		if quotaExceeded {
			return true, blockReasonCooldown, next
		}
		return true, blockReasonOther, next
	}
	if hasRecoveryTime {
		return false, blockReasonNone, time.Time{}
	}
	return true, blockReasonOther, time.Time{}
}

// SessionAffinitySelector wraps another selector with session-sticky behavior.
// It extracts session ID from multiple sources and maintains session-to-auth
// mappings with automatic failover when the bound auth becomes unavailable.
const sessionAffinityResolutionLockStripes = 256

type SessionAffinitySelector struct {
	fallback        Selector
	cache           *SessionCache
	resolutionLocks [sessionAffinityResolutionLockStripes]sync.Mutex
}

// SessionAffinityConfig configures the session affinity selector.
type SessionAffinityConfig struct {
	Fallback Selector
	TTL      time.Duration
}

// NewSessionAffinitySelector creates a new session-aware selector.
func NewSessionAffinitySelector(fallback Selector) *SessionAffinitySelector {
	return NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: fallback,
		TTL:      time.Hour,
	})
}

// NewSessionAffinitySelectorWithConfig creates a selector with custom configuration.
func NewSessionAffinitySelectorWithConfig(cfg SessionAffinityConfig) *SessionAffinitySelector {
	if cfg.Fallback == nil {
		cfg.Fallback = &RoundRobinSelector{}
	}
	if cfg.TTL <= 0 {
		cfg.TTL = time.Hour
	}
	return &SessionAffinitySelector{
		fallback: cfg.Fallback,
		cache:    NewSessionCache(cfg.TTL),
	}
}

// Pick selects an auth with session affinity when possible.
// All recognized explicit session signals in the same request act as aliases
// for the same logical session. Fallback identities are considered only when no
// explicit root session exists.
//
// Note: The cache key includes the caller scope, provider, model, and session ID.
// Caller scope keeps two downstream API keys that reuse the same client session ID
// on separate bindings; provider and model handle sessions that span several models
// supported by different credentials. The client type is deliberately not part of
// the key: it is detected per request, so a request that arrives without the usual
// headers would otherwise split an active session onto a second binding.
func (s *SessionAffinitySelector) Pick(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	entry := selectorLogEntry(ctx)
	identity := sessionIdentityFromOptions(opts)
	ids := identity.IDs()
	if len(ids) == 0 {
		entry.Debugf("session-affinity: no session ID extracted, falling back to default selector | provider=%s model=%s", provider, model)
		return s.fallback.Pick(ctx, provider, model, opts, auths)
	}

	primaryID := identity.ID
	callerScope := optionsMetadataString(opts, cliproxyexecutor.CallerScopeMetadataKey)

	cacheKeys := make([]string, 0, len(ids))
	for _, id := range ids {
		cacheKeys = append(cacheKeys, sessionCacheKey(callerScope, provider, model, id))
	}

	now := time.Now()
	availabilityCandidates := auths
	if _, weighted := s.fallback.(*WeightedRoundRobinSelector); weighted {
		availabilityCandidates = positiveWeightAuths(auths)
	}
	available, err := getAvailableAuths(availabilityCandidates, provider, model, now)
	if err != nil {
		return nil, err
	}
	availableByID := make(map[string]*Auth, len(available))
	for _, auth := range available {
		if auth != nil && auth.ID != "" {
			availableByID[auth.ID] = auth
		}
	}

	for {
		snapshot := s.cache.bindingSnapshot(cacheKeys)
		lockKeys := sessionAffinityResolutionLockKeys(cacheKeys, snapshot.groupLockKeys)
		heldLocks, unlock := s.lockSessionAffinityResolution(lockKeys)

		current := s.cache.bindingSnapshot(cacheKeys)
		currentLockKeys := sessionAffinityResolutionLockKeys(cacheKeys, current.groupLockKeys)
		if !sessionAffinityResolutionLocksCover(heldLocks, currentLockKeys) {
			unlock()
			continue
		}

		var selected *Auth
		resolution := "miss"
		switch len(current.authIDs) {
		case 0:
			selected, err = s.fallback.Pick(ctx, provider, model, opts, auths)
		case 1:
			selected = availableByID[current.authIDs[0]]
			if selected != nil {
				resolution = "hit"
				break
			}
			resolution = "unavailable"
			selected, err = s.fallback.Pick(ctx, provider, model, opts, auths)
		default:
			resolution = "conflict"
			selected, err = s.fallback.Pick(ctx, provider, model, opts, auths)
		}
		if err != nil {
			unlock()
			return nil, err
		}
		s.cache.SetAliases(selected.ID, cacheKeys...)
		unlock()

		switch resolution {
		case "hit":
			entry.Infof("session-affinity: cache hit | session=%s aliases=%d auth=%s provider=%s model=%s", truncateSessionID(primaryID), len(ids), selected.ID, provider, model)
		case "unavailable":
			entry.Infof("session-affinity: cache hit but auth unavailable, reselected | session=%s aliases=%d auth=%s provider=%s model=%s", truncateSessionID(primaryID), len(ids), selected.ID, provider, model)
		case "conflict":
			entry.Warnf("session-affinity: alias group conflict detected, reselecting | aliases=%d auth_count=%d provider=%s model=%s", len(ids), len(current.authIDs), provider, model)
			entry.Infof("session-affinity: resolved alias group conflict with new binding | session=%s aliases=%d auth=%s provider=%s model=%s", truncateSessionID(primaryID), len(ids), selected.ID, provider, model)
		default:
			entry.Infof("session-affinity: cache miss, new binding | session=%s aliases=%d auth=%s provider=%s model=%s", truncateSessionID(primaryID), len(ids), selected.ID, provider, model)
		}
		return selected, nil
	}
}

func sessionAffinityResolutionLockKeys(cacheKeys, groupLockKeys []string) []string {
	keys := make([]string, 0, len(cacheKeys)+len(groupLockKeys))
	for _, key := range cacheKeys {
		keys = append(keys, "alias\x00"+key)
	}
	for _, key := range groupLockKeys {
		keys = append(keys, "group\x00"+key)
	}
	return keys
}

func sessionAffinityResolutionLockIndex(key string) int {
	const (
		offset32 = uint32(2166136261)
		prime32  = uint32(16777619)
	)
	hash := offset32
	for index := 0; index < len(key); index++ {
		hash ^= uint32(key[index])
		hash *= prime32
	}
	return int(hash % sessionAffinityResolutionLockStripes)
}

func (s *SessionAffinitySelector) lockSessionAffinityResolution(keys []string) ([sessionAffinityResolutionLockStripes]bool, func()) {
	var held [sessionAffinityResolutionLockStripes]bool
	indices := make([]int, 0, len(keys))
	for _, key := range keys {
		index := sessionAffinityResolutionLockIndex(key)
		if held[index] {
			continue
		}
		held[index] = true
		indices = append(indices, index)
	}
	sort.Ints(indices)
	for _, index := range indices {
		s.resolutionLocks[index].Lock()
	}
	return held, func() {
		for index := len(indices) - 1; index >= 0; index-- {
			s.resolutionLocks[indices[index]].Unlock()
		}
	}
}

func sessionAffinityResolutionLocksCover(held [sessionAffinityResolutionLockStripes]bool, keys []string) bool {
	for _, key := range keys {
		if !held[sessionAffinityResolutionLockIndex(key)] {
			return false
		}
	}
	return true
}

func selectorLogEntry(ctx context.Context) *log.Entry {
	if ctx == nil {
		return log.NewEntry(log.StandardLogger())
	}
	if reqID := logging.GetRequestID(ctx); reqID != "" {
		return log.WithField("request_id", reqID)
	}
	return log.NewEntry(log.StandardLogger())
}

// truncateSessionID shortens session ID for logging (first 8 chars + "...")
func truncateSessionID(id string) string {
	if len(id) <= 20 {
		return id
	}
	return id[:8] + "..."
}

// Stop releases resources held by the selector.
func (s *SessionAffinitySelector) Stop() {
	if s.cache != nil {
		s.cache.Stop()
	}
}

// InvalidateAuth removes all session bindings for a specific auth.
// Called when an auth becomes rate-limited or unavailable.
func (s *SessionAffinitySelector) InvalidateAuth(authID string) {
	if s.cache != nil {
		s.cache.InvalidateAuth(authID)
	}
}

// sessionCacheKeyParts is the number of "::" separated fields in a session cache key.
const sessionCacheKeyParts = 4

// sessionCacheKey builds a session binding key namespaced by caller scope, provider
// and model. The session ID is placed last so it can be recovered exactly even when
// an opaque client identifier itself contains the "::" separator.
func sessionCacheKey(callerScope, provider, model, sessionID string) string {
	return strings.Join([]string{callerScope, provider, model, sessionID}, "::")
}

// sessionCacheKeySessionID recovers the session ID from a key built by sessionCacheKey.
func sessionCacheKeySessionID(key string) string {
	parts := strings.SplitN(key, "::", sessionCacheKeyParts)
	if len(parts) < sessionCacheKeyParts {
		return ""
	}
	return parts[sessionCacheKeyParts-1]
}

// sessionIdentityFromOptions returns the identity resolved once per request by
// session.Enrich, and extracts it on demand for direct SDK callers that bypass Enrich.
func sessionIdentityFromOptions(opts cliproxyexecutor.Options) cliproxysession.Identity {
	if identity, ok := cliproxysession.FromMetadata(opts.Metadata); ok {
		return identity
	}
	return cliproxysession.Extract(opts.Headers, opts.OriginalRequest, opts.Metadata)
}

func optionsMetadataString(opts cliproxyexecutor.Options, key string) string {
	if opts.Metadata == nil {
		return ""
	}
	value, _ := opts.Metadata[key].(string)
	return strings.TrimSpace(value)
}

// ExtractSessionID returns the representative session identifier selected for
// display and compatibility. Affinity routing uses every ID returned by Identity.IDs.
func ExtractSessionID(headers http.Header, payload []byte, metadata map[string]any) string {
	return cliproxysession.Extract(headers, payload, metadata).ID
}

// extractSessionIDs returns the representative ID and first alias for compatibility.
// SessionAffinitySelector routes on the full identifier set instead.
func extractSessionIDs(headers http.Header, payload []byte, metadata map[string]any) (string, string) {
	identity := cliproxysession.Extract(headers, payload, metadata)
	return identity.ID, identity.FallbackID()
}

// extractSessionID is kept for backward compatibility.
// Deprecated: Use ExtractSessionID instead.
func extractSessionID(payload []byte) string {
	return ExtractSessionID(nil, payload, nil)
}
