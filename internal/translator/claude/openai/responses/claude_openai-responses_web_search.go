package responses

import (
	"context"
	"regexp"
	"strings"

	"github.com/MoeForge/moego/ojson"
	translatorcommon "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/common"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
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
	return strings.TrimSpace(ojson.Get(input, "query").String())
}

// claudeWebSearchResultsToResponses converts the content of a Claude
// `web_search_tool_result` block into the Responses `results` array.
//
// Entries ride through verbatim rather than being rebuilt field by field:
// Anthropic requires a genuine `encrypted_content` on every result it is asked
// to replay and rejects fabricated values, so anything we drop here can never be
// reconstructed later.
func claudeWebSearchResultsToResponses(content ojson.Result) []byte {
	if content.IsObject() {
		return []byte(content.Raw)
	}
	if !content.IsArray() {
		return nil
	}
	var results [][]byte
	content.ForEach(func(_, entry ojson.Result) bool {
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
	return buildResponsesWebSearchCallItemWithStatus(claudeToolUseID, query, "completed", results)
}

func buildResponsesWebSearchCallItemWithStatus(claudeToolUseID, query, status string, results []byte) []byte {
	if status == "" {
		status = "completed"
	}
	itemDoc := ojson.NewDocString(`{"id":"","type":"web_search_call","status":"completed","action":{"type":"search","query":""}}`)
	_ = itemDoc.Set("id", responsesWebSearchCallID(claudeToolUseID))
	_ = itemDoc.Set("status", status)
	_ = itemDoc.Set("action.query", query)
	if len(results) > 0 {
		_ = itemDoc.SetRawBytes("results", results)
	}
	return itemDoc.Bytes()
}

type webSearchResultLookupContextKey struct{}

// WithWebSearchResultLookup attaches a request-scoped lookup that restores
// cached Claude web_search_tool_result content for a given server_tool_use id
// when a Responses web_search_call item arrives without results.
func WithWebSearchResultLookup(ctx context.Context, lookup func(toolUseID string) ([]byte, bool)) context.Context {
	if lookup == nil {
		return ctx
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, webSearchResultLookupContextKey{}, lookup)
}

func lookupWebSearchResultFor(ctx context.Context, toolUseID string) []byte {
	if ctx == nil || toolUseID == "" {
		return nil
	}
	lookup, ok := ctx.Value(webSearchResultLookupContextKey{}).(func(string) ([]byte, bool))
	if !ok || lookup == nil {
		return nil
	}
	content, found := lookup(toolUseID)
	if !found || len(content) == 0 {
		return nil
	}
	return content
}

// convertResponsesWebSearchCallToClaudeBlocks is the inverse of
// buildResponsesWebSearchCallItem: it rebuilds the Claude block pair so a
// replayed turn still shows that the search happened and what it returned.
func convertResponsesWebSearchCallToClaudeBlocks(item ojson.Result) [][]byte {
	return convertResponsesWebSearchCallToClaudeBlocksWithQuery(item, "", true)
}

// convertResponsesWebSearchCallToClaudeBlocksWithQuery rebuilds the server_tool_use
// block, using fallbackQuery when the item carries none, and the result block
// only when withResult is set.
func convertResponsesWebSearchCallToClaudeBlocksWithQuery(item ojson.Result, fallbackQuery string, withResult bool) [][]byte {
	return convertResponsesWebSearchCallToClaudeBlocksWithContext(context.Background(), item, fallbackQuery, withResult)
}

func convertResponsesWebSearchCallToClaudeBlocksWithContext(ctx context.Context, item ojson.Result, fallbackQuery string, withResult bool) [][]byte {
	toolUseID := claudeWebSearchToolUseID(strings.TrimSpace(item.Get("id").String()))
	if toolUseID == "" {
		return nil
	}

	useDoc := ojson.NewDocString(`{"type":"server_tool_use","id":"","name":"","input":{}}`)
	_ = useDoc.Set("id", toolUseID)
	_ = useDoc.Set("name", claudeWebSearchToolName)
	query := responsesWebSearchCallQuery(item)
	if query == "" {
		query = fallbackQuery
	}
	if query != "" {
		_ = useDoc.Set("input.query", query)
	}
	if !withResult {
		return [][]byte{useDoc.Bytes()}
	}

	resultDoc := ojson.NewDocString(`{"type":"web_search_tool_result","tool_use_id":"","content":[]}`)
	_ = resultDoc.Set("tool_use_id", toolUseID)
	content := responsesWebSearchResultsToClaude(item.Get("results"))
	if len(content) == 0 {
		content = lookupWebSearchResultFor(ctx, toolUseID)
	}
	if len(content) > 0 {
		_ = resultDoc.SetRawBytes("content", content)
	}
	return [][]byte{useDoc.Bytes(), resultDoc.Bytes()}
}

// responsesWebSearchHistory indexes web_search_call items of one request so a
// search paused by pause_turn or deferred by tool_use replays faithfully:
//   - while the open search turn is the tail of the history, the search is sent
//     back as a server_tool_use without a result, unchanged, as Anthropic requires;
//   - when the continuation delivered the result under the same id within the
//     same assistant run (no user turn in between), the open item is skipped and
//     the continuation item emits the full pair, keeping the query recorded by
//     the open item.
//
// Every other web_search_call, including repeated ids across separate turns,
// keeps its previous one-pair-per-item replay.
type responsesWebSearchHistory struct {
	// boundaryAfter[i] reports whether a user turn follows index i.
	boundaryAfter []bool
	// skip marks paused items that a later continuation item replaces.
	skip map[int]bool
	// pausedQuery carries the paused item's query to its continuation item.
	pausedQuery map[int]string
}

func responsesWebSearchItemIsPaused(item ojson.Result) bool {
	return item.Get("type").String() == "web_search_call" &&
		strings.EqualFold(strings.TrimSpace(item.Get("status").String()), "in_progress") &&
		!item.Get("results").Exists()
}

// responsesItemIsTurnBoundary reports a user turn. Tool outputs do not end the
// assistant turn: Anthropic runs a deferred search right after them.
func responsesItemIsTurnBoundary(item ojson.Result) bool {
	typ := item.Get("type").String()
	role := item.Get("role").String()
	if typ == "" && role != "" {
		typ = "message"
	}
	return typ == "message" && role != "assistant"
}

func scanResponsesWebSearchHistory(items []ojson.Result) responsesWebSearchHistory {
	history := responsesWebSearchHistory{
		boundaryAfter: make([]bool, len(items)),
		skip:          map[int]bool{},
		pausedQuery:   map[int]string{},
	}
	boundary := false
	for index := len(items) - 1; index >= 0; index-- {
		history.boundaryAfter[index] = boundary
		if responsesItemIsTurnBoundary(items[index]) {
			boundary = true
		}
	}
	for index, item := range items {
		if !responsesWebSearchItemIsPaused(item) {
			continue
		}
		id := strings.TrimSpace(item.Get("id").String())
		if id == "" {
			continue
		}
		for next := index + 1; next < len(items); next++ {
			if responsesItemIsTurnBoundary(items[next]) {
				break
			}
			if items[next].Get("type").String() == "web_search_call" && strings.TrimSpace(items[next].Get("id").String()) == id {
				history.skip[index] = true
				query := responsesWebSearchCallQuery(item)
				if previous, ok := history.pausedQuery[index]; ok && query == "" {
					query = previous
				}
				history.pausedQuery[next] = query
				break
			}
		}
	}
	return history
}

func (h responsesWebSearchHistory) claudeBlocks(index int, item ojson.Result) [][]byte {
	return h.claudeBlocksWithContext(context.Background(), index, item)
}

func (h responsesWebSearchHistory) claudeBlocksWithContext(ctx context.Context, index int, item ojson.Result) [][]byte {
	if h.skip[index] {
		return nil
	}
	query := h.pausedQuery[index]
	tail := index >= len(h.boundaryAfter) || !h.boundaryAfter[index]
	if responsesWebSearchItemIsPaused(item) && tail {
		return convertResponsesWebSearchCallToClaudeBlocksWithContext(ctx, item, query, false)
	}
	return convertResponsesWebSearchCallToClaudeBlocksWithContext(ctx, item, query, true)
}

// responsesWebSearchCallQuery reads the query from a Responses web_search_call,
// tolerating the `queries` array and the `open_page` action shape that OpenAI
// clients emit.
func responsesWebSearchCallQuery(item ojson.Result) string {
	if query := strings.TrimSpace(item.Get("action.query").String()); query != "" {
		return query
	}
	if query := strings.TrimSpace(item.Get("action.queries.0").String()); query != "" {
		return query
	}
	return strings.TrimSpace(item.Get("action.url").String())
}

func responsesWebSearchResultsToClaude(results ojson.Result) []byte {
	if results.IsObject() {
		return []byte(results.Raw)
	}
	if !results.IsArray() {
		return nil
	}
	var blocks [][]byte
	results.ForEach(func(_, entry ojson.Result) bool {
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
		block, _ = ojson.SetBytes(block, "type", "web_search_result")
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
func attachClaudeCitations(textBlock []byte, annotations ojson.Result) []byte {
	if !annotations.IsArray() {
		return textBlock
	}
	var citations [][]byte
	annotations.ForEach(func(_, annotation ojson.Result) bool {
		if strings.TrimSpace(annotation.Get("encrypted_index").String()) != "" {
			citations = append(citations, []byte(annotation.Raw))
		}
		return true
	})
	if len(citations) == 0 {
		return textBlock
	}
	updated, err := ojson.SetRawBytes(textBlock, "citations", translatorcommon.JoinRawArray(citations))
	if err != nil {
		return textBlock
	}
	return updated
}

func claudeBlocksHaveEmptyWebSearchResult(blocks [][]byte) bool {
	for _, raw := range blocks {
		block := util.ParseGJSONBytesNoCopy(raw)
		if block.Get("type").String() != "web_search_tool_result" {
			continue
		}
		content := block.Get("content")
		if !content.Exists() || content.Type == ojson.Null || (content.IsArray() && len(content.Array()) == 0) {
			return true
		}
	}
	return false
}

// stripClaudeWebSearchCitationsFromParts removes web_search_result_location
// citations from text blocks when a web_search_tool_result in the same
// assistant turn ended up without content. Anthropic intermittently rejects
// citations whose encrypted_index has no matching search result in the turn.
func stripClaudeWebSearchCitationsFromParts(parts [][]byte) ([][]byte, int) {
	droppedTotal := 0
	for i, raw := range parts {
		part := util.ParseGJSONBytesNoCopy(raw)
		if part.Get("type").String() != "text" {
			continue
		}
		updated, dropped := stripClaudeWebSearchCitationsFromTextBlock(raw, part.Get("citations"))
		if dropped > 0 {
			parts[i] = updated
			droppedTotal += dropped
		}
	}
	return parts, droppedTotal
}

func stripClaudeWebSearchCitationsFromTextBlock(textBlock []byte, citations ojson.Result) ([]byte, int) {
	if !citations.Exists() || !citations.IsArray() {
		return textBlock, 0
	}
	var kept [][]byte
	dropped := 0
	citations.ForEach(func(_, citation ojson.Result) bool {
		if citation.Get("type").String() == "web_search_result_location" {
			dropped++
			return true
		}
		kept = append(kept, []byte(citation.Raw))
		return true
	})
	if dropped == 0 {
		return textBlock, 0
	}
	if len(kept) == 0 {
		updated, errDelete := ojson.DeleteBytes(textBlock, "citations")
		if errDelete != nil {
			return textBlock, 0
		}
		return updated, dropped
	}
	updated, errSet := ojson.SetRawBytes(textBlock, "citations", translatorcommon.JoinRawArray(kept))
	if errSet != nil {
		return textBlock, 0
	}
	return updated, dropped
}
