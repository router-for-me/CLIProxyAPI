package claude

import (
	"encoding/json"
	"fmt"
	"strings"

	translatorcommon "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/common"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func appendCodexWebSearchServerToolUse(output []byte, params *ConvertCodexResponseToClaudeParams, root, item gjson.Result) []byte {
	toolUseID := codexWebSearchToolUseID(params, root, item)
	if toolUseID == "" {
		return output
	}
	if params.WebSearchToolUseIDs == nil {
		params.WebSearchToolUseIDs = make(map[string]struct{})
	}
	if _, ok := params.WebSearchToolUseIDs[toolUseID]; ok {
		return output
	}

	output = append(output, finalizeCodexThinkingBlock(params)...)
	template := []byte(`{"type":"content_block_start","index":0,"content_block":{"type":"server_tool_use","id":"","name":"web_search","input":{}}}`)
	template, _ = sjson.SetBytes(template, "index", params.BlockIndex)
	template, _ = sjson.SetBytes(template, "content_block.id", toolUseID)
	output = translatorcommon.AppendSSEEventBytes(output, "content_block_start", template, 2)

	if query := codexWebSearchQuery(root, item); query != "" {
		partialJSON, _ := json.Marshal(map[string]string{"query": query})
		delta := []byte(`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":""}}`)
		delta, _ = sjson.SetBytes(delta, "index", params.BlockIndex)
		delta, _ = sjson.SetBytes(delta, "delta.partial_json", string(partialJSON))
		output = translatorcommon.AppendSSEEventBytes(output, "content_block_delta", delta, 2)
	}

	stop := []byte(`{"type":"content_block_stop","index":0}`)
	stop, _ = sjson.SetBytes(stop, "index", params.BlockIndex)
	output = translatorcommon.AppendSSEEventBytes(output, "content_block_stop", stop, 2)
	params.WebSearchToolUseIDs[toolUseID] = struct{}{}
	params.BlockIndex++
	return output
}

func appendCodexWebSearchToolResult(output []byte, params *ConvertCodexResponseToClaudeParams, root, item gjson.Result) []byte {
	toolUseID := codexWebSearchToolUseID(params, root, item)
	if toolUseID == "" {
		return output
	}
	output = appendCodexWebSearchServerToolUse(output, params, root, item)
	if params.WebSearchToolResultIDs == nil {
		params.WebSearchToolResultIDs = make(map[string]struct{})
	}
	if _, ok := params.WebSearchToolResultIDs[toolUseID]; ok {
		return output
	}

	template := []byte(`{"type":"content_block_start","index":0,"content_block":{"type":"web_search_tool_result","tool_use_id":"","content":[]}}`)
	template, _ = sjson.SetBytes(template, "index", params.BlockIndex)
	template, _ = sjson.SetBytes(template, "content_block.tool_use_id", toolUseID)
	if content := codexWebSearchResultContent(root, item); len(content) > 0 {
		template, _ = sjson.SetRawBytes(template, "content_block.content", content)
	}
	output = translatorcommon.AppendSSEEventBytes(output, "content_block_start", template, 2)

	stop := []byte(`{"type":"content_block_stop","index":0}`)
	stop, _ = sjson.SetBytes(stop, "index", params.BlockIndex)
	output = translatorcommon.AppendSSEEventBytes(output, "content_block_stop", stop, 2)
	params.WebSearchToolResultIDs[toolUseID] = struct{}{}
	params.BlockIndex++
	if toolUseID == params.LastWebSearchToolUseID {
		params.LastWebSearchToolUseID = ""
	}
	return output
}

func codexWebSearchToolUseID(params *ConvertCodexResponseToClaudeParams, root, item gjson.Result) string {
	for _, path := range []string{"id", "output_item_id", "call_id"} {
		if value := strings.TrimSpace(item.Get(path).String()); value != "" {
			return value
		}
		if value := strings.TrimSpace(root.Get(path).String()); value != "" {
			return value
		}
	}
	if params.LastWebSearchToolUseID != "" {
		return params.LastWebSearchToolUseID
	}
	for _, path := range []string{"item_id"} {
		if value := strings.TrimSpace(item.Get(path).String()); value != "" {
			return value
		}
		if value := strings.TrimSpace(root.Get(path).String()); value != "" {
			return value
		}
	}
	id := fmt.Sprintf("web_search_%d", params.BlockIndex)
	params.LastWebSearchToolUseID = id
	return id
}

func codexWebSearchQuery(root, item gjson.Result) string {
	for _, path := range []string{"action.query", "query", "input.query"} {
		if value := strings.TrimSpace(item.Get(path).String()); value != "" {
			return value
		}
		if value := strings.TrimSpace(root.Get(path).String()); value != "" {
			return value
		}
	}
	return ""
}

func codexWebSearchResultContent(root, item gjson.Result) []byte {
	results := item.Get("results")
	if !results.IsArray() {
		results = root.Get("results")
	}
	if !results.IsArray() {
		return nil
	}
	content := []byte(`[]`)
	results.ForEach(func(_, result gjson.Result) bool {
		url := strings.TrimSpace(result.Get("url").String())
		if url == "" {
			return true
		}
		block := []byte(`{"type":"web_search_result","title":"","url":"","page_age":null}`)
		block, _ = sjson.SetBytes(block, "url", url)
		title := strings.TrimSpace(result.Get("title").String())
		if title == "" {
			title = url
		}
		block, _ = sjson.SetBytes(block, "title", title)
		content, _ = sjson.SetRawBytes(content, "-1", block)
		return true
	})
	return content
}
