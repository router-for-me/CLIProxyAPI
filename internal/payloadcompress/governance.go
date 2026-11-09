package payloadcompress

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/MoeForge/moego/ojson"
)

// Opt-in safety helpers used by the compression pipeline. All helpers are
// deterministic and fail open: on any doubt they keep the original text.

// applyFidelityGate reverts a compression step when it dropped a token that
// must survive verbatim. Returns the chosen text and whether it reverted.
func applyFidelityGate(before, after string) (string, bool) {
	if before == "" {
		return after, false
	}
	for _, tok := range protectedTokens(before) {
		if !strings.Contains(after, tok) {
			return before, true
		}
	}
	// Upstream's numeric-integrity invariant: every number literal survives.
	for _, num := range fidelityNumericRe.FindAllString(before, -1) {
		if !strings.Contains(after, num) {
			return before, true
		}
	}
	return after, false
}

// protectedTokens extracts the literals a rewrite must not lose.
func protectedTokens(text string) []string {
	seen := make(map[string]struct{})
	var out []string
	add := func(tok string) {
		tok = strings.TrimSpace(tok)
		if len(tok) < 2 {
			return
		}
		if _, ok := seen[tok]; ok {
			return
		}
		seen[tok] = struct{}{}
		out = append(out, tok)
	}
	for _, re := range fidelityPatterns {
		for _, m := range re.FindAllString(text, -1) {
			add(m)
		}
	}
	for _, m := range jsonKeyPattern.FindAllStringSubmatch(text, -1) {
		add(`"` + m[1] + `"`)
	}
	if strings.Contains(text, "diff --git") {
		for _, line := range strings.Split(text, "\n") {
			if len(line) > 1 && (line[0] == '+' || line[0] == '-') && line[1] != line[0] {
				add(line)
			}
		}
	}
	return out
}

var (
	fidelityPatterns = []*regexp.Regexp{
		regexp.MustCompile(`https?://[^\s)\]"'>]+`),
		regexp.MustCompile(`(?:\b[\w.-]+/){1,}[\w.-]+`),
		regexp.MustCompile(`\b[A-Za-z]:\\[^\s"']+`),
		regexp.MustCompile(`(?i)\b\d+(?:\.\d+)?\s?(?:ms|s|secs?|mins?|hrs?|days?|kb|mb|gb|tb|kib|mib|gib|%|px|em|rem|rps|qps|tps)\b`),
		regexp.MustCompile("(?s)```.*?```"),
		regexp.MustCompile(`(?m)^(?:diff --git [^\n]*|@@ [^\n]*)$`),
	}
	jsonKeyPattern = regexp.MustCompile(`"([^"\\\n]{1,80})"\s*:`)
	// fidelityNumericRe mirrors upstream's NUMERIC_RE for the numeric-integrity
	// invariant: a number plus any trailing run of digits/dots/commas.
	fidelityNumericRe = regexp.MustCompile(`\d[\d.,]{0,40}`)
)

// maskRisks replaces secret spans with placeholders. The returned restore
// func maps the placeholders back to the exact original text, so callers can
// compress the masked text and then un-mask the result.
func maskRisks(text string) (string, func(string) string) {
	identity := func(s string) string { return s }
	if text == "" {
		return text, identity
	}
	var spans []riskSpan
	for _, re := range riskPatterns {
		for _, loc := range re.FindAllStringIndex(text, -1) {
			spans = append(spans, riskSpan{start: loc[0], end: loc[1]})
		}
	}
	if len(spans) == 0 {
		return text, identity
	}
	sort.Slice(spans, func(i, j int) bool {
		if spans[i].start != spans[j].start {
			return spans[i].start < spans[j].start
		}
		return spans[i].end > spans[j].end
	})
	merged := spans[:1]
	for _, sp := range spans[1:] {
		last := &merged[len(merged)-1]
		if sp.start <= last.end {
			if sp.end > last.end {
				last.end = sp.end
			}
			continue
		}
		merged = append(merged, sp)
	}
	var b strings.Builder
	originals := make([]string, 0, len(merged))
	last := 0
	for i, sp := range merged {
		b.WriteString(text[last:sp.start])
		originals = append(originals, text[sp.start:sp.end])
		fmt.Fprintf(&b, "\x00OMNI_RISK_%d\x00", i)
		last = sp.end
	}
	b.WriteString(text[last:])
	masked := b.String()
	restore := func(s string) string {
		for i, original := range originals {
			s = strings.ReplaceAll(s, fmt.Sprintf("\x00OMNI_RISK_%d\x00", i), original)
		}
		// A length-based truncation can split a placeholder, so strip any
		// remaining marker fragment instead of leaking it to the provider.
		return riskFragmentRe.ReplaceAllString(s, "")
	}
	return masked, restore
}

