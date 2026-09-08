package diagnosticcapture

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	"github.com/tidwall/gjson"
)

const capturePathEnv = "CLIPROXY_TOOL_CONTINUATION_CAPTURE"

type ToolChoice struct {
	Kind string `json:"kind,omitempty"`
	Name string `json:"name,omitempty"`
}

type FunctionCall struct {
	CallID string `json:"call_id"`
	Name   string `json:"name,omitempty"`
}

type FunctionCallOutput struct {
	CallID      string `json:"call_id"`
	MatchedCall bool   `json:"matched_call"`
}

type Record struct {
	RequestOrdinal      int                  `json:"request_ordinal"`
	Stage               string               `json:"stage"`
	ToolsCount          int                  `json:"tools_count,omitempty"`
	ToolNames           []string             `json:"tool_names,omitempty"`
	ToolChoice          ToolChoice           `json:"tool_choice,omitempty"`
	FunctionCalls       []FunctionCall       `json:"function_calls,omitempty"`
	FunctionCallOutputs []FunctionCallOutput `json:"function_call_outputs,omitempty"`
	PreviousResponseID  string               `json:"previous_response_id,omitempty"`
	ResponseID          string               `json:"response_id,omitempty"`
	ResponseItemTypes   []string             `json:"response_item_types,omitempty"`
	HTTPStatus          int                  `json:"http_status,omitempty"`
	ErrorClass          string               `json:"error_class,omitempty"`
}

var state = struct {
	sync.Mutex
	ordinals       map[string]int
	nextOrdinal    int
	callIDs        map[string]string
	responseIDs    map[string]string
	nextCallID     int
	nextResponseID int
}{
	ordinals:    map[string]int{},
	callIDs:     map[string]string{},
	responseIDs: map[string]string{},
}

var writeMu sync.Mutex

func Enabled() bool {
	return strings.TrimSpace(os.Getenv(capturePathEnv)) != ""
}

func RequestOrdinal(ctx context.Context) int {
	if !Enabled() {
		return 0
	}
	key := strings.TrimSpace(logging.GetRequestID(ctx))
	if key == "" {
		key = fmt.Sprintf("anonymous-%p", ctx)
	}
	state.Lock()
	defer state.Unlock()
	if ordinal := state.ordinals[key]; ordinal > 0 {
		return ordinal
	}
	state.nextOrdinal++
	state.ordinals[key] = state.nextOrdinal
	return state.nextOrdinal
}

func CaptureInbound(ctx context.Context, raw []byte) {
	captureRequestStage(ctx, "inbound_client", raw)
}

func CaptureForwarded(ctx context.Context, raw []byte) {
	captureRequestStage(ctx, "forwarded_upstream", raw)
}

func CaptureUpstreamResponse(ctx context.Context, status int, raw []byte) {
	if !Enabled() {
		return
	}
	_ = WriteRecord(summarizeUpstreamResponse(RequestOrdinal(ctx), status, raw))
}

func captureRequestStage(ctx context.Context, stage string, raw []byte) {
	if !Enabled() {
		return
	}
	_ = WriteRecord(summarizeRequest(RequestOrdinal(ctx), stage, raw))
}

func summarizeRequest(ordinal int, stage string, raw []byte) Record {
	record := Record{RequestOrdinal: ordinal, Stage: stage}
	root := gjson.ParseBytes(raw)

	if tools := root.Get("tools"); tools.IsArray() {
		seen := map[string]bool{}
		for _, tool := range tools.Array() {
			name := tool.Get("name").String()
			if name == "" {
				name = tool.Get("function.name").String()
			}
			if name != "" && !seen[name] {
				seen[name] = true
				record.ToolNames = append(record.ToolNames, name)
			}
		}
		record.ToolsCount = len(tools.Array())
	}
	record.ToolChoice = summarizeToolChoice(root.Get("tool_choice"))
	if previous := strings.TrimSpace(root.Get("previous_response_id").String()); previous != "" {
		record.PreviousResponseID = responsePlaceholder(previous)
	}
	calls, outputs := summarizeInputCorrelation(root.Get("input"))
	record.FunctionCalls = calls
	record.FunctionCallOutputs = outputs
	return record
}

func summarizeToolChoice(choice gjson.Result) ToolChoice {
	if !choice.Exists() {
		return ToolChoice{}
	}
	if choice.Type == gjson.String {
		return ToolChoice{Kind: choice.String()}
	}
	kind := choice.Get("type").String()
	name := choice.Get("name").String()
	if name == "" {
		name = choice.Get("function.name").String()
	}
	return ToolChoice{Kind: kind, Name: name}
}

func summarizeInputCorrelation(input gjson.Result) ([]FunctionCall, []FunctionCallOutput) {
	if !input.IsArray() {
		return nil, nil
	}
	presentCalls := map[string]bool{}
	var calls []FunctionCall
	for _, item := range input.Array() {
		itemType := item.Get("type").String()
		if itemType != "function_call" && itemType != "custom_tool_call" {
			continue
		}
		rawID := strings.TrimSpace(item.Get("call_id").String())
		if rawID == "" {
			continue
		}
		presentCalls[rawID] = true
		calls = append(calls, FunctionCall{CallID: callPlaceholder(rawID), Name: item.Get("name").String()})
	}
	var outputs []FunctionCallOutput
	for _, item := range input.Array() {
		itemType := item.Get("type").String()
		if itemType != "function_call_output" && itemType != "custom_tool_call_output" {
			continue
		}
		rawID := strings.TrimSpace(item.Get("call_id").String())
		if rawID == "" {
			continue
		}
		outputs = append(outputs, FunctionCallOutput{CallID: callPlaceholder(rawID), MatchedCall: presentCalls[rawID]})
	}
	return calls, outputs
}

