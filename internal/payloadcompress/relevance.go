package payloadcompress

import (
	"math"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// relevanceEngine is the opt-in body engine ported from OmniRoute's
// engines/relevance. It keeps the sentences of older user turns that overlap the
// latest user query and drops the rest within a character budget.
type relevanceEngine struct {
	opts StepOptions
}

func (relevanceEngine) name() string { return "relevance" }

// Defaults from OmniRoute configSchema.ts. StepOptions zero values resolve to
// these; boilerplateWeight has no config field and stays at its upstream default.
const (
	relevanceDefaultBudgetPercent    = 0.5
	relevanceDefaultOverlapThreshold = 0.1
	relevanceBoilerplateWeight       = 0.5
)

func relevanceConfig(opts StepOptions) (budgetPercent, overlapThreshold float64) {
	budgetPercent = opts.RelevanceBudgetPercent
	if budgetPercent == 0 {
		budgetPercent = relevanceDefaultBudgetPercent
	}
	overlapThreshold = opts.RelevanceOverlapThreshold
	if overlapThreshold == 0 {
		overlapThreshold = relevanceDefaultOverlapThreshold
	}
	return budgetPercent, overlapThreshold
}

// rewrite scores every user text block against the latest user message, which
// upstream extracts by joining all of its text blocks. System, assistant and
// tool segments are never rewritten. A latest message made of several text
// blocks is left untouched: rewriting each block independently would scramble
// the message.
func (e relevanceEngine) rewrite(texts []string, kinds []segmentKind, skips []bool) map[int]string {
	query := ""
	latestCount := 0
	for i := range texts {
		if kinds[i] == kindUserLatest {
			latestCount++
			if query != "" {
				query += " "
			}
			query += strings.TrimSpace(texts[i])
		}
	}
	if latestCount == 0 {
		for i := len(texts) - 1; i >= 0; i-- {
			if kinds[i] == kindUser {
				query = strings.TrimSpace(texts[i])
				break
			}
		}
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return nil
	}

	budgetPercent, overlapThreshold := relevanceConfig(e.opts)
	var out map[int]string
	for i := range texts {
		latest := kinds[i] == kindUserLatest
		if kinds[i] != kindUser && !latest {
			continue
		}
		if latest && latestCount > 1 {
			continue
		}
		if i < len(skips) && skips[i] {
			continue
		}
		if res, changed := relevanceApply(texts[i], query, budgetPercent, overlapThreshold); changed {
			if out == nil {
				out = make(map[int]string)
			}
			out[i] = res
		}
	}
	return out
}

// relevanceApply runs the upstream sentence keep/drop algorithm on one text.
func relevanceApply(text, query string, budgetPercent, overlapThreshold float64) (string, bool) {
	sentences := relevanceSplitSentences(text)
	if len(sentences) <= 1 {
		return text, false
	}

	scores := relevanceScoreSentences(sentences, query)
	// Approximation: upstream counts UTF-16 code units; runes match that for BMP
	// text and are the closest length measure available cheaply.
	budget := int(math.Floor(float64(utf8.RuneCountInString(text)) * budgetPercent))

	type indexedSentence struct {
		s     string
		i     int
		score float64
	}
	indexed := make([]indexedSentence, len(sentences))
	for i, s := range sentences {
		indexed[i] = indexedSentence{s: s, i: i, score: scores[i]}
	}
	sorted := append([]indexedSentence(nil), indexed...)
	sort.SliceStable(sorted, func(a, b int) bool { return sorted[a].score > sorted[b].score })

	keepSet := make(map[int]bool)
	for _, item := range indexed {
		if relevancePreserveRe.MatchString(item.s) {
			keepSet[item.i] = true
		}
	}

	kept := 0
	for _, item := range sorted {
		if keepSet[item.i] {
			continue
		}
		if kept >= budget {
			break
		}
		if item.score >= overlapThreshold {
			keepSet[item.i] = true
			kept += utf8.RuneCountInString(item.s) + 1
		}
	}

	if len(keepSet) == 0 && len(sorted) > 0 {
		keepSet[sorted[0].i] = true
	}
	if len(keepSet) == len(sentences) {
		return text, false
	}

	ordered := make([]indexedSentence, 0, len(keepSet))
	for _, item := range indexed {
		if keepSet[item.i] {
			ordered = append(ordered, item)
		}
	}
	sort.SliceStable(ordered, func(a, b int) bool { return ordered[a].i < ordered[b].i })

	var b strings.Builder
	for _, item := range ordered {
		b.WriteString(item.s)
	}
	result := b.String()
	return result, result != text
}

// relevancePreserveRe mirrors upstream SENTENCE_PRESERVE_RE. Sentences hitting
// real signals (numbers, URLs, error prefixes, code fences, stack frames, paths,
// key=value) are kept unconditionally and do not consume budget.
var relevancePreserveRe = regexp.MustCompile(
	"(?i)\\d|https?://|(?:Error|Exception|TypeError|RangeError|SyntaxError|ReferenceError|Traceback):|```|^\\s*at\\s|/[\\w.-]+/|\\w+=\\S",
)

// relevanceSplitSentences mirrors splitting on /(?<=[.!?]\s)/: the trailing
// whitespace stays with the preceding sentence.
func relevanceSplitSentences(text string) []string {
	var parts []string
	start := 0
	for i := 0; i < len(text); i++ {
		c := text[i]
		if c != '.' && c != '!' && c != '?' {
			continue
		}
		r, size := utf8.DecodeRuneInString(text[i+1:])
		if size == 0 || !unicode.IsSpace(r) {
			continue
		}
		end := i + 1 + size
		parts = append(parts, text[start:end])
		start = end
		i = end - 1
	}
	if start < len(text) {
		parts = append(parts, text[start:])
	}
	out := parts[:0]
	for _, s := range parts {
		if strings.TrimSpace(s) != "" {
			out = append(out, s)
		}
	}
	return out
}

func relevanceScoreSentences(sentences []string, query string) []float64 {
	scores := make([]float64, len(sentences))
	if strings.TrimSpace(query) == "" {
		return scores
	}
	queryTokens := relevanceTokenSet(query)
	for i, sentence := range sentences {
		tokens := relevanceTokenize(sentence)
		if len(tokens) == 0 {
			continue
		}
		overlap := relevanceJaccard(relevanceSet(tokens), queryTokens)
		boilerplate := relevanceBoilerplateScore(tokens) * relevanceBoilerplateWeight
		if score := overlap - boilerplate; score > 0 {
			scores[i] = score
		}
	}
	return scores
}

// relevanceBoilerplateTokens mirrors upstream BOILERPLATE_TOKENS.
var relevanceBoilerplateTokens = map[string]bool{
	"please": true, "note": true, "important": true, "indeed": true,
	"certainly": true, "basically": true, "essentially": true, "obviously": true,
	"clearly": true, "simply": true, "just": true, "really": true,
	"actually": true, "honestly": true, "conclusion": true, "summary": true,
	"hope": true, "understand": true, "thing": true, "things": true,
	"something": true,
}

func relevanceTokenize(text string) []string {
	lower := strings.ToLower(text)
	var tokens []string
	start := -1
	for i := 0; i <= len(lower); i++ {
		alnum := i < len(lower) && relevanceAlnum(lower[i])
		if alnum {
			if start == -1 {
				start = i
			}
			continue
		}
		if start != -1 {
			tokens = append(tokens, lower[start:i])
			start = -1
		}
	}
	return tokens
}

func relevanceAlnum(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9')
}

func relevanceSet(tokens []string) map[string]bool {
	set := make(map[string]bool, len(tokens))
	for _, t := range tokens {
		set[t] = true
	}
	return set
}

func relevanceTokenSet(text string) map[string]bool {
	return relevanceSet(relevanceTokenize(text))
}

func relevanceJaccard(a, b map[string]bool) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	intersection := 0
	for t := range a {
		if b[t] {
			intersection++
		}
	}
	union := len(a) + len(b) - intersection
	if union == 0 {
		return 0
	}
	return float64(intersection) / float64(union)
}

func relevanceBoilerplateScore(tokens []string) float64 {
	if len(tokens) == 0 {
		return 0
	}
	count := 0
	for _, t := range tokens {
		if relevanceBoilerplateTokens[t] {
			count++
		}
	}
	return float64(count) / float64(len(tokens))
}
