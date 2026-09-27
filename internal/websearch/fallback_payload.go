package websearch

import (
	"fmt"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Proxy search function surface shared by every entry format.
const (
	proxySearchDescription = "Search the web for current information. Returns an answer with sources and citations."
	proxySearchQueryDesc   = "The search query."
	proxySearchName        = "web_search"
)

// HasServerSearchTool reports whether payload declares a server-side search
// tool for the entry format.
func HasServerSearchTool(format string, payload []byte) bool {
	switch normalizeFallbackFormat(format) {
	case FallbackFormatClaude:
		return hasClaudeServerSearchTool(payload)
	case FallbackFormatOpenAIResponse, FallbackFormatCodex:
		return hasResponsesServerSearchTool(payload)
	case FallbackFormatOpenAI:
		return hasChatServerSearchTool(payload)
	case FallbackFormatGemini:
		return hasGeminiServerSearchTool(payload)
	default:
		return false
	}
}

// searchOnlyTools reports whether every declared tool is a server search
// tool. Mixed tool sets need the proxy loop even on capable routes whose
// native path only serves dedicated search envelopes.
func searchOnlyTools(format string, payload []byte) bool {
	switch normalizeFallbackFormat(format) {
	case FallbackFormatClaude:
		return allClaudeToolsAreSearch(payload)
	case FallbackFormatOpenAIResponse, FallbackFormatCodex:
		return allResponsesToolsAreSearch(payload)
	case FallbackFormatOpenAI:
		return allChatToolsAreSearch(payload)
	case FallbackFormatGemini:
		return allGeminiToolsAreSearch(payload)
	default:
		return false
	}
}

// choiceAllowsSearch reports whether the request tool choice permits a
// native search. Choices forcing a specific non-search tool deny it.
func choiceAllowsSearch(format string, payload []byte) bool {
	switch normalizeFallbackFormat(format) {
	case FallbackFormatClaude:
		return claudeChoiceAllowsSearch(payload)
	case FallbackFormatOpenAIResponse, FallbackFormatCodex:
		return responsesChoiceAllowsSearch(payload)
	case FallbackFormatOpenAI:
		return chatChoiceAllowsSearch(payload)
	case FallbackFormatGemini:
		return geminiChoiceAllowsSearch(payload)
	default:
		return false
	}
}

// RewriteForFallback converts server search tools into proxy-executed
// function tools. It returns the rewritten payload and the chosen function
// names, aliased to avoid collisions with client-declared functions.
func RewriteForFallback(format string, payload []byte) ([]byte, []string) {
	switch normalizeFallbackFormat(format) {
	case FallbackFormatClaude:
		return rewriteClaudeSearchTools(payload)
	case FallbackFormatOpenAIResponse, FallbackFormatCodex:
		return rewriteResponsesSearchTools(payload)
	case FallbackFormatOpenAI:
		return rewriteChatSearchTools(payload)
	case FallbackFormatGemini:
		return rewriteGeminiSearchTools(payload)
	default:
		return payload, nil
	}
}

// ExtractProxyCalls parses proxy search calls from a non-streaming
// response body. Client function calls are ignored.
func ExtractProxyCalls(format string, body []byte, names map[string]bool) []ProxyCall {
	switch normalizeFallbackFormat(format) {
	case FallbackFormatClaude:
		return extractClaudeProxyCalls(body, names)
	case FallbackFormatOpenAIResponse, FallbackFormatCodex:
		return extractResponsesProxyCalls(body, names)
	case FallbackFormatOpenAI:
		return extractChatProxyCalls(body, names)
	case FallbackFormatGemini:
		return extractGeminiProxyCalls(body, names)
	default:
		return nil
	}
}

// AppendSearchResults appends the echoed assistant turn plus proxy search
// outputs to the request payload. outputs[i] answers calls[i].
func AppendSearchResults(format string, payload, body []byte, calls []ProxyCall, outputs []string) []byte {
	switch normalizeFallbackFormat(format) {
	case FallbackFormatClaude:
		return appendClaudeSearchResults(payload, body, calls, outputs)
	case FallbackFormatOpenAIResponse, FallbackFormatCodex:
		return appendResponsesSearchResults(payload, body, calls, outputs)
	case FallbackFormatOpenAI:
		return appendChatSearchResults(payload, body, calls, outputs)
	case FallbackFormatGemini:
		return appendGeminiSearchResults(payload, body, calls, outputs)
	default:
		return payload
	}
}

// StripProxyTools removes rewritten proxy function tools and neutralizes
// choices forcing them, so a final answer can be requested without search.
func StripProxyTools(format string, payload []byte, names map[string]bool) []byte {
	switch normalizeFallbackFormat(format) {
	case FallbackFormatClaude:
		return stripClaudeProxyTools(payload, names)
	case FallbackFormatOpenAIResponse, FallbackFormatCodex:
		return stripResponsesProxyTools(payload, names)
	case FallbackFormatOpenAI:
		return stripChatProxyTools(payload, names)
	case FallbackFormatGemini:
		return stripGeminiProxyTools(payload, names)
	default:
		return payload
	}
}

func isClaudeWebSearchToolType(toolType string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(toolType)), "web_search_")
}

