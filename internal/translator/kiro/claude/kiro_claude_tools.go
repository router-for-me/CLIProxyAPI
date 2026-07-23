// Package claude provides tool calling support for Kiro to Claude translation.
// This package handles parsing embedded tool calls, JSON repair, and deduplication.
package claude

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/google/uuid"
	kirocommon "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/kiro/common"
	log "github.com/sirupsen/logrus"
)

// ToolUseState tracks the state of an in-progress tool use during streaming.
type ToolUseState struct {
	ToolUseID      string
	Name           string
	InputBuffer    strings.Builder
	IsComplete     bool
	TruncationInfo *TruncationInfo // Set when truncation detector is enabled and detects truncation
}

// Pre-compiled regex patterns for performance
var (
	// embeddedToolCallPattern matches [Called tool_name with args: {...}] format
	embeddedToolCallPattern = regexp.MustCompile(`\[Called\s+([A-Za-z0-9_.-]+)\s+with\s+args:\s*`)
	// trailingCommaPattern matches trailing commas before closing braces/brackets
	trailingCommaPattern = regexp.MustCompile(`,\s*([}\]])`)
)

// ParseEmbeddedToolCalls extracts [Called tool_name with args: {...}] format from text.
// Kiro sometimes embeds tool calls in text content instead of using toolUseEvent.
// Returns the cleaned text (with tool calls removed) and extracted tool uses.
func ParseEmbeddedToolCalls(text string, processedIDs map[string]bool) (string, []KiroToolUse) {
	if !strings.Contains(text, "[Called") {
		return text, nil
	}

	var toolUses []KiroToolUse
	cleanText := text

	// Find all [Called markers
	matches := embeddedToolCallPattern.FindAllStringSubmatchIndex(text, -1)
	if len(matches) == 0 {
		return text, nil
	}

	// Process matches in reverse order to maintain correct indices
	for i := len(matches) - 1; i >= 0; i-- {
		matchStart := matches[i][0]
		toolNameStart := matches[i][2]
		toolNameEnd := matches[i][3]

		if toolNameStart < 0 || toolNameEnd < 0 {
			continue
		}

		toolName := text[toolNameStart:toolNameEnd]

		// Find the JSON object start (after "with args:")
		jsonStart := matches[i][1]
		if jsonStart >= len(text) {
			continue
		}

		// Skip whitespace to find the opening brace
		for jsonStart < len(text) && (text[jsonStart] == ' ' || text[jsonStart] == '\t') {
			jsonStart++
		}

		if jsonStart >= len(text) || text[jsonStart] != '{' {
			continue
		}

		// Find matching closing bracket
		jsonEnd := findMatchingBracket(text, jsonStart)
		if jsonEnd < 0 {
			continue
		}

		// Extract JSON and find the closing bracket of [Called ...]
		jsonStr := text[jsonStart : jsonEnd+1]

		// Find the closing ] after the JSON
		closingBracket := jsonEnd + 1
		for closingBracket < len(text) && text[closingBracket] != ']' {
			closingBracket++
		}
		if closingBracket >= len(text) {
			continue
		}

		// End index of the full tool call (closing ']' inclusive)
		matchEnd := closingBracket + 1

		// Repair and parse JSON
		repairedJSON := RepairJSON(jsonStr)
		var inputMap map[string]interface{}
		if err := json.Unmarshal([]byte(repairedJSON), &inputMap); err != nil {
			log.Debugf("kiro: failed to parse embedded tool call JSON: %v, raw: %s", err, jsonStr)
			continue
		}

		// Generate unique tool ID
		toolUseID := "toolu_" + uuid.New().String()[:12]

		// Check for duplicates using name+input as key
		dedupeKey := toolName + ":" + repairedJSON
		if processedIDs != nil {
			if processedIDs[dedupeKey] {
				log.Debugf("kiro: skipping duplicate embedded tool call: %s", toolName)
				// Still remove from text even if duplicate
				if matchStart >= 0 && matchEnd <= len(cleanText) && matchStart <= matchEnd {
					cleanText = cleanText[:matchStart] + cleanText[matchEnd:]
				}
				continue
			}
			processedIDs[dedupeKey] = true
		}

		toolUses = append(toolUses, KiroToolUse{
			ToolUseID: toolUseID,
			Name:      toolName,
			Input:     inputMap,
		})

		log.Infof("kiro: extracted embedded tool call: %s (ID: %s)", toolName, toolUseID)

		// Remove from clean text (index-based removal to avoid deleting the wrong occurrence)
		if matchStart >= 0 && matchEnd <= len(cleanText) && matchStart <= matchEnd {
			cleanText = cleanText[:matchStart] + cleanText[matchEnd:]
		}
	}

	return cleanText, toolUses
}

