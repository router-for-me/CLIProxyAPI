package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/api/handlers"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	codexDirectResponsesPath = "/backend-api/codex/responses"
	codexDirectCompactPath   = "/backend-api/codex/responses/compact"

	// Direct HTTP continuation state is process-local by design. A restart drops
	// bindings/recent evidence and forces unknown continuations to fail closed
	// before upstream.
	codexDirectContinuationTTL                = 30 * time.Minute
	codexDirectContinuationMaxBindingCapacity = 1024
)

type codexDirectContinuationBinding struct {
	authID             string
	modelName          string
	requestJSON        []byte
	responseOutputJSON []byte
	expiresAt          time.Time
}

type codexDirectRecentEvidence struct {
	authID             string
	modelName          string
	scopeKey           string
	responseOutputJSON []byte
	expiresAt          time.Time
}

type codexDirectContinuationStore struct {
	mu             sync.Mutex
	bindings       map[string]codexDirectContinuationBinding
	recentEvidence map[string]codexDirectRecentEvidence
}

var codexDirectContinuations = &codexDirectContinuationStore{
	bindings:       make(map[string]codexDirectContinuationBinding),
	recentEvidence: make(map[string]codexDirectRecentEvidence),
}

type codexDirectContinuationTracker struct {
	modelName      string
	requestJSON    []byte
	routeKind      string
	scopeKey       string
	compactRequest bool

	mu                  sync.Mutex
	authID              string
	outputItemsByIndex  map[int64][]byte
	outputItemsFallback [][]byte
}

type codexDirectContinuationSnapshot struct {
	responseID         string
	responseOutputJSON []byte
}

type codexDirectEvidenceCounts struct {
	itemCount               int
	assistantMessageCount   int
	functionCallCount       int
	functionCallOutputCount int
}

func (h *OpenAIResponsesAPIHandler) prepareCodexDirectContinuationContext(c *gin.Context, rawJSON []byte, modelName string, ctx context.Context) (context.Context, []byte, *codexDirectContinuationTracker, bool) {
	if !isCodexDirectContinuationRequest(c) {
		return ctx, rawJSON, nil, true
	}

	modelName = strings.TrimSpace(modelName)
	requestJSON := bytes.Clone(rawJSON)
	tracker := &codexDirectContinuationTracker{
		modelName:      modelName,
		requestJSON:    bytes.Clone(requestJSON),
		routeKind:      codexDirectRouteKind(c),
		scopeKey:       codexDirectContinuationScopeKey(c, requestJSON),
		compactRequest: isCodexDirectCompactRequest(c),
	}

	if isCodexDirectResponsesRequest(c) {
		previousResponseID := strings.TrimSpace(gjson.GetBytes(rawJSON, "previous_response_id").String())
		if previousResponseID != "" {
			binding, ok := codexDirectContinuations.lookup(previousResponseID, modelName)
			if !ok {
				tracker.logDecision(rawJSON, "miss", "none", "missing_binding")
				writeCodexDirectContinuationError(c)
				return ctx, nil, nil, false
			}
			if !h.codexDirectContinuationBindingAuthUsable(binding, modelName) {
				tracker.logDecision(rawJSON, "unusable", "none", "unusable_auth")
				writeCodexDirectContinuationError(c)
				return ctx, nil, nil, false
			}

			repairedJSON, errRepair := repairCodexDirectContinuationRequest(rawJSON, binding)
			if errRepair != nil {
				tracker.logDecision(rawJSON, "hit", "failed", codexDirectContinuationRepairFailReason(errRepair))
				writeCodexDirectContinuationRepairError(c)
				return ctx, nil, nil, false
			}
			requestJSON = repairedJSON
			tracker.requestJSON = bytes.Clone(requestJSON)
			tracker.setAuthID(binding.authID)
			tracker.logDecision(requestJSON, "hit", "repaired", "none")
			ctx = handlers.WithPinnedAuthID(ctx, binding.authID)
		}
	}

	if tracker.compactRequest {
		tracker.logDecision(requestJSON, "none", "none", "none")
	}

	ctx = handlers.WithSelectedAuthIDCallback(ctx, tracker.setAuthID)
	return ctx, requestJSON, tracker, true
}

