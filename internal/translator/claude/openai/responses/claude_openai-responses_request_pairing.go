package responses

import (
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// reorderClaudeToolUseResultPairs performs a defense-in-depth "pairing
// sanitization" pass over the final Anthropic `messages` array produced by
// ConvertOpenAIResponsesRequestToClaude.
//
// Background: AWS Bedrock (Anthropic on Bedrock) rejects requests with HTTP 400
// "tool_use and tool_result blocks must be correctly paired and ordered" when a
// non-tool message (e.g. a compact-continuation developer/text message) is
// inserted between an assistant tool_use message and its matching user
// tool_result message.
//
// This pass works at the content-BLOCK level, not the message level. For every
// assistant message carrying one or more tool_use blocks (a "batch"), it pulls
// the matching tool_result BLOCKS to sit in a user message immediately after the
// tool_use message — matching strictly on tool_use_id.
//
// Why block-level and not message-level: the request converter merges ADJACENT
// function_call_output items into a SINGLE user message (see
// claude_openai-responses_request.go, the "parallel-tool-merge" mirror branch).
// So a single user message can carry tool_result blocks belonging to TWO
// different assistant batches, e.g. input
//
//	assistant[use:a] | user(compact) | assistant[use:b] | output:a | output:b
//
// collapses to a user message content=[tool_result:a, tool_result:b]. Moving the
// WHOLE message after use:a would orphan tool_result:b (use:b would have nothing
// after it) — re-triggering the very Bedrock 400 this pass prevents. Instead we
// split the merged container: tool_result:a goes after use:a, tool_result:b goes
// after use:b. Anthropic requires that the parallel tool_result blocks for one
// batch all live in the single user turn right after that batch, so a batch's
// matched results are gathered into one user message together.
//
// The pass is idempotent: an already-correctly-ordered sequence is returned with
// the original bytes untouched (the identity permutation short-circuits), and a
// sequence that was actually reordered is left byte-identical on a second pass.
func reorderClaudeToolUseResultPairs(out []byte) []byte {
	msgs := gjson.GetBytes(out, "messages")
	if !msgs.IsArray() {
		return out
	}
	arr := msgs.Array()
	n := len(arr)
	if n < 2 {
		return out
	}

	// Pre-parse each message's content blocks (only meaningful for array
	// content; string content yields nil).
	blocks := make([][]contentBlock, n)
	blockTaken := make([][]bool, n)
	for i := range arr {
		blocks[i] = parseContentBlocks(arr[i])
		if blocks[i] != nil {
			blockTaken[i] = make([]bool, len(blocks[i]))
		}
	}

	consumed := make([]bool, n)
	// An emitted entry is either an original message (idx >= 0) or a freshly
	// constructed message (idx == -1, use raw).
	type entry struct {
		idx int
		raw []byte
	}
	emitted := make([]entry, 0, n)

	for i := 0; i < n; i++ {
		if consumed[i] {
			continue
		}
		consumed[i] = true

		ids := toolUseIDsOrdered(arr[i])
		if len(ids) == 0 {
			// Not an assistant tool_use batch. If some of this message's
			// tool_result blocks were already pulled out by an earlier batch,
			// emit only the leftover blocks; otherwise emit the message as-is.
			if blocks[i] == nil {
				emitted = append(emitted, entry{idx: i})
				continue
			}
			anyTaken := false
			for _, t := range blockTaken[i] {
				if t {
					anyTaken = true
					break
				}
			}
			if !anyTaken {
				emitted = append(emitted, entry{idx: i})
				continue
			}
			remaining := make([]string, 0, len(blocks[i]))
			for bi, b := range blocks[i] {
				if !blockTaken[i][bi] {
					remaining = append(remaining, b.raw)
				}
			}
			if len(remaining) == 0 {
				// Every block was pulled elsewhere; drop the now-empty container.
				continue
			}
			emitted = append(emitted, entry{idx: -1, raw: buildBlocksMessage(arr[i].Get("role").String(), remaining)})
			continue
		}

		// Assistant tool_use batch: emit it, then pull the matching tool_result
		// blocks (forward only) to sit immediately after it.
		emitted = append(emitted, entry{idx: i})

		matchedRaw := make([]string, 0, len(ids))
		srcMsg := -1
		singleSrc := true
		for _, id := range ids {
			mi, bi, ok := findForwardResultBlock(blocks, blockTaken, consumed, i+1, id)
			if !ok {
				continue
			}
			blockTaken[mi][bi] = true
			matchedRaw = append(matchedRaw, blocks[mi][bi].raw)
			if srcMsg == -1 {
				srcMsg = mi
			} else if srcMsg != mi {
				singleSrc = false
			}
		}
		if len(matchedRaw) == 0 {
			continue
		}

		// Byte-preservation fast path: when every matched result came from one
		// still-unconsumed message that holds ONLY these tool_result blocks
		// (nothing else, nothing extra), keep that message verbatim instead of
		// rebuilding it. This is what keeps an already-canonical sequence
		// byte-identical (idempotency).
		if singleSrc && srcMsg >= 0 && !consumed[srcMsg] &&
			len(matchedRaw) == len(blocks[srcMsg]) && allToolResults(blocks[srcMsg]) {
			consumed[srcMsg] = true
			emitted = append(emitted, entry{idx: srcMsg})
			continue
		}
		emitted = append(emitted, entry{idx: -1, raw: buildBlocksMessage("user", matchedRaw)})
	}

	// Idempotency guard: if the emitted order is the identity permutation of the
	// original messages (no splits, no moves), return the original bytes.
	identity := len(emitted) == n
	if identity {
		for k, e := range emitted {
			if e.idx != k {
				identity = false
				break
			}
		}
	}
	if identity {
		return out
	}

	rebuilt := []byte(`[]`)
	for _, e := range emitted {
		if e.idx >= 0 {
			rebuilt, _ = sjson.SetRawBytes(rebuilt, "-1", []byte(arr[e.idx].Raw))
		} else {
			rebuilt, _ = sjson.SetRawBytes(rebuilt, "-1", e.raw)
		}
	}
	// Moving an intervening message after a tool_use/tool_result pair can leave two
	// consecutive same-role messages (e.g. the relocated tool_result user message
	// followed by an intervening user message). Anthropic/Bedrock require strict
	// user/assistant alternation and reject violations with HTTP 400, so merge any
	// consecutive same-role messages by concatenating their content blocks.
	rebuilt = mergeConsecutiveSameRoleMessages(rebuilt)
	result, err := sjson.SetRawBytes(out, "messages", rebuilt)
	if err != nil {
		return out
	}
	return result
}

// mergeConsecutiveSameRoleMessages collapses adjacent messages that share the
// same role into a single message whose content is the concatenation of both
// messages' content blocks. String content is wrapped into a single text block
// so it can be merged with array content. Input is a JSON array of messages;
// output is the (possibly shorter) JSON array.
func mergeConsecutiveSameRoleMessages(msgsJSON []byte) []byte {
	arr := gjson.ParseBytes(msgsJSON).Array()
	if len(arr) < 2 {
		return msgsJSON
	}
	// contentBlocksOf returns the raw JSON of each content block for a message,
	// wrapping string content into one text block.
	contentBlocksOf := func(m gjson.Result) []string {
		c := m.Get("content")
		if c.IsArray() {
			var bs []string
			c.ForEach(func(_, b gjson.Result) bool {
				bs = append(bs, b.Raw)
				return true
			})
			return bs
		}
		tb := []byte(`{"type":"text","text":""}`)
		tb, _ = sjson.SetBytes(tb, "text", c.String())
		return []string{string(tb)}
	}

	merged := false
	out := []byte(`[]`)
	var curRole string
	var curBlocks []string
	haveCur := false
	flush := func() {
		if !haveCur {
			return
		}
		out, _ = sjson.SetRawBytes(out, "-1", buildBlocksMessage(curRole, curBlocks))
	}
	for _, m := range arr {
		role := m.Get("role").String()
		if haveCur && role == curRole {
			curBlocks = append(curBlocks, contentBlocksOf(m)...)
			merged = true
			continue
		}
		flush()
		curRole = role
		curBlocks = contentBlocksOf(m)
		haveCur = true
	}
	flush()
	if !merged {
		// No adjacent same-role messages: keep original bytes (preserves any
		// string-content messages verbatim instead of rewrapping into blocks).
		return msgsJSON
	}
	return out
}

// contentBlock is a lightweight view of one content array element. For
// tool_result blocks it carries the tool_use_id so results can be matched to
// their owning assistant batch.
type contentBlock struct {
	raw      string
	isResult bool
	id       string
}

// parseContentBlocks returns the content array blocks of a message, or nil when
// the message content is not an array (e.g. a plain string text message).
func parseContentBlocks(msg gjson.Result) []contentBlock {
	content := msg.Get("content")
	if !content.IsArray() {
		return nil
	}
	var out []contentBlock
	content.ForEach(func(_, b gjson.Result) bool {
		cb := contentBlock{raw: b.Raw}
		if b.Get("type").String() == "tool_result" {
			cb.isResult = true
			cb.id = b.Get("tool_use_id").String()
		}
		out = append(out, cb)
		return true
	})
	return out
}

// toolUseIDsOrdered returns the tool_use ids carried by an assistant message
// whose content is an array of content blocks, in first-appearance order and
// de-duplicated. Returns nil for any message that is not an assistant tool_use
// container.
func toolUseIDsOrdered(msg gjson.Result) []string {
	if msg.Get("role").String() != "assistant" {
		return nil
	}
	content := msg.Get("content")
	if !content.IsArray() {
		return nil
	}
	var ids []string
	seen := map[string]struct{}{}
	content.ForEach(func(_, b gjson.Result) bool {
		if b.Get("type").String() == "tool_use" {
			id := b.Get("id").String()
			if id != "" {
				if _, ok := seen[id]; !ok {
					seen[id] = struct{}{}
					ids = append(ids, id)
				}
			}
		}
		return true
	})
	return ids
}

// findForwardResultBlock scans forward from message index `from` for the first
// untaken tool_result block whose tool_use_id equals `id`. Returns the message
// index, block index, and whether a match was found. Forward-only by design:
// results that appear BEFORE their tool_use (a separate, out-of-scope concern)
// are left untouched.
func findForwardResultBlock(blocks [][]contentBlock, blockTaken [][]bool, consumed []bool, from int, id string) (int, int, bool) {
	for j := from; j < len(blocks); j++ {
		if consumed[j] || blocks[j] == nil {
			continue
		}
		for bi, b := range blocks[j] {
			if b.isResult && !blockTaken[j][bi] && b.id == id {
				return j, bi, true
			}
		}
	}
	return -1, -1, false
}

// allToolResults reports whether every block in bs is a tool_result block (and
// the slice is non-empty).
func allToolResults(bs []contentBlock) bool {
	for _, b := range bs {
		if !b.isResult {
			return false
		}
	}
	return len(bs) > 0
}

// buildBlocksMessage constructs a `{"role":role,"content":[...]}` message from
// the given raw content-block JSON fragments, preserving their order.
func buildBlocksMessage(role string, rawBlocks []string) []byte {
	msg := []byte(`{"role":"","content":[]}`)
	msg, _ = sjson.SetBytes(msg, "role", role)
	for _, r := range rawBlocks {
		msg, _ = sjson.SetRawBytes(msg, "content.-1", []byte(r))
	}
	return msg
}
