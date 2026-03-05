package executor

import (
	"strings"

	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	thinkingStartTag = "<thinking>"
	thinkingEndTag   = "</thinking>"
	maxTagBufferLen  = 12
)

var thinkingStartPartials = []string{
	"<thinking",
	"<thinkin",
	"<thinki",
	"<think",
	"<thin",
	"<thi",
	"<th",
	"<t",
	"<",
}

var thinkingEndPartials = []string{
	"</thinking",
	"</thinkin",
	"</thinki",
	"</think",
	"</thin",
	"</thi",
	"</th",
	"</t",
	"</",
}

type ThinkingTagParser struct {
	inThinking bool
	tagBuffer  string
	active     bool
}

func NewThinkingTagParser(modelName string) *ThinkingTagParser {
	active := strings.Contains(strings.ToLower(modelName), "claude")
	if active {
		log.Debugf("antigravity executor: thinking tag parser activated for model: %s", modelName)
	}
	return &ThinkingTagParser{
		active: active,
	}
}

type thinkingTextSegment struct {
	text    string
	thought bool
}

func (p *ThinkingTagParser) Process(payload []byte) []byte {
	if !p.active {
		return payload
	}
	if len(payload) == 0 || !gjson.ValidBytes(payload) {
		return payload
	}

	if p.inThinking {
		log.Debugf("antigravity executor: thinking tag parser processing payload (%d bytes), inThinking=%v, tagBuffer=%q", len(payload), p.inThinking, p.tagBuffer)
	}

	partsResult := gjson.GetBytes(payload, "response.candidates.0.content.parts")
	if !partsResult.Exists() || !partsResult.IsArray() {
		return payload
	}

	parts := partsResult.Array()
	if len(parts) == 0 {
		return payload
	}

	updatedParts := make([]string, 0, len(parts))
	changed := false

	for _, part := range parts {
		if !part.Get("text").Exists() {
			updatedParts = append(updatedParts, part.Raw)
			continue
		}
		if part.Get("functionCall").Exists() || part.Get("inlineData").Exists() || part.Get("inline_data").Exists() {
			updatedParts = append(updatedParts, part.Raw)
			continue
		}

		originalText := part.Get("text").String()
		text := originalText
		if p.tagBuffer != "" {
			log.Debugf("antigravity executor: thinking tag parser prepending buffered tag: %q", p.tagBuffer)
			text = p.tagBuffer + text
			p.tagBuffer = ""
		}

		segments := p.splitThinkingText(text)
		if len(segments) == 0 {
			changed = true
			continue
		}

		if len(segments) == 1 {
			updated := part.Raw
			if segments[0].text != originalText {
				updated, _ = sjson.Set(updated, "text", segments[0].text)
			}
			if segments[0].thought && !part.Get("thought").Bool() {
				updated, _ = sjson.Set(updated, "thought", true)
			}
			if updated != part.Raw {
				changed = true
			}
			updatedParts = append(updatedParts, updated)
			continue
		}

		changed = true
		log.Debugf("antigravity executor: thinking tag parser split chunk into %d segments", len(segments))
		for _, segment := range segments {
			if segment.text == "" {
				continue
			}
			partJSON := `{}`
			partJSON, _ = sjson.Set(partJSON, "text", segment.text)
			if segment.thought {
				partJSON, _ = sjson.Set(partJSON, "thought", true)
			}
			updatedParts = append(updatedParts, partJSON)
		}
	}

	if !changed {
		return payload
	}

	partsJSON := "[" + strings.Join(updatedParts, ",") + "]"
	updated, err := sjson.SetRawBytes(payload, "response.candidates.0.content.parts", []byte(partsJSON))
	if err != nil {
		log.Errorf("antigravity executor: thinking tag parser failed to set updated parts: %v", err)
		return payload
	}
	log.Debugf("antigravity executor: thinking tag parser rewrote parts (%d parts)", len(updatedParts))
	return updated
}

func (p *ThinkingTagParser) splitThinkingText(text string) []thinkingTextSegment {
	if text == "" {
		return []thinkingTextSegment{{text: "", thought: p.inThinking}}
	}

	segments := make([]thinkingTextSegment, 0, 2)
	remaining := text

	for len(remaining) > 0 {
		if p.inThinking {
			endIdx := strings.Index(remaining, thinkingEndTag)
			if endIdx >= 0 {
				if endIdx > 0 {
					segments = append(segments, thinkingTextSegment{text: remaining[:endIdx], thought: true})
				}
				remaining = remaining[endIdx+len(thinkingEndTag):]
				p.inThinking = false
				log.Debugf("antigravity executor: thinking tag parser found </thinking>, exiting thinking state")
				continue
			}

			trimmed, buffer := splitTrailingPartialTag(remaining, thinkingEndPartials)
			if buffer != "" {
				p.tagBuffer = buffer
				log.Debugf("antigravity executor: thinking tag parser buffered partial end tag: %q", buffer)
			}
			if trimmed != "" {
				segments = append(segments, thinkingTextSegment{text: trimmed, thought: true})
			}
			remaining = ""
			continue
		}

		startIdx := strings.Index(remaining, thinkingStartTag)
		if startIdx >= 0 {
			if startIdx > 0 {
				segments = append(segments, thinkingTextSegment{text: remaining[:startIdx], thought: false})
			}
			remaining = remaining[startIdx+len(thinkingStartTag):]
			p.inThinking = true
			log.Debugf("antigravity executor: thinking tag parser found <thinking>, entering thinking state")
			continue
		}

		trimmed, buffer := splitTrailingPartialTag(remaining, thinkingStartPartials)
		if buffer != "" {
			p.tagBuffer = buffer
			log.Debugf("antigravity executor: thinking tag parser buffered partial start tag: %q", buffer)
		}
		if trimmed != "" {
			segments = append(segments, thinkingTextSegment{text: trimmed, thought: false})
		}
		remaining = ""
	}

	return segments
}

func splitTrailingPartialTag(text string, partials []string) (string, string) {
	for _, partial := range partials {
		if strings.HasSuffix(text, partial) {
			if len(partial) > maxTagBufferLen {
				return text, ""
			}
			return text[:len(text)-len(partial)], partial
		}
	}
	return text, ""
}
