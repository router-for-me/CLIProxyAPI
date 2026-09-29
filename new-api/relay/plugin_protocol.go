package relay

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
)

const (
	pluginResponseStatusInProgress = "in_progress"
	pluginResponseStatusCompleted  = "completed"
	pluginResponseStatusFailed     = "failed"
	pluginResponseStatusIncomplete = "incomplete"
	pluginResponseStatusQueued     = "queued"
)

// PluginProtocolLimits bounds untrusted semantic output before it reaches the
// host-owned Responses state machine.
type PluginProtocolLimits struct {
	MaxEventsPerTick      int
	MaxEventsBytes        int
	MaxEventBytes         int
	MaxEventDepth         int
	MaxStateBytes         int
	MaxStateDepth         int
	MaxOutputs            int
	MaxTotalOutputBytes   int
	MaxMessageBytes       int
	MaxMetadataValueBytes int
	MaxCodeBytes          int
}

func DefaultPluginProtocolLimits() PluginProtocolLimits {
	return PluginProtocolLimits{
		MaxEventsPerTick:      16,
		MaxEventsBytes:        64 << 10,
		MaxEventBytes:         32 << 10,
		MaxEventDepth:         16,
		MaxStateBytes:         16 << 10,
		MaxStateDepth:         16,
		MaxOutputs:            64,
		MaxTotalOutputBytes:   1 << 20,
		MaxMessageBytes:       4 << 10,
		MaxMetadataValueBytes: 512,
		MaxCodeBytes:          128,
	}
}

func (l PluginProtocolLimits) withDefaults() PluginProtocolLimits {
	defaults := DefaultPluginProtocolLimits()
	if l.MaxEventsPerTick <= 0 {
		l.MaxEventsPerTick = defaults.MaxEventsPerTick
	}
	if l.MaxEventsBytes <= 0 {
		l.MaxEventsBytes = defaults.MaxEventsBytes
	}
	if l.MaxEventBytes <= 0 {
		l.MaxEventBytes = defaults.MaxEventBytes
	}
	if l.MaxEventDepth <= 0 {
		l.MaxEventDepth = defaults.MaxEventDepth
	}
	if l.MaxStateBytes <= 0 {
		l.MaxStateBytes = defaults.MaxStateBytes
	}
	if l.MaxStateDepth <= 0 {
		l.MaxStateDepth = defaults.MaxStateDepth
	}
	if l.MaxOutputs <= 0 {
		l.MaxOutputs = defaults.MaxOutputs
	}
	if l.MaxTotalOutputBytes <= 0 {
		l.MaxTotalOutputBytes = defaults.MaxTotalOutputBytes
	}
	if l.MaxMessageBytes <= 0 {
		l.MaxMessageBytes = defaults.MaxMessageBytes
	}
	if l.MaxMetadataValueBytes <= 0 {
		l.MaxMetadataValueBytes = defaults.MaxMetadataValueBytes
	}
	if l.MaxCodeBytes <= 0 {
		l.MaxCodeBytes = defaults.MaxCodeBytes
	}
	return l
}

// ProtocolState distinguishes an omitted state property from an explicit JSON
// null. Value is always a validated, detached JSON value when Present is true.
type ProtocolState struct {
	Present bool
	Null    bool
	Value   json.RawMessage
}

func (s ProtocolState) PluginValue() (any, error) {
	if !s.Present || s.Null {
		return nil, nil
	}
	var value any
	if err := common.Unmarshal(s.Value, &value); err != nil {
		return nil, err
	}
	return value, nil
}

type ProtocolSemanticEvent struct {
	Type     string
	Progress *float64
	Message  *string
	Data     json.RawMessage
	Code     *string
}

type ProtocolEventResult struct {
	Events []ProtocolSemanticEvent
	State  ProtocolState
	Done   bool
}