func (h *OpenAIResponsesAPIHandler) codexDirectContinuationBindingAuthUsable(binding codexDirectContinuationBinding, modelName string) bool {
	authID := strings.TrimSpace(binding.authID)
	if authID == "" || h == nil || h.AuthManager == nil {
		return false
	}
	auth, ok := h.AuthManager.GetByID(authID)
	if !ok || auth == nil {
		return false
	}
	if !responsesWebsocketAuthAvailableForModel(auth, modelName, time.Now()) {
		return false
	}
	return true
}

func (t *codexDirectContinuationTracker) observeStream(chunks <-chan []byte) <-chan []byte {
	if t == nil || chunks == nil {
		return chunks
	}

	out := make(chan []byte)
	go func() {
		defer close(out)
		for chunk := range chunks {
			t.bindResponseIDs(chunk)
			out <- chunk
		}
	}()
	return out
}

func (t *codexDirectContinuationTracker) bindResponseIDs(payload []byte) {
	if t == nil || len(payload) == 0 {
		return
	}
	authID := t.getAuthID()
	if authID == "" {
		return
	}
	for _, snapshot := range t.snapshotsFromPayload(payload) {
		codexDirectContinuations.bind(
			snapshot.responseID,
			authID,
			t.modelName,
			t.scopeKey,
			t.requestJSON,
			snapshot.responseOutputJSON,
			t.routeKind,
			t.compactRequest,
		)
	}
}

func (t *codexDirectContinuationTracker) snapshotsFromPayload(payload []byte) []codexDirectContinuationSnapshot {
	payloads := websocketJSONPayloadsFromChunk(payload)
	if len(payloads) == 0 {
		return nil
	}

	snapshots := make([]codexDirectContinuationSnapshot, 0, len(payloads))
	for _, raw := range payloads {
		root := gjson.ParseBytes(raw)
		switch strings.TrimSpace(root.Get("type").String()) {
		case "response.output_item.done":
			t.recordOutputItem(root)
		case "response.completed":
			responseID := strings.TrimSpace(root.Get("response.id").String())
			if responseID == "" {
				continue
			}
			outputJSON := codexDirectResponseOutputJSON(root)
			if codexDirectOutputJSONIsEmpty(outputJSON) {
				outputJSON = t.completedOutputFromDoneItems()
			} else {
				t.clearOutputItems()
			}
			snapshots = append(snapshots, codexDirectContinuationSnapshot{
				responseID:         responseID,
				responseOutputJSON: outputJSON,
			})
		default:
			responseID := strings.TrimSpace(root.Get("id").String())
			if responseID == "" {
				continue
			}
			snapshots = append(snapshots, codexDirectContinuationSnapshot{
				responseID:         responseID,
				responseOutputJSON: codexDirectResponseOutputJSON(root),
			})
		}
	}
	return snapshots
}

func (t *codexDirectContinuationTracker) recordOutputItem(root gjson.Result) {
	item := root.Get("item")
	if !item.Exists() || !item.IsObject() || strings.TrimSpace(item.Get("type").String()) == "" {
		return
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	if outputIndex := root.Get("output_index"); outputIndex.Exists() {
		if t.outputItemsByIndex == nil {
			t.outputItemsByIndex = make(map[int64][]byte)
		}
		t.outputItemsByIndex[outputIndex.Int()] = bytes.Clone([]byte(item.Raw))
		return
	}
	t.outputItemsFallback = append(t.outputItemsFallback, bytes.Clone([]byte(item.Raw)))
}

func (t *codexDirectContinuationTracker) completedOutputFromDoneItems() []byte {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.outputItemsByIndex) == 0 && len(t.outputItemsFallback) == 0 {
		return []byte("[]")
	}

	indexes := make([]int64, 0, len(t.outputItemsByIndex))
	for index := range t.outputItemsByIndex {
		indexes = append(indexes, index)
	}
	sort.Slice(indexes, func(i, j int) bool {
		return indexes[i] < indexes[j]
	})

	items := make([][]byte, 0, len(t.outputItemsByIndex)+len(t.outputItemsFallback))
	for _, index := range indexes {
		items = append(items, t.outputItemsByIndex[index])
	}
	items = append(items, t.outputItemsFallback...)
	t.outputItemsByIndex = nil
	t.outputItemsFallback = nil

	var out bytes.Buffer
	out.WriteByte('[')
	for i, item := range items {
		if i > 0 {
			out.WriteByte(',')
		}
		out.Write(item)
	}
	out.WriteByte(']')
	return out.Bytes()
}

