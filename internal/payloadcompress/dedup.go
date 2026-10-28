package payloadcompress

import (
	"strconv"
	"strings"
)

// dedupEngine replaces whole segments that are byte-identical to an earlier one.
// Agent loops re-read the same file or re-run the same command constantly; the
// first copy stays in place, so the content is still in context.
type dedupEngine struct {
	opts StepOptions
}

func (*dedupEngine) name() string { return EngineSessionDedup }

const (
	dedupMarker         = "[context-compressed: identical to earlier message"
	dedupDefaultMinSize = 80
	dedupHashSpan       = 4096
)

func (e *dedupEngine) rewrite(texts []string) map[int]string {
	minSize := e.opts.MinBlockChars
	if minSize <= 0 {
		minSize = dedupDefaultMinSize
	}
	seen := make(map[uint64]int, len(texts))
	var repl map[int]string
	for i, text := range texts {
		if len(text) < minSize || strings.HasPrefix(text, dedupMarker) {
			continue
		}
		h := sampledHash(text)
		prev, ok := seen[h]
		if !ok {
			seen[h] = i
			continue
		}
		// Hash collisions are possible: only elide a byte-identical segment.
		if texts[prev] != text {
			continue
		}
		if repl == nil {
			repl = make(map[int]string, 1)
		}
		repl[i] = dedupMarker + " #" + strconv.Itoa(prev+1) + "; " +
			strconv.Itoa(len(text)) + " bytes omitted]"
	}
	return repl
}

// sampledHash hashes at most two 4 KiB windows. Callers verify equality of the
// full text before acting, so a collision only costs a missed deduplication.
func sampledHash(s string) uint64 {
	const (
		basis = uint64(14695981039346656037)
		prime = uint64(1099511628211)
	)
	h := basis
	step := func(b byte) {
		h ^= uint64(b)
		h *= prime
	}
	for i := 0; i < len(s) && i < dedupHashSpan; i++ {
		step(s[i])
	}
	if len(s) > 2*dedupHashSpan {
		for i := len(s) - dedupHashSpan; i < len(s); i++ {
			step(s[i])
		}
	} else if len(s) > dedupHashSpan {
		for i := dedupHashSpan; i < len(s); i++ {
			step(s[i])
		}
	}
	step(byte(len(s)))
	step(byte(len(s) >> 8))
	step(byte(len(s) >> 16))
	return h
}