// DecodePluginProtocolEventResult converts an exported JS value into the
// deliberately small semantic event contract. Unknown fields are rejected so
// plugins cannot smuggle protocol-owned wire fields into the response.
func DecodePluginProtocolEventResult(value any, limits PluginProtocolLimits) (ProtocolEventResult, error) {
	limits = limits.withDefaults()
	encoded, err := common.Marshal(value)
	if err != nil {
		return ProtocolEventResult{}, fmt.Errorf("protocol event result is not JSON-compatible: %w", err)
	}
	maxResultBytes := limits.MaxEventsBytes + limits.MaxStateBytes + 4096
	if len(encoded) > maxResultBytes {
		return ProtocolEventResult{}, fmt.Errorf("protocol event result exceeds %d bytes", maxResultBytes)
	}

	var fields map[string]json.RawMessage
	if err := common.Unmarshal(encoded, &fields); err != nil || fields == nil {
		return ProtocolEventResult{}, errors.New("protocol event result must be an object")
	}
	for name := range fields {
		switch name {
		case "events", "state", "done":
		default:
			return ProtocolEventResult{}, fmt.Errorf("protocol event result contains unknown field %q", name)
		}
	}

	rawEvents, ok := fields["events"]
	if !ok || isJSONNull(rawEvents) {
		return ProtocolEventResult{}, errors.New("protocol event result events must be an array")
	}
	if len(rawEvents) > limits.MaxEventsBytes {
		return ProtocolEventResult{}, fmt.Errorf("protocol events exceed %d bytes", limits.MaxEventsBytes)
	}
	var encodedEvents []json.RawMessage
	if err := common.Unmarshal(rawEvents, &encodedEvents); err != nil {
		return ProtocolEventResult{}, errors.New("protocol event result events must be an array")
	}
	if len(encodedEvents) > limits.MaxEventsPerTick {
		return ProtocolEventResult{}, fmt.Errorf("protocol events exceed limit of %d", limits.MaxEventsPerTick)
	}

	rawDone, ok := fields["done"]
	if !ok || isJSONNull(rawDone) {
		return ProtocolEventResult{}, errors.New("protocol event result done must be a boolean")
	}
	var done bool
	if err := common.Unmarshal(rawDone, &done); err != nil {
		return ProtocolEventResult{}, errors.New("protocol event result done must be a boolean")
	}

	result := ProtocolEventResult{
		Events: make([]ProtocolSemanticEvent, 0, len(encodedEvents)),
		Done:   done,
	}
	for _, rawEvent := range encodedEvents {
		event, err := decodePluginSemanticEvent(rawEvent, limits)
		if err != nil {
			return ProtocolEventResult{}, err
		}
		result.Events = append(result.Events, event)
	}

	if rawState, exists := fields["state"]; exists {
		if len(rawState) > limits.MaxStateBytes {
			return ProtocolEventResult{}, fmt.Errorf("protocol state exceeds %d bytes", limits.MaxStateBytes)
		}
		depth, err := pluginJSONDepth(rawState)
		if err != nil {
			return ProtocolEventResult{}, errors.New("protocol state must be JSON-compatible")
		}
		if depth > limits.MaxStateDepth {
			return ProtocolEventResult{}, fmt.Errorf("protocol state exceeds depth limit of %d", limits.MaxStateDepth)
		}
		result.State = ProtocolState{
			Present: true,
			Null:    isJSONNull(rawState),
			Value:   append(json.RawMessage(nil), rawState...),
		}
	}
	return result, nil
}

func decodePluginSemanticEvent(raw json.RawMessage, limits PluginProtocolLimits) (ProtocolSemanticEvent, error) {
	if len(raw) > limits.MaxEventBytes {
		return ProtocolSemanticEvent{}, fmt.Errorf("protocol event exceeds %d bytes", limits.MaxEventBytes)
	}
	depth, err := pluginJSONDepth(raw)
	if err != nil {
		return ProtocolSemanticEvent{}, errors.New("protocol event must be a JSON object")
	}
	if depth > limits.MaxEventDepth {
		return ProtocolSemanticEvent{}, fmt.Errorf("protocol event exceeds depth limit of %d", limits.MaxEventDepth)
	}

	var fields map[string]json.RawMessage
	if err := common.Unmarshal(raw, &fields); err != nil || fields == nil {
		return ProtocolSemanticEvent{}, errors.New("protocol event must be a JSON object")
	}
	rawType, ok := fields["type"]
	if !ok || isJSONNull(rawType) {
		return ProtocolSemanticEvent{}, errors.New("protocol event type is required")
	}
	var eventType string
	if err := common.Unmarshal(rawType, &eventType); err != nil {
		return ProtocolSemanticEvent{}, errors.New("protocol event type must be a string")
	}

	event := ProtocolSemanticEvent{Type: eventType}
	switch eventType {
	case "progress":
		if err := rejectUnknownProtocolFields(fields, "type", "progress", "message"); err != nil {
			return ProtocolSemanticEvent{}, err
		}
		if rawProgress, exists := fields["progress"]; exists {
			if isJSONNull(rawProgress) {
				return ProtocolSemanticEvent{}, errors.New("progress event progress must be a number")
			}
			var progress float64
			if err := common.Unmarshal(rawProgress, &progress); err != nil ||
				math.IsNaN(progress) || math.IsInf(progress, 0) ||
				progress < 0 || progress > 100 {
				return ProtocolSemanticEvent{}, errors.New("progress event progress must be between 0 and 100")
			}
			event.Progress = &progress
		}
		if rawMessage, exists := fields["message"]; exists {
			message, err := decodeBoundedProtocolString(rawMessage, "progress event message", limits.MaxMetadataValueBytes, false)
			if err != nil {
				return ProtocolSemanticEvent{}, err
			}
			event.Message = &message
		}
	case "output":
		if err := rejectUnknownProtocolFields(fields, "type", "data"); err != nil {
			return ProtocolSemanticEvent{}, err
		}
		rawData, exists := fields["data"]
		if !exists {
			return ProtocolSemanticEvent{}, errors.New("output event data is required")
		}
		if len(rawData) > limits.MaxEventBytes {
			return ProtocolSemanticEvent{}, fmt.Errorf("output event data exceeds %d bytes", limits.MaxEventBytes)
		}
		event.Data = append(json.RawMessage(nil), rawData...)
	case "error":
		if err := rejectUnknownProtocolFields(fields, "type", "code", "message"); err != nil {
			return ProtocolSemanticEvent{}, err
		}
		rawMessage, exists := fields["message"]
		if !exists {
			return ProtocolSemanticEvent{}, errors.New("error event message is required")
		}
		message, err := decodeBoundedProtocolString(rawMessage, "error event message", limits.MaxMessageBytes, true)
		if err != nil {
			return ProtocolSemanticEvent{}, err
		}
		event.Message = &message
		if rawCode, exists := fields["code"]; exists {
			code, err := decodeBoundedProtocolString(rawCode, "error event code", limits.MaxCodeBytes, false)
			if err != nil {
				return ProtocolSemanticEvent{}, err
			}
			event.Code = &code
		}
	default:
		return ProtocolSemanticEvent{}, fmt.Errorf("unsupported protocol event type %q", eventType)
	}
	return event, nil
}

