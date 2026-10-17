package chat_completions

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// malformedFunctionCallPrefix is the finishMessage text Antigravity puts in
// front of a function call it could not validate.
const malformedFunctionCallPrefix = "Malformed function call:"

const callMarker = "call:"

// recoveredCall is one function call lifted out of Antigravity's malformed
// text form. Start is an index into the visible text when FromText is set;
// finishMessage recoveries leave Start at -1.
type recoveredCall struct {
	Name      string
	Arguments string
	Start     int
	FromText  bool
}

// resolveOpenAIFinishReason maps cached upstream state onto the OpenAI
// finish_reason pair. A real functionCall part still wins. MAX_TOKENS stays a
// length stop. A recoverable call is promoted only for STOP and
// MALFORMED_FUNCTION_CALL; SAFETY, RECITATION, and other upstream blocks are
// passed through in lowercase, matching the non-stream Gemini translator, and
// are never turned into tool_calls. An unrecoverable malformed call keeps
// finish_reason malformed_function_call so the client can retry.
func resolveOpenAIFinishReason(params *convertCliResponseToOpenAIChatParams) (finishReason, nativeFinishReason string, calls []recoveredCall) {
	nativeFinishReason = "stop"
	if params.UpstreamFinishReason != "" {
		nativeFinishReason = strings.ToLower(params.UpstreamFinishReason)
	}
	switch {
	case params.SawToolCall:
		finishReason = "tool_calls"
	case params.UpstreamFinishReason == "MAX_TOKENS":
		finishReason = "max_tokens"
	default:
		if recovered, ok := recoverMalformedFunctionCalls(params); ok {
			return "tool_calls", nativeFinishReason, recovered
		}
		switch params.UpstreamFinishReason {
		case "", "STOP":
			finishReason = "stop"
		case "MALFORMED_FUNCTION_CALL":
			finishReason = "malformed_function_call"
		default:
			finishReason = nativeFinishReason
		}
	}
	return finishReason, nativeFinishReason, nil
}

func recoverableUpstreamFinish(reason string) bool {
	switch reason {
	case "STOP", "MALFORMED_FUNCTION_CALL":
		return true
	default:
		return false
	}
}

// recoveryAllowedTools applies the request's tool_choice to textual recovery
// only. Native functionCall parts continue through the normal translation path.
func recoveryAllowedTools(request []byte) map[string]string {
	declared := util.SanitizedFunctionNameMap(request)
	choice := gjson.GetBytes(request, "tool_choice")
	if choice.Type == gjson.String {
		if strings.EqualFold(strings.TrimSpace(choice.String()), "none") {
			return nil
		}
	} else if choice.IsObject() {
		switch strings.ToLower(strings.TrimSpace(choice.Get("type").String())) {
		case "none":
			return nil
		case "function":
			name := choice.Get("function.name").String()
			if sanitized, ok := declared[name]; ok {
				return map[string]string{name: sanitized}
			}
			return nil
		}
	}
	return declared
}

func recoverMalformedFunctionCalls(params *convertCliResponseToOpenAIChatParams) ([]recoveredCall, bool) {
	if !recoverableUpstreamFinish(params.UpstreamFinishReason) {
		return nil, false
	}
	allowPreamble := params.UpstreamFinishReason == "MALFORMED_FUNCTION_CALL"
	if calls, ok := recoverCalls(params.VisibleText.String(), params.DeclaredTools, allowPreamble); ok {
		for i := range calls {
			calls[i].FromText = true
		}
		return calls, true
	}
	// finishMessage is only the call Antigravity could not place in a part.
	// It must not execute a tool when the visible text was already rejected,
	// or when the upstream reason is a normal stop with other text.
	if strings.TrimSpace(params.VisibleText.String()) != "" || params.UpstreamFinishReason != "MALFORMED_FUNCTION_CALL" {
		return nil, false
	}
	if calls, ok := recoverCalls(params.FinishMessage, params.DeclaredTools, false); ok {
		return calls, true
	}
	return nil, false
}

