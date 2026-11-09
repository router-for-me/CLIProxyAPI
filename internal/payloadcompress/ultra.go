package payloadcompress

import (
	"regexp"
	"strings"
)

// ultraEngine is the deterministic Tier-A heuristic of OmniRoute's ultra
// compression. The ONNX/llmlingua SLM tier is not available in Go, so this
// engine only prunes prose tokens while preservation keeps structured content
// byte-identical. Chat `messages` bodies only.

const compressedPrefix = "[COMPRESSED:"

// Upstream DEFAULT_ULTRA_CONFIG values (types.ts).
const (
	ultraDefaultCompressionRate      = 0.5
	ultraDefaultMinScoreThreshold    = 0.3
	ultraDefaultMaxTokensPerMessage  = 0
	ultraPreserveSystemPromptDefault = true
)

type ultraEngine struct {
	opts                 StepOptions
	compressionRate      float64
	minScoreThreshold    float64
	maxTokensPerMessage  int
	preserveSystemPrompt bool
}

func newUltraEngine() ultraEngine {
	return ultraEngine{
		compressionRate:      ultraDefaultCompressionRate,
		minScoreThreshold:    ultraDefaultMinScoreThreshold,
		maxTokensPerMessage:  ultraDefaultMaxTokensPerMessage,
		preserveSystemPrompt: ultraPreserveSystemPromptDefault,
	}
}

func (ultraEngine) name() string { return "ultra" }

// compress satisfies textEngine for pipeline callers that do not inspect kinds.
func (e ultraEngine) compress(text string) (string, bool) {
	return e.compressKind(kindUser, text)
}

// compressKind prunes prose on user and assistant turns. System prompts are
// preserved by default; tool output is never touched.
func (e ultraEngine) compressKind(kind segmentKind, text string) (string, bool) {
	switch kind {
	case kindUser, kindUserLatest, kindAssistant:
	default:
		return text, false
	}
	if text == "" || strings.HasPrefix(text, compressedPrefix) {
		return text, false
	}
	if e.maxTokensPerMessage > 0 && (charLen(text)+3)/4 <= e.maxTokensPerMessage {
		return text, false
	}
	out := pruneProseOnly(text, e.compressionRate, e.minScoreThreshold)
	return out, out != text
}

// applyBody compresses the chat messages array and fails open to the original
// body on any unexpected shape. Untouched messages stay byte-identical.
func (e ultraEngine) applyBody(body []byte) (out []byte) {
	out = body
	defer func() {
		if recover() != nil {
			out = body
		}
	}()

	doc, items, ok := bodyMessageItems(body)
	if !ok {
		return body
	}

	raws := make([]string, len(items))
	changed := false
	for i, item := range items {
		raws[i] = item.Raw
		if e.preserveSystemPrompt && item.Get("role").String() == "system" {
			continue
		}
		text := msgText(item.Get("content"))
		if text == "" || strings.HasPrefix(text, compressedPrefix) {
			continue
		}
		if e.maxTokensPerMessage > 0 && (charLen(text)+3)/4 <= e.maxTokensPerMessage {
			continue
		}
		next, mapped := msgMapText(item.Raw, func(part string, _ int) string {
			if part == "" || strings.HasPrefix(part, compressedPrefix) {
				return part
			}
			return pruneProseOnly(part, e.compressionRate, e.minScoreThreshold)
		})
		if mapped && next != raws[i] {
			raws[i] = next
			changed = true
		}
	}
	if !changed {
		return body
	}

	nextBody, ok := setBodyMessages(doc, raws)
	if !ok {
		return body
	}
	return nextBody
}

// pruneProseOnly tombstones structured content, prunes only the prose gaps, and
// re-stitches the preserved blocks verbatim.
func pruneProseOnly(text string, rate, minScore float64) string {
	if text == "" {
		return text
	}
	withPlaceholders, blocks := extractPreservedBlocks(text, nil)
	if len(blocks) == 0 {
		return pruneByScore(text, rate, minScore)
	}

	index := make(map[string]string, len(blocks))
	placeholders := make([]string, 0, len(blocks))
	for _, b := range blocks {
		index[b.placeholder] = b.content
		placeholders = append(placeholders, regexp.QuoteMeta(b.placeholder))
	}
	placeholderRE := regexp.MustCompile(`(` + strings.Join(placeholders, "|") + `)`)

	var b strings.Builder
	cursor := 0
	for _, loc := range placeholderRE.FindAllStringIndex(withPlaceholders, -1) {
		b.WriteString(pruneByScore(withPlaceholders[cursor:loc[0]], rate, minScore))
		b.WriteString(index[withPlaceholders[loc[0]:loc[1]]])
		cursor = loc[1]
	}
	b.WriteString(pruneByScore(withPlaceholders[cursor:], rate, minScore))
	return b.String()
}