func isResponsesWebSearchToolType(toolType string) bool {
	switch strings.TrimSpace(toolType) {
	case "web_search", "web_search_2025_08_26", "web_search_preview", "web_search_preview_2025_03_11":
		return true
	default:
		return false
	}
}

func hasClaudeServerSearchTool(payload []byte) bool {
	found := false
	gjson.GetBytes(payload, "tools").ForEach(func(_, tool gjson.Result) bool {
		if isClaudeWebSearchToolType(tool.Get("type").String()) {
			found = true
			return false
		}
		return true
	})
	return found
}

func allClaudeToolsAreSearch(payload []byte) bool {
	tools := gjson.GetBytes(payload, "tools")
	if !tools.IsArray() || len(tools.Array()) == 0 {
		return false
	}
	searchOnly := true
	tools.ForEach(func(_, tool gjson.Result) bool {
		if !isClaudeWebSearchToolType(tool.Get("type").String()) {
			searchOnly = false
			return false
		}
		return true
	})
	return searchOnly
}

func hasResponsesServerSearchTool(payload []byte) bool {
	for _, path := range responsesToolArrayPaths(payload) {
		if toolsHasType(payload, path, isResponsesWebSearchToolType) {
			return true
		}
	}
	return false
}

func claudeServerSearchToolNames(payload []byte) map[string]bool {
	names := map[string]bool{}
	gjson.GetBytes(payload, "tools").ForEach(func(_, tool gjson.Result) bool {
		if isClaudeWebSearchToolType(tool.Get("type").String()) {
			if name := strings.TrimSpace(tool.Get("name").String()); name != "" {
				names[name] = true
			}
		}
		return true
	})
	return names
}

func claudeChoiceAllowsSearch(payload []byte) bool {
	choice := gjson.GetBytes(payload, "tool_choice")
	if !choice.Exists() {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(choice.Get("type").String())) {
	case "", "auto", "any":
		return true
	case "none":
		return false
	case "tool":
		return claudeServerSearchToolNames(payload)[strings.TrimSpace(choice.Get("name").String())]
	default:
		return true
	}
}
func toolsHasType(payload []byte, path string, match func(string) bool) bool {
	found := false
	gjson.GetBytes(payload, path).ForEach(func(_, tool gjson.Result) bool {
		if match(tool.Get("type").String()) {
			found = true
			return false
		}
		return true
	})
	return found
}

// responsesToolArrayPaths lists tool arrays: top-level plus any
// additional_tools input items.
func responsesToolArrayPaths(payload []byte) []string {
	paths := []string{"tools"}
	input := gjson.GetBytes(payload, "input")
	if input.IsArray() {
		for index := range input.Array() {
			if gjson.GetBytes(payload, fmt.Sprintf("input.%d.type", index)).String() == "additional_tools" {
				paths = append(paths, fmt.Sprintf("input.%d.tools", index))
			}
		}
	}
	return paths
}

func allResponsesToolsAreSearch(payload []byte) bool {
	count := 0
	searchOnly := true
	for _, path := range responsesToolArrayPaths(payload) {
		gjson.GetBytes(payload, path).ForEach(func(_, tool gjson.Result) bool {
			count++
			if !isResponsesWebSearchToolType(tool.Get("type").String()) {
				searchOnly = false
				return false
			}
			return true
		})
		if !searchOnly {
			return false
		}
	}
	return searchOnly && count > 0
}

func responsesChoiceAllowsSearch(payload []byte) bool {
	choice := gjson.GetBytes(payload, "tool_choice")
	if !choice.Exists() {
		return true
	}
	if choice.Type == gjson.String {
		switch strings.ToLower(strings.TrimSpace(choice.String())) {
		case "none":
			return false
		default:
			return true
		}
	}
	switch strings.ToLower(strings.TrimSpace(choice.Get("type").String())) {
	case "", "auto", "required":
		return true
	case "none":
		return false
	case "allowed_tools":
		allowed := false
		choice.Get("tools").ForEach(func(_, tool gjson.Result) bool {
			if isResponsesWebSearchToolType(tool.Get("type").String()) {
				allowed = true
				return false
			}
			return true
		})
		return allowed
	default:
		if isResponsesWebSearchToolType(choice.Get("type").String()) {
			return true
		}
		return false
	}
}

func hasChatServerSearchTool(payload []byte) bool {
	return toolsHasType(payload, "tools", isResponsesWebSearchToolType)
}