// findMatchingBracket finds the index of the closing brace/bracket that matches
// the opening one at startPos. Handles nested objects and strings correctly.
func findMatchingBracket(text string, startPos int) int {
	if startPos >= len(text) {
		return -1
	}

	openChar := text[startPos]
	var closeChar byte
	switch openChar {
	case '{':
		closeChar = '}'
	case '[':
		closeChar = ']'
	default:
		return -1
	}

	depth := 1
	inString := false
	escapeNext := false

	for i := startPos + 1; i < len(text); i++ {
		char := text[i]

		if escapeNext {
			escapeNext = false
			continue
		}

		if char == '\\' && inString {
			escapeNext = true
			continue
		}

		if char == '"' {
			inString = !inString
			continue
		}

		if !inString {
			if char == openChar {
				depth++
			} else if char == closeChar {
				depth--
				if depth == 0 {
					return i
				}
			}
		}
	}

	return -1
}

// RepairJSON attempts to fix common JSON issues that may occur in tool call arguments.
// Conservative repair strategy:
// 1. First try to parse JSON directly - if valid, return as-is
// 2. Only attempt repair if parsing fails
// 3. After repair, validate the result - if still invalid, return original
func RepairJSON(jsonString string) string {
	// Handle empty or invalid input
	if jsonString == "" {
		return "{}"
	}

	str := strings.TrimSpace(jsonString)
	if str == "" {
		return "{}"
	}

	// CONSERVATIVE STRATEGY: First try to parse directly
	var testParse interface{}
	if err := json.Unmarshal([]byte(str), &testParse); err == nil {
		log.Debugf("kiro: repairJSON - JSON is already valid, returning unchanged")
		return str
	}

	log.Debugf("kiro: repairJSON - JSON parse failed, attempting repair")
	originalStr := str

	// First, escape unescaped newlines/tabs within JSON string values
	str = escapeNewlinesInStrings(str)
	// Remove trailing commas before closing braces/brackets
	str = trailingCommaPattern.ReplaceAllString(str, "$1")

	// Calculate bracket balance
	braceCount := 0
	bracketCount := 0
	inString := false
	escape := false
	lastValidIndex := -1

	for i := 0; i < len(str); i++ {
		char := str[i]

		if escape {
			escape = false
			continue
		}

		if char == '\\' {
			escape = true
			continue
		}

		if char == '"' {
			inString = !inString
			continue
		}

		if inString {
			continue
		}

		switch char {
		case '{':
			braceCount++
		case '}':
			braceCount--
		case '[':
			bracketCount++
		case ']':
			bracketCount--
		}

		if braceCount >= 0 && bracketCount >= 0 {
			lastValidIndex = i
		}
	}

	// If brackets are unbalanced, try to repair
	if braceCount > 0 || bracketCount > 0 {
		if lastValidIndex > 0 && lastValidIndex < len(str)-1 {
			truncated := str[:lastValidIndex+1]
			// Recount brackets after truncation
			braceCount = 0
			bracketCount = 0
			inString = false
			escape = false
			for i := 0; i < len(truncated); i++ {
				char := truncated[i]
				if escape {
					escape = false
					continue
				}
				if char == '\\' {
					escape = true
					continue
				}
				if char == '"' {
					inString = !inString
					continue
				}
				if inString {
					continue
				}
				switch char {
				case '{':
					braceCount++
				case '}':
					braceCount--
				case '[':
					bracketCount++
				case ']':
					bracketCount--
				}
			}
			str = truncated
		}

		// Add missing closing brackets
		for braceCount > 0 {
			str += "}"
			braceCount--
		}
		for bracketCount > 0 {
			str += "]"
			bracketCount--
		}
	}

	// Validate repaired JSON
	if err := json.Unmarshal([]byte(str), &testParse); err != nil {
		log.Warnf("kiro: repairJSON - repair failed to produce valid JSON, returning original")
		return originalStr
	}

	log.Debugf("kiro: repairJSON - successfully repaired JSON")
	return str
}

