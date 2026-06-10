package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	defaultWebSearchBackendModel = "gemini-3.1-flash-lite"
	antigravityPath              = "/v1internal:generateContent"
	metadataWebSearchModelIDs    = "antigravity_web_search_model_ids"
	metadataWebSearchBackend     = "antigravity_web_search_backend_model"
)

type rpcHostHTTPRequest struct {
	HostCallbackID string              `json:"host_callback_id,omitempty"`
	Method         string              `json:"method,omitempty"`
	URL            string              `json:"url,omitempty"`
	Headers        map[string][]string `json:"headers,omitempty"`
	Body           []byte              `json:"body,omitempty"`
}

type hostHTTPResponseEnvelope struct {
	OK     bool                   `json:"ok"`
	Result pluginapi.HTTPResponse `json:"result,omitempty"`
	Error  *envelopeError         `json:"error,omitempty"`
}

type groundingSupport struct {
	StartIndex int64
	EndIndex   int64
	Text       string
	ChunkURLs  []string
	ChunkTitle string
}

type citedTextBlock struct {
	Text      string
	Citations []map[string]any
}

func shouldHandleServerTool(req pluginapi.ServerToolRequest, cfg pluginConfig) bool {
	cfg = normalizePluginConfig(cfg)
	if provider := strings.ToLower(strings.TrimSpace(req.Provider)); provider != "" && provider != "antigravity" {
		return false
	}
	if isNativeGoogleSearchModel(req.UpstreamModel, req, cfg) || isNativeGoogleSearchModel(req.RouteModel, req, cfg) {
		return false
	}
	sourceFormat := strings.ToLower(strings.TrimSpace(req.SourceFormat))
	if sourceFormat != "claude" && sourceFormat != "anthropic" {
		return false
	}
	tools := gjson.GetBytes(req.Payload, "tools")
	if !tools.IsArray() {
		return false
	}
	var sawWebSearch bool
	for _, tool := range tools.Array() {
		toolType := tool.Get("type").String()
		if !isClaudeWebSearchToolType(toolType) {
			return false
		}
		if maxUses := int(tool.Get("max_uses").Int()); cfg.MaxUses > 0 && maxUses > cfg.MaxUses {
			return false
		}
		sawWebSearch = true
	}
	return sawWebSearch
}

func isClaudeWebSearchToolType(toolType string) bool {
	return toolType == "web_search_20250305" || toolType == "web_search_20260209"
}

func isNativeGoogleSearchModel(model string, req pluginapi.ServerToolRequest, cfg pluginConfig) bool {
	model = normalizeModelID(model)
	if model == "" {
		return false
	}
	for _, candidate := range webSearchBackendModels(req, cfg) {
		if model == normalizeModelID(candidate) {
			return true
		}
	}
	return false
}

func webSearchBackendModel(req pluginapi.ServerToolRequest, cfg pluginConfig) string {
	models := webSearchBackendModels(req, cfg)
	if len(models) == 0 {
		return normalizePluginConfig(cfg).BackendModel
	}
	return models[0]
}

func webSearchBackendModels(req pluginapi.ServerToolRequest, cfg pluginConfig) []string {
	models := metadataStringSlice(req.Metadata, metadataWebSearchModelIDs)
	if backend := metadataString(req.Metadata, metadataWebSearchBackend); backend != "" {
		models = append([]string{backend}, models...)
	}
	if len(models) == 0 {
		models = append(models, normalizePluginConfig(cfg).BackendModel)
	}
	out := make([]string, 0, len(models))
	seen := make(map[string]struct{}, len(models))
	for _, model := range models {
		normalized := normalizeModelID(model)
		if normalized == "" {
			continue
		}
		if _, exists := seen[normalized]; exists {
			continue
		}
		seen[normalized] = struct{}{}
		out = append(out, normalized)
	}
	return out
}

func normalizeModelID(model string) string {
	model = strings.ToLower(strings.TrimSpace(model))
	if open := strings.LastIndex(model, "("); open >= 0 && strings.HasSuffix(model, ")") {
		model = strings.TrimSpace(model[:open])
	}
	return model
}