// recoverCalls parses call:<namespace>:<tool>{...} suffixes.
// A normal stop may recover only when the whole text is the call (optionally
// behind Antigravity's malformed prefix). A MALFORMED_FUNCTION_CALL may also
// recover a suffix glued to a preamble, but the preamble must not itself
// contain "call:" and every tool must be one the client declared.
func recoverCalls(text string, declared map[string]string, allowPreamble bool) ([]recoveredCall, bool) {
	if len(declared) == 0 {
		return nil, false
	}
	working, origin := normalizeMalformedText(text)
	if working == "" {
		return nil, false
	}
	start, ok := contiguousCallSuffix(working)
	if !ok {
		return nil, false
	}
	preamble := stripTrailingDiagnosticPrefix(working[:start])
	if strings.Contains(preamble, callMarker) {
		return nil, false
	}
	if !allowPreamble && strings.TrimSpace(preamble) != "" {
		return nil, false
	}
	calls, ok := parseCallRun(working, start)
	if !ok {
		return nil, false
	}
	for i := range calls {
		clientName, declaredOK := resolveDeclaredName(declared, calls[i].Name)
		if !declaredOK {
			return nil, false
		}
		calls[i].Name = clientName
		calls[i].Start += origin
	}
	return calls, true
}

func normalizeMalformedText(text string) (working string, origin int) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return "", 0
	}
	origin = strings.Index(text, trimmed)
	if origin < 0 {
		origin = 0
	}
	if strings.HasPrefix(trimmed, malformedFunctionCallPrefix) {
		rest := strings.TrimSpace(trimmed[len(malformedFunctionCallPrefix):])
		if rest == "" {
			return "", 0
		}
		if at := strings.Index(text, rest); at >= 0 {
			origin = at
		}
		return rest, origin
	}
	return trimmed, origin
}

func resolveDeclaredName(declared map[string]string, name string) (string, bool) {
	if name == "" || len(declared) == 0 {
		return "", false
	}
	if _, ok := declared[name]; ok {
		return name, true
	}
	for original, sanitized := range declared {
		if sanitized == name {
			return original, true
		}
	}
	return "", false
}

func contiguousCallSuffix(text string) (int, bool) {
	trimmedLen := len(strings.TrimRightFunc(text, unicode.IsSpace))
	if trimmedLen == 0 {
		return 0, false
	}
	type span struct{ start, end int }
	var spans []span
	for i := 0; i < trimmedLen; {
		rel := strings.Index(text[i:trimmedLen], callMarker)
		if rel < 0 {
			break
		}
		at := i + rel
		kind, end := classifyCall(text, at)
		if kind == callOK && end > at && end <= trimmedLen {
			spans = append(spans, span{at, end})
			i = end
			continue
		}
		i = at + len(callMarker)
	}
	if len(spans) == 0 || spans[len(spans)-1].end != trimmedLen {
		return 0, false
	}
	idx := len(spans) - 1
	for idx > 0 {
		gap := text[spans[idx-1].end:spans[idx].start]
		if strings.TrimSpace(gap) != "" {
			break
		}
		idx--
	}
	return spans[idx].start, true
}

func parseCallRun(text string, start int) ([]recoveredCall, bool) {
	trimmedLen := len(strings.TrimRightFunc(text, unicode.IsSpace))
	var calls []recoveredCall
	pos := start
	for pos < trimmedLen {
		for pos < trimmedLen && isASCIISpace(text[pos]) {
			pos++
		}
		if pos >= trimmedLen {
			break
		}
		kind, end := classifyCall(text, pos)
		if kind != callOK || end <= pos || end > trimmedLen {
			return nil, false
		}
		call, ok := callAt(text, pos, end)
		if !ok {
			return nil, false
		}
		calls = append(calls, call)
		pos = end
	}
	if len(calls) == 0 || pos != trimmedLen {
		return nil, false
	}
	return calls, true
}

func callAt(text string, start, end int) (recoveredCall, bool) {
	header := text[start:end]
	rest := strings.TrimPrefix(header, callMarker)
	nsEnd := strings.IndexByte(rest, ':')
	if nsEnd <= 0 {
		return recoveredCall{}, false
	}
	nameAndArgs := rest[nsEnd+1:]
	brace := strings.IndexByte(nameAndArgs, '{')
	if brace <= 0 || !strings.HasSuffix(nameAndArgs, "}") {
		return recoveredCall{}, false
	}
	name := nameAndArgs[:brace]
	args, ok := parseArgs(nameAndArgs[brace+1 : len(nameAndArgs)-1])
	if !ok {
		return recoveredCall{}, false
	}
	encoded, ok := marshalArgs(args)
	if !ok {
		return recoveredCall{}, false
	}
	return recoveredCall{Name: name, Arguments: encoded, Start: start}, true
}

