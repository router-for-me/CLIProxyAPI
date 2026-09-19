package responses

import (
	"strings"

	translatorcommon "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/common"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// normalizeOpenAIResponsesToolSearchItems rewrites replayed client-executed
// tool_search history into the function call shape that the rest of the
// Gemini request conversion already understands. The tool_search declaration
// itself is exposed as a function by util.BuildGeminiFunctionDeclarations, so
// the call history has to use the same function name for Gemini to accept the
// functionCall/functionResponse pairing. Items of any other type pass through
// untouched.
func normalizeOpenAIResponsesToolSearchItems(items []gjson.Result, forwardMap map[string]string) []gjson.Result {
	normalized := make([]gjson.Result, 0, len(items))
	for _, item := range items {
		switch item.Get("type").String() {
		case "tool_search_call":
			normalized = append(normalized, openAIResponsesToolSearchCallAsFunctionCall(item, forwardMap))
		case util.ResponsesToolSearchOutputType:
			normalized = append(normalized, openAIResponsesToolSearchOutputAsFunctionCallOutput(item, forwardMap))
		default:
			normalized = append(normalized, item)
		}
	}
	return normalized
}

func openAIResponsesToolSearchCallAsFunctionCall(item gjson.Result, forwardMap map[string]string) gjson.Result {
	call := []byte(`{"type":"function_call","name":"","call_id":"","arguments":"{}"}`)
	call, _ = sjson.SetBytes(call, "name", util.MapResponsesToolName(forwardMap, util.ResponsesToolSearchFunctionName))
	call, _ = sjson.SetBytes(call, "call_id", extractOpenAIResponsesCallID(item))
	if id := item.Get("id"); id.Exists() {
		call, _ = sjson.SetBytes(call, "id", id.String())
	}
	if status := item.Get("status"); status.Exists() {
		call, _ = sjson.SetBytes(call, "status", status.String())
	}
	// Native tool_search_call items carry object-valued arguments; function
	// calls carry them as a JSON string.
	if arguments := item.Get("arguments"); arguments.Exists() {
		if arguments.Type == gjson.String {
			call, _ = translatorcommon.SetStringWithoutHTMLEscape(call, "arguments", arguments.String())
		} else {
			call, _ = translatorcommon.SetStringWithoutHTMLEscape(call, "arguments", arguments.Raw)
		}
	}
	return gjson.ParseBytes(call)
}

// openAIResponsesToolSearchOutputAsFunctionCallOutput turns a tool_search_output
// item into a function_call_output whose text tells the model which functions
// the discovery made callable. The discovered declarations themselves are
// collected into the function declarations by util.BuildGeminiFunctionDeclarations.
func openAIResponsesToolSearchOutputAsFunctionCallOutput(item gjson.Result, forwardMap map[string]string) gjson.Result {
	output := []byte(`{"type":"function_call_output","call_id":"","output":""}`)
	output, _ = sjson.SetBytes(output, "call_id", extractOpenAIResponsesCallID(item))
	if id := item.Get("id"); id.Exists() {
		output, _ = sjson.SetBytes(output, "id", id.String())
	}

	summary := []byte(`{"status":"completed","tools":[]}`)
	if status := strings.TrimSpace(item.Get("status").String()); status != "" {
		summary, _ = sjson.SetBytes(summary, "status", status)
	}
	if errorResult := item.Get("error"); errorResult.Exists() && errorResult.Type != gjson.Null {
		summary, _ = sjson.SetRawBytes(summary, "error", []byte(errorResult.Raw))
	}
	names := openAIResponsesDiscoveredToolNames(item.Get("tools"), forwardMap)
	summary, _ = sjson.SetBytes(summary, "tools", names)
	output, _ = translatorcommon.SetStringWithoutHTMLEscape(output, "output", string(summary))
	return gjson.ParseBytes(output)
}

// openAIResponsesDiscoveredToolNames lists the Gemini-visible function names
// declared by a tool_search_output, so the model learns the exact names it
// may call next.
func openAIResponsesDiscoveredToolNames(tools gjson.Result, forwardMap map[string]string) []string {
	names := make([]string, 0)
	if !tools.Exists() || !tools.IsArray() {
		return names
	}
	seen := make(map[string]struct{})
	appendName := func(name string) {
		name = strings.TrimSpace(name)
		if name == "" {
			return
		}
		mapped := util.MapResponsesToolName(forwardMap, name)
		if _, exists := seen[mapped]; exists {
			return
		}
		seen[mapped] = struct{}{}
		names = append(names, mapped)
	}
	tools.ForEach(func(_, tool gjson.Result) bool {
		switch strings.TrimSpace(tool.Get("type").String()) {
		case "", "function", "custom":
			appendName(tool.Get("name").String())
		case "namespace":
			namespaceName := strings.TrimSpace(tool.Get("name").String())
			children := tool.Get("tools")
			if !children.Exists() || !children.IsArray() {
				children = tool.Get("children")
			}
			children.ForEach(func(_, child gjson.Result) bool {
				appendName(util.QualifyResponsesNamespaceToolName(namespaceName, child.Get("name").String()))
				return true
			})
		}
		return true
	})
	return names
}

// buildOpenAIResponsesToolSearchCallItem renders a client-executed
// tool_search_call output item. Unlike function_call, its arguments are a JSON
// object and it carries no name.
func buildOpenAIResponsesToolSearchCallItem(id, callID, status, argsJSON string) []byte {
	item := []byte(`{"id":"","type":"tool_search_call","status":"","execution":"client","call_id":"","arguments":{}}`)
	item, _ = sjson.SetBytes(item, "id", id)
	item, _ = sjson.SetBytes(item, "status", status)
	item, _ = sjson.SetBytes(item, "call_id", callID)
	if args := gjson.Parse(argsJSON); args.IsObject() {
		item, _ = sjson.SetRawBytes(item, "arguments", []byte(args.Raw))
	}
	return item
}

func openAIResponsesToolSearchCallItemID(callID string) string {
	return "ts_" + callID
}
