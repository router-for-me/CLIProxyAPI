package helps

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/responsestools"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// WireContract mirrors the read-only tool contract attached by the auth
// Manager for the final send guard. A nil contract means the attempt is
// inactive and the guard is a no-op with no extra parsing.
type WireContract = cliproxyexecutor.WireContract

// WireContractFromContext reads the guard contract back through the shared
// executor key.
func WireContractFromContext(ctx context.Context) *WireContract {
	return cliproxyexecutor.WireContractFromContext(ctx)
}

// ValidateOutboundToolContract verifies the final wire payload after all
// translators, plugin normalizers, thinking rewrites, payload overrides, and
// tool fixups, immediately before the real send. It rejects payloads where a
// post rule deleted or renamed a bridged alias, reintroduced an unsupported
// built-in, dropped a required eager declaration, or broke call identity.
// Normal model and thinking adjustments pass untouched; only tool fields are
// checked. Inactive attempts skip JSON parsing entirely.
func ValidateOutboundToolContract(ctx context.Context, payload []byte, maxActiveToolBytes int) error {
	wire := WireContractFromContext(ctx)
	if wire == nil {
		return nil
	}
	trimmed := bytes.TrimSpace(payload)
	if len(trimmed) == 0 {
		if wire.SearchBridgeActive || len(wire.SearchAliases) > 0 || len(wire.CustomAliases) > 0 ||
			len(wire.HistoryAliases) > 0 || len(wire.HistoryCalls) > 0 {
			return wireError(http.StatusUnprocessableEntity, "post rule removed all bridged tool declarations")
		}
		return nil
	}
	var root map[string]any
	if err := json.Unmarshal(trimmed, &root); err != nil {
		return wireError(http.StatusUnprocessableEntity, "post rule produced an invalid tool request payload")
	}
	if maxActiveToolBytes <= 0 {
		maxActiveToolBytes = wire.MaxActiveToolBytes
	}
	if maxActiveToolBytes > 0 {
		if size := outboundToolArraysBytesFromRoot(root); size > maxActiveToolBytes {
			return wireError(http.StatusRequestEntityTooLarge, "outbound tool declarations exceed budget after post rules: %d > %d", size, maxActiveToolBytes)
		}
	}
	if !wire.SearchBridgeActive && len(wire.SearchAliases) == 0 && len(wire.CustomAliases) == 0 &&
		len(wire.HistoryAliases) == 0 && len(wire.HistoryCalls) == 0 &&
		len(wire.RequiredFunctionNames) == 0 {
		return nil
	}
	declared := collectOutboundToolNames(root)
	if (wire.SearchBridgeActive || len(wire.SearchAliases) > 0) && containsOutboundToolSearchDeclaration(root) {
		return wireError(http.StatusUnprocessableEntity, "post rule reintroduced the bridged tool_search built-in")
	}
	for _, alias := range append(append([]string(nil), wire.SearchAliases...), wire.CustomAliases...) {
		if alias == "" {
			continue
		}
		if _, ok := declared[alias]; !ok {
			return wireError(http.StatusUnprocessableEntity, "post rule removed bridged tool %q", alias)
		}
	}
	for _, name := range wire.RequiredFunctionNames {
		if name == "" {
			continue
		}
		if _, ok := declared[name]; !ok {
			return wireError(http.StatusUnprocessableEntity, "post rule removed required function tool %q", name)
		}
	}
	if len(wire.HistoryCalls) > 0 {
		expected := make(map[string]map[string]struct{}, len(wire.HistoryCalls))
		bridgedAliases := make(map[string]struct{}, len(wire.SearchAliases)+len(wire.CustomAliases)+len(wire.HistoryAliases))
		for _, alias := range wire.SearchAliases {
			bridgedAliases[alias] = struct{}{}
		}
		for _, alias := range wire.CustomAliases {
			bridgedAliases[alias] = struct{}{}
		}
		for _, alias := range wire.HistoryAliases {
			bridgedAliases[alias] = struct{}{}
		}
		expectedCallIDs := make(map[string]struct{}, len(wire.HistoryCalls))
		for _, call := range wire.HistoryCalls {
			if call.CallID == "" || call.Name == "" {
				return wireError(http.StatusUnprocessableEntity, "bridged history reference is incomplete")
			}
			if _, exists := expected[call.CallID]; exists {
				return wireError(http.StatusUnprocessableEntity, "bridged history call_id is duplicated")
			}
			allowedNames := map[string]struct{}{call.Name: {}}
			for _, alternate := range call.AlternateNames {
				if alternate = strings.TrimSpace(alternate); alternate != "" {
					allowedNames[alternate] = struct{}{}
					bridgedAliases[alternate] = struct{}{}
				}
			}
			expected[call.CallID] = allowedNames
			expectedCallIDs[call.CallID] = struct{}{}
		}
		history := collectOutboundToolHistory(root, bridgedAliases, expectedCallIDs)
		if history.invalid {
			return wireError(http.StatusUnprocessableEntity, "post rule duplicated or corrupted outbound tool history")
		}
		for _, call := range wire.HistoryCalls {
			name, exists := history.calls[call.CallID]
			_, allowedName := expected[call.CallID][name]
			if !exists || !allowedName || history.callCounts[call.CallID] != 1 {
				return wireError(http.StatusUnprocessableEntity, "post rule changed bridged history identity")
			}
			if history.outputCounts[call.CallID] != 1 {
				return wireError(http.StatusUnprocessableEntity, "post rule removed bridged history output")
			}
		}
		for callID, name := range history.calls {
			if _, bridged := bridgedAliases[name]; !bridged {
				continue
			}
			if _, allowed := expected[callID][name]; !allowed {
				return wireError(http.StatusUnprocessableEntity, "post rule introduced or changed bridged history")
			}
		}
	}
	return nil
}

