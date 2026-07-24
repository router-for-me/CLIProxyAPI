package helps

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/tidwall/gjson"
)

func TestPlanClaudePromptCacheUsesAutomaticHistoryForOfficialAnthropic(t *testing.T) {
	runtime := NewClaudePromptCacheRuntime()
	payload := buildClaudePromptCacheTestPayload([]string{"Read", "Bash"}, false)

	plannedPayload, plan := runtime.PlanClaudePromptCache(
		"official-scope",
		payload,
		ClaudePromptCacheCapabilities{AutomaticHistory: true, ExplicitHistory: true},
	)

	if plan == nil {
		t.Fatal("PlanClaudePromptCache() plan = nil")
	}
	if !plan.Summary.AutomaticHistory {
		t.Fatal("plan.Summary.AutomaticHistory = false, want true")
	}
	if !isValidClaudeCacheControl(gjson.GetBytes(plannedPayload, "cache_control")) {
		t.Fatalf("top-level cache_control = %s, want valid automatic control", gjson.GetBytes(plannedPayload, "cache_control").Raw)
	}
	if !gjson.GetBytes(plannedPayload, "messages.0.content.0.cache_control").Exists() {
		t.Fatal("official Anthropic plan missing second-to-last user breakpoint")
	}
	locations, invalidPaths := collectClaudeCacheBreakpoints(plannedPayload)
	if len(invalidPaths) != 0 {
		t.Fatalf("invalid cache-control paths = %v, want none", invalidPaths)
	}
	if len(locations) != 3 {
		t.Fatalf("explicit breakpoint count = %d, want 3 (tools-tail + system-tail + history)", len(locations))
	}
	if plan.Summary.FinalBreakpoints != len(locations) {
		t.Fatalf("summary final breakpoints = %d, want %d", plan.Summary.FinalBreakpoints, len(locations))
	}
	foundAutomaticPrefix := false
	for _, prefix := range plan.Prefixes {
		if prefix.Kind == "automatic" {
			foundAutomaticPrefix = true
			break
		}
	}
	if !foundAutomaticPrefix {
		t.Fatal("automatic history is present in the payload but absent from the runtime plan")
	}
}

func TestPlanClaudePromptCacheAutomaticHistoryKeepsSharedConversationPrefixes(t *testing.T) {
	runtime := NewClaudePromptCacheRuntime()
	firstPayload := []byte(`{
		"messages":[
			{"role":"user","content":"first"},
			{"role":"assistant","content":[{"type":"text","text":"answer"}]},
			{"role":"user","content":[{"type":"text","text":"second"}]}
		]
	}`)
	appendedPayload := []byte(`{
		"messages":[
			{"role":"user","content":"first"},
			{"role":"assistant","content":[{"type":"text","text":"answer"}]},
			{"role":"user","content":[{"type":"text","text":"second"}]},
			{"role":"assistant","content":[{"type":"text","text":"next answer"}]},
			{"role":"user","content":[{"type":"text","text":"third"}]}
		]
	}`)

	_, firstPlan := runtime.PlanClaudePromptCache(
		"automatic-prefix-scope",
		firstPayload,
		ClaudePromptCacheCapabilities{AutomaticHistory: true},
	)
	_, appendedPlan := runtime.PlanClaudePromptCache(
		"automatic-prefix-scope",
		appendedPayload,
		ClaudePromptCacheCapabilities{AutomaticHistory: true},
	)
	firstAutomaticKeys := make(map[string]struct{})
	for _, prefix := range firstPlan.Prefixes {
		if prefix.Kind == "automatic" {
			firstAutomaticKeys[prefix.Key] = struct{}{}
		}
	}
	sharedAutomaticPrefix := false
	for _, prefix := range appendedPlan.Prefixes {
		if prefix.Kind != "automatic" {
			continue
		}
		if _, exists := firstAutomaticKeys[prefix.Key]; exists {
			sharedAutomaticPrefix = true
			break
		}
	}
	if !sharedAutomaticPrefix {
		t.Fatal("appending messages removed every shared automatic-history prefix")
	}
}

