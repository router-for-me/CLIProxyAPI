package payloadcompress

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/MoeForge/moego/ojson"
)

// Rule-based tool-result compressor ported from upstream
// toolResultCompressor.ts. Each OpenAI-shape tool/function result text is
// classified (file content, grep listing, shell output, JSON, error) and
// rewritten with a strategy-specific truncation.

const (
	toolStrategyNone = "none"

	toolFileKeepHead      = 20
	toolFileKeepTail      = 5
	toolGrepMaxLines      = 30
	toolShellMaxLines     = 50
	toolJSONMaxChars      = 2000
	toolJSONMaxKeys       = 20
	toolJSONArrayKeepHead = 5
	toolJSONArrayKeepTail = 2
	toolJSONArrayMinItems = 7
	toolErrorHead         = 10
	toolErrorTail         = 3
	toolErrorElideAfter   = 13
)

var (
	ansiRe        = regexp.MustCompile("\x1b\\[[0-9;]*[a-zA-Z]")
	shellPromptRe = regexp.MustCompile(`\$\s`)
	jsonPrefixRe  = regexp.MustCompile(`^\s*[{[]`)
)

type toolCompressionResult struct {
	compressed string
	strategy   string
	saved      int
}

// charTokens mirrors upstream estimateTokens: ceil(chars/4).
func charTokens(text string) int {
	return (charLen(text) + 3) / 4
}

func isCodeLikeLine(rawLine string) bool {
	line := strings.TrimLeft(rawLine, " \t")
	switch {
	case strings.HasPrefix(line, "import "),
		strings.HasPrefix(line, "export "),
		strings.HasPrefix(line, "function "),
		strings.HasPrefix(line, "class "),
		strings.HasPrefix(line, "const "),
		strings.HasPrefix(line, "let "),
		strings.HasPrefix(line, "var "),
		strings.HasPrefix(line, "return "),
		strings.HasPrefix(line, "if("),
		strings.HasPrefix(line, "if ("),
		strings.HasPrefix(line, "for("),
		strings.HasPrefix(line, "for ("),
		strings.HasPrefix(line, "while("),
		strings.HasPrefix(line, "while ("):
		return true
	}
	return false
}

func parseGrepLinePath(line string) (string, bool) {
	firstColon := strings.IndexByte(line, ':')
	if firstColon <= 0 {
		return "", false
	}
	secondRel := strings.IndexByte(line[firstColon+1:], ':')
	if secondRel < 0 {
		return "", false
	}
	secondColon := firstColon + 1 + secondRel
	lineNumber := line[firstColon+1 : secondColon]
	if lineNumber == "" {
		return "", false
	}
	for i := 0; i < len(lineNumber); i++ {
		if lineNumber[i] < '0' || lineNumber[i] > '9' {
			return "", false
		}
	}
	filePath := line[:firstColon]
	if filePath == "" || strings.ContainsAny(filePath, " \t\r\n\f\v") {
		return "", false
	}
	return filePath, true
}

func hasErrorLikeOutput(content string) bool {
	lower := strings.ToLower(content)
	for _, needle := range []string{
		"error:", "error ", "[error]", "exception:", "exception ", "traceback",
	} {
		if strings.Contains(lower, needle) {
			return true
		}
	}
	return false
}

func compressFileContent(content string) (string, bool) {
	lines := strings.Split(content, "\n")
	if len(lines) < 3 {
		return "", false
	}
	codeLike := false
	for _, line := range lines {
		if isCodeLikeLine(line) {
			codeLike = true
			break
		}
	}
	if !codeLike {
		return "", false
	}
	if len(lines) <= toolFileKeepHead+toolFileKeepTail {
		return content, true
	}
	head := strings.Join(lines[:toolFileKeepHead], "\n")
	tail := strings.Join(lines[len(lines)-toolFileKeepTail:], "\n")
	elided := len(lines) - toolFileKeepHead - toolFileKeepTail
	return head + "\n… [" + strconv.Itoa(elided) + " lines elided] …\n" + tail, true
}