// ValidateOutboundToolContractRequest validates the final request body without
// consuming the body that the HTTP client will send.
func ValidateOutboundToolContractRequest(ctx context.Context, request *http.Request, maxActiveToolBytes int) error {
	if WireContractFromContext(ctx) == nil || request == nil {
		return nil
	}
	if request.GetBody == nil {
		return wireError(http.StatusUnprocessableEntity, "cannot inspect the final outbound request body")
	}
	body, err := request.GetBody()
	if err != nil {
		return wireError(http.StatusUnprocessableEntity, "cannot inspect the final outbound request body: %v", err)
	}
	defer func() {
		if errClose := body.Close(); errClose != nil {
			// Validation must not hide its primary result behind a close failure.
		}
	}()
	payload, err := io.ReadAll(body)
	if err != nil {
		return wireError(http.StatusUnprocessableEntity, "cannot read the final outbound request body: %v", err)
	}
	return ValidateOutboundToolContract(ctx, payload, maxActiveToolBytes)
}

func collectOutboundToolNames(root map[string]any) map[string]struct{} {
	out := make(map[string]struct{})
	for _, toolRoot := range outboundToolRoots(root) {
		collectOutboundToolArray(toolRoot["tools"], "", out)
		if input, ok := toolRoot["input"].([]any); ok {
			for _, rawItem := range input {
				item, okItem := rawItem.(map[string]any)
				if okItem && responsestools.IsToolDeclarationInput(item) {
					collectOutboundToolArray(item["tools"], "", out)
				}
			}
		}
	}
	return out
}

