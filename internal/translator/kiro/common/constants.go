// Package common provides shared constants and utilities for Kiro translator.
package common

const (
	// KiroMaxToolDescLen is the maximum description length for Kiro API tools.
	// Kiro API limit is 10240 bytes, leave room for "..."
	KiroMaxToolDescLen = 10237

	// ToolCompressionTargetSize is the target total size for compressed tools (20KB).
	// If tools exceed this size, compression will be applied.
	ToolCompressionTargetSize = 20 * 1024 // 20KB

	// MinToolDescriptionLength is the minimum description length after compression.
	// Descriptions will not be shortened below this length.
	MinToolDescriptionLength = 50

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
	DefaultAssistantContentWithTools = "I'll help you with that."

	// DefaultAssistantContent is the fallback content for assistant messages
	// that have no content at all. Kiro API requires non-empty content.
	DefaultAssistantContent = "I understand."

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
