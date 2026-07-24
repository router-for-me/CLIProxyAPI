package helps

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	ClaudePromptCacheMaxBreakpoints = 4

	claudePromptCacheMaxScopes             = 256
	claudePromptCacheMaxSequencesPerScope  = 8
	claudePromptCacheMaxPrefixesPerScope   = 64
	claudePromptCacheMaxTrackedTools       = 256
	claudePromptCacheMaxActiveFlights      = 1024
	claudePromptCacheMaxDiagnosticSessions = 1024
	claudePromptCacheStartedLifetime       = 30 * time.Second
	claudePromptCacheDefaultTTL            = 5 * time.Minute
	claudePromptCacheExtendedTTL           = time.Hour
	claudePromptCacheDiagnosticLifetime    = 2 * time.Hour
	claudePromptCacheDefaultSafetyMargin   = 30 * time.Second
	claudePromptCacheExtendedSafetyMargin  = 2 * time.Minute
	claudePromptCacheAutomaticControlJSON  = `{"type":"ephemeral"}`
	claudePromptCacheFingerprintDomain     = "claude-prompt-cache-tool-v1"
	claudePromptCachePrefixFingerprintV1   = "claude-prompt-cache-prefix-v1"
	claudePromptCacheSequenceFingerprintV1 = "claude-prompt-cache-sequence-v1"
)

// ClaudePromptCacheCapabilities describes cache-control support for one upstream.
type ClaudePromptCacheCapabilities struct {
	AutomaticHistory bool
	ExplicitHistory  bool
}

// ClaudePromptCachePlanSummary contains non-sensitive planner diagnostics.
type ClaudePromptCachePlanSummary struct {
	ExistingBreakpoints int
	AddedBreakpoints    int
	RemovedBreakpoints  int
	FinalBreakpoints    int
	CurrentToolCount    int
	StableToolCut       int
	AutomaticHistory    bool
}

// ClaudePromptCachePrefix identifies one explicit or automatic cacheable prefix.
type ClaudePromptCachePrefix struct {
	Key       string
	Kind      string
	Depth     int
	TTL       time.Duration
	ToolCut   int
	Added     bool
	Confirmed bool
}

// ClaudePromptCachePlan describes the final cache layout for one request.
type ClaudePromptCachePlan struct {
	ScopeKey         string
	ToolSequenceKey  string
	ToolFingerprints [][32]byte
	Prefixes         []ClaudePromptCachePrefix
	Summary          ClaudePromptCachePlanSummary
}

type claudePromptCachePrefixState struct {
	startedUntil   time.Time
	startedBy      *claudePromptCacheFlight
	confirmedUntil time.Time
	ttl            time.Duration
	lastTouched    time.Time
}

type claudePromptCacheWarmSequence struct {
	sequenceKey      string
	toolFingerprints [][32]byte
	candidateCuts    map[int]string
	validUntil       time.Time
	lastSuccess      time.Time
	lastUsed         time.Time
}

type claudePromptCacheScopeState struct {
	prefixes   map[string]*claudePromptCachePrefixState
	sequences  []*claudePromptCacheWarmSequence
	lastAccess time.Time
}

type claudePromptCacheDiagnosticState struct {
	messageID            string
	generation           uint64
	minimumGeneration    uint64
	successfulGeneration uint64
	lastUsed             time.Time
}

type claudePromptCacheFlightOutcome uint8

const (
	claudePromptCacheFlightPending claudePromptCacheFlightOutcome = iota
	claudePromptCacheFlightStarted
	claudePromptCacheFlightFailed
)

type claudePromptCacheFlight struct {
	keys         []string
	done         chan struct{}
	outcome      claudePromptCacheFlightOutcome
	completeOnce sync.Once
}

// ClaudePromptCacheRuntime keeps bounded, process-local prompt-cache knowledge.
type ClaudePromptCacheRuntime struct {
	mutex                sync.Mutex
	scopes               map[string]*claudePromptCacheScopeState
	flights              map[string]*claudePromptCacheFlight
	diagnostics          map[string]*claudePromptCacheDiagnosticState
	diagnosticsDisabled  map[string]time.Time
	diagnosticGeneration uint64
	now                  func() time.Time
}

// ClaudePromptCacheAttempt coordinates a request without sharing its response.
type ClaudePromptCacheAttempt struct {
	runtime     *ClaudePromptCacheRuntime
	plan        *ClaudePromptCachePlan
	flight      *claudePromptCacheFlight
	claimedKeys []string
	finishOnce  sync.Once
}

type claudeBreakpointLocation struct {
	kind           string
	path           string
	primaryIndex   int
	secondaryIndex int
	depth          int
	cacheControl   string
	added          bool
}

// NewClaudePromptCacheRuntime creates an isolated prompt-cache runtime.
func NewClaudePromptCacheRuntime() *ClaudePromptCacheRuntime {
	return &ClaudePromptCacheRuntime{
		scopes:              make(map[string]*claudePromptCacheScopeState),
		flights:             make(map[string]*claudePromptCacheFlight),
		diagnostics:         make(map[string]*claudePromptCacheDiagnosticState),
		diagnosticsDisabled: make(map[string]time.Time),
		now:                 time.Now,
	}
}

func (runtime *ClaudePromptCacheRuntime) initialize() {
	if runtime == nil {
		return
	}
	runtime.mutex.Lock()
	defer runtime.mutex.Unlock()
	if runtime.scopes == nil {
		runtime.scopes = make(map[string]*claudePromptCacheScopeState)
	}
	if runtime.flights == nil {
		runtime.flights = make(map[string]*claudePromptCacheFlight)
	}
	if runtime.diagnostics == nil {
		runtime.diagnostics = make(map[string]*claudePromptCacheDiagnosticState)
	}
	if runtime.diagnosticsDisabled == nil {
		runtime.diagnosticsDisabled = make(map[string]time.Time)
	}
	if runtime.now == nil {
		runtime.now = time.Now
	}
}

// DiagnosticAllowed reports whether proxy-owned diagnostics are enabled for a scope.
func (runtime *ClaudePromptCacheRuntime) DiagnosticAllowed(scopeKey string) bool {
	if runtime == nil || strings.TrimSpace(scopeKey) == "" {
		return false
	}
	runtime.initialize()
	now := runtime.now()
	runtime.mutex.Lock()
	defer runtime.mutex.Unlock()
	disabledUntil := runtime.diagnosticsDisabled[scopeKey]
	if !disabledUntil.After(now) {
		delete(runtime.diagnosticsDisabled, scopeKey)
		return true
	}
	return false
}

// DisableDiagnostic temporarily suppresses proxy-owned diagnostics for a scope.
func (runtime *ClaudePromptCacheRuntime) DisableDiagnostic(scopeKey string) {
	if runtime == nil || strings.TrimSpace(scopeKey) == "" {
		return
	}
	runtime.initialize()
	now := runtime.now()
	runtime.mutex.Lock()
	defer runtime.mutex.Unlock()
	runtime.diagnosticsDisabled[scopeKey] = now.Add(claudePromptCacheDiagnosticLifetime)
	if len(runtime.diagnosticsDisabled) <= claudePromptCacheMaxScopes {
		return
	}
	oldestScopeKey := ""
	oldestExpiry := time.Time{}
	for candidateScopeKey, disabledUntil := range runtime.diagnosticsDisabled {
		if oldestScopeKey == "" || disabledUntil.Before(oldestExpiry) {
			oldestScopeKey = candidateScopeKey
			oldestExpiry = disabledUntil
		}
	}
	delete(runtime.diagnosticsDisabled, oldestScopeKey)
}