const (
	callReject = iota
	callOK
	callIncomplete
)

func classifyCall(text string, start int) (kind, end int) {
	if start < 0 || start > len(text) || !strings.HasPrefix(text[start:], callMarker) {
		return callReject, start
	}
	i := start + len(callMarker)
	_, i, status := readIdent(text, i)
	if status == identIncomplete {
		return callIncomplete, len(text)
	}
	if status != identOK {
		return callReject, start
	}
	if i >= len(text) {
		return callIncomplete, len(text)
	}
	if text[i] != ':' {
		return callReject, start
	}
	i++
	_, i, status = readIdent(text, i)
	if status == identIncomplete {
		return callIncomplete, len(text)
	}
	if status != identOK {
		return callReject, start
	}
	if i >= len(text) {
		return callIncomplete, len(text)
	}
	if text[i] != '{' {
		return callReject, start
	}
	closeAt, scanState := scanContainer(text, i)
	switch scanState {
	case scanUnclosed:
		return callIncomplete, len(text)
	case scanBad:
		return callReject, start
	}
	if _, ok := parseArgs(text[i+1 : closeAt-1]); !ok {
		return callReject, start
	}
	return callOK, closeAt
}

const (
	identReject = iota
	identOK
	identIncomplete
)

func readIdent(text string, i int) (string, int, int) {
	if i >= len(text) {
		return "", i, identIncomplete
	}
	if !isIdentStart(text[i]) {
		return "", i, identReject
	}
	j := i + 1
	for j < len(text) && isIdentCont(text[j]) {
		j++
	}
	return text[i:j], j, identOK
}

func isIdentStart(b byte) bool {
	return b == '_' || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

func isIdentCont(b byte) bool {
	return isIdentStart(b) || (b >= '0' && b <= '9')
}

func isASCIISpace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r'
}

const (
	scanOK = iota
	scanUnclosed
	scanBad
)

func scanContainer(text string, open int) (int, int) {
	pairs := map[byte]byte{'{': '}', '[': ']'}
	if open < 0 || open >= len(text) {
		return 0, scanBad
	}
	closing, ok := pairs[text[open]]
	if !ok {
		return 0, scanBad
	}
	stack := []byte{closing}
	var inSingle, inDouble, escaped bool
	for i := open + 1; i < len(text); i++ {
		char := text[i]
		if escaped {
			escaped = false
			continue
		}
		if (inSingle || inDouble) && char == '\\' {
			escaped = true
			continue
		}
		if inSingle {
			if char == '\'' {
				inSingle = false
			}
			continue
		}
		if inDouble {
			if char == '"' {
				inDouble = false
			}
			continue
		}
		switch char {
		case '\'':
			inSingle = true
		case '"':
			inDouble = true
		case '{', '[':
			stack = append(stack, pairs[char])
		case '}', ']':
			if len(stack) == 0 || stack[len(stack)-1] != char {
				return 0, scanBad
			}
			stack = stack[:len(stack)-1]
			if len(stack) == 0 {
				return i + 1, scanOK
			}
		}
	}
	return 0, scanUnclosed
}

func parseArgs(body string) (map[string]any, bool) {
	args := make(map[string]any)
	i := 0
	skipWS := func() {
		for i < len(body) && isASCIISpace(body[i]) {
			i++
		}
	}
	skipWS()
	if i >= len(body) {
		return args, true
	}
	for i < len(body) {
		if !isIdentStart(body[i]) {
			return nil, false
		}
		keyStart := i
		i++
		for i < len(body) && isIdentCont(body[i]) {
			i++
		}
		key := body[keyStart:i]
		if _, exists := args[key]; exists {
			return nil, false
		}
		skipWS()
		if i >= len(body) || body[i] != ':' {
			return nil, false
		}
		i++
		skipWS()
		if i >= len(body) {
			return nil, false
		}
		value, next, bareWord, ok := parseValue(body, i)
		if !ok {
			return nil, false
		}
		// A later bare word is ambiguous (`echo a,b:c` splits into two keys).
		// Numbers, booleans, quotes, and JSON containers stay valid.
		if len(args) >= 1 && bareWord {
			return nil, false
		}
		args[key] = value
		i = next
		skipWS()
		if i >= len(body) {
			return args, true
		}
		if body[i] != ',' {
			return nil, false
		}
		i++
		skipWS()
		if i >= len(body) {
			return nil, false
		}
	}
	return args, true
}