func metadataString(metadata map[string]any, key string) string {
	if len(metadata) == 0 {
		return ""
	}
	value, ok := metadata[key]
	if !ok || value == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func metadataStringSlice(metadata map[string]any, key string) []string {
	if len(metadata) == 0 {
		return nil
	}
	value, ok := metadata[key]
	if !ok || value == nil {
		return nil
	}
	switch typed := value.(type) {
	case []string:
		return append([]string(nil), typed...)
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if text := strings.TrimSpace(fmt.Sprint(item)); text != "" {
				out = append(out, text)
			}
		}
		return out
	case string:
		if strings.TrimSpace(typed) == "" {
			return nil
		}
		return []string{typed}
	default:
		return nil
	}
}

func extractUserQuery(payload []byte) string {
	messages := gjson.GetBytes(payload, "messages")
	if !messages.IsArray() {
		return ""
	}
	arr := messages.Array()
	for i := len(arr) - 1; i >= 0; i-- {
		msg := arr[i]
		if msg.Get("role").String() != "user" {
			continue
		}
		content := msg.Get("content")
		if content.Type == gjson.String {
			return normalizeSearchQuery(content.String())
		}
		if content.IsArray() {
			for _, item := range content.Array() {
				if item.Get("type").String() == "text" {
					return normalizeSearchQuery(item.Get("text").String())
				}
			}
		}
	}
	return ""
}

func normalizeSearchQuery(raw string) string {
	raw = strings.TrimSpace(raw)
	const prefix = "perform a web search for the query:"
	if strings.HasPrefix(strings.ToLower(raw), prefix) {
		return strings.TrimSpace(raw[len(prefix):])
	}
	return raw
}

func executeAntigravityWebSearch(req rpcServerToolRequest, query string, cfg pluginConfig) ([]byte, error) {
	payload, errPayload := buildGeminiWebSearchPayload(query, webSearchBackendModel(req.ServerToolRequest, cfg), projectIDFromRequest(req.ServerToolRequest))
	if errPayload != nil {
		return nil, errPayload
	}
	var lastErr error
	for _, baseURL := range cfg.BaseURLs {
		hostReq := rpcHostHTTPRequest{
			HostCallbackID: req.HostCallbackID,
			Method:         http.MethodPost,
			URL:            strings.TrimRight(baseURL, "/") + antigravityPath,
			Headers: map[string][]string{
				"Content-Type": {"application/json"},
				"Accept":       {"application/json"},
			},
			Body: payload,
		}
		rawReq, errMarshal := json.Marshal(hostReq)
		if errMarshal != nil {
			return nil, errMarshal
		}
		rawResp, errCall := callHost(pluginabi.MethodHostHTTPDo, rawReq)
		if errCall != nil {
			lastErr = errCall
			continue
		}
		var resp hostHTTPResponseEnvelope
		if errUnmarshal := json.Unmarshal(rawResp, &resp); errUnmarshal != nil {
			lastErr = errUnmarshal
			continue
		}
		if !resp.OK {
			if resp.Error != nil {
				lastErr = fmt.Errorf("%s", resp.Error.Message)
			} else {
				lastErr = fmt.Errorf("host http callback failed")
			}
			continue
		}
		if resp.Result.StatusCode < http.StatusOK || resp.Result.StatusCode >= http.StatusMultipleChoices {
			lastErr = fmt.Errorf("antigravity web search status %d: %s", resp.Result.StatusCode, strings.TrimSpace(string(resp.Result.Body)))
			continue
		}
		return resp.Result.Body, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no antigravity base URL configured")
	}
	return nil, lastErr
}

func buildGeminiWebSearchPayload(query, backendModel, projectID string) ([]byte, error) {
	payload := `{"model":"","request":{"contents":[{"role":"user","parts":[{"text":""}]}],"tools":[{"googleSearch":{}}],"generationConfig":{"candidateCount":1},"systemInstruction":{"role":"user","parts":[{"text":"You are a search engine bot. You will be given a query from a user. Your task is to search the web for relevant information that will help the user. You MUST perform a web search. Do not respond or interact with the user, please respond as if they typed the query into a search bar."}]}},"requestType":"web_search","project":""}`
	payload, _ = sjson.Set(payload, "model", backendModel)
	payload, _ = sjson.Set(payload, "request.contents.0.parts.0.text", query)
	payload, _ = sjson.Set(payload, "project", projectID)
	return []byte(payload), nil
}