func rejectUnknownProtocolFields(fields map[string]json.RawMessage, allowed ...string) error {
	allowedSet := make(map[string]struct{}, len(allowed))
	for _, name := range allowed {
		allowedSet[name] = struct{}{}
	}
	for name := range fields {
		if _, ok := allowedSet[name]; !ok {
			return fmt.Errorf("protocol event contains unknown field %q", name)
		}
	}
	return nil
}

func decodeBoundedProtocolString(raw json.RawMessage, field string, maxBytes int, required bool) (string, error) {
	if isJSONNull(raw) {
		return "", fmt.Errorf("%s must be a string", field)
	}
	var value string
	if err := common.Unmarshal(raw, &value); err != nil {
		return "", fmt.Errorf("%s must be a string", field)
	}
	if required && strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("%s is required", field)
	}
	if len(value) > maxBytes {
		return "", fmt.Errorf("%s exceeds %d bytes", field, maxBytes)
	}
	return value, nil
}

func pluginJSONDepth(raw json.RawMessage) (int, error) {
	var value any
	if err := common.Unmarshal(raw, &value); err != nil {
		return 0, err
	}
	var depth func(any) int
	depth = func(current any) int {
		switch typed := current.(type) {
		case []any:
			maxChild := 0
			for _, child := range typed {
				maxChild = max(maxChild, depth(child))
			}
			return 1 + maxChild
		case map[string]any:
			maxChild := 0
			for _, child := range typed {
				maxChild = max(maxChild, depth(child))
			}
			return 1 + maxChild
		default:
			return 1
		}
	}
	return depth(value), nil
}

func isJSONNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

// PluginResponsesMachine owns the Responses wire state for one durable task.
// It contains no IO and is intentionally independent of plugin runtimes.
type PluginResponsesMachine struct {
	taskID           string
	responseID       string
	model            string
	createdAt        int64
	limits           PluginProtocolLimits
	nextSequence     int
	started          bool
	terminal         bool
	status           string
	metadata         map[string]string
	outputs          []dto.PluginResponsesOutput
	totalOutputBytes int
	usage            *dto.PluginResponsesUsage
	background       bool
}

func NewPluginResponsesMachine(taskID, model string, createdAt int64, limits PluginProtocolLimits) *PluginResponsesMachine {
	taskID = strings.TrimSpace(taskID)
	responseID := "resp_" + strings.TrimPrefix(taskID, "task_")
	return &PluginResponsesMachine{
		taskID:     taskID,
		responseID: responseID,
		model:      model,
		createdAt:  createdAt,
		limits:     limits.withDefaults(),
		status:     pluginResponseStatusInProgress,
		metadata: map[string]string{
			"task_id":        taskID,
			"task_status":    pluginResponseStatusQueued,
			"retrieval_path": "/v1/responses/" + responseID,
		},
		outputs: make([]dto.PluginResponsesOutput, 0),
	}
}

func (m *PluginResponsesMachine) SetBackground(background bool) {
	m.background = background
}