func (t *codexDirectContinuationTracker) clearOutputItems() {
	t.mu.Lock()
	t.outputItemsByIndex = nil
	t.outputItemsFallback = nil
	t.mu.Unlock()
}

func (t *codexDirectContinuationTracker) setAuthID(authID string) {
	if t == nil {
		return
	}
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return
	}
	t.mu.Lock()
	t.authID = authID
	t.mu.Unlock()
}

func (t *codexDirectContinuationTracker) getAuthID() string {
	if t == nil {
		return ""
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.authID
}

func (t *codexDirectContinuationTracker) logDecision(requestJSON []byte, bindingResult, repairResult, failReason string) {
	if t == nil {
		return
	}
	fields := codexDirectBaseDiagnosticFields(
		t.routeKind,
		t.compactRequest,
		strings.TrimSpace(gjson.GetBytes(requestJSON, "previous_response_id").String()) != "" || bindingResult == "hit" || bindingResult == "miss" || bindingResult == "unusable",
		t.scopeKey != "",
		bindingResult,
		repairResult,
		failReason,
		codexDirectInputEvidenceCounts(requestJSON),
	)
	if repairResult == "failed" {
		log.WithFields(fields).Warn("codex direct http continuation diagnostic")
		return
	}
	log.WithFields(fields).Info("codex direct http continuation diagnostic")
}

func (s *codexDirectContinuationStore) bind(responseID, authID, modelName, scopeKey string, requestJSON []byte, responseOutputJSON []byte, routeKind string, allowRecentEvidenceAugmentation bool) {
	responseID = strings.TrimSpace(responseID)
	authID = strings.TrimSpace(authID)
	modelName = strings.TrimSpace(modelName)
	scopeKey = strings.TrimSpace(scopeKey)
	if responseID == "" || authID == "" {
		return
	}

	s.mu.Lock()
	s.ensureLocked()
	now := time.Now()
	s.pruneExpiredLocked(now)
	outputJSON := normalizeCodexDirectResponseOutputJSON(responseOutputJSON)
	compactOutputHasEvidence := codexDirectOutputHasAssistantOrToolEvidence(outputJSON)
	recentEvidenceHit := false
	compactEvidenceAugmented := false
	if allowRecentEvidenceAugmentation && !compactOutputHasEvidence {
		if recent, ok := s.recentEvidenceLocked(authID, modelName, scopeKey, now); ok {
			outputJSON = recent.responseOutputJSON
			recentEvidenceHit = true
			compactEvidenceAugmented = true
		}
	}
	if codexDirectOutputHasAssistantOrToolEvidence(outputJSON) {
		s.rememberRecentEvidenceLocked(authID, modelName, scopeKey, outputJSON, now)
	}
	s.bindings[responseID] = codexDirectContinuationBinding{
		authID:             authID,
		modelName:          modelName,
		requestJSON:        bytes.Clone(requestJSON),
		responseOutputJSON: outputJSON,
		expiresAt:          now.Add(codexDirectContinuationTTL),
	}
	s.trimLocked(codexDirectContinuationMaxBindingCapacity)
	s.mu.Unlock()

	if allowRecentEvidenceAugmentation {
		codexDirectLogCompactEvidenceDiagnostic(
			routeKind,
			scopeKey != "",
			requestJSON,
			outputJSON,
			compactOutputHasEvidence,
			recentEvidenceHit,
			compactEvidenceAugmented,
		)
	}
}

func codexDirectBaseDiagnosticFields(routeKind string, compactRequest, hasPreviousResponseID, scopePresent bool, bindingResult, repairResult, failReason string, counts codexDirectEvidenceCounts) log.Fields {
	routeKind = strings.TrimSpace(routeKind)
	if routeKind == "" {
		routeKind = "unknown"
	}
	bindingResult = strings.TrimSpace(bindingResult)
	if bindingResult == "" {
		bindingResult = "none"
	}
	repairResult = strings.TrimSpace(repairResult)
	if repairResult == "" {
		repairResult = "none"
	}
	failReason = strings.TrimSpace(failReason)
	if failReason == "" {
		failReason = "none"
	}
	return log.Fields{
		"route_kind":                     routeKind,
		"compact_request":                compactRequest,
		"has_previous_response_id":       hasPreviousResponseID,
		"scope_present":                  scopePresent,
		"binding_result":                 bindingResult,
		"repair_result":                  repairResult,
		"input_item_count":               counts.itemCount,
		"assistant_message_count":        counts.assistantMessageCount,
		"function_call_count":            counts.functionCallCount,
		"function_call_output_count":     counts.functionCallOutputCount,
		"compact_output_has_evidence":    false,
		"recent_evidence_hit":            false,
		"compact_evidence_augmented":     false,
		"fail_reason":                    failReason,
		"bound_output_item_count":        0,
		"bound_output_assistant_count":   0,
		"bound_output_tool_call_count":   0,
		"bound_output_tool_output_count": 0,
	}
}

func codexDirectLogCompactEvidenceDiagnostic(routeKind string, scopePresent bool, requestJSON, boundOutputJSON []byte, compactOutputHasEvidence, recentEvidenceHit, compactEvidenceAugmented bool) {
	fields := codexDirectBaseDiagnosticFields(
		routeKind,
		true,
		strings.TrimSpace(gjson.GetBytes(requestJSON, "previous_response_id").String()) != "",
		scopePresent,
		"none",
		"none",
		"none",
		codexDirectInputEvidenceCounts(requestJSON),
	)
	boundCounts := codexDirectOutputEvidenceCounts(boundOutputJSON)
	fields["compact_output_has_evidence"] = compactOutputHasEvidence
	fields["recent_evidence_hit"] = recentEvidenceHit
	fields["compact_evidence_augmented"] = compactEvidenceAugmented
	fields["bound_output_item_count"] = boundCounts.itemCount
	fields["bound_output_assistant_count"] = boundCounts.assistantMessageCount
	fields["bound_output_tool_call_count"] = boundCounts.functionCallCount
	fields["bound_output_tool_output_count"] = boundCounts.functionCallOutputCount
	log.WithFields(fields).Info("codex direct http compact evidence diagnostic")
}

func codexDirectInputEvidenceCounts(rawJSON []byte) codexDirectEvidenceCounts {
	return codexDirectEvidenceCountsFromArray(gjson.GetBytes(rawJSON, "input"))
}

func codexDirectOutputEvidenceCounts(rawJSON []byte) codexDirectEvidenceCounts {
	return codexDirectEvidenceCountsFromArray(gjson.ParseBytes(normalizeCodexDirectResponseOutputJSON(rawJSON)))
}

func codexDirectEvidenceCountsFromArray(input gjson.Result) codexDirectEvidenceCounts {
	if !input.IsArray() {
		return codexDirectEvidenceCounts{}
	}
	items := input.Array()
	counts := codexDirectEvidenceCounts{itemCount: len(items)}
	for _, item := range items {
		itemType := strings.TrimSpace(item.Get("type").String())
		if itemType == "message" && strings.TrimSpace(item.Get("role").String()) == "assistant" {
			counts.assistantMessageCount++
		}
		if isResponsesToolCallType(itemType) {
			counts.functionCallCount++
		}
		if isResponsesToolCallOutputType(itemType) {
			counts.functionCallOutputCount++
		}
	}
	return counts
}

func codexDirectContinuationRepairFailReason(err error) string {
	if err == nil {
		return "none"
	}
	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "tool output missing matching call"):
		return "orphan_tool_output"
	case strings.Contains(message, "missing array input"),
		strings.Contains(message, "missing array"),
		strings.Contains(message, "invalid request input"),
		strings.Contains(message, "invalid previous response output"),
		strings.Contains(message, "set repaired input"):
		return "invalid_input"
	default:
		return "invalid_input"
	}
}

