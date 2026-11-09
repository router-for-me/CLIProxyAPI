package payloadcompress

import (
	"crypto/rand"
	"encoding/hex"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Preservation tombstones structured content (code fences, URLs, paths, diffs,
// math, env vars, error text) behind sentinels so a prose pruner never touches
// it. Ported from OmniRoute open-sse/services/compression/preservation.ts.

type preservedBlock struct {
	placeholder string
	content     string
	kind        string
}

type compiledPattern struct {
	pattern *regexp.Regexp
	kind    string
}

const sentinelPrefix = "\x00OMNI_CAVEMAN"

func randomSentinelSeed() string {
	var bytes [8]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "r" + strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	return "r" + hex.EncodeToString(bytes[:])
}

func replacePattern(text string, pattern *regexp.Regexp, kind string, addBlock func(content, kind string) string) string {
	return pattern.ReplaceAllStringFunc(text, func(match string) string {
		if match == "" || strings.Contains(match, sentinelPrefix) {
			return match
		}
		return addBlock(match, kind)
	})
}

// inlineMathRE approximates the upstream lookaround expression; the guards in
// replaceInlineMath re-apply the lookbehind/lookahead checks Go's RE2 cannot.
var inlineMathRE = regexp.MustCompile(`\$(\\.|[^$\n\\]){1,160}\$`)

func replaceInlineMath(text, kind string, addBlock func(content, kind string) string) string {
	locs := inlineMathRE.FindAllStringIndex(text, -1)
	if len(locs) == 0 {
		return text
	}
	var b strings.Builder
	last := 0
	replaced := false
	for _, loc := range locs {
		start, end := loc[0], loc[1]
		if start < last {
			continue
		}
		// (?<!\$)
		if start > 0 && text[start-1] == '$' {
			continue
		}
		// (?=\$)(?![\s$\d]) applied to the opening delimiter
		inner := text[start+1 : end-1]
		if inner == "" {
			continue
		}
		first, _ := utf8.DecodeRuneInString(inner)
		if unicode.IsSpace(first) || first == '$' || unicode.IsDigit(first) {
			continue
		}
		lastRune, _ := utf8.DecodeLastRuneInString(inner)
		if unicode.IsSpace(lastRune) {
			continue
		}
		// (?!$)
		if end < len(text) && text[end] == '$' {
			continue
		}
		b.WriteString(text[last:start])
		b.WriteString(addBlock(text[start:end], kind))
		last = end
		replaced = true
	}
	if !replaced {
		return text
	}
	b.WriteString(text[last:])
	return b.String()
}

var (
	regionSystemReminderRE = regexp.MustCompile(`(?s)<system-reminder>.*?</system-reminder>`)
	regionInstructionsRE   = regexp.MustCompile(`(?s)<instructions?>.*?</instructions?>`)
	regionProjectInstrRE   = regexp.MustCompile(`(?s)<project[- ]instructions?>.*?</project[- ]instructions?>`)
	mathBlockDollarRE      = regexp.MustCompile(`(?s)\$\$.*?\$\$`)
	mathBlockBracketRE     = regexp.MustCompile(`(?s)\\\[.*?\\\]`)
	latexBlockRE           = regexp.MustCompile(`(?s)\\begin\{[A-Za-z*]+\}.*?\\end\{[A-Za-z*]+\}`)
	markdownHeadingRE      = regexp.MustCompile(`(?m)^#{1,6}\s+.+$`)
	markdownTableRE        = regexp.MustCompile(`(?m)^\s*\|.*\|\s*$`)
	markdownTableSepRE     = regexp.MustCompile(`(?m)^\s*\|?\s*:?-{3,}:?\s*(?:\|\s*:?-{3,}:?\s*)+\|?\s*$`)
	typstDirectiveRE       = regexp.MustCompile(`(?m)^\s*#(?:set|show|let|import|include)\b.+$`)
	inlineCodeRE           = regexp.MustCompile("`[^`\\n]+`")
	markdownLinkRE         = regexp.MustCompile(`\[[^\]\n]+\]\([^) \n]+(?:\s+"[^"]*")?\)`)
	urlRE                  = regexp.MustCompile(`(?i)\bhttps?://[^\s)\]"'>]+`)
	constCaseRE            = regexp.MustCompile(`\b[A-Z][A-Z0-9]*(?:_[A-Z0-9]+)+\b`)
	processEnvRE           = regexp.MustCompile(`\bprocess\.env\.[A-Za-z_][A-Za-z0-9_]*\b`)
	shellEnvRE             = regexp.MustCompile(`\$[A-Z_][A-Z0-9_]*\b`)
	versionRE              = regexp.MustCompile(`\b\d+(?:\.\d+){1,3}(?:[-+][A-Za-z0-9.-]+)?\b`)
	dottedIdentifierRE     = regexp.MustCompile(`\b[a-zA-Z_$][\w$]*(?:\.[a-zA-Z_$][\w$]*)+\(\)?`)
	functionCallRE         = regexp.MustCompile(`\b[A-Za-z_$][\w$]*\s*\([^()\n]*\)`)
	filePathRE             = regexp.MustCompile(`(?:^|\s)(?:\.{0,2}/[A-Za-z0-9_@./-]+|[A-Za-z]:\\[A-Za-z0-9_.\\/-]+)`)
	errorMessageRE         = regexp.MustCompile(`\b(?:TypeError|ReferenceError|SyntaxError|RangeError|URIError|EvalError|Error|Exception):[^\n]+`)
	fenceOpenRE            = regexp.MustCompile("^([ \t]{0,3})(`{3,}|~{3,})[^\\n]*(?:\\n|$)")
	fenceCloseRE           = regexp.MustCompile("^([ \t]{0,3})(`{3,}|~{3,})\\s*(?:\\n|$)")
)

// compileUserPatterns compiles caller-supplied preserve patterns, ignoring
// invalid expressions the same way the upstream hot path does.
func compileUserPatterns(patterns []string) []compiledPattern {
	compiled := make([]compiledPattern, 0, len(patterns))
	for _, pattern := range patterns {
		re, err := regexp.Compile(pattern)
		if err != nil {
			continue
		}
		compiled = append(compiled, compiledPattern{pattern: re, kind: "custom"})
	}
	return compiled
}

func splitLinesKeepEnds(text string) []string {
	if text == "" {
		return []string{""}
	}
	lines := make([]string, 0, strings.Count(text, "\n")+1)
	start := 0
	for i := 0; i < len(text); i++ {
		if text[i] == '\n' {
			lines = append(lines, text[start:i+1])
			start = i + 1
		}
	}
	if start < len(text) {
		lines = append(lines, text[start:])
	}
	return lines
}

func extractFencedCodeBlocks(text string, addBlock func(content string) string) string {
	lines := splitLinesKeepEnds(text)
	var output strings.Builder
	i := 0
	for i < len(lines) {
		line := lines[i]
		if line == "" && i == len(lines)-1 {
			break
		}
		opening := fenceOpenRE.FindStringSubmatch(line)
		if opening == nil {
			output.WriteString(line)
			i++
			continue
		}
		fence := opening[2]
		fenceChar := fence[0]
		minLen := len(fence)
		var block strings.Builder
		block.WriteString(line)
		j := i + 1
		closed := false
		for j < len(lines) {
			candidate := lines[j]
			block.WriteString(candidate)
			closeMatch := fenceCloseRE.FindStringSubmatch(candidate)
			if closeMatch != nil && closeMatch[2][0] == fenceChar && len(closeMatch[2]) >= minLen {
				closed = true
				break
			}
			j++
		}
		if !closed {
			output.WriteString(line)
			i++
			continue
		}
		output.WriteString(addBlock(block.String()))
		i = j + 1
	}
	return output.String()
}

func extractFrontmatter(text string, addBlock func(content, kind string) string) string {
	if !strings.HasPrefix(text, "---\n") {
		return text
	}
	close := strings.Index(text[4:], "\n---")
	if close == -1 {
		return text
	}
	end := 4 + close + 4 + 1
	if end > len(text) {
		end = len(text)
	}
	return addBlock(text[:end], "frontmatter") + text[end:]
}

// extractPreservedBlocks replaces structured spans with sentinels and returns
// the rewritten text plus the blocks needed to restore it verbatim.
func extractPreservedBlocks(text string, preservePatterns []string) (string, []preservedBlock) {
	blocks := make([]preservedBlock, 0, 8)
	seed := randomSentinelSeed()
	counter := 0
	addBlock := func(content, kind string) string {
		placeholder := sentinelPrefix + "_" + seed + "_" + strconv.Itoa(counter) + "\x00"
		blocks = append(blocks, preservedBlock{placeholder: placeholder, content: content, kind: kind})
		counter++
		return placeholder
	}

	result := extractFrontmatter(text, addBlock)

	regionPatterns := append(compileUserPatterns(preservePatterns),
		compiledPattern{pattern: regionSystemReminderRE, kind: "system_instruction"},
		compiledPattern{pattern: regionInstructionsRE, kind: "system_instruction"},
		compiledPattern{pattern: regionProjectInstrRE, kind: "system_instruction"},
	)
	for _, p := range regionPatterns {
		result = replacePattern(result, p.pattern, p.kind, addBlock)
	}

	result = extractFencedCodeBlocks(result, func(content string) string {
		return addBlock(content, "fenced_code")
	})

	result = replacePattern(result, mathBlockDollarRE, "math_block", addBlock)
	result = replacePattern(result, mathBlockBracketRE, "math_block", addBlock)
	result = replaceInlineMath(result, "math_inline", addBlock)

	builtIns := []compiledPattern{
		{pattern: latexBlockRE, kind: "latex_block"},
		{pattern: markdownHeadingRE, kind: "markdown_heading"},
		{pattern: markdownTableRE, kind: "markdown_table"},
		{pattern: markdownTableSepRE, kind: "markdown_table"},
		{pattern: typstDirectiveRE, kind: "typst_directive"},
		{pattern: inlineCodeRE, kind: "inline_code"},
		{pattern: markdownLinkRE, kind: "markdown_link"},
		{pattern: urlRE, kind: "url"},
		{pattern: constCaseRE, kind: "const_case"},
		{pattern: processEnvRE, kind: "env_var"},
		{pattern: shellEnvRE, kind: "env_var"},
		{pattern: versionRE, kind: "version"},
		{pattern: dottedIdentifierRE, kind: "dotted_identifier"},
		{pattern: functionCallRE, kind: "function_call"},
		{pattern: filePathRE, kind: "file_path"},
		{pattern: errorMessageRE, kind: "error_message"},
	}
	for _, p := range builtIns {
		result = replacePattern(result, p.pattern, p.kind, addBlock)
	}

	return result, blocks
}

// findFencedCodeBlocks returns the raw fenced code blocks in text.
func findFencedCodeBlocks(text string) []string {
	blocks := make([]string, 0)
	extractFencedCodeBlocks(text, func(content string) string {
		blocks = append(blocks, content)
		return content
	})
	return blocks
}

type span struct {
	start int
	end   int
	kind  string
}

// preserveSpans wraps explicit literal spans by byte offset. Spans must be
// non-overlapping and sorted ascending.
func preserveSpans(text string, spans []span) (string, []preservedBlock) {
	if len(spans) == 0 {
		return text, nil
	}
	seed := randomSentinelSeed()
	blocks := make([]preservedBlock, 0, len(spans))
	var result strings.Builder
	cursor := 0
	counter := 0
	for _, s := range spans {
		if s.start < cursor || s.end > len(text) || s.start >= s.end {
			continue
		}
		placeholder := sentinelPrefix + "_" + seed + "_" + strconv.Itoa(counter) + "\x00"
		blocks = append(blocks, preservedBlock{placeholder: placeholder, content: text[s.start:s.end], kind: s.kind})
		counter++
		result.WriteString(text[cursor:s.start])
		result.WriteString(placeholder)
		cursor = s.end
	}
	result.WriteString(text[cursor:])
	return result.String(), blocks
}

func restorePreservedBlocks(text string, blocks []preservedBlock) string {
	result := text
	for _, b := range blocks {
		result = strings.ReplaceAll(result, b.placeholder, b.content)
	}
	return result
}

func shouldPreserve(text string, preservePatterns []*regexp.Regexp) bool {
	for _, pattern := range preservePatterns {
		if pattern.MatchString(text) {
			return true
		}
	}
	return false
}
