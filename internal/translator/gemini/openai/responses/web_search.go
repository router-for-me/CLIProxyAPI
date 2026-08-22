package responses

import (
	"fmt"
	"strings"

	translatorcommon "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/common"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type openAIResponsesGroundingChunk struct {
	URL   string
	Title string
}

func isOpenAIResponsesWebSearchToolType(toolType string) bool {
	toolType = strings.ToLower(strings.TrimSpace(toolType))
	return toolType == "web_search" || strings.HasPrefix(toolType, "web_search_")
}

func hasOpenAIResponsesWebSearchTool(payload []byte) bool {
	if len(payload) == 0 || !gjson.ValidBytes(payload) {
		return false
	}
	root := gjson.ParseBytes(payload)
	for _, path := range []string{"tools", "request.tools"} {
		tools := root.Get(path)
		if !tools.IsArray() {
			continue
		}
		for _, tool := range tools.Array() {
			if isOpenAIResponsesWebSearchToolType(tool.Get("type").String()) {
				return true
			}
		}
	}
	return false
}

func hasGeminiGoogleSearchTool(payload []byte) bool {
	if len(payload) == 0 || !gjson.ValidBytes(payload) {
		return false
	}
	root := gjson.ParseBytes(payload)
	for _, path := range []string{"tools", "request.tools"} {
		tools := root.Get(path)
		if !tools.IsArray() {
			continue
		}
		for _, tool := range tools.Array() {
			if tool.Get("googleSearch").Exists() || tool.Get("google_search").Exists() {
				return true
			}
		}
	}
	return false
}

func shouldTranslateOpenAIResponsesWebSearch(originalRequestRawJSON, requestRawJSON []byte) bool {
	return hasOpenAIResponsesWebSearchTool(originalRequestRawJSON) ||
		hasOpenAIResponsesWebSearchTool(requestRawJSON) ||
		hasGeminiGoogleSearchTool(requestRawJSON)
}

func openAIResponsesGroundingChunks(groundingMetadata gjson.Result) []openAIResponsesGroundingChunk {
	chunksResult := groundingMetadata.Get("groundingChunks")
	if !chunksResult.IsArray() {
		return nil
	}
	chunks := make([]openAIResponsesGroundingChunk, 0, len(chunksResult.Array()))
	for _, chunk := range chunksResult.Array() {
		web := chunk.Get("web")
		chunks = append(chunks, openAIResponsesGroundingChunk{
			URL:   strings.TrimSpace(web.Get("uri").String()),
			Title: web.Get("title").String(),
		})
	}
	return chunks
}

func buildOpenAIResponsesWebSearchSources(groundingMetadata gjson.Result) [][]byte {
	seenURLs := make(map[string]bool)
	var sources [][]byte
	for _, chunk := range openAIResponsesGroundingChunks(groundingMetadata) {
		if chunk.URL == "" || seenURLs[chunk.URL] {
			continue
		}
		seenURLs[chunk.URL] = true
		source := []byte(`{"type":"url","url":""}`)
		source, _ = sjson.SetBytes(source, "url", chunk.URL)
		sources = append(sources, source)
	}
	return sources
}

func buildOpenAIResponsesWebSearchAnnotations(groundingMetadata gjson.Result) [][]byte {
	chunks := openAIResponsesGroundingChunks(groundingMetadata)
	if len(chunks) == 0 {
		return nil
	}
	supports := groundingMetadata.Get("groundingSupports")
	if !supports.IsArray() {
		return nil
	}

	seen := make(map[string]bool)
	var annotations [][]byte
	for _, support := range supports.Array() {
		segment := support.Get("segment")
		if !segment.Exists() {
			continue
		}
		startIndex := segment.Get("startIndex").Int()
		endIndex := segment.Get("endIndex").Int()
		if startIndex < 0 || endIndex <= startIndex {
			continue
		}
		chunkIndices := support.Get("groundingChunkIndices")
		if !chunkIndices.IsArray() {
			continue
		}
		for _, chunkIndexResult := range chunkIndices.Array() {
			chunkIndex := int(chunkIndexResult.Int())
			if chunkIndex < 0 || chunkIndex >= len(chunks) || chunks[chunkIndex].URL == "" {
				continue
			}
			chunk := chunks[chunkIndex]
			key := fmt.Sprintf("%d:%d:%s", startIndex, endIndex, chunk.URL)
			if seen[key] {
				continue
			}
			seen[key] = true
			annotation := []byte(`{"type":"url_citation","start_index":0,"end_index":0,"url":"","title":""}`)
			annotation, _ = sjson.SetBytes(annotation, "start_index", startIndex)
			annotation, _ = sjson.SetBytes(annotation, "end_index", endIndex)
			annotation, _ = sjson.SetBytes(annotation, "url", chunk.URL)
			annotation, _ = sjson.SetBytes(annotation, "title", chunk.Title)
			annotations = append(annotations, annotation)
		}
	}
	return annotations
}

func openAIResponsesWebSearchAnnotationsForRange(annotations [][]byte, startIndex, endIndex int64) [][]byte {
	if len(annotations) == 0 || endIndex <= startIndex {
		return nil
	}
	var ranged [][]byte
	for _, annotation := range annotations {
		result := gjson.ParseBytes(annotation)
		annotationStart := result.Get("start_index").Int()
		annotationEnd := result.Get("end_index").Int()
		if annotationStart < startIndex || annotationEnd > endIndex || annotationEnd <= annotationStart {
			continue
		}
		adjusted := append([]byte(nil), annotation...)
		adjusted, _ = sjson.SetBytes(adjusted, "start_index", annotationStart-startIndex)
		adjusted, _ = sjson.SetBytes(adjusted, "end_index", annotationEnd-startIndex)
		ranged = append(ranged, adjusted)
	}
	return ranged
}

func buildOpenAIResponsesWebSearchCallItem(responseID string, groundingMetadata gjson.Result) []byte {
	itemIDBase := strings.TrimPrefix(responseID, "resp_")
	if itemIDBase == "" {
		itemIDBase = "search"
	}
	item := []byte(`{"id":"","type":"web_search_call","status":"completed","action":{"type":"search","query":"","sources":[]}}`)
	item, _ = sjson.SetBytes(item, "id", fmt.Sprintf("ws_%s_0", itemIDBase))
	if queries := groundingMetadata.Get("webSearchQueries"); queries.IsArray() {
		for _, query := range queries.Array() {
			if value := strings.TrimSpace(query.String()); value != "" {
				item, _ = sjson.SetBytes(item, "action.query", value)
				break
			}
		}
	}
	if sources := buildOpenAIResponsesWebSearchSources(groundingMetadata); len(sources) > 0 {
		item, _ = sjson.SetRawBytes(item, "action.sources", translatorcommon.JoinRawArray(sources))
	}
	return item
}

func bufferGeminiResponsesWebSearchStreamChunk(st *geminiToResponsesState, root gjson.Result) (gjson.Result, bool) {
	if parts := root.Get("candidates.0.content.parts"); parts.IsArray() {
		for _, part := range parts.Array() {
			st.WebSearchBufferedParts = append(st.WebSearchBufferedParts, []byte(part.Raw))
		}
	}
	if value := root.Get("responseId"); value.Exists() && value.String() != "" {
		st.WebSearchResponseID = value.String()
	}
	if value := root.Get("createTime"); value.Exists() && value.String() != "" {
		st.WebSearchCreateTime = value.String()
	}
	if value := root.Get("modelVersion"); value.Exists() && value.String() != "" {
		st.WebSearchModelVersion = value.String()
	}
	if value := root.Get("usageMetadata"); value.Exists() {
		st.WebSearchUsageMetadata = []byte(value.Raw)
	}
	if value := root.Get("candidates.0.groundingMetadata"); value.Exists() {
		st.WebSearchGroundingMetadata = []byte(value.Raw)
	}

	finishReason := root.Get("candidates.0.finishReason").String()
	if finishReason == "" {
		return gjson.Result{}, false
	}

	payload := []byte(`{"candidates":[{"content":{"parts":[]},"finishReason":""}]}`)
	payload, _ = sjson.SetBytes(payload, "candidates.0.finishReason", finishReason)
	if len(st.WebSearchBufferedParts) > 0 {
		payload, _ = sjson.SetRawBytes(payload, "candidates.0.content.parts", translatorcommon.JoinRawArray(st.WebSearchBufferedParts))
	}
	if st.WebSearchResponseID != "" {
		payload, _ = sjson.SetBytes(payload, "responseId", st.WebSearchResponseID)
	}
	if st.WebSearchCreateTime != "" {
		payload, _ = sjson.SetBytes(payload, "createTime", st.WebSearchCreateTime)
	}
	if st.WebSearchModelVersion != "" {
		payload, _ = sjson.SetBytes(payload, "modelVersion", st.WebSearchModelVersion)
	}
	if len(st.WebSearchUsageMetadata) > 0 {
		payload, _ = sjson.SetRawBytes(payload, "usageMetadata", st.WebSearchUsageMetadata)
	}
	if len(st.WebSearchGroundingMetadata) > 0 {
		payload, _ = sjson.SetRawBytes(payload, "candidates.0.groundingMetadata", st.WebSearchGroundingMetadata)
	}
	return gjson.ParseBytes(payload), true
}