func TestPlanClaudePromptCacheUsesExplicitHistoryForCompatibleProvider(t *testing.T) {
	runtime := NewClaudePromptCacheRuntime()
	payload := buildClaudePromptCacheTestPayload([]string{"Read", "Bash"}, true)

	plannedPayload, plan := runtime.PlanClaudePromptCache(
		"compatible-scope",
		payload,
		ClaudePromptCacheCapabilities{ExplicitHistory: true},
	)

	if plan == nil {
		t.Fatal("PlanClaudePromptCache() plan = nil")
	}
	if gjson.GetBytes(plannedPayload, "cache_control").Exists() {
		t.Fatalf("top-level cache_control = %s, want removed", gjson.GetBytes(plannedPayload, "cache_control").Raw)
	}
	if !gjson.GetBytes(plannedPayload, "messages.0.content.0.cache_control").Exists() {
		t.Fatal("compatible-provider history breakpoint is missing")
	}
	locations, invalidPaths := collectClaudeCacheBreakpoints(plannedPayload)
	if len(invalidPaths) != 0 {
		t.Fatalf("invalid cache-control paths = %v, want none", invalidPaths)
	}
	if len(locations) != 3 {
		t.Fatalf("explicit breakpoint count = %d, want 3", len(locations))
	}
}

func TestPlanClaudePromptCacheDeterministicallyLimitsExplicitBreakpoints(t *testing.T) {
	runtime := NewClaudePromptCacheRuntime()
	payload := []byte(`{
		"tools":[
			{"name":"A","input_schema":{"type":"object"},"cache_control":{"type":"ephemeral"}},
			{"name":"B","input_schema":{"type":"object"},"cache_control":{"type":"ephemeral"}},
			{"name":"C","input_schema":{"type":"object"},"cache_control":{"type":"ephemeral"}},
			{"name":"D","input_schema":{"type":"object"},"cache_control":{"type":"ephemeral"}},
			{"name":"E","input_schema":{"type":"object"},"cache_control":{"type":"ephemeral"}}
		],
		"system":[{"type":"text","text":"system","cache_control":{"type":"invalid"}}],
		"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]
	}`)

	firstPayload, firstPlan := runtime.PlanClaudePromptCache(
		"limited-scope",
		payload,
		ClaudePromptCacheCapabilities{AutomaticHistory: true},
	)
	secondPayload, secondPlan := runtime.PlanClaudePromptCache(
		"limited-scope",
		payload,
		ClaudePromptCacheCapabilities{AutomaticHistory: true},
	)

	if string(firstPayload) != string(secondPayload) {
		t.Fatalf("planner output is not deterministic\nfirst:  %s\nsecond: %s", firstPayload, secondPayload)
	}
	if firstPlan == nil || secondPlan == nil {
		t.Fatal("PlanClaudePromptCache() returned a nil plan")
	}
	locations, invalidPaths := collectClaudeCacheBreakpoints(firstPayload)
	if len(invalidPaths) != 0 {
		t.Fatalf("invalid cache-control paths = %v, want none", invalidPaths)
	}
	if len(locations) != ClaudePromptCacheMaxBreakpoints {
		t.Fatalf("explicit breakpoint count = %d, want %d", len(locations), ClaudePromptCacheMaxBreakpoints)
	}
	if firstPlan.Summary.RemovedBreakpoints != 2 {
		t.Fatalf("removed breakpoints = %d, want 2", firstPlan.Summary.RemovedBreakpoints)
	}
	if gjson.GetBytes(firstPayload, "system.0.cache_control").Exists() {
		t.Fatal("invalid system cache_control was not removed")
	}
}

func TestPlanClaudePromptCacheNormalizesTTLAfterAddingEarlierBreakpoint(t *testing.T) {
	runtime := NewClaudePromptCacheRuntime()
	payload := []byte(`{
		"cache_control":{"type":"ephemeral","ttl":"1h"},
		"tools":[{"name":"Read","input_schema":{"type":"object"}}],
		"system":[{"type":"text","text":"system","cache_control":{"type":"ephemeral","ttl":"1h"}}],
		"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]
	}`)

	plannedPayload, _ := runtime.PlanClaudePromptCache(
		"ttl-normalization-scope",
		payload,
		ClaudePromptCacheCapabilities{AutomaticHistory: true},
	)
	if !gjson.GetBytes(plannedPayload, "tools.0.cache_control").Exists() {
		t.Fatal("planner did not add the earlier tool breakpoint")
	}
	if gjson.GetBytes(plannedPayload, "system.0.cache_control.ttl").Exists() {
		t.Fatalf("later one-hour TTL was not normalized: %s", plannedPayload)
	}
	if gjson.GetBytes(plannedPayload, "cache_control.ttl").Exists() {
		t.Fatalf("top-level automatic one-hour TTL was not normalized: %s", plannedPayload)
	}
}

