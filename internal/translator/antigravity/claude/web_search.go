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

func antigravityNativeGoogleSearchModel(model string) string {
	return registry.AntigravityWebSearchModelFor(model)
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
	for _, chunk := range groundingChunks.Array() {
		web := chunk.Get("web")
		if !web.Exists() {
			continue
		}
		result := []byte(`{"type":"web_search_result","page_age":null}`)
		if title := web.Get("title"); title.Exists() {
			result, _ = sjson.SetBytes(result, "title", title.String())
		}
		if uri := web.Get("uri"); uri.Exists() {
			result, _ = sjson.SetBytes(result, "url", uri.String())
		}
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
		if support.StartIndex > lastEnd {
			start := int(lastEnd)
			end := min(int(support.StartIndex), len(textBytes))
			if start < end {
				blocks = append(blocks, webSearchCitedTextBlock{Text: string(textBytes[start:end])})
			}
		}
		if support.Text != "" && len(support.ChunkURLs) > 0 {
			citation := map[string]any{
				"type":       "web_search_result_location",
				"cited_text": support.Text,
				"url":        support.ChunkURLs[0],
				"title":      support.ChunkTitle,
			}
			blocks = append(blocks, webSearchCitedTextBlock{
				Text:      support.Text,
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
