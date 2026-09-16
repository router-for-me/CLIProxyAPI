package responses

import (
	"strings"
	"unicode/utf8"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	translatorcommon "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/common"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ModelSupportsWebSearch checks whether the given model supports native web search,
// checking both dynamic registry capability (SupportsWebSearch) and models.json
// static definitions (native_capabilities.web_search). Explicit false wins as a veto.
func ModelSupportsWebSearch(modelID string) bool {
	info := registry.LookupModelInfo(modelID)
	infoAG := registry.LookupModelInfo(modelID, "antigravity")

	// 1. Explicit false in static definitions acts as an absolute veto.
	if info != nil && info.NativeCapabilities != nil && info.NativeCapabilities.WebSearch != nil && !*info.NativeCapabilities.WebSearch {
		return false
	}
	if infoAG != nil && infoAG.NativeCapabilities != nil && infoAG.NativeCapabilities.WebSearch != nil && !*infoAG.NativeCapabilities.WebSearch {
		return false
	}

	// 2. Explicit true in static definitions.
	if info != nil && info.NativeCapabilities != nil && info.NativeCapabilities.WebSearch != nil && *info.NativeCapabilities.WebSearch {
		return true
	}
	if infoAG != nil && infoAG.NativeCapabilities != nil && infoAG.NativeCapabilities.WebSearch != nil && *infoAG.NativeCapabilities.WebSearch {
		return true
	}

	// 3. Dynamic capability checks via Antigravity probes and registry flags.
	if registry.AntigravityWebSearchModelFor(modelID) != "" {
		return true
	}
	if (info != nil && info.SupportsWebSearch) || (infoAG != nil && infoAG.SupportsWebSearch) {
		return true
	}
	return false
}

// isResponsesWebSearchToolType checks whether a tool type matches OpenAI Responses web search tool.
// Official OpenAI documentation supports "web_search", "web_search_2025_08_26", and legacy "web_search_preview".
func isResponsesWebSearchToolType(toolType string) bool {
	switch toolType {
	case "web_search", "web_search_2025_08_26", "web_search_preview":
		return true
	default:
		return false
	}
}

// HasResponsesWebSearchTool checks if the request tools array contains an OpenAI web search tool.
func HasResponsesWebSearchTool(root gjson.Result) bool {
	tools := root.Get("tools")
	if !tools.IsArray() {
		return false
	}
	for _, tool := range tools.Array() {
		if isResponsesWebSearchToolType(tool.Get("type").String()) {
			return true
		}
	}
	return false
}

// HasOnlyResponsesWebSearchTools checks if every tool in the tools array is a web search tool.
func HasOnlyResponsesWebSearchTools(root gjson.Result) bool {
	tools := root.Get("tools")
	if !tools.IsArray() {
		return false
	}
	hasSearch := false
	for _, tool := range tools.Array() {
		if isResponsesWebSearchToolType(tool.Get("type").String()) {
			hasSearch = true
			continue
		}
		return false
	}
	return hasSearch
}

// AllowsResponsesWebSearchToolChoice checks whether tool_choice permits web search execution.
func AllowsResponsesWebSearchToolChoice(root gjson.Result) bool {
	toolChoice := root.Get("tool_choice")
	if !toolChoice.Exists() {
		return true
	}
	if toolChoice.Type == gjson.String {
		switch toolChoice.String() {
		case "", "auto", "required":
			return true
		case "none":
			return false
		default:
			return false
		}
	}
	if toolChoice.IsObject() {
		switch toolChoice.Get("type").String() {
		case "", "auto", "required":
			return true
		case "web_search", "web_search_2025_08_26", "web_search_preview":
			return true
		case "allowed_tools":
			tools := toolChoice.Get("tools")
			if tools.IsArray() {
				for _, t := range tools.Array() {
					if isResponsesWebSearchToolType(t.Get("type").String()) {
						return true
					}
				}
			}
			return false
		default:
			return false
		}
	}
	return false
}

// ExtractResponsesWebSearchQuery extracts the search query from OpenAI Responses input.
// It handles simple string inputs, array of conversation messages, direct content part arrays, and instruction fallbacks.
func ExtractResponsesWebSearchQuery(root gjson.Result) string {
	input := root.Get("input")
	if input.Type == gjson.String {
		return strings.TrimSpace(input.String())
	}
	if input.IsArray() {
		items := input.Array()
		// Check if input is a flat array of content parts (e.g. [{"type":"input_text",...}])
		var flatParts []string
		isFlatParts := true
		for _, item := range items {
			if item.Get("type").String() == "input_text" {
				if text := strings.TrimSpace(item.Get("text").String()); text != "" {
					flatParts = append(flatParts, text)
				}
			} else if item.Get("role").Exists() {
				isFlatParts = false
				break
			}
		}
		if isFlatParts && len(flatParts) > 0 {
			return strings.Join(flatParts, "\n")
		}

		for i := len(items) - 1; i >= 0; i-- {
			item := items[i]
			role := item.Get("role").String()
			if role != "" && role != "user" {
				continue
			}
			content := item.Get("content")
			if content.Type == gjson.String && strings.TrimSpace(content.String()) != "" {
				return strings.TrimSpace(content.String())
			}
			if content.IsArray() {
				var textParts []string
				for _, part := range content.Array() {
					if text := strings.TrimSpace(part.Get("text").String()); text != "" {
						textParts = append(textParts, text)
					}
				}
				if len(textParts) > 0 {
					return strings.Join(textParts, "\n")
				}
			}
			if text := strings.TrimSpace(item.Get("text").String()); text != "" {
				return text
			}
		}
	}
	if instructions := root.Get("instructions").String(); strings.TrimSpace(instructions) != "" {
		return strings.TrimSpace(instructions)
	}
	return ""
}

// ExtractResponsesWebSearchAllowedDomains extracts allowed domains from tools[].filters.allowed_domains.
func ExtractResponsesWebSearchAllowedDomains(root gjson.Result) []string {
	tools := root.Get("tools")
	if !tools.IsArray() {
		return nil
	}
	for _, tool := range tools.Array() {
		if !isResponsesWebSearchToolType(tool.Get("type").String()) {
			continue
		}
		allowedDomains := tool.Get("filters.allowed_domains")
		if !allowedDomains.IsArray() {
			continue
		}
		var domains []string
		for _, domain := range allowedDomains.Array() {
			if d := strings.TrimSpace(domain.String()); d != "" {
				domains = append(domains, d)
			}
		}
		return domains
	}
	return nil
}

// ExtractGroundingMetadata locates groundingMetadata in either direct or wrapped Gemini response.
func ExtractGroundingMetadata(root gjson.Result) gjson.Result {
	if gm := root.Get("candidates.0.groundingMetadata"); gm.Exists() {
		return gm
	}
	if gm := root.Get("response.candidates.0.groundingMetadata"); gm.Exists() {
		return gm
	}
	return gjson.Result{}
}

// ExtractGroundingQueries retrieves the list of search queries from groundingMetadata.
func ExtractGroundingQueries(groundingMetadata gjson.Result) []string {
	var queries []string
	if webSearchQueries := groundingMetadata.Get("webSearchQueries"); webSearchQueries.IsArray() {
		for _, q := range webSearchQueries.Array() {
			if str := strings.TrimSpace(q.String()); str != "" {
				queries = append(queries, str)
			}
		}
	}
	return queries
}

// ExtractGroundingSources converts groundingMetadata groundingChunks into OpenAI Responses source objects.
func ExtractGroundingSources(groundingMetadata gjson.Result) [][]byte {
	chunks := groundingMetadata.Get("groundingChunks").Array()
	if len(chunks) == 0 {
		return nil
	}
	var sources [][]byte
	seenURLs := make(map[string]bool)
	for _, chunk := range chunks {
		uri := strings.TrimSpace(chunk.Get("web.uri").String())
		if uri == "" || seenURLs[uri] {
			continue
		}
		seenURLs[uri] = true
		src := []byte(`{"type":"url","url":""}`)
		src, _ = sjson.SetBytes(src, "url", uri)
		sources = append(sources, src)
	}
	return sources
}

// BuildResponsesWebSearchCallItem formats an OpenAI Responses web_search_call output item.
func BuildResponsesWebSearchCallItem(id string, query string, queries []string, sources [][]byte) []byte {
	item := []byte(`{"id":"","type":"web_search_call","status":"completed","action":{"type":"search","query":""}}`)
	item, _ = sjson.SetBytes(item, "id", id)
	item, _ = sjson.SetBytes(item, "action.query", query)
	if len(queries) > 0 {
		item, _ = sjson.SetBytes(item, "action.queries", queries)
	}
	if len(sources) > 0 {
		item, _ = sjson.SetRawBytes(item, "action.sources", translatorcommon.JoinRawArray(sources))
	}
	return item
}

// HasValidWebGrounding checks whether groundingMetadata contains actual web search queries or web grounding chunks.
func HasValidWebGrounding(groundingMetadata gjson.Result) bool {
	if !groundingMetadata.Exists() {
		return false
	}
	if queries := groundingMetadata.Get("webSearchQueries"); queries.IsArray() && len(queries.Array()) > 0 {
		for _, q := range queries.Array() {
			if strings.TrimSpace(q.String()) != "" {
				return true
			}
		}
	}
	if chunks := groundingMetadata.Get("groundingChunks"); chunks.IsArray() && len(chunks.Array()) > 0 {
		for _, c := range chunks.Array() {
			if strings.TrimSpace(c.Get("web.uri").String()) != "" {
				return true
			}
		}
	}
	return false
}

// byteOffsetToRuneOffset converts a UTF-8 byte offset in text to a 0-based rune (character) offset.
func byteOffsetToRuneOffset(text string, byteOffset int64) int64 {
	if byteOffset <= 0 {
		return 0
	}
	textBytes := []byte(text)
	if byteOffset >= int64(len(textBytes)) {
		return int64(utf8.RuneCount(textBytes))
	}
	return int64(utf8.RuneCount(textBytes[:byteOffset]))
}

// BuildResponsesURLCitations extracts url_citation annotations from groundingMetadata groundingSupports.
// Optional text parameter enables converting UTF-8 byte offsets to Unicode character (rune) offsets.
func BuildResponsesURLCitations(groundingMetadata gjson.Result, text ...string) [][]byte {
	chunks := groundingMetadata.Get("groundingChunks").Array()
	supports := groundingMetadata.Get("groundingSupports").Array()
	if len(supports) == 0 || len(chunks) == 0 {
		return nil
	}
	var fullText string
	if len(text) > 0 {
		fullText = text[0]
	}
	var citations [][]byte
	type citationKey struct {
		url   string
		start int64
		end   int64
	}
	seen := make(map[citationKey]bool)

	for _, support := range supports {
		startIndex := support.Get("segment.startIndex").Int()
		endIndex := support.Get("segment.endIndex").Int()
		if fullText != "" {
			startIndex = byteOffsetToRuneOffset(fullText, startIndex)
			endIndex = byteOffsetToRuneOffset(fullText, endIndex)
		}
		if endIndex <= startIndex || startIndex < 0 {
			continue
		}
		indices := support.Get("groundingChunkIndices").Array()
		for _, idxResult := range indices {
			idx := int(idxResult.Int())
			if idx < 0 || idx >= len(chunks) {
				continue
			}
			chunk := chunks[idx]
			uri := strings.TrimSpace(chunk.Get("web.uri").String())
			title := strings.TrimSpace(chunk.Get("web.title").String())
			if uri == "" {
				continue
			}
			key := citationKey{url: uri, start: startIndex, end: endIndex}
			if seen[key] {
				continue
			}
			seen[key] = true

			cite := []byte(`{"type":"url_citation","url":"","title":"","start_index":0,"end_index":0}`)
			cite, _ = sjson.SetBytes(cite, "url", uri)
			cite, _ = sjson.SetBytes(cite, "title", title)
			cite, _ = sjson.SetBytes(cite, "start_index", startIndex)
			cite, _ = sjson.SetBytes(cite, "end_index", endIndex)
			citations = append(citations, cite)
		}
	}
	return citations
}