func TestClaudePromptCacheLearnsStableAppendedToolPrefix(t *testing.T) {
	runtime := NewClaudePromptCacheRuntime()
	currentTime := time.Date(2026, time.July, 10, 12, 0, 0, 0, time.UTC)
	runtime.now = func() time.Time { return currentTime }

	initialPayload := buildClaudePromptCacheTestPayload([]string{"Read", "Bash", "Glob"}, false)
	_, initialPlan := runtime.PlanClaudePromptCache(
		"stable-tools-scope",
		initialPayload,
		ClaudePromptCacheCapabilities{AutomaticHistory: true},
	)
	initialAttempt, errAcquire := runtime.Acquire(context.Background(), initialPlan, time.Second)
	if errAcquire != nil {
		t.Fatalf("Acquire() error = %v", errAcquire)
	}
	if !initialAttempt.IsLeader() {
		t.Fatal("initial attempt is not the cold-prefix leader")
	}
	initialAttempt.MarkResponseStarted()
	initialAttempt.Complete(0, 100)
	initialAttempt.Fail()

	appendedPayload := buildClaudePromptCacheTestPayload([]string{"Read", "Bash", "Glob", "Task"}, false)
	plannedPayload, appendedPlan := runtime.PlanClaudePromptCache(
		"stable-tools-scope",
		appendedPayload,
		ClaudePromptCacheCapabilities{AutomaticHistory: true},
	)

	if appendedPlan.Summary.StableToolCut != 3 {
		t.Fatalf("stable tool cut = %d, want 3", appendedPlan.Summary.StableToolCut)
	}
	if !gjson.GetBytes(plannedPayload, "tools.2.cache_control").Exists() {
		t.Fatal("stable tool-prefix breakpoint tools.2.cache_control is missing")
	}
	if !gjson.GetBytes(plannedPayload, "tools.3.cache_control").Exists() {
		t.Fatal("current tool-tail breakpoint tools.3.cache_control is missing")
	}

	currentTime = currentTime.Add(claudePromptCacheDefaultTTL)
	_, expiredPlan := runtime.PlanClaudePromptCache(
		"stable-tools-scope",
		appendedPayload,
		ClaudePromptCacheCapabilities{AutomaticHistory: true},
	)
	if expiredPlan.Summary.StableToolCut != 0 {
		t.Fatalf("expired stable tool cut = %d, want 0", expiredPlan.Summary.StableToolCut)
	}
}

func TestClaudePromptCacheAggregateUsageDoesNotConfirmEveryPrefix(t *testing.T) {
	runtime := NewClaudePromptCacheRuntime()
	currentTime := time.Date(2026, time.July, 10, 12, 0, 0, 0, time.UTC)
	runtime.now = func() time.Time { return currentTime }
	payload := buildClaudePromptCacheTestPayload([]string{"Read", "Bash"}, false)
	_, plan := runtime.PlanClaudePromptCache(
		"aggregate-evidence-scope",
		payload,
		ClaudePromptCacheCapabilities{AutomaticHistory: true},
	)
	attempt, errAcquire := runtime.Acquire(context.Background(), plan, time.Second)
	if errAcquire != nil {
		t.Fatalf("Acquire() error = %v", errAcquire)
	}
	attempt.MarkResponseStarted()
	attempt.Complete(0, 100)
	attempt.Fail()

	scopeState := runtime.scopes["aggregate-evidence-scope"]
	if scopeState == nil {
		t.Fatal("scope state was not retained for candidate tool learning")
	}
	for prefixKey, prefixState := range scopeState.prefixes {
		if prefixState.confirmedUntil.After(currentTime) {
			t.Fatalf("aggregate cache creation incorrectly confirmed prefix %s", prefixKey)
		}
	}
	if len(scopeState.sequences) != 1 || len(scopeState.sequences[0].candidateCuts) == 0 {
		t.Fatal("aggregate cache evidence did not produce a bounded candidate tool sequence")
	}
}