func allChatToolsAreSearch(payload []byte) bool {
	tools := gjson.GetBytes(payload, "tools")
	if !tools.IsArray() || len(tools.Array()) == 0 {
		return false
	}
	searchOnly := true
	tools.ForEach(func(_, tool gjson.Result) bool {
		if !isResponsesWebSearchToolType(tool.Get("type").String()) {
			searchOnly = false
			return false
		}
		return true
	})
	return searchOnly
}

func chatChoiceAllowsSearch(payload []byte) bool {
	choice := gjson.GetBytes(payload, "tool_choice")
	if !choice.Exists() {
		return true
	}
	if choice.Type == gjson.String {
		switch strings.ToLower(strings.TrimSpace(choice.String())) {
		case "none":
			return false
		default:
			return true
		}
	}
	choiceType := strings.ToLower(strings.TrimSpace(choice.Get("type").String()))
	if isResponsesWebSearchToolType(choice.Get("type").String()) {
		return true
	}
	switch choiceType {
	case "none":
		return false
	case "function", "tool":
		return false
	default:
		return true
	}
}

func hasGeminiServerSearchTool(payload []byte) bool {
	found := false
	gjson.GetBytes(payload, "tools").ForEach(func(_, tool gjson.Result) bool {
		if tool.Get("googleSearch").Exists() {
			found = true
			return false
		}
		return true
	})
	return found
}

func allGeminiToolsAreSearch(payload []byte) bool {
	tools := gjson.GetBytes(payload, "tools")
	if !tools.IsArray() || len(tools.Array()) == 0 {
		return false
	}
	searchOnly := true
	tools.ForEach(func(_, tool gjson.Result) bool {
		if !tool.Get("googleSearch").Exists() || tool.Get("functionDeclarations").Exists() {
			searchOnly = false
			return false
		}
		return true
	})
	return searchOnly
}

func geminiChoiceAllowsSearch(payload []byte) bool {
	mode := gjson.GetBytes(payload, "toolConfig.functionCallingConfig.mode").String()
	switch strings.ToUpper(strings.TrimSpace(mode)) {
	case "", "AUTO", "ANY", "VALIDATED":
		return true
	default:
		return false
	}
}

// aliasProxyName picks a function name that does not collide with taken.
func aliasProxyName(desired string, taken map[string]bool) string {
	desired = strings.TrimSpace(desired)
	if desired == "" {
		desired = proxySearchName
	}
	if !taken[desired] {
		return desired
	}
	for i := 1; ; i++ {
		candidate := fmt.Sprintf("proxy_%s_%d", proxySearchName, i)
		if i == 1 {
			candidate = "proxy_" + proxySearchName
		}
		if !taken[candidate] {
			return candidate
		}
	}
}

func claudeTakenFunctionNames(payload []byte) map[string]bool {
	taken := map[string]bool{}
	gjson.GetBytes(payload, "tools").ForEach(func(_, tool gjson.Result) bool {
		if !isClaudeWebSearchToolType(tool.Get("type").String()) {
			if name := strings.TrimSpace(tool.Get("name").String()); name != "" {
				taken[name] = true
			}
		}
		return true
	})
	return taken
}

func rewriteClaudeSearchTools(payload []byte) ([]byte, []string) {
	tools := gjson.GetBytes(payload, "tools")
	if !tools.IsArray() {
		return payload, nil
	}
	taken := claudeTakenFunctionNames(payload)
	renamed := map[string]string{}
	var names []string
	out := payload
	entries := tools.Array()
	for index := range entries {
		tool := entries[index]
		if !isClaudeWebSearchToolType(tool.Get("type").String()) {
			continue
		}
		original := strings.TrimSpace(tool.Get("name").String())
		if original == "" {
			original = proxySearchName
		}
		chosen := aliasProxyName(original, taken)
		taken[chosen] = true
		renamed[original] = chosen
		names = append(names, chosen)
		replacement := []byte(`{"name":"","description":"","input_schema":{"type":"object","properties":{"query":{"type":"string","description":""}},"required":["query"],"additionalProperties":false}}`)
		replacement, _ = sjson.SetBytes(replacement, "name", chosen)
		replacement, _ = sjson.SetBytes(replacement, "description", proxySearchDescription)
		replacement, _ = sjson.SetBytes(replacement, "input_schema.properties.query.description", proxySearchQueryDesc)
		out, _ = sjson.SetRawBytes(out, fmt.Sprintf("tools.%d", index), replacement)
	}
	if len(names) == 0 {
		return payload, nil
	}
	choice := gjson.GetBytes(out, "tool_choice")
	if choice.IsObject() && strings.ToLower(strings.TrimSpace(choice.Get("type").String())) == "tool" {
		if chosen, ok := renamed[strings.TrimSpace(choice.Get("name").String())]; ok {
			out, _ = sjson.SetBytes(out, "tool_choice.name", chosen)
		}
	}
	return out, names
}