// InvalidateDiagnosticMessageID clears a stale previous-message link unless a
// newer successful response has already advanced the chain.
func (runtime *ClaudePromptCacheRuntime) InvalidateDiagnosticMessageID(
	diagnosticKey string,
	generation uint64,
	rejectedMessageID string,
) {
	rejectedMessageID = strings.TrimSpace(rejectedMessageID)
	if runtime == nil || strings.TrimSpace(diagnosticKey) == "" || generation == 0 || rejectedMessageID == "" {
		return
	}
	runtime.initialize()
	now := runtime.now()
	runtime.mutex.Lock()
	defer runtime.mutex.Unlock()
	state := runtime.diagnostics[diagnosticKey]
	if state == nil || state.successfulGeneration >= generation || state.messageID != rejectedMessageID {
		return
	}
	state.messageID = ""
	state.lastUsed = now
}

// BeginDiagnostic reserves the next generation in one diagnostic chain.
func (runtime *ClaudePromptCacheRuntime) BeginDiagnostic(diagnosticKey string) (string, uint64) {
	if runtime == nil || strings.TrimSpace(diagnosticKey) == "" {
		return "", 0
	}
	runtime.initialize()
	now := runtime.now()
	runtime.mutex.Lock()
	defer runtime.mutex.Unlock()
	runtime.cleanupDiagnosticsLocked(now)
	state := runtime.diagnostics[diagnosticKey]
	createdState := false
	if state == nil {
		state = &claudePromptCacheDiagnosticState{}
		runtime.diagnostics[diagnosticKey] = state
		createdState = true
	}
	runtime.diagnosticGeneration++
	if runtime.diagnosticGeneration == 0 {
		runtime.diagnosticGeneration++
	}
	state.generation = runtime.diagnosticGeneration
	if createdState {
		state.minimumGeneration = state.generation
	}
	state.lastUsed = now
	runtime.trimDiagnosticsLocked()
	return state.messageID, state.generation
}

// RecordDiagnosticMessageID advances a diagnostic chain unless a newer
// successful diagnostic response has already advanced it.
func (runtime *ClaudePromptCacheRuntime) RecordDiagnosticMessageID(
	diagnosticKey string,
	generation uint64,
	messageID string,
) {
	if runtime == nil || strings.TrimSpace(diagnosticKey) == "" || generation == 0 || strings.TrimSpace(messageID) == "" {
		return
	}
	runtime.initialize()
	now := runtime.now()
	runtime.mutex.Lock()
	defer runtime.mutex.Unlock()
	runtime.cleanupDiagnosticsLocked(now)
	state := runtime.diagnostics[diagnosticKey]
	if state == nil ||
		generation < state.minimumGeneration ||
		generation > state.generation ||
		generation <= state.successfulGeneration {
		return
	}
	state.messageID = strings.TrimSpace(messageID)
	state.successfulGeneration = generation
	state.lastUsed = now
}

// PlanClaudePromptCache applies adaptive cache-control planning to payload.
func (runtime *ClaudePromptCacheRuntime) PlanClaudePromptCache(
	scopeKey string,
	payload []byte,
	capabilities ClaudePromptCacheCapabilities,
) ([]byte, *ClaudePromptCachePlan) {
	if runtime == nil || strings.TrimSpace(scopeKey) == "" || len(payload) == 0 || !gjson.ValidBytes(payload) {
		return payload, nil
	}
	runtime.initialize()

	now := runtime.now()
	toolFingerprints, toolPrefixKeys := fingerprintClaudeTools(payload)
	stableToolCut := runtime.findStableToolCut(scopeKey, toolFingerprints, now)
	existingLocations, invalidPaths := collectClaudeCacheBreakpoints(payload)

	updatedPayload := payload
	for _, invalidPath := range invalidPaths {
		if nextPayload, errDelete := sjson.DeleteBytes(updatedPayload, invalidPath); errDelete == nil {
			updatedPayload = nextPayload
		}
	}

	selectedExisting := selectExistingClaudeBreakpoints(existingLocations, ClaudePromptCacheMaxBreakpoints)
	selectedPaths := make(map[string]struct{}, ClaudePromptCacheMaxBreakpoints)
	for _, location := range selectedExisting {
		selectedPaths[location.path] = struct{}{}
	}
	for _, location := range existingLocations {
		if _, keep := selectedPaths[location.path]; keep {
			continue
		}
		if nextPayload, errDelete := sjson.DeleteBytes(updatedPayload, location.path); errDelete == nil {
			updatedPayload = nextPayload
		}
	}

	remainingSlots := ClaudePromptCacheMaxBreakpoints - len(selectedExisting)
	addedBreakpoints := 0
	addedPaths := make(map[string]struct{}, ClaudePromptCacheMaxBreakpoints)
	if remainingSlots > 0 && stableToolCut > 0 && stableToolCut <= len(toolFingerprints) {
		path := fmt.Sprintf("tools.%d.cache_control", stableToolCut-1)
		if _, exists := selectedPaths[path]; !exists {
			if nextPayload, errSet := sjson.SetRawBytes(updatedPayload, path, []byte(claudePromptCacheAutomaticControlJSON)); errSet == nil {
				updatedPayload = nextPayload
				selectedPaths[path] = struct{}{}
				addedPaths[path] = struct{}{}
				remainingSlots--
				addedBreakpoints++
			}
		}
	}

	if remainingSlots > 0 && len(toolFingerprints) > 0 {
		path := fmt.Sprintf("tools.%d.cache_control", len(toolFingerprints)-1)
		if _, exists := selectedPaths[path]; !exists {
			if nextPayload, errSet := sjson.SetRawBytes(updatedPayload, path, []byte(claudePromptCacheAutomaticControlJSON)); errSet == nil {
				updatedPayload = nextPayload
				selectedPaths[path] = struct{}{}
				addedPaths[path] = struct{}{}
				remainingSlots--
				addedBreakpoints++
			}
		}
	}

	if remainingSlots > 0 {
		var added bool
		var addedPath string
		updatedPayload, added, addedPath = addClaudeSystemTailBreakpoint(updatedPayload, selectedPaths)
		if added {
			addedPaths[addedPath] = struct{}{}
			remainingSlots--
			addedBreakpoints++
		}
	}

	if remainingSlots > 0 && capabilities.ExplicitHistory {
		var added bool
		var addedPath string
		updatedPayload, added, addedPath = addClaudeHistoryBreakpoint(updatedPayload, selectedPaths)
		if added {
			addedPaths[addedPath] = struct{}{}
			remainingSlots--
			addedBreakpoints++
		}
	}

	automaticHistory := false
	automaticHistoryAdded := false
	if capabilities.AutomaticHistory {
		cacheControl := gjson.GetBytes(updatedPayload, "cache_control")
		if !isValidClaudeCacheControl(cacheControl) {
			if cacheControl.Exists() {
				updatedPayload, _ = sjson.DeleteBytes(updatedPayload, "cache_control")
			}
			if nextPayload, errSet := sjson.SetRawBytes(updatedPayload, "cache_control", []byte(claudePromptCacheAutomaticControlJSON)); errSet == nil {
				updatedPayload = nextPayload
				automaticHistory = true
				automaticHistoryAdded = true
			}
		} else {
			automaticHistory = true
		}
	} else if gjson.GetBytes(updatedPayload, "cache_control").Exists() {
		updatedPayload, _ = sjson.DeleteBytes(updatedPayload, "cache_control")
	}
	updatedPayload = NormalizeClaudeCacheControlTTL(updatedPayload)

	finalLocations, _ := collectClaudeCacheBreakpoints(updatedPayload)
	for locationIndex := range finalLocations {
		_, finalLocations[locationIndex].added = addedPaths[finalLocations[locationIndex].path]
	}
	prefixes := buildClaudePromptCachePrefixes(scopeKey, updatedPayload, finalLocations, toolPrefixKeys, runtime, now)
	if automaticHistory {
		prefixes = append(prefixes, buildClaudeAutomaticHistoryPrefixes(
			scopeKey,
			updatedPayload,
			claudeCacheControlTTL(gjson.GetBytes(updatedPayload, "cache_control").Raw),
			automaticHistoryAdded,
			runtime,
			now,
		)...)
	}
	sequenceKey := fingerprintClaudeToolSequence(toolFingerprints)
	plan := &ClaudePromptCachePlan{
		ScopeKey:         scopeKey,
		ToolSequenceKey:  sequenceKey,
		ToolFingerprints: cloneClaudeToolFingerprints(toolFingerprints),
		Prefixes:         prefixes,
		Summary: ClaudePromptCachePlanSummary{
			ExistingBreakpoints: len(existingLocations),
			AddedBreakpoints:    addedBreakpoints,
			RemovedBreakpoints:  len(invalidPaths) + len(existingLocations) - len(selectedExisting),
			FinalBreakpoints:    len(finalLocations),
			CurrentToolCount:    len(toolFingerprints),
			StableToolCut:       stableToolCut,
			AutomaticHistory:    automaticHistory,
		},
	}
	return updatedPayload, plan
}