func parseValue(body string, start int) (value any, end int, bareWord bool, ok bool) {
	char := body[start]
	switch char {
	case '"', '\'':
		quoted, quotedEnd, quotedOK := parseQuoted(body, start, char)
		if !quotedOK {
			return nil, 0, false, false
		}
		return quoted, quotedEnd, false, true
	case '{', '[':
		containerEnd, state := scanContainer(body, start)
		if state != scanOK {
			return nil, 0, false, false
		}
		parsed, parsedOK := decodeJSONValue(body[start:containerEnd])
		if !parsedOK {
			return nil, 0, false, false
		}
		return parsed, containerEnd, false, true
	default:
		rawEnd := scanRaw(body, start)
		if rawEnd <= start {
			return nil, 0, false, false
		}
		raw := strings.TrimRightFunc(body[start:rawEnd], unicode.IsSpace)
		if raw == "" {
			return nil, 0, false, false
		}
		coerced := coerceScalar(raw)
		_, bareWord = coerced.(string)
		return coerced, start + len(raw), bareWord, true
	}
}

func decodeJSONValue(raw string) (any, bool) {
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.UseNumber()
	var parsed any
	if err := dec.Decode(&parsed); err != nil {
		return nil, false
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return nil, false
	}
	return parsed, true
}

// parseQuoted rejects any backslash. JSON and shell escapes are not the same
// spelling, and guessing would rewrite a command before it runs.
func parseQuoted(body string, start int, quote byte) (string, int, bool) {
	var b strings.Builder
	for i := start + 1; i < len(body); i++ {
		char := body[i]
		if char == '\\' {
			return "", 0, false
		}
		if char == quote {
			return b.String(), i + 1, true
		}
		b.WriteByte(char)
	}
	return "", 0, false
}

func scanRaw(body string, start int) int {
	var inSingle, inDouble, escaped bool
	depth := 0
	for i := start; i < len(body); i++ {
		char := body[i]
		if escaped {
			escaped = false
			continue
		}
		if (inSingle || inDouble) && char == '\\' {
			escaped = true
			continue
		}
		if inSingle {
			if char == '\'' {
				inSingle = false
			}
			continue
		}
		if inDouble {
			if char == '"' {
				inDouble = false
			}
			continue
		}
		switch char {
		case '\'':
			inSingle = true
		case '"':
			inDouble = true
		case '{', '[':
			depth++
		case '}', ']':
			if depth == 0 {
				return i
			}
			depth--
		case ',':
			if depth == 0 && looksLikeNextKey(body, i+1) {
				return i
			}
		}
	}
	return len(body)
}

func looksLikeNextKey(body string, start int) bool {
	i := start
	for i < len(body) && isASCIISpace(body[i]) {
		i++
	}
	if i >= len(body) || !isIdentStart(body[i]) {
		return false
	}
	i++
	for i < len(body) && isIdentCont(body[i]) {
		i++
	}
	for i < len(body) && isASCIISpace(body[i]) {
		i++
	}
	return i < len(body) && body[i] == ':'
}

func coerceScalar(raw string) any {
	switch raw {
	case "true":
		return true
	case "false":
		return false
	case "null":
		return nil
	}
	if numberPattern(raw) {
		return json.Number(raw)
	}
	return raw
}

func numberPattern(raw string) bool {
	if raw == "" {
		return false
	}
	i := 0
	if raw[0] == '-' {
		i++
		if i >= len(raw) {
			return false
		}
	}
	if raw[i] == '0' {
		i++
	} else if raw[i] >= '1' && raw[i] <= '9' {
		i++
		for i < len(raw) && raw[i] >= '0' && raw[i] <= '9' {
			i++
		}
	} else {
		return false
	}
	if i < len(raw) && raw[i] == '.' {
		i++
		if i >= len(raw) || raw[i] < '0' || raw[i] > '9' {
			return false
		}
		for i < len(raw) && raw[i] >= '0' && raw[i] <= '9' {
			i++
		}
	}
	if i < len(raw) && (raw[i] == 'e' || raw[i] == 'E') {
		i++
		if i < len(raw) && (raw[i] == '+' || raw[i] == '-') {
			i++
		}
		if i >= len(raw) || raw[i] < '0' || raw[i] > '9' {
			return false
		}
		for i < len(raw) && raw[i] >= '0' && raw[i] <= '9' {
			i++
		}
	}
	return i == len(raw)
}

