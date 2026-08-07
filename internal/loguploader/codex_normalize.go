package loguploader

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"
)

// codexNormalizedRecord is the Go port of the Python transform script's
// output schema.  Six fields (ExtraInfo, Tools, Inputs, Response,
// ToolResult, Metadata) are JSON strings (double-encoded), not nested objects.
type codexNormalizedRecord struct {
	MessageID      string `json:"message_id"`
	ConversationID string `json:"conversation_id"`
	SessionID      string `json:"session_id"`
	ThinkType      any    `json:"think_type"`
	ExtraInfo      string `json:"extra_info"`
	Tools          string `json:"tools"`
	Inputs         string `json:"inputs"`
	Response       string `json:"response"`
	Timestamp      string `json:"timestamp"`
	ModelName      string `json:"model_name"`
	UserID         string `json:"user_id"`
	ToolResult     string `json:"tool_result"`
	Metadata       string `json:"metadata"`
}

var (
	isoTimestampRe = regexp.MustCompile(
		`^(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2})(\.\d+)?(Z|[+-]\d{2}:\d{2})?$`)
	sseMarkerRe = regexp.MustCompile(`(?m)^(?:event|data):`)
)

// normalizeCodexRecord reads and normalizes a single Codex .log file.
// Returns (nil, sha256, nil) when response outputs are empty (record should
// be filtered out of the archive).
func normalizeCodexRecord(source sourceLog) (*codexNormalizedRecord, string, error) {
	raw, errRead := os.ReadFile(source.Path)
	if errRead != nil {
		return nil, "", fmt.Errorf("read codex log for normalization: %w", errRead)
	}
	// Verify the file has not changed since inspectSourceLog.
	info, errStat := os.Stat(source.Path)
	if errStat != nil {
		return nil, "", fmt.Errorf("stat codex log after read: %w", errStat)
	}
	if info.Size() != source.Size || !info.ModTime().Equal(source.ModTime) {
		return nil, "", fmt.Errorf("codex log changed during normalization: %s", source.Relative)
	}
	hash := fmt.Sprintf("%x", sha256.Sum256(raw))
	text := string(raw)

	// ---- Extract sections (reuse extractGateSection from session_parse.go) ----
	requestInfoRaw := extractGateSection(text, "REQUEST INFO")
	requestInfo, _ := parsePairs(strings.Split(requestInfoRaw, "\n"))

	headersRaw := extractGateSection(text, "HEADERS")
	headers, _ := parsePairs(strings.Split(headersRaw, "\n"))

	requestBodyRaw := extractGateSection(text, "REQUEST BODY")
	var requestBody map[string]any
	if trimmed := strings.TrimSpace(requestBodyRaw); trimmed != "" {
		requestBody = decodeJSONObject(trimmed)
	}
	if requestBody == nil {
		requestBody = make(map[string]any)
	}

	responseRaw := extractGateSection(text, "RESPONSE")
	responseEnvelope := parseResponseSection(responseRaw)

	// ---- Build request / response body views ----
	requestBodyView := requestBody
	responseBody := payloadObject(responseEnvelope)
	if responseBody == nil {
		responseBody = make(map[string]any)
	}

	// ---- Extract client_metadata and turn_metadata ----
	clientMetadata, _ := requestBodyView["client_metadata"].(map[string]any)
	if clientMetadata == nil {
		clientMetadata = make(map[string]any)
	}

	turnMetaRaw := firstPresent(
		caseInsensitiveGet(clientMetadata, "x-codex-turn-metadata"),
		caseInsensitiveGetAny(headers, "x-codex-turn-metadata"),
	)
	turnMetadata := parseJSONObject(turnMetaRaw)

	// ---- Identity fields ----
	messageID := firstPresent(
		caseInsensitiveGet(clientMetadata, "turn_id"),
		caseInsensitiveGet(turnMetadata, "turn_id"),
		caseInsensitiveGetAny(headers, "x-client-request-id"),
		mapGet(responseBody, "id"),
	)
	conversationID := firstPresent(
		caseInsensitiveGet(clientMetadata, "thread_id"),
		caseInsensitiveGet(turnMetadata, "thread_id"),
		caseInsensitiveGetAny(headers, "thread-id"),
		caseInsensitiveGet(clientMetadata, "session_id"),
	)
	sessionID := firstPresent(
		caseInsensitiveGet(clientMetadata, "session_id"),
		caseInsensitiveGet(turnMetadata, "session_id"),
		caseInsensitiveGetAny(headers, "session-id", "session_id"),
	)

	// ---- Think type ----
	var thinkType any
	if reasoning, ok := requestBodyView["reasoning"]; ok {
		if rm, ok := reasoning.(map[string]any); ok {
			thinkType = rm["effort"]
		} else {
			thinkType = reasoning
		}
	}

	// ---- Model name ----
	modelName := firstPresent(
		mapGet(requestBodyView, "model"),
		source.Model,
		mapGet(responseBody, "model"),
	)

	// ---- Timestamp ----
	requestTimestamp := firstPresent(
		caseInsensitiveGet(requestInfo, "timestamp"),
	)
	timestamp := timestampToUTC(requestTimestamp)
	if timestamp == nil || timestamp == "" {
		timestamp = source.Timestamp.UTC().Format(time.RFC3339Nano)
	}

	// ---- User ID ----
	userID := source.KeyName

	// ---- Extra info (JSON string) ----
	extraInfo := make(map[string]any)
	skipKeys := map[string]bool{"turn_id": true, "thread_id": true, "session_id": true}
	for k, v := range turnMetadata {
		if !skipKeys[strings.ToLower(k)] {
			extraInfo[k] = v
		}
	}
	cmSkipKeys := map[string]bool{
		"turn_id": true, "thread_id": true, "session_id": true,
		"x-codex-turn-metadata": true,
	}
	for k, v := range clientMetadata {
		if !cmSkipKeys[strings.ToLower(k)] {
			if _, exists := extraInfo[k]; !exists {
				extraInfo[k] = v
			}
		}
	}
	extraInfoJSON := jsonStringField(extraInfo)

	// ---- Tools and inputs (from request body, JSON strings) ----
	tools, inputs := extractToolsAndInputs(requestBodyView)
	toolsJSON := jsonStringField(tools)
	inputsJSON := jsonStringField(inputs)

	// ---- Response outputs and tool_result (from response body, JSON strings) ----
	responseOutputs, toolResult := popOutputsAndTools(responseBody)
	if len(responseOutputs) == 0 {
		return nil, hash, nil
	}
	responseJSON := jsonStringField(responseOutputs)
	toolResultJSON := jsonStringField(toolResult)

	// ---- Metadata (JSON string) ----
	sourceMeta := map[string]any{
		"source_file":       source.Relative,
		"source_size_bytes": source.Size,
		"timestamp":         source.Timestamp.Format(time.RFC3339Nano),
		"provider":          source.Provider,
	}
	// Build request envelope for metadata.
	requestEnvelope := map[string]any{
		"info":    requestInfo,
		"headers": headers,
		"body":    requestBodyView,
	}
	metadataObj := map[string]any{
		"source":   sourceMeta,
		"request":  requestEnvelope,
		"response": responseEnvelope,
	}
	metadataJSON := jsonStringField(metadataObj)

	return &codexNormalizedRecord{
		MessageID:      fmt.Sprint(messageID),
		ConversationID: fmt.Sprint(conversationID),
		SessionID:      fmt.Sprint(sessionID),
		ThinkType:      thinkType,
		ExtraInfo:      extraInfoJSON,
		Tools:          toolsJSON,
		Inputs:         inputsJSON,
		Response:       responseJSON,
		Timestamp:      fmt.Sprint(timestamp),
		ModelName:      fmt.Sprint(modelName),
		UserID:         userID,
		ToolResult:     toolResultJSON,
		Metadata:       metadataJSON,
	}, hash, nil
}