func collectOutboundToolArray(value any, inheritedNamespace string, names map[string]struct{}) {
	tools, ok := value.([]any)
	if !ok {
		return
	}
	for _, rawTool := range tools {
		tool, ok := rawTool.(map[string]any)
		if !ok {
			continue
		}
		switch strings.TrimSpace(stringField(tool, "type")) {
		case "namespace":
			namespace := strings.TrimSpace(stringField(tool, "name"))
			if inheritedNamespace != "" && namespace != "" && !strings.HasPrefix(namespace, inheritedNamespace) {
				namespace = joinOutboundNamespace(inheritedNamespace, namespace)
			} else if inheritedNamespace != "" {
				namespace = inheritedNamespace
			}
			collectOutboundToolArray(tool["tools"], namespace, names)
			continue
		}
		namespace := strings.TrimSpace(stringField(tool, "namespace"))
		if namespace == "" {
			namespace = inheritedNamespace
		}
		if name := outboundFunctionName(tool); name != "" {
			names[strings.TrimSpace(name)] = struct{}{}
			names[qualifyOutboundToolName(namespace, name)] = struct{}{}
		}
		collectFunctionDeclarationNames(tool["function_declarations"], namespace, names)
		collectFunctionDeclarationNames(tool["functionDeclarations"], namespace, names)
	}
}

func containsOutboundToolSearchDeclaration(root map[string]any) bool {
	var hasType func(any) bool
	hasType = func(value any) bool {
		tools, ok := value.([]any)
		if !ok {
			return false
		}
		for _, rawTool := range tools {
			tool, ok := rawTool.(map[string]any)
			if !ok {
				continue
			}
			switch strings.TrimSpace(stringField(tool, "type")) {
			case "tool_search":
				return true
			case "namespace":
				if hasType(tool["tools"]) {
					return true
				}
			}
		}
		return false
	}
	for _, toolRoot := range outboundToolRoots(root) {
		if hasType(toolRoot["tools"]) {
			return true
		}
		if input, ok := toolRoot["input"].([]any); ok {
			for _, rawItem := range input {
				item, okItem := rawItem.(map[string]any)
				if okItem && responsestools.IsToolDeclarationInput(item) && hasType(item["tools"]) {
					return true
				}
			}
		}
	}
	return false
}

func outboundToolRoots(root map[string]any) []map[string]any {
	roots := []map[string]any{root}
	if request, ok := root["request"].(map[string]any); ok && request != nil {
		roots = append(roots, request)
	}
	return roots
}

func collectFunctionDeclarationNames(value any, inheritedNamespace string, names map[string]struct{}) {
	declarations, ok := value.([]any)
	if !ok {
		return
	}
	for _, rawDeclaration := range declarations {
		declaration, ok := rawDeclaration.(map[string]any)
		if !ok {
			continue
		}
		if name, okName := declaration["name"].(string); okName && name != "" {
			namespace := strings.TrimSpace(stringField(declaration, "namespace"))
			if namespace == "" {
				namespace = inheritedNamespace
			}
			names[strings.TrimSpace(name)] = struct{}{}
			names[qualifyOutboundToolName(namespace, name)] = struct{}{}
		}
	}
}

func qualifyOutboundToolName(namespace, name string) string {
	namespace = strings.TrimSpace(namespace)
	name = strings.TrimSpace(name)
	if namespace == "" || name == "" || strings.HasPrefix(name, "mcp__") {
		return name
	}
	// A shared prefix alone ("fs_read" under namespace "fs") is not
	// qualification; only the exact namespace or its separator-delimited form
	// counts. Matching responsestools keeps the guard's idea of the wire name
	// identical to the one the request path required.
	if name == namespace {
		return name
	}
	if strings.HasSuffix(namespace, "__") {
		if strings.HasPrefix(name, namespace) {
			return name
		}
		return namespace + name
	}
	if strings.HasPrefix(name, namespace+"__") {
		return name
	}
	return joinOutboundNamespace(namespace, name)
}

func joinOutboundNamespace(parent, child string) string {
	if strings.HasSuffix(parent, "__") {
		return parent + child
	}
	return parent + "__" + child
}

type outboundToolHistory struct {
	calls        map[string]string
	callCounts   map[string]int
	outputCounts map[string]int
	invalid      bool
}