func (s *codexDirectContinuationStore) lookup(responseID, modelName string) (codexDirectContinuationBinding, bool) {
	responseID = strings.TrimSpace(responseID)
	modelName = strings.TrimSpace(modelName)
	if responseID == "" {
		return codexDirectContinuationBinding{}, false
	}

	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureLocked()
	s.pruneExpiredLocked(now)
	binding, ok := s.bindings[responseID]
	if !ok {
		return codexDirectContinuationBinding{}, false
	}
	if binding.modelName != "" && modelName != "" && binding.modelName != modelName {
		return codexDirectContinuationBinding{}, false
	}
	if strings.TrimSpace(binding.authID) == "" {
		return codexDirectContinuationBinding{}, false
	}
	binding.requestJSON = bytes.Clone(binding.requestJSON)
	binding.responseOutputJSON = bytes.Clone(binding.responseOutputJSON)
	return binding, true
}

func (s *codexDirectContinuationStore) ensureLocked() {
	if s.bindings == nil {
		s.bindings = make(map[string]codexDirectContinuationBinding)
	}
	if s.recentEvidence == nil {
		s.recentEvidence = make(map[string]codexDirectRecentEvidence)
	}
}

func (s *codexDirectContinuationStore) pruneExpiredLocked(now time.Time) {
	if now.IsZero() {
		now = time.Now()
	}
	for responseID, binding := range s.bindings {
		if !binding.expiresAt.IsZero() && !now.Before(binding.expiresAt) {
			delete(s.bindings, responseID)
		}
	}
	for key, recent := range s.recentEvidence {
		if !recent.expiresAt.IsZero() && !now.Before(recent.expiresAt) {
			delete(s.recentEvidence, key)
		}
	}
}

