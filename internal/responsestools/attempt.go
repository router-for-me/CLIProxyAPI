package responsestools

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Attempt owns one actual executor call: the normalized wire body, the
// compact identity view for response recovery, the custom bridge, the stream
// feed, and the budget lease. Attempts are never shared across requests; a
// retry rebuilds from the original client contract with a fresh Prepare.
type Attempt struct {
	Body     []byte
	contract *ToolContract
	bridge   *CustomBridge
	feed     *StreamFeed
	lease    *Lease
	limits   Limits
	policy   RoutePolicy
	adapted  bool
	closed   bool
}

// Prepared is the result of Prepare: the wire body plus the attempt when the
// route needs adaptation, or the original body with a nil attempt when the
// request passes through untouched.
type Prepared struct {
	Body    []byte
	Attempt *Attempt
}

// Prepare parses one raw client request body against the effective route
// policy and returns the normalized wire body. Pure computation: it starts no
// network or tool execution. Unchanged requests return the original slice
// with no state.
func Prepare(body []byte, policy RoutePolicy, limits Limits, limiter *Limiter) (Prepared, error) {
	contract := ParseContract(body)
	customDeclarations := hasCustomDeclarations(body)
	customHistory := hasCustomHistory(body)
	needsCustomHistory := (policy.CustomTools == CustomToolsFunction || policy.CustomTools == CustomToolsStrip) && customHistory
	if contract == nil && !customHistory {
		return Prepared{Body: body}, nil
	}
	if contract == nil {
		contract = NewToolContract()
	}
	switch policy.ClientSearch {
	case ClientSearchDisabled:
		if contract.ClientSearch {
			return Prepared{}, unprocessableError(ReasonUnsupportedProtocol, fmt.Errorf("client search is disabled for this route"))
		}
	case ClientSearchBridge:
		// Handled below.
	}
	if policy.CustomTools == CustomToolsReject && (customDeclarations || customHistory) {
		return Prepared{}, unprocessableError(ReasonUnsupportedProtocol, fmt.Errorf("custom tools are rejected for this route"))
	}

	value, ok := decodeValue(body)
	if !ok {
		return Prepared{}, syntaxError(fmt.Errorf("request body is not valid JSON"))
	}
	root, ok := value.(map[string]any)
	if !ok {
		return Prepared{Body: body}, nil
	}
	if needsCustomHistory {
		if err := validateCustomHistory(root); err != nil {
			return Prepared{}, err
		}
	}
	needsSearch := contract.ClientSearch && policy.ClientSearch == ClientSearchBridge
	contract.SearchBridged = needsSearch
	needsCustom := policy.CustomTools == CustomToolsFunction || policy.CustomTools == CustomToolsStrip
	needsSchema := policy.Schema.CompleteSearchRequired || policy.Schema.LocalRefs == LocalRefsInline
	needsCustomConversion := (policy.CustomTools == CustomToolsFunction || policy.CustomTools == CustomToolsStrip) &&
		(customDeclarations || customHistory)
	if contract.discoveryConflict && (needsSearch || needsCustomConversion) {
		return Prepared{}, unprocessableError(ReasonAmbiguousIdentity, fmt.Errorf("discovery round contains conflicting declarations for one tool identity"))
	}
	if hasOpaqueResponseHistory(root) && (needsSearch || needsCustomConversion ||
		(policy.Schema.CompleteSearchRequired && contract.ClientSearch)) {
		return Prepared{}, unprocessableError(ReasonOpaqueHistory, fmt.Errorf("tool adaptation cannot resolve opaque response or item history"))
	}
	if !needsSearch && !needsCustom && !needsSchema {
		return Prepared{Body: body}, nil
	}

	lease, err := limiter.Acquire(ContractSize(contract))
	if err != nil {
		return Prepared{}, err
	}
	var bridge *CustomBridge
	if policy.CustomTools == CustomToolsFunction {
		bridge = BuildCustomBridgeWithReserved(root, policy.CustomGrammar, contractWireAliases(contract))
		replaceCustomAliases(contract, bridge)
	}
	attempt := &Attempt{Body: body, contract: CompactContract(contract), lease: lease, limits: limits, policy: policy}
	attempt.bridge = bridge
	closeOnError := true
	defer func() {
		if closeOnError {
			attempt.Close()
		}
	}()

	changed := false
	if needsSchema && policy.Schema.LocalRefs == LocalRefsInline {
		if tools, okTools := root["tools"].([]any); okTools {
			if errDepth := ValidateToolArrayDepth(tools, 1, limits.MaxDepth); errDepth != nil {
				return Prepared{}, errDepth
			}
			if inlined, err := InlineLocalRefs(tools, BudgetFromLimits(limits)); err != nil {
				return Prepared{}, err
			} else if inlined {
				changed = true
			}
		}
		if input, okInput := root["input"].([]any); okInput {
			for _, rawItem := range input {
				item, okItem := rawItem.(map[string]any)
				if !okItem || stringField(item, "type") != "additional_tools" {
					continue
				}
				tools, okTools := item["tools"].([]any)
				if !okTools {
					continue
				}
				if errDepth := ValidateToolArrayDepth(tools, 1, limits.MaxDepth); errDepth != nil {
					return Prepared{}, errDepth
				}
				if inlined, err := InlineLocalRefs(tools, BudgetFromLimits(limits)); err != nil {
					return Prepared{}, err
				} else if inlined {
					changed = true
				}
			}
		}
	}
	if needsSchema && policy.Schema.CompleteSearchRequired {
		if tools, okTools := root["tools"].([]any); okTools {
			schemaChanged, synthetic, seen, schemaErr := CompleteToolSearchSchemasWithSyntheticNulls(tools)
			if schemaErr != nil {
				return Prepared{}, schemaErr
			}
			attempt.contract.mergeSearchSyntheticNulls(synthetic, seen)
			if schemaChanged {
				changed = true
			}
		}
		if input, okInput := root["input"].([]any); okInput {
			for _, rawItem := range input {
				if item, okItem := rawItem.(map[string]any); okItem && stringField(item, "type") == "additional_tools" {
					if tools, okTools := item["tools"].([]any); okTools {
						schemaChanged, synthetic, seen, schemaErr := CompleteToolSearchSchemasWithSyntheticNulls(tools)
						if schemaErr != nil {
							return Prepared{}, schemaErr
						}
						attempt.contract.mergeSearchSyntheticNulls(synthetic, seen)
						if schemaChanged {
							changed = true
						}
					}
				}
			}
		}
	}
	if needsSearch {
		searchChanged, err := RewriteRequest(root, policy, contract, limits)
		if err != nil {
			return Prepared{}, err
		}
		if searchChanged {
			changed = true
		}
	}
	if policy.CustomTools == CustomToolsFunction {
		var customChanged bool
		customChanged, err = rewriteCustomDeclarationsValue(root, bridge)
		if err != nil {
			return Prepared{}, err
		}
		if customChanged {
			changed = true
		}
	} else if policy.CustomTools == CustomToolsStrip {
		stripChanged, err := StripCustomDeclarations(root)
		if err != nil {
			return Prepared{}, err
		}
		if stripChanged {
			changed = true
		}
		if historyChanged, err := ConvertCustomHistory(root); err != nil {
			return Prepared{}, err
		} else if historyChanged {
			changed = true
		}
	}
	if !changed {
		closeOnError = false
		attempt.Close()
		return Prepared{Body: body}, nil
	}
	activeBytes, err := ActiveToolArraysBytes(root)
	if err != nil {
		return Prepared{}, upstreamError(ReasonUpstreamContract, err)
	}
	if activeBytes > limits.MaxActiveToolBytes {
		return Prepared{}, budgetError(ReasonDeclarationBudget, fmt.Errorf("active declarations do not fit: %d > %d", activeBytes, limits.MaxActiveToolBytes))
	}
	out, err := json.Marshal(root)
	if err != nil {
		return Prepared{}, upstreamError(ReasonUpstreamContract, err)
	}
	if err := lease.Grow(len(out)); err != nil {
		return Prepared{}, err
	}
	attempt.Body = out
	attempt.adapted = true
	attempt.feed = NewStreamFeed(attempt.contract, attempt.bridge, lease, limits)
	closeOnError = false
	return Prepared{Body: out, Attempt: attempt}, nil
}