func collectOutboundToolHistory(root map[string]any, bridgedAliases, expectedCallIDs map[string]struct{}) outboundToolHistory {
	history := outboundToolHistory{
		calls:        make(map[string]string),
		callCounts:   make(map[string]int),
		outputCounts: make(map[string]int),
	}
	recordCall := func(callID, name string) {
		callID = strings.TrimSpace(callID)
		name = strings.TrimSpace(name)
		_, bridgedName := bridgedAliases[name]
		_, expectedID := expectedCallIDs[callID]
		if callID == "" || name == "" {
			if bridgedName || expectedID {
				history.invalid = true
			}
			return
		}
		if previous, exists := history.calls[callID]; exists && previous != name {
			history.invalid = true
		} else if history.callCounts[callID] > 0 && (bridgedName || expectedID) {
			history.invalid = true
		}
		if _, exists := history.calls[callID]; !exists {
			history.calls[callID] = name
		}
		history.callCounts[callID]++
	}
	recordOutput := func(callID string) {
		callID = strings.TrimSpace(callID)
		if callID != "" {
			if history.outputCounts[callID] > 0 {
				history.invalid = true
			}
			history.outputCounts[callID]++
		}
	}
	collectResponsesHistory := func(value any) {
		items, ok := value.([]any)
		if !ok {
			return
		}
		for _, rawItem := range items {
			item, ok := rawItem.(map[string]any)
			if !ok {
				continue
			}
			switch strings.TrimSpace(stringField(item, "type")) {
			case "function_call", "custom_tool_call":
				recordCall(stringField(item, "call_id"), stringField(item, "name"))
			case "function_call_output", "custom_tool_call_output":
				recordOutput(firstStringField(item, "call_id", "tool_call_id", "callId"))
			}
		}
	}
	collectChatAndClaudeHistory := func(value any) {
		messages, ok := value.([]any)
		if !ok {
			return
		}
		for _, rawMessage := range messages {
			message, ok := rawMessage.(map[string]any)
			if !ok {
				continue
			}
			if strings.EqualFold(strings.TrimSpace(stringField(message, "role")), "tool") {
				recordOutput(firstStringField(message, "tool_call_id", "call_id", "callId"))
			}
			if legacyCall, ok := message["function_call"].(map[string]any); ok {
				recordCall(firstStringField(message, "call_id", "id"), stringField(legacyCall, "name"))
			}
			if toolCalls, ok := message["tool_calls"].([]any); ok {
				for _, rawCall := range toolCalls {
					call, ok := rawCall.(map[string]any)
					if !ok {
						continue
					}
					callType := strings.TrimSpace(stringField(call, "type"))
					if callType != "" && callType != "function" {
						continue
					}
					function, _ := call["function"].(map[string]any)
					recordCall(firstStringField(call, "id", "call_id", "callId"), stringField(function, "name"))
				}
			}
			content, ok := message["content"].([]any)
			if !ok {
				continue
			}
			for _, rawBlock := range content {
				block, ok := rawBlock.(map[string]any)
				if !ok {
					continue
				}
				switch strings.TrimSpace(stringField(block, "type")) {
				case "tool_use":
					recordCall(firstStringField(block, "id", "call_id", "callId"), stringField(block, "name"))
				case "tool_result":
					recordOutput(firstStringField(block, "tool_use_id", "call_id", "tool_call_id", "callId"))
				}
			}
		}
	}
	collectGeminiHistory := func(value any) {
		contents, ok := value.([]any)
		if !ok {
			return
		}
		for _, rawContent := range contents {
			content, ok := rawContent.(map[string]any)
			if !ok {
				continue
			}
			parts, ok := content["parts"].([]any)
			if !ok {
				continue
			}
			for _, rawPart := range parts {
				part, ok := rawPart.(map[string]any)
				if !ok {
					continue
				}
				if call, ok := part["functionCall"].(map[string]any); ok {
					recordCall(firstStringField(call, "id", "call_id", "callId"), stringField(call, "name"))
				}
				if response, ok := part["functionResponse"].(map[string]any); ok {
					recordOutput(firstStringField(response, "id", "call_id", "tool_call_id", "callId"))
				}
			}
		}
	}
	for _, toolRoot := range outboundToolRoots(root) {
		collectResponsesHistory(toolRoot["input"])
		collectChatAndClaudeHistory(toolRoot["messages"])
		collectGeminiHistory(toolRoot["contents"])
	}
	return history
}