func projectIDFromRequest(req pluginapi.ServerToolRequest) string {
	if req.AuthMetadata != nil {
		if raw, ok := req.AuthMetadata["project_id"]; ok {
			if projectID := strings.TrimSpace(fmt.Sprint(raw)); projectID != "" {
				return projectID
			}
		}
	}
	return "web-search-" + strings.ToLower(randomHex(4))
}

func convertGeminiToClaudeNonStream(model string, geminiResp []byte) string {
	textContent := geminiTextContent(geminiResp)
	groundingMetadata := geminiGroundingMetadata(geminiResp)
	inputTokens, outputTokens := geminiUsage(geminiResp)
	toolUseID := "srvtoolu_" + randomHex(10)

	content := []map[string]any{
		{
			"type":  "server_tool_use",
			"id":    toolUseID,
			"name":  "web_search",
			"input": map[string]any{"query": searchQueryFromGrounding(groundingMetadata)},
		},
		{
			"type":        "web_search_tool_result",
			"tool_use_id": toolUseID,
			"content":     webSearchResultsFromGrounding(groundingMetadata),
		},
	}
	for _, block := range buildCitedTextBlocks(textContent, parseGroundingSupports(groundingMetadata)) {
		textBlock := map[string]any{
			"type": "text",
			"text": block.Text,
		}
		if block.Citations != nil {
			textBlock["citations"] = block.Citations
		}
		content = append(content, textBlock)
	}

	response := map[string]any{
		"id":            "msg_" + randomHex(12),
		"type":          "message",
		"role":          "assistant",
		"content":       content,
		"model":         model,
		"stop_reason":   "end_turn",
		"stop_sequence": nil,
		"usage": map[string]any{
			"input_tokens":  inputTokens,
			"output_tokens": outputTokens,
			"server_tool_use": map[string]any{
				"web_search_requests": 1,
			},
		},
	}
	respJSON, _ := json.Marshal(response)
	return string(respJSON)
}

