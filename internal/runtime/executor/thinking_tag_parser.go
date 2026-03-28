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
)

var thinkingStartPartials = generatePartials(thinkingStartTag)
var thinkingEndPartials = generatePartials(thinkingEndTag)

func generatePartials(tag string) []string {
	partials := make([]string, 0, len(tag)-1)
	for i := len(tag) - 1; i > 0; i-- {
		partials = append(partials, tag[:i])
	}
	return partials
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

func extractThoughtSignature(part gjson.Result) string {
	sig := part.Get("thoughtSignature").String()
	if sig == "" {
		sig = part.Get("thought_signature").String()
	}
	return sig
}

func rewriteThinkingSegmentPart(original string, segment thinkingTextSegment) (string, error) {
	updated, err := sjson.Set(original, "text", segment.text)
	if err != nil {
		return "", err
	}
	if segment.thought {
		updated, err = sjson.Set(updated, "thought", true)
		if err != nil {
			return "", err
		}
	} else {
		updated, _ = sjson.Delete(updated, "thought")
	}
	updated, _ = sjson.Delete(updated, "thoughtSignature")
	updated, _ = sjson.Delete(updated, "thought_signature")
	return updated, nil
}

func buildThoughtSignaturePart(signature string) (string, error) {
	// Keep signatures on their own thought part instead of attaching them to
	// the text-bearing thought segment. Downstream Claude translation treats a
	// signature-bearing thought part as the delimiter that closes and caches the
	// accumulated thinking text.
	partJSON := "{\"text\":\"\",\"thought\":true}"
	return sjson.Set(partJSON, "thoughtSignature", signature)
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

partLoop:
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
		if p.tagBuffer == "" && !p.inThinking && !strings.Contains(originalText, "<") {
			updatedParts = append(updatedParts, part.Raw)
			continue
		}

		originalThought := part.Get("thought").Bool()
		signature := extractThoughtSignature(part)
		wasInThinking := p.inThinking
		text := originalText
		if p.tagBuffer != "" {
			log.Debugf("antigravity executor: thinking tag parser prepending buffered tag: %q", p.tagBuffer)
			text = p.tagBuffer + text
			p.tagBuffer = ""
		}

		segments := p.splitThinkingText(text)
		hasThoughtSegments := false
		for _, segment := range segments {
			if segment.thought && segment.text != "" {
				hasThoughtSegments = true
				break
			}
		}
		// Preserve a signature when this source part finishes a thinking run,
		// even if the current chunk only contains a closing tag and visible text.
		shouldEmitSignature := signature != "" && !p.inThinking && (hasThoughtSegments || wasInThinking)
		thoughtStateChanged := len(segments) == 1 &&
			segments[0].thought != originalThought &&
			(wasInThinking || p.inThinking)
		changedByParsing := len(segments) != 1 ||
			(len(segments) == 1 && (segments[0].text != originalText || thoughtStateChanged)) ||
			wasInThinking != p.inThinking
		if !changedByParsing && !shouldEmitSignature {
			updatedParts = append(updatedParts, part.Raw)
			continue
		}

		changed = true
		rewrittenParts := make([]string, 0, len(segments)+1)
		lastThoughtIdx := -1
		for _, segment := range segments {
			if segment.text == "" {
				continue
			}
			partJSON, err := rewriteThinkingSegmentPart(part.Raw, segment)
			if err != nil {
				log.Errorf("antigravity executor: thinking tag parser failed to rewrite split part: %v", err)
				updatedParts = append(updatedParts, part.Raw)
				continue partLoop
			}
			rewrittenParts = append(rewrittenParts, partJSON)
			if segment.thought {
				lastThoughtIdx = len(rewrittenParts) - 1
			}
		}
		if shouldEmitSignature {
			signaturePart, err := buildThoughtSignaturePart(signature)
			if err != nil {
				log.Errorf("antigravity executor: thinking tag parser failed to build signature part: %v", err)
				updatedParts = append(updatedParts, part.Raw)
				continue
			}
			insertAt := 0
			if lastThoughtIdx >= 0 {
				// The signature must come immediately after the final derived
				// thought segment so downstream consumers can associate it with
				// the thinking text that just ended, before any visible text.
				insertAt = lastThoughtIdx + 1
			}
			rewrittenParts = append(rewrittenParts[:insertAt], append([]string{signaturePart}, rewrittenParts[insertAt:]...)...)
		}
		if len(rewrittenParts) == 0 {
			continue
		}
		if len(rewrittenParts) > 1 {
			log.Debugf("antigravity executor: thinking tag parser split chunk into %d segments", len(rewrittenParts))
		}
		updatedParts = append(updatedParts, rewrittenParts...)
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
			return text[:len(text)-len(partial)], partial
		}
	}
	return text, ""
}
