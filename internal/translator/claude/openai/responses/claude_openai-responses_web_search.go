package responses

import (
	"regexp"
	"strings"

	translatorcommon "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/common"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Claude reports server-side web search as a pair of assistant content blocks: a
// `server_tool_use` block carrying the query and a `web_search_tool_result` block
// carrying the hits. OpenAI Responses models the same exchange as a single
// `web_search_call` item. The representations are isomorphic, so the pair folds
// into one item on the way out and expands back on the way in.
//
// Dropping the pair instead is not merely cosmetic: the replayed turn then shows
// a conclusion with no search behind it, which makes the model treat its own
// previous answer as unsourced and search again.
const (
	claudeWebSearchToolName = "web_search"

	// responsesWebSearchIDPrefix namespaces the Claude server_tool_use id inside
	// the Responses item id, mirroring the fc_/ctc_ prefixes used for tool calls
	// so the original id can be recovered on replay.
	responsesWebSearchIDPrefix = "ws_"

	// claudeServerToolIDPrefix is mandated by Anthropic: server tool ids must
	// match ^srvtoolu_[a-zA-Z0-9_]+$.
	claudeServerToolIDPrefix = "srvtoolu_"
)

// claudeServerToolIDSanitizer matches the characters Anthropic forbids in a
// server tool id. Note it is stricter than util.SanitizeClaudeToolID, which
// targets tool_use ids and allows '-'.
var claudeServerToolIDSanitizer = regexp.MustCompile(`[^a-zA-Z0-9_]`)

func responsesWebSearchCallID(claudeToolUseID string) string {
	return responsesWebSearchIDPrefix + claudeToolUseID
}

// claudeWebSearchToolUseID recovers the Claude server_tool_use id from a
// Responses item id, normalising it to Anthropic's required shape. A Responses
// history need not come from Claude at all - a native OpenAI web_search_call
// carries an id like "ws_00112233aabb" - and replaying such an id verbatim is
// rejected outright, so the body is always sanitised and re-prefixed. The
// transform is idempotent, leaving genuine Claude ids untouched.
func claudeWebSearchToolUseID(responsesItemID string) string {
	body := strings.TrimPrefix(strings.TrimSpace(responsesItemID), responsesWebSearchIDPrefix)
	body = claudeServerToolIDSanitizer.ReplaceAllString(strings.TrimPrefix(body, claudeServerToolIDPrefix), "_")
	if body == "" {
		return ""
	}
	return claudeServerToolIDPrefix + body
}

// claudeWebSearchQuery extracts the query from a Claude server_tool_use input,
// accepting either the accumulated streaming JSON or an already parsed object.
func claudeWebSearchQuery(input string) string {
	if input == "" {
		return ""
	}
	return strings.TrimSpace(gjson.Get(input, "query").String())
}

// claudeWebSearchResultsToResponses converts the content of a Claude
// `web_search_tool_result` block into the Responses `results` array.
//
// Entries ride through verbatim rather than being rebuilt field by field:
// Anthropic requires a genuine `encrypted_content` on every result it is asked
// to replay and rejects fabricated values, so anything we drop here can never be
// reconstructed later.
func claudeWebSearchResultsToResponses(content gjson.Result) []byte {
	if content.IsObject() {
		return []byte(content.Raw)
	}
	if !content.IsArray() {
		return nil
	}
	var results [][]byte
	content.ForEach(func(_, entry gjson.Result) bool {
		if entry.Get("type").String() == "web_search_tool_result_error" || strings.TrimSpace(entry.Get("url").String()) != "" {
			results = append(results, []byte(entry.Raw))
		}
		return true
	})
	if len(results) == 0 {
		return []byte(`[]`)
	}
	return translatorcommon.JoinRawArray(results)
}

// buildResponsesWebSearchCallItem renders the Responses item that stands for one
// Claude server-side search. results may be nil when the upstream turn ended
// before the result block arrived.
func buildResponsesWebSearchCallItem(claudeToolUseID, query string, results []byte) []byte {
	item := []byte(`{"id":"","type":"web_search_call","status":"completed","action":{"type":"search","query":""}}`)
	item, _ = sjson.SetBytes(item, "id", responsesWebSearchCallID(claudeToolUseID))
	item, _ = sjson.SetBytes(item, "action.query", query)
	if len(results) > 0 {
		item, _ = sjson.SetRawBytes(item, "results", results)
	}
	return item
}

// convertResponsesWebSearchCallToClaudeBlocks is the inverse of
// buildResponsesWebSearchCallItem: it rebuilds the Claude block pair so a
// replayed turn still shows that the search happened and what it returned.
func convertResponsesWebSearchCallToClaudeBlocks(item gjson.Result) [][]byte {
	toolUseID := claudeWebSearchToolUseID(strings.TrimSpace(item.Get("id").String()))
	if toolUseID == "" {
		return nil
	}

	use := []byte(`{"type":"server_tool_use","id":"","name":"","input":{}}`)
	use, _ = sjson.SetBytes(use, "id", toolUseID)
	use, _ = sjson.SetBytes(use, "name", claudeWebSearchToolName)
	if query := responsesWebSearchCallQuery(item); query != "" {
		use, _ = sjson.SetBytes(use, "input.query", query)
	}

	result := []byte(`{"type":"web_search_tool_result","tool_use_id":"","content":[]}`)
	result, _ = sjson.SetBytes(result, "tool_use_id", toolUseID)
	if content := responsesWebSearchResultsToClaude(item.Get("results")); len(content) > 0 {
		result, _ = sjson.SetRawBytes(result, "content", content)
	}
	return [][]byte{use, result}
}

// responsesWebSearchCallQuery reads the query from a Responses web_search_call,
// tolerating the `queries` array and the `open_page` action shape that OpenAI
// clients emit.
func responsesWebSearchCallQuery(item gjson.Result) string {
	if query := strings.TrimSpace(item.Get("action.query").String()); query != "" {
		return query
	}
	if query := strings.TrimSpace(item.Get("action.queries.0").String()); query != "" {
		return query
	}
	return strings.TrimSpace(item.Get("action.url").String())
}

func responsesWebSearchResultsToClaude(results gjson.Result) []byte {
	if results.IsObject() {
		return []byte(results.Raw)
	}
	if !results.IsArray() {
		return nil
	}
	var blocks [][]byte
	results.ForEach(func(_, entry gjson.Result) bool {
		if entry.Get("type").String() == "web_search_tool_result_error" {
			blocks = append(blocks, []byte(entry.Raw))
			return true
		}
		// Anthropic validates encrypted_content and rejects the whole request when
		// it is missing or forged. An entry that lost it in transit is therefore
		// unusable; an empty result list is the only safe degradation, and it is
		// accepted alongside the mandatory server_tool_use block.
		if strings.TrimSpace(entry.Get("encrypted_content").String()) == "" {
			return true
		}
		block := []byte(entry.Raw)
		block, _ = sjson.SetBytes(block, "type", "web_search_result")
		blocks = append(blocks, block)
		return true
	})
	if len(blocks) == 0 {
		return nil
	}
	return translatorcommon.JoinRawArray(blocks)
}

// attachClaudeCitations mirrors Responses `annotations` back onto a Claude text
// block as `citations`. The annotations were copied verbatim from Claude on the
// way out, so they ride back unchanged; entries without the mandatory
// `encrypted_index` cannot be replayed and are dropped rather than rejected.
func attachClaudeCitations(textBlock []byte, annotations gjson.Result) []byte {
	if !annotations.IsArray() {
		return textBlock
	}
	var citations [][]byte
	annotations.ForEach(func(_, annotation gjson.Result) bool {
		if strings.TrimSpace(annotation.Get("encrypted_index").String()) != "" {
			citations = append(citations, []byte(annotation.Raw))
		}
		return true
	})
	if len(citations) == 0 {
		return textBlock
	}
	updated, err := sjson.SetRawBytes(textBlock, "citations", translatorcommon.JoinRawArray(citations))
	if err != nil {
		return textBlock
	}
	return updated
}