func TestClaudePromptCacheFollowerClaimsNonOverlappingColdPrefix(t *testing.T) {
	runtime := NewClaudePromptCacheRuntime()
	leaderPlan := &ClaudePromptCachePlan{
		ScopeKey: "overlap-scope",
		Prefixes: []ClaudePromptCachePrefix{{Key: "prefix-one", Depth: 1, TTL: claudePromptCacheDefaultTTL}},
	}
	followerPlan := &ClaudePromptCachePlan{
		ScopeKey: "overlap-scope",
		Prefixes: []ClaudePromptCachePrefix{
			{Key: "prefix-one", Depth: 1, TTL: claudePromptCacheDefaultTTL},
			{Key: "prefix-two", Depth: 2, TTL: claudePromptCacheDefaultTTL},
		},
	}

	leaderAttempt, errAcquire := runtime.Acquire(context.Background(), leaderPlan, time.Second)
	if errAcquire != nil {
		t.Fatalf("leader Acquire() error = %v", errAcquire)
	}
	if !leaderAttempt.IsLeader() {
		t.Fatal("first attempt is not leader")
	}

	type acquireResult struct {
		attempt *ClaudePromptCacheAttempt
		err     error
	}
	resultChannel := make(chan acquireResult, 1)
	go func() {
		attempt, err := runtime.Acquire(context.Background(), followerPlan, time.Second)
		resultChannel <- acquireResult{attempt: attempt, err: err}
	}()

	leaderAttempt.MarkResponseStarted()
	defer leaderAttempt.Fail()

	select {
	case result := <-resultChannel:
		if result.err != nil {
			t.Fatalf("follower Acquire() error = %v", result.err)
		}
		if !result.attempt.IsLeader() {
			t.Fatal("follower did not become leader for the remaining cold prefix")
		}
		if len(result.attempt.claimedKeys) != 1 || result.attempt.claimedKeys[0] != "prefix-two" {
			t.Fatalf("follower claimed keys = %v, want [prefix-two]", result.attempt.claimedKeys)
		}
		result.attempt.Fail()
	case <-time.After(2 * time.Second):
		t.Fatal("follower did not resume after the overlapping flight started")
	}
}

func TestClaudePromptCachePendingFlightDoesNotExpireWhileLeaderIsActive(t *testing.T) {
	runtime := NewClaudePromptCacheRuntime()
	currentTime := time.Date(2026, time.July, 10, 12, 0, 0, 0, time.UTC)
	runtime.now = func() time.Time { return currentTime }
	plan := &ClaudePromptCachePlan{
		ScopeKey: "long-flight-scope",
		Prefixes: []ClaudePromptCachePrefix{{Key: "long-flight-prefix", Depth: 1}},
	}

	leaderAttempt, errAcquire := runtime.Acquire(context.Background(), plan, time.Second)
	if errAcquire != nil {
		t.Fatalf("leader Acquire() error = %v", errAcquire)
	}
	if !leaderAttempt.IsLeader() {
		t.Fatal("initial request is not the cold-prefix leader")
	}
	currentTime = currentTime.Add(10 * time.Minute)
	probeAttempt, errAcquire := runtime.Acquire(context.Background(), plan, time.Millisecond)
	if errAcquire != nil {
		t.Fatalf("probe Acquire() error = %v", errAcquire)
	}
	if probeAttempt.IsLeader() {
		probeAttempt.Fail()
		t.Fatal("active pending flight was released solely because of elapsed time")
	}
	leaderAttempt.Fail()
}

func TestClaudePromptCacheFailRevokesStartedState(t *testing.T) {
	runtime := NewClaudePromptCacheRuntime()
	plan := &ClaudePromptCachePlan{
		ScopeKey: "abort-scope",
		Prefixes: []ClaudePromptCachePrefix{{Key: "abort-prefix", Depth: 1, TTL: claudePromptCacheDefaultTTL}},
	}

	firstAttempt, errAcquire := runtime.Acquire(context.Background(), plan, time.Second)
	if errAcquire != nil {
		t.Fatalf("first Acquire() error = %v", errAcquire)
	}
	firstAttempt.MarkResponseStarted()
	firstAttempt.Fail()

	secondAttempt, errAcquire := runtime.Acquire(context.Background(), plan, time.Second)
	if errAcquire != nil {
		t.Fatalf("second Acquire() error = %v", errAcquire)
	}
	if !secondAttempt.IsLeader() {
		t.Fatal("second attempt is not leader after the first attempt failed")
	}
	secondAttempt.Fail()
}