func marshalArgs(args map[string]any) (string, bool) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(args); err != nil {
		return "", false
	}
	return strings.TrimSpace(buf.String()), true
}

// holdStart is how far visible text may be streamed before the terminal
// chunk. Scanning starts at flushed so already-sent prose is not walked
// again. A complete call suffix, an unfinished call, a trailing piece of
// "call:" or "Malformed function call:", and any completed calls sitting
// immediately before that piece stay buffered. Bytes already flushed are not
// retracted. The diagnostic prefix is held with the call so a later promotion
// does not also leave it in the content channel.
func holdStart(text string, flushed int) int {
	if flushed < 0 {
		flushed = 0
	}
	if flushed > len(text) {
		flushed = len(text)
	}
	tail := text[flushed:]
	// A later chunk may turn boundary whitespace into trailing preamble
	// whitespace. Hold it until subsequent prose or the finish decision arrives.
	rel := len(strings.TrimRightFunc(tail, unicode.IsSpace))
	if start, ok := pendingSuffixStart(tail); ok && start < rel {
		rel = start
	}
	if n := trailingCallPrefixLen(tail); n > 0 {
		cut := len(tail) - n
		if back, ok := completedRunBefore(tail, cut); ok {
			cut = back
		}
		if cut < rel {
			rel = cut
		}
	}
	if cut, ok := diagnosticHoldCut(tail); ok {
		if back, ok := completedRunBefore(tail, cut); ok {
			cut = back
		}
		if cut < rel {
			rel = cut
		}
	}
	abs := flushed + rel
	if pulled := pullBackDiagnosticPrefix(text, abs); pulled < abs {
		abs = pulled
	}
	if abs < flushed {
		return flushed
	}
	abs = flushed + len(strings.TrimRightFunc(text[flushed:abs], unicode.IsSpace))
	if abs > len(text) {
		return len(text)
	}
	return abs
}

func completedRunBefore(text string, cut int) (int, bool) {
	if cut <= 0 || cut > len(text) {
		return 0, false
	}
	head := strings.TrimRightFunc(text[:cut], unicode.IsSpace)
	if head == "" {
		return 0, false
	}
	return contiguousCallSuffix(head)
}

func pullBackDiagnosticPrefix(text string, start int) int {
	if start <= 0 || start > len(text) {
		return start
	}
	head := strings.TrimRightFunc(text[:start], unicode.IsSpace)
	if !strings.HasSuffix(head, malformedFunctionCallPrefix) {
		return start
	}
	prefixAt := len(head) - len(malformedFunctionCallPrefix)
	return len(strings.TrimRightFunc(head[:prefixAt], unicode.IsSpace))
}

func stripTrailingDiagnosticPrefix(text string) string {
	trimmed := strings.TrimRightFunc(text, unicode.IsSpace)
	if strings.HasSuffix(trimmed, malformedFunctionCallPrefix) {
		trimmed = strings.TrimRightFunc(trimmed[:len(trimmed)-len(malformedFunctionCallPrefix)], unicode.IsSpace)
	}
	return trimmed
}

func trailingCallPrefixLen(text string) int {
	return trailingMarkerPrefixLen(text, callMarker)
}

// diagnosticHoldCut is the first byte of text that must stay buffered because
// it is a complete "Malformed function call:" marker, trailing space after
// that marker, or a still-incomplete prefix of it. Whitespace between prose
// and the marker stays buffered so a later chunk cannot change the preamble.
func diagnosticHoldCut(text string) (int, bool) {
	if text == "" {
		return 0, false
	}
	trimmed := strings.TrimRightFunc(text, unicode.IsSpace)
	if strings.HasSuffix(trimmed, malformedFunctionCallPrefix) {
		prefixAt := len(trimmed) - len(malformedFunctionCallPrefix)
		return len(strings.TrimRightFunc(trimmed[:prefixAt], unicode.IsSpace)), true
	}
	if n := trailingMarkerPrefixLen(text, malformedFunctionCallPrefix); n > 0 {
		return len(strings.TrimRightFunc(text[:len(text)-n], unicode.IsSpace)), true
	}
	return 0, false
}