func convertGeminiToClaudeSSEStream(model string, geminiResp []byte) []string {
	var events []string
	textContent := geminiTextContent(geminiResp)
	groundingMetadata := geminiGroundingMetadata(geminiResp)
	inputTokens, outputTokens := geminiUsage(geminiResp)
	msgID := "msg_" + randomHex(12)
	toolUseID := "srvtoolu_" + randomHex(10)

	messageStart := fmt.Sprintf(`{"type":"message_start","message":{"id":"%s","type":"message","role":"assistant","content":[],"model":"%s","stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":%d,"output_tokens":0}}}`,
		msgID, model, inputTokens)
	events = append(events, sse("message_start", messageStart))

	contentIndex := 0
	serverToolUseStart := fmt.Sprintf(`{"type":"content_block_start","index":%d,"content_block":{"type":"server_tool_use","id":"%s","name":"web_search","input":{}}}`,
		contentIndex, toolUseID)
	events = append(events, sse("content_block_start", serverToolUseStart))
	if searchQuery := searchQueryFromGrounding(groundingMetadata); searchQuery != "" {
		queryJSON, _ := sjson.Set(`{}`, "query", searchQuery)
		inputDelta := fmt.Sprintf(`{"type":"content_block_delta","index":%d,"delta":{"type":"input_json_delta","partial_json":""}}`, contentIndex)
		inputDelta, _ = sjson.Set(inputDelta, "delta.partial_json", queryJSON)
		events = append(events, sse("content_block_delta", inputDelta))
	}
	events = append(events, sse("content_block_stop", fmt.Sprintf(`{"type":"content_block_stop","index":%d}`, contentIndex)))
	contentIndex++

	webSearchToolResultStart := fmt.Sprintf(`{"type":"content_block_start","index":%d,"content_block":{"type":"web_search_tool_result","tool_use_id":"%s","content":[]}}`,
		contentIndex, toolUseID)
	resultsJSON, _ := json.Marshal(webSearchResultsFromGrounding(groundingMetadata))
	webSearchToolResultStart, _ = sjson.SetRaw(webSearchToolResultStart, "content_block.content", string(resultsJSON))
	events = append(events, sse("content_block_start", webSearchToolResultStart))
	events = append(events, sse("content_block_stop", fmt.Sprintf(`{"type":"content_block_stop","index":%d}`, contentIndex)))
	contentIndex++

	for _, block := range buildCitedTextBlocks(textContent, parseGroundingSupports(groundingMetadata)) {
		if block.Text == "" {
			continue
		}
		textBlockStart := fmt.Sprintf(`{"type":"content_block_start","index":%d,"content_block":{"type":"text","text":""}}`, contentIndex)
		if block.Citations != nil {
			textBlockStart = fmt.Sprintf(`{"type":"content_block_start","index":%d,"content_block":{"citations":[],"type":"text","text":""}}`, contentIndex)
		}
		events = append(events, sse("content_block_start", textBlockStart))
		for _, citation := range block.Citations {
			citationJSON, _ := json.Marshal(citation)
			citationDelta := fmt.Sprintf(`{"type":"content_block_delta","index":%d,"delta":{"type":"citations_delta","citation":%s}}`, contentIndex, string(citationJSON))
			events = append(events, sse("content_block_delta", citationDelta))
		}
		for _, chunk := range splitRunes(block.Text, 50) {
			textDelta := fmt.Sprintf(`{"type":"content_block_delta","index":%d,"delta":{"type":"text_delta","text":""}}`, contentIndex)
			textDelta, _ = sjson.Set(textDelta, "delta.text", chunk)
			events = append(events, sse("content_block_delta", textDelta))
		}
		events = append(events, sse("content_block_stop", fmt.Sprintf(`{"type":"content_block_stop","index":%d}`, contentIndex)))
		contentIndex++
	}

	messageDelta := fmt.Sprintf(`{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"input_tokens":%d,"output_tokens":%d,"server_tool_use":{"web_search_requests":1}}}`,
		inputTokens, outputTokens)
	events = append(events, sse("message_delta", messageDelta))
	events = append(events, sse("message_stop", `{"type":"message_stop"}`))
	return events
}

func geminiTextContent(geminiResp []byte) string {
	var b strings.Builder
	parts := gjson.GetBytes(geminiResp, "response.candidates.0.content.parts")
	if !parts.IsArray() {
		parts = gjson.GetBytes(geminiResp, "candidates.0.content.parts")
	}
	if parts.IsArray() {
		for _, part := range parts.Array() {
			if text := part.Get("text"); text.Exists() {
				b.WriteString(text.String())
			}
		}
	}
	return b.String()
}

func geminiGroundingMetadata(geminiResp []byte) gjson.Result {
	groundingMetadata := gjson.GetBytes(geminiResp, "response.candidates.0.groundingMetadata")
	if !groundingMetadata.Exists() {
		groundingMetadata = gjson.GetBytes(geminiResp, "candidates.0.groundingMetadata")
	}
	return groundingMetadata
}

func geminiUsage(geminiResp []byte) (int64, int64) {
	inputTokens := gjson.GetBytes(geminiResp, "response.usageMetadata.promptTokenCount").Int()
	if inputTokens == 0 {
		inputTokens = gjson.GetBytes(geminiResp, "usageMetadata.promptTokenCount").Int()
	}
	outputTokens := gjson.GetBytes(geminiResp, "response.usageMetadata.candidatesTokenCount").Int()
	if outputTokens == 0 {
		outputTokens = gjson.GetBytes(geminiResp, "usageMetadata.candidatesTokenCount").Int()
	}
	return inputTokens, outputTokens
}

func searchQueryFromGrounding(groundingMetadata gjson.Result) string {
	if queries := groundingMetadata.Get("webSearchQueries"); queries.IsArray() {
		arr := queries.Array()
		if len(arr) > 0 {
			return arr[0].String()
		}
	}
	return ""
}