func (s *codexDirectContinuationStore) trimLocked(maxBindings int) {
	if maxBindings <= 0 {
		for responseID := range s.bindings {
			delete(s.bindings, responseID)
		}
		for key := range s.recentEvidence {
			delete(s.recentEvidence, key)
		}
		return
	}
	for len(s.bindings) > maxBindings {
		oldestResponseID := ""
		var oldestExpiresAt time.Time
		for responseID, binding := range s.bindings {
			if oldestResponseID == "" || binding.expiresAt.Before(oldestExpiresAt) {
				oldestResponseID = responseID
				oldestExpiresAt = binding.expiresAt
			}
		}
		if oldestResponseID == "" {
			return
		}
		delete(s.bindings, oldestResponseID)
	}
	for len(s.recentEvidence) > maxBindings {
		oldestKey := ""
		var oldestExpiresAt time.Time
		for key, recent := range s.recentEvidence {
			if oldestKey == "" || recent.expiresAt.Before(oldestExpiresAt) {
				oldestKey = key
				oldestExpiresAt = recent.expiresAt
			}
		}
		if oldestKey == "" {
			return
		}
		delete(s.recentEvidence, oldestKey)
	}
}

func (s *codexDirectContinuationStore) rememberRecentEvidenceLocked(authID, modelName, scopeKey string, responseOutputJSON []byte, now time.Time) {
	key := codexDirectRecentEvidenceKey(authID, modelName, scopeKey)
	if key == "" {
		return
	}
	s.recentEvidence[key] = codexDirectRecentEvidence{
		authID:             authID,
		modelName:          modelName,
		scopeKey:           scopeKey,
		responseOutputJSON: bytes.Clone(responseOutputJSON),
		expiresAt:          now.Add(codexDirectContinuationTTL),
	}
}

func (s *codexDirectContinuationStore) recentEvidenceLocked(authID, modelName, scopeKey string, now time.Time) (codexDirectRecentEvidence, bool) {
	key := codexDirectRecentEvidenceKey(authID, modelName, scopeKey)
	if key == "" {
		return codexDirectRecentEvidence{}, false
	}
	recent, ok := s.recentEvidence[key]
	if !ok {
		return codexDirectRecentEvidence{}, false
	}
	if !recent.expiresAt.IsZero() && !now.Before(recent.expiresAt) {
		delete(s.recentEvidence, key)
		return codexDirectRecentEvidence{}, false
	}
	if recent.authID != strings.TrimSpace(authID) || recent.modelName != strings.TrimSpace(modelName) || recent.scopeKey != strings.TrimSpace(scopeKey) {
		return codexDirectRecentEvidence{}, false
	}
	recent.responseOutputJSON = bytes.Clone(recent.responseOutputJSON)
	return recent, true
}