// escapeNewlinesInStrings escapes literal newlines, tabs, and other control characters
// that appear inside JSON string values.
func escapeNewlinesInStrings(raw string) string {
	var result strings.Builder
	result.Grow(len(raw) + 100)

	inString := false
	escaped := false

	for i := 0; i < len(raw); i++ {
		c := raw[i]

		if escaped {
			result.WriteByte(c)
			escaped = false
			continue
		}

		if c == '\\' && inString {
			result.WriteByte(c)
			escaped = true
			continue
		}

		if c == '"' {
			inString = !inString
			result.WriteByte(c)
			continue
		}

		if inString {
			switch c {
			case '\n':
				result.WriteString("\\n")
			case '\r':
				result.WriteString("\\r")
			case '\t':
				result.WriteString("\\t")
			default:
				result.WriteByte(c)
			}
		} else {
			result.WriteByte(c)
		}
	}

	return result.String()
}

// ProcessToolUseEvent handles a toolUseEvent from the Kiro stream.
// It accumulates input fragments and emits tool_use blocks when complete.
// Returns events to emit and updated state.
func ProcessToolUseEvent(event map[string]interface{}, currentToolUse *ToolUseState, processedIDs map[string]bool) ([]KiroToolUse, *ToolUseState) {
	var toolUses []KiroToolUse

	// Extract from nested toolUseEvent or direct format
	tu := event
	if nested, ok := event["toolUseEvent"].(map[string]interface{}); ok {
		tu = nested
	}

	toolUseID := kirocommon.GetString(tu, "toolUseId")
	toolName := kirocommon.GetString(tu, "name")
	isStop := false
	if stop, ok := tu["stop"].(bool); ok {
		isStop = stop
	}

	// Get input - can be string (fragment) or object (complete)
	var inputFragment string
	var inputMap map[string]interface{}

	if inputRaw, ok := tu["input"]; ok {
		switch v := inputRaw.(type) {
		case string:
			inputFragment = v
		case map[string]interface{}:
			inputMap = v
		}
	}

	// New tool use starting
	if toolUseID != "" && toolName != "" {
		if currentToolUse != nil && currentToolUse.ToolUseID != toolUseID {
			log.Warnf("kiro: interleaved tool use detected - new ID %s arrived while %s in progress, completing previous",
				toolUseID, currentToolUse.ToolUseID)
			if !processedIDs[currentToolUse.ToolUseID] {
				incomplete := KiroToolUse{
					ToolUseID: currentToolUse.ToolUseID,
					Name:      currentToolUse.Name,
				}
				if currentToolUse.InputBuffer.Len() > 0 {
					raw := currentToolUse.InputBuffer.String()
					repaired := RepairJSON(raw)

					var input map[string]interface{}
					if err := json.Unmarshal([]byte(repaired), &input); err != nil {
						log.Warnf("kiro: failed to parse interleaved tool input: %v, raw: %s", err, raw)
						input = make(map[string]interface{})
					}
					incomplete.Input = input
				}
				toolUses = append(toolUses, incomplete)
				processedIDs[currentToolUse.ToolUseID] = true
			}
			currentToolUse = nil
		}

		if currentToolUse == nil {
			if processedIDs != nil && processedIDs[toolUseID] {
				log.Debugf("kiro: skipping duplicate toolUseEvent: %s", toolUseID)
				return nil, nil
			}

			currentToolUse = &ToolUseState{
				ToolUseID: toolUseID,
				Name:      toolName,
			}
			log.Infof("kiro: starting new tool use: %s (ID: %s)", toolName, toolUseID)
		}
	}

	// Accumulate input fragments
	if currentToolUse != nil && inputFragment != "" {
		currentToolUse.InputBuffer.WriteString(inputFragment)
		log.Debugf("kiro: accumulated input fragment, total length: %d", currentToolUse.InputBuffer.Len())
	}

	// If complete input object provided directly
	if currentToolUse != nil && inputMap != nil {
		inputBytes, _ := json.Marshal(inputMap)
		currentToolUse.InputBuffer.Reset()
		currentToolUse.InputBuffer.Write(inputBytes)
	}

	// Tool use complete
	if isStop && currentToolUse != nil {
		fullInput := currentToolUse.InputBuffer.String()

		// Repair and parse the accumulated JSON
		repairedJSON := RepairJSON(fullInput)
		var finalInput map[string]interface{}
		if err := json.Unmarshal([]byte(repairedJSON), &finalInput); err != nil {
			log.Warnf("kiro: failed to parse accumulated tool input: %v, raw: %s", err, fullInput)
			finalInput = make(map[string]interface{})
		}

		toolUse := KiroToolUse{
			ToolUseID: currentToolUse.ToolUseID,
			Name:      currentToolUse.Name,
			Input:     finalInput,
		}

		// Run truncation detection only when explicitly enabled.
		if kirocommon.IsTruncationDetectorEnabled() {
			truncInfo := DetectTruncation(currentToolUse.Name, currentToolUse.ToolUseID, fullInput, finalInput)
			if truncInfo.IsTruncated {
				log.Warnf("kiro: truncation detected for tool %s (ID: %s): type=%s, raw_size=%d bytes",
					currentToolUse.Name, currentToolUse.ToolUseID, truncInfo.TruncationType, len(fullInput))
				toolUse.IsTruncated = true
				toolUse.TruncationInfo = &truncInfo
			}
		}

		toolUses = append(toolUses, toolUse)

		if processedIDs != nil {
			processedIDs[currentToolUse.ToolUseID] = true
		}

		log.Infof("kiro: completed tool use: %s (ID: %s, input: %d bytes)", currentToolUse.Name, currentToolUse.ToolUseID, len(fullInput))
		return toolUses, nil
	}

	return toolUses, currentToolUse
}

