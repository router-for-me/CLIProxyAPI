package responses

import (
	"strings"

	translatorcommon "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/common"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const openAIResponsesToolSearchIDPrefix = "ts_"

// normalizeOpenAIResponsesToolSearchItems rewrites replayed client-executed
// tool_search history into the function_call / function_call_output shape that
// the Gemini request conversion already pairs and serializes.
func normalizeOpenAIResponsesToolSearchItems(items []gjson.Result, forwardMap map[string]string) []gjson.Result {
	normalized := make([]gjson.Result, 0, len(items))
	for _, item := range items {
		switch item.Get("type").String() {
		case util.ResponsesToolSearchCallType:
			if util.IsResponsesClientToolSearchItem(item) {
				normalized = append(normalized, openAIResponsesToolSearchCallAsFunctionCall(item, forwardMap))
			}
		case util.ResponsesToolSearchOutputType:
			if util.IsResponsesClientToolSearchItem(item) {
				normalized = append(normalized, openAIResponsesToolSearchOutputAsFunctionCallOutput(item, forwardMap))
			}
		default:
			normalized = append(normalized, item)
		}
	}
	return normalized
}

func openAIResponsesToolSearchCallAsFunctionCall(item gjson.Result, forwardMap map[string]string) gjson.Result {
	call := []byte(`{"type":"function_call","name":"","call_id":"","arguments":"{}"}`)
	call, _ = sjson.SetBytes(call, "name", util.MapResponsesClientToolSearchName(forwardMap))
	call, _ = sjson.SetBytes(call, "call_id", extractOpenAIResponsesCallID(item))
	if id := item.Get("id"); id.Exists() {
		call, _ = sjson.SetBytes(call, "id", id.String())
	}
	if status := item.Get("status"); status.Exists() {
		call, _ = sjson.SetBytes(call, "status", status.String())
	}
	if arguments := item.Get("arguments"); arguments.Exists() {
		if arguments.Type == gjson.String {
			call, _ = translatorcommon.SetStringWithoutHTMLEscape(call, "arguments", arguments.String())
		} else if arguments.IsObject() || arguments.IsArray() {
			call, _ = translatorcommon.SetStringWithoutHTMLEscape(call, "arguments", arguments.Raw)
		}
	}
	return gjson.ParseBytes(call)
}

func openAIResponsesToolSearchOutputAsFunctionCallOutput(item gjson.Result, forwardMap map[string]string) gjson.Result {
	output := []byte(`{"type":"function_call_output","name":"","call_id":"","output":""}`)
	output, _ = sjson.SetBytes(output, "name", util.MapResponsesClientToolSearchName(forwardMap))
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
			name := strings.TrimSpace(tool.Get("name").String())
			if name == "" {
				name = strings.TrimSpace(tool.Get("function.name").String())
			}
			appendName(name)
		case "namespace":
			namespaceName := strings.TrimSpace(tool.Get("name").String())
			children := tool.Get("tools")
			if !children.Exists() || !children.IsArray() {
				children = tool.Get("children")
			}
			children.ForEach(func(_, child gjson.Result) bool {
				childName := strings.TrimSpace(child.Get("name").String())
				if childName == "" {
					childName = strings.TrimSpace(child.Get("function.name").String())
				}
				if childName != "" {
					appendName(util.QualifyResponsesNamespaceToolName(namespaceName, childName))
				}
				return true
			})
		}
		return true
	})
	return names
}

func openAIResponsesToolSearchCallStatus(status, argsJSON string) string {
	if status == "completed" {
		trimmed := strings.TrimSpace(argsJSON)
		if !gjson.Valid(trimmed) || !gjson.Parse(trimmed).IsObject() {
			return "incomplete"
		}
	}
	return status
}

func buildOpenAIResponsesToolSearchCallItem(id, callID, status, argsJSON string) []byte {
	status = openAIResponsesToolSearchCallStatus(status, argsJSON)
	item := []byte(`{"id":"","type":"tool_search_call","status":"","execution":"client","call_id":"","arguments":{}}`)
	item, _ = sjson.SetBytes(item, "id", id)
	item, _ = sjson.SetBytes(item, "status", status)
	item, _ = sjson.SetBytes(item, "call_id", callID)
	if trimmed := strings.TrimSpace(argsJSON); gjson.Valid(trimmed) {
		if args := gjson.Parse(trimmed); args.IsObject() {
			item, _ = sjson.SetRawBytes(item, "arguments", []byte(args.Raw))
		}
	}
	return item
}

func openAIResponsesToolSearchCallItemID(callID string) string {
	return openAIResponsesToolSearchIDPrefix + callID
}
