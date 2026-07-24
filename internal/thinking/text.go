package thinking

import (
	"regexp"
	"strings"

	"github.com/tidwall/gjson"
)

// GetThinkingText extracts the thinking text from a content part.
// Handles various formats:
// - Simple string: { "thinking": "text" } or { "text": "text" }
// - Wrapped object: { "thinking": { "text": "text", "cache_control": {...} } }
// - Gemini-style: { "thought": true, "text": "text" }
// Returns the extracted text string.
func GetThinkingText(part gjson.Result) string {
	// Try direct text field first (Gemini-style)
	if text := part.Get("text"); text.Exists() && text.Type == gjson.String {
		return text.String()
	}

	// Try thinking field
	thinkingField := part.Get("thinking")
	if !thinkingField.Exists() {
		return ""
	}

	// thinking is a string
	if thinkingField.Type == gjson.String {
		return thinkingField.String()
	}

	// thinking is an object with inner text/thinking
	if thinkingField.IsObject() {
		if inner := thinkingField.Get("text"); inner.Exists() && inner.Type == gjson.String {
			return inner.String()
		}
		if inner := thinkingField.Get("thinking"); inner.Exists() && inner.Type == gjson.String {
			return inner.String()
		}
	}

	return ""
}

// thoughtTagPattern matches <thought>...</thought> and <think>...</think> tags, including
// multiline content. It is used to extract thinking content that providers like MiniMax
// embed inside XML tags within the regular content field.
var thoughtTagPattern = regexp.MustCompile(`<thought>([\s\S]*?)</thought>|<think>([\s\S]*?)</think>`)

// StripThoughtTags removes <thought>...</thought> and <think>...</think> tags from text,
// returning the extracted thinking content and the cleaned visible text.
//
// Some OpenAI-compatible providers (e.g., MiniMax M3 via OpenCode Go) embed reasoning
// content inside XML tags within the regular content field instead of using the standard
// reasoning_content field. This function separates the tagged thinking content so it can
// be placed into proper Anthropic thinking blocks.
//
// If no tags are found, thinking returns empty string and clean is the original text.
func StripThoughtTags(text string) (thinking, clean string) {
	if !strings.Contains(text, "<thought") && !strings.Contains(text, "<think") {
		return "", text
	}

	var thinkingParts []string
	clean = thoughtTagPattern.ReplaceAllStringFunc(text, func(match string) string {
		// Extract content between tags
		inner := match
		if strings.HasPrefix(match, "<thought>") {
			inner = match[len("<thought>") : len(match)-len("</thought>")]
		} else if strings.HasPrefix(match, "<think>") {
			inner = match[len("<think>") : len(match)-len("</think>")]
		}
		if trimmed := strings.TrimSpace(inner); trimmed != "" {
			thinkingParts = append(thinkingParts, trimmed)
		}
		return " "
	})

	if len(thinkingParts) > 0 {
		thinking = strings.Join(thinkingParts, "\n")
	}
	// Collapse multiple spaces introduced by tag removal.
	clean = strings.TrimSpace(clean)
	clean = multiSpace.ReplaceAllString(clean, " ")
	return thinking, clean
}

// multiSpace matches two or more consecutive whitespace characters.
var multiSpace = regexp.MustCompile(`\s{2,}`)

// ThoughtTagStripper maintains state for stripping thought tags across streaming chunks.
// Some providers emit <thought> tags that span multiple SSE delta chunks, requiring
// stateful processing.
type ThoughtTagStripper struct {
	buf         strings.Builder
	inThought   bool
	currentTag  string // "thought" or "think"
	thinkingBuf strings.Builder
}

// NewThoughtTagStripper creates a new ThoughtTagStripper instance.
func NewThoughtTagStripper() *ThoughtTagStripper {
	return &ThoughtTagStripper{}
}