func responsesTakenFunctionNames(payload []byte) map[string]bool {
	taken := map[string]bool{}
	for _, path := range responsesToolArrayPaths(payload) {
		gjson.GetBytes(payload, path).ForEach(func(_, tool gjson.Result) bool {
			if tool.Get("type").String() == "function" || tool.Get("type").String() == "custom" {
				if name := strings.TrimSpace(tool.Get("name").String()); name != "" {
					taken[name] = true
				}
			}
			return true
		})
	}
	return taken
}

func rewriteResponsesSearchTools(payload []byte) ([]byte, []string) {
	taken := responsesTakenFunctionNames(payload)
	var names []string
	out := payload
	for _, path := range responsesToolArrayPaths(out) {
		tools := gjson.GetBytes(out, path)
		if !tools.IsArray() {
			continue
		}
		entries := tools.Array()
		for index := range entries {
			tool := entries[index]
			if !isResponsesWebSearchToolType(tool.Get("type").String()) {
				continue
			}
			chosen := aliasProxyName(proxySearchName, taken)
			taken[chosen] = true
			names = append(names, chosen)
			replacement := []byte(`{"type":"function","name":"","description":"","parameters":{"type":"object","properties":{"query":{"type":"string","description":""}},"required":["query"],"additionalProperties":false}}`)
			replacement, _ = sjson.SetBytes(replacement, "name", chosen)
			replacement, _ = sjson.SetBytes(replacement, "description", proxySearchDescription)
			replacement, _ = sjson.SetBytes(replacement, "parameters.properties.query.description", proxySearchQueryDesc)
			out, _ = sjson.SetRawBytes(out, fmt.Sprintf("%s.%d", path, index), replacement)
		}
	}
	if len(names) == 0 {
		return payload, nil
	}
	out = rewriteResponsesSearchChoice(out, names[0])
	return out, names
}

func rewriteResponsesSearchChoice(payload []byte, chosen string) []byte {
	choice := gjson.GetBytes(payload, "tool_choice")
	if !choice.IsObject() {
		return payload
	}
	choiceType := choice.Get("type").String()
	if isResponsesWebSearchToolType(choiceType) {
		out, _ := sjson.SetBytes(payload, "tool_choice.type", "function")
		out, _ = sjson.SetBytes(out, "tool_choice.name", chosen)
		return out
	}
	if strings.ToLower(strings.TrimSpace(choiceType)) == "allowed_tools" {
		members := choice.Get("tools")
		if !members.IsArray() {
			return payload
		}
		out := payload
		seenChosen := false
		// Collect-then-replace to keep indices stable.
		kept := [][]byte{}
		for _, member := range members.Array() {
			if isResponsesWebSearchToolType(member.Get("type").String()) {
				if seenChosen {
					continue
				}
				seenChosen = true
				replacement := []byte(`{"type":"function","name":""}`)
				replacement, _ = sjson.SetBytes(replacement, "name", chosen)
				kept = append(kept, replacement)
				continue
			}
			kept = append(kept, []byte(member.Raw))
		}
		joined := []byte("[]")
		if len(kept) > 0 {
			joined = joinRawJSONArray(kept)
		}
		out, _ = sjson.SetRawBytes(out, "tool_choice.tools", joined)
		return out
	}
	return payload
}

func joinRawJSONArray(items [][]byte) []byte {
	out := []byte("[")
	for index, item := range items {
		if index > 0 {
			out = append(out, ',')
		}
		out = append(out, item...)
	}
	return append(out, ']')
}

func chatTakenFunctionNames(payload []byte) map[string]bool {
	taken := map[string]bool{}
	gjson.GetBytes(payload, "tools").ForEach(func(_, tool gjson.Result) bool {
		if name := strings.TrimSpace(tool.Get("function.name").String()); name != "" {
			taken[name] = true
		}
		return true
	})
	return taken
}

func rewriteChatSearchTools(payload []byte) ([]byte, []string) {
	tools := gjson.GetBytes(payload, "tools")
	if !tools.IsArray() {
		return payload, nil
	}
	taken := chatTakenFunctionNames(payload)
	var names []string
	out := payload
	entries := tools.Array()
	for index := range entries {
		tool := entries[index]
		if !isResponsesWebSearchToolType(tool.Get("type").String()) {
			continue
		}
		chosen := aliasProxyName(proxySearchName, taken)
		taken[chosen] = true
		names = append(names, chosen)
		replacement := []byte(`{"type":"function","function":{"name":"","description":"","parameters":{"type":"object","properties":{"query":{"type":"string","description":""}},"required":["query"],"additionalProperties":false}}}`)
		replacement, _ = sjson.SetBytes(replacement, "function.name", chosen)
		replacement, _ = sjson.SetBytes(replacement, "function.description", proxySearchDescription)
		replacement, _ = sjson.SetBytes(replacement, "function.parameters.properties.query.description", proxySearchQueryDesc)
		out, _ = sjson.SetRawBytes(out, fmt.Sprintf("tools.%d", index), replacement)
	}
	if len(names) == 0 {
		return payload, nil
	}
	choice := gjson.GetBytes(out, "tool_choice")
	if choice.IsObject() && isResponsesWebSearchToolType(choice.Get("type").String()) {
		replacement := []byte(`{"type":"function","function":{"name":""}}`)
		replacement, _ = sjson.SetBytes(replacement, "function.name", names[0])
		out, _ = sjson.SetRawBytes(out, "tool_choice", replacement)
	}
	return out, names
}