// parseResponseSection parses a raw RESPONSE section into an envelope map
// with info, status, headers, body, and optionally stream.
func parseResponseSection(content string) map[string]any {
	if strings.TrimSpace(content) == "" {
		return map[string]any{}
	}
	lines := strings.Split(content, "\n")
	startIdx := payloadStartIndex(lines)
	prelude := lines[:startIdx]
	payload := strings.Join(lines[startIdx:], "\n")

	allPairs, _ := parsePairs(prelude)
	info := make(map[string]any)
	headersMap := make(map[string]any)
	statusKey := ""
	for k, v := range allPairs {
		lk := strings.ToLower(k)
		if lk == "status" || lk == "timestamp" {
			info[k] = v
			if lk == "status" {
				statusKey = k
			}
		} else {
			headersMap[k] = v
		}
	}

	var status any
	if statusKey != "" {
		status = parseStatusValue(info[statusKey])
		delete(info, statusKey)
	}

	body, sseStats := parseResponsePayload(payload)
	envelope := map[string]any{
		"info":    info,
		"status":  status,
		"headers": headersMap,
		"body":    body,
	}
	if sseStats != nil {
		streamInfo := map[string]any{
			"is_sse":                     true,
			"event_count":                sseStats.EventCount,
			"done_marker_count":          sseStats.DoneMarkerCount,
			"json_decode_error_count":    sseStats.JSONDecodeErrors,
			"event_type_counts":          sseStats.EventTypeCounts,
			"terminal_event_type":        sseStats.TerminalType,
			"reconstructed_output_count": sseStats.Reconstructed,
			"final_output_count":         sseStats.FinalOutputCount,
			"used_reconstructed_output":  sseStats.UsedReconstructed,
		}
		// Preserve raw SSE payload in stream metadata.
		streamInfo["raw_sse"] = sseStats.RawSSE
		envelope["stream"] = streamInfo
	}
	return envelope
}