type riskSpan struct{ start, end int }

// riskFragmentRe matches a risk placeholder that a truncation cut through,
// including a fragment that lost its leading NUL or trailing NUL.
var riskFragmentRe = regexp.MustCompile(`\x00OMNI_[A-Z_]*[0-9]*|(?:OMNI_)?RISK_[0-9]*\x00`)

// riskPatterns mirror the upstream secret catalog (private key + assignment)
// plus standalone key/token formats and credential-bearing DSNs.
var riskPatterns = []*regexp.Regexp{
	regexp.MustCompile(`-----BEGIN [A-Z0-9 ]{0,40}PRIVATE KEY-----[\s\S]+?-----END [A-Z0-9 ]{0,40}PRIVATE KEY-----`),
	regexp.MustCompile(`(?i)\b(?:api[_-]?key|secret|token|password|passwd|bearer|authorization|client[_-]?secret)\b[ \t]{0,20}[:=][ \t]{0,20}["']?[A-Za-z0-9._\-+/]{8,200}`),
	regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{16,}`),
	regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`),
	regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{20,}`),
	regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9-]{10,}`),
	regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{35}\b`),
	regexp.MustCompile(`(?i)\bBearer\s+[A-Za-z0-9._~+/-]{16,}=*`),
	regexp.MustCompile(`\b[a-z][a-z0-9+.-]*://[^\s:@/]+:[^\s@/]+@[^\s]+`),
	regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}`),
}

// applyHardBudget caps the body at maxTokens by dropping the lowest-saliency
// line/sentence units inside each message. Messages themselves are never
// removed, so tool-call/result structure stays valid (upstream hardBudget.ts).
// protectUntil and protectSystem mirror the scanner's protections: messages at
// or before the cache-prefix cut and preserved system prompts are left intact.
func applyHardBudget(body string, maxTokens, protectUntil int, protectSystem bool) string {
	if maxTokens <= 0 || body == "" {
		return body
	}
	doc := ojson.NewDocString(body)
	msgs := doc.Get("messages")
	if !msgs.IsArray() {
		return body
	}
	array := msgs.Array()
	protected := func(i int) bool {
		if i <= protectUntil {
			return true
		}
		if protectSystem {
			role := doc.Get(fmt.Sprintf("messages.%d.role", i)).String()
			if role == "system" || role == "developer" {
				return true
			}
		}
		return false
	}
	var total, protectedText strings.Builder
	for i := range array {
		content := doc.Get(fmt.Sprintf("messages.%d.content", i))
		text := content.Raw
		if content.Type == ojson.String {
			text = content.String()
		}
		total.WriteString(text)
		total.WriteByte(' ')
		if protected(i) {
			protectedText.WriteString(text)
			protectedText.WriteByte(' ')
		}
	}
	totalTokens := approxTokens(total.String())
	if totalTokens <= maxTokens {
		return body
	}
	protectedTokenCount := approxTokens(protectedText.String())
	budget := maxTokens - protectedTokenCount
	if budget <= 0 {
		// The protected messages alone exceed the budget; trimming anything else
		// cannot get under the cap without corrupting the protected prefix.
		return body
	}
	unprotectedTokens := totalTokens - protectedTokenCount
	changed := false
	for i := range array {
		if protected(i) {
			continue
		}
		path := fmt.Sprintf("messages.%d.content", i)
		content := doc.Get(path)
		if content.Type != ojson.String {
			continue
		}
		text := content.String()
		target := budget
		if unprotectedTokens > 0 {
			target = budget * approxTokens(text) / unprotectedTokens
		}
		out := hardCompressText(text, target)
		if out == text {
			continue
		}
		if errSet := doc.Set(path, out); errSet != nil {
			return body
		}
		changed = true
	}
	if !changed {
		return body
	}
	return doc.String()
}

// hardUnitPreserveRe mirrors upstream UNIT_PRESERVE_RE: units matching it must
// never be dropped.
var hardUnitPreserveRe = regexp.MustCompile(`(?i)\d|https?://|(?:Error|Exception|TypeError|RangeError|SyntaxError|ReferenceError|Traceback):|` + "```" + `|^\s*at\s|/[\w.-]+/|[A-Za-z_]\w*=\S`)