func geminiTakenFunctionNames(payload []byte) map[string]bool {
	taken := map[string]bool{}
	gjson.GetBytes(payload, "tools").ForEach(func(_, tool gjson.Result) bool {
		tool.Get("functionDeclarations").ForEach(func(_, decl gjson.Result) bool {
			if name := strings.TrimSpace(decl.Get("name").String()); name != "" {
				taken[name] = true
			}
			return true
		})
		return true
	})
	return taken
}

func rewriteGeminiSearchTools(payload []byte) ([]byte, []string) {
	tools := gjson.GetBytes(payload, "tools")
	if !tools.IsArray() {
		return payload, nil
	}
	found := false
	out := payload
	entries := tools.Array()
	kept := [][]byte{}
	for _, tool := range entries {
		if !tool.Get("googleSearch").Exists() {
			kept = append(kept, []byte(tool.Raw))
			continue
		}
		found = true
		stripped, _ := sjson.DeleteBytes([]byte(tool.Raw), "googleSearch")
		if strings.TrimSpace(string(stripped)) != "{}" {
			kept = append(kept, stripped)
		}
	}
	if !found {
		return payload, nil
	}
	taken := geminiTakenFunctionNames(payload)
	chosen := aliasProxyName(proxySearchName, taken)
	declaration := []byte(`{"name":"","description":"","parameters":{"type":"OBJECT","properties":{"query":{"type":"STRING","description":""}},"required":["query"]}}`)
	declaration, _ = sjson.SetBytes(declaration, "name", chosen)
	declaration, _ = sjson.SetBytes(declaration, "description", proxySearchDescription)
	declaration, _ = sjson.SetBytes(declaration, "parameters.properties.query.description", proxySearchQueryDesc)
	block := []byte(`{"functionDeclarations":[]}`)
	block, _ = sjson.SetRawBytes(block, "functionDeclarations", joinRawJSONArray([][]byte{declaration}))
	kept = append(kept, block)
	out, _ = sjson.SetRawBytes(out, "tools", joinRawJSONArray(kept))
	if allowed := gjson.GetBytes(out, "toolConfig.functionCallingConfig.allowedFunctionNames"); allowed.IsArray() {
		present := false
		allowed.ForEach(func(_, name gjson.Result) bool {
			if name.String() == chosen {
				present = true
				return false
			}
			return true
		})
		if !present {
			out, _ = sjson.SetRawBytes(out, "toolConfig.functionCallingConfig.allowedFunctionNames.-1", []byte(`"`+chosen+`"`))
		}
	}
	return out, []string{chosen}
}

// searchQueryFromArgs extracts the query from function arguments that may be
// an object with common key names or a bare string.
func searchQueryFromArgs(args gjson.Result) string {
	if !args.Exists() {
		return ""
	}
	if args.Type == gjson.String {
		return strings.TrimSpace(args.String())
	}
	if args.Type != gjson.JSON {
		return ""
	}
	for _, key := range []string{"query", "q", "search_query", "text", "keywords"} {
		if value := args.Get(key); value.Type == gjson.String && strings.TrimSpace(value.String()) != "" {
			return strings.TrimSpace(value.String())
		}
	}
	return ""
}

func searchQueryFromJSONString(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	parsed := gjson.Parse(raw)
	if !parsed.Exists() {
		return raw
	}
	if query := searchQueryFromArgs(parsed); query != "" {
		return query
	}
	if parsed.Type == gjson.String {
		return strings.TrimSpace(parsed.String())
	}
	return ""
}

func extractClaudeProxyCalls(body []byte, names map[string]bool) []ProxyCall {
	var calls []ProxyCall
	gjson.GetBytes(body, "content").ForEach(func(_, block gjson.Result) bool {
		if block.Get("type").String() != "tool_use" {
			return true
		}
		name := block.Get("name").String()
		if !names[name] {
			return true
		}
		calls = append(calls, ProxyCall{
			ID:    block.Get("id").String(),
			Name:  name,
			Query: searchQueryFromArgs(block.Get("input")),
		})
		return true
	})
	return calls
}