// NormalizeClaudeCacheControlTTL enforces Anthropic's tools → system →
// messages TTL ordering. Once a default 5-minute breakpoint appears, later
// one-hour breakpoints are downgraded to the default TTL.
func NormalizeClaudeCacheControlTTL(payload []byte) []byte {
	if len(payload) == 0 || !gjson.ValidBytes(payload) {
		return payload
	}

	originalPayload := payload
	seenDefaultTTL := false
	modified := false
	processBlock := func(path string, object gjson.Result) {
		cacheControl := object.Get("cache_control")
		if !cacheControl.Exists() {
			return
		}
		if !cacheControl.IsObject() {
			seenDefaultTTL = true
			return
		}
		ttl := cacheControl.Get("ttl")
		if ttl.Type != gjson.String || ttl.String() != "1h" {
			seenDefaultTTL = true
			return
		}
		if !seenDefaultTTL {
			return
		}
		ttlPath := "cache_control.ttl"
		if path != "" {
			ttlPath = path + ".cache_control.ttl"
		}
		updatedPayload, errDelete := sjson.DeleteBytes(payload, ttlPath)
		if errDelete != nil {
			return
		}
		payload = updatedPayload
		modified = true
	}

	tools := gjson.GetBytes(payload, "tools")
	if tools.IsArray() {
		tools.ForEach(func(index, item gjson.Result) bool {
			processBlock(fmt.Sprintf("tools.%d", index.Int()), item)
			return true
		})
	}
	system := gjson.GetBytes(payload, "system")
	if system.IsArray() {
		system.ForEach(func(index, item gjson.Result) bool {
			processBlock(fmt.Sprintf("system.%d", index.Int()), item)
			return true
		})
	}
	messages := gjson.GetBytes(payload, "messages")
	if messages.IsArray() {
		messages.ForEach(func(messageIndex, message gjson.Result) bool {
			content := message.Get("content")
			if !content.IsArray() {
				return true
			}
			content.ForEach(func(contentIndex, item gjson.Result) bool {
				processBlock(
					fmt.Sprintf("messages.%d.content.%d", messageIndex.Int(), contentIndex.Int()),
					item,
				)
				return true
			})
			return true
		})
	}
	processBlock("", gjson.ParseBytes(payload))

	if !modified {
		return originalPayload
	}
	return payload
}

// Acquire coordinates a cold prefix. Followers wait only until the leader starts a response.
func (runtime *ClaudePromptCacheRuntime) Acquire(
	ctx context.Context,
	plan *ClaudePromptCachePlan,
	maxWait time.Duration,
) (*ClaudePromptCacheAttempt, error) {
	attempt := &ClaudePromptCacheAttempt{runtime: runtime, plan: plan}
	if runtime == nil || plan == nil || len(plan.Prefixes) == 0 {
		return attempt, nil
	}
	runtime.initialize()
	if maxWait <= 0 {
		return attempt, nil
	}

	deadline := time.Now().Add(maxWait)
	maxAcquisitionRounds := len(plan.Prefixes) + 1
	for acquisitionRound := 0; acquisitionRound < maxAcquisitionRounds; acquisitionRound++ {
		flight, claimedKeys, waitFlight := runtime.acquireFlight(plan)
		if flight != nil {
			attempt.flight = flight
			attempt.claimedKeys = claimedKeys
			return attempt, nil
		}
		if waitFlight == nil {
			return attempt, nil
		}

		remainingWait := time.Until(deadline)
		if remainingWait <= 0 {
			return attempt, nil
		}
		waitTimer := time.NewTimer(remainingWait)
		select {
		case <-waitFlight.done:
			if !waitTimer.Stop() {
				select {
				case <-waitTimer.C:
				default:
				}
			}
			continue
		case <-ctx.Done():
			if !waitTimer.Stop() {
				select {
				case <-waitTimer.C:
				default:
				}
			}
			return attempt, ctx.Err()
		case <-waitTimer.C:
			return attempt, nil
		}
	}

	return attempt, nil
}

// IsLeader reports whether this request owns a cold-prefix flight.
func (attempt *ClaudePromptCacheAttempt) IsLeader() bool {
	return attempt != nil && attempt.flight != nil && len(attempt.claimedKeys) > 0
}

// ClaimedPrefixKeys returns the hashed prefix keys owned by this attempt.
func (attempt *ClaudePromptCacheAttempt) ClaimedPrefixKeys() []string {
	if attempt == nil {
		return nil
	}
	return append([]string(nil), attempt.claimedKeys...)
}

// MarkResponseStarted releases followers after successful upstream response headers.
func (attempt *ClaudePromptCacheAttempt) MarkResponseStarted() {
	if attempt == nil || !attempt.IsLeader() {
		return
	}
	attempt.finishOnce.Do(func() {
		attempt.runtime.finishFlight(attempt.flight, attempt.claimedKeys, claudePromptCacheFlightStarted)
	})
}

// MarkResponseProgress extends the short-lived started protection while a
// streaming leader is still receiving upstream events.
func (attempt *ClaudePromptCacheAttempt) MarkResponseProgress() {
	if attempt == nil || !attempt.IsLeader() {
		return
	}
	attempt.runtime.extendStartedPrefixes(attempt.flight, attempt.claimedKeys)
}

