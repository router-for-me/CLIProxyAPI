package precompact

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Split describes a request body divided into a byte-identical head (system
// prompt, tools, leading system messages), a middle block to summarize, and a
// byte-identical tail of recent turns.
type Split struct {
	// Prefix holds raw JSON of leading OpenAI system messages (empty for Claude).
	Prefix []string
	// Middle holds raw JSON of the messages to summarize.
	Middle []string
	// Recent holds raw JSON of the messages kept verbatim.
	Recent []string
}

// isUserTurn reports whether a raw message starts a real user turn: role user
// and not a tool-result carrier. Cutting only at such messages never separates
// a tool_use from its tool_result.
func isUserTurn(format sdktranslator.Format, msg gjson.Result) bool {
	role := msg.Get("role").String()
	if role != "user" {
		return false
	}
	content := msg.Get("content")
	if content.Type == gjson.String {
		return true
	}
	if !content.IsArray() {
		return true
	}
	for _, block := range content.Array() {
		if block.Get("type").String() == "tool_result" {
			return false
		}
	}
	_ = format
	return true
}

// SplitMessages splits body.messages so the last keepTurns user turns stay
// verbatim. ok is false when there is nothing to summarize. It applies no
// token limit; see SplitMessagesBudget.
func SplitMessages(format sdktranslator.Format, body []byte, keepTurns int) (Split, bool) {
	return SplitMessagesBudget(format, body, keepTurns, 0, nil)
}

// SplitMessagesBudget splits body.messages keeping the most recent turns
// verbatim until both limits are reached: at most keepTurns user turns, and
// stop earlier once the kept tail exceeds keepTokens (0 = no token limit).
// At least one full turn (the last user turn and everything after it, so a
// tool_use is never separated from its tool_result) is always kept.
// tokens estimates the token count of one raw message; nil disables the
// token limit. ok is false when nothing would be left to summarize.
func SplitMessagesBudget(format sdktranslator.Format, body []byte, keepTurns, keepTokens int, tokens func(raw string) int) (Split, bool) {
	var out Split
	msgs := gjson.GetBytes(body, "messages")
	if !msgs.IsArray() {
		return out, false
	}
	raw := make([]string, 0, len(msgs.Array()))
	for _, m := range msgs.Array() {
		raw = append(raw, m.Raw)
	}
	start := 0
	if format == sdktranslator.FormatOpenAI {
		for start < len(raw) {
			role := gjson.Get(raw[start], "role").String()
			if role != "system" && role != "developer" {
				break
			}
			start++
		}
	}
	if keepTurns < 1 {
		keepTurns = 1
	}
	limitTokens := keepTokens > 0 && tokens != nil
	// Walk back over user-turn boundaries. The first boundary is always kept;
	// each further one only while both limits still hold.
	cut := -1
	seen := 0
	kept := 0
	for i := len(raw) - 1; i >= start; i-- {
		if limitTokens {
			kept += tokens(raw[i])
		}
		if !isUserTurn(format, gjson.Parse(raw[i])) {
			continue
		}
		if seen > 0 && limitTokens && kept > keepTokens {
			break
		}
		seen++
		cut = i
		if seen >= keepTurns {
			break
		}
	}
	if cut <= start {
		return out, false
	}
	out.Prefix = raw[:start]
	out.Middle = raw[start:cut]
	out.Recent = raw[cut:]
	return out, true
}

// ChunkTurns groups msgs into consecutive chunks that each fit within budget
// tokens, cutting only at user-turn boundaries so a tool_use always stays in
// the same chunk as its tool_result. A single turn larger than budget forms
// its own chunk. budget <= 0 or a nil counter returns one chunk.
func ChunkTurns(format sdktranslator.Format, msgs []string, budget int, tokens func(raw string) int) [][]string {
	if len(msgs) == 0 {
		return nil
	}
	if budget <= 0 || tokens == nil {
		return [][]string{msgs}
	}
	var chunks [][]string
	chunkStart, chunkTokens := 0, 0
	turnStart, turnTokens := 0, 0
	flushTurn := func(end int) {
		if turnStart > chunkStart && chunkTokens+turnTokens > budget {
			chunks = append(chunks, msgs[chunkStart:turnStart])
			chunkStart = turnStart
			chunkTokens = 0
		}
		chunkTokens += turnTokens
		turnStart = end
		turnTokens = 0
	}
	for i, raw := range msgs {
		if i > 0 && isUserTurn(format, gjson.Parse(raw)) {
			flushTurn(i)
		}
		turnTokens += tokens(raw)
	}
	flushTurn(len(msgs))
	chunks = append(chunks, msgs[chunkStart:])
	return chunks
}

// ScaledEstimator returns a per-message token estimator calibrated on the
// real count of the whole body: each message gets its byte share of total.
// It never re-tokenizes, so splitting and chunking stay cheap.
func ScaledEstimator(totalTokens, totalBytes int) func(raw string) int {
	ratio := 0.25
	if totalTokens > 0 && totalBytes > 0 {
		ratio = float64(totalTokens) / float64(totalBytes)
	}
	return func(raw string) int {
		n := int(float64(len(raw)) * ratio)
		if n < 1 {
			n = 1
		}
		return n
	}
}

// Rebuild writes a compacted messages array back into body. Everything outside
// "messages" stays byte-identical.
func Rebuild(format sdktranslator.Format, body []byte, sp Split, summary string) ([]byte, error) {
	var b strings.Builder
	b.WriteString("[")
	first := true
	add := func(raw string) {
		if !first {
			b.WriteString(",")
		}
		first = false
		b.WriteString(raw)
	}
	for _, p := range sp.Prefix {
		add(p)
	}
	userMsg, _ := sjson.Set(`{"role":"user"}`, "content", summaryText(summary))
	ackMsg, _ := sjson.Set(`{"role":"assistant"}`, "content", "Understood. I will continue from that summary.")
	if format == sdktranslator.FormatClaude {
		userMsg, _ = sjson.SetRaw(`{"role":"user"}`, "content", textBlocks(summaryText(summary)))
		ackMsg, _ = sjson.SetRaw(`{"role":"assistant"}`, "content", textBlocks("Understood. I will continue from that summary."))
	}
	add(userMsg)
	add(ackMsg)
	for _, r := range sp.Recent {
		add(r)
	}
	b.WriteString("]")
	return sjson.SetRawBytes(body, "messages", []byte(b.String()))
}

func textBlocks(text string) string {
	block, _ := sjson.Set(`{"type":"text"}`, "text", text)
	return "[" + block + "]"
}

func summaryText(summary string) string {
	return "[Conversation compacted by the proxy because it exceeded the model's context window. Summary of the earlier conversation follows.]\n\n" + summary
}

// Transcript renders middle messages as plain text for the summarizer.
// Thinking blocks are dropped; tool calls and results are kept in short form.
func Transcript(msgs []string) string {
	var b strings.Builder
	for _, raw := range msgs {
		m := gjson.Parse(raw)
		role := m.Get("role").String()
		content := m.Get("content")
		b.WriteString(strings.ToUpper(role))
		b.WriteString(": ")
		if content.Type == gjson.String {
			b.WriteString(content.String())
		} else if content.IsArray() {
			for _, block := range content.Array() {
				switch block.Get("type").String() {
				case "thinking", "redacted_thinking":
					continue
				case "text":
					b.WriteString(block.Get("text").String())
				case "tool_use":
					b.WriteString("[tool_use ")
					b.WriteString(block.Get("name").String())
					b.WriteString(" ")
					b.WriteString(clip(block.Get("input").Raw, 2000))
					b.WriteString("]")
				case "tool_result":
					b.WriteString("[tool_result ")
					b.WriteString(clip(block.Get("content").String(), 2000))
					b.WriteString("]")
				default:
					b.WriteString(clip(block.Raw, 500))
				}
				b.WriteString("\n")
			}
		}
		// OpenAI tool calls.
		if calls := m.Get("tool_calls"); calls.IsArray() {
			for _, c := range calls.Array() {
				b.WriteString("[tool_call ")
				b.WriteString(c.Get("function.name").String())
				b.WriteString(" ")
				b.WriteString(clip(c.Get("function.arguments").String(), 2000))
				b.WriteString("]\n")
			}
		}
		b.WriteString("\n")
	}
	return b.String()
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// HashMessages returns a stable hash of raw messages, used for cache prefix matching.
func HashMessages(msgs []string) string {
	h := sha256.New()
	for _, m := range msgs {
		h.Write([]byte(m))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}