func extractResponsesProxyCalls(body []byte, names map[string]bool) []ProxyCall {
	var calls []ProxyCall
	gjson.GetBytes(body, "output").ForEach(func(index, item gjson.Result) bool {
		if item.Get("type").String() != "function_call" {
			return true
		}
		name := item.Get("name").String()
		if !names[name] {
			return true
		}
		id := item.Get("call_id").String()
		if id == "" {
			id = item.Get("id").String()
		}
		if id == "" {
			id = fmt.Sprintf("proxy-call-%d", len(calls))
		}
		calls = append(calls, ProxyCall{ID: id, Name: name, Query: searchQueryFromJSONString(item.Get("arguments").String())})
		return true
	})
	return calls
}

func extractChatProxyCalls(body []byte, names map[string]bool) []ProxyCall {
	var calls []ProxyCall
	gjson.GetBytes(body, "choices.0.message.tool_calls").ForEach(func(index, call gjson.Result) bool {
		name := call.Get("function.name").String()
		if !names[name] {
			return true
		}
		id := call.Get("id").String()
		if id == "" {
			id = fmt.Sprintf("proxy-call-%d", len(calls))
		}
		calls = append(calls, ProxyCall{ID: id, Name: name, Query: searchQueryFromJSONString(call.Get("function.arguments").String())})
		return true
	})
	return calls
}

func extractGeminiProxyCalls(body []byte, names map[string]bool) []ProxyCall {
	var calls []ProxyCall
	gjson.GetBytes(body, "candidates.0.content.parts").ForEach(func(index, part gjson.Result) bool {
		call := part.Get("functionCall")
		if !call.Exists() {
			return true
		}
		name := call.Get("name").String()
		if !names[name] {
			return true
		}
		calls = append(calls, ProxyCall{
			ID:    fmt.Sprintf("gemini-call-%d", len(calls)),
			Name:  name,
			Query: searchQueryFromArgs(call.Get("args")),
		})
		return true
	})
	return calls
}

func appendClaudeSearchResults(payload, body []byte, calls []ProxyCall, outputs []string) []byte {
	echo := gjson.GetBytes(body, "content")
	var assistantContent []byte
	if echo.IsArray() && len(echo.Array()) > 0 {
		assistantContent = []byte(echo.Raw)
	} else {
		blocks := make([][]byte, 0, len(calls))
		for _, call := range calls {
			block := []byte(`{"type":"tool_use","id":"","name":"","input":{"query":""}}`)
			block, _ = sjson.SetBytes(block, "id", call.ID)
			block, _ = sjson.SetBytes(block, "name", call.Name)
			block, _ = sjson.SetBytes(block, "input.query", call.Query)
			blocks = append(blocks, block)
		}
		assistantContent = joinRawJSONArray(blocks)
	}
	out := payload
	out, _ = sjson.SetRawBytes(out, "messages.-1", joinClaudeMessage("assistant", assistantContent))
	results := make([][]byte, 0, len(calls))
	for index, call := range calls {
		output := ""
		if index < len(outputs) {
			output = outputs[index]
		}
		result := []byte(`{"type":"tool_result","tool_use_id":"","content":""}`)
		result, _ = sjson.SetBytes(result, "tool_use_id", call.ID)
		result, _ = sjson.SetBytes(result, "content", output)
		results = append(results, result)
	}
	out, _ = sjson.SetRawBytes(out, "messages.-1", joinClaudeMessage("user", joinRawJSONArray(results)))
	return out
}

func joinClaudeMessage(role string, content []byte) []byte {
	message := []byte(`{"role":"","content":[]}`)
	message, _ = sjson.SetBytes(message, "role", role)
	message, _ = sjson.SetRawBytes(message, "content", content)
	return message
}

func appendResponsesSearchResults(payload, body []byte, calls []ProxyCall, outputs []string) []byte {
	out := payload
	gjson.GetBytes(body, "output").ForEach(func(_, item gjson.Result) bool {
		out, _ = sjson.SetRawBytes(out, "input.-1", []byte(item.Raw))
		return true
	})
	if !gjson.GetBytes(body, "output").IsArray() {
		for _, call := range calls {
			item := []byte(`{"type":"function_call","call_id":"","name":"","arguments":""}`)
			item, _ = sjson.SetBytes(item, "call_id", call.ID)
			item, _ = sjson.SetBytes(item, "name", call.Name)
			arguments, _ := sjson.SetBytes([]byte(`{}`), "query", call.Query)
			item, _ = sjson.SetBytes(item, "arguments", string(arguments))
			out, _ = sjson.SetRawBytes(out, "input.-1", item)
		}
	}
	for index, call := range calls {
		output := ""
		if index < len(outputs) {
			output = outputs[index]
		}
		entry := []byte(`{"type":"function_call_output","call_id":"","output":""}`)
		entry, _ = sjson.SetBytes(entry, "call_id", call.ID)
		entry, _ = sjson.SetBytes(entry, "output", output)
		out, _ = sjson.SetRawBytes(out, "input.-1", entry)
	}
	return out
}