// PendingResponse is the host-synthesized non-terminal Responses snapshot.
// Callers must pass a non-terminal task status; completed/failed/incomplete
// inputs are mapped to in_progress so the wire status stays queued|in_progress.
func (m *PluginResponsesMachine) PendingResponse(taskStatus string) map[string]any {
	status := pluginTaskStatus(taskStatus)
	if status != pluginResponseStatusQueued && status != pluginResponseStatusInProgress {
		status = pluginResponseStatusInProgress
	}
	return map[string]any{
		"id":                 m.responseID,
		"object":             "response",
		"created_at":         m.createdAt,
		"status":             status,
		"background":         m.background,
		"completed_at":       nil,
		"error":              nil,
		"incomplete_details": nil,
		"model":              m.model,
		"output":             []any{},
		"usage":              nil,
		"metadata": map[string]string{
			"task_id":        m.taskID,
			"task_status":    status,
			"retrieval_path": "/v1/responses/" + m.responseID,
		},
	}
}

func (m *PluginResponsesMachine) CreatedEvent() (dto.PluginResponsesStreamEvent, error) {
	if m.started {
		return dto.PluginResponsesStreamEvent{}, errors.New("response.created was already emitted")
	}
	if m.terminal {
		return dto.PluginResponsesStreamEvent{}, errors.New("response is already terminal")
	}
	m.started = true
	return m.responseEvent("response.created"), nil
}

// ApplyTick maps bounded plugin semantics onto host-owned Responses events.
// taskStatus is the current durable DB status (for example, IN_PROGRESS,
// SUCCESS, or FAILURE).
func (m *PluginResponsesMachine) ApplyTick(result ProtocolEventResult, taskStatus string) ([]dto.PluginResponsesStreamEvent, error) {
	if !m.started {
		return nil, errors.New("response.created must be emitted before applying events")
	}
	if m.terminal {
		return nil, errors.New("response is already terminal")
	}
	if strings.EqualFold(strings.TrimSpace(taskStatus), "FAILURE") {
		m.metadata["task_status"] = pluginResponseStatusFailed
		return []dto.PluginResponsesStreamEvent{m.fail("server_error", "The task failed.")}, nil
	}

	outputTexts := make(map[int]string)
	additionalBytes := 0
	additionalOutputs := 0
	for index, event := range result.Events {
		switch event.Type {
		case "progress":
			if event.Progress != nil &&
				(math.IsNaN(*event.Progress) || math.IsInf(*event.Progress, 0) ||
					*event.Progress < 0 || *event.Progress > 100) {
				return nil, errors.New("progress event progress must be between 0 and 100")
			}
			if event.Message != nil && len(*event.Message) > m.limits.MaxMetadataValueBytes {
				return nil, fmt.Errorf("progress event message exceeds %d bytes", m.limits.MaxMetadataValueBytes)
			}
		case "output":
			text, err := pluginOutputText(event.Data)
			if err != nil {
				return nil, err
			}
			if len(text) > m.limits.MaxEventBytes {
				return nil, fmt.Errorf("output event data exceeds %d bytes", m.limits.MaxEventBytes)
			}
			outputTexts[index] = text
			additionalBytes += len(text)
			additionalOutputs++
		case "error":
			if event.Message == nil || strings.TrimSpace(*event.Message) == "" {
				return nil, errors.New("error event message is required")
			}
		default:
			return nil, fmt.Errorf("unsupported protocol event type %q", event.Type)
		}
	}
	if len(m.outputs)+additionalOutputs > m.limits.MaxOutputs {
		return nil, fmt.Errorf("response outputs exceed limit of %d", m.limits.MaxOutputs)
	}
	if m.totalOutputBytes+additionalBytes > m.limits.MaxTotalOutputBytes {
		return nil, fmt.Errorf("response output exceeds cumulative limit of %d bytes", m.limits.MaxTotalOutputBytes)
	}

	m.metadata["task_status"] = pluginTaskStatus(taskStatus)
	events := make([]dto.PluginResponsesStreamEvent, 0, len(result.Events)*2+1)
	for index, semantic := range result.Events {
		switch semantic.Type {
		case "progress":
			if semantic.Progress != nil {
				m.metadata["task_progress"] = strconv.FormatFloat(*semantic.Progress, 'f', -1, 64)
			}
			if semantic.Message != nil {
				m.metadata["task_message"] = *semantic.Message
			}
			if len(m.outputs) == 0 {
				events = append(events, m.progressEvent())
			}
		case "output":
			events = append(events, m.appendOutput(outputTexts[index])...)
		case "error":
			events = append(events, m.fail(
				"server_error",
				"The task failed.",
			))
			return events, nil
		}
	}

	switch strings.ToUpper(strings.TrimSpace(taskStatus)) {
	case "SUCCESS":
		events = append(events, m.complete())
	case "FAILURE":
		events = append(events, m.fail("server_error", "The task failed."))
	default:
		if result.Done {
			events = append(events, m.incomplete())
		}
	}
	return events, nil
}