// DeduplicateToolUses removes duplicate tool uses based on toolUseId and content.
func DeduplicateToolUses(toolUses []KiroToolUse) []KiroToolUse {
	seenIDs := make(map[string]bool)
	seenContent := make(map[string]bool)
	var unique []KiroToolUse

	for _, tu := range toolUses {
		if seenIDs[tu.ToolUseID] {
			log.Debugf("kiro: removing ID-duplicate tool use: %s (name: %s)", tu.ToolUseID, tu.Name)
			continue
		}

		inputJSON, _ := json.Marshal(tu.Input)
		contentKey := tu.Name + ":" + string(inputJSON)

		if seenContent[contentKey] {
			log.Debugf("kiro: removing content-duplicate tool use: %s (id: %s)", tu.Name, tu.ToolUseID)
			continue
		}

		seenIDs[tu.ToolUseID] = true
		seenContent[contentKey] = true
		unique = append(unique, tu)
	}

	return unique
}

// Kiro IDE native tool-call format
// ---------------------------------
// When the request uses the AI_EDITOR origin, the Kiro-backed model frequently
// emits tool calls as inline text in Claude's XML function-calling format
// instead of structured toolUseEvent events:
//
//	<invoke name="grepSearch">
//	<parameter name="query">foo|bar</parameter>
//	<parameter name="includePattern">apps/**</parameter>
//	</invoke>
//
// It also uses Kiro IDE's own tool names (grepSearch, readFile, fileSearch,
// readMultipleFiles, listDirectory, fsRead, ...) rather than the tool names the
// client actually declared (Read, Grep, Glob, Bash, ...). If left as plain text
// the client never sees a tool_use block, so no tool ever runs and the model
// starts hallucinating fake tool results. The helpers below recover these calls
// by parsing the invoke XML and mapping the native names onto the client's
// canonical tools.

