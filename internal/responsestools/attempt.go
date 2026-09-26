package responsestools

import (
	"bytes"
	"encoding/json"
	"fmt"
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
	if contract == nil {
		return Prepared{Body: body}, nil
	}
	switch policy.ClientSearch {
	case ClientSearchDisabled:
		if contract.ClientSearch {
			return Prepared{}, unprocessableError(ReasonUnsupportedProtocol, fmt.Errorf("client search is disabled for this route"))
		}
	case ClientSearchBridge:
		// Handled below.
	case ClientSearchNative, ClientSearchInherit:
		if policy.CustomTools == CustomToolsInherit || policy.CustomTools == CustomToolsNative {
			return Prepared{Body: body}, nil
		}
	}
	if policy.CustomTools == CustomToolsReject && hasCustomDeclarations(body) {
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

	needsSearch := contract.ClientSearch && policy.ClientSearch == ClientSearchBridge
	needsCustom := policy.CustomTools == CustomToolsFunction || policy.CustomTools == CustomToolsStrip
	needsSchema := policy.Schema.CompleteSearchRequired || policy.Schema.LocalRefs == LocalRefsInline
	if !needsSearch && !needsCustom && !needsSchema {
		return Prepared{Body: body}, nil
	}

	lease, err := limiter.Acquire(ContractSize(contract))
	if err != nil {
		return Prepared{}, err
	}
	attempt := &Attempt{Body: body, contract: CompactContract(contract), lease: lease, limits: limits, policy: policy}
	closeOnError := true
	defer func() {
		if closeOnError {
			attempt.Close()
		}
	}()

	changed := false
	if needsSchema && policy.Schema.CompleteSearchRequired {
		if tools, okTools := root["tools"].([]any); okTools {
			if CompleteToolSearchSchemas(tools) {
				changed = true
			}
		}
		if input, okInput := root["input"].([]any); okInput {
			for _, rawItem := range input {
				if item, okItem := rawItem.(map[string]any); okItem && stringField(item, "type") == "additional_tools" {
					if tools, okTools := item["tools"].([]any); okTools {
						if CompleteToolSearchSchemas(tools) {
							changed = true
						}
					}
				}
			}
		}
	}
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
	}
	var bridge *CustomBridge
	if policy.CustomTools == CustomToolsFunction {
		var customChanged bool
		var err error
		bridge, customChanged, err = RewriteCustomDeclarations(root, policy.CustomGrammar)
		if err != nil {
			return Prepared{}, err
		}
		if customChanged {
			changed = true
		}
		attempt.bridge = bridge
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
	if needsSearch {
		searchChanged, err := RewriteRequest(root, policy, contract, limits)
		if err != nil {
			return Prepared{}, err
		}
		if searchChanged {
			changed = true
		}
	}
	if !changed {
		closeOnError = false
		attempt.Close()
		return Prepared{Body: body}, nil
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
	if !bytes.Contains(trimmed, []byte("function_call")) && !bytes.Contains(trimmed, []byte("custom_tool_call")) {
		return body, nil
	}
	value, ok := decodeValue(body)
	if !ok {
		return body, nil
	}
	if !RewriteResponseBody(value, a.contract, a.bridge) {
		return body, nil
	}
	out, err := json.Marshal(value)
	if err != nil {
		return body, upstreamError(ReasonUpstreamContract, err)
	}
	return out, nil
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
