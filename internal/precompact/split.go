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
// verbatim. ok is false when there is nothing to summarize.
func SplitMessages(format sdktranslator.Format, body []byte, keepTurns int) (Split, bool) {
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
	// Find the cut: the keepTurns-th user turn counted from the end.
	cut := -1
	seen := 0
	for i := len(raw) - 1; i >= start; i-- {
		if isUserTurn(format, gjson.Parse(raw[i])) {
			seen++
			if seen == keepTurns {
				cut = i
				break
			}
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
