package responses

import (
	"context"
	"strings"
	"sync/atomic"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/translator/common"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Codex declares client-executed tool search as {"type":"tool_search",
// "execution":"client"} and keeps deferred tools out of tools[]. The model
// calls it through a tool_search_call item, Codex runs the search locally and
// replays a tool_search_output item whose tools[] become callable from then on.
// Claude has no counterpart, so the declaration becomes an ordinary function
// tool, the call and output become a tool_use/tool_result pair, and every tool
// discovered in history is declared on later requests.
//
// With native tool search, discovered tools are declared with defer_loading
// and referenced through tool_reference blocks. Anthropic keeps the prompt
// cache when deferred tools are added, but not when a request goes from no
// deferred tool to one, so a constant deferred anchor is declared from the
// first request on and discoveries only append after it.
const (
	// responsesToolSearchType is both the declaration type and the tool name,
	// as Codex names the tool after its type.
	responsesToolSearchType = "tool_search"
	// responsesToolSearchIDPrefix mirrors Codex's item id prefix for tool_search_call.
	responsesToolSearchIDPrefix = "tsc_"
	// responsesDeferredAnchorName names the constant deferred tool declared with
	// native tool search; nothing ever references it.
	responsesDeferredAnchorName = "tool_search_deferred_anchor"
)

// clientToolSearchEnabled mirrors upstream.claude.tool-search. While it is off
// the translator keeps the behavior that predates tool_search support:
// tool_search declarations are generic tools and tool_search_call and
// tool_search_output items are unmapped.
var clientToolSearchEnabled atomic.Bool

// SetClientToolSearchEnabled applies upstream.claude.tool-search.
func SetClientToolSearchEnabled(enabled bool) {
	clientToolSearchEnabled.Store(enabled)
}

// ClientToolSearchEnabled reports whether upstream.claude.tool-search is on.
func ClientToolSearchEnabled() bool {
	return clientToolSearchEnabled.Load()
}

type clientToolSearchContextKey struct{}

// WithClientToolSearch pins upstream.claude.tool-search for one request, so
// its fallback decision, request translation and response translation agree
// even if the configuration is reloaded while it runs.
func WithClientToolSearch(ctx context.Context, enabled bool) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, clientToolSearchContextKey{}, enabled)
}

// ClientToolSearchEnabledFor returns the value pinned in ctx, or the current
// upstream.claude.tool-search when ctx carries none.
func ClientToolSearchEnabledFor(ctx context.Context) bool {
	if ctx != nil {
		if enabled, ok := ctx.Value(clientToolSearchContextKey{}).(bool); ok {
			return enabled
		}
	}
	return ClientToolSearchEnabled()
}

const claudeToolSearchDefaultSchema = `{"type":"object","properties":{"query":{"type":"string","description":"Search query for deferred tools."},"limit":{"type":"number","description":"Maximum number of tools to return."}},"required":["query"],"additionalProperties":false}`

const responsesDeferredAnchorJSON = `{"name":"` + responsesDeferredAnchorName + `","description":"Placeholder that is never loaded; tool search returns the real tools.","input_schema":{"type":"object","properties":{}},"defer_loading":true}`

// responsesDeferredAnchorTool returns the deferred anchor declaration, unless a
// converted tool already carries its Claude name (names are normalized during
// conversion, so the check runs on the final tools[]). On a clash the request
// keeps the client's tool and simply has no anchor.
func responsesDeferredAnchorTool(toolItems [][]byte) ([]byte, bool) {
	for _, tool := range toolItems {
		if gjson.GetBytes(tool, "name").String() == responsesDeferredAnchorName {
			return nil, false
		}
	}
	return []byte(responsesDeferredAnchorJSON), true
}

// isResponsesClientToolSearch reports whether a tool declaration asks the
// client to execute tool search. Hosted (server) search cannot run on Claude.
func isResponsesClientToolSearch(tool gjson.Result) bool {
	return strings.TrimSpace(tool.Get("type").String()) == responsesToolSearchType &&
		strings.TrimSpace(tool.Get("execution").String()) == "client"
}

// convertResponsesToolSearchToolToClaude keeps the client's declared schema
// but guarantees the required query field Codex parses.
func convertResponsesToolSearchToolToClaude(tool gjson.Result, claudeName string) ([]byte, bool) {
	schema := []byte(claudeToolSearchDefaultSchema)
	if parameters := tool.Get("parameters"); parameters.IsObject() {
		schema = util.NormalizeClaudeToolInputSchema([]byte(parameters.Raw))
	}
	if gjson.GetBytes(schema, "properties.query.type").String() != "string" {
		schema, _ = sjson.SetRawBytes(schema, "properties.query", []byte(`{"type":"string","description":"Search query for deferred tools."}`))
	}
	if !gjson.GetBytes(schema, `required.#(=="query")`).Exists() {
		schema, _ = sjson.SetBytes(schema, "required.-1", "query")
	}

	tJSON := []byte(`{"name":"","description":"","input_schema":{}}`)
	tJSON, _ = sjson.SetBytes(tJSON, "name", claudeName)
	tJSON, _ = sjson.SetBytes(tJSON, "description", responsesToolDescription(tool))
	tJSON, _ = sjson.SetRawBytes(tJSON, "input_schema", schema)
	return tJSON, true
}