// FailureEvent terminates an already-started stream after a host observation,
// hook, or validation failure. It never accepts a detail string, preventing
// upstream and plugin internals from reaching clients.
func (m *PluginResponsesMachine) FailureEvent(taskStatus ...string) (dto.PluginResponsesStreamEvent, error) {
	if !m.started {
		return dto.PluginResponsesStreamEvent{}, errors.New("response.created must be emitted before response.failed")
	}
	if m.terminal {
		return dto.PluginResponsesStreamEvent{}, errors.New("response is already terminal")
	}
	m.setPersistedTaskStatus(taskStatus)
	return m.fail("server_error", "The task could not be observed."), nil
}

func (m *PluginResponsesMachine) TimeoutEvent(taskStatus ...string) (dto.PluginResponsesStreamEvent, error) {
	if !m.started {
		return dto.PluginResponsesStreamEvent{}, errors.New("response.created must be emitted before response.incomplete")
	}
	if m.terminal {
		return dto.PluginResponsesStreamEvent{}, errors.New("response is already terminal")
	}
	m.setPersistedTaskStatus(taskStatus)
	return m.incomplete(), nil
}

// FinalResponse validates a plugin-authored complete Responses object and
// overwrites every host-owned identity and lifecycle field. Unknown
// protocol fields are retained so the unreleased v1 Record contract can
// represent response features beyond output_text.
func (m *PluginResponsesMachine) FinalResponse(payload any, taskStatus string) (map[string]any, error) {
	if m.started || m.terminal {
		return nil, errors.New("response state machine has already started")
	}
	switch strings.ToUpper(strings.TrimSpace(taskStatus)) {
	case "SUCCESS":
		response, err := m.canonicalFinalResponse(payload)
		if err != nil {
			return nil, err
		}
		m.status = pluginResponseStatusCompleted
		m.metadata["task_status"] = pluginResponseStatusCompleted
		response["id"] = m.responseID
		response["object"] = "response"
		response["created_at"] = m.createdAt
		response["status"] = pluginResponseStatusCompleted
		response["error"] = nil
		response["incomplete_details"] = nil
		response["model"] = m.model
		response["metadata"] = m.finalMetadata(response["metadata"])
		response["usage"] = zeroPluginResponsesUsage()
		delete(response, "sequence_number")
		m.terminal = true
		return response, nil
	case "FAILURE":
		m.status = pluginResponseStatusFailed
		m.metadata["task_status"] = pluginResponseStatusFailed
	default:
		return nil, errors.New("final response requires a terminal task")
	}
	m.terminal = true
	response, err := pluginResponseMap(m.responseSnapshot(&dto.PluginResponsesError{
		Code:    "server_error",
		Message: "The task failed.",
	}))
	if err != nil {
		return nil, err
	}
	return response, nil
}

// FinalFromEvents synthesizes the retrieval Response for stream-only plugins
// from one renderEvents call at terminal task status. Synthesis runs on a
// scratch machine so a hook failure leaves the receiver untouched and the
// caller's failure-envelope path (which requires an unstarted machine) stays valid.
func (m *PluginResponsesMachine) FinalFromEvents(result ProtocolEventResult, taskStatus string) (map[string]any, error) {
	if m.started || m.terminal {
		return nil, errors.New("response state machine has already started")
	}
	scratch := NewPluginResponsesMachine(m.taskID, m.model, m.createdAt, m.limits)
	scratch.background = m.background
	scratch.started = true
	if _, err := scratch.ApplyTick(result, taskStatus); err != nil {
		return nil, err
	}
	if !scratch.terminal {
		return nil, errors.New("renderEvents did not terminate at terminal task status")
	}
	if scratch.status != pluginResponseStatusCompleted {
		return nil, errors.New("renderEvents reported failure at terminal task status")
	}
	*m = *scratch
	return pluginResponseMap(m.responseSnapshot(nil))
}

// FailureResponse returns a sanitized non-stream failure for host-side
// protocol errors.
func (m *PluginResponsesMachine) FailureResponse(taskStatus ...string) (*dto.PluginResponsesResponse, error) {
	if m.started || m.terminal {
		return nil, errors.New("response state machine has already started")
	}
	m.status = pluginResponseStatusFailed
	m.setPersistedTaskStatus(taskStatus)
	m.terminal = true
	return m.responseSnapshot(&dto.PluginResponsesError{
		Code:    "server_error",
		Message: "The task could not be observed.",
	}), nil
}

// TimeoutResponse is the documented non-stream polling timeout shape. Unlike
// a live stream timeout, its top-level state remains queued so clients know to
// use retrieval_path rather than treating observation timeout as task failure.
func (m *PluginResponsesMachine) TimeoutResponse(taskStatus ...string) (*dto.PluginResponsesResponse, error) {
	if m.started || m.terminal {
		return nil, errors.New("response state machine has already started")
	}
	m.status = pluginResponseStatusQueued
	lastStatus := ""
	if len(taskStatus) > 0 {
		lastStatus = taskStatus[0]
	}
	persistedStatus := pluginTaskStatus(lastStatus)
	if persistedStatus == "" {
		persistedStatus = pluginResponseStatusQueued
	}
	m.metadata["task_status"] = persistedStatus
	m.terminal = true
	return m.responseSnapshot(nil), nil
}

