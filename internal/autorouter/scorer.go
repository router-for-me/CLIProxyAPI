package autorouter

import (
	"strings"
	"unicode"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/constant"
	"github.com/tidwall/gjson"
)

// ScoreResult is the result of scoring one request against an optional
// scoring profile. When Profile is nil the built-in thresholds and weights
// apply.
type ScoreResult struct {
	Score          RequestScore         `json:"score"`
	EffectiveTier  Tier                 `json:"effective_tier"`
	DecisionCause  string               `json:"decision_cause"`
	MatchedRules   []MatchedKeywordRule `json:"matched_rules,omitempty"`
	ProfileVersion int64                `json:"profile_version"`
	ProfileHash    string               `json:"profile_hash"`
	ProfileConfig  ProfileConfig        `json:"profile_config"`
}

// DecisionCause values emitted in ScoreResult.DecisionCause / snapshot.
const (
	DecisionCauseKeywordMatch     = "literal_keyword_match"
	DecisionCauseComplexityScorer = "complexity_scorer"
)

// Score inspects the raw request body using the built-in scoring policy. It
// retains the original RequestScore API for existing callers.
func Score(rawJSON []byte, format string) RequestScore {
	return ScoreWithProfile(rawJSON, format, nil).Score
}

// ScoreWithProfile runs Score using the supplied profile. When profile is nil
// the built-in thresholds and weights apply, which preserves the historical
// behavior for callers that have not yet been migrated.
func ScoreWithProfile(rawJSON []byte, format string, profile *Profile) ScoreResult {
	config := DefaultProfileConfig()
	hash := ""
	version := int64(1)
	if profile != nil {
		config = profile.ProfileConfig
		hash = profile.ProfileHash
		version = profile.ProfileVersion
	}
	ext := extractRequest(rawJSON, format)
	fields := scoreDimensionsStructured(ext)
	weights := config.Weights
	if weights == nil {
		weights = DefaultWeights
	}
	total := 0.0
	norm := 0.0
	for _, f := range []ScoreField{
		FieldTokens, FieldCode, FieldReasoningMark, FieldTechnicalTerms,
		FieldMultiStep, FieldQuestion,
	} {
		weight, ok := weights[f]
		if !ok {
			weight = DefaultWeights[f]
		}
		total += fields[f] * weight
		norm += weight
	}
	simpleWeight, ok := weights[FieldSimpleIndic]
	if !ok {
		simpleWeight = DefaultWeights[FieldSimpleIndic]
	}
	total -= fields[FieldSimpleIndic] * simpleWeight
	if norm > 0 {
		total /= norm
	}
	total = clamp01(total)
	markers := countReasoningMarkers(ext.FlatText)
	scoredTier := tierFor(total, markers, config.Thresholds)
	// Single normalization pass: the flat text is normalized once here and the
	// result feeds keyword matching; the dimensions keep working on the
	// punctuation-preserving lowercase text (looksLikeCode needs the symbols).
	matched := matchedKeywordRules(normalizeKeywordText(ext.FlatText), config.KeywordTierRules)
	effective := scoredTier
	cause := DecisionCauseComplexityScorer
	if len(matched) > 0 {
		effective = scoredTier
		cause = DecisionCauseComplexityScorer
		for _, rule := range matched {
			if tierIndex(rule.Tier) > tierIndex(effective) {
				effective = rule.Tier
			}
		}
		if effective != scoredTier {
			cause = DecisionCauseKeywordMatch
		}
	}
	return ScoreResult{
		Score: RequestScore{
			Fields:           fields,
			Total:            total,
			ReasoningMarkers: markers,
			Tier:             effective,
		},
		EffectiveTier:  effective,
		DecisionCause:  cause,
		MatchedRules:   matched,
		ProfileVersion: version,
		ProfileHash:    hash,
		ProfileConfig:  config,
	}
}

// tierFor maps a weighted total and reasoning-marker count to a tier. Two or
// more independent reasoning markers force REASONING regardless of the score.
func tierFor(total float64, reasonerMarkers int, configured ...TierThresholds) Tier {
	thresholds := TierThresholds{SimpleMax: SimpleMax, MediumMax: MediumMax, ComplexMax: ComplexMax}
	if len(configured) > 0 {
		thresholds = configured[0]
	}
	if reasonerMarkers >= ReasonerMarkerThreshold {
		return TierReasoning
	}
	switch {
	case total < thresholds.SimpleMax:
		return TierSimple
	case total < thresholds.MediumMax:
		return TierMedium
	case total <= thresholds.ComplexMax:
		return TierComplex
	default:
		return TierReasoning
	}
}