// StartResponseHeartbeat keeps a response-owning flight protected even when
// an upstream stream is temporarily silent between response events.
func (attempt *ClaudePromptCacheAttempt) StartResponseHeartbeat() func() {
	if attempt == nil || !attempt.IsLeader() {
		return func() {}
	}
	stopHeartbeat := make(chan struct{})
	var stopOnce sync.Once
	go func() {
		heartbeatTicker := time.NewTicker(claudePromptCacheStartedLifetime / 2)
		defer heartbeatTicker.Stop()
		for {
			select {
			case <-heartbeatTicker.C:
				attempt.MarkResponseProgress()
			case <-stopHeartbeat:
				return
			}
		}
	}()
	return func() {
		stopOnce.Do(func() {
			close(stopHeartbeat)
		})
	}
}

// Fail releases followers and revokes this attempt's temporary started state.
func (attempt *ClaudePromptCacheAttempt) Fail() {
	if attempt == nil || !attempt.IsLeader() {
		return
	}
	attempt.finishOnce.Do(func() {
		attempt.runtime.finishFlight(attempt.flight, attempt.claimedKeys, claudePromptCacheFlightFailed)
	})
	attempt.runtime.clearStartedPrefixes(attempt.flight, attempt.claimedKeys)
}

// Complete records aggregate cache evidence after a terminal successful response.
func (attempt *ClaudePromptCacheAttempt) Complete(cacheReadTokens, cacheCreationTokens int64) {
	if attempt == nil || attempt.runtime == nil || attempt.plan == nil {
		return
	}
	attempt.runtime.initialize()
	attempt.runtime.completePlan(attempt.plan, attempt.flight, cacheReadTokens, cacheCreationTokens)
}

func (runtime *ClaudePromptCacheRuntime) acquireFlight(plan *ClaudePromptCachePlan) (*claudePromptCacheFlight, []string, *claudePromptCacheFlight) {
	now := runtime.now()
	runtime.mutex.Lock()
	defer runtime.mutex.Unlock()
	runtime.cleanupLocked(now)

	scopeState := runtime.ensureScopeLocked(plan.ScopeKey, now)
	coldPrefixes := make([]ClaudePromptCachePrefix, 0, len(plan.Prefixes))
	var overlappingFlight *claudePromptCacheFlight
	overlappingDepth := -1
	for _, prefix := range plan.Prefixes {
		prefixState := scopeState.prefixes[prefix.Key]
		if prefixState != nil {
			prefixState.lastTouched = now
			if prefixState.confirmedUntil.After(now) || prefixState.startedUntil.After(now) {
				continue
			}
		}
		if flight := runtime.flights[prefix.Key]; flight != nil {
			if prefix.Depth > overlappingDepth {
				overlappingFlight = flight
				overlappingDepth = prefix.Depth
			}
			continue
		}
		coldPrefixes = append(coldPrefixes, prefix)
	}

	if overlappingFlight != nil {
		return nil, nil, overlappingFlight
	}
	if len(coldPrefixes) == 0 || len(runtime.flights) >= claudePromptCacheMaxActiveFlights {
		return nil, nil, nil
	}

	claimedKeys := make([]string, 0, len(coldPrefixes))
	for _, prefix := range coldPrefixes {
		claimedKeys = append(claimedKeys, prefix.Key)
		prefixState := scopeState.prefixes[prefix.Key]
		if prefixState == nil {
			prefixState = &claudePromptCachePrefixState{}
			scopeState.prefixes[prefix.Key] = prefixState
		}
		prefixState.ttl = prefix.TTL
		prefixState.lastTouched = now
	}
	flight := &claudePromptCacheFlight{
		keys:    append([]string(nil), claimedKeys...),
		done:    make(chan struct{}),
		outcome: claudePromptCacheFlightPending,
	}
	for _, prefixKey := range claimedKeys {
		runtime.flights[prefixKey] = flight
	}
	return flight, claimedKeys, nil
}

func (runtime *ClaudePromptCacheRuntime) finishFlight(flight *claudePromptCacheFlight, claimedKeys []string, outcome claudePromptCacheFlightOutcome) {
	if runtime == nil || flight == nil {
		return
	}
	now := runtime.now()
	runtime.mutex.Lock()
	for _, prefixKey := range claimedKeys {
		if runtime.flights[prefixKey] != flight {
			continue
		}
		delete(runtime.flights, prefixKey)
		if outcome == claudePromptCacheFlightStarted {
			for _, scopeState := range runtime.scopes {
				if prefixState := scopeState.prefixes[prefixKey]; prefixState != nil {
					prefixState.startedUntil = now.Add(claudePromptCacheStartedLifetime)
					prefixState.startedBy = flight
					prefixState.lastTouched = now
				}
			}
		}
	}
	runtime.mutex.Unlock()
	flight.completeOnce.Do(func() {
		flight.outcome = outcome
		close(flight.done)
	})
}

func (runtime *ClaudePromptCacheRuntime) clearStartedPrefixes(
	flight *claudePromptCacheFlight,
	claimedKeys []string,
) {
	if runtime == nil || flight == nil {
		return
	}
	now := runtime.now()
	runtime.mutex.Lock()
	defer runtime.mutex.Unlock()
	for _, prefixKey := range claimedKeys {
		for _, scopeState := range runtime.scopes {
			prefixState := scopeState.prefixes[prefixKey]
			if prefixState == nil || prefixState.startedBy != flight {
				continue
			}
			prefixState.startedUntil = time.Time{}
			prefixState.startedBy = nil
			prefixState.lastTouched = now
		}
	}
}

func (runtime *ClaudePromptCacheRuntime) extendStartedPrefixes(
	flight *claudePromptCacheFlight,
	claimedKeys []string,
) {
	if runtime == nil || flight == nil {
		return
	}
	now := runtime.now()
	runtime.mutex.Lock()
	defer runtime.mutex.Unlock()
	for _, prefixKey := range claimedKeys {
		for _, scopeState := range runtime.scopes {
			prefixState := scopeState.prefixes[prefixKey]
			if prefixState == nil || prefixState.startedBy != flight {
				continue
			}
			prefixState.startedUntil = now.Add(claudePromptCacheStartedLifetime)
			prefixState.lastTouched = now
		}
	}
}

func (runtime *ClaudePromptCacheRuntime) completePlan(
	plan *ClaudePromptCachePlan,
	flight *claudePromptCacheFlight,
	cacheReadTokens,
	cacheCreationTokens int64,
) {
	now := runtime.now()
	runtime.mutex.Lock()
	defer runtime.mutex.Unlock()
	runtime.cleanupLocked(now)
	scopeState := runtime.ensureScopeLocked(plan.ScopeKey, now)

	// Anthropic reports request-level cache totals, not per-breakpoint evidence.
	// Use them only to learn short-lived candidate tool cuts; never mark every
	// explicit prefix as confirmed from an aggregate value.
	hasAggregateCacheEvidence := cacheReadTokens > 0 || cacheCreationTokens > 0
	candidateCuts := make(map[int]string)
	for _, prefix := range plan.Prefixes {
		prefixState := scopeState.prefixes[prefix.Key]
		if prefixState == nil {
			prefixState = &claudePromptCachePrefixState{}
			scopeState.prefixes[prefix.Key] = prefixState
		}
		prefixState.lastTouched = now
		if prefixState.ttl <= 0 {
			prefixState.ttl = prefix.TTL
		}
		if hasAggregateCacheEvidence && prefix.Kind == "tools" && prefix.ToolCut > 0 {
			candidateCuts[prefix.ToolCut] = prefix.Key
		}
		if flight != nil && prefixState.startedBy == flight {
			prefixState.startedUntil = time.Time{}
			prefixState.startedBy = nil
		}
	}

	if len(plan.ToolFingerprints) == 0 ||
		len(plan.ToolFingerprints) > claudePromptCacheMaxTrackedTools ||
		len(candidateCuts) == 0 {
		return
	}
	sequence := findClaudeWarmSequence(scopeState.sequences, plan.ToolSequenceKey)
	if sequence == nil {
		sequence = &claudePromptCacheWarmSequence{
			sequenceKey:      plan.ToolSequenceKey,
			toolFingerprints: cloneClaudeToolFingerprints(plan.ToolFingerprints),
			candidateCuts:    make(map[int]string),
		}
		scopeState.sequences = append(scopeState.sequences, sequence)
	}
	for cut, prefixKey := range candidateCuts {
		sequence.candidateCuts[cut] = prefixKey
	}
	sequence.validUntil = now.Add(localClaudePromptCacheTTL(claudePromptCacheDefaultTTL))
	sequence.lastSuccess = now
	sequence.lastUsed = now
	trimClaudeWarmSequences(scopeState)
	trimClaudePrefixStates(scopeState, now, runtime.flights)
}