func compressGrepSearch(content string) (string, bool) {
	lines := strings.Split(content, "\n")
	grepLines := make([]string, 0, len(lines))
	for _, line := range lines {
		if _, ok := parseGrepLinePath(line); ok {
			grepLines = append(grepLines, line)
		}
	}
	if len(grepLines) == 0 {
		return "", false
	}
	var paths []string
	seen := make(map[string]struct{})
	for _, line := range grepLines {
		path, ok := parseGrepLinePath(line)
		if !ok {
			continue
		}
		if _, dup := seen[path]; dup {
			continue
		}
		seen[path] = struct{}{}
		paths = append(paths, path)
	}
	top := grepLines
	if len(top) > toolGrepMaxLines {
		top = top[:toolGrepMaxLines]
	}
	result := strings.Join(top, "\n")
	if remaining := len(grepLines) - len(top); remaining > 0 {
		result += "\n… [" + strconv.Itoa(remaining) + " more matches]"
	}
	result += "\nFiles: " + strings.Join(paths, ", ")
	return result, true
}

func compressShellOutput(content string) (string, bool) {
	hasANSI := ansiRe.MatchString(content)
	hasPrompt := shellPromptRe.MatchString(content)
	if !hasANSI && !hasPrompt {
		return "", false
	}
	cleaned := ansiRe.ReplaceAllString(content, "")
	lines := strings.Split(cleaned, "\n")
	if len(lines) > toolShellMaxLines {
		lines = lines[len(lines)-toolShellMaxLines:]
	}
	deduped := make([]string, 0, len(lines))
	for _, line := range lines {
		if len(deduped) == 0 || line != deduped[len(deduped)-1] {
			deduped = append(deduped, line)
		}
	}
	return strings.Join(deduped, "\n"), true
}

func compressJSONText(content string) (string, bool) {
	if charLen(content) <= toolJSONMaxChars {
		return "", false
	}
	if !jsonPrefixRe.MatchString(content) {
		return "", false
	}
	if !ojson.Valid(content) {
		return "", false
	}
	parsed := ojson.Parse(content)

	if parsed.IsArray() {
		arr := parsed.Array()
		if len(arr) <= toolJSONArrayMinItems {
			return content, true
		}
		head := arr[:toolJSONArrayKeepHead]
		tail := arr[len(arr)-toolJSONArrayKeepTail:]
		pairs := []jsonKVPair{
			{"type", appendJSONString(nil, "array")},
			{"total", []byte(strconv.Itoa(len(arr)))},
			{"first5", appendJSONArrayPretty(nil, head, 1)},
			{"last2", appendJSONArrayPretty(nil, tail, 1)},
		}
		return summaryPrettyObject(pairs, 0), true
	}

	if parsed.IsObject() {
		var pairs []jsonKVPair
		total := 0
		parsed.ForEach(func(key, value ojson.Result) bool {
			total++
			if len(pairs) >= toolJSONMaxKeys {
				return true
			}
			var rendered []byte
			if value.IsObject() {
				rendered = appendJSONString(nil, "{…"+strconv.Itoa(ojsonObjectLen(value))+" keys}")
			} else {
				rendered = appendJSONPretty(nil, value, 1)
			}
			pairs = append(pairs, jsonKVPair{key.String(), rendered})
			return true
		})
		if total > toolJSONMaxKeys {
			pairs = append(pairs, jsonKVPair{
				"_remaining_" + strconv.Itoa(total-toolJSONMaxKeys) + "_keys",
				[]byte("true"),
			})
		}
		return summaryPrettyObject(pairs, 0), true
	}
	return "", false
}