func trailingMarkerPrefixLen(text, marker string) int {
	maxLen := len(marker) - 1
	if maxLen <= 0 {
		return 0
	}
	if maxLen > len(text) {
		maxLen = len(text)
	}
	for n := maxLen; n > 0; n-- {
		if strings.HasSuffix(text, marker[:n]) {
			return n
		}
	}
	return 0
}

func pendingSuffixStart(text string) (int, bool) {
	trimmedLen := len(strings.TrimRightFunc(text, unicode.IsSpace))
	runStart := -1
	prevEnd := 0
	for i := 0; i < len(text); {
		rel := strings.Index(text[i:], callMarker)
		if rel < 0 {
			break
		}
		at := i + rel
		kind, end := classifyCall(text, at)
		if kind == callReject {
			runStart = -1
			i = at + len(callMarker)
			continue
		}
		if runStart < 0 || strings.TrimSpace(text[prevEnd:at]) != "" {
			runStart = at
		}
		if kind == callIncomplete {
			return runStart, true
		}
		prevEnd = end
		i = end
		if i >= trimmedLen {
			return runStart, true
		}
	}
	return 0, false
}

func finalizeStreamChunk(params *convertCliResponseToOpenAIChatParams, template []byte, terminal bool) []byte {
	var finish, native string
	var calls []recoveredCall
	if terminal {
		finish, native, calls = resolveOpenAIFinishReason(params)
	}
	if len(calls) > 0 && calls[0].FromText {
		template = writeMeaningfulPreamble(params, template, calls[0].Start)
		template = appendRecoveredToolCalls(template, params, calls)
		params.FlushedVisible = params.VisibleText.Len()
	} else if len(calls) > 0 {
		template = writeVisibleDelta(params, template, true)
		template = appendRecoveredToolCalls(template, params, calls)
	} else {
		template = writeVisibleDelta(params, template, terminal)
	}
	if terminal {
		template, _ = sjson.SetBytes(template, "choices.0.finish_reason", finish)
		template, _ = sjson.SetBytes(template, "choices.0.native_finish_reason", native)
		params.SawFinishReason = true
	}
	return template
}

func writeVisibleDelta(params *convertCliResponseToOpenAIChatParams, template []byte, releaseHeld bool) []byte {
	full := params.VisibleText.String()
	end := len(full)
	if !releaseHeld {
		end = holdStart(full, params.FlushedVisible)
	}
	return writeRange(params, template, params.FlushedVisible, end)
}

func writeMeaningfulPreamble(params *convertCliResponseToOpenAIChatParams, template []byte, callStart int) []byte {
	full := params.VisibleText.String()
	if callStart < 0 {
		callStart = 0
	}
	if callStart > len(full) {
		callStart = len(full)
	}
	// Clean the complete preamble, then emit only its unsent range. Trimming
	// the remaining delta alone would delete spaces joining it to prior prose.
	preamble := meaningfulPreamble(full[:callStart])
	if preamble == "" {
		return template
	}
	start := strings.Index(full[:callStart], preamble)
	return writeRange(params, template, start, start+len(preamble))
}

func meaningfulPreamble(text string) string {
	text = strings.TrimSpace(stripTrailingDiagnosticPrefix(text))
	if strings.HasPrefix(text, malformedFunctionCallPrefix) {
		text = strings.TrimSpace(text[len(malformedFunctionCallPrefix):])
	}
	return strings.TrimSpace(text)
}

func writeRange(params *convertCliResponseToOpenAIChatParams, template []byte, from, to int) []byte {
	full := params.VisibleText.String()
	if from < params.FlushedVisible {
		from = params.FlushedVisible
	}
	if to > len(full) {
		to = len(full)
	}
	if to <= from {
		return template
	}
	template, _ = sjson.SetBytes(template, "choices.0.delta.content", full[from:to])
	template, _ = sjson.SetBytes(template, "choices.0.delta.role", "assistant")
	params.FlushedVisible = to
	return template
}