func (runtime *ClaudePromptCacheRuntime) findStableToolCut(scopeKey string, current [][32]byte, now time.Time) int {
	if runtime == nil || len(current) == 0 || len(current) > claudePromptCacheMaxTrackedTools {
		return 0
	}
	runtime.mutex.Lock()
	defer runtime.mutex.Unlock()
	runtime.cleanupLocked(now)
	scopeState := runtime.scopes[scopeKey]
	if scopeState == nil {
		return 0
	}
	scopeState.lastAccess = now

	bestCut := 0
	for _, sequence := range scopeState.sequences {
		if !sequence.validUntil.After(now) {
			continue
		}
		lcpLength := longestCommonClaudeToolPrefix(current, sequence.toolFingerprints)
		for cut := range sequence.candidateCuts {
			if cut <= 0 || cut > lcpLength {
				continue
			}
			if cut > bestCut {
				bestCut = cut
				sequence.lastUsed = now
			}
		}
	}
	return bestCut
}

func (runtime *ClaudePromptCacheRuntime) ensureScopeLocked(scopeKey string, now time.Time) *claudePromptCacheScopeState {
	scopeState := runtime.scopes[scopeKey]
	if scopeState == nil {
		scopeState = &claudePromptCacheScopeState{prefixes: make(map[string]*claudePromptCachePrefixState)}
		runtime.scopes[scopeKey] = scopeState
	}
	scopeState.lastAccess = now
	return scopeState
}

func (runtime *ClaudePromptCacheRuntime) cleanupLocked(now time.Time) {
	runtime.cleanupDiagnosticsLocked(now)
	for scopeKey, scopeState := range runtime.scopes {
		trimClaudePrefixStates(scopeState, now, runtime.flights)
		activeSequences := scopeState.sequences[:0]
		for _, sequence := range scopeState.sequences {
			if sequence.validUntil.After(now) && len(sequence.candidateCuts) > 0 {
				activeSequences = append(activeSequences, sequence)
			}
		}
		scopeState.sequences = activeSequences
		if len(scopeState.prefixes) == 0 && len(scopeState.sequences) == 0 && now.Sub(scopeState.lastAccess) > claudePromptCacheExtendedTTL {
			delete(runtime.scopes, scopeKey)
		}
	}
	if len(runtime.scopes) <= claudePromptCacheMaxScopes {
		return
	}
	type scopeAccess struct {
		key        string
		lastAccess time.Time
	}
	orderedScopes := make([]scopeAccess, 0, len(runtime.scopes))
	for scopeKey, scopeState := range runtime.scopes {
		if claudePromptCacheScopeHasProtectedPrefix(scopeState, runtime.flights, now) {
			continue
		}
		orderedScopes = append(orderedScopes, scopeAccess{key: scopeKey, lastAccess: scopeState.lastAccess})
	}
	sort.Slice(orderedScopes, func(left, right int) bool {
		return orderedScopes[left].lastAccess.Before(orderedScopes[right].lastAccess)
	})
	for len(runtime.scopes) > claudePromptCacheMaxScopes && len(orderedScopes) > 0 {
		delete(runtime.scopes, orderedScopes[0].key)
		orderedScopes = orderedScopes[1:]
	}
}

func claudePromptCacheScopeHasProtectedPrefix(
	scopeState *claudePromptCacheScopeState,
	activeFlights map[string]*claudePromptCacheFlight,
	now time.Time,
) bool {
	if scopeState == nil {
		return false
	}
	for prefixKey, prefixState := range scopeState.prefixes {
		if activeFlights[prefixKey] != nil || prefixState.startedUntil.After(now) {
			return true
		}
	}
	return false
}

func (runtime *ClaudePromptCacheRuntime) cleanupDiagnosticsLocked(now time.Time) {
	for scopeKey, disabledUntil := range runtime.diagnosticsDisabled {
		if !disabledUntil.After(now) {
			delete(runtime.diagnosticsDisabled, scopeKey)
		}
	}
	for diagnosticKey, state := range runtime.diagnostics {
		if state == nil || now.Sub(state.lastUsed) > claudePromptCacheDiagnosticLifetime {
			delete(runtime.diagnostics, diagnosticKey)
		}
	}
}

func (runtime *ClaudePromptCacheRuntime) trimDiagnosticsLocked() {
	if len(runtime.diagnostics) <= claudePromptCacheMaxDiagnosticSessions {
		return
	}
	type diagnosticAccess struct {
		key      string
		lastUsed time.Time
	}
	orderedDiagnostics := make([]diagnosticAccess, 0, len(runtime.diagnostics))
	for diagnosticKey, state := range runtime.diagnostics {
		orderedDiagnostics = append(orderedDiagnostics, diagnosticAccess{
			key:      diagnosticKey,
			lastUsed: state.lastUsed,
		})
	}
	sort.Slice(orderedDiagnostics, func(left, right int) bool {
		return orderedDiagnostics[left].lastUsed.Before(orderedDiagnostics[right].lastUsed)
	})
	for len(runtime.diagnostics) > claudePromptCacheMaxDiagnosticSessions && len(orderedDiagnostics) > 0 {
		delete(runtime.diagnostics, orderedDiagnostics[0].key)
		orderedDiagnostics = orderedDiagnostics[1:]
	}
}

// StripAllClaudeCacheControls removes every client/top-level/in-body cache_control
// marker so adaptive planning can re-apply CPA's breakpoint strategy from scratch.
func StripAllClaudeCacheControls(payload []byte) []byte {
	if len(payload) == 0 || !gjson.ValidBytes(payload) {
		return payload
	}
	updatedPayload := payload
	if gjson.GetBytes(updatedPayload, "cache_control").Exists() {
		if nextPayload, errDelete := sjson.DeleteBytes(updatedPayload, "cache_control"); errDelete == nil {
			updatedPayload = nextPayload
		}
	}
	locations, invalidPaths := collectClaudeCacheBreakpoints(updatedPayload)
	for _, invalidPath := range invalidPaths {
		if nextPayload, errDelete := sjson.DeleteBytes(updatedPayload, invalidPath); errDelete == nil {
			updatedPayload = nextPayload
		}
	}
	for _, location := range locations {
		if nextPayload, errDelete := sjson.DeleteBytes(updatedPayload, location.path); errDelete == nil {
			updatedPayload = nextPayload
		}
	}
	return updatedPayload
}