// hardForcePreserveRe mirrors upstream FORCE_PRESERVE_RE.
var hardForcePreserveRe = regexp.MustCompile(`(?i)\d|https?://|[._/\\]|Error:|Exception:|` + "```")

var hardStopwords = map[string]struct{}{
	"a": {}, "an": {}, "the": {}, "is": {}, "are": {}, "was": {}, "were": {},
	"be": {}, "been": {}, "being": {}, "have": {}, "has": {}, "had": {},
	"don't": {}, "will": {}, "would": {}, "could": {}, "should": {},
	"may": {}, "might": {}, "shall": {}, "dare": {}, "ought": {}, "used": {},
	"i": {}, "we": {}, "you": {}, "he": {}, "she": {}, "it": {}, "they": {},
	"me": {}, "us": {}, "him": {}, "her": {}, "them": {}, "my": {}, "our": {},
	"your": {}, "his": {}, "its": {}, "their": {}, "this": {}, "that": {},
	"these": {}, "those": {}, "and": {}, "but": {}, "or": {}, "nor": {},
	"for": {}, "yet": {}, "so": {}, "as": {}, "at": {}, "by": {}, "in": {},
	"of": {}, "on": {}, "to": {}, "up": {}, "via": {}, "with": {}, "from": {},
	"into": {}, "onto": {}, "upon": {}, "about": {}, "just": {}, "very": {},
	"really": {}, "quite": {}, "rather": {}, "also": {}, "too": {}, "even": {},
	"still": {}, "already": {}, "never": {}, "often": {}, "usually": {},
	"sometimes": {}, "here": {}, "there": {},
}

var hardPolarityWords = map[string]struct{}{
	"never": {}, "always": {}, "no": {}, "not": {}, "nor": {},
	"must": {}, "shall": {}, "shall not": {},
	"do": {}, "does": {}, "did": {},
	"don't": {}, "doesn't": {}, "didn't": {},
	"can": {}, "cannot": {}, "can't": {},
	"should": {}, "shouldn't": {},
	"need": {}, "needs": {}, "mustn't": {},
	"won't": {}, "wouldn't": {}, "could": {}, "couldn't": {},
}

type hardUnit struct {
	i        int
	text     string
	score    float64
	preserve bool
}

// hardCompressText drops the lowest-scoring units until text fits target.
func hardCompressText(text string, target int) string {
	if approxTokens(text) <= target {
		return text
	}
	units := hardSplitUnits(text)
	if len(units) <= 1 {
		return text
	}
	tagged := make([]hardUnit, len(units))
	for i, u := range units {
		tagged[i] = hardUnit{
			i:        i,
			text:     u,
			score:    hardScoreUnit(u),
			preserve: hardUnitPreserveRe.MatchString(u),
		}
	}
	dropped := hardDropToTarget(tagged, text, target)
	if len(dropped) == 0 {
		return text
	}
	kept := make([]string, 0, len(tagged))
	for _, u := range tagged {
		if !dropped[u.i] {
			kept = append(kept, u.text)
		}
	}
	return strings.Join(kept, "\n")
}