// payloadStartIndex returns the index of the first line that looks like
// SSE or JSON payload content.
func payloadStartIndex(lines []string) int {
	for i, line := range lines {
		trimmed := strings.TrimLeft(line, " \t")
		if strings.HasPrefix(trimmed, "event:") ||
			strings.HasPrefix(trimmed, "data:") ||
			strings.HasPrefix(trimmed, "{") ||
			strings.HasPrefix(trimmed, "[") {
			return i
		}
	}
	return len(lines)
}

// parseResponsePayload parses the body portion of a response section.
// Returns the parsed body and SSE stats (nil for non-SSE payloads).
func parseResponsePayload(payload string) (any, *sseStats) {
	trimmed := strings.TrimSpace(payload)
	if trimmed == "" {
		return nil, nil
	}
	if sseMarkerRe.MatchString(trimmed) {
		body, stats := parseSSEPayload(trimmed)
		return body, &stats
	}
	return decodeJSONObject(trimmed), nil
}

// ---- Helper functions ----

// parsePairs parses "Key: Value" lines with multi-line continuation and
// duplicate key support.  Port of Python parse_pairs().
func parsePairs(lines []string) (map[string]any, []string) {
	result := make(map[string]any)
	var extras []string
	var lastName string
	for _, original := range lines {
		line := strings.TrimRight(original, "\r\n")
		if strings.TrimSpace(line) == "" {
			continue
		}
		if idx := strings.Index(line, ":"); idx >= 0 {
			name := strings.TrimSpace(line[:idx])
			value := strings.TrimSpace(line[idx+1:])
			if name != "" {
				addPair(result, name, value)
				lastName = name
				continue
			}
		}
		if lastName != "" {
			switch cur := result[lastName].(type) {
			case []string:
				cur[len(cur)-1] += "\n" + line
				result[lastName] = cur
			case string:
				result[lastName] = cur + "\n" + line
			}
		} else {
			extras = append(extras, line)
		}
	}
	return result, extras
}

// addPair adds a key-value pair, converting to a string slice on duplicates.
func addPair(target map[string]any, name, value string) {
	existing, ok := target[name]
	if !ok {
		target[name] = value
		return
	}
	switch v := existing.(type) {
	case []string:
		target[name] = append(v, value)
	case string:
		target[name] = []string{v, value}
	default:
		target[name] = value
	}
}

// pairsToStringMap converts a map[string]any (from parsePairs) to
// map[string]string, taking the last value for multi-valued keys.
func pairsToStringMap(pairs map[string]any) map[string]string {
	out := make(map[string]string, len(pairs))
	for k, v := range pairs {
		switch val := v.(type) {
		case string:
			out[k] = val
		case []string:
			if len(val) > 0 {
				out[k] = val[len(val)-1]
			}
		default:
			out[k] = fmt.Sprint(v)
		}
	}
	return out
}

// caseInsensitiveGet does a case-insensitive lookup in a map[string]any.
func caseInsensitiveGet(m map[string]any, names ...string) any {
	wanted := make(map[string]bool, len(names))
	for _, n := range names {
		wanted[strings.ToLower(n)] = true
	}
	for k, v := range m {
		if wanted[strings.ToLower(k)] {
			return v
		}
	}
	return nil
}

// caseInsensitiveGetAny does a case-insensitive lookup in map[string]any
// where the values are strings (from parsePairs output).
func caseInsensitiveGetAny(m map[string]any, names ...string) any {
	return caseInsensitiveGet(m, names...)
}

// mapGet retrieves a value from a map by exact key.
func mapGet(m map[string]any, key string) any {
	if m == nil {
		return nil
	}
	return m[key]
}

// firstPresent returns the first non-nil, non-empty value.
func firstPresent(values ...any) any {
	for _, v := range values {
		if v == nil {
			continue
		}
		if s, ok := v.(string); ok && s == "" {
			continue
		}
		return v
	}
	return nil
}

// parseJSONObject parses a JSON string or map into a map[string]any.
func parseJSONObject(value any) map[string]any {
	switch v := value.(type) {
	case map[string]any:
		return v
	case string:
		if strings.TrimSpace(v) == "" {
			return make(map[string]any)
		}
		var obj map[string]any
		if err := json.Unmarshal([]byte(v), &obj); err != nil {
			return make(map[string]any)
		}
		return obj
	default:
		return make(map[string]any)
	}
}

