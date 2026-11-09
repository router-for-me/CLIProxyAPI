package payloadcompress

import (
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/MoeForge/moego/ojson"
)

// codexResponsesEngine ports upstream engines/codexResponses: a conservative,
// lossless-first compressor for eligible Responses tool outputs.
//
// Divergence: upstream counts tokens with tiktoken; Go has no tokenizer here,
// so counts use ceil(charLen/4). This only shifts thresholds and stats.
type codexResponsesEngine struct{}

func newCodexResponsesEngine() *codexResponsesEngine { return &codexResponsesEngine{} }

func (*codexResponsesEngine) name() string { return "codex-responses" }

// Upstream DEFAULT_CODEX_RESPONSES_CONFIG.
const (
	codexMinBytes          = 512
	codexMaxOutputBytes    = 2 * 1024 * 1024
	codexMaxCandidateBytes = 512 * 1024
	codexMaxLines          = 160
	codexMinSearchMatches  = 8
	codexMinLogLines       = 24
)

// codexSupportedTypes mirrors upstream SUPPORTED_TYPES. custom_tool_call_output
// is intentionally absent: upstream adapter handles it without engine meta.
var codexSupportedTypes = map[string]bool{
	"function_call_output":    true,
	"local_shell_call_output": true,
	"apply_patch_call_output": true,
}

// codexProtectedToolNames is DEFAULT_CODEX_PROTECTED_TOOL_NAMES plus the
// lowercased DEFAULT_CODEX_RESPONSES_CONFIG.preserveToolNames.
var codexProtectedToolNames = map[string]bool{
	"read":       true,
	"glob":       true,
	"grep":       true,
	"write":      true,
	"edit":       true,
	"websearch":  true,
	"webfetch":   true,
	"web_search": true,
	"web_fetch":  true,
}

var (
	codexDiffHunkRe   = regexp.MustCompile(`(?m)^@@{1,2}\s+-\d+(?:,\d+)?(?:\s+-\d+(?:,\d+)?)*\s+\+\d+(?:,\d+)?(?:,\d+)?\s+@@{1,2}`)
	codexSearchLineRe = regexp.MustCompile(`(?m)^\s*(?:[\w./-]+:\d+(?::\d+)?:|\d+(?::\d+)?[-:])\s*\S`)
	codexImportantRe  = regexp.MustCompile(`(?i)(?:^|\b)(?:error|warning|warn|failed|failure|fatal|panic|exception|traceback|assertion|exit\s+code)\b`)
	codexBuildRe      = regexp.MustCompile(`(?i)\b(?:build|compile|test|lint|npm|yarn|pnpm|mix|make|cargo|gradle|maven|pytest|rspec|exunit)\b`)
	codexStackLineRe  = regexp.MustCompile(`^(?:\s*(?:at\s|File\s|\S+[:(]\d+))`)
	codexPathColonRe  = regexp.MustCompile(`:\d`)
	codexPathDashRe   = regexp.MustCompile(`-\d`)
)

// applyBody compresses eligible Responses input items in place and returns the
// original body when nothing changed or the shape is unsupported.
func (e *codexResponsesEngine) applyBody(body []byte) []byte {
	if len(body) == 0 {
		return body
	}
	doc, path, items, ok := liteBodyArray(body)
	if !ok || path != "input" {
		return body
	}
	calls := codexFunctionCalls(items)
	raws := make([]string, len(items))
	changed := false
	for i, item := range items {
		raws[i] = item.Raw
		typ := item.Get("type").String()
		if !codexSupportedTypes[typ] {
			continue
		}
		if typ == "function_call_output" {
			callID := item.Get("call_id").String()
			name, found := calls[callID]
			if !found || codexProtectedToolNames[name] {
				continue
			}
		}
		text, field, ok := codexOutputText(item)
		if !ok {
			continue
		}
		if typ == "local_shell_call_output" && codexHasLossyRewrite(text) {
			continue
		}
		next, ok := codexRewriteText(text)
		if !ok {
			continue
		}
		raw, ok := codexSetOutputText(item, field, next)
		if !ok {
			continue
		}
		raws[i] = raw
		changed = true
	}
	return liteSetArray(doc, path, raws, changed, body)
}

// codexFunctionCalls maps call_id to the lowercased function name, skipping
// headless retrieval calls like upstream's eligibility pass.
func codexFunctionCalls(items []ojson.Result) map[string]string {
	calls := make(map[string]string, len(items))
	for _, item := range items {
		if item.Get("type").String() != "function_call" {
			continue
		}
		callID := item.Get("call_id").String()
		if callID == "" {
			continue
		}
		name := strings.ToLower(item.Get("name").String())
		if name == "headless_retrieval" || strings.HasSuffix(name, "__headless_retrieval") {
			continue
		}
		calls[callID] = name
	}
	return calls
}

// codexOutputText returns the text upstream feeds to the engine: item.content
// when defined, otherwise item.output. Only string text is eligible; content
// arrays and objects are left untouched, matching the engine's string check.
func codexOutputText(item ojson.Result) (text, field string, ok bool) {
	field = "output"
	if out := item.Get("output"); !out.Exists() || out.Type == ojson.Null {
		field = "content"
	}
	src := item.Get(field)
	if src.Type != ojson.String {
		return "", field, false
	}
	return src.String(), field, true
}

func codexSetOutputText(item ojson.Result, field, text string) (string, bool) {
	sub := ojson.NewDocString(item.Raw)
	if err := sub.Set(field, text); err != nil {
		return "", false
	}
	return sub.String(), true
}

