package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"gopkg.in/natefinch/lumberjack.v2"
)

type diagnosticSink struct {
	mu     sync.Mutex
	writer *lumberjack.Logger
	clock  func() time.Time
}

func newDiagnosticSink(cfg diagnosticsConfig, clock func() time.Time) (*diagnosticSink, error) {
	if !cfg.Enabled {
		return nil, nil
	}
	dir := filepath.Dir(cfg.Path)
	if errMkdir := os.MkdirAll(dir, 0o750); errMkdir != nil {
		return nil, fmt.Errorf("create diagnostics directory %q: %w", dir, errMkdir)
	}
	probe, errOpen := os.OpenFile(cfg.Path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if errOpen != nil {
		return nil, fmt.Errorf("open diagnostics path %q: %w", cfg.Path, errOpen)
	}
	if errClose := probe.Close(); errClose != nil {
		return nil, fmt.Errorf("close diagnostics path probe %q: %w", cfg.Path, errClose)
	}
	if clock == nil {
		clock = time.Now
	}
	return &diagnosticSink{
		clock: clock,
		writer: &lumberjack.Logger{
			Filename:   cfg.Path,
			MaxSize:    cfg.MaxSizeMB,
			MaxBackups: cfg.MaxBackups,
			Compress:   true,
		},
	}, nil
}

func (s *diagnosticSink) write(level, message string, fields map[string]any, hostCallbackID string) {
	if s == nil || s.writer == nil {
		return
	}
	record := make(map[string]any, len(fields)+4)
	record["timestamp"] = s.clock().UTC().Format(time.RFC3339Nano)
	record["level"] = level
	record["message"] = message
	for key, value := range fields {
		record[key] = value
	}
	if hash := shortValueHash(hostCallbackID); hash != "" {
		record["callback_hash"] = hash
	}
	raw, errMarshal := json.Marshal(record)
	if errMarshal != nil {
		return
	}
	raw = append(raw, '\n')
	s.mu.Lock()
	_, _ = s.writer.Write(raw)
	s.mu.Unlock()
}

func (s *diagnosticSink) close() {
	if s == nil || s.writer == nil {
		return
	}
	s.mu.Lock()
	_ = s.writer.Close()
	s.mu.Unlock()
}

type laneObservationKey struct {
	Generation uint64
	Alias      string
	SessionID  string
	Provider   string
	Model      string
}

type laneObservation struct {
	SystemFingerprint string
	ToolsFingerprint  string
	HistoryItems      []string
	ExpiresAt         time.Time
}

type laneObservationStore struct {
	mu      sync.Mutex
	entries map[laneObservationKey]laneObservation
	clock   func() time.Time
}

func newLaneObservationStore(clock func() time.Time) *laneObservationStore {
	if clock == nil {
		clock = time.Now
	}
	return &laneObservationStore{entries: make(map[laneObservationKey]laneObservation), clock: clock}
}

// observe compares one prior lane snapshot against the current request, emits the
// resulting continuity fields, and replaces the snapshot with the current observation.
func (s *laneObservationStore) observe(key laneObservationKey, current requestObservation, ttl time.Duration) map[string]any {
	fields := map[string]any{
		"lane_history_items": len(current.HistoryItems),
		"lane_continuity":    "first_observation",
	}
	if s == nil {
		return fields
	}
	now := s.clock()
	s.mu.Lock()
	previous, found := s.entries[key]
	if found && !previous.ExpiresAt.After(now) {
		delete(s.entries, key)
		found = false
	}
	if found {
		systemMatch := previous.SystemFingerprint == current.SystemFingerprint
		toolsMatch := previous.ToolsFingerprint == current.ToolsFingerprint
		prefixMatch := stringSlicePrefix(previous.HistoryItems, current.HistoryItems)
		fields["system_match"] = systemMatch
		fields["tools_match"] = toolsMatch
		fields["prior_history_prefix_match"] = prefixMatch
		fields["prior_history_items"] = len(previous.HistoryItems)
		switch {
		case !systemMatch || !toolsMatch:
			fields["lane_continuity"] = "settings_changed"
		case !prefixMatch:
			fields["lane_continuity"] = "history_prefix_mismatch"
		default:
			fields["lane_continuity"] = "warm_prefix_candidate"
		}
	}
	s.entries[key] = laneObservation{
		SystemFingerprint: current.SystemFingerprint,
		ToolsFingerprint:  current.ToolsFingerprint,
		HistoryItems:      append([]string(nil), current.HistoryItems...),
		ExpiresAt:         now.Add(ttl),
	}
	s.mu.Unlock()
	return fields
}

// diagnosticFields renders the content-free request description that accompanies
// one route record, including the continuity comparison for the selected lane.
func (r *runtimeState) diagnosticFields(decision routeDecision) map[string]any {
	observation := decision.Observation
	opaqueContinuation := observation.HasPreviousID || observation.HasConversationID || observation.HasContainer
	fields := map[string]any{
		"source_format":             decision.SourceFormat,
		"stream":                    decision.Stream,
		"input_kind":                observation.InputKind,
		"has_tool_result":           observation.HasToolResult,
		"has_previous_response_id":  observation.HasPreviousID,
		"has_conversation_id":       observation.HasConversationID,
		"has_hosted_container":      observation.HasContainer,
		"opaque_continuation":       opaqueContinuation,
		"portable_history":          len(observation.HistoryItems) > 0 && !opaqueContinuation,
		"thinking_signature_count":  observation.ThinkingSignatures,
		"encrypted_reasoning_count": observation.EncryptedReasoning,
		"system_fingerprint":        observation.SystemFingerprint,
		"tools_fingerprint":         observation.ToolsFingerprint,
		"history_fingerprint":       observation.HistoryFingerprint,
		"history_items":             len(observation.HistoryItems),
	}
	if decision.Identity.Value != "" {
		key := laneObservationKey{
			Generation: decision.Config.Generation,
			Alias:      decision.Alias.LookupKey,
			SessionID:  decision.Identity.Value,
			Provider:   decision.Selection.Target.Provider,
			Model:      decision.TargetModel,
		}
		for name, value := range r.observations.observe(key, observation, decision.Config.SessionTTL) {
			fields[name] = value
		}
	}
	return fields
}

func (s *laneObservationStore) reset() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.entries = make(map[laneObservationKey]laneObservation)
	s.mu.Unlock()
}