// Feed processes a streaming content chunk and returns any extracted thinking text
// and the cleaned visible text. The thinking and visible outputs may each be empty
// if the chunk is entirely consumed by tag processing.
func (s *ThoughtTagStripper) Feed(chunk string) (thinking, visible string) {
	s.buf.WriteString(chunk)
	full := s.buf.String()
	s.buf.Reset()

	var thinkingOut strings.Builder
	var visibleOut strings.Builder

	emitVisible := func(text string) {
		text = strings.TrimSpace(text)
		if text == "" {
			return
		}
		if visibleOut.Len() > 0 {
			visibleOut.WriteByte(' ')
		}
		visibleOut.WriteString(text)
	}

	emitThinking := func(text string) {
		text = strings.TrimSpace(text)
		if text == "" {
			return
		}
		if thinkingOut.Len() > 0 {
			thinkingOut.WriteByte('\n')
		}
		thinkingOut.WriteString(text)
	}

	i := 0
	for i < len(full) {
		if s.inThought {
			closeTag := "</" + s.currentTag + ">"
			closeIdx := strings.Index(full[i:], closeTag)
			if closeIdx >= 0 {
				s.thinkingBuf.WriteString(full[i : i+closeIdx])
				emitThinking(s.thinkingBuf.String())
				s.thinkingBuf.Reset()
				i += closeIdx + len(closeTag)
				s.inThought = false
				s.currentTag = ""
			} else {
				s.thinkingBuf.WriteString(full[i:])
				i = len(full)
			}
			continue
		}

		// Not in a thought — look for an opening tag.
		thoughtIdx := strings.Index(full[i:], "<thought>")
		thinkIdx := strings.Index(full[i:], "<think>")

		openIdx := -1
		tagName := ""
		if thoughtIdx >= 0 && (thinkIdx < 0 || thoughtIdx <= thinkIdx) {
			openIdx = thoughtIdx
			tagName = "thought"
		} else if thinkIdx >= 0 {
			openIdx = thinkIdx
			tagName = "think"
		}

		if openIdx < 0 {
			// No opening tag in buffer. Check for a partial tag at the very end
			// that could complete on the next chunk.
			remaining := full[i:]
			partialStart := lastPotentialTagPrefix(remaining)
			if partialStart >= 0 {
				emitVisible(remaining[:partialStart])
				// Keep the partial prefix in buffer for next chunk.
				s.buf.WriteString(remaining[partialStart:])
			} else {
				emitVisible(remaining)
			}
			i = len(full)
			continue
		}

		// Emit text before the tag.
		emitVisible(full[i : i+openIdx])

		// Enter thought.
		i += openIdx + len("<"+tagName+">")
		s.inThought = true
		s.currentTag = tagName

		// Check if closing tag is in the same buffer.
		closeTag := "</" + tagName + ">"
		if closeIdx := strings.Index(full[i:], closeTag); closeIdx >= 0 {
			emitThinking(full[i : i+closeIdx])
			i += closeIdx + len(closeTag)
			s.inThought = false
			s.currentTag = ""
		} else {
			s.thinkingBuf.WriteString(full[i:])
			i = len(full)
		}
	}

	return thinkingOut.String(), visibleOut.String()
}

// lastPotentialTagPrefix checks if the trailing portion of s looks like it could be
// the start of <thought>, </thought>, <think>, or </think>. Returns the index within s
// where the potential tag starts, or -1 if no partial tag prefix is detected.
func lastPotentialTagPrefix(s string) int {
	lastLT := strings.LastIndex(s, "<")
	if lastLT < 0 {
		return -1
	}
	candidate := s[lastLT:]

	for _, tag := range []string{
		"<thought>", "</thought>", "<think>", "</think>",
	} {
		if strings.HasPrefix(tag, candidate) && len(candidate) < len(tag) {
			return lastLT
		}
	}
	return -1
}

// Flush returns any remaining buffered thinking text. Call this when the stream
// ends to ensure no thinking content is lost. Also returns any remaining visible
// text in the buffer.
func (s *ThoughtTagStripper) Flush() (thinking, visible string) {
	if s.inThought {
		// Unterminated thought tag — treat buffered content as thinking
		thinking = strings.TrimSpace(s.thinkingBuf.String())
	}
	// Any remaining non-tag content
	if !s.inThought {
		visible = strings.TrimSpace(s.buf.String())
	}
	s.buf.Reset()
	s.thinkingBuf.Reset()
	s.inThought = false
	s.currentTag = ""
	return thinking, visible
}

// Reset clears all internal state for reuse.
func (s *ThoughtTagStripper) Reset() {
	s.buf.Reset()
	s.thinkingBuf.Reset()
	s.inThought = false
	s.currentTag = ""
}