var (
	// invokeOpenPattern matches the invoke opening tag and captures the tool name.
	// The antml: namespace prefix is tolerated for compatibility.
	invokeOpenPattern = regexp.MustCompile(`<(?:antml:)?invoke\s+name="([^"]+)"\s*>`)
	// invokeCloseTag is the literal closing tag (with and without namespace).
	invokeCloseTag      = "</invoke>"
	invokeCloseTagAntml = "</antml:invoke>"
	// invokeStartTag / invokeStartTagAntml are the prefixes used for cross-chunk
	// boundary detection while streaming.
	invokeStartTag      = "<invoke"
	invokeStartTagAntml = "<antml:invoke"
	// parameterPattern matches a single <parameter name="k">value</parameter>.
	parameterPattern = regexp.MustCompile(`(?s)<(?:antml:)?parameter\s+name="([^"]+)"\s*>(.*?)</(?:antml:)?parameter>`)
)

// parseInvokeBlock parses a single complete <invoke ...>...</invoke> block and
// returns the native tool name plus its parameters. Parameter values are
// JSON-decoded when possible so arrays (paths) and numbers (depth) keep their
// types; otherwise they remain strings. Returns ok=false when the block is
// malformed (missing name or unparseable opening tag).
func parseInvokeBlock(block string) (name string, params map[string]interface{}, ok bool) {
	m := invokeOpenPattern.FindStringSubmatch(block)
	if m == nil {
		return "", nil, false
	}
	name = m[1]

	params = make(map[string]interface{})
	for _, pm := range parameterPattern.FindAllStringSubmatch(block, -1) {
		key := pm[1]
		raw := strings.TrimSpace(pm[2])
		// Preserve arrays/objects/numbers/bools by attempting a JSON decode first.
		var decoded interface{}
		if raw != "" && json.Unmarshal([]byte(raw), &decoded) == nil {
			params[key] = decoded
		} else {
			params[key] = raw
		}
	}
	return name, params, true
}

// firstStringParam returns the first usable string value for the given keys.
// It accepts either a plain string or a single-element array (for paths).
func firstStringParam(params map[string]interface{}, keys ...string) string {
	for _, k := range keys {
		v, exists := params[k]
		if !exists {
			continue
		}
		switch t := v.(type) {
		case string:
			if t != "" {
				return t
			}
		case []interface{}:
			for _, item := range t {
				if s, ok := item.(string); ok && s != "" {
					return s
				}
			}
		}
	}
	return ""
}

// intParam returns the integer value of a parameter, tolerating JSON numbers
// and numeric strings. Returns 0 when absent or unparseable.
func intParam(params map[string]interface{}, keys ...string) int {
	for _, k := range keys {
		v, exists := params[k]
		if !exists {
			continue
		}
		switch t := v.(type) {
		case float64:
			return int(t)
		case string:
			var n int
			if _, err := fmt.Sscanf(t, "%d", &n); err == nil {
				return n
			}
		}
	}
	return 0
}

