// Package claude provides tool calling support for Kiro to Claude translation.
// This package handles parsing embedded tool calls, JSON repair, and deduplication.
package claude

import (
	"encoding/json"
	"regexp"
	"strings"

	"github.com/google/uuid"
	kirocommon "github.com/router-for-me/CLIProxyAPI/v6/internal/translator/kiro/common"
	log "github.com/sirupsen/logrus"
)

// ToolUseState tracks the state of an in-progress tool use during streaming.
type ToolUseState struct {
	ToolUseID      string
	Name           string
	InputBuffer    strings.Builder
	IsComplete     bool
	TruncationInfo *TruncationInfo // Truncation detection result (set when complete)
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

		// Detect truncation for all tools
		truncInfo := DetectTruncation(currentToolUse.Name, currentToolUse.ToolUseID, fullInput, finalInput)
		if truncInfo.IsTruncated {
			log.Warnf("kiro: TRUNCATION DETECTED for tool %s (ID: %s): type=%s, raw_size=%d bytes",
				currentToolUse.Name, currentToolUse.ToolUseID, truncInfo.TruncationType, len(fullInput))
			log.Warnf("kiro: truncation details: %s", truncInfo.ErrorMessage)
			if len(truncInfo.ParsedFields) > 0 {
				log.Infof("kiro: partial fields received: %v", truncInfo.ParsedFields)
			}
			// Store truncation info in the state for upstream handling
			currentToolUse.TruncationInfo = &truncInfo
		} else {
			log.Infof("kiro: tool use %s input length: %d bytes (no truncation)", currentToolUse.Name, len(fullInput))
		}

		// Create the tool use with truncation info if applicable
		toolUse := KiroToolUse{
			ToolUseID:      currentToolUse.ToolUseID,
			Name:           currentToolUse.Name,
			Input:          finalInput,
			IsTruncated:    truncInfo.IsTruncated,
			TruncationInfo: nil, // Will be set below if truncated
		}
		if truncInfo.IsTruncated {
			toolUse.TruncationInfo = &truncInfo
		}
		toolUses = append(toolUses, toolUse)

		if processedIDs != nil {
			processedIDs[currentToolUse.ToolUseID] = true
		}

		log.Infof("kiro: completed tool use: %s (ID: %s, truncated: %v)", currentToolUse.Name, currentToolUse.ToolUseID, truncInfo.IsTruncated)
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
