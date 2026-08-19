// Package common provides shared constants and utilities for Kiro translator.
package common

import "sync/atomic"

const (
	// KiroMaxToolDescLen is the maximum description length for Kiro API tools.
	// Kiro API limit is 10240 bytes, leave room for "..."
	KiroMaxToolDescLen = 10237

	// ThinkingStartTag is the start tag for thinking blocks in responses.
	ThinkingStartTag = "<thinking>"

	// ThinkingEndTag is the end tag for thinking blocks in responses.
	ThinkingEndTag = "</thinking>"

	// CodeFenceMarker is the markdown code fence marker.
	CodeFenceMarker = "```"

	// AltCodeFenceMarker is the alternative markdown code fence marker.
	AltCodeFenceMarker = "~~~"

	// InlineCodeMarker is the markdown inline code marker (backtick).
	InlineCodeMarker = "`"

	// DefaultAssistantContentWithTools is the fallback content for assistant messages
	// that have tool_use but no text content. Kiro API requires non-empty content.
	// IMPORTANT: Use a bracketed marker so the model recognizes it as a structural
	// placeholder rather than conversational content to parrot back.
	// History: "." caused the model to echo "." in subsequent turns; "I'll help
	// you with that." caused parroting of that exact phrase.
	DefaultAssistantContentWithTools = "[tool_call]"

	// DefaultAssistantContent is the fallback content for assistant messages
	// that have no content at all. Kiro API requires non-empty content.
	// IMPORTANT: Use a bracketed marker so the model recognizes it as a structural
	// placeholder rather than conversational content to parrot back.
	DefaultAssistantContent = "[empty]"

	// DefaultUserContentWithToolResults is the fallback content for user messages
	// that have only tool_result (no text). Kiro API requires non-empty content.
	// IMPORTANT: Use a bracketed marker so the model recognizes it as a structural
	// placeholder rather than conversational content to parrot back.
	DefaultUserContentWithToolResults = "[tool_result]"

	// DefaultUserContent is the fallback content for user messages
	// that have no content at all. Kiro API requires non-empty content.
	// IMPORTANT: Use a bracketed marker so the model recognizes it as a structural
	// placeholder rather than conversational content to parrot back.
	DefaultUserContent = "[continue]"

	// KiroAgenticSystemPrompt is injected only for -agentic models to prevent timeouts on large writes.
	// AWS Kiro API has a 2-3 minute timeout for large file write operations.
	KiroAgenticSystemPrompt = `
# CRITICAL: CHUNKED WRITE PROTOCOL (MANDATORY)

You MUST follow these rules for ALL file operations. Violation causes server timeouts and task failure.

## ABSOLUTE LIMITS
- **MAXIMUM 350 LINES** per single write/edit operation - NO EXCEPTIONS
- **RECOMMENDED 300 LINES** or less for optimal performance
- **NEVER** write entire files in one operation if >300 lines

## MANDATORY CHUNKED WRITE STRATEGY

### For NEW FILES (>300 lines total):
1. FIRST: Write initial chunk (first 250-300 lines) using write_to_file/fsWrite
2. THEN: Append remaining content in 250-300 line chunks using file append operations
3. REPEAT: Continue appending until complete

### For EDITING EXISTING FILES:
1. Use surgical edits (apply_diff/targeted edits) - change ONLY what's needed
2. NEVER rewrite entire files - use incremental modifications
3. Split large refactors into multiple small, focused edits

### For LARGE CODE GENERATION:
1. Generate in logical sections (imports, types, functions separately)
2. Write each section as a separate operation
3. Use append operations for subsequent sections

## EXAMPLES OF CORRECT BEHAVIOR

✅ CORRECT: Writing a 600-line file
- Operation 1: Write lines 1-300 (initial file creation)
- Operation 2: Append lines 301-600

✅ CORRECT: Editing multiple functions
- Operation 1: Edit function A
- Operation 2: Edit function B
- Operation 3: Edit function C

❌ WRONG: Writing 500 lines in single operation → TIMEOUT
❌ WRONG: Rewriting entire file to change 5 lines → TIMEOUT
❌ WRONG: Generating massive code blocks without chunking → TIMEOUT

## WHY THIS MATTERS
- Server has 2-3 minute timeout for operations
- Large writes exceed timeout and FAIL completely
- Chunked writes are FASTER and more RELIABLE
- Failed writes waste time and require retry

REMEMBER: When in doubt, write LESS per operation. Multiple small operations > one large operation.`
)

// systemPromptInjectEnabled controls whether system prompts are wrapped with
// --- SYSTEM PROMPT --- markers and injected into Kiro user messages.
// Default: 0 (disabled). Set to 1 to inject wrapped system prompts.
var systemPromptInjectEnabled atomic.Int32

func init() {
	systemPromptInjectEnabled.Store(0)
}

// SetSystemPromptInjectEnabled configures whether system prompts should be
// injected into Kiro user messages. When false, system prompts are dropped
// entirely — Kiro API will not see any system instructions.
func SetSystemPromptInjectEnabled(enabled bool) {
	if enabled {
		systemPromptInjectEnabled.Store(1)
	} else {
		systemPromptInjectEnabled.Store(0)
	}
}

// IsSystemPromptInjectEnabled reports whether system prompt injection is active.
func IsSystemPromptInjectEnabled() bool {
	return systemPromptInjectEnabled.Load() == 1
}

// truncationDetectorEnabled controls whether the heuristic truncation detector
// is applied to Kiro tool use responses. When enabled, tool calls that appear
// truncated (invalid JSON, missing required fields, etc.) are silently skipped.
// Default: 0 (disabled). The detector uses heuristic matching that can produce
// false positives (e.g. code fence counting), so it is off by default.
var truncationDetectorEnabled atomic.Int32

func init() {
	truncationDetectorEnabled.Store(0)
}

// SetTruncationDetectorEnabled toggles the heuristic truncation detector.
func SetTruncationDetectorEnabled(enabled bool) {
	if enabled {
		truncationDetectorEnabled.Store(1)
	} else {
		truncationDetectorEnabled.Store(0)
	}
}

// IsTruncationDetectorEnabled reports whether the truncation detector is active.
func IsTruncationDetectorEnabled() bool {
	return truncationDetectorEnabled.Load() == 1
}

// extractThinkingTagEnabled controls whether inline <thinking>...</thinking>
// tags inside assistantResponseEvent content are parsed into Claude thinking
// blocks. This is an unofficial path — Kiro's official reasoning signal is
// reasoningContentEvent. The tag parser can false-positive when content
// literally mentions the tag string (code samples, discussion, XML fixtures),
// which silently truncates responses. Default: 0 (disabled).
var extractThinkingTagEnabled atomic.Int32

func init() {
	extractThinkingTagEnabled.Store(0)
}

// SetExtractThinkingTagEnabled toggles inline <thinking> tag extraction.
func SetExtractThinkingTagEnabled(enabled bool) {
	if enabled {
		extractThinkingTagEnabled.Store(1)
	} else {
		extractThinkingTagEnabled.Store(0)
	}
}

// IsExtractThinkingTagEnabled reports whether inline <thinking> tag extraction is active.
func IsExtractThinkingTagEnabled() bool {
	return extractThinkingTagEnabled.Load() == 1
}