func TestClaudePromptCacheResponseProgressExtendsStartedProtection(t *testing.T) {
	runtime := NewClaudePromptCacheRuntime()
	currentTime := time.Date(2026, time.July, 10, 12, 0, 0, 0, time.UTC)
	runtime.now = func() time.Time { return currentTime }
	plan := &ClaudePromptCachePlan{
		ScopeKey: "progress-scope",
		Prefixes: []ClaudePromptCachePrefix{{Key: "progress-prefix", Depth: 1}},
	}

	streamAttempt, errAcquire := runtime.Acquire(context.Background(), plan, time.Second)
	if errAcquire != nil {
		t.Fatalf("initial Acquire() error = %v", errAcquire)
	}
	streamAttempt.MarkResponseStarted()
	currentTime = currentTime.Add(claudePromptCacheStartedLifetime - time.Second)
	streamAttempt.MarkResponseProgress()
	currentTime = currentTime.Add(2 * time.Second)

	probeAttempt, errAcquire := runtime.Acquire(context.Background(), plan, time.Millisecond)
	if errAcquire != nil {
		t.Fatalf("probe Acquire() error = %v", errAcquire)
	}
	if probeAttempt.IsLeader() {
		probeAttempt.Fail()
		t.Fatal("progress heartbeat did not extend started protection")
	}
	streamAttempt.Fail()
}

func TestClaudePromptCacheDiagnosticsRejectsOutOfOrderCompletion(t *testing.T) {
	runtime := NewClaudePromptCacheRuntime()
	currentTime := time.Date(2026, time.July, 10, 12, 0, 0, 0, time.UTC)
	runtime.now = func() time.Time { return currentTime }

	previousMessageID, firstGeneration := runtime.BeginDiagnostic("diagnostic-key")
	if previousMessageID != "" {
		t.Fatalf("first previous message ID = %q, want empty", previousMessageID)
	}
	_, secondGeneration := runtime.BeginDiagnostic("diagnostic-key")
	runtime.RecordDiagnosticMessageID("diagnostic-key", secondGeneration, "msg_newer")
	runtime.RecordDiagnosticMessageID("diagnostic-key", firstGeneration, "msg_older")

	previousMessageID, _ = runtime.BeginDiagnostic("diagnostic-key")
	if previousMessageID != "msg_newer" {
		t.Fatalf("previous message ID = %q, want msg_newer", previousMessageID)
	}

	currentTime = currentTime.Add(claudePromptCacheDiagnosticLifetime + time.Second)
	expiredMessageID, _ := runtime.BeginDiagnostic("diagnostic-key")
	if expiredMessageID != "" {
		t.Fatalf("expired previous message ID = %q, want empty", expiredMessageID)
	}
}

func TestClaudePromptCacheDiagnosticsAcceptsOlderSuccessWhenNewerRequestFails(t *testing.T) {
	runtime := NewClaudePromptCacheRuntime()

	_, firstGeneration := runtime.BeginDiagnostic("diagnostic-key")
	_, _ = runtime.BeginDiagnostic("diagnostic-key")
	// The newer reservation fails and therefore records no response ID.
	runtime.RecordDiagnosticMessageID("diagnostic-key", firstGeneration, "msg_older_success")

	previousMessageID, _ := runtime.BeginDiagnostic("diagnostic-key")
	if previousMessageID != "msg_older_success" {
		t.Fatalf("previous message ID = %q, want msg_older_success", previousMessageID)
	}
}