func tierIndex(t Tier) int {
	for i, candidate := range TierOrder {
		if candidate == t {
			return i
		}
	}
	return -1
}

// extractMessageString renders a single message content value (a plain string
// or an array of content blocks) into a whitespace-joined text string.
func extractMessageString(content gjson.Result) string {
	if !content.Exists() {
		return ""
	}
	if content.IsArray() {
		var sb strings.Builder
		for _, block := range content.Array() {
			if t := block.Get("text").String(); t != "" {
				sb.WriteString(t)
				sb.WriteByte(' ')
			}
			// Image/media blocks carry no text; skipped.
		}
		return sb.String()
	}
	return content.String()
}

// extractedRequest is the structured result of parsing one request body:
// everything the scorer needs, extracted once. It replaces the single
// lowercased flat string that extractText used to return.
type extractedRequest struct {
	// FlatText is the whitespace-joined, lowercased text of every message plus
	// system instructions (the historical extractText output).
	FlatText string
	// LatestUserText is the lowercased text of the final message whose role is
	// "user" (empty when the format has no notion of roles, e.g. Gemini).
	LatestUserText string
	// HistoryTokens is the lowercased word count of everything except the
	// latest user turn (system + prior turns + assistant replies).
	HistoryTokens int
	// CodeFenceTokens is the raw (non-lowercased) whitespace-token count of
	// fenced code blocks (``` … ```) and multi-line JSON/XML-looking literals
	// found anywhere in message text.
	CodeFenceTokens int
}

// extractRequest parses the raw body into an extractedRequest according to the
// entry protocol format, mirroring the format dispatch of the former
// extractText. One walk yields the flat scoring text, the role split, and the
// fenced-code payload size.
func extractRequest(rawJSON []byte, format string) extractedRequest {
	format = strings.ToLower(strings.TrimSpace(format))
	switch format {
	case constant.OpenAI, constant.OpenaiResponse, constant.Claude:
		return extractChatRequest(rawJSON)
	case constant.Gemini, constant.GeminiInteractions, constant.Interactions:
		return extractGeminiRequest(rawJSON)
	default:
		// Unknown/empty format: probe for the most specific known shape.
		if gjson.GetBytes(rawJSON, "contents").Exists() {
			return extractGeminiRequest(rawJSON)
		}
		if gjson.GetBytes(rawJSON, "messages").Exists() || gjson.GetBytes(rawJSON, "input").Exists() {
			return extractChatRequest(rawJSON)
		}
		flat := strings.ToLower(gjson.GetBytes(rawJSON, "prompt").String())
		return extractedRequest{FlatText: flat, LatestUserText: flat}
	}
}

// extractChatRequest walks the OpenAI/Claude message arrays (including the
// Responses "input"/"instructions" fields) once, accumulating the lowercased
// flat text, tracking the final user turn, and sizing the history word count.
// Responses-format messages carry text via either a `content` block (string or
// array of content parts) or a top-level `text` field — the `text` field is
// picked up regardless of whether the message also declares a `role`, so a
// role-less message that holds its prompt body in `text` is not silently
// dropped from scoring.
func extractChatRequest(rawJSON []byte) extractedRequest {
	root := gjson.ParseBytes(rawJSON)
	var ext extractedRequest
	var flat strings.Builder
	collect := func(role, text string) {
		if text == "" {
			return
		}
		lower := strings.ToLower(text)
		flat.WriteString(lower)
		flat.WriteByte(' ')
		if role == "user" {
			ext.LatestUserText = lower
			return
		}
		ext.HistoryTokens += len(strings.Fields(lower))
	}
	if sys := root.Get("system").String(); sys != "" {
		collect("system", sys)
	}
	if instr := root.Get("instructions").String(); instr != "" {
		collect("system", instr)
	}
	for _, msg := range root.Get("messages").Array() {
		collect(strings.ToLower(msg.Get("role").String()), extractMessageString(msg.Get("content")))
	}
	for _, msg := range root.Get("input").Array() {
		role := strings.ToLower(msg.Get("role").String())
		text := extractMessageString(msg.Get("content"))
		if t := msg.Get("text").String(); t != "" {
			if text != "" {
				text += " "
			}
			text += t
		}
		collect(role, text)
	}
	ext.FlatText = strings.TrimSpace(flat.String())
	if ext.LatestUserText != "" {
		ext.CodeFenceTokens = countFenceTokens(ext.LatestUserText)
	}
	return ext
}