func hasOpaqueResponseHistory(root map[string]any) bool {
	if stringField(root, "previous_response_id") != "" || stringField(root, "previous_item_id") != "" {
		return true
	}
	input, _ := root["input"].([]any)
	for _, rawItem := range input {
		item, ok := rawItem.(map[string]any)
		if ok && strings.EqualFold(strings.TrimSpace(stringField(item, "type")), "item_reference") {
			return true
		}
	}
	return false
}

// RewriteResponse restores client tool semantics in one non-streaming
// upstream response body.
func (a *Attempt) RewriteResponse(body []byte) ([]byte, error) {
	if a == nil || !a.adapted {
		return body, nil
	}
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return body, nil
	}
	if !bytes.Contains(trimmed, []byte("function_call")) &&
		!bytes.Contains(trimmed, []byte("custom_tool_call")) &&
		!bytes.Contains(trimmed, []byte("tool_search_call")) {
		return body, nil
	}
	value, ok := decodeValue(body)
	if !ok {
		return body, nil
	}
	changed, err := RewriteResponseBodyChecked(value, a.contract, a.bridge)
	if err != nil {
		return nil, err
	}
	if !changed {
		return body, nil
	}
	out, err := json.Marshal(value)
	if err != nil {
		return body, upstreamError(ReasonUpstreamContract, err)
	}
	return out, nil
}