func appendChatSearchResults(payload, body []byte, calls []ProxyCall, outputs []string) []byte {
	out := payload
	assistant := gjson.GetBytes(body, "choices.0.message")
	if assistant.IsObject() {
		echo := []byte(assistant.Raw)
		echo, _ = sjson.SetBytes(echo, "role", "assistant")
		out, _ = sjson.SetRawBytes(out, "messages.-1", echo)
	} else {
		fallback := []byte(`{"role":"assistant","content":null,"tool_calls":[]}`)
		toolCalls := make([][]byte, 0, len(calls))
		for _, call := range calls {
			entry := []byte(`{"id":"","type":"function","function":{"name":"","arguments":""}}`)
			entry, _ = sjson.SetBytes(entry, "id", call.ID)
			entry, _ = sjson.SetBytes(entry, "function.name", call.Name)
			arguments, _ := sjson.SetBytes([]byte(`{}`), "query", call.Query)
			entry, _ = sjson.SetBytes(entry, "function.arguments", string(arguments))
			toolCalls = append(toolCalls, entry)
		}
		fallback, _ = sjson.SetRawBytes(fallback, "tool_calls", joinRawJSONArray(toolCalls))
		out, _ = sjson.SetRawBytes(out, "messages.-1", fallback)
	}
	for index, call := range calls {
		output := ""
		if index < len(outputs) {
			output = outputs[index]
		}
		entry := []byte(`{"role":"tool","tool_call_id":"","content":""}`)
		entry, _ = sjson.SetBytes(entry, "tool_call_id", call.ID)
		entry, _ = sjson.SetBytes(entry, "content", output)
		out, _ = sjson.SetRawBytes(out, "messages.-1", entry)
	}
	return out
}

func appendGeminiSearchResults(payload, body []byte, calls []ProxyCall, outputs []string) []byte {
	out := payload
	parts := gjson.GetBytes(body, "candidates.0.content.parts")
	var echo []byte
	if parts.IsArray() && len(parts.Array()) > 0 {
		echo = []byte(parts.Raw)
	} else {
		synthesized := make([][]byte, 0, len(calls))
		for _, call := range calls {
			part := []byte(`{"functionCall":{"name":"","args":{"query":""}}}`)
			part, _ = sjson.SetBytes(part, "functionCall.name", call.Name)
			part, _ = sjson.SetBytes(part, "functionCall.args.query", call.Query)
			synthesized = append(synthesized, part)
		}
		echo = joinRawJSONArray(synthesized)
	}
	modelTurn := []byte(`{"role":"model","parts":[]}`)
	modelTurn, _ = sjson.SetRawBytes(modelTurn, "parts", echo)
	out, _ = sjson.SetRawBytes(out, "contents.-1", modelTurn)
	responses := make([][]byte, 0, len(calls))
	for index, call := range calls {
		output := ""
		if index < len(outputs) {
			output = outputs[index]
		}
		part := []byte(`{"functionResponse":{"name":"","response":{"output":""}}}`)
		part, _ = sjson.SetBytes(part, "functionResponse.name", call.Name)
		part, _ = sjson.SetBytes(part, "functionResponse.response.output", output)
		responses = append(responses, part)
	}
	userTurn := []byte(`{"role":"user","parts":[]}`)
	userTurn, _ = sjson.SetRawBytes(userTurn, "parts", joinRawJSONArray(responses))
	out, _ = sjson.SetRawBytes(out, "contents.-1", userTurn)
	return out
}

func stripClaudeProxyTools(payload []byte, names map[string]bool) []byte {
	out := payload
	tools := gjson.GetBytes(out, "tools")
	if tools.IsArray() {
		kept := [][]byte{}
		for _, tool := range tools.Array() {
			if names[strings.TrimSpace(tool.Get("name").String())] {
				continue
			}
			kept = append(kept, []byte(tool.Raw))
		}
		out, _ = sjson.SetRawBytes(out, "tools", joinRawJSONArray(kept))
	}
	choice := gjson.GetBytes(out, "tool_choice")
	if choice.IsObject() {
		choiceType := strings.ToLower(strings.TrimSpace(choice.Get("type").String()))
		if choiceType == "any" || (choiceType == "tool" && names[strings.TrimSpace(choice.Get("name").String())]) {
			out, _ = sjson.SetBytes(out, "tool_choice.type", "auto")
			out, _ = sjson.DeleteBytes(out, "tool_choice.name")
		}
	}
	return dropEmptyToolChoice(out, "tools")
}

