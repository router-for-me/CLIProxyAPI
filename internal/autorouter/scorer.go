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
	text := extractText(rawJSON, format)
	fields := scoreDimensions(text)
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
	markers := countReasoningMarkers(text)
	scoredTier := tierFor(total, markers, config.Thresholds)
	matched := matchedKeywordRules(text, config.KeywordTierRules)
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

// extractText gathers every piece of message text plus system instructions from
// the request body, joined into a single lowercased string for scoring.
func extractText(rawJSON []byte, format string) string {
	format = strings.ToLower(strings.TrimSpace(format))
	switch format {
	case constant.OpenAI, constant.OpenaiResponse, constant.Claude:
		return extractMessagesAndSystem(rawJSON)
	case constant.Gemini, constant.GeminiInteractions, constant.Interactions:
		return extractGeminiText(rawJSON)
	default:
		// Unknown/empty format: probe for the most specific known shape.
		if gjson.GetBytes(rawJSON, "contents").Exists() {
			return extractGeminiText(rawJSON)
		}
		if gjson.GetBytes(rawJSON, "messages").Exists() || gjson.GetBytes(rawJSON, "input").Exists() {
			return extractMessagesAndSystem(rawJSON)
		}
		return strings.ToLower(gjson.GetBytes(rawJSON, "prompt").String())
	}
}

// extractMessagesAndSystem reads the OpenAI/Claude message arrays (including
// the Responses "input"/"instructions" fields) and joins their text.
func extractMessagesAndSystem(rawJSON []byte) string {
	root := gjson.ParseBytes(rawJSON)
	var parts []string
	if sys := root.Get("system").String(); sys != "" {
		parts = append(parts, sys)
	}
	if instr := root.Get("instructions").String(); instr != "" {
		parts = append(parts, instr)
	}
	for _, msg := range root.Get("messages").Array() {
		if t := extractMessageString(msg.Get("content")); t != "" {
			parts = append(parts, t)
		}
	}
	// Responses format places content in "input" (an array of message objects).
	// Each message may carry text via either a `content` block (string or array
	// of content parts) or a top-level `text` field — the `text` field must be
	// picked up regardless of whether the message also declares a `role`, so a
	// role-less message that holds its prompt body in `text` is not silently
	// dropped from scoring.
	for _, msg := range root.Get("input").Array() {
		if t := extractMessageString(msg.Get("content")); t != "" {
			parts = append(parts, t)
		}
		if t := msg.Get("text").String(); t != "" {
			parts = append(parts, t)
		}
	}
	return strings.ToLower(strings.Join(parts, " "))
}

// extractGeminiText reads text out of Gemini contents[].parts[].text.
func extractGeminiText(rawJSON []byte) string {
	root := gjson.ParseBytes(rawJSON)
	var parts []string
	if sys := root.Get("system_instruction.parts.#.text").String(); sys != "" {
		parts = append(parts, sys)
	}
	for _, t := range root.Get("contents.#.parts.#.text").Array() {
		if s := t.String(); s != "" {
			parts = append(parts, s)
		}
	}
	return strings.ToLower(strings.Join(parts, " "))
}

// scoreDimensions computes the seven 0-1 sub-scores for a lowered text string.
// Each dimension uses a meaningful share of the 0-1 range so the weighted total
// can cross the tier thresholds (0.15 / 0.35 / 0.60) cleanly.
func scoreDimensions(text string) map[ScoreField]float64 {
	fields := map[ScoreField]float64{}
	words := strings.Fields(text)
	wordCount := len(words)

	// Token count: longer bodies carry more context and are harder to answer.
	fields[FieldTokens] = clamp01(float64(wordCount) / 1200.0)

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
	// or identifiers scores high.
	if wordCount > 0 {
		fields[FieldCode] = clamp01((float64(codeTokens) / float64(wordCount)) * 4.0)
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