func hasCustomHistory(body []byte) bool {
	if !bytes.Contains(body, []byte("custom_tool_call")) {
		return false
	}
	value, ok := decodeValue(body)
	if !ok {
		return false
	}
	var visit func(any) bool
	visit = func(current any) bool {
		switch typed := current.(type) {
		case map[string]any:
			itemType := stringField(typed, "type")
			if itemType == "custom_tool_call" || itemType == "custom_tool_call_output" {
				return true
			}
			for _, child := range typed {
				if visit(child) {
					return true
				}
			}
		case []any:
			for _, child := range typed {
				if visit(child) {
					return true
				}
			}
		}
		return false
	}
	return visit(value)
}

func validateCustomHistory(root map[string]any) error {
	input, ok := root["input"].([]any)
	if !ok {
		return nil
	}
	calls := make(map[string]struct{})
	outputs := make(map[string]struct{})
	for _, rawItem := range input {
		item, ok := rawItem.(map[string]any)
		if !ok {
			continue
		}
		callID := strings.TrimSpace(stringField(item, "call_id"))
		switch stringField(item, "type") {
		case "custom_tool_call":
			if callID == "" {
				return unprocessableError(ReasonInvalidCustomInput, fmt.Errorf("custom history call is missing call_id"))
			}
			if _, exists := calls[callID]; exists {
				return unprocessableError(ReasonInvalidCustomInput, fmt.Errorf("custom history repeats call_id"))
			}
			if _, ok := item["input"].(string); !ok {
				return unprocessableError(ReasonInvalidCustomInput, fmt.Errorf("custom history input is not a string"))
			}
			calls[callID] = struct{}{}
		case "custom_tool_call_output":
			if callID == "" {
				return unprocessableError(ReasonInvalidCustomInput, fmt.Errorf("custom history output is missing call_id"))
			}
			if _, exists := calls[callID]; !exists {
				return unprocessableError(ReasonInvalidCustomInput, fmt.Errorf("custom history output has no matching call"))
			}
			if _, exists := outputs[callID]; exists {
				return unprocessableError(ReasonInvalidCustomInput, fmt.Errorf("custom history repeats output for call_id"))
			}
			outputs[callID] = struct{}{}
		}
	}
	return nil
}

// Feed adapts one upstream stream frame. A nil attempt passes frames through.
func (a *Attempt) Feed(frame []byte) ([][]byte, error) {
	if a == nil || !a.adapted || a.feed == nil {
		return [][]byte{frame}, nil
	}
	return a.feed.Feed(frame)
}

// Finish validates the stream tail for a bridge attempt.
func (a *Attempt) Finish() ([][]byte, error) {
	if a == nil || !a.adapted || a.feed == nil {
		return nil, nil
	}
	return a.feed.Finish()
}

// Close releases the attempt lease. It is idempotent.
func (a *Attempt) Close() {
	if a == nil || a.closed {
		return
	}
	a.closed = true
	if a.lease != nil {
		a.lease.Close()
	}
}

// Adapted reports whether the attempt rewrote the wire body.
func (a *Attempt) Adapted() bool {
	return a != nil && a.adapted
}

// MaxActiveToolBytes returns the validated declaration ceiling that was
// applied during preparation.
func (a *Attempt) MaxActiveToolBytes() int {
	if a == nil {
		return 0
	}
	return a.limits.MaxActiveToolBytes
}

