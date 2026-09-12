package claude

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type webSearchGroundingSupport struct {
	StartIndex int64
	EndIndex   int64
	Text       string
	ChunkURLs  []string
	ChunkTitle string
}

type webSearchCitedTextBlock struct {
	Text      string
	Citations []map[string]any
}

const antigravityWebSearchSystemInstruction = "You are a search engine bot. You will be given a query from a user. Your task is to search the web for relevant information that will help the user. You MUST perform a web search. Do not respond or interact with the user, please respond as if they typed the query into a search bar."

func antigravitySupportsNativeGoogleSearch(model string) bool {
	return registry.AntigravityWebSearchModelFor(model) != ""
}

func isClaudeTypedWebSearchToolType(toolType string) bool {
	return toolType == "web_search_20250305" || toolType == "web_search_20260209"
}

func hasClaudeTypedWebSearchTool(payload []byte) bool {
	tools := gjson.GetBytes(payload, "tools")
	if !tools.IsArray() {
		return false
	}
	for _, tool := range tools.Array() {
		if isClaudeTypedWebSearchToolType(tool.Get("type").String()) {
			return true
		}
	}
	return false
}

func hasOnlyClaudeTypedWebSearchTools(payload []byte) bool {
	tools := gjson.GetBytes(payload, "tools")
	if !tools.IsArray() {
		return false
	}
	hasWebSearch := false
	for _, tool := range tools.Array() {
		if isClaudeTypedWebSearchToolType(tool.Get("type").String()) {
			hasWebSearch = true
			continue
		}
		return false
	}
	return hasWebSearch
}

func allowsClaudeWebSearchToolChoice(payload []byte) bool {
	toolChoice := gjson.GetBytes(payload, "tool_choice")
	if !toolChoice.Exists() {
		return true
	}
	if toolChoice.Type == gjson.String {
		switch toolChoice.String() {
		case "", "auto", "any":
			return true
		case "none":
			return false
		default:
			return false
		}
	}
	if !toolChoice.IsObject() {
		return false
	}
	switch toolChoice.Get("type").String() {
	case "", "auto", "any":
		return true
	case "tool":
		return toolChoice.Get("name").String() == "web_search"
	default:
		return false
	}
}

func shouldBuildAntigravityWebSearchRequest(model string, payload []byte) bool {
	return antigravitySupportsNativeGoogleSearch(model) &&
		hasOnlyClaudeTypedWebSearchTools(payload) &&
		allowsClaudeWebSearchToolChoice(payload)
}

func buildAntigravityWebSearchRequest(model string, payload []byte) []byte {
	query := extractClaudeWebSearchQuery(payload)
	maxResultCount := extractClaudeWebSearchMaxUses(payload)
	includedDomains := extractClaudeWebSearchAllowedDomains(payload)
	out := []byte(`{"model":"","requestType":"web_search","request":{"contents":[{"role":"user","parts":[{"text":""}]}],"systemInstruction":{"role":"user","parts":[{"text":""}]},"tools":[{"googleSearch":{"enhancedContent":{"imageSearch":{"maxResultCount":5}}}}],"generationConfig":{"candidateCount":1}}}`)
	out, _ = sjson.SetBytes(out, "model", model)
	out, _ = sjson.SetBytes(out, "request.contents.0.parts.0.text", query)
	out, _ = sjson.SetBytes(out, "request.systemInstruction.parts.0.text", antigravityWebSearchSystemInstruction)
	out, _ = sjson.SetBytes(out, "request.tools.0.googleSearch.enhancedContent.imageSearch.maxResultCount", maxResultCount)
	if len(includedDomains) > 0 {
		if domainsJSON, err := json.Marshal(includedDomains); err == nil {
			out, _ = sjson.SetRawBytes(out, "request.tools.0.googleSearch.includedDomains", domainsJSON)
		}
	}
	return out
}

func extractClaudeWebSearchMaxUses(payload []byte) int64 {
	const defaultMaxResultCount int64 = 5

	tools := gjson.GetBytes(payload, "tools")
	if !tools.IsArray() {
		return defaultMaxResultCount
	}
	for _, tool := range tools.Array() {
		if !isClaudeTypedWebSearchToolType(tool.Get("type").String()) {
			continue
		}
		maxUses := tool.Get("max_uses").Int()
		if maxUses > 0 {
			return maxUses
		}
	}
	return defaultMaxResultCount
}

