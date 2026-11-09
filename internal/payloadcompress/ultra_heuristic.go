package payloadcompress

import (
	"math"
	"regexp"
	"sort"
	"strings"
)

// Heuristic token pruner for ultra mode. Ported from OmniRoute
// open-sse/services/compression/ultraHeuristic.ts. Tokens are scored by
// information density; low-value prose tokens are dropped until the target keep
// rate is reached. Polarity/modality words and structured content never prune.

var stopwords = map[string]struct{}{
	"a": {}, "an": {}, "the": {},
	"is": {}, "are": {}, "was": {}, "were": {}, "be": {}, "been": {}, "being": {},
	"have": {}, "has": {}, "had": {},
	// do/does/did are kept: they carry polarity in imperatives and negations.
	"will": {}, "would": {}, "could": {},
	"may": {}, "might": {}, "shall": {},
	"dare": {}, "ought": {}, "used": {},
	"i": {}, "we": {}, "you": {}, "he": {}, "she": {}, "it": {}, "they": {},
	"me": {}, "us": {}, "him": {}, "her": {}, "them": {},
	"my": {}, "our": {}, "your": {}, "his": {}, "its": {}, "their": {},
	"this": {}, "that": {}, "these": {}, "those": {},
	"and": {}, "but": {}, "or": {},
	// nor is kept: negation word.
	"for": {}, "yet": {}, "so": {}, "as": {}, "at": {}, "by": {}, "in": {},
	"of": {}, "on": {}, "to": {}, "up": {}, "via": {}, "with": {}, "from": {},
	"into": {}, "onto": {}, "upon": {}, "about": {}, "just": {}, "very": {},
	"really": {}, "quite": {}, "rather": {}, "also": {}, "too": {}, "even": {},
	"still": {}, "already": {},
	// always/never are kept: polarity words are the highest-value tokens.
	"often": {}, "usually": {}, "sometimes": {}, "here": {}, "there": {},
}

// forcePreserveRE marks tokens with digits, URLs, path/identifier punctuation,
// or error markers as must-keep.
var forcePreserveRE = regexp.MustCompile("(?i)\\d|https?://|[._/\\\\]|Error:|Exception:|```")

// polarityWords must never be pruned: dropping them flips meaning.
var polarityWords = map[string]struct{}{
	"never": {}, "always": {}, "no": {}, "not": {}, "nor": {},
	"must": {}, "shall": {}, "shall not": {},
	"do": {}, "does": {}, "did": {},
	"don't": {}, "doesn't": {}, "didn't": {},
	"can": {}, "cannot": {}, "can't": {},
	"should": {}, "shouldn't": {},
	"need": {}, "needs": {}, "mustn't": {},
	"won't": {}, "wouldn't": {},
	"could": {}, "couldn't": {},
}

func scoreToken(token string) float64 {
	if forcePreserveRE.MatchString(token) {
		return 1.0
	}
	lower := strings.ToLower(token)
	if _, ok := polarityWords[lower]; ok {
		return 1.0
	}
	if _, ok := stopwords[lower]; ok {
		return 0.1
	}
	if charLen(token) <= 2 {
		return 0.2
	}
	if token[0] >= 'A' && token[0] <= 'Z' {
		return 0.8
	}
	if charLen(token) >= 6 {
		return 0.7
	}
	return 0.5
}

var whitespaceRE = regexp.MustCompile(`\s+`)
var multiSpaceRE = regexp.MustCompile(`[ \t]{2,}`)

// pruneByScore drops prose tokens scoring below minScore until only keepRate of
// the words remain. Whitespace is preserved when tokens are removed.
func pruneByScore(text string, keepRate, minScore float64) string {
	if text == "" || keepRate >= 1 {
		return text
	}

	wordLocs := whitespaceRE.FindAllStringIndex(text, -1)
	// Derive word spans as the gaps between whitespace runs.
	words := make([]string, 0, len(wordLocs)+1)
	starts := make([]int, 0, len(wordLocs)+1)
	cursor := 0
	for _, loc := range wordLocs {
		if cursor < loc[0] {
			words = append(words, text[cursor:loc[0]])
			starts = append(starts, cursor)
		}
		cursor = loc[1]
	}
	if cursor < len(text) {
		words = append(words, text[cursor:])
		starts = append(starts, cursor)
	}
	if len(words) == 0 {
		return text
	}

	targetKeep := int(math.Ceil(float64(len(words)) * keepRate))

	type candidate struct {
		index int
		score float64
	}
	scored := make([]candidate, len(words))
	for i, word := range words {
		scored[i] = candidate{index: i, score: scoreToken(word)}
	}
	sort.SliceStable(scored, func(i, j int) bool {
		if scored[i].score == scored[j].score {
			return scored[i].index < scored[j].index
		}
		return scored[i].score < scored[j].score
	})

	prune := make(map[int]struct{})
	pruned := 0
	for _, c := range scored {
		if pruned >= len(words)-targetKeep {
			break
		}
		if c.score < minScore {
			prune[c.index] = struct{}{}
			pruned++
		}
	}

	var b strings.Builder
	for i, word := range words {
		// Keep the original separator preceding this word.
		sepStart := 0
		if i > 0 {
			sepStart = starts[i-1] + len(words[i-1])
		}
		b.WriteString(text[sepStart:starts[i]])
		if _, dropped := prune[i]; !dropped {
			b.WriteString(word)
		}
	}

	return strings.TrimSpace(multiSpaceRE.ReplaceAllString(b.String(), " "))
}