// extractGeminiRequest reads text out of Gemini contents[].parts[].text. The
// format has no per-part roles in practice, so LatestUserText stays empty and
// every part counts as history — the token dimension then falls back to the
// flat text (see scoreDimensions).
func extractGeminiRequest(rawJSON []byte) extractedRequest {
	root := gjson.ParseBytes(rawJSON)
	var ext extractedRequest
	var flat strings.Builder
	var raw strings.Builder
	if sys := root.Get("system_instruction.parts.#.text").String(); sys != "" {
		flat.WriteString(strings.ToLower(sys))
		flat.WriteByte(' ')
		raw.WriteString(sys)
		raw.WriteByte('\n')
		ext.HistoryTokens += len(strings.Fields(sys))
	}
	for _, t := range root.Get("contents.#.parts.#.text").Array() {
		if s := t.String(); s != "" {
			flat.WriteString(strings.ToLower(s))
			flat.WriteByte(' ')
			raw.WriteString(s)
			raw.WriteByte('\n')
			ext.HistoryTokens += len(strings.Fields(s))
		}
	}
	ext.FlatText = strings.TrimSpace(flat.String())
	ext.CodeFenceTokens = countFenceTokens(raw.String())
	return ext
}

// countFenceTokens counts whitespace-separated tokens inside fenced code
// regions of the raw (non-lowercased) text: ``` fenced blocks and multi-line
// brace/bracket literals (a line opening with "{" or "[" whose block spans at
// least two further indented lines and closes). Single-line braces in prose do
// not count. Deterministic and allocation-light (no slices kept).
func countFenceTokens(text string) int {
	const fenceMarker = "```"
	total := 0
	inFence := false
	var literal strings.Builder
	// literalDepth tracks brace/bracket nesting across consecutive lines for the
	// non-fenced literal detector; >0 means we are inside a multi-line literal.
	literalDepth := 0
	literalLines := 0
	flushLiteral := func() {
		total += len(strings.Fields(literal.String()))
		literal.Reset()
		literalLines = 0
		literalDepth = 0
	}
	for line := range strings.Lines(text) {
		trimmed := strings.TrimRight(line, "\r\n")
		if strings.HasPrefix(strings.TrimSpace(trimmed), fenceMarker) {
			if inFence {
				// Closing fence: flush the accumulated block.
				total += len(strings.Fields(literal.String()))
				literal.Reset()
				inFence = false
			} else {
				// Opening fence: flush any literal state first.
				flushLiteral()
				inFence = true
			}
			continue
		}
		if inFence {
			literal.WriteString(trimmed)
			literal.WriteByte(' ')
			continue
		}
		// Multi-line brace/bracket literal detection outside fences.
		openCount := strings.Count(trimmed, "{") + strings.Count(trimmed, "[")
		closeCount := strings.Count(trimmed, "}") + strings.Count(trimmed, "]")
		indent := strings.TrimSpace(trimmed)
		opensLiteral := literalDepth == 0 && openCount > closeCount &&
			(strings.HasPrefix(indent, "{") || strings.HasPrefix(indent, "["))
		if opensLiteral {
			literalDepth = openCount - closeCount
			literalLines = 1
			literal.WriteString(trimmed)
			literal.WriteByte(' ')
			continue
		}
		if literalDepth > 0 {
			literal.WriteString(trimmed)
			literal.WriteByte(' ')
			literalLines++
			literalDepth += openCount - closeCount
			if literalDepth <= 0 {
				if literalLines >= 3 {
					total += len(strings.Fields(literal.String()))
				}
				flushLiteral()
			}
		}
	}
	// Unterminated block at EOF: treat as literal only when it spanned enough
	// lines to look like real code/JSON rather than a stray brace.
	if literalDepth > 0 && literalLines >= 3 {
		total += len(strings.Fields(literal.String()))
	} else if inFence && literal.Len() > 0 {
		total += len(strings.Fields(literal.String()))
	}
	return total
}

// scoreDimensions computes the seven 0-1 sub-scores for a lowered text string.
// Each dimension uses a meaningful share of the 0-1 range so the weighted total
// can cross the tier thresholds (0.15 / 0.35 / 0.60) cleanly.
func scoreDimensions(text string) map[ScoreField]float64 {
	return scoreDimensionsWithFence(text, 0)
}