func stringSlicePrefix(prefix, values []string) bool {
	if len(prefix) > len(values) {
		return false
	}
	for index := range prefix {
		if prefix[index] != values[index] {
			return false
		}
	}
	return true
}

func (r *runtimeState) handleUsage(record pluginapi.UsageRecord) {
	if r == nil {
		return
	}
	cfg := r.loadedConfig()
	if cfg == nil || !cfg.Enabled {
		return
	}
	aliasBase, _, _ := parseSupportedEffortSuffix(strings.TrimSpace(record.Alias))
	if cfg.ByLookup[normalizedAliasKey(aliasBase)] == nil {
		return
	}
	inputForRate := record.Detail.InputTokens
	if strings.EqualFold(record.Provider, "claude") {
		inputForRate += record.Detail.CacheReadTokens + record.Detail.CacheCreationTokens
	}
	cacheReadRate := float64(0)
	if inputForRate > 0 {
		cacheReadRate = float64(record.Detail.CacheReadTokens) / float64(inputForRate)
	}
	fields := map[string]any{
		"event":                   "usage",
		"alias":                   record.Alias,
		"provider":                record.Provider,
		"executor_type":           record.ExecutorType,
		"model":                   record.Model,
		"auth_hash":               shortValueHash(record.AuthID),
		"source":                  record.Source,
		"reasoning_effort":        record.ReasoningEffort,
		"generate":                record.Generate,
		"requested_at":            record.RequestedAt.UTC().Format(time.RFC3339Nano),
		"latency_ms":              record.Latency.Milliseconds(),
		"ttft_ms":                 record.TTFT.Milliseconds(),
		"failed":                  record.Failed,
		"status_code":             record.Failure.StatusCode,
		"input_tokens":            record.Detail.InputTokens,
		"output_tokens":           record.Detail.OutputTokens,
		"reasoning_tokens":        record.Detail.ReasoningTokens,
		"cached_tokens":           record.Detail.CachedTokens,
		"cache_read_tokens":       record.Detail.CacheReadTokens,
		"cache_creation_tokens":   record.Detail.CacheCreationTokens,
		"cache_read_rate":         cacheReadRate,
		"total_tokens":            record.Detail.TotalTokens,
		"upstream_cache_observed": record.Detail.CacheReadTokens > 0 || record.Detail.CachedTokens > 0,
	}
	r.log("debug", "model-sequence-router: upstream usage", fields, "")
}
