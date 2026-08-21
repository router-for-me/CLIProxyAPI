package main

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
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

type requestObservation struct {
	SystemFingerprint  string
	ToolsFingerprint   string
	HistoryFingerprint string
	HistoryItems       []string
	InputKind          string
	HasToolResult      bool
	HasPreviousID      bool
	HasConversationID  bool
	HasContainer       bool
	ThinkingSignatures int
	EncryptedReasoning int
}

func inspectRequest(body []byte, salt []byte) requestObservation {
	observation := requestObservation{InputKind: "unknown"}
	if len(bytes.TrimSpace(body)) == 0 {
		return observation
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var root map[string]any
	if errDecode := decoder.Decode(&root); errDecode != nil {
		return observation
	}
	system, hasSystem := root["system"]
	if !hasSystem {
		system, hasSystem = root["instructions"]
	}
	if hasSystem {
		observation.SystemFingerprint = fingerprintJSON(system, salt)
	}
	if tools, ok := root["tools"]; ok {
		observation.ToolsFingerprint = fingerprintJSON(tools, salt)
	}
	history := firstArray(root, "messages", "input", "contents")
	if len(history) > 0 {
		observation.HistoryItems = make([]string, 0, len(history))
		for _, item := range history {
			observation.HistoryItems = append(observation.HistoryItems, fingerprintJSON(item, salt))
		}
		observation.HistoryFingerprint = fingerprintStrings(observation.HistoryItems, salt)
		last := history[len(history)-1]
		observation.HasToolResult = containsToolResult(last)
		switch {
		case observation.HasToolResult:
			observation.InputKind = "tool_result"
		case containsRole(last, "user"):
			observation.InputKind = "user"
		default:
			observation.InputKind = "history"
		}
	}
	observation.HasPreviousID = nonEmptyJSONValue(root["previous_response_id"])
	observation.HasConversationID = nonEmptyJSONValue(root["conversation_id"]) || nonEmptyJSONValue(root["conversation"])
	observation.HasContainer = nonEmptyJSONValue(root["container_id"]) || nonEmptyJSONValue(root["container"])
	walkJSON(root, func(node map[string]any) {
		typeName, _ := node["type"].(string)
		if (typeName == "thinking" || typeName == "redacted_thinking") && nonEmptyJSONValue(node["signature"]) {
			observation.ThinkingSignatures++
		}
		if typeName == "reasoning" && nonEmptyJSONValue(node["encrypted_content"]) {
			observation.EncryptedReasoning++
		}
	})
	return observation
}

func firstArray(root map[string]any, keys ...string) []any {
	for _, key := range keys {
		if values, ok := root[key].([]any); ok {
			return values
		}
	}
	return nil
}

func containsToolResult(value any) bool {
	found := false
	walkJSON(value, func(node map[string]any) {
		if found {
			return
		}
		if role, _ := node["role"].(string); strings.EqualFold(strings.TrimSpace(role), "tool") {
			found = true
			return
		}
		typeName, _ := node["type"].(string)
		switch strings.ToLower(strings.TrimSpace(typeName)) {
		case "tool_result", "function_call_output", "custom_tool_call_output":
			found = true
		}
	})
	return found
}

func containsRole(value any, want string) bool {
	found := false
	walkJSON(value, func(node map[string]any) {
		if role, _ := node["role"].(string); strings.EqualFold(strings.TrimSpace(role), want) {
			found = true
		}
	})
	return found
}

func walkJSON(value any, visit func(map[string]any)) {
	switch typed := value.(type) {
	case map[string]any:
		visit(typed)
		for _, child := range typed {
			walkJSON(child, visit)
		}
	case []any:
		for _, child := range typed {
			walkJSON(child, visit)
		}
	}
}

func nonEmptyJSONValue(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case string:
		return strings.TrimSpace(typed) != ""
	case []any:
		return len(typed) > 0
	case map[string]any:
		return len(typed) > 0
	default:
		return true
	}
}

func fingerprintJSON(value any, salt []byte) string {
	raw, errMarshal := json.Marshal(value)
	if errMarshal != nil {
		return ""
	}
	return saltedHash(raw, salt)
}

func fingerprintStrings(values []string, salt []byte) string {
	return saltedHash([]byte(strings.Join(values, "\x00")), salt)
}

func saltedHash(value, salt []byte) string {
	hash := sha256.New()
	_, _ = hash.Write(salt)
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write(value)
	return hex.EncodeToString(hash.Sum(nil)[:8])
}

func shortValueHash(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:6])
}

func newFingerprintSalt() []byte {
	salt := make([]byte, 32)
	if _, errRead := rand.Read(salt); errRead == nil {
		return salt
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d", time.Now().UnixNano())))
	return sum[:]
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