// scoreDimensionsWithFence computes the seven dimensions for a lowered text
// plus an optional pre-extracted fenced-code token count (0 = none).
func scoreDimensionsWithFence(text string, fenceTokens int) map[ScoreField]float64 {
	fields := map[ScoreField]float64{}
	words := strings.Fields(text)
	wordCount := len(words)

	// Token count: longer bodies carry more context and are harder to answer.
	fields[FieldTokens] = clamp01(float64(wordCount) / 1200.0)

	// Role-aware token share is applied by scoreDimensionsStructured (see
	// below): when the request carries roles, the latest user turn drives the
	// token dimension and history (system + prior turns) is capped at 20% so a
	// huge agent system prompt cannot push a short user request into a hard
	// tier. Formats without roles keep the flat computation above.

	codeTokens := 0
	simpleInd := 0
	technical := 0
	hardTask := 0
	mediumTask := 0
	reasoningTokens := 0

	for _, w := range words {
		if looksLikeCode(w) {
			codeTokens++
		}
		if simpleIndicatorSet[w] {
			simpleInd++
		}
		if technicalTermSet[w] {
			technical++
		}
		if hardTaskSet[w] {
			hardTask++
		}
		if mediumTaskSet[w] {
			mediumTask++
		}
		if reasoningMarkerSet[w] {
			reasoningTokens++
		}
	}

	// Code: density of code-like tokens. A request that is largely source code
	// or identifiers scores high. Fenced/literal code blocks count directly
	// toward the dimension as well: a request whose payload is mostly source
	// inside fences is a code request even when the fenced tokens are plain
	// words the density path cannot see (the fence is taken via max() so the
	// two signals combine without double-counting). The 1.5 factor means
	// fences matching ~2/3 of the body saturate the dimension.
	if wordCount > 0 {
		density := clamp01((float64(codeTokens) / float64(wordCount)) * 4.0)
		fence := clamp01((float64(fenceTokens) / float64(wordCount)) * 1.5)
		fields[FieldCode] = max(density, fence)
	}
	// Reasoning: explicit analytical words saturate around ~3 tokens.
	fields[FieldReasoningMark] = clamp01(float64(reasoningTokens) / 3.0)
	// Simple indicator penalty and technical density.
	fields[FieldSimpleIndic] = clamp01(float64(simpleInd) / 5.0)
	fields[FieldTechnicalTerms] = clamp01(float64(technical) / 8.0)

	// Multi-step / implementation burden: driven primarily by whether the
	// request asks for a concrete engineering task (a hard verb) vs a plain
	// generation request (a medium verb) vs nothing at all. Sequence words add
	// a little on top.
	multiStep := 0.0
	if hardTask > 0 {
		multiStep = 0.85
	} else if mediumTask > 0 {
		multiStep = 0.6
	}
	for _, w := range words {
		if multiStepSet[w] {
			multiStep += 0.08
		}
	}
	fields[FieldMultiStep] = clamp01(multiStep)

	// Question complexity: base for a real objective, plus wh/analytical cues
	// and task verbs. A hard engineering task contributes more than a medium
	// generation request, which in turn contributes more than a trivial prompt.
	q := 0.15
	for _, w := range words {
		if questionCueSet[w] {
			q += 0.10
		}
		if conditionalSet[w] {
			q += 0.06
		}
		if mediumTaskSet[w] {
			q += 0.12
		}
		if hardTaskSet[w] {
			q += 0.18
		}
	}
	if strings.Contains(text, "?") {
		q += 0.10
	}
	fields[FieldQuestion] = clamp01(q)
	return fields
}

// scoreDimensionsStructured computes the seven dimensions from a structured
// extraction: all dimensions run on the flat text as before, then the token
// dimension is overridden with the role-aware split (latest user turn leading,
// history contribution at 20%). When the extraction carries no user turn (no
// roles in the format), the flat token computation stands.
func scoreDimensionsStructured(ext extractedRequest) map[ScoreField]float64 {
	fields := scoreDimensionsWithFence(ext.FlatText, ext.CodeFenceTokens)
	if userWords := len(strings.Fields(ext.LatestUserText)); userWords > 0 {
		history := 0.2 * float64(ext.HistoryTokens)
		fields[FieldTokens] = clamp01((float64(userWords) + history) / 1200.0)
	}
	return fields
}

