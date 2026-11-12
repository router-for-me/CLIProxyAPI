package payloadcompress

import (
	"bytes"
	"strings"

	"github.com/MoeForge/moego/ojson"
)

// The marker-protocol note (upstream engines/ccr/protocolInstruction.ts). Large
// blocks the CCR engine replaced carry `[CCR retrieve hash=<24hex> chars=N]`, and
// session dedup writes `[dedup:ref sha=...]`. A model that was never told what a
// marker means cannot recover the content, so one leading `system` note explains
// any marker present in the final body.
//
// Upstream 616dc5bebc moved this out of the CCR engine into a pipeline step that
// runs after the inflation guard: the note is a protocol explanation, not a
// compression, so its fixed size must not count against that guard, and a dedup
// marker is now explained even when CCR replaced nothing.

const (
	// Leading marker identifying the injected note; also the idempotency sentinel.
	ccrProtocolSentinel  = "[CCR protocol]"
	ccrMarkerPrefix      = "[CCR retrieve hash="
	ccrDedupMarkerPrefix = "[dedup:ref sha="
)

const ccrDedupClause = "If you instead see a marker like `[dedup:ref sha=...]`, it means that content already " +
	"appeared earlier in this conversation — look back in the message history for it."

// ccrDedupOnlyInstruction explains a dedup marker without announcing a tool the
// caller cannot reach.
const ccrDedupOnlyInstruction = ccrProtocolSentinel + " Some content in this conversation is replaced by markers " +
	"like `" + ccrDedupMarkerPrefix + "...]`, which means that content already appeared earlier — " +
	"look back in the message history for it."

// ccrRetrieveInstruction is the full note. announcedToolName is the tool name
// exactly as the client advertised it, possibly namespaced by a gateway, so the
// model calls a tool it can actually see.
func ccrRetrieveInstruction(announcedToolName string) string {
	return ccrProtocolSentinel + " This conversation uses content-compression-retrieve (CCR). " +
		"When you see a marker like `" + ccrMarkerPrefix + "<24hex> chars=N]` in a message, it means the full original text (N characters) was stored and " +
		"replaced with this marker to save space — call the `" + announcedToolName + "` tool with that hash to get the original text back verbatim. " +
		"Copy the hash EXACTLY as written — all 24 hexadecimal characters, never truncated, abbreviated, or reformatted — a single wrong character will make the retrieval fail. " +
		ccrDedupClause + " Do NOT call " + announcedToolName + " for a dedup reference."
}

// applyMarkerProtocolNote prepends the note when the final body carries a marker
// to explain, and is a no-op otherwise. It fails open: a body it cannot parse is
// returned untouched.
func applyMarkerProtocolNote(body []byte) []byte {
	// Cheap byte scan first: the common body carries no marker, and parsing every
	// request would cost allocations on the zero-alloc fast path.
	if !bytes.Contains(body, []byte(ccrMarkerPrefix)) && !bytes.Contains(body, []byte(ccrDedupMarkerPrefix)) {
		return body
	}
	doc, path, items, ok := bodyConvItems(body)
	if !ok {
		return body
	}
	hasCcr, hasDedup, hasNote := false, false, false
	for _, item := range items {
		text := messageText(item)
		if strings.Contains(text, ccrProtocolSentinel) {
			hasNote = true
		}
		if strings.Contains(text, ccrMarkerPrefix) {
			hasCcr = true
		}
		if strings.Contains(text, ccrDedupMarkerPrefix) {
			hasDedup = true
		}
	}
	if hasNote {
		return body
	}
	// A CCR marker only ever exists for callers that passed the tool gate, so a
	// marker without a reachable tool has nothing to teach. The note is then
	// chosen by tool reachability alone: an advertised tool earns the full
	// contract even when only a dedup marker is present, so the model is told not
	// to call it for a dedup reference.
	toolName, hasTool := ccrResolveRetrieveToolName(body)
	if !(hasCcr && hasTool) && !hasDedup {
		return body
	}
	note := ccrDedupOnlyInstruction
	if hasTool {
		note = ccrRetrieveInstruction(toolName)
	}
	// Responses `input[]` items carry a `type`; a bare role/content object is not
	// a valid item there, so emit the shape upstream's adapter restores.
	head := `{"role":"system","content":`
	if path == "input" {
		head = `{"type":"message","role":"system","content":`
	}
	raws := make([]string, 0, len(items)+1)
	raws = append(raws, string(appendJSONString([]byte(head), note))+"}")
	for _, item := range items {
		raws = append(raws, item.Raw)
	}
	out, ok := setBodyArray(doc, path, raws)
	if !ok {
		return body
	}
	return out
}

// messageText returns the text a chat item carries, joining text parts.
func messageText(item ojson.Result) string {
	content := item.Get("content")
	if content.Type == ojson.String {
		return content.String()
	}
	if !content.IsArray() {
		return ""
	}
	var b strings.Builder
	for _, part := range content.Array() {
		if text := part.Get("text"); text.Type == ojson.String {
			if b.Len() > 0 {
				b.WriteByte('\n')
			}
			b.WriteString(text.String())
		}
	}
	return b.String()
}

// ccrResolveRetrieveToolName returns the advertised name of the CCR retrieve
// tool, exactly as the client sent it, and whether one was found.
func ccrResolveRetrieveToolName(body []byte) (string, bool) {
	tools := ojson.Get(string(body), "tools")
	if !tools.IsArray() {
		return "", false
	}
	for _, tool := range tools.Array() {
		if name := tool.Get("name").String(); ccrMatchesRetrieveToolName(name) {
			return name, true
		}
		if name := tool.Get("function.name").String(); ccrMatchesRetrieveToolName(name) {
			return name, true
		}
	}
	return "", false
}