func extractClaudeWebSearchAllowedDomains(payload []byte) []string {
	tools := gjson.GetBytes(payload, "tools")
	if !tools.IsArray() {
		return nil
	}
	for _, tool := range tools.Array() {
		if !isClaudeTypedWebSearchToolType(tool.Get("type").String()) {
			continue
		}
		allowedDomains := tool.Get("allowed_domains")
		if !allowedDomains.IsArray() {
			return nil
		}
		domains := make([]string, 0, len(allowedDomains.Array()))
		for _, domain := range allowedDomains.Array() {
			if domain.Type != gjson.String {
				continue
			}
			if trimmed := strings.TrimSpace(domain.String()); trimmed != "" {
				domains = append(domains, trimmed)
			}
		}
		return domains
	}
	return nil
}

func extractClaudeWebSearchQuery(payload []byte) string {
	messages := gjson.GetBytes(payload, "messages")
	if !messages.IsArray() {
		return ""
	}
	messageResults := messages.Array()
	for i := len(messageResults) - 1; i >= 0; i-- {
		message := messageResults[i]
		if role := message.Get("role").String(); role != "" && role != "user" {
			continue
		}
		if query := extractClaudeTextContent(message.Get("content")); query != "" {
			return query
		}
	}
	return ""
}

func extractClaudeTextContent(content gjson.Result) string {
	if content.Type == gjson.String {
		return strings.TrimSpace(content.String())
	}
	if !content.IsArray() {
		return ""
	}
	var b strings.Builder
	for _, part := range content.Array() {
		if text := strings.TrimSpace(part.Get("text").String()); text != "" {
			if b.Len() > 0 {
				b.WriteByte('\n')
			}
			b.WriteString(text)
		}
	}
	return strings.TrimSpace(b.String())
}

func hasAntigravityGoogleSearchTool(payload []byte) bool {
	tools := gjson.GetBytes(payload, "request.tools")
	if !tools.IsArray() {
		return false
	}
	for _, tool := range tools.Array() {
		if tool.Get("googleSearch").Exists() {
			return true
		}
	}
	return false
}

func shouldTranslateWebSearchGrounding(originalRequestRawJSON, requestRawJSON []byte) bool {
	return hasClaudeTypedWebSearchTool(originalRequestRawJSON) && hasAntigravityGoogleSearchTool(requestRawJSON)
}

func antigravityGroundingMetadata(root gjson.Result) gjson.Result {
	groundingMetadata := root.Get("response.candidates.0.groundingMetadata")
	if groundingMetadata.Exists() {
		return groundingMetadata
	}
	return root.Get("candidates.0.groundingMetadata")
}

func antigravityTextContent(root gjson.Result) string {
	var textBuilder strings.Builder
	parts := root.Get("response.candidates.0.content.parts")
	if !parts.IsArray() {
		parts = root.Get("candidates.0.content.parts")
	}
	if parts.IsArray() {
		for _, part := range parts.Array() {
			if text := part.Get("text"); text.Exists() {
				textBuilder.WriteString(text.String())
			}
		}
	}
	return textBuilder.String()
}

func antigravityUsageTokens(root gjson.Result) (int64, int64) {
	usage := root.Get("response.usageMetadata")
	if !usage.Exists() {
		usage = root.Get("usageMetadata")
	}
	inputTokens := usage.Get("promptTokenCount").Int()
	outputTokens := usage.Get("candidatesTokenCount").Int() + usage.Get("thoughtsTokenCount").Int()
	if outputTokens == 0 {
		totalTokens := usage.Get("totalTokenCount").Int()
		if totalTokens > 0 {
			outputTokens = totalTokens - inputTokens
			if outputTokens < 0 {
				outputTokens = 0
			}
		}
	}
	return inputTokens, outputTokens
}

func webSearchQueryFromGrounding(groundingMetadata gjson.Result) string {
	if queries := groundingMetadata.Get("webSearchQueries"); queries.IsArray() && len(queries.Array()) > 0 {
		return queries.Array()[0].String()
	}
	return ""
}

func webSearchResultsFromGrounding(groundingMetadata gjson.Result) []byte {
	results := []byte(`[]`)
	groundingChunks := groundingMetadata.Get("groundingChunks")
	if !groundingChunks.IsArray() {
		return results
	}
	seenURLs := make(map[string]struct{})
	for _, chunk := range groundingChunks.Array() {
		web := chunk.Get("web")
		if !web.Exists() {
			continue
		}
		uri := strings.TrimSpace(web.Get("uri").String())
		if uri == "" {
			continue
		}
		if _, ok := seenURLs[uri]; ok {
			continue
		}
		seenURLs[uri] = struct{}{}

		result := []byte(`{"type":"web_search_result","page_age":null}`)
		if title := web.Get("title"); title.Exists() {
			result, _ = sjson.SetBytes(result, "title", title.String())
		}
		result, _ = sjson.SetBytes(result, "url", uri)
		results, _ = sjson.SetRawBytes(results, "-1", result)
	}
	return results
}