func collectClaudeCacheBreakpoints(payload []byte) ([]claudeBreakpointLocation, []string) {
	locations := make([]claudeBreakpointLocation, 0, ClaudePromptCacheMaxBreakpoints)
	invalidPaths := make([]string, 0)
	tools := gjson.GetBytes(payload, "tools")
	if tools.IsArray() {
		tools.ForEach(func(index, tool gjson.Result) bool {
			cacheControl := tool.Get("cache_control")
			if !cacheControl.Exists() {
				return true
			}
			path := fmt.Sprintf("tools.%d.cache_control", index.Int())
			if !isValidClaudeCacheControl(cacheControl) {
				invalidPaths = append(invalidPaths, path)
				return true
			}
			locations = append(locations, claudeBreakpointLocation{
				kind:         "tools",
				path:         path,
				primaryIndex: int(index.Int()),
				depth:        int(index.Int()) + 1,
				cacheControl: cacheControl.Raw,
			})
			return true
		})
	}

	system := gjson.GetBytes(payload, "system")
	if system.IsArray() {
		system.ForEach(func(index, block gjson.Result) bool {
			cacheControl := block.Get("cache_control")
			if !cacheControl.Exists() {
				return true
			}
			path := fmt.Sprintf("system.%d.cache_control", index.Int())
			if !isValidClaudeCacheControl(cacheControl) {
				invalidPaths = append(invalidPaths, path)
				return true
			}
			locations = append(locations, claudeBreakpointLocation{
				kind:         "system",
				path:         path,
				primaryIndex: int(index.Int()),
				depth:        1_000_000 + int(index.Int()) + 1,
				cacheControl: cacheControl.Raw,
			})
			return true
		})
	}

	messages := gjson.GetBytes(payload, "messages")
	if messages.IsArray() {
		messages.ForEach(func(messageIndex, message gjson.Result) bool {
			content := message.Get("content")
			if !content.IsArray() {
				return true
			}
			content.ForEach(func(contentIndex, block gjson.Result) bool {
				cacheControl := block.Get("cache_control")
				if !cacheControl.Exists() {
					return true
				}
				path := fmt.Sprintf("messages.%d.content.%d.cache_control", messageIndex.Int(), contentIndex.Int())
				if !isValidClaudeCacheControl(cacheControl) {
					invalidPaths = append(invalidPaths, path)
					return true
				}
				locations = append(locations, claudeBreakpointLocation{
					kind:           "messages",
					path:           path,
					primaryIndex:   int(messageIndex.Int()),
					secondaryIndex: int(contentIndex.Int()),
					depth:          2_000_000 + int(messageIndex.Int())*10_000 + int(contentIndex.Int()) + 1,
					cacheControl:   cacheControl.Raw,
				})
				return true
			})
			return true
		})
	}
	return locations, invalidPaths
}

func selectExistingClaudeBreakpoints(locations []claudeBreakpointLocation, maxBreakpoints int) []claudeBreakpointLocation {
	if len(locations) <= maxBreakpoints {
		return append([]claudeBreakpointLocation(nil), locations...)
	}
	ranked := append([]claudeBreakpointLocation(nil), locations...)
	lastToolIndex := -1
	lastSystemIndex := -1
	for _, location := range ranked {
		switch location.kind {
		case "tools":
			if location.primaryIndex > lastToolIndex {
				lastToolIndex = location.primaryIndex
			}
		case "system":
			if location.primaryIndex > lastSystemIndex {
				lastSystemIndex = location.primaryIndex
			}
		}
	}
	priority := func(location claudeBreakpointLocation) int {
		switch {
		case location.kind == "tools" && location.primaryIndex == lastToolIndex:
			return 0
		case location.kind == "system" && location.primaryIndex == lastSystemIndex:
			return 1
		case location.kind == "messages":
			return 2
		case location.kind == "tools":
			return 3
		default:
			return 4
		}
	}
	sort.SliceStable(ranked, func(left, right int) bool {
		leftPriority := priority(ranked[left])
		rightPriority := priority(ranked[right])
		if leftPriority != rightPriority {
			return leftPriority < rightPriority
		}
		return ranked[left].depth > ranked[right].depth
	})
	return ranked[:maxBreakpoints]
}

func addClaudeSystemTailBreakpoint(
	payload []byte,
	selectedPaths map[string]struct{},
) ([]byte, bool, string) {
	system := gjson.GetBytes(payload, "system")
	if !system.Exists() {
		return payload, false, ""
	}
	if system.Type == gjson.String {
		text := system.String()
		if strings.TrimSpace(text) == "" {
			return payload, false, ""
		}
		block := []byte(`{"type":"text","text":"","cache_control":{"type":"ephemeral"}}`)
		block, _ = sjson.SetBytes(block, "text", text)
		updated, errSet := sjson.SetRawBytes(payload, "system", []byte("["+string(block)+"]"))
		if errSet != nil {
			return payload, false, ""
		}
		path := "system.0.cache_control"
		selectedPaths[path] = struct{}{}
		return updated, true, path
	}
	if !system.IsArray() {
		return payload, false, ""
	}
	count := int(system.Get("#").Int())
	if count == 0 {
		return payload, false, ""
	}
	path := fmt.Sprintf("system.%d.cache_control", count-1)
	if _, exists := selectedPaths[path]; exists || gjson.GetBytes(payload, path).Exists() {
		return payload, false, ""
	}
	updated, errSet := sjson.SetRawBytes(payload, path, []byte(claudePromptCacheAutomaticControlJSON))
	if errSet != nil {
		return payload, false, ""
	}
	selectedPaths[path] = struct{}{}
	return updated, true, path
}

func addClaudeHistoryBreakpoint(
	payload []byte,
	selectedPaths map[string]struct{},
) ([]byte, bool, string) {
	messages := gjson.GetBytes(payload, "messages")
	if !messages.IsArray() {
		return payload, false, ""
	}
	userIndexes := make([]int, 0)
	messages.ForEach(func(index, message gjson.Result) bool {
		if message.Get("role").String() == "user" {
			userIndexes = append(userIndexes, int(index.Int()))
		}
		return true
	})
	if len(userIndexes) < 2 {
		return payload, false, ""
	}
	messageIndex := userIndexes[len(userIndexes)-2]
	contentPath := fmt.Sprintf("messages.%d.content", messageIndex)
	content := gjson.GetBytes(payload, contentPath)
	if content.Type == gjson.String {
		block := []byte(`{"type":"text","text":"","cache_control":{"type":"ephemeral"}}`)
		block, _ = sjson.SetBytes(block, "text", content.String())
		updated, errSet := sjson.SetRawBytes(payload, contentPath, []byte("["+string(block)+"]"))
		if errSet != nil {
			return payload, false, ""
		}
		path := fmt.Sprintf("messages.%d.content.0.cache_control", messageIndex)
		selectedPaths[path] = struct{}{}
		return updated, true, path
	}
	if !content.IsArray() {
		return payload, false, ""
	}
	contentCount := int(content.Get("#").Int())
	if contentCount == 0 {
		return payload, false, ""
	}
	path := fmt.Sprintf("messages.%d.content.%d.cache_control", messageIndex, contentCount-1)
	if _, exists := selectedPaths[path]; exists || gjson.GetBytes(payload, path).Exists() {
		return payload, false, ""
	}
	updated, errSet := sjson.SetRawBytes(payload, path, []byte(claudePromptCacheAutomaticControlJSON))
	if errSet != nil {
		return payload, false, ""
	}
	selectedPaths[path] = struct{}{}
	return updated, true, path
}

