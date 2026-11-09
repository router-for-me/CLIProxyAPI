package payloadcompress

import (
	"regexp"
	"strings"

	"github.com/MoeForge/moego/ojson"
)

// Rule-based summarizer ported from upstream summarizer.ts. It never calls a
// model: it extracts the user's intents, touched files, error snippets and the
// last decision into a compact "[COMPRESSED:summary] ..." marker.

const summaryMarker = "[COMPRESSED:summary]"

var (
	summaryIntentRe = regexp.MustCompile(`(?i)^(?:request|fix|implement|add|remove|update|refactor|create|delete|change|build)\s*:`)

	summaryFileExt = map[string]struct{}{
		"ts": {}, "tsx": {}, "js": {}, "jsx": {}, "py": {}, "md": {}, "json": {},
		"sql": {}, "css": {}, "html": {}, "yaml": {}, "yml": {}, "sh": {}, "rb": {},
		"go": {}, "rs": {}, "java": {}, "c": {}, "cpp": {}, "h": {}, "hpp": {},
	}
)

// summarizeRuleBased builds a bounded summary for the given chat messages.
func summarizeRuleBased(messages []ojson.Result, maxLen int, preserveCode bool) string {
	filtered := make([]ojson.Result, 0, len(messages))
	for _, item := range messages {
		text := msgText(item.Get("content"))
		if strings.TrimSpace(text) == "" {
			continue
		}
		if strings.HasPrefix(text, compressedPrefix) {
			continue
		}
		filtered = append(filtered, item)
	}
	if len(filtered) == 0 {
		return ""
	}

	intents := summaryExtractIntents(filtered)
	files := summaryExtractFilePaths(filtered)
	errors := summaryExtractErrors(filtered)
	decision := summaryExtractLastDecision(filtered)

	parts := []string{summaryMarker}
	if len(intents) > 0 {
		parts = append(parts, "Intents: "+strings.Join(intents, "; ")+".")
	}
	if len(files) > 0 {
		parts = append(parts, "Files touched: "+strings.Join(files, ", ")+".")
	}
	if len(errors) > 0 {
		parts = append(parts, "Errors: "+strings.Join(errors, "; ")+".")
	}
	if decision != "" {
		processed := decision
		if preserveCode {
			processed = summaryTrimCodeFences(decision)
		}
		parts = append(parts, "Last decision: "+summaryTruncChars(processed, 200)+".")
	}

	result := strings.Join(parts, " ")
	if maxLen > 0 && charLen(result) > maxLen {
		result = summaryTruncChars(result, maxLen-3) + "..."
	}
	return result
}

func summaryExtractIntents(messages []ojson.Result) []string {
	var intents []string
	for _, item := range messages {
		if item.Get("role").String() != "user" {
			continue
		}
		text := msgText(item.Get("content"))
		if text == "" {
			continue
		}
		firstLine := text
		if idx := strings.IndexByte(text, '\n'); idx >= 0 {
			firstLine = text[:idx]
		}
		firstLine = strings.TrimSpace(firstLine)
		if firstLine != "" && summaryIntentRe.MatchString(firstLine) {
			intents = append(intents, summaryTruncChars(firstLine, 120))
		} else if len(intents) == 0 && firstLine != "" {
			intents = append(intents, summaryTruncChars(firstLine, 120))
		}
	}
	return intents
}

func summaryExtractFilePaths(messages []ojson.Result) []string {
	seen := make(map[string]struct{})
	var paths []string
	for _, item := range messages {
		text := msgText(item.Get("content"))
		if text == "" {
			continue
		}
		for _, token := range strings.Fields(text) {
			path, ok := summaryKnownFilePath(token)
			if !ok {
				continue
			}
			if _, dup := seen[path]; dup {
				continue
			}
			seen[path] = struct{}{}
			paths = append(paths, path)
		}
	}
	if len(paths) > 20 {
		paths = paths[:20]
	}
	return paths
}