// mapKiroNativeTool maps a Kiro IDE native tool call onto the client's
// canonical Claude tool name and input. Unknown native tools are passed through
// unchanged so the client can surface a meaningful "unknown tool" error.
func mapKiroNativeTool(name string, params map[string]interface{}) (string, map[string]interface{}) {
	switch name {
	case "readFile", "fsRead":
		return "Read", map[string]interface{}{
			"file_path": firstStringParam(params, "path", "file_path", "filePath"),
		}
	case "readMultipleFiles":
		return "Read", map[string]interface{}{
			"file_path": firstStringParam(params, "paths", "path", "file_path"),
		}
	case "grepSearch":
		input := map[string]interface{}{
			"pattern": firstStringParam(params, "query", "pattern"),
		}
		if glob := firstStringParam(params, "includePattern", "include", "glob"); glob != "" {
			input["glob"] = glob
		}
		return "Grep", input
	case "fileSearch":
		return "Glob", map[string]interface{}{
			"pattern": firstStringParam(params, "query", "pattern"),
		}
	case "listDirectory":
		path := firstStringParam(params, "path", "dir", "directory")
		if path == "" {
			path = "."
		}
		command := "ls " + path
		if intParam(params, "depth") > 1 {
			command = "ls -R " + path
		}
		return "Bash", map[string]interface{}{"command": command}
	case "writeFile", "fsWrite", "createFile":
		return "Write", map[string]interface{}{
			"file_path": firstStringParam(params, "path", "file_path", "filePath"),
			"content":   firstStringParam(params, "content", "fileText", "text", "file_text"),
		}
	case "strReplace", "strReplaceEditor", "editFile", "replaceInFile":
		return "Edit", map[string]interface{}{
			"file_path":  firstStringParam(params, "path", "file_path", "filePath"),
			"old_string": firstStringParam(params, "oldStr", "old_string", "old_str"),
			"new_string": firstStringParam(params, "newStr", "new_string", "new_str"),
		}
	case "executeBash", "runCommand", "executeCommand", "bash", "shell":
		return "Bash", map[string]interface{}{
			"command": firstStringParam(params, "command", "cmd"),
		}
	default:
		// Unrecognized native tool: pass through with original name/params.
		return name, params
	}
}

// buildInvokeToolUse converts a parsed native invoke block into a KiroToolUse,
// applying the native->client tool name mapping and deduplication.
func buildInvokeToolUse(block string, processedIDs map[string]bool) *KiroToolUse {
	nativeName, params, ok := parseInvokeBlock(block)
	if !ok {
		return nil
	}
	clientName, input := mapKiroNativeTool(nativeName, params)

	inputJSON, _ := json.Marshal(input)
	dedupeKey := clientName + ":" + string(inputJSON)
	if processedIDs != nil {
		if processedIDs[dedupeKey] {
			log.Debugf("kiro: skipping duplicate invoke tool call: %s", clientName)
			return nil
		}
		processedIDs[dedupeKey] = true
	}

	toolUseID := "toolu_" + uuid.New().String()[:12]
	log.Infof("kiro: extracted invoke tool call: %s -> %s (ID: %s)", nativeName, clientName, toolUseID)
	return &KiroToolUse{
		ToolUseID: toolUseID,
		Name:      clientName,
		Input:     input,
	}
}

// findInvokeClose returns the index just past the end of the first invoke
// closing tag at or after from. The boolean reports whether one was found.
func findInvokeClose(s string, from int) (int, bool) {
	idx := -1
	if i := strings.Index(s[from:], invokeCloseTag); i >= 0 {
		idx = from + i + len(invokeCloseTag)
	}
	if i := strings.Index(s[from:], invokeCloseTagAntml); i >= 0 {
		if end := from + i + len(invokeCloseTagAntml); idx == -1 || end < idx {
			idx = end
		}
	}
	if idx < 0 {
		return 0, false
	}
	return idx, true
}

// findInvokeOpen returns the index of the first invoke opening tag ("<invoke"
// or "<antml:invoke") at or after from. The boolean reports whether one was found.
func findInvokeOpen(s string, from int) (int, bool) {
	idx := -1
	if i := strings.Index(s[from:], invokeStartTag); i >= 0 {
		idx = from + i
	}
	if i := strings.Index(s[from:], invokeStartTagAntml); i >= 0 {
		if idx == -1 || from+i < idx {
			idx = from + i
		}
	}
	if idx < 0 {
		return 0, false
	}
	return idx, true
}

// pendingInvokeSuffix returns the length of the trailing partial invoke-start
// tag (e.g. "<inv", "<antml:in") that should be held back for the next chunk.
func pendingInvokeSuffix(s string) int {
	a := PendingTagSuffix(s, invokeStartTag)
	b := PendingTagSuffix(s, invokeStartTagAntml)
	if b > a {
		return b
	}
	return a
}