func hasCustomDeclarations(body []byte) bool {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return false
	}
	if !bytes.Contains(trimmed, []byte("\"custom\"")) {
		return false
	}
	value, ok := decodeValue(body)
	if !ok {
		return false
	}
	return scanCustomDeclarations(value)
}

func scanCustomDeclarations(value any) bool {
	switch typed := value.(type) {
	case map[string]any:
		if stringsEqualFold(stringField(typed, "type"), "custom") {
			return true
		}
		for _, child := range typed {
			if scanCustomDeclarations(child) {
				return true
			}
		}
	case []any:
		for _, child := range typed {
			if scanCustomDeclarations(child) {
				return true
			}
		}
	}
	return false
}

// WireAliases returns the bridged search and custom aliases of this attempt
// for the outbound guard. Nil attempts yield empty lists.
func (a *Attempt) WireAliases() (search []string, custom []string) {
	if a == nil {
		return nil, nil
	}
	declared := declaredFunctionNames(a.Body)
	if a.policy.ClientSearch == ClientSearchBridge && a.contract != nil &&
		a.contract.ClientSearch && a.contract.SearchAlias != "" {
		if _, exists := declared[a.contract.SearchAlias]; exists {
			search = []string{a.contract.SearchAlias}
		}
	}
	if a.bridge != nil {
		for _, alias := range a.bridge.Aliases() {
			if _, exists := declared[alias]; exists {
				custom = append(custom, alias)
			}
		}
	}
	return search, custom
}

// WireHistoryAliases returns aliases used by bridged history even when the
// corresponding tool is no longer declared in the current request.
func (a *Attempt) WireHistoryAliases() []string {
	if a == nil {
		return nil
	}
	aliases := make(map[string]struct{})
	if a.contract != nil && a.contract.SearchBridged && a.contract.SearchAlias != "" {
		aliases[a.contract.SearchAlias] = struct{}{}
	}
	if a.bridge != nil {
		for _, alias := range a.bridge.Aliases() {
			aliases[alias] = struct{}{}
		}
	}
	if len(aliases) == 0 {
		return nil
	}
	out := make([]string, 0, len(aliases))
	for alias := range aliases {
		out = append(out, alias)
	}
	sort.Strings(out)
	return out
}

// BridgesClientSearch reports whether this attempt rewrites client tool_search.
func (a *Attempt) BridgesClientSearch() bool {
	return a != nil && a.contract != nil && a.contract.SearchBridged
}

func contractWireAliases(contract *ToolContract) []string {
	if contract == nil {
		return nil
	}
	aliases := make([]string, 0, len(contract.AliasByID)+1)
	for _, alias := range contract.AliasByID {
		aliases = append(aliases, alias)
	}
	if contract.SearchAlias != "" {
		aliases = append(aliases, contract.SearchAlias)
	}
	return aliases
}

func replaceCustomAliases(contract *ToolContract, bridge *CustomBridge) {
	if contract == nil || bridge == nil {
		return
	}
	for identity, previous := range contract.AliasByID {
		if identity.Kind != ToolKindCustom {
			continue
		}
		alias, ok := bridge.Alias(identity)
		if !ok {
			continue
		}
		if mapped, exists := contract.IDByAlias[previous]; exists && mapped == identity {
			delete(contract.IDByAlias, previous)
		}
		contract.AliasByID[identity] = alias
		contract.IDByAlias[alias] = identity
	}
}

func declaredFunctionNames(body []byte) map[string]struct{} {
	names := make(map[string]struct{})
	value, ok := decodeValue(body)
	if !ok {
		return names
	}
	var collectTools func(any)
	collectTools = func(value any) {
		tools, okTools := value.([]any)
		if !okTools {
			return
		}
		for _, rawTool := range tools {
			tool, okTool := rawTool.(map[string]any)
			if !okTool {
				continue
			}
			if stringField(tool, "type") == NamespaceToolType {
				collectTools(tool["tools"])
				continue
			}
			if stringField(tool, "type") != "function" {
				continue
			}
			if name := strings.TrimSpace(stringField(tool, "name")); name != "" {
				names[name] = struct{}{}
			}
		}
	}
	if root, okRoot := value.(map[string]any); okRoot {
		collectTools(root["tools"])
		if input, okInput := root["input"].([]any); okInput {
			for _, rawItem := range input {
				item, okItem := rawItem.(map[string]any)
				if okItem && stringField(item, "type") == "additional_tools" {
					collectTools(item["tools"])
				}
			}
		}
	} else {
		collectTools(value)
	}
	return names
}