func webSearchResultsFromGrounding(groundingMetadata gjson.Result) []map[string]any {
	results := []map[string]any{}
	chunks := groundingMetadata.Get("groundingChunks")
	if !chunks.IsArray() {
		return results
	}
	for _, chunk := range chunks.Array() {
		web := chunk.Get("web")
		if !web.Exists() {
			continue
		}
		result := map[string]any{
			"type":     "web_search_result",
			"page_age": nil,
		}
		if title := web.Get("title"); title.Exists() {
			result["title"] = title.String()
		}
		if uri := web.Get("uri"); uri.Exists() {
			result["url"] = uri.String()
		}
		results = append(results, result)
	}
	return results
}

func parseGroundingSupports(groundingMetadata gjson.Result) []groundingSupport {
	chunks := groundingMetadata.Get("groundingChunks")
	if !chunks.IsArray() {
		return nil
	}
	chunkData := make([]struct {
		URL   string
		Title string
	}, len(chunks.Array()))
	for i, chunk := range chunks.Array() {
		web := chunk.Get("web")
		if web.Exists() {
			chunkData[i].URL = web.Get("uri").String()
			chunkData[i].Title = web.Get("title").String()
		}
	}
	supportsRaw := groundingMetadata.Get("groundingSupports")
	if !supportsRaw.IsArray() {
		return nil
	}
	supports := make([]groundingSupport, 0, len(supportsRaw.Array()))
	for _, support := range supportsRaw.Array() {
		segment := support.Get("segment")
		if !segment.Exists() {
			continue
		}
		item := groundingSupport{
			StartIndex: segment.Get("startIndex").Int(),
			EndIndex:   segment.Get("endIndex").Int(),
			Text:       segment.Get("text").String(),
		}
		if indices := support.Get("groundingChunkIndices"); indices.IsArray() {
			for _, idx := range indices.Array() {
				i := int(idx.Int())
				if i >= 0 && i < len(chunkData) {
					item.ChunkURLs = append(item.ChunkURLs, chunkData[i].URL)
					if item.ChunkTitle == "" {
						item.ChunkTitle = chunkData[i].Title
					}
				}
			}
		}
		supports = append(supports, item)
	}
	return supports
}

func buildCitedTextBlocks(textContent string, supports []groundingSupport) []citedTextBlock {
	if len(supports) == 0 {
		if textContent == "" {
			return nil
		}
		return []citedTextBlock{{Text: textContent}}
	}
	textBytes := []byte(textContent)
	blocks := make([]citedTextBlock, 0, len(supports)+1)
	var lastEnd int64
	for _, support := range supports {
		if support.StartIndex > lastEnd {
			start := int(lastEnd)
			end := min(int(support.StartIndex), len(textBytes))
			if start < end {
				blocks = append(blocks, citedTextBlock{Text: string(textBytes[start:end])})
			}
		}
		if support.Text != "" && len(support.ChunkURLs) > 0 {
			blocks = append(blocks, citedTextBlock{
				Text: support.Text,
				Citations: []map[string]any{{
					"type":       "web_search_result_location",
					"cited_text": support.Text,
					"url":        support.ChunkURLs[0],
					"title":      support.ChunkTitle,
				}},
			})
		}
		if support.EndIndex > lastEnd {
			lastEnd = support.EndIndex
		}
	}
	if int(lastEnd) < len(textBytes) {
		blocks = append(blocks, citedTextBlock{Text: string(textBytes[lastEnd:])})
	}
	return blocks
}

func sse(eventType string, data string) string {
	return "event: " + eventType + "\ndata: " + data + "\n\n"
}

func splitRunes(text string, size int) []string {
	if size <= 0 {
		size = 50
	}
	runes := []rune(text)
	out := make([]string, 0, (len(runes)+size-1)/size)
	for i := 0; i < len(runes); i += size {
		end := i + size
		if end > len(runes) {
			end = len(runes)
		}
		out = append(out, string(runes[i:end]))
	}
	return out
}

func randomHex(bytesLen int) string {
	if bytesLen <= 0 {
		bytesLen = 8
	}
	buf := make([]byte, bytesLen)
	if _, errRead := rand.Read(buf); errRead == nil {
		return hex.EncodeToString(buf)
	}
	return fmt.Sprintf("%x", time.Now().UnixNano())
}