func codexDirectRecentEvidenceKey(authID, modelName, scopeKey string) string {
	authID = strings.TrimSpace(authID)
	modelName = strings.TrimSpace(modelName)
	scopeKey = strings.TrimSpace(scopeKey)
	if authID == "" || modelName == "" || scopeKey == "" {
		return ""
	}
	return authID + "\x00" + modelName + "\x00" + scopeKey
}

func repairCodexDirectContinuationRequest(rawJSON []byte, binding codexDirectContinuationBinding) ([]byte, error) {
	if strings.TrimSpace(binding.authID) == "" {
		return nil, fmt.Errorf("missing bound auth")
	}
	nextInput := gjson.GetBytes(rawJSON, "input")
	if !nextInput.Exists() || !nextInput.IsArray() {
		return nil, fmt.Errorf("continuation request is missing array input")
	}

	mergedInput := nextInput.Raw
	if !inputContainsFullTranscript(nextInput) {
		previousInput := gjson.GetBytes(binding.requestJSON, "input")
		if !previousInput.Exists() || !previousInput.IsArray() {
			return nil, fmt.Errorf("cached continuation request is missing array input")
		}

		var errMerge error
		mergedInput, errMerge = mergeJSONArrayRaw(previousInput.Raw, string(normalizeCodexDirectResponseOutputJSON(binding.responseOutputJSON)))
		if errMerge != nil {
			return nil, fmt.Errorf("merge cached response output: %w", errMerge)
		}
		mergedInput, errMerge = mergeJSONArrayRaw(mergedInput, nextInput.Raw)
		if errMerge != nil {
			return nil, fmt.Errorf("merge continuation input: %w", errMerge)
		}
	}

	if dedupedInput, errDedupe := dedupeFunctionCallsByCallID(mergedInput); errDedupe == nil {
		mergedInput = dedupedInput
	}
	if errValidate := validateCodexDirectContinuationToolOutputs(mergedInput); errValidate != nil {
		return nil, fmt.Errorf("invalid repaired tool outputs: %w", errValidate)
	}

	repaired, errDelete := sjson.DeleteBytes(rawJSON, "previous_response_id")
	if errDelete != nil {
		repaired = bytes.Clone(rawJSON)
	}
	var errSet error
	repaired, errSet = sjson.SetRawBytes(repaired, "input", []byte(mergedInput))
	if errSet != nil {
		return nil, fmt.Errorf("set repaired input: %w", errSet)
	}
	if !gjson.GetBytes(repaired, "model").Exists() {
		modelName := strings.TrimSpace(gjson.GetBytes(binding.requestJSON, "model").String())
		if modelName != "" {
			repaired, _ = sjson.SetBytes(repaired, "model", modelName)
		}
	}
	if !gjson.GetBytes(repaired, "instructions").Exists() {
		instructions := gjson.GetBytes(binding.requestJSON, "instructions")
		if instructions.Exists() {
			repaired, _ = sjson.SetRawBytes(repaired, "instructions", []byte(instructions.Raw))
		}
	}
	return repaired, nil
}

func validateCodexDirectContinuationToolOutputs(rawArray string) error {
	rawArray = strings.TrimSpace(rawArray)
	if rawArray == "" {
		return nil
	}

	var items []json.RawMessage
	if errUnmarshal := json.Unmarshal([]byte(rawArray), &items); errUnmarshal != nil {
		return errUnmarshal
	}

	callIDs := make(map[string]struct{}, len(items))
	for _, item := range items {
		itemType := strings.TrimSpace(gjson.GetBytes(item, "type").String())
		if !isResponsesToolCallType(itemType) {
			continue
		}
		callID := strings.TrimSpace(gjson.GetBytes(item, "call_id").String())
		if callID != "" {
			callIDs[callID] = struct{}{}
		}
	}

	for _, item := range items {
		itemType := strings.TrimSpace(gjson.GetBytes(item, "type").String())
		if !isResponsesToolCallOutputType(itemType) {
			continue
		}
		callID := strings.TrimSpace(gjson.GetBytes(item, "call_id").String())
		if callID == "" {
			return fmt.Errorf("tool output missing matching call")
		}
		if _, ok := callIDs[callID]; !ok {
			return fmt.Errorf("tool output missing matching call")
		}
	}
	return nil
}