func summarizeUpstreamResponse(ordinal, status int, raw []byte) Record {
	record := Record{
		RequestOrdinal: ordinal,
		Stage:          "upstream_response",
		HTTPStatus:     status,
		ErrorClass:     normalizeErrorClass(status, raw),
	}

	itemsByKey := map[string]gjson.Result{}
	responseIDs := map[string]bool{}
	visitResponseJSON(raw, func(value gjson.Result) {
		if responseID := strings.TrimSpace(value.Get("response.id").String()); responseID != "" {
			responseIDs[responseID] = true
		}
		if responseID := strings.TrimSpace(value.Get("id").String()); value.Get("object").String() == "response" && responseID != "" {
			responseIDs[responseID] = true
		}
		collectResponseItems(itemsByKey, value.Get("item"))
		collectResponseItems(itemsByKey, value.Get("response.output"))
		collectResponseItems(itemsByKey, value.Get("output"))
	})

	if len(responseIDs) > 0 {
		ids := make([]string, 0, len(responseIDs))
		for id := range responseIDs {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		record.ResponseID = responsePlaceholder(ids[0])
	}

	keys := make([]string, 0, len(itemsByKey))
	for key := range itemsByKey {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	seenTypes := map[string]bool{}
	for _, key := range keys {
		item := itemsByKey[key]
		itemType := item.Get("type").String()
		if itemType != "" && !seenTypes[itemType] {
			seenTypes[itemType] = true
			record.ResponseItemTypes = append(record.ResponseItemTypes, itemType)
		}
		if itemType == "function_call" || itemType == "custom_tool_call" {
			rawID := strings.TrimSpace(item.Get("call_id").String())
			if rawID != "" {
				record.FunctionCalls = append(record.FunctionCalls, FunctionCall{CallID: callPlaceholder(rawID), Name: item.Get("name").String()})
			}
		}
	}
	return record
}

func visitResponseJSON(raw []byte, visit func(gjson.Result)) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return
	}
	if gjson.ValidBytes(trimmed) {
		visit(gjson.ParseBytes(trimmed))
		return
	}
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(nil, 52_428_800)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if bytes.HasPrefix(line, []byte("data:")) {
			line = bytes.TrimSpace(line[len("data:"):])
		}
		if len(line) == 0 || bytes.Equal(line, []byte("[DONE]")) || !gjson.ValidBytes(line) {
			continue
		}
		visit(gjson.ParseBytes(line))
	}
}

func collectResponseItems(items map[string]gjson.Result, result gjson.Result) {
	if !result.Exists() {
		return
	}
	if result.IsArray() {
		for _, item := range result.Array() {
			collectResponseItems(items, item)
		}
		return
	}
	if !result.IsObject() {
		return
	}
	itemType := result.Get("type").String()
	if itemType == "" {
		return
	}
	key := itemType + "|" + result.Get("call_id").String() + "|" + result.Get("name").String()
	items[key] = result
}

func normalizeErrorClass(status int, raw []byte) string {
	lower := strings.ToLower(string(raw))
	switch {
	case strings.Contains(lower, "no tool call found for function call output") || strings.Contains(lower, "function_call_output") && strings.Contains(lower, "call_id"):
		return "tool_correlation"
	case strings.Contains(lower, "previous_response_not_found") || strings.Contains(lower, "previous_response_id") && strings.Contains(lower, "not found"):
		return "previous_response_not_found"
	case status == 401 || status == 403:
		return "auth"
	case status == 404:
		return "not_found"
	case status == 408:
		return "timeout"
	case status == 409:
		return "conflict"
	case status == 429:
		return "rate_limit"
	case status >= 500:
		return "upstream_5xx"
	case status >= 400:
		return "invalid_request"
	default:
		return "none"
	}
}

func WriteRecord(record Record) error {
	path := strings.TrimSpace(os.Getenv(capturePathEnv))
	if path == "" {
		return nil
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	writeMu.Lock()
	defer writeMu.Unlock()
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	if err = file.Chmod(0o600); err != nil {
		return err
	}
	_, err = file.Write(append(encoded, '\n'))
	return err
}

func callPlaceholder(raw string) string {
	return placeholder(raw, true)
}

func responsePlaceholder(raw string) string {
	return placeholder(raw, false)
}

func placeholder(raw string, call bool) string {
	state.Lock()
	defer state.Unlock()
	mapping := state.responseIDs
	prefix := "RESP_"
	counter := &state.nextResponseID
	if call {
		mapping = state.callIDs
		prefix = "CALL_"
		counter = &state.nextCallID
	}
	if existing := mapping[raw]; existing != "" {
		return existing
	}
	*counter++
	value := prefix + alphaLabel(*counter)
	mapping[raw] = value
	return value
}

func alphaLabel(n int) string {
	if n <= 0 {
		return "A"
	}
	var out []byte
	for n > 0 {
		n--
		out = append([]byte{byte('A' + n%26)}, out...)
		n /= 26
	}
	return string(out)
}

func resetForTest() {
	state.Lock()
	defer state.Unlock()
	state.ordinals = map[string]int{}
	state.nextOrdinal = 0
	state.callIDs = map[string]string{}
	state.responseIDs = map[string]string{}
	state.nextCallID = 0
	state.nextResponseID = 0
}
