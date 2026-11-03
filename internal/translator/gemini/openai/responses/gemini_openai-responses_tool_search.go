package responses

import (
	"strings"

	"github.com/MoeForge/moego/ojson"
	translatorcommon "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/common"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
)

const openAIResponsesToolSearchIDPrefix = "tsc_"

// normalizeOpenAIResponsesToolSearchItems rewrites replayed client-executed
// tool_search history into the function_call / function_call_output shape that
// the Gemini request conversion already pairs and serializes.
func normalizeOpenAIResponsesToolSearchItems(items []ojson.Result, forwardMap map[string]string) []ojson.Result {
	normalized := make([]ojson.Result, 0, len(items))
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

func openAIResponsesToolSearchCallAsFunctionCall(item ojson.Result, forwardMap map[string]string) ojson.Result {
	callDoc := ojson.NewDocString(`{"type":"function_call","name":"","call_id":"","arguments":"{}"}`)
	_ = callDoc.Set("name", util.MapResponsesClientToolSearchName(forwardMap))
	_ = callDoc.Set("call_id", extractOpenAIResponsesCallID(item))
	if id := item.Get("id"); id.Exists() {
		_ = callDoc.Set("id", id.String())
	}
	if status := item.Get("status"); status.Exists() {
		_ = callDoc.Set("status", status.String())
	}
	if arguments := item.Get("arguments"); arguments.Exists() {
		if arguments.Type == ojson.String {
			_ = translatorcommon.SetStringWithoutHTMLEscapeDoc(callDoc, "arguments", arguments.String())
		} else if arguments.IsObject() || arguments.IsArray() {
			_ = translatorcommon.SetStringWithoutHTMLEscapeDoc(callDoc, "arguments", arguments.Raw)
		}
	}
	return util.ParseGJSONBytesNoCopy(callDoc.Bytes())
}

func openAIResponsesToolSearchOutputAsFunctionCallOutput(item ojson.Result, forwardMap map[string]string) ojson.Result {
	outputDoc := ojson.NewDocString(`{"type":"function_call_output","name":"","call_id":"","output":""}`)
	_ = outputDoc.Set("name", util.MapResponsesClientToolSearchName(forwardMap))
	_ = outputDoc.Set("call_id", extractOpenAIResponsesCallID(item))
	if id := item.Get("id"); id.Exists() {
		_ = outputDoc.Set("id", id.String())
	}

	summaryDoc := ojson.NewDocString(`{"status":"completed","tools":[]}`)
	if status := strings.TrimSpace(item.Get("status").String()); status != "" {
		_ = summaryDoc.Set("status", status)
	}
	if errorResult := item.Get("error"); errorResult.Exists() && errorResult.Type != ojson.Null {
		_ = summaryDoc.SetRaw("error", errorResult.Raw)
	}
	names := openAIResponsesDiscoveredToolNames(item.Get("tools"), forwardMap)
	_ = summaryDoc.Set("tools", names)
	_ = translatorcommon.SetStringWithoutHTMLEscapeDoc(outputDoc, "output", summaryDoc.String())
	return util.ParseGJSONBytesNoCopy(outputDoc.Bytes())
}

func openAIResponsesDiscoveredToolNames(tools ojson.Result, forwardMap map[string]string) []string {
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
	tools.ForEach(func(_, tool ojson.Result) bool {
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
			children.ForEach(func(_, child ojson.Result) bool {
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
		if !ojson.Valid(trimmed) || !ojson.Parse(trimmed).IsObject() {
			return "incomplete"
		}
	}
	return status
}

func buildOpenAIResponsesToolSearchCallItem(id, callID, status, argsJSON string) []byte {
	status = openAIResponsesToolSearchCallStatus(status, argsJSON)
	itemDoc := ojson.NewDocString(`{"id":"","type":"tool_search_call","status":"","execution":"client","call_id":"","arguments":{}}`)
	_ = itemDoc.Set("id", id)
	_ = itemDoc.Set("status", status)
	_ = itemDoc.Set("call_id", callID)
	if trimmed := strings.TrimSpace(argsJSON); ojson.Valid(trimmed) {
		if args := ojson.Parse(trimmed); args.IsObject() {
			_ = itemDoc.SetRaw("arguments", args.Raw)
		}
	}
	return itemDoc.Bytes()
}

func openAIResponsesToolSearchCallItemID(callID string) string {
	return openAIResponsesToolSearchIDPrefix + callID
}