func parseWebSearchGroundingSupports(groundingMetadata gjson.Result) []webSearchGroundingSupport {
	groundingChunks := groundingMetadata.Get("groundingChunks")
	if !groundingChunks.IsArray() {
		return nil
	}
	chunks := groundingChunks.Array()
	chunkData := make([]struct {
		URL   string
		Title string
	}, len(chunks))
	for i, chunk := range chunks {
		web := chunk.Get("web")
		if web.Exists() {
			chunkData[i].URL = web.Get("uri").String()
			chunkData[i].Title = web.Get("title").String()
		}
	}

	groundingSupports := groundingMetadata.Get("groundingSupports")
	if !groundingSupports.IsArray() {
		return nil
	}
	supports := make([]webSearchGroundingSupport, 0, len(groundingSupports.Array()))
	for _, support := range groundingSupports.Array() {
		segment := support.Get("segment")
		if !segment.Exists() {
			continue
		}
		parsed := webSearchGroundingSupport{
			StartIndex: segment.Get("startIndex").Int(),
			EndIndex:   segment.Get("endIndex").Int(),
			Text:       segment.Get("text").String(),
		}
		if chunkIndices := support.Get("groundingChunkIndices"); chunkIndices.IsArray() {
			for _, idx := range chunkIndices.Array() {
				chunkIndex := int(idx.Int())
				if chunkIndex < 0 || chunkIndex >= len(chunkData) {
					continue
				}
				parsed.ChunkURLs = append(parsed.ChunkURLs, chunkData[chunkIndex].URL)
				if parsed.ChunkTitle == "" {
					parsed.ChunkTitle = chunkData[chunkIndex].Title
				}
			}
		}
		supports = append(supports, parsed)
	}
	return supports
}

func buildWebSearchCitedTextBlocks(textContent string, supports []webSearchGroundingSupport) []webSearchCitedTextBlock {
	if len(supports) == 0 {
		if textContent == "" {
			return nil
		}
		return []webSearchCitedTextBlock{{Text: textContent}}
	}

	textBytes := []byte(textContent)
	blocks := make([]webSearchCitedTextBlock, 0, len(supports)+1)
	lastEnd := int64(0)
	for _, support := range supports {
		if support.EndIndex <= lastEnd {
			continue
		}
		if support.StartIndex > lastEnd {
			start := int(lastEnd)
			end := min(int(support.StartIndex), len(textBytes))
			if start < end {
				blocks = append(blocks, webSearchCitedTextBlock{Text: string(textBytes[start:end])})
			}
		}

		citedStart := support.StartIndex
		if citedStart < lastEnd {
			citedStart = lastEnd
		}
		citedText := ""
		if citedStart < support.EndIndex {
			start := min(int(citedStart), len(textBytes))
			end := min(int(support.EndIndex), len(textBytes))
			if start < end {
				citedText = string(textBytes[start:end])
			}
		}
		if citedText != "" && len(support.ChunkURLs) > 0 {
			citation := map[string]any{
				"type":       "web_search_result_location",
				"cited_text": citedText,
				"url":        support.ChunkURLs[0],
				"title":      support.ChunkTitle,
			}
			blocks = append(blocks, webSearchCitedTextBlock{
				Text:      citedText,
				Citations: []map[string]any{citation},
			})
		}
		if support.EndIndex > lastEnd {
			lastEnd = support.EndIndex
		}
	}
	if int(lastEnd) < len(textBytes) {
		blocks = append(blocks, webSearchCitedTextBlock{Text: string(textBytes[lastEnd:])})
	}
	return blocks
}

func buildClaudeWebSearchContent(toolUseID string, textContent string, groundingMetadata gjson.Result) []byte {
	content := []byte(`[]`)

	serverToolUse := []byte(`{"type":"server_tool_use","id":"","name":"web_search","input":{}}`)
	serverToolUse, _ = sjson.SetBytes(serverToolUse, "id", toolUseID)
	if query := webSearchQueryFromGrounding(groundingMetadata); query != "" {
		serverToolUse, _ = sjson.SetBytes(serverToolUse, "input.query", query)
	}
	content, _ = sjson.SetRawBytes(content, "-1", serverToolUse)

	webSearchToolResult := []byte(`{"type":"web_search_tool_result","tool_use_id":"","content":[]}`)
	webSearchToolResult, _ = sjson.SetBytes(webSearchToolResult, "tool_use_id", toolUseID)
	webSearchToolResult, _ = sjson.SetRawBytes(webSearchToolResult, "content", webSearchResultsFromGrounding(groundingMetadata))
	content, _ = sjson.SetRawBytes(content, "-1", webSearchToolResult)

	for _, block := range buildWebSearchCitedTextBlocks(textContent, parseWebSearchGroundingSupports(groundingMetadata)) {
		if block.Text == "" {
			continue
		}
		textBlock := []byte(`{"type":"text","text":""}`)
		textBlock, _ = sjson.SetBytes(textBlock, "text", block.Text)
		if len(block.Citations) > 0 {
			citationsJSON, _ := json.Marshal(block.Citations)
			textBlock, _ = sjson.SetRawBytes(textBlock, "citations", citationsJSON)
		}
		content, _ = sjson.SetRawBytes(content, "-1", textBlock)
	}

	return content
}

