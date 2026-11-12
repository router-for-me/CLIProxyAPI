package payloadcompress

import (
	"strconv"
	"strings"
	"unicode"

	"github.com/MoeForge/moego/ojson"
)

// ccrEngine ports upstream's CCR (Content-Compression-Retrieve) engine. Large
// non-system, non-tool text blocks become `[CCR retrieve hash=<24hex> chars=N]`
// markers backed by the in-process ccr store. Because the marker is only
// resolvable by an MCP caller that advertises omniroute_ccr_retrieve, the whole
// engine (and the protocol instruction it injects) is gated on that tool.
const (
	ccrDefaultMinChars        = 600
	ccrMarkerPreambleChars    = 200
	ccrRetrieveToolName       = "omniroute_ccr_retrieve"
	ccrRetrieveToolSeparators = "__./:_"
)

type ccrEngine struct{}

func newCCREngine() ccrEngine { return ccrEngine{} }

func (ccrEngine) name() string { return "ccr" }

// applyBody rewrites eligible chat message text and prepends the retrieve
// protocol instruction. It fails open and never panics.
func (ccrEngine) applyBody(body []byte) (out []byte) {
	out = body
	defer func() {
		if recover() != nil {
			out = body
		}
	}()
	if !ccrCallerSupportsRetrieve(body) {
		return body
	}
	doc, items, ok := bodyMessageItems(body)
	if !ok {
		return body
	}
	raws := make([]string, len(items))
	replaced := 0
	for i, item := range items {
		raw := item.Raw
		if ccrSkipMessage(item) {
			raws[i] = raw
			continue
		}
		next, changed := msgMapText(raw, func(text string, _ int) string {
			replacement, hit := ccrMaybeReplace(text)
			if hit {
				replaced++
				return replacement
			}
			return text
		})
		if changed {
			raws[i] = next
		} else {
			raws[i] = raw
		}
	}
	if replaced == 0 {
		return body
	}
	newBody, ok := setBodyMessages(doc, raws)
	if !ok {
		return body
	}
	return newBody
}

// ccrMaybeReplace stores a text block and returns its marker replacement. The
// block is only stored when the replacement actually shrinks the text.
func ccrMaybeReplace(text string) (string, bool) {
	chars := charLen(text)
	if chars < ccrDefaultMinChars {
		return "", false
	}
	id := ccrBlockID(text)
	marker := "[CCR retrieve hash=" + id + " chars=" + strconv.Itoa(chars) + "]"
	replacement := ccrBuildReplacementText(text, marker)
	if charLen(replacement) >= chars {
		return "", false
	}
	if _, ok := ccrTryStore(text); !ok {
		return "", false
	}
	if _, ok := RetrieveCCRBlock(id); !ok {
		return "", false
	}
	return replacement, true
}

// ccrBuildReplacementText keeps a short preamble so a caller that cannot
// resolve the marker still sees the start of the original block.
func ccrBuildReplacementText(text, marker string) string {
	runes := []rune(text)
	if len(runes) > ccrMarkerPreambleChars {
		runes = runes[:ccrMarkerPreambleChars]
	}
	preamble := strings.TrimRightFunc(string(runes), unicode.IsSpace)
	return preamble + "… " + marker
}

func ccrSkipMessage(item ojson.Result) bool {
	role := item.Get("role").String()
	if role == "system" || role == "tool" {
		return true
	}
	if role != "user" {
		return false
	}
	content := item.Get("content")
	if !content.IsArray() {
		return false
	}
	parts := content.Array()
	if len(parts) == 0 {
		return false
	}
	for _, part := range parts {
		if !msgIsToolResultBlock(part) {
			return false
		}
	}
	return true
}

// ccrCallerSupportsRetrieve proves via tools[] that the caller can resolve a
// marker before any content is replaced.
func ccrCallerSupportsRetrieve(body []byte) bool {
	tools := ojson.Get(string(body), "tools")
	if !tools.IsArray() {
		return false
	}
	for _, tool := range tools.Array() {
		if ccrMatchesRetrieveToolName(tool.Get("name").String()) ||
			ccrMatchesRetrieveToolName(tool.Get("function.name").String()) {
			return true
		}
	}
	return false
}

func ccrMatchesRetrieveToolName(name string) bool {
	if name == "" {
		return false
	}
	if name == ccrRetrieveToolName {
		return true
	}
	for _, separator := range ccrRetrieveToolSeparators {
		if strings.HasSuffix(name, string(separator)+ccrRetrieveToolName) {
			return true
		}
	}
	return false
}
