package websearch

import (
	"fmt"
	"sync/atomic"
	"time"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

var groundingIDCounter uint64

type GroundingChunk struct {
	URL   string `json:"url"`
	Title string `json:"title"`
}

type GroundingMetadata struct {
	WebSearchQueries []string         `json:"web_search_queries"`
	GroundingChunks  []GroundingChunk `json:"grounding_chunks"`
}

func ExtractGroundingMetadata(rawJSON []byte) *GroundingMetadata {
	metadata := gjson.GetBytes(rawJSON, "candidates.0.groundingMetadata")
	if !metadata.Exists() {
		return nil
	}

	result := &GroundingMetadata{}

	queries := metadata.Get("webSearchQueries")
	if queries.IsArray() {
		for _, q := range queries.Array() {
			result.WebSearchQueries = append(result.WebSearchQueries, q.String())
		}
	}

	chunks := metadata.Get("groundingChunks")
	if chunks.IsArray() {
		for _, chunk := range chunks.Array() {
			web := chunk.Get("web")
			if web.Exists() {
				result.GroundingChunks = append(result.GroundingChunks, GroundingChunk{
					URL:   web.Get("uri").String(),
					Title: web.Get("title").String(),
				})
			}
		}
	}

	return result
}

func InjectGroundingToOpenAIResponse(template string, metadata *GroundingMetadata) string {
	if metadata == nil || len(metadata.GroundingChunks) == 0 {
		return template
	}

	toolUseID := fmt.Sprintf("srvtoolu_%d_%d", time.Now().UnixNano(), atomic.AddUint64(&groundingIDCounter, 1))

	var searchResults []map[string]interface{}
	for _, chunk := range metadata.GroundingChunks {
		if chunk.URL != "" {
			searchResults = append(searchResults, map[string]interface{}{
				"url":               chunk.URL,
				"title":             chunk.Title,
				"encrypted_content": "",
				"page_age":          nil,
			})
		}
	}

	if len(searchResults) == 0 {
		return template
	}

	toolCallsResult := gjson.Get(template, "choices.0.message.tool_calls")
	if !toolCallsResult.Exists() || !toolCallsResult.IsArray() {
		template, _ = sjson.SetRaw(template, "choices.0.message.tool_calls", `[]`)
	}

	webSearchToolCall := fmt.Sprintf(`{"id":"%s","type":"function","function":{"name":"web_search","arguments":"{}"}}`, toolUseID)
	template, _ = sjson.SetRaw(template, "choices.0.message.tool_calls.-1", webSearchToolCall)

	template, _ = sjson.Set(template, "choices.0.grounding_metadata.web_search_queries", metadata.WebSearchQueries)
	template, _ = sjson.Set(template, "choices.0.grounding_metadata.search_results", searchResults)

	return template
}

func InjectGroundingToOpenAIStreamResponse(template string, metadata *GroundingMetadata) string {
	if metadata == nil || len(metadata.GroundingChunks) == 0 {
		return template
	}

	toolUseID := fmt.Sprintf("srvtoolu_%d_%d", time.Now().UnixNano(), atomic.AddUint64(&groundingIDCounter, 1))

	var searchResults []map[string]interface{}
	for _, chunk := range metadata.GroundingChunks {
		if chunk.URL != "" {
			searchResults = append(searchResults, map[string]interface{}{
				"url":               chunk.URL,
				"title":             chunk.Title,
				"encrypted_content": "",
				"page_age":          nil,
			})
		}
	}

	if len(searchResults) == 0 {
		return template
	}

	toolCallsResult := gjson.Get(template, "choices.0.delta.tool_calls")
	if !toolCallsResult.Exists() || !toolCallsResult.IsArray() {
		template, _ = sjson.SetRaw(template, "choices.0.delta.tool_calls", `[]`)
	}

	webSearchToolCall := fmt.Sprintf(`{"id":"%s","index":0,"type":"function","function":{"name":"web_search","arguments":"{}"}}`, toolUseID)
	template, _ = sjson.SetRaw(template, "choices.0.delta.tool_calls.-1", webSearchToolCall)

	template, _ = sjson.Set(template, "choices.0.grounding_metadata.web_search_queries", metadata.WebSearchQueries)
	template, _ = sjson.Set(template, "choices.0.grounding_metadata.search_results", searchResults)

	return template
}