func stringField(value map[string]any, field string) string {
	if value == nil {
		return ""
	}
	text, _ := value[field].(string)
	return text
}

func firstStringField(value map[string]any, fields ...string) string {
	for _, field := range fields {
		if candidate := strings.TrimSpace(stringField(value, field)); candidate != "" {
			return candidate
		}
	}
	return ""
}

func outboundToolName(tool map[string]any) string {
	if name, ok := tool["name"].(string); ok && name != "" {
		return name
	}
	function, _ := tool["function"].(map[string]any)
	name, _ := function["name"].(string)
	return name
}

func outboundFunctionName(tool map[string]any) string {
	toolType, _ := tool["type"].(string)
	if toolType != "" && toolType != "function" {
		return ""
	}
	if _, ok := tool["function"].(map[string]any); ok {
		return outboundToolName(tool)
	}
	if toolType == "function" || tool["parameters"] != nil ||
		tool["parametersJsonSchema"] != nil || tool["input_schema"] != nil {
		return outboundToolName(tool)
	}
	return ""
}

func outboundToolArraysBytes(payload []byte) int {
	var root map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(payload), &root); err != nil {
		return 0
	}
	return outboundToolArraysBytesFromRoot(root)
}

func outboundToolArraysBytesFromRoot(root map[string]any) int {
	total := 0
	for _, toolRoot := range outboundToolRoots(root) {
		if tools, exists := toolRoot["tools"]; exists {
			if encoded, err := json.Marshal(tools); err == nil {
				total += len(encoded)
			}
		}
		if input, ok := toolRoot["input"].([]any); ok {
			for _, rawItem := range input {
				if item, okItem := rawItem.(map[string]any); okItem && responsestools.IsToolDeclarationInput(item) {
					if tools, exists := item["tools"]; exists {
						if encoded, err := json.Marshal(tools); err == nil {
							total += len(encoded)
						}
					}
				}
			}
		}
	}
	return total
}

type wireContractError struct {
	status int
	msg    string
}

func (e *wireContractError) Error() string { return e.msg }

// StatusCode exposes the HTTP status for handler error mapping.
func (e *wireContractError) StatusCode() int { return e.status }

// IsRequestScoped marks a post-rule violation as a tool compatibility error.
func (e *wireContractError) IsRequestScoped() bool { return true }

// AvailabilityNeutral reports that the credential was not at fault.
func (e *wireContractError) AvailabilityNeutral() bool { return true }

func wireError(status int, format string, args ...any) error {
	return &wireContractError{status: status, msg: fmt.Sprintf(format, args...)}
}

// ResponsesToolsDuplexSteeringError reports that a bridged turn cannot use the
// native duplex steering path. It is request-scoped and availability-neutral.
func ResponsesToolsDuplexSteeringError() error {
	return wireError(http.StatusUnprocessableEntity, "responses tools rewrite requires replay; live duplex steering is not supported for this turn")
}

// WireContractByteLimit returns the declaration budget for the outbound guard.
// Without an active attempt it returns 0 and the guard skips size checks.
func WireContractByteLimit(ctx context.Context) int {
	wire := WireContractFromContext(ctx)
	if wire == nil {
		return 0
	}
	return wire.MaxActiveToolBytes
}