func TestClaudePromptCacheDiagnosticFallbackState(t *testing.T) {
	runtime := NewClaudePromptCacheRuntime()
	currentTime := time.Date(2026, time.July, 10, 12, 0, 0, 0, time.UTC)
	runtime.now = func() time.Time { return currentTime }

	if !runtime.DiagnosticAllowed("unsupported-scope") {
		t.Fatal("new diagnostic scope is unexpectedly disabled")
	}
	runtime.DisableDiagnostic("unsupported-scope")
	if runtime.DiagnosticAllowed("unsupported-scope") {
		t.Fatal("unsupported diagnostic scope was not disabled")
	}
	currentTime = currentTime.Add(claudePromptCacheDiagnosticLifetime + time.Second)
	if !runtime.DiagnosticAllowed("unsupported-scope") {
		t.Fatal("diagnostic scope did not recover after the disable lifetime")
	}

	_, firstGeneration := runtime.BeginDiagnostic("stale-chain")
	runtime.RecordDiagnosticMessageID("stale-chain", firstGeneration, "msg_stale")
	previousMessageID, secondGeneration := runtime.BeginDiagnostic("stale-chain")
	if previousMessageID != "msg_stale" {
		t.Fatalf("previous message ID = %q, want msg_stale", previousMessageID)
	}
	runtime.InvalidateDiagnosticMessageID("stale-chain", secondGeneration, "msg_stale")
	previousMessageID, _ = runtime.BeginDiagnostic("stale-chain")
	if previousMessageID != "" {
		t.Fatalf("invalidated previous message ID = %q, want empty", previousMessageID)
	}
}

func TestClaudePromptCacheDiagnosticInvalidationPreservesNewerMessageID(t *testing.T) {
	runtime := NewClaudePromptCacheRuntime()
	_, initialGeneration := runtime.BeginDiagnostic("concurrent-chain")
	runtime.RecordDiagnosticMessageID("concurrent-chain", initialGeneration, "msg_initial")

	firstPreviousMessageID, firstGeneration := runtime.BeginDiagnostic("concurrent-chain")
	secondPreviousMessageID, secondGeneration := runtime.BeginDiagnostic("concurrent-chain")
	if firstPreviousMessageID != "msg_initial" || secondPreviousMessageID != "msg_initial" {
		t.Fatalf(
			"concurrent previous IDs = %q and %q, want msg_initial",
			firstPreviousMessageID,
			secondPreviousMessageID,
		)
	}
	runtime.RecordDiagnosticMessageID("concurrent-chain", firstGeneration, "msg_advanced")
	runtime.InvalidateDiagnosticMessageID("concurrent-chain", secondGeneration, secondPreviousMessageID)

	previousMessageID, _ := runtime.BeginDiagnostic("concurrent-chain")
	if previousMessageID != "msg_advanced" {
		t.Fatalf("concurrent invalidation cleared newer message ID: got %q", previousMessageID)
	}
}

func TestClaudePromptCacheStartedPrefixSurvivesCapacityTrimming(t *testing.T) {
	runtime := NewClaudePromptCacheRuntime()
	currentTime := time.Date(2026, time.July, 10, 12, 0, 0, 0, time.UTC)
	targetPrefixKey := "started-prefix"
	scopeState := &claudePromptCacheScopeState{
		prefixes:   make(map[string]*claudePromptCachePrefixState),
		lastAccess: currentTime,
	}
	scopeState.prefixes[targetPrefixKey] = &claudePromptCachePrefixState{
		startedUntil: currentTime.Add(claudePromptCacheStartedLifetime),
		lastTouched:  currentTime.Add(-time.Hour),
	}
	for prefixIndex := 0; prefixIndex < claudePromptCacheMaxPrefixesPerScope; prefixIndex++ {
		prefixKey := fmt.Sprintf("confirmed-prefix-%d", prefixIndex)
		scopeState.prefixes[prefixKey] = &claudePromptCachePrefixState{
			confirmedUntil: currentTime.Add(claudePromptCacheDefaultTTL),
			lastTouched:    currentTime.Add(time.Duration(prefixIndex) * time.Second),
		}
	}

	trimClaudePrefixStates(scopeState, currentTime, runtime.flights)
	if scopeState.prefixes[targetPrefixKey] == nil {
		t.Fatal("started prefix was removed by capacity trimming")
	}
}