func (m *PluginResponsesMachine) canonicalFinalResponse(payload any) (map[string]any, error) {
	encoded, err := common.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("final response is not JSON-compatible: %w", err)
	}
	if len(encoded) > m.limits.MaxTotalOutputBytes+(64<<10) {
		return nil, fmt.Errorf("final response exceeds %d bytes", m.limits.MaxTotalOutputBytes+(64<<10))
	}
	depth, err := pluginJSONDepth(encoded)
	if err != nil || depth > m.limits.MaxEventDepth {
		return nil, fmt.Errorf("final response exceeds depth limit of %d", m.limits.MaxEventDepth)
	}
	var response map[string]any
	if err = common.Unmarshal(encoded, &response); err != nil || response == nil {
		return nil, errors.New("final response must be an object")
	}

	rawOutput, exists := response["output"]
	output := []any{}
	if exists {
		var ok bool
		output, ok = rawOutput.([]any)
		if !ok {
			return nil, errors.New("final response output must be an array")
		}
	}
	if len(output) > m.limits.MaxOutputs {
		return nil, fmt.Errorf("response outputs exceed limit of %d", m.limits.MaxOutputs)
	}
	for outputIndex, rawItem := range output {
		item, ok := rawItem.(map[string]any)
		if !ok {
			return nil, errors.New("final response output items must be objects")
		}
		itemType, ok := item["type"].(string)
		if !ok || itemType != "message" {
			return nil, errors.New("final response output items must be message objects")
		}
		role, ok := item["role"].(string)
		if !ok || role != "assistant" {
			return nil, errors.New("final response message role must be assistant")
		}
		item["id"] = fmt.Sprintf("item_%s_%d", m.taskID, outputIndex)
		item["status"] = pluginResponseStatusCompleted
		rawContent, hasContent := item["content"]
		if !hasContent {
			return nil, errors.New("final response message content must be an array")
		}
		content, ok := rawContent.([]any)
		if !ok || len(content) > m.limits.MaxOutputs {
			return nil, fmt.Errorf("final response output content must contain at most %d objects", m.limits.MaxOutputs)
		}
		for contentIndex, rawPart := range content {
			part, ok := rawPart.(map[string]any)
			if !ok {
				return nil, errors.New("final response output content parts must be objects")
			}
			partType, ok := part["type"].(string)
			if !ok {
				return nil, errors.New("final response content part type is required")
			}
			switch partType {
			case "output_text":
				if _, ok = part["text"].(string); !ok {
					return nil, errors.New("final response output_text text must be a string")
				}
				if _, ok = part["annotations"].([]any); !ok {
					return nil, errors.New("final response output_text annotations must be an array")
				}
				if _, ok = part["logprobs"].([]any); !ok {
					return nil, errors.New("final response output_text logprobs must be an array")
				}
			case "refusal":
				if _, ok = part["refusal"].(string); !ok {
					return nil, errors.New("final response refusal must be a string")
				}
			default:
				return nil, fmt.Errorf("unsupported final response content part type %q", partType)
			}
			part["id"] = fmt.Sprintf("content_%s_%d_%d", m.taskID, outputIndex, contentIndex)
		}
	}
	encodedOutput, err := common.Marshal(output)
	if err != nil || len(encodedOutput) > m.limits.MaxTotalOutputBytes {
		return nil, fmt.Errorf("final response output exceeds %d bytes", m.limits.MaxTotalOutputBytes)
	}
	response["output"] = output
	if err = normalizeFinalResponseDefaults(response); err != nil {
		return nil, err
	}
	return response, nil
}

func normalizeFinalResponseDefaults(response map[string]any) error {
	if instructions, exists := response["instructions"]; exists {
		switch instructions.(type) {
		case nil, string, []any:
		default:
			return errors.New("final response instructions must be null, a string, or an array")
		}
	} else {
		response["instructions"] = nil
	}
	if parallel, exists := response["parallel_tool_calls"]; exists {
		if _, ok := parallel.(bool); !ok {
			return errors.New("final response parallel_tool_calls must be a boolean")
		}
	} else {
		response["parallel_tool_calls"] = true
	}
	if err := normalizeFinalResponseNumber(response, "temperature", 1, 0, 2); err != nil {
		return err
	}
	if toolChoice, exists := response["tool_choice"]; exists {
		switch toolChoice.(type) {
		case string, map[string]any:
		default:
			return errors.New("final response tool_choice must be a string or object")
		}
	} else {
		response["tool_choice"] = "auto"
	}
	if tools, exists := response["tools"]; exists {
		if _, ok := tools.([]any); !ok {
			return errors.New("final response tools must be an array")
		}
	} else {
		response["tools"] = []any{}
	}
	return normalizeFinalResponseNumber(response, "top_p", 1, 0, 1)
}