func appendRecoveredToolCalls(template []byte, params *convertCliResponseToOpenAIChatParams, calls []recoveredCall) []byte {
	if !gjson.GetBytes(template, "choices.0.delta.tool_calls").IsArray() {
		template, _ = sjson.SetRawBytes(template, "choices.0.delta.tool_calls", []byte("[]"))
	}
	for _, call := range calls {
		index := params.FunctionIndex
		params.FunctionIndex++
		item := []byte(`{"id":"","index":0,"type":"function","function":{"name":"","arguments":""}}`)
		item, _ = sjson.SetBytes(item, "id", fmt.Sprintf("%s-%d-%d", call.Name, time.Now().UnixNano(), atomic.AddUint64(&functionCallIDCounter, 1)))
		item, _ = sjson.SetBytes(item, "index", index)
		item, _ = sjson.SetBytes(item, "function.name", call.Name)
		item, _ = sjson.SetBytes(item, "function.arguments", call.Arguments)
		template, _ = sjson.SetBytes(template, "choices.0.delta.role", "assistant")
		template, _ = sjson.SetRawBytes(template, "choices.0.delta.tool_calls.-1", item)
	}
	return template
}

func promoteNonStreamMalformedFunctionCall(out, responseJSON, originalRequestRawJSON []byte) []byte {
	if len(out) == 0 {
		return out
	}
	declaredTools := recoveryAllowedTools(originalRequestRawJSON)
	for candidateIndex, candidate := range gjson.GetBytes(responseJSON, "candidates").Array() {
		choicePath := fmt.Sprintf("choices.%d", candidateIndex)
		if existing := gjson.GetBytes(out, choicePath+".message.tool_calls"); existing.IsArray() && len(existing.Array()) > 0 {
			continue
		}

		params := &convertCliResponseToOpenAIChatParams{
			ToolsLoaded:   true,
			DeclaredTools: declaredTools,
		}
		if finish := candidate.Get("finishReason"); finish.Exists() {
			params.UpstreamFinishReason = strings.ToUpper(finish.String())
		}
		if msg := candidate.Get("finishMessage"); msg.Exists() {
			params.FinishMessage = msg.String()
		}
		parts := candidate.Get("content.parts")
		if parts.IsArray() {
			for _, part := range parts.Array() {
				if part.Get("thought").Bool() {
					continue
				}
				if text := part.Get("text"); text.Exists() {
					params.VisibleText.WriteString(text.String())
				}
			}
		}
		_, native, calls := resolveOpenAIFinishReason(params)
		if len(calls) == 0 {
			if params.UpstreamFinishReason == "MALFORMED_FUNCTION_CALL" {
				out, _ = sjson.SetBytes(out, choicePath+".finish_reason", "malformed_function_call")
				out, _ = sjson.SetBytes(out, choicePath+".native_finish_reason", "malformed_function_call")
			}
			continue
		}
		if calls[0].FromText {
			full := params.VisibleText.String()
			preamble := ""
			if calls[0].Start > 0 && calls[0].Start <= len(full) {
				preamble = strings.TrimRightFunc(full[:calls[0].Start], unicode.IsSpace)
			}
			emit := meaningfulPreamble(preamble)
			if emit == "" {
				out, _ = sjson.SetRawBytes(out, choicePath+".message.content", []byte("null"))
			} else {
				out, _ = sjson.SetBytes(out, choicePath+".message.content", emit)
			}
		}
		var toolCalls []byte
		toolCalls = append(toolCalls, '[')
		for i, call := range calls {
			if i > 0 {
				toolCalls = append(toolCalls, ',')
			}
			item := []byte(`{"id":"","type":"function","function":{"name":"","arguments":""}}`)
			item, _ = sjson.SetBytes(item, "id", fmt.Sprintf("%s-%d-%d", call.Name, time.Now().UnixNano(), atomic.AddUint64(&functionCallIDCounter, 1)))
			item, _ = sjson.SetBytes(item, "function.name", call.Name)
			item, _ = sjson.SetBytes(item, "function.arguments", call.Arguments)
			toolCalls = append(toolCalls, item...)
		}
		toolCalls = append(toolCalls, ']')
		out, _ = sjson.SetRawBytes(out, choicePath+".message.tool_calls", toolCalls)
		out, _ = sjson.SetBytes(out, choicePath+".finish_reason", "tool_calls")
		out, _ = sjson.SetBytes(out, choicePath+".native_finish_reason", native)
	}
	return out
}