func TestClaudePromptCacheStartedScopeSurvivesCapacityTrimming(t *testing.T) {
	runtime := NewClaudePromptCacheRuntime()
	currentTime := time.Date(2026, time.July, 10, 12, 0, 0, 0, time.UTC)
	runtime.now = func() time.Time { return currentTime }
	runtime.scopes["started-scope"] = &claudePromptCacheScopeState{
		lastAccess: currentTime.Add(-time.Hour),
		prefixes: map[string]*claudePromptCachePrefixState{
			"started-prefix": {
				startedUntil: currentTime.Add(claudePromptCacheStartedLifetime),
				lastTouched:  currentTime.Add(-time.Hour),
			},
		},
	}
	for scopeIndex := 0; scopeIndex < claudePromptCacheMaxScopes; scopeIndex++ {
		scopeKey := fmt.Sprintf("confirmed-scope-%d", scopeIndex)
		runtime.scopes[scopeKey] = &claudePromptCacheScopeState{
			lastAccess: currentTime.Add(time.Duration(scopeIndex) * time.Second),
			prefixes: map[string]*claudePromptCachePrefixState{
				"confirmed-prefix": {
					confirmedUntil: currentTime.Add(claudePromptCacheDefaultTTL),
					lastTouched:    currentTime,
				},
			},
		}
	}

	runtime.mutex.Lock()
	runtime.cleanupLocked(currentTime)
	runtime.mutex.Unlock()
	if runtime.scopes["started-scope"] == nil {
		t.Fatal("scope containing a started prefix was removed by capacity trimming")
	}
}

func TestClaudePromptCacheDiagnosticsRejectsCompletionFromEvictedGeneration(t *testing.T) {
	runtime := NewClaudePromptCacheRuntime()
	currentTime := time.Date(2026, time.July, 10, 12, 0, 0, 0, time.UTC)
	runtime.now = func() time.Time { return currentTime }

	_, evictedGeneration := runtime.BeginDiagnostic("diagnostic-key")
	currentTime = currentTime.Add(claudePromptCacheDiagnosticLifetime + time.Second)
	_, _ = runtime.BeginDiagnostic("diagnostic-key")
	runtime.RecordDiagnosticMessageID("diagnostic-key", evictedGeneration, "msg_evicted")

	previousMessageID, _ := runtime.BeginDiagnostic("diagnostic-key")
	if previousMessageID != "" {
		t.Fatalf("previous message ID = %q, want empty after evicted generation completion", previousMessageID)
	}
}

func TestStripAllClaudeCacheControlsRemovesTopLevelAndInBody(t *testing.T) {
	payload := []byte(`{
		"cache_control":{"type":"ephemeral"},
		"tools":[{"name":"Read","input_schema":{"type":"object"},"cache_control":{"type":"ephemeral"}}],
		"system":[{"type":"text","text":"system","cache_control":{"type":"ephemeral"}}],
		"messages":[{"role":"user","content":[{"type":"text","text":"hello","cache_control":{"type":"ephemeral"}}]}]
	}`)

	stripped := StripAllClaudeCacheControls(payload)
	if gjson.GetBytes(stripped, "cache_control").Exists() {
		t.Fatal("top-level cache_control was not stripped")
	}
	locations, invalidPaths := collectClaudeCacheBreakpoints(stripped)
	if len(invalidPaths) != 0 {
		t.Fatalf("invalid paths after strip = %v", invalidPaths)
	}
	if len(locations) != 0 {
		t.Fatalf("in-body breakpoints after strip = %d, want 0", len(locations))
	}
}

func buildClaudePromptCacheTestPayload(toolNames []string, includeTopLevelCacheControl bool) []byte {
	tools := make([]map[string]any, 0, len(toolNames))
	for _, toolName := range toolNames {
		tools = append(tools, map[string]any{
			"name":        toolName,
			"description": "test tool " + toolName,
			"input_schema": map[string]any{
				"type": "object",
			},
		})
	}
	payload := map[string]any{
		"model": "claude-sonnet-4-5",
		"tools": tools,
		"system": []map[string]any{
			{"type": "text", "text": "stable system instructions"},
		},
		"messages": []map[string]any{
			{"role": "user", "content": "first user turn"},
			{"role": "assistant", "content": []map[string]any{{"type": "text", "text": "assistant turn"}}},
			{"role": "user", "content": []map[string]any{{"type": "text", "text": "second user turn"}}},
		},
	}
	if includeTopLevelCacheControl {
		payload["cache_control"] = map[string]any{"type": "ephemeral"}
	}
	encodedPayload, errMarshal := json.Marshal(payload)
	if errMarshal != nil {
		panic(errMarshal)
	}
	return encodedPayload
}