func summaryExtractErrors(messages []ojson.Result) []string {
	var errors []string
	for _, item := range messages {
		text := msgText(item.Get("content"))
		if text == "" {
			continue
		}
		for _, snippet := range summaryErrorSnippets(text) {
			errors = append(errors, summaryTruncChars(snippet, 150))
		}
	}
	if len(errors) > 10 {
		errors = errors[:10]
	}
	return errors
}

func summaryExtractLastDecision(messages []ojson.Result) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Get("role").String() != "assistant" {
			continue
		}
		text := msgText(messages[i].Get("content"))
		if text != "" {
			return text
		}
	}
	return ""
}

func summaryErrorSnippets(text string) []string {
	var snippets []string
	for _, segment := range summarySplitErrors(text) {
		trimmed := strings.TrimSpace(segment)
		if trimmed == "" {
			continue
		}
		lower := strings.ToLower(trimmed)
		if strings.Contains(trimmed, "TypeError:") ||
			strings.Contains(trimmed, "ReferenceError:") ||
			strings.Contains(trimmed, "SyntaxError:") ||
			strings.Contains(trimmed, "RangeError:") ||
			strings.Contains(trimmed, "URIError:") ||
			strings.Contains(trimmed, "EvalError:") ||
			strings.Contains(trimmed, "Error:") ||
			strings.Contains(trimmed, "Exception:") ||
			strings.Contains(lower, "error ts") {
			snippets = append(snippets, trimmed)
		}
	}
	return snippets
}

// summarySplitErrors splits on dots and newlines like upstream's /[.\n]/.
func summarySplitErrors(text string) []string {
	return strings.FieldsFunc(text, func(r rune) bool { return r == '.' || r == '\n' })
}

func summaryKnownFilePath(token string) (string, bool) {
	clean := summaryStripTokenPunctuation(token)
	lastDot := strings.LastIndexByte(clean, '.')
	if lastDot <= 0 || lastDot == len(clean)-1 {
		return "", false
	}
	ext := strings.ToLower(clean[lastDot+1:])
	if _, ok := summaryFileExt[ext]; !ok {
		return "", false
	}
	if !strings.Contains(clean, "/") && !strings.Contains(clean, ".") {
		return "", false
	}
	return clean, true
}

func summaryStripTokenPunctuation(token string) string {
	const leading = "'\"`([{"
	const trailing = "'\"`)]},;:"
	start := 0
	end := len(token)
	for start < end && strings.IndexByte(leading, token[start]) >= 0 {
		start++
	}
	for end > start && strings.IndexByte(trailing, token[end-1]) >= 0 {
		end--
	}
	return token[start:end]
}

func summaryTrimCodeFences(text string) string {
	var b strings.Builder
	cursor := 0
	for cursor < len(text) {
		start := strings.Index(text[cursor:], "```")
		if start < 0 {
			b.WriteString(text[cursor:])
			break
		}
		start += cursor
		openingRel := strings.IndexByte(text[start+3:], '\n')
		if openingRel < 0 {
			b.WriteString(text[cursor:])
			break
		}
		openingEnd := start + 3 + openingRel
		closeRel := strings.Index(text[openingEnd+1:], "\n```")
		if closeRel < 0 {
			b.WriteString(text[cursor:])
			break
		}
		closeStart := openingEnd + 1 + closeRel
		closeEnd := closeStart + 4
		opening := text[start:openingEnd]
		code := text[openingEnd+1 : closeStart]
		lines := strings.Split(code, "\n")

		b.WriteString(text[cursor:start])
		if len(lines) <= 4 {
			b.WriteString(text[start:closeEnd])
		} else {
			head := strings.Join(lines[:3], "\n")
			tail := lines[len(lines)-1]
			b.WriteString(opening + "\n" + head + "\n… [code elided] …\n" + tail + "\n```")
		}
		cursor = closeEnd
	}
	return b.String()
}

// summaryTruncChars truncates s to at most n runes.
func summaryTruncChars(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