func buildClaudePromptCachePrefixes(
	scopeKey string,
	payload []byte,
	locations []claudeBreakpointLocation,
	toolPrefixKeys []string,
	runtime *ClaudePromptCacheRuntime,
	now time.Time,
) []ClaudePromptCachePrefix {
	prefixes := make([]ClaudePromptCachePrefix, 0, len(locations))
	for _, location := range locations {
		prefixKey := ""
		toolCut := 0
		switch location.kind {
		case "tools":
			toolCut = location.primaryIndex + 1
			if toolCut > 0 && toolCut <= len(toolPrefixKeys) {
				prefixKey = hashClaudePromptCachePrefix(scopeKey, "tools", toolPrefixKeys[toolCut-1])
			}
		case "system":
			prefixKey = hashClaudePromptCachePrefix(scopeKey, "system", buildClaudeSystemPrefixMaterial(payload, location.primaryIndex))
		case "messages":
			prefixKey = hashClaudePromptCachePrefix(scopeKey, "messages", buildClaudeMessagePrefixMaterial(payload, location.primaryIndex, location.secondaryIndex))
		}
		if prefixKey == "" {
			continue
		}
		prefixes = append(prefixes, ClaudePromptCachePrefix{
			Key:       prefixKey,
			Kind:      location.kind,
			Depth:     location.depth,
			TTL:       claudeCacheControlTTL(location.cacheControl),
			ToolCut:   toolCut,
			Added:     location.added,
			Confirmed: runtime.prefixConfirmed(scopeKey, prefixKey, now),
		})
	}
	return prefixes
}

func buildClaudeAutomaticHistoryPrefixes(
	scopeKey string,
	payload []byte,
	ttl time.Duration,
	added bool,
	runtime *ClaudePromptCacheRuntime,
	now time.Time,
) []ClaudePromptCachePrefix {
	const maxAutomaticHistoryPrefixes = 20
	type messageBoundary struct {
		messageIndex int
		contentIndex int
		depth        int
	}

	messages := gjson.GetBytes(payload, "messages")
	if !messages.IsArray() {
		return nil
	}
	boundaries := make([]messageBoundary, 0, maxAutomaticHistoryPrefixes)
	messages.ForEach(func(messageIndex, message gjson.Result) bool {
		content := message.Get("content")
		if content.IsArray() {
			content.ForEach(func(contentIndex, _ gjson.Result) bool {
				boundaries = append(boundaries, messageBoundary{
					messageIndex: int(messageIndex.Int()),
					contentIndex: int(contentIndex.Int()),
					depth:        2_000_000 + int(messageIndex.Int())*10_000 + int(contentIndex.Int()) + 1,
				})
				return true
			})
			return true
		}
		if content.Type == gjson.String && strings.TrimSpace(content.String()) != "" {
			boundaries = append(boundaries, messageBoundary{
				messageIndex: int(messageIndex.Int()),
				contentIndex: -1,
				depth:        2_000_000 + int(messageIndex.Int())*10_000 + 1,
			})
		}
		return true
	})
	if len(boundaries) > maxAutomaticHistoryPrefixes {
		boundaries = boundaries[len(boundaries)-maxAutomaticHistoryPrefixes:]
	}

	prefixes := make([]ClaudePromptCachePrefix, 0, len(boundaries))
	for boundaryIndex, boundary := range boundaries {
		prefixKey := hashClaudePromptCachePrefix(
			scopeKey,
			"automatic",
			buildClaudeMessagePrefixMaterial(payload, boundary.messageIndex, boundary.contentIndex),
		)
		prefixes = append(prefixes, ClaudePromptCachePrefix{
			Key:       prefixKey,
			Kind:      "automatic",
			Depth:     boundary.depth,
			TTL:       ttl,
			Added:     added && boundaryIndex == len(boundaries)-1,
			Confirmed: runtime.prefixConfirmed(scopeKey, prefixKey, now),
		})
	}
	return prefixes
}

func (runtime *ClaudePromptCacheRuntime) prefixConfirmed(scopeKey, prefixKey string, now time.Time) bool {
	runtime.mutex.Lock()
	defer runtime.mutex.Unlock()
	scopeState := runtime.scopes[scopeKey]
	if scopeState == nil {
		return false
	}
	prefixState := scopeState.prefixes[prefixKey]
	return prefixState != nil && prefixState.confirmedUntil.After(now)
}

func fingerprintClaudeTools(payload []byte) ([][32]byte, []string) {
	tools := gjson.GetBytes(payload, "tools")
	if !tools.IsArray() {
		return nil, nil
	}
	fingerprints := make([][32]byte, 0, int(tools.Get("#").Int()))
	prefixKeys := make([]string, 0, int(tools.Get("#").Int()))
	chain := sha256.Sum256([]byte(claudePromptCacheFingerprintDomain))
	tools.ForEach(func(index, tool gjson.Result) bool {
		toolWithoutCacheControl := tool.Raw
		if tool.Get("cache_control").Exists() {
			toolWithoutCacheControl, _ = sjson.Delete(tool.Raw, "cache_control")
		}
		fingerprint := sha256.Sum256(append([]byte(claudePromptCacheFingerprintDomain+"\x00"), []byte(toolWithoutCacheControl)...))
		fingerprints = append(fingerprints, fingerprint)
		chainInput := make([]byte, 0, len(chain)+len(fingerprint)+32)
		chainInput = append(chainInput, []byte(claudePromptCacheFingerprintDomain)...)
		chainInput = append(chainInput, 0)
		chainInput = append(chainInput, []byte(fmt.Sprintf("%d", index.Int()+1))...)
		chainInput = append(chainInput, 0)
		chainInput = append(chainInput, chain[:]...)
		chainInput = append(chainInput, fingerprint[:]...)
		chain = sha256.Sum256(chainInput)
		prefixKeys = append(prefixKeys, hex.EncodeToString(chain[:]))
		return true
	})
	return fingerprints, prefixKeys
}

func fingerprintClaudeToolSequence(fingerprints [][32]byte) string {
	hasher := sha256.New()
	_, _ = hasher.Write([]byte(claudePromptCacheSequenceFingerprintV1))
	for _, fingerprint := range fingerprints {
		_, _ = hasher.Write(fingerprint[:])
	}
	return hex.EncodeToString(hasher.Sum(nil))
}

func longestCommonClaudeToolPrefix(left, right [][32]byte) int {
	limit := len(left)
	if len(right) < limit {
		limit = len(right)
	}
	for index := 0; index < limit; index++ {
		if left[index] != right[index] {
			return index
		}
	}
	return limit
}

func hashClaudePromptCachePrefix(scopeKey, kind, material string) string {
	hasher := sha256.New()
	_, _ = hasher.Write([]byte(claudePromptCachePrefixFingerprintV1))
	_, _ = hasher.Write([]byte{0})
	_, _ = hasher.Write([]byte(scopeKey))
	_, _ = hasher.Write([]byte{0})
	_, _ = hasher.Write([]byte(kind))
	_, _ = hasher.Write([]byte{0})
	_, _ = hasher.Write([]byte(material))
	return hex.EncodeToString(hasher.Sum(nil))
}