// codexTokenCount is the documented divergence from upstream tiktoken.
func codexTokenCount(s string) int {
	if s == "" {
		return 0
	}
	return (charLen(s) + 3) / 4
}

func codexMinifyJSON(text string) (string, bool) {
	if !ojson.Valid(text) {
		return "", false
	}
	if ojson.Parse(text).Type != ojson.JSON {
		return "", false
	}
	ugly := ojson.Get(text, "@ugly")
	if ugly.Raw == "" {
		return "", false
	}
	return ugly.Raw, true
}

func codexSplitLines(text string) []string {
	return strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
}

func codexCollapseLines(text string) (string, bool) {
	lines := codexSplitLines(text)
	if len(lines) < codexMinLogLines {
		return "", false
	}
	var important []int
	for i, line := range lines {
		if codexImportantRe.MatchString(line) || codexStackLineRe.MatchString(line) {
			important = append(important, i)
		}
	}
	if len(important) == 0 && !codexBuildRe.MatchString(text) {
		return "", false
	}
	keep := make(map[int]bool, len(lines))
	head := 8
	if len(lines) < head {
		head = len(lines)
	}
	for i := 0; i < head; i++ {
		keep[i] = true
	}
	tail := len(lines) - 8
	if tail < 0 {
		tail = 0
	}
	for i := tail; i < len(lines); i++ {
		keep[i] = true
	}
	for _, idx := range important {
		for off := -2; off <= 2; off++ {
			if i := idx + off; i >= 0 && i < len(lines) {
				keep[i] = true
			}
		}
	}
	indexes := make([]int, 0, len(keep))
	for i := range keep {
		indexes = append(indexes, i)
	}
	sort.Ints(indexes)
	if len(indexes) > codexMaxLines {
		indexes = indexes[:codexMaxLines]
	}
	var out []string
	previous := -1
	for _, idx := range indexes {
		if idx-previous > 1 {
			out = append(out, "[compressed log output: omitted "+strconv.Itoa(idx-previous-1)+" lines]")
		}
		out = append(out, lines[idx])
		previous = idx
	}
	if previous < len(lines)-1 {
		out = append(out, "[compressed log output: omitted "+strconv.Itoa(len(lines)-previous-1)+" lines]")
	}
	return strings.Join(out, "\n"), true
}

func codexCompactDiff(text string) (string, bool) {
	if !codexDiffHunkRe.MatchString(text) {
		return "", false
	}
	lines := codexSplitLines(text)
	out := make([]string, 0, len(lines))
	changedSinceContext := 0
	for _, line := range lines {
		changed := strings.HasPrefix(line, "+") || strings.HasPrefix(line, "-") || strings.HasPrefix(line, "@@")
		if changed {
			out = append(out, line)
			changedSinceContext = 0
		} else if changedSinceContext < 2 {
			out = append(out, line)
			changedSinceContext++
		}
	}
	if len(out) < len(lines) && len(out) <= codexMaxLines*4 {
		return strings.Join(out, "\n"), true
	}
	return "", false
}

func codexCompactSearch(text string) (string, bool) {
	lines := codexSplitLines(text)
	var matches []string
	for _, line := range lines {
		if codexSearchLineRe.MatchString(line) {
			matches = append(matches, line)
		}
	}
	if len(matches) < codexMinSearchMatches {
		return "", false
	}
	var order []string
	groups := make(map[string][]string)
	for _, line := range matches {
		trimmed := strings.TrimSpace(line)
		path := codexPathColonRe.Split(trimmed, -1)[0]
		path = codexPathDashRe.Split(path, -1)[0]
		group, seen := groups[path]
		if !seen {
			order = append(order, path)
		}
		if len(group) < 3 {
			group = append(group, trimmed)
		}
		groups[path] = group
	}
	chunks := make([]string, 0, len(order))
	for _, path := range order {
		chunks = append(chunks, strings.Join(append([]string{path}, groups[path]...), "\n"))
	}
	out := strings.Join(chunks, "\n")
	if len(out) < len(text) {
		return out, true
	}
	return "", false
}

func codexCandidateRewrite(text string) (string, bool) {
	if len(text) < codexMinBytes || len(text) > codexMaxCandidateBytes {
		return "", false
	}
	var candidates []string
	if v, ok := codexMinifyJSON(text); ok {
		candidates = append(candidates, v)
	}
	if v, ok := codexCompactDiff(text); ok {
		candidates = append(candidates, v)
	}
	if v, ok := codexCompactSearch(text); ok {
		candidates = append(candidates, v)
	}
	if v, ok := codexCollapseLines(text); ok {
		candidates = append(candidates, v)
	}
	if len(candidates) == 0 {
		return "", false
	}
	sort.SliceStable(candidates, func(i, j int) bool { return len(candidates[i]) < len(candidates[j]) })
	return candidates[0], true
}

func codexRewriteText(value string) (string, bool) {
	if len(value) > codexMaxOutputBytes {
		return "", false
	}
	rewritten, ok := codexCandidateRewrite(value)
	if !ok || len(rewritten) >= len(value) {
		return "", false
	}
	if codexTokenCount(rewritten) >= codexTokenCount(value) {
		return "", false
	}
	return rewritten, true
}

func codexHasLossyRewrite(text string) bool {
	if _, ok := codexCompactDiff(text); ok {
		return true
	}
	if _, ok := codexCompactSearch(text); ok {
		return true
	}
	if _, ok := codexCollapseLines(text); ok {
		return true
	}
	return false
}