func normalizeFinalResponseNumber(
	response map[string]any,
	field string,
	defaultValue float64,
	minimum float64,
	maximum float64,
) error {
	value, exists := response[field]
	if !exists {
		response[field] = defaultValue
		return nil
	}
	number, ok := value.(float64)
	if !ok || math.IsNaN(number) || math.IsInf(number, 0) || number < minimum || number > maximum {
		return fmt.Errorf("final response %s must be between %g and %g", field, minimum, maximum)
	}
	return nil
}

func (m *PluginResponsesMachine) finalMetadata(pluginValue any) map[string]string {
	metadata := make(map[string]string)
	if pluginMetadata, ok := pluginValue.(map[string]any); ok {
		keys := make([]string, 0, len(pluginMetadata))
		for key := range pluginMetadata {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		pluginLimit := max(0, 16-len(m.metadata))
		for _, key := range keys {
			if len(metadata) >= pluginLimit {
				break
			}
			if _, hostOwned := m.metadata[key]; hostOwned {
				continue
			}
			rawValue := pluginMetadata[key]
			value, stringOK := rawValue.(string)
			if !stringOK || len(key) > 64 || len(value) > m.limits.MaxMetadataValueBytes {
				continue
			}
			metadata[key] = value
		}
	}
	maps.Copy(metadata, m.metadata)
	return metadata
}

func pluginResponseMap(response *dto.PluginResponsesResponse) (map[string]any, error) {
	encoded, err := common.Marshal(response)
	if err != nil {
		return nil, err
	}
	var value map[string]any
	if err = common.Unmarshal(encoded, &value); err != nil {
		return nil, err
	}
	return value, nil
}

func (m *PluginResponsesMachine) appendOutput(text string) []dto.PluginResponsesStreamEvent {
	outputIndex := len(m.outputs)
	itemID := fmt.Sprintf("msg_%s_%d", m.taskID, outputIndex)
	contentID := fmt.Sprintf("content_%s_%d", m.taskID, outputIndex)
	emptyLogprobs := []any{}

	addedItem := dto.PluginResponsesOutput{
		ID:      itemID,
		Type:    "message",
		Status:  pluginResponseStatusInProgress,
		Role:    "assistant",
		Content: []dto.PluginResponsesContent{},
	}
	partAdded := dto.PluginResponsesContent{
		ID:          contentID,
		Type:        "output_text",
		Text:        "",
		Annotations: []any{},
		Logprobs:    []any{},
	}
	completedItem := m.newCompletedOutputWithIDs(itemID, contentID, text)
	m.outputs = append(m.outputs, completedItem)
	m.totalOutputBytes += len(text)

	return []dto.PluginResponsesStreamEvent{
		m.event(dto.PluginResponsesStreamEvent{
			Type:        "response.output_item.added",
			OutputIndex: intPointer(outputIndex),
			Item:        &addedItem,
		}),
		m.event(dto.PluginResponsesStreamEvent{
			Type:         "response.content_part.added",
			OutputIndex:  intPointer(outputIndex),
			ContentIndex: intPointer(0),
			ItemID:       itemID,
			Part:         &partAdded,
		}),
		m.event(dto.PluginResponsesStreamEvent{
			Type:         "response.output_text.delta",
			OutputIndex:  intPointer(outputIndex),
			ContentIndex: intPointer(0),
			ItemID:       itemID,
			Delta:        &text,
			Logprobs:     &emptyLogprobs,
		}),
		m.event(dto.PluginResponsesStreamEvent{
			Type:         "response.output_text.done",
			OutputIndex:  intPointer(outputIndex),
			ContentIndex: intPointer(0),
			ItemID:       itemID,
			Text:         &text,
			Logprobs:     &emptyLogprobs,
		}),
		m.event(dto.PluginResponsesStreamEvent{
			Type:         "response.content_part.done",
			OutputIndex:  intPointer(outputIndex),
			ContentIndex: intPointer(0),
			ItemID:       itemID,
			Part:         &completedItem.Content[0],
		}),
		m.event(dto.PluginResponsesStreamEvent{
			Type:        "response.output_item.done",
			OutputIndex: intPointer(outputIndex),
			Item:        &completedItem,
		}),
	}
}

func (m *PluginResponsesMachine) newCompletedOutputWithIDs(itemID, contentID, text string) dto.PluginResponsesOutput {
	return dto.PluginResponsesOutput{
		ID:     itemID,
		Type:   "message",
		Status: pluginResponseStatusCompleted,
		Role:   "assistant",
		Content: []dto.PluginResponsesContent{
			{
				ID:          contentID,
				Type:        "output_text",
				Text:        text,
				Annotations: []any{},
				Logprobs:    []any{},
			},
		},
	}
}

func (m *PluginResponsesMachine) complete() dto.PluginResponsesStreamEvent {
	m.status = pluginResponseStatusCompleted
	m.metadata["task_status"] = pluginResponseStatusCompleted
	m.usage = zeroPluginResponsesUsage()
	m.terminal = true
	return m.responseEvent("response.completed")
}

func (m *PluginResponsesMachine) incomplete() dto.PluginResponsesStreamEvent {
	m.status = pluginResponseStatusIncomplete
	m.usage = zeroPluginResponsesUsage()
	m.terminal = true
	return m.responseEvent("response.incomplete")
}

func (m *PluginResponsesMachine) fail(code, message string) dto.PluginResponsesStreamEvent {
	m.status = pluginResponseStatusFailed
	m.terminal = true
	return m.event(dto.PluginResponsesStreamEvent{
		Type: "response.failed",
		Response: m.responseSnapshot(&dto.PluginResponsesError{
			Code:    code,
			Message: message,
		}),
	})
}

func (m *PluginResponsesMachine) responseEvent(eventType string) dto.PluginResponsesStreamEvent {
	return m.event(dto.PluginResponsesStreamEvent{
		Type:     eventType,
		Response: m.responseSnapshot(nil),
	})
}

func (m *PluginResponsesMachine) progressEvent() dto.PluginResponsesStreamEvent {
	response := m.responseSnapshot(nil)
	// Progress events carry task metadata, while output content is already
	// represented by its own item/content events and the eventual terminal
	// snapshot. Keeping this list empty prevents repeated 1 MiB snapshots.
	response.Output = []dto.PluginResponsesOutput{}
	return m.event(dto.PluginResponsesStreamEvent{
		Type:     "response.in_progress",
		Response: response,
	})
}

func (m *PluginResponsesMachine) setPersistedTaskStatus(taskStatus []string) {
	if len(taskStatus) == 0 {
		return
	}
	status := pluginTaskStatus(taskStatus[0])
	if status != "" {
		m.metadata["task_status"] = status
	}
}

func (m *PluginResponsesMachine) event(event dto.PluginResponsesStreamEvent) dto.PluginResponsesStreamEvent {
	event.SequenceNumber = m.nextSequence
	m.nextSequence++
	return event
}

func (m *PluginResponsesMachine) responseSnapshot(responseError *dto.PluginResponsesError) *dto.PluginResponsesResponse {
	metadata := make(map[string]string, len(m.metadata))
	maps.Copy(metadata, m.metadata)
	outputs := make([]dto.PluginResponsesOutput, len(m.outputs))
	for index, output := range m.outputs {
		outputs[index] = output
		outputs[index].Content = append([]dto.PluginResponsesContent(nil), output.Content...)
	}
	return &dto.PluginResponsesResponse{
		ID:                m.responseID,
		Object:            "response",
		CreatedAt:         m.createdAt,
		Status:            m.status,
		Error:             responseError,
		IncompleteDetails: nil,
		Instructions:      nil,
		Model:             m.model,
		Output:            outputs,
		ParallelToolCalls: true,
		Temperature:       1,
		ToolChoice:        "auto",
		Tools:             []any{},
		TopP:              1,
		Metadata:          metadata,
		Usage:             m.usage,
	}
}

func zeroPluginResponsesUsage() *dto.PluginResponsesUsage {
	return &dto.PluginResponsesUsage{}
}

func pluginOutputText(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "", errors.New("output event data is required")
	}
	if common.GetJsonType(raw) == "string" {
		var text string
		if err := common.Unmarshal(raw, &text); err != nil {
			return "", errors.New("output event data must be JSON-compatible")
		}
		return text, nil
	}
	var value any
	if err := common.Unmarshal(raw, &value); err != nil {
		return "", errors.New("output event data must be JSON-compatible")
	}
	encoded, err := common.Marshal(value)
	if err != nil {
		return "", errors.New("output event data must be JSON-compatible")
	}
	return string(encoded), nil
}

func pluginTaskStatus(taskStatus string) string {
	switch strings.ToUpper(strings.TrimSpace(taskStatus)) {
	case "SUCCESS":
		return pluginResponseStatusCompleted
	case "FAILURE":
		return pluginResponseStatusFailed
	case "IN_PROGRESS":
		return pluginResponseStatusInProgress
	case "NOT_START", "SUBMITTED", "QUEUED", "UNKNOWN", "":
		return pluginResponseStatusQueued
	default:
		return pluginResponseStatusQueued
	}
}

func intPointer(value int) *int {
	return &value
}