// decodeJSONObject parses a JSON string, falling back to extracting the
// first JSON object or array from the text.
func decodeJSONObject(raw string) map[string]any {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(raw), &obj); err == nil {
		return obj
	}
	// Fallback: find first { and parse from there.
	if i := strings.Index(raw, "{"); i >= 0 {
		if err := json.Unmarshal([]byte(raw[i:]), &obj); err == nil {
			return obj
		}
	}
	return nil
}

// payloadObject returns the "body" sub-map of a response envelope, or
// the envelope itself if no "body" key exists.
func payloadObject(envelope map[string]any) map[string]any {
	if envelope == nil {
		return nil
	}
	if body, ok := envelope["body"].(map[string]any); ok {
		return body
	}
	return envelope
}

// extractToolsAndInputs separates tools and inputs from the request body.
// Port of Python extract_tools_and_inputs().
func extractToolsAndInputs(requestBody map[string]any) ([]any, any) {
	rawInputs := requestBody["inputs"]
	if rawInputs == nil {
		rawInputs = requestBody["input"]
	}

	var additionalTools []any
	var filteredInputs any
	if inputList, ok := rawInputs.([]any); ok {
		var filtered []any
		for _, item := range inputList {
			if m, ok := item.(map[string]any); ok && m["type"] == "additional_tools" {
				if tools, ok := m["tools"].([]any); ok {
					additionalTools = append(additionalTools, tools...)
				}
				continue
			}
			filtered = append(filtered, item)
		}
		filteredInputs = filtered
	} else {
		if rawInputs != nil {
			filteredInputs = rawInputs
		} else {
			filteredInputs = []any{}
		}
	}

	directTools, _ := requestBody["tools"].([]any)
	tools := additionalTools
	if len(tools) == 0 && len(directTools) > 0 {
		tools = directTools
	}
	if tools == nil {
		tools = []any{}
	}
	return tools, filteredInputs
}

// popOutputsAndTools extracts output items and tool results from the
// response body.  Port of Python pop_outputs_and_tools().
func popOutputsAndTools(responseBody map[string]any) ([]any, []any) {
	outputs, _ := responseBody["outputs"].([]any)
	if outputs == nil {
		outputs, _ = responseBody["output"].([]any)
	}
	tools, _ := responseBody["tools"].([]any)
	if outputs == nil {
		outputs = []any{}
	}
	if tools == nil {
		tools = []any{}
	}
	return outputs, tools
}

// timestampToUTC converts an ISO timestamp to UTC, preserving fractional
// second precision.  Port of Python timestamp_to_utc().
func timestampToUTC(value any) any {
	s, ok := value.(string)
	if !ok || s == "" {
		return value
	}
	match := isoTimestampRe.FindStringSubmatch(s)
	if match == nil {
		return value
	}
	base := match[1]
	fraction := match[2]
	zone := match[3]
	if zone == "" {
		zone = "Z"
	}

	// Try parsing with Go's time.Parse.
	isoValue := base
	if zone == "Z" {
		isoValue += "+00:00"
	} else {
		isoValue += zone
	}
	parsed, err := time.Parse("2006-01-02T15:04:05-07:00", isoValue)
	if err != nil {
		return value
	}
	utc := parsed.UTC()
	return utc.Format("2006-01-02T15:04:05") + fraction + "Z"
}

// parseStatusValue extracts an HTTP status code integer from a value.
func parseStatusValue(value any) any {
	s, ok := value.(string)
	if !ok {
		return value
	}
	re := regexp.MustCompile(`\b(\d{3})\b`)
	match := re.FindStringSubmatch(s)
	if match != nil {
		var code int
		if _, err := fmt.Sscanf(match[1], "%d", &code); err == nil {
			return code
		}
	}
	return value
}

// jsonStringField serializes a value to a JSON string for double-encoding.
func jsonStringField(value any) string {
	if value == nil {
		return "null"
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return "null"
	}
	return string(raw)
}

// writeCodexNormalizedRecord normalizes a Codex source log and writes the
// result as a single JSONL line.  Returns (0, sha256, nil) when the record
// is filtered (empty response outputs).
func writeCodexNormalizedRecord(dst io.Writer, source sourceLog) (int64, string, error) {
	record, hash, errNorm := normalizeCodexRecord(source)
	if errNorm != nil {
		return 0, hash, fmt.Errorf("normalize codex record %s: %w", source.Relative, errNorm)
	}
	if record == nil {
		return 0, hash, nil
	}

	counter := &countingWriter{writer: dst}
	encoder := json.NewEncoder(counter)
	encoder.SetEscapeHTML(false)
	if errEncode := encoder.Encode(record); errEncode != nil {
		return counter.count, hash, fmt.Errorf("encode normalized codex record: %w", errEncode)
	}
	return counter.count, hash, nil
}

// sortedKeys returns the keys of a map[string]any in sorted order.
func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