// ParseInvokeToolCalls extracts complete <invoke name="...">...</invoke> blocks
// from a full (non-streaming) response body. It returns the text with the
// invoke blocks removed and the recovered tool calls (mapped to client tools).
// Incomplete/trailing invoke markup is left in the text unchanged.
func ParseInvokeToolCalls(text string, processedIDs map[string]bool) (string, []KiroToolUse) {
	if !strings.Contains(text, invokeStartTag) && !strings.Contains(text, invokeStartTagAntml) {
		return text, nil
	}

	var toolUses []KiroToolUse
	var clean strings.Builder

	remaining := text
	for {
		open, hasOpen := findInvokeOpen(remaining, 0)
		if !hasOpen {
			clean.WriteString(remaining)
			break
		}
		// Emit text before the invoke block.
		clean.WriteString(remaining[:open])

		closeIdx, hasClose := findInvokeClose(remaining, open)
		if !hasClose {
			// No closing tag yet (truncated response) - keep the rest as text.
			clean.WriteString(remaining[open:])
			break
		}

		block := remaining[open:closeIdx]
		if tu := buildInvokeToolUse(block, processedIDs); tu != nil {
			toolUses = append(toolUses, *tu)
		}
		remaining = remaining[closeIdx:]
	}

	return clean.String(), toolUses
}

// InvokeStreamParser incrementally recovers <invoke> tool calls from a stream
// of content deltas. Text outside invoke blocks is passed through as ordinary
// text; complete invoke blocks are converted into tool calls. Partial tags at
// chunk boundaries are buffered until the closing tag arrives.
type InvokeStreamParser struct {
	buf          strings.Builder
	processedIDs map[string]bool
}

// NewInvokeStreamParser creates a streaming invoke parser. processedIDs is
// shared with the structured tool-use dedup map so the same call is not emitted
// twice across the text and structured channels.
func NewInvokeStreamParser(processedIDs map[string]bool) *InvokeStreamParser {
	return &InvokeStreamParser{processedIDs: processedIDs}
}

// Feed consumes one content delta and returns any plain text that is safe to
// emit now plus any tool calls completed by this delta.
func (p *InvokeStreamParser) Feed(delta string) (string, []KiroToolUse) {
	p.buf.WriteString(delta)

	var textOut strings.Builder
	var toolUses []KiroToolUse

	for {
		s := p.buf.String()
		open, hasOpen := findInvokeOpen(s, 0)
		if !hasOpen {
			// No invoke tag. Hold back a trailing partial tag, emit the rest.
			hold := pendingInvokeSuffix(s)
			if hold > 0 {
				textOut.WriteString(s[:len(s)-hold])
				p.buf.Reset()
				p.buf.WriteString(s[len(s)-hold:])
			} else {
				textOut.WriteString(s)
				p.buf.Reset()
			}
			break
		}

		// Emit text before the invoke block.
		if open > 0 {
			textOut.WriteString(s[:open])
		}

		closeIdx, hasClose := findInvokeClose(s, open)
		if !hasClose {
			// Invoke not complete yet - keep buffering from the open tag.
			p.buf.Reset()
			p.buf.WriteString(s[open:])
			break
		}

		block := s[open:closeIdx]
		if tu := buildInvokeToolUse(block, p.processedIDs); tu != nil {
			toolUses = append(toolUses, *tu)
		}
		p.buf.Reset()
		p.buf.WriteString(s[closeIdx:])
	}

	return textOut.String(), toolUses
}

// Flush emits any buffered content at end of stream. An incomplete invoke is
// surfaced as plain text so no content is silently dropped.
func (p *InvokeStreamParser) Flush() (string, []KiroToolUse) {
	s := p.buf.String()
	p.buf.Reset()
	if s == "" {
		return "", nil
	}
	// A complete invoke could in theory sit in the buffer if the closing tag was
	// the very last thing buffered; Feed already handles those, so whatever
	// remains here is incomplete markup -> emit as text.
	return s, nil
}