// countReasoningMarkers returns the number of independent reasoning cue
// categories present in the text (deduplicated per category) — used to shortcut
// to the REASONING tier regardless of the weighted score. Only genuine
// analytical/mathematical/concurrency-correctness cues count; ordinary requests
// (even with code or architecture words) stay below the threshold.
func countReasoningMarkers(text string) int {
	count := 0
	for _, kw := range []string{
		// Formal / mathematical proof and derivation.
		"prove", "proof", "derive", "theorem", "mathematical", "induction",
		"contradiction", "closed form",
		// Concurrency & correctness reasoning.
		"deadlock", "race condition", "concurrent", "synchronization",
		"happens-before", "memory model", "atomicity",
		// Complexity / asymptotic analysis.
		"algorithm complexity", "complexity analysis", "asymptotic", "big o",
		// Rigorous justification.
		"justify rigorously", "formally", "rigorously",
		// Deliberate trade-off analysis (hyphenated form only, to avoid
		// over-triggering on casual "tradeoffs").
		"trade-off",
	} {
		if strings.Contains(text, kw) {
			count++
		}
	}
	// A cluster of three or more distinct analytical verbs also signals a
	// reasoning task.
	analytical := 0
	for _, kw := range []string{"explain", "analyze", "compare", "evaluate", "reason"} {
		if strings.Contains(text, kw) {
			analytical++
		}
	}
	if analytical >= 3 {
		count++
	}
	return count
}

// looksLikeCode reports whether a token is predominantly made of code
// punctuation (brackets, operators, etc.) or carries camelCase/acronym style
// that indicates source code.
func looksLikeCode(w string) bool {
	if w == "" {
		return false
	}
	letters := 0
	syms := 0
	for _, r := range w {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			letters++
		} else if !unicode.IsSpace(r) {
			syms++
		}
	}
	if letters > 0 && syms >= letters {
		return true
	}
	// camelCase or snake_with separators and digits strongly suggest identifiers.
	return letters > 1 && syms > 0 && (strings.Contains(w, "_") || strings.Contains(w, "(") || strings.Contains(w, ")") || strings.ContainsAny(w, "{}[];=<>"))
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

var reasoningMarkerSet = setOf(
	"explain", "analyze", "reasoning", "reason", "why", "evaluate", "derive",
	"prove", "proof", "logic", "logical", "hypothes", "theorem", "argument",
	"inference", "conclude", "combination", "permutation", "probability",
	"consequently", "therefore", "thus", "since", "implication",
)

var simpleIndicatorSet = setOf(
	"hi", "hello", "hey", "thanks", "thank", "ok", "okay", "yes", "no",
	"greet", "greeting", "short", "simple", "quick", "brief", "please",
	"just", "fast", "easy", "basic", "trivial", "small", "little", "couple",
)

var multiStepSet = setOf(
	"first", "then", "next", "plan", "steps", "step", "procedure", "workflow",
	"pipeline", "sequence", "order", "phase", "stage", "iterate", "recursive",
	"recursion", "loop", "parse", "orchestrate", "coordinate", "dependencies",
)

// hardTaskSet lists verbs/requests that demand substantial implementation or
// engineering effort. These drive the multi-step and question dimensions so a
// real coding/architecture request reliably lands in COMPLEX or above.
var hardTaskSet = setOf(
	"refactor", "implement", "design", "build", "deploy", "migrate", "test",
	"optimize", "debug", "architect", "integrate", "configure", "monitor",
	"secure", "scale", "write", "create", "develop",
)

// mediumTaskSet lists single-objective generation tasks that should land in
// MEDIUM rather than SIMPLE (summarize/write/translate/... is a real request,
// not a trivial greeting).
var mediumTaskSet = setOf(
	"summarize", "write", "translate", "draft", "describe", "explain",
	"paraphrase", "rewrite", "outline", "define", "clarify", "list", "generate",
)

var technicalTermSet = setOf(
	"api", "http", "tcp", "udp", "json", "xml", "yaml", "sql", "database",
	"schema", "index", "query", "cache", "redis", "kafka", "docker", "kubernetes",
	"k8s", "nginx", "postgres", "mysql", "mongodb", "grpc", "rest", "graphql",
	"oauth", "jwt", "ssl", "tls", "dns", "cpu", "memory", "gpu", "latency",
	"throughput", "concurrency", "goroutine", "thread", "process", "kernel",
	"compiler", "runtime", "binary", "protocol", "endpoint", "middleware",
	"authentication", "authorization", "encryption", "hash", "compression",
)

var questionCueSet = setOf(
	"how", "why", "explain", "analyze", "compare", "evaluate", "derive",
	"prove", "design", "implement", "debug", "optimize", "what", "which",
	"discuss", "describe", "justify", "predict",
)

var conditionalSet = setOf(
	"if", "then", "otherwise", "assuming", "given", "when", "unless",
	"provided", "suppose",
)

func setOf(items ...string) map[string]bool {
	m := make(map[string]bool, len(items))
	for _, it := range items {
		m[it] = true
	}
	return m
}