func buildClaudeSystemPrefixMaterial(payload []byte, systemEndIndex int) string {
	var buffer bytes.Buffer
	writeClaudeToolsWithoutCacheControl(&buffer, payload, -1)
	system := gjson.GetBytes(payload, "system")
	if system.Type == gjson.String {
		buffer.WriteString(system.Raw)
		return buffer.String()
	}
	if system.IsArray() {
		system.ForEach(func(index, block gjson.Result) bool {
			if int(index.Int()) > systemEndIndex {
				return false
			}
			buffer.WriteString(stripClaudeObjectCacheControl(block.Raw))
			buffer.WriteByte('\n')
			return true
		})
	}
	return buffer.String()
}

func buildClaudeMessagePrefixMaterial(payload []byte, messageEndIndex, contentEndIndex int) string {
	var buffer bytes.Buffer
	writeClaudeToolsWithoutCacheControl(&buffer, payload, -1)
	writeClaudeSystemWithoutCacheControl(&buffer, payload)
	messages := gjson.GetBytes(payload, "messages")
	if !messages.IsArray() {
		return buffer.String()
	}
	messages.ForEach(func(index, message gjson.Result) bool {
		messageIndex := int(index.Int())
		if messageIndex > messageEndIndex {
			return false
		}
		if messageIndex < messageEndIndex {
			buffer.WriteString(stripClaudeMessageCacheControls(message.Raw, -1))
			buffer.WriteByte('\n')
			return true
		}
		buffer.WriteString(stripClaudeMessageCacheControls(message.Raw, contentEndIndex))
		buffer.WriteByte('\n')
		return false
	})
	return buffer.String()
}

func writeClaudeToolsWithoutCacheControl(buffer *bytes.Buffer, payload []byte, endIndex int) {
	tools := gjson.GetBytes(payload, "tools")
	if !tools.IsArray() {
		return
	}
	tools.ForEach(func(index, tool gjson.Result) bool {
		if endIndex >= 0 && int(index.Int()) > endIndex {
			return false
		}
		buffer.WriteString(stripClaudeObjectCacheControl(tool.Raw))
		buffer.WriteByte('\n')
		return true
	})
}

func writeClaudeSystemWithoutCacheControl(buffer *bytes.Buffer, payload []byte) {
	system := gjson.GetBytes(payload, "system")
	if system.Type == gjson.String {
		buffer.WriteString(system.Raw)
		buffer.WriteByte('\n')
		return
	}
	if !system.IsArray() {
		return
	}
	system.ForEach(func(_, block gjson.Result) bool {
		buffer.WriteString(stripClaudeObjectCacheControl(block.Raw))
		buffer.WriteByte('\n')
		return true
	})
}

func stripClaudeObjectCacheControl(raw string) string {
	if !gjson.Get(raw, "cache_control").Exists() {
		return raw
	}
	updated, errDelete := sjson.Delete(raw, "cache_control")
	if errDelete != nil {
		return raw
	}
	return updated
}

func stripClaudeMessageCacheControls(raw string, contentEndIndex int) string {
	content := gjson.Get(raw, "content")
	if !content.IsArray() {
		return raw
	}
	parts := content.Array()
	if contentEndIndex >= 0 && contentEndIndex+1 < len(parts) {
		parts = parts[:contentEndIndex+1]
	}
	partStrings := make([]string, 0, len(parts))
	for _, part := range parts {
		partStrings = append(partStrings, stripClaudeObjectCacheControl(part.Raw))
	}
	updated, errSet := sjson.SetRaw(raw, "content", "["+strings.Join(partStrings, ",")+"]")
	if errSet != nil {
		return raw
	}
	return updated
}

func isValidClaudeCacheControl(cacheControl gjson.Result) bool {
	if !cacheControl.Exists() || !cacheControl.IsObject() || cacheControl.Get("type").String() != "ephemeral" {
		return false
	}
	ttl := cacheControl.Get("ttl")
	return !ttl.Exists() || (ttl.Type == gjson.String && (ttl.String() == "5m" || ttl.String() == "1h"))
}

func claudeCacheControlTTL(raw string) time.Duration {
	if gjson.Get(raw, "ttl").String() == "1h" {
		return claudePromptCacheExtendedTTL
	}
	return claudePromptCacheDefaultTTL
}

func localClaudePromptCacheTTL(ttl time.Duration) time.Duration {
	if ttl >= claudePromptCacheExtendedTTL {
		return ttl - claudePromptCacheExtendedSafetyMargin
	}
	if ttl > claudePromptCacheDefaultSafetyMargin {
		return ttl - claudePromptCacheDefaultSafetyMargin
	}
	return ttl
}

func cloneClaudeToolFingerprints(input [][32]byte) [][32]byte {
	return append([][32]byte(nil), input...)
}

func findClaudeWarmSequence(sequences []*claudePromptCacheWarmSequence, sequenceKey string) *claudePromptCacheWarmSequence {
	for _, sequence := range sequences {
		if sequence.sequenceKey == sequenceKey {
			return sequence
		}
	}
	return nil
}

func trimClaudeWarmSequences(scopeState *claudePromptCacheScopeState) {
	if scopeState == nil || len(scopeState.sequences) <= claudePromptCacheMaxSequencesPerScope {
		return
	}
	sort.Slice(scopeState.sequences, func(left, right int) bool {
		return scopeState.sequences[left].lastUsed.After(scopeState.sequences[right].lastUsed)
	})
	scopeState.sequences = scopeState.sequences[:claudePromptCacheMaxSequencesPerScope]
}

func trimClaudePrefixStates(
	scopeState *claudePromptCacheScopeState,
	now time.Time,
	activeFlights map[string]*claudePromptCacheFlight,
) {
	if scopeState == nil {
		return
	}
	for prefixKey, prefixState := range scopeState.prefixes {
		if activeFlights[prefixKey] != nil || prefixState.startedUntil.After(now) {
			continue
		}
		if !prefixState.confirmedUntil.After(now) && !prefixState.startedUntil.After(now) {
			delete(scopeState.prefixes, prefixKey)
		}
	}
	if len(scopeState.prefixes) <= claudePromptCacheMaxPrefixesPerScope {
		return
	}
	type prefixAccess struct {
		key         string
		lastTouched time.Time
	}
	orderedPrefixes := make([]prefixAccess, 0, len(scopeState.prefixes))
	for prefixKey, prefixState := range scopeState.prefixes {
		if activeFlights[prefixKey] != nil || prefixState.startedUntil.After(now) {
			continue
		}
		orderedPrefixes = append(orderedPrefixes, prefixAccess{key: prefixKey, lastTouched: prefixState.lastTouched})
	}
	sort.Slice(orderedPrefixes, func(left, right int) bool {
		return orderedPrefixes[left].lastTouched.Before(orderedPrefixes[right].lastTouched)
	})
	for len(scopeState.prefixes) > claudePromptCacheMaxPrefixesPerScope && len(orderedPrefixes) > 0 {
		delete(scopeState.prefixes, orderedPrefixes[0].key)
		orderedPrefixes = orderedPrefixes[1:]
	}
}