func codexDirectResponseOutputJSON(root gjson.Result) []byte {
	for _, path := range []string{"output", "response.output"} {
		output := root.Get(path)
		if output.Exists() && output.IsArray() {
			return bytes.Clone([]byte(output.Raw))
		}
	}
	return []byte("[]")
}

func normalizeCodexDirectResponseOutputJSON(raw []byte) []byte {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return []byte("[]")
	}
	result := gjson.ParseBytes(trimmed)
	if result.Type == gjson.JSON && result.IsArray() {
		return bytes.Clone(trimmed)
	}
	return []byte("[]")
}

func codexDirectOutputJSONIsEmpty(raw []byte) bool {
	result := gjson.ParseBytes(normalizeCodexDirectResponseOutputJSON(raw))
	return !result.IsArray() || len(result.Array()) == 0
}

func codexDirectOutputHasAssistantOrToolEvidence(raw []byte) bool {
	result := gjson.ParseBytes(normalizeCodexDirectResponseOutputJSON(raw))
	if !result.IsArray() {
		return false
	}
	for _, item := range result.Array() {
		itemType := strings.TrimSpace(item.Get("type").String())
		if isResponsesToolCallType(itemType) && strings.TrimSpace(item.Get("call_id").String()) != "" {
			return true
		}
		if itemType == "message" && strings.TrimSpace(item.Get("role").String()) == "assistant" {
			return true
		}
	}
	return false
}

func codexDirectContinuationScopeKey(c *gin.Context, rawJSON []byte) string {
	if promptCacheKey := strings.TrimSpace(gjson.GetBytes(rawJSON, "prompt_cache_key").String()); promptCacheKey != "" {
		return "prompt_cache_key:" + promptCacheKey
	}
	if c == nil || c.Request == nil {
		return ""
	}
	for _, headerName := range []string{"Session_id", "X-Codex-Turn-Metadata"} {
		if headerValue := strings.TrimSpace(c.Request.Header.Get(headerName)); headerValue != "" {
			return headerName + ":" + headerValue
		}
	}
	return ""
}

func isCodexDirectContinuationRequest(c *gin.Context) bool {
	return isCodexDirectResponsesRequest(c) || isCodexDirectCompactRequest(c)
}

func codexDirectRouteKind(c *gin.Context) string {
	switch {
	case isCodexDirectCompactRequest(c):
		return "compact"
	case isCodexDirectResponsesRequest(c):
		return "responses"
	default:
		return "unknown"
	}
}

func isCodexDirectResponsesRequest(c *gin.Context) bool {
	return c != nil && c.Request != nil && c.Request.URL != nil && c.Request.URL.Path == codexDirectResponsesPath
}

func isCodexDirectCompactRequest(c *gin.Context) bool {
	return c != nil && c.Request != nil && c.Request.URL != nil && c.Request.URL.Path == codexDirectCompactPath
}

func writeCodexDirectContinuationError(c *gin.Context) {
	if c == nil {
		return
	}
	c.JSON(http.StatusConflict, handlers.ErrorResponse{
		Error: handlers.ErrorDetail{
			Message: "Codex continuation cannot be safely routed to the original auth; retry without previous_response_id",
			Type:    "invalid_request_error",
			Code:    "codex_continuation_auth_unknown",
		},
	})
}

func writeCodexDirectContinuationRepairError(c *gin.Context) {
	if c == nil {
		return
	}
	c.JSON(http.StatusConflict, handlers.ErrorResponse{
		Error: handlers.ErrorDetail{
			Message: "Codex continuation cannot be safely reconstructed locally; retry with a full input transcript",
			Type:    "invalid_request_error",
			Code:    "codex_continuation_repair_failed",
		},
	})
}