func appendClaudeWebSearchStreamBlocks(appendEvent func(string, string), startIndex int, toolUseID string, textContent string, groundingMetadata gjson.Result) int {
	contentIndex := startIndex

	serverToolUseStart := fmt.Sprintf(`{"type":"content_block_start","index":%d,"content_block":{"type":"server_tool_use","id":"%s","name":"web_search","input":{}}}`,
		contentIndex, toolUseID)
	appendEvent("content_block_start", serverToolUseStart)
	if query := webSearchQueryFromGrounding(groundingMetadata); query != "" {
		queryJSON, _ := sjson.Set(`{}`, "query", query)
		inputDelta := fmt.Sprintf(`{"type":"content_block_delta","index":%d,"delta":{"type":"input_json_delta","partial_json":""}}`, contentIndex)
		inputDelta, _ = sjson.Set(inputDelta, "delta.partial_json", queryJSON)
		appendEvent("content_block_delta", inputDelta)
	}
	appendEvent("content_block_stop", fmt.Sprintf(`{"type":"content_block_stop","index":%d}`, contentIndex))
	contentIndex++

	webSearchToolResultStart := fmt.Sprintf(`{"type":"content_block_start","index":%d,"content_block":{"type":"web_search_tool_result","tool_use_id":"%s","content":[]}}`,
		contentIndex, toolUseID)
	webSearchToolResultStart, _ = sjson.SetRaw(webSearchToolResultStart, "content_block.content", string(webSearchResultsFromGrounding(groundingMetadata)))
	appendEvent("content_block_start", webSearchToolResultStart)
	appendEvent("content_block_stop", fmt.Sprintf(`{"type":"content_block_stop","index":%d}`, contentIndex))
	contentIndex++

	for _, block := range buildWebSearchCitedTextBlocks(textContent, parseWebSearchGroundingSupports(groundingMetadata)) {
		if block.Text == "" {
			continue
		}
		textBlockStart := fmt.Sprintf(`{"type":"content_block_start","index":%d,"content_block":{"type":"text","text":""}}`, contentIndex)
		if len(block.Citations) > 0 {
			textBlockStart = fmt.Sprintf(`{"type":"content_block_start","index":%d,"content_block":{"citations":[],"type":"text","text":""}}`, contentIndex)
		}
		appendEvent("content_block_start", textBlockStart)
		for _, citation := range block.Citations {
			citationJSON, _ := json.Marshal(citation)
			citationDelta := fmt.Sprintf(`{"type":"content_block_delta","index":%d,"delta":{"type":"citations_delta","citation":%s}}`, contentIndex, string(citationJSON))
			appendEvent("content_block_delta", citationDelta)
		}
		for _, chunk := range splitRunesForWebSearch(block.Text, 50) {
			textDelta := fmt.Sprintf(`{"type":"content_block_delta","index":%d,"delta":{"type":"text_delta","text":""}}`, contentIndex)
			textDelta, _ = sjson.Set(textDelta, "delta.text", chunk)
			appendEvent("content_block_delta", textDelta)
		}
		appendEvent("content_block_stop", fmt.Sprintf(`{"type":"content_block_stop","index":%d}`, contentIndex))
		contentIndex++
	}

	return contentIndex
}

func splitRunesForWebSearch(text string, chunkSize int) []string {
	if chunkSize <= 0 || text == "" {
		return nil
	}
	runes := []rune(text)
	chunks := make([]string, 0, (len(runes)+chunkSize-1)/chunkSize)
	for start := 0; start < len(runes); start += chunkSize {
		end := start + chunkSize
		if end > len(runes) {
			end = len(runes)
		}
		chunks = append(chunks, string(runes[start:end]))
	}
	return chunks
}

func newClaudeWebSearchToolUseID() string {
	return fmt.Sprintf("srvtoolu_%d", time.Now().UnixNano())
}