func stripResponsesProxyTools(payload []byte, names map[string]bool) []byte {
	out := payload
	for _, path := range responsesToolArrayPaths(out) {
		tools := gjson.GetBytes(out, path)
		if !tools.IsArray() {
			continue
		}
		kept := [][]byte{}
		for _, tool := range tools.Array() {
			toolType := tool.Get("type").String()
			if (toolType == "function" || toolType == "custom") && names[strings.TrimSpace(tool.Get("name").String())] {
				continue
			}
			kept = append(kept, []byte(tool.Raw))
		}
		out, _ = sjson.SetRawBytes(out, path, joinRawJSONArray(kept))
	}
	choice := gjson.GetBytes(out, "tool_choice")
	if choice.IsObject() {
		choiceType := strings.ToLower(strings.TrimSpace(choice.Get("type").String()))
		switch choiceType {
		case "function", "custom", "tool":
			if names[strings.TrimSpace(choice.Get("name").String())] {
				out, _ = sjson.SetBytes(out, "tool_choice", "auto")
			}
		case "allowed_tools":
			members := choice.Get("tools")
			if members.IsArray() {
				kept := [][]byte{}
				for _, member := range members.Array() {
					memberType := member.Get("type").String()
					if (memberType == "function" || memberType == "custom" || memberType == "tool") &&
						names[strings.TrimSpace(member.Get("name").String())] {
						continue
					}
					kept = append(kept, []byte(member.Raw))
				}
				if len(kept) == 0 {
					out, _ = sjson.SetBytes(out, "tool_choice", "auto")
				} else {
					out, _ = sjson.SetRawBytes(out, "tool_choice.tools", joinRawJSONArray(kept))
				}
			}
		}
	}
	return dropEmptyToolChoice(out, "tools")
}

func stripChatProxyTools(payload []byte, names map[string]bool) []byte {
	out := payload
	tools := gjson.GetBytes(out, "tools")
	if tools.IsArray() {
		kept := [][]byte{}
		for _, tool := range tools.Array() {
			if names[strings.TrimSpace(tool.Get("function.name").String())] {
				continue
			}
			kept = append(kept, []byte(tool.Raw))
		}
		out, _ = sjson.SetRawBytes(out, "tools", joinRawJSONArray(kept))
	}
	choice := gjson.GetBytes(out, "tool_choice")
	if choice.IsObject() {
		if names[strings.TrimSpace(choice.Get("function.name").String())] {
			out, _ = sjson.SetBytes(out, "tool_choice", "auto")
		} else if names[strings.TrimSpace(choice.Get("name").String())] &&
			strings.ToLower(strings.TrimSpace(choice.Get("type").String())) == "function" {
			out, _ = sjson.SetBytes(out, "tool_choice", "auto")
		}
	}
	return dropEmptyToolChoice(out, "tools")
}

func stripGeminiProxyTools(payload []byte, names map[string]bool) []byte {
	out := payload
	tools := gjson.GetBytes(out, "tools")
	if tools.IsArray() {
		kept := [][]byte{}
		for _, tool := range tools.Array() {
			declarations := tool.Get("functionDeclarations")
			if !declarations.IsArray() {
				kept = append(kept, []byte(tool.Raw))
				continue
			}
			remaining := [][]byte{}
			for _, decl := range declarations.Array() {
				if names[strings.TrimSpace(decl.Get("name").String())] {
					continue
				}
				remaining = append(remaining, []byte(decl.Raw))
			}
			if len(remaining) == 0 {
				continue
			}
			updated, _ := sjson.SetRawBytes([]byte(tool.Raw), "functionDeclarations", joinRawJSONArray(remaining))
			kept = append(kept, updated)
		}
		out, _ = sjson.SetRawBytes(out, "tools", joinRawJSONArray(kept))
	}
	allowed := gjson.GetBytes(out, "toolConfig.functionCallingConfig.allowedFunctionNames")
	if allowed.IsArray() {
		kept := [][]byte{}
		for _, name := range allowed.Array() {
			if names[name.String()] {
				continue
			}
			kept = append(kept, []byte(`"`+name.String()+`"`))
		}
		if len(kept) == 0 {
			out, _ = sjson.DeleteBytes(out, "toolConfig")
		} else {
			out, _ = sjson.SetRawBytes(out, "toolConfig.functionCallingConfig.allowedFunctionNames", joinRawJSONArray(kept))
		}
	}
	if tools := gjson.GetBytes(out, "tools"); !tools.IsArray() || len(tools.Array()) == 0 {
		out, _ = sjson.DeleteBytes(out, "tools")
	}
	return out
}

// dropEmptyToolChoice removes tool_choice (and tools) when no tools remain,
// since upstreams reject choices without declarations.
func dropEmptyToolChoice(payload []byte, toolsPath string) []byte {
	tools := gjson.GetBytes(payload, toolsPath)
	if !tools.Exists() {
		return payload
	}
	if tools.IsArray() && len(tools.Array()) > 0 {
		return payload
	}
	out, _ := sjson.DeleteBytes(payload, "tools")
	out, _ = sjson.DeleteBytes(out, "tool_choice")
	return out
}
