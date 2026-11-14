package responses

import (
	"strconv"
	"strings"

	"github.com/MoeForge/moego/ojson"
	translatorcommon "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/common"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
)

// Chat Completions has no tool search, so the client-executed Responses
// tool_search declaration is bridged as an ordinary function. Calls to it are
// restored as tool_search_call items, the client runs the search, and the
// tools it returns in tool_search_output stay declared as functions on every
// later request (see walkResponsesToolDeclarations).

const (
	responsesToolSearchIDPrefix          = "tsc_"
	responsesToolSearchDefaultParameters = `{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}`
)

func convertResponsesToolSearchToolToOpenAIChat(tool ojson.Result, name string) ([]byte, bool) {
	chatToolDoc := ojson.NewDocString(`{"type":"function","function":{"name":"","description":"Search for deferred tools and load the matching tools.","parameters":{}}}`)
	_ = chatToolDoc.Set("function.name", name)
	if description := util.ResponsesToolDescription(tool); description != "" {
		_ = chatToolDoc.Set("function.description", description)
	}
	parameters := responsesToolSearchDefaultParameters
	if declared := util.ResponsesToolParameters(tool); declared.Exists() && declared.IsObject() {
		parameters = declared.Raw
	}
	_ = chatToolDoc.SetRaw("function.parameters", parameters)
	return chatToolDoc.Bytes(), true
}

func (idx *responsesToolIndex) isToolSearch(name string) bool {
	return idx.byChat[name].search
}

func (idx *responsesToolIndex) toolSearchName() string {
	for _, d := range idx.declarations {
		if d.search {
			return d.chatName
		}
	}
	return ""
}

// toolSearchHistory rewrites replayed client tool_search_call and
// tool_search_output items into the function_call pair that the Chat
// Completions conversion already groups and pairs. Hosted (server) search
// items are left as they are.
func (idx *responsesToolIndex) toolSearchHistory(items []ojson.Result) []ojson.Result {
	var out []ojson.Result
	name := ""
	resolveName := func() string {
		if name != "" {
			return name
		}
		name = idx.toolSearchName()
		if name != "" {
			return name
		}
		reserved := make(map[string]bool)
		for key := range idx.byChat {
			reserved[key] = true
		}
		for key := range idx.byRaw {
			reserved[key] = true
		}
		for key := range idx.byLocal {
			reserved[key] = true
		}
		for _, item := range items {
			if kind := item.Get("type").String(); kind == "function_call" || kind == "custom_tool_call" {
				reserved[idx.canonicalName(item.Get("name").String())] = true
			}
		}
		name = util.ResponsesToolSearchFunctionName
		for suffix := 1; reserved[name]; suffix++ {
			name = util.ResponsesToolSearchFunctionName + "_" + strconv.Itoa(suffix)
		}
		return name
	}
	for i, item := range items {
		itemType := item.Get("type").String()
		if (itemType != util.ResponsesToolSearchCallType && itemType != util.ResponsesToolSearchOutputType) ||
			!util.IsResponsesClientToolSearchItem(item) {
			if out != nil {
				out = append(out, item)
			}
			continue
		}
		if out == nil {
			out = append(make([]ojson.Result, 0, len(items)), items[:i]...)
		}
		if itemType == util.ResponsesToolSearchCallType {
			out = append(out, idx.toolSearchCallAsFunctionCall(item, resolveName()))
		} else {
			out = append(out, idx.toolSearchOutputAsFunctionCallOutput(item))
		}
	}
	if out == nil {
		return items
	}
	return out
}

func (idx *responsesToolIndex) toolSearchCallAsFunctionCall(item ojson.Result, searchName string) ojson.Result {
	callDoc := ojson.NewDocString(`{"type":"function_call","call_id":"","name":"","arguments":"{}"}`)
	_ = callDoc.Set("call_id", translatorcommon.ExtractResponsesCallID(item))
	_ = callDoc.Set("name", searchName)
	if arguments := item.Get("arguments"); arguments.Type == ojson.String && strings.TrimSpace(arguments.String()) != "" {
		_ = translatorcommon.SetStringWithoutHTMLEscapeDoc(callDoc, "arguments", arguments.String())
	} else if arguments.IsObject() {
		_ = translatorcommon.SetStringWithoutHTMLEscapeDoc(callDoc, "arguments", arguments.Raw)
	}
	return util.ParseGJSONBytesNoCopy(callDoc.Bytes())
}

func (idx *responsesToolIndex) toolSearchOutputAsFunctionCallOutput(item ojson.Result) ojson.Result {
	names := make([]string, 0)
	seen := make(map[string]struct{})
	appendName := func(name string) {
		if _, exists := seen[name]; name == "" || exists {
			return
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}
	item.Get("tools").ForEach(func(_, tool ojson.Result) bool {
		if strings.TrimSpace(tool.Get("type").String()) == "namespace" {
			namespaceName := strings.TrimSpace(tool.Get("name").String())
			tool.Get("tools").ForEach(func(_, child ojson.Result) bool {
				if childName := responsesToolName(child); childName != "" {
					appendName(idx.namespaceName(namespaceName, childName))
				}
				return true
			})
		} else if name := responsesToolName(tool); name != "" {
			appendName(idx.canonicalName(name))
		}
		return true
	})

	summaryDoc := ojson.NewDocString(`{"status":"completed","tools":[]}`)
	if status := strings.TrimSpace(item.Get("status").String()); status != "" {
		_ = summaryDoc.Set("status", status)
	}
	_ = summaryDoc.Set("tools", names)
	outputDoc := ojson.NewDocString(`{"type":"function_call_output","call_id":"","output":""}`)
	_ = outputDoc.Set("call_id", translatorcommon.ExtractResponsesCallID(item))
	_ = translatorcommon.SetStringWithoutHTMLEscapeDoc(outputDoc, "output", summaryDoc.String())
	return util.ParseGJSONBytesNoCopy(outputDoc.Bytes())
}

// toolSearchCallItem renders a bridged search function call as a
// client-executed Responses tool_search_call with object arguments.
func toolSearchCallItem(callID, args, status string) []byte {
	trimmed := strings.TrimSpace(args)
	parsedArgs := ojson.Parse(trimmed)
	validArgs := ojson.Valid(trimmed) && parsedArgs.IsObject()
	if status == "completed" && !validArgs {
		status = "incomplete"
	}
	itemDoc := ojson.NewDocString(`{"id":"","type":"tool_search_call","status":"","execution":"client","call_id":"","arguments":{}}`)
	_ = itemDoc.Set("id", responsesToolSearchIDPrefix+callID)
	_ = itemDoc.Set("status", status)
	_ = itemDoc.Set("call_id", callID)
	if validArgs {
		_ = itemDoc.SetRaw("arguments", parsedArgs.Raw)
	}
	return itemDoc.Bytes()
}

func toolSearchCallPlaceholder(callID string) []byte {
	itemDoc := ojson.NewDocString(`{"id":"","type":"tool_search_call","status":"in_progress","execution":"client","call_id":"","arguments":{}}`)
	_ = itemDoc.Set("id", responsesToolSearchIDPrefix+callID)
	_ = itemDoc.Set("call_id", callID)
	return itemDoc.Bytes()
}