// convertResponsesToolSearchCallToClaude replays a client tool_search_call as
// an assistant tool_use and returns it with its raw call id. Hosted calls have
// no call id and return nil.
func convertResponsesToolSearchCallToClaude(item gjson.Result, claudeName string) ([]byte, string) {
	callID := strings.TrimSpace(item.Get("call_id").String())
	if callID == "" || strings.TrimSpace(item.Get("execution").String()) != "client" {
		return nil, ""
	}
	toolUse := []byte(`{"type":"tool_use","id":"","name":"","input":{}}`)
	toolUse, _ = sjson.SetBytes(toolUse, "id", util.SanitizeClaudeToolID(callID))
	toolUse, _ = sjson.SetBytes(toolUse, "name", claudeName)
	arguments := item.Get("arguments")
	if arguments.Type == gjson.String {
		arguments = gjson.Parse(arguments.String())
	}
	if arguments.IsObject() {
		toolUse, _ = sjson.SetRawBytes(toolUse, "input", []byte(arguments.Raw))
	}
	return toolUse, callID
}

// responsesToolSearchOutputText summarizes discovered tools for the model.
// Their full definitions are declared in tools[], so names suffice here.
func responsesToolSearchOutputText(item gjson.Result, names claudeToolNames) string {
	var discovered []string
	for _, descriptor := range appendResponsesToolDescriptors(nil, item.Get("tools"), responsesDiscoveredToolPriority, true) {
		discovered = append(discovered, names.claudeName(descriptor.name))
	}
	if len(discovered) == 0 {
		return "No matching tools were found."
	}
	return "Tools now available to call: " + strings.Join(discovered, ", ") + "."
}

// responsesToolSearchOutputContent returns the raw tool_result content for a
// paired tool_search_output. With native tool search it lists a tool_reference
// for each tool that only loads through search, so Claude expands the deferred
// definition at this point in the conversation. Tools that are already declared
// directly need no reference; when none qualify, the text summary is used.
func responsesToolSearchOutputContent(item gjson.Result, names claudeToolNames, winners map[string]responsesToolDescriptor, nativeReferences bool) []byte {
	if nativeReferences {
		var references [][]byte
		seen := make(map[string]struct{})
		for _, descriptor := range appendResponsesToolDescriptors(nil, item.Get("tools"), responsesDiscoveredToolPriority, true) {
			winner, ok := winners[descriptor.name]
			if !ok || winner.sourcePriority != responsesDiscoveredToolPriority || (winner.toolType != "function" && winner.toolType != "custom") {
				continue
			}
			claudeName := names.claudeName(descriptor.name)
			if _, duplicate := seen[claudeName]; duplicate {
				continue
			}
			seen[claudeName] = struct{}{}
			reference, _ := sjson.SetBytes([]byte(`{"type":"tool_reference","tool_name":""}`), "tool_name", claudeName)
			references = append(references, reference)
		}
		if len(references) > 0 {
			return common.JoinRawArray(references)
		}
	}
	text, _ := sjson.SetBytes([]byte(`{"text":""}`), "text", responsesToolSearchOutputText(item, names))
	return []byte(gjson.GetBytes(text, "text").Raw)
}

// toolSearchCallStatus downgrades a completed call whose arguments are not a
// JSON object, so a truncated search is never presented as a finished call.
func toolSearchCallStatus(status, arguments string) string {
	if status == "completed" && !isJSONObject(arguments) {
		return "incomplete"
	}
	return status
}

func isJSONObject(raw string) bool {
	return gjson.Valid(raw) && gjson.Parse(raw).IsObject()
}

// buildResponsesToolSearchCallItem renders the tool_search_call item Codex
// parses: call_id, execution "client" and an arguments object.
func buildResponsesToolSearchCallItem(callID, status, arguments string) []byte {
	item := []byte(`{"id":"","type":"tool_search_call","status":"","call_id":"","execution":"client","arguments":{}}`)
	item, _ = sjson.SetBytes(item, "id", responsesToolSearchIDPrefix+callID)
	item, _ = sjson.SetBytes(item, "status", status)
	item, _ = sjson.SetBytes(item, "call_id", callID)
	if isJSONObject(arguments) {
		item, _ = sjson.SetRawBytes(item, "arguments", []byte(arguments))
	}
	return item
}

// toolSearchCallEvent wraps a tool_search_call item in an output_item event.
// Responses has no argument delta events for it; Codex dispatches the search
// from output_item.done.
func toolSearchCallEvent(event string, sequence, outputIndex int, item []byte) []byte {
	payload := []byte(`{"type":"","sequence_number":0,"output_index":0,"item":{}}`)
	payload, _ = sjson.SetBytes(payload, "type", event)
	payload, _ = sjson.SetBytes(payload, "sequence_number", sequence)
	payload, _ = sjson.SetBytes(payload, "output_index", outputIndex)
	payload, _ = sjson.SetRawBytes(payload, "item", item)
	return emitEvent(event, payload)
}