func hardDropToTarget(units []hardUnit, text string, target int) map[int]bool {
	// Track the rune count instead of summing per-unit token estimates: the
	// rounding of many small units would otherwise let the result drift over
	// the budget. Separators are not subtracted, so the estimate stays high.
	remainingChars := utf8.RuneCountInString(text)
	candidates := make([]hardUnit, 0, len(units))
	for _, u := range units {
		if !u.preserve {
			candidates = append(candidates, u)
		}
	}
	sort.SliceStable(candidates, func(a, b int) bool {
		return candidates[a].score < candidates[b].score
	})
	dropped := make(map[int]bool)
	for _, c := range candidates {
		if remainingChars <= target*4 {
			break
		}
		dropped[c.i] = true
		remainingChars -= utf8.RuneCountInString(c.text)
	}
	return dropped
}

func hardScoreUnit(unit string) float64 {
	words := strings.Fields(unit)
	if len(words) == 0 {
		return 0.5
	}
	total := 0.0
	for _, w := range words {
		total += hardScoreToken(w)
	}
	return total / float64(len(words))
}

func hardScoreToken(token string) float64 {
	if hardForcePreserveRe.MatchString(token) {
		return 1
	}
	lower := strings.ToLower(token)
	if _, ok := hardPolarityWords[lower]; ok {
		return 1
	}
	if _, ok := hardStopwords[lower]; ok {
		return 0.1
	}
	runes := []rune(token)
	if len(runes) <= 2 {
		return 0.2
	}
	if runes[0] >= 'A' && runes[0] <= 'Z' {
		return 0.8
	}
	if len(runes) >= 6 {
		return 0.7
	}
	return 0.5
}

// hardSplitUnits splits text into lines, sentence-splitting long prose lines
// only (upstream splitUnits).
func hardSplitUnits(text string) []string {
	units := make([]string, 0, strings.Count(text, "\n")+1)
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) == "" {
			units = append(units, line)
			continue
		}
		if !hardUnitPreserveRe.MatchString(line) && utf8.RuneCountInString(line) > 60 {
			if sentences := hardSplitSentences(line); len(sentences) > 1 {
				units = append(units, sentences...)
				continue
			}
		}
		units = append(units, line)
	}
	return units
}

// hardSplitSentences splits after sentence punctuation followed by whitespace.
func hardSplitSentences(line string) []string {
	out := make([]string, 0, 2)
	start := 0
	for i := 0; i+1 < len(line); i++ {
		switch line[i] {
		case '.', '!', '?':
			j := i + 1
			for j < len(line) && (line[j] == ' ' || line[j] == '\t' || line[j] == '\r' || line[j] == '\n') {
				j++
			}
			if j > i+1 {
				out = append(out, line[start:i+1])
				start = j
				i = j - 1
			}
		}
	}
	out = append(out, line[start:])
	return out
}

// approxTokens is a cheap deterministic token estimate. Neither tiktoken nor
// cl100k tables are available without a dependency; four runes per token is
// close enough for a hard safety ceiling.
func approxTokens(s string) int {
	return (utf8.RuneCountInString(s) + 3) / 4
}

// isExcluded reports whether model or provider/model matches any glob pattern.
// Matching is case-insensitive and anchored, so `openai/*` and `openai/gpt-4o`
// both work.
func isExcluded(model string, patterns []string, provider ...string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	composite := model
	if len(provider) > 0 {
		if p := strings.ToLower(strings.TrimSpace(provider[0])); p != "" && model != "" {
			composite = p + "/" + model
		}
	}
	if model == "" {
		return false
	}
	for _, pattern := range patterns {
		pattern = strings.ToLower(strings.TrimSpace(pattern))
		if pattern == "" {
			continue
		}
		if ok, err := regexp.MatchString(globRegexp(pattern), model); err == nil && ok {
			return true
		}
		if composite != model {
			if ok, err := regexp.MatchString(globRegexp(pattern), composite); err == nil && ok {
				return true
			}
		}
	}
	return false
}

func globRegexp(pattern string) string {
	var b strings.Builder
	b.WriteString("^")
	for _, r := range pattern {
		switch r {
		case '*':
			b.WriteString(".*")
		case '?':
			b.WriteString(".")
		default:
			b.WriteString(regexp.QuoteMeta(string(r)))
		}
	}
	b.WriteString("$")
	return b.String()
}