func compressErrorMessage(content string) (string, bool) {
	if !hasErrorLikeOutput(content) {
		return "", false
	}
	lines := strings.Split(content, "\n")
	errorLine := ""
	if len(lines) > 0 {
		errorLine = lines[0]
	}
	stack := lines[1:]
	headEnd := toolErrorHead
	if headEnd > len(stack) {
		headEnd = len(stack)
	}
	head := stack[:headEnd]

	var tail []string
	if len(stack) > toolErrorHead {
		tail = stack[len(stack)-toolErrorTail:]
	}
	var middle []string
	if len(stack) > toolErrorElideAfter {
		middle = []string{"… [" + strconv.Itoa(len(stack)-toolErrorElideAfter) + " frames elided] …"}
	}

	result := make([]string, 0, 2+len(head)+len(middle)+len(tail))
	result = append(result, errorLine)
	result = append(result, head...)
	result = append(result, middle...)
	result = append(result, tail...)
	return strings.Join(result, "\n"), true
}

// compressToolResult applies the enabled strategies in upstream order.
func compressToolResult(content string) toolCompressionResult {
	type attempt struct {
		strategy string
		fn       func(string) (string, bool)
	}
	attempts := []attempt{
		{"fileContent", compressFileContent},
		{"grepSearch", compressGrepSearch},
		{"shellOutput", compressShellOutput},
		{"json", compressJSONText},
		{"errorMessage", compressErrorMessage},
	}
	for _, a := range attempts {
		result, ok := a.fn(content)
		if !ok {
			continue
		}
		return toolCompressionResult{
			compressed: result,
			strategy:   a.strategy,
			saved:      charTokens(content) - charTokens(result),
		}
	}
	return toolCompressionResult{compressed: content, strategy: toolStrategyNone}
}

// jsonKVPair is a pre-rendered key/value pair for summaryPrettyObject.
type jsonKVPair struct {
	key string
	val []byte
}

// summaryPrettyObject renders a JSON object with two-space indentation at the
// given depth. Values must already be rendered at depth+1.
func summaryPrettyObject(pairs []jsonKVPair, depth int) string {
	if len(pairs) == 0 {
		return "{}"
	}
	pad := strings.Repeat("  ", depth)
	child := strings.Repeat("  ", depth+1)
	var b []byte
	b = append(b, '{')
	for i, p := range pairs {
		if i > 0 {
			b = append(b, ',')
		}
		b = append(b, '\n')
		b = append(b, child...)
		b = appendJSONString(b, p.key)
		b = append(b, ':', ' ')
		b = append(b, p.val...)
	}
	b = append(b, '\n')
	b = append(b, pad...)
	b = append(b, '}')
	return string(b)
}

func appendJSONArrayPretty(dst []byte, vals []ojson.Result, depth int) []byte {
	if len(vals) == 0 {
		return append(dst, '[', ']')
	}
	pad := strings.Repeat("  ", depth)
	child := strings.Repeat("  ", depth+1)
	dst = append(dst, '[')
	for i, v := range vals {
		if i > 0 {
			dst = append(dst, ',')
		}
		dst = append(dst, '\n')
		dst = append(dst, child...)
		dst = appendJSONPretty(dst, v, depth+1)
	}
	dst = append(dst, '\n')
	dst = append(dst, pad...)
	return append(dst, ']')
}

func appendJSONPretty(dst []byte, value ojson.Result, depth int) []byte {
	switch {
	case value.IsObject():
		var pairs []jsonKVPair
		value.ForEach(func(key, val ojson.Result) bool {
			pairs = append(pairs, jsonKVPair{key.String(), appendJSONPretty(nil, val, depth+1)})
			return true
		})
		return append(dst, summaryPrettyObject(pairs, depth)...)
	case value.IsArray():
		return appendJSONArrayPretty(dst, value.Array(), depth)
	case value.Type == ojson.String:
		return appendJSONString(dst, value.String())
	default:
		return append(dst, value.Raw...)
	}
}

func ojsonObjectLen(value ojson.Result) int {
	count := 0
	value.ForEach(func(_, _ ojson.Result) bool {
		count++
		return true
	})
	return count
}
