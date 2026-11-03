package payloadcompress

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// dedupEngine replaces repeated line-suffix blocks with a content-addressed
// marker, following upstream's session-dedup: pass 1 records the first segment
// that owns each suffix block, pass 2 replaces a duplicate block in a later
// segment with [dedup:ref sha=…]. Repeats across messages and, when the payload
// holds a single text, inside that text are covered; a repeated block that is
// not a suffix (a shared prefix followed by different lines) is not.
//
// The first copy of a block always stays intact, so the content is still in
// context. The current turn is never rewritten: a marker where the model
// expects the result it just asked for reads as a lost read.
type dedupEngine struct {
	opts StepOptions
}

func (*dedupEngine) name() string { return EngineSessionDedup }

const (
	// dedupRefPrefix starts every marker. A segment already carrying one is
	// skipped, which keeps the engine idempotent.
	dedupRefPrefix      = "[dedup:ref sha="
	dedupDefaultMinSize = 80
	// dedupMinLines is upstream's MIN_BLOCK_LINES: fewer lines is not worth a
	// marker, and the first copy is always kept anyway.
	dedupMinLines = 3
	// The caps bound the suffix strings one huge segment can produce (every
	// start line yields a full-length copy). Skipping the tail only forgoes
	// compression, never correctness.
	dedupMaxStarts     = 2000
	dedupMaxBlockBytes = 8 << 20
)

func (e *dedupEngine) rewrite(texts []string, kinds []segmentKind, skips []bool) map[int]string {
	minSize := e.opts.MinBlockChars
	if minSize <= 0 {
		minSize = dedupDefaultMinSize
	}
	// System prompts are never rewritten, and a segment that already carries a
	// marker is finished: this is what makes a second Apply a no-op.
	idx := make([]int, 0, len(texts))
	for i := range texts {
		if kindAt(kinds, i) == kindSystem || strings.HasPrefix(texts[i], dedupRefPrefix) {
			continue
		}
		idx = append(idx, i)
	}
	if len(idx) == 0 {
		return nil
	}
	// One text is the whole payload: the only duplicates it can hold are inside
	// itself, which the cross-message pass cannot see.
	if len(idx) == 1 {
		if skipAt(skips, idx[0]) {
			return nil
		}
		if out, changed := dedupeWithin(texts[idx[0]], minSize); changed {
			return map[int]string{idx[0]: out}
		}
		return nil
	}

	// Pass 1: record the first segment that owns each block. Every segment is
	// recorded, including this turn's, so later turns can still elide against it.
	type owner struct {
		msg   int
		block string
	}
	first := make(map[string]owner, len(idx)*4)
	for _, i := range idx {
		for _, b := range suffixBlocks(texts[i], minSize) {
			if _, ok := first[b.sha]; !ok {
				first[b.sha] = owner{msg: i, block: b.text}
			}
		}
	}

	// Pass 2: replace the longest block that a strictly earlier segment owns.
	// Every block is a suffix, so the longest duplicate covers the shorter ones.
	var repl map[int]string
	for _, i := range idx {
		if skipAt(skips, i) {
			continue
		}
		text := texts[i]
		var longest suffixBlock
		for _, b := range suffixBlocks(text, minSize) {
			o, ok := first[b.sha]
			if !ok || o.msg >= i || o.block != b.text {
				continue
			}
			if len(b.text) > len(longest.text) {
				longest = b
			}
		}
		if longest.text == "" || !strings.HasSuffix(text, longest.text) {
			continue
		}
		if repl == nil {
			repl = make(map[int]string, 1)
		}
		repl[i] = text[:len(text)-len(longest.text)] + dedupRefPrefix + longest.sha + "]"
	}
	return repl
}

// kindAt keeps the engine callable with a bare text slice, which the unit tests
// do; a missing kind is treated as prose.
func kindAt(kinds []segmentKind, i int) segmentKind {
	if i < len(kinds) {
		return kinds[i]
	}
	return kindUser
}

// skipAt reports whether a body engine must leave segment i alone. A short
// slice counts as "nothing to skip", which keeps the callers simple.
func skipAt(skips []bool, i int) bool {
	return i < len(skips) && skips[i]
}

// suffixBlock is one suffix of a segment's lines, with its content hash.
type suffixBlock struct {
	text string
	sha  string
}

// suffixBlocks enumerates the suffix line blocks of text that meet the size
// floor, longest first, dropping identical ones.
func suffixBlocks(text string, minChars int) []suffixBlock {
	lines := strings.Split(text, "\n")
	starts := len(lines)
	if starts > dedupMaxStarts {
		starts = dedupMaxStarts
	}
	blocks := make([]suffixBlock, 0, starts)
	seen := make(map[string]struct{}, starts)
	total := 0
	for start := 0; start < starts; start++ {
		if len(lines)-start < dedupMinLines {
			break
		}
		block := strings.Join(lines[start:], "\n")
		if len(block) < minChars || charLen(block) < minChars {
			continue
		}
		if _, ok := seen[block]; ok {
			continue
		}
		seen[block] = struct{}{}
		blocks = append(blocks, suffixBlock{text: block, sha: blockHash(block)})
		total += len(block)
		if total >= dedupMaxBlockBytes {
			break
		}
	}
	return blocks
}

// blockHash returns the 24 hex characters a marker carries. 96 bits makes a
// collision that points a marker at the wrong block practically impossible.
func blockHash(block string) string {
	sum := sha256.Sum256([]byte(block))
	return hex.EncodeToString(sum[:])[:24]
}

// dedupeWithin replaces every occurrence but the first of a block that repeats
// inside one text. Upstream sorts by frequency first, but its suffix scan has
// already dropped duplicates, so longest-first is the same order.
func dedupeWithin(text string, minChars int) (string, bool) {
	blocks := suffixBlocks(text, minChars)
	if len(blocks) < 2 {
		return text, false
	}
	result := text
	changed := false
	for _, b := range blocks {
		if strings.Count(result, b.text) < 2 {
			continue
		}
		result = replaceAfterFirst(result, b.text, dedupRefPrefix+b.sha+"]")
		changed = true
	}
	return result, changed
}

// replaceAfterFirst keeps the first occurrence of block and replaces the rest
// with marker.
func replaceAfterFirst(text, block, marker string) string {
	first := strings.Index(text, block)
	if first < 0 {
		return text
	}
	var b strings.Builder
	b.Grow(len(text))
	b.WriteString(text[:first+len(block)])
	rest := text[first+len(block):]
	for {
		i := strings.Index(rest, block)
		if i < 0 {
			b.WriteString(rest)
			return b.String()
		}
		b.WriteString(rest[:i])
		b.WriteString(marker)
		rest = rest[i+len(block):]
	}
}
