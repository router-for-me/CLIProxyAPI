package geminichat

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/relaykit/dto"
	kitutil "github.com/QuantumNous/new-api/relaykit/relayconvert/kitutil"
)

type geminiGroundingChunk struct {
	Web              *geminiGroundingSource `json:"web,omitempty"`
	RetrievedContext *geminiGroundingSource `json:"retrievedContext,omitempty"`
}

type geminiGroundingSource struct {
	URI   string `json:"uri,omitempty"`
	Title string `json:"title,omitempty"`
}

type geminiGroundingSupport struct {
	Segment struct {
		PartIndex  *int   `json:"partIndex,omitempty"`
		StartIndex int    `json:"startIndex,omitempty"`
		EndIndex   int    `json:"endIndex,omitempty"`
		Text       string `json:"text,omitempty"`
	} `json:"segment"`
	GroundingChunkIndices []int `json:"groundingChunkIndices"`
}

type renderedGeminiPart struct {
	text      string
	startByte int
}

type streamedGeminiPartSpan struct {
	partStartByte     int
	partEndByte       int
	renderedStartByte int
}

type streamedGeminiPart struct {
	text  strings.Builder
	spans []streamedGeminiPartSpan
}

// geminiGroundingStreamCandidate retains the protocol state needed to resolve
// grounding metadata emitted after the text it describes. Gemini's streaming
// contract makes grounding chunk indexes cumulative across response chunks and
// keeps segment offsets relative to the accumulated candidate part.
type geminiGroundingStreamCandidate struct {
	rendered strings.Builder
	parts    map[int]*streamedGeminiPart
	chunks   []geminiGroundingChunk
}

// GroundingWebSearchQueries returns the distinct hosted-search queries that
// Gemini reports for a response. The provider may repeat metadata across
// candidates or stream chunks, so callers can safely accumulate this result
// without manufacturing duplicate Responses tool calls.
func GroundingWebSearchQueries(response *dto.GeminiChatResponse) []string {
	if response == nil {
		return nil
	}
	queries := make([]string, 0)
	seen := make(map[string]struct{})
	for candidateIndex := range response.Candidates {
		metadata := response.Candidates[candidateIndex].GroundingMetadata
		if metadata == nil {
			continue
		}
		for _, query := range metadata.WebSearchQueries {
			query = strings.TrimSpace(query)
			if query == "" {
				continue
			}
			if _, exists := seen[query]; exists {
				continue
			}
			seen[query] = struct{}{}
			queries = append(queries, query)
		}
	}
	return queries
}

func groundingAnnotationsToChat(metadata *dto.GeminiGroundingMetadata, content dto.GeminiChatContent, rendered string) []byte {
	if metadata == nil || len(metadata.GroundingChunks) == 0 || len(metadata.GroundingSupports) == 0 {
		return nil
	}
	var chunks []geminiGroundingChunk
	if err := kitutil.Unmarshal(metadata.GroundingChunks, &chunks); err != nil {
		return nil
	}
	var supports []geminiGroundingSupport
	if err := kitutil.Unmarshal(metadata.GroundingSupports, &supports); err != nil {
		return nil
	}

	parts := locateRenderedGeminiParts(content, rendered)
	textPartCount := 0
	soleTextPart := -1
	for index := range parts {
		if parts[index].startByte < 0 {
			continue
		}
		textPartCount++
		soleTextPart = index
	}

	annotations := make([]any, 0)
	seen := make(map[string]struct{})
	for _, support := range supports {
		partIndex := soleTextPart
		if support.Segment.PartIndex != nil {
			partIndex = *support.Segment.PartIndex
		} else if textPartCount != 1 {
			continue
		}
		if partIndex < 0 || partIndex >= len(parts) || parts[partIndex].startByte < 0 {
			continue
		}
		part := parts[partIndex]
		start, end, ok := groundingRuneRange(rendered, part, support.Segment.StartIndex, support.Segment.EndIndex)
		if !ok {
			continue
		}
		if support.Segment.Text != "" && part.text[support.Segment.StartIndex:support.Segment.EndIndex] != support.Segment.Text {
			continue
		}
		annotations = appendGroundingAnnotations(annotations, chunks, support, start, end, "", seen)
	}
	return marshalGroundingAnnotations(annotations)
}

func newGeminiGroundingStreamCandidate() *geminiGroundingStreamCandidate {
	return &geminiGroundingStreamCandidate{parts: make(map[int]*streamedGeminiPart)}
}

func (s *geminiGroundingStreamCandidate) appendContent(content dto.GeminiChatContent, rendered string) {
	if s == nil {
		return
	}
	if s.parts == nil {
		s.parts = make(map[int]*streamedGeminiPart)
	}

	renderedParts := locateRenderedGeminiParts(content, rendered)
	renderedBase := s.rendered.Len()
	for index := range content.Parts {
		partContent := content.Parts[index]
		text := partContent.Text
		if text == "" || partContent.Thought {
			continue
		}
		part := s.parts[index]
		if part == nil {
			part = &streamedGeminiPart{}
			s.parts[index] = part
		}
		partStart := part.text.Len()
		part.text.WriteString(text)

		// A standalone newline is intentionally omitted by the existing Gemini
		// renderer. Keep it in the source part so later byte offsets stay correct,
		// but do not claim that it has a corresponding rendered span.
		if text == "\n" || index >= len(renderedParts) || renderedParts[index].startByte < 0 {
			continue
		}
		renderedStart := renderedBase + renderedParts[index].startByte
		part.spans = append(part.spans, streamedGeminiPartSpan{
			partStartByte:     partStart,
			partEndByte:       partStart + len(text),
			renderedStartByte: renderedStart,
		})
	}
	s.rendered.WriteString(rendered)
}

func (s *geminiGroundingStreamCandidate) appendGroundingChunks(metadata *dto.GeminiGroundingMetadata) {
	if s == nil || metadata == nil || len(metadata.GroundingChunks) == 0 {
		return
	}
	var chunks []geminiGroundingChunk
	if err := kitutil.Unmarshal(metadata.GroundingChunks, &chunks); err != nil {
		return
	}
	s.chunks = append(s.chunks, chunks...)
}

func (s *geminiGroundingStreamCandidate) groundingAnnotations(
	metadata *dto.GeminiGroundingMetadata,
	candidateIndex int64,
	seen map[string]struct{},
) []byte {
	if s == nil || metadata == nil {
		return nil
	}
	s.appendGroundingChunks(metadata)
	if len(s.chunks) == 0 || len(metadata.GroundingSupports) == 0 {
		return nil
	}
	var supports []geminiGroundingSupport
	if err := kitutil.Unmarshal(metadata.GroundingSupports, &supports); err != nil {
		return nil
	}

	annotations := make([]any, 0)
	keyPrefix := fmt.Sprintf("%d:", candidateIndex)
	for _, support := range supports {
		partIndex, ok := s.groundingPartIndex(support)
		if !ok {
			continue
		}
		start, end, ok := s.groundingRuneRange(partIndex, support.Segment.StartIndex, support.Segment.EndIndex)
		if !ok {
			continue
		}
		part := s.parts[partIndex]
		if support.Segment.Text != "" && part.text.String()[support.Segment.StartIndex:support.Segment.EndIndex] != support.Segment.Text {
			continue
		}
		annotations = appendGroundingAnnotations(annotations, s.chunks, support, start, end, keyPrefix, seen)
	}
	return marshalGroundingAnnotations(annotations)
}

func (s *geminiGroundingStreamCandidate) groundingPartIndex(support geminiGroundingSupport) (int, bool) {
	if support.Segment.PartIndex != nil {
		partIndex := *support.Segment.PartIndex
		part := s.parts[partIndex]
		return partIndex, part != nil && len(part.spans) > 0
	}
	solePartIndex := -1
	for partIndex, part := range s.parts {
		if part == nil || len(part.spans) == 0 {
			continue
		}
		if solePartIndex >= 0 {
			return 0, false
		}
		solePartIndex = partIndex
	}
	return solePartIndex, solePartIndex >= 0
}

func (s *geminiGroundingStreamCandidate) groundingRuneRange(partIndex int, startByte int, endByte int) (int, int, bool) {
	if s == nil {
		return 0, 0, false
	}
	part := s.parts[partIndex]
	if part == nil {
		return 0, 0, false
	}
	partText := part.text.String()
	if startByte < 0 || endByte <= startByte || endByte > len(partText) {
		return 0, 0, false
	}
	if !utf8.ValidString(partText[:startByte]) || !utf8.ValidString(partText[:endByte]) {
		return 0, 0, false
	}

	renderedStart, renderedEnd := -1, -1
	for _, span := range part.spans {
		if renderedStart < 0 && startByte >= span.partStartByte && startByte < span.partEndByte {
			renderedStart = span.renderedStartByte + startByte - span.partStartByte
		}
		if endByte > span.partStartByte && endByte <= span.partEndByte {
			renderedEnd = span.renderedStartByte + endByte - span.partStartByte
		}
	}
	if renderedStart < 0 || renderedEnd <= renderedStart {
		return 0, 0, false
	}
	rendered := s.rendered.String()
	if renderedEnd > len(rendered) || rendered[renderedStart:renderedEnd] != partText[startByte:endByte] {
		return 0, 0, false
	}
	if !utf8.ValidString(rendered[:renderedStart]) || !utf8.ValidString(rendered[:renderedEnd]) {
		return 0, 0, false
	}
	return utf8.RuneCountInString(rendered[:renderedStart]), utf8.RuneCountInString(rendered[:renderedEnd]), true
}

func appendGroundingAnnotations(
	annotations []any,
	chunks []geminiGroundingChunk,
	support geminiGroundingSupport,
	start int,
	end int,
	keyPrefix string,
	seen map[string]struct{},
) []any {
	for _, chunkIndex := range support.GroundingChunkIndices {
		if chunkIndex < 0 || chunkIndex >= len(chunks) {
			continue
		}
		source := chunks[chunkIndex].Web
		if source == nil {
			source = chunks[chunkIndex].RetrievedContext
		}
		if source == nil || source.URI == "" {
			continue
		}
		key := fmt.Sprintf("%s%d:%d:%s", keyPrefix, start, end, source.URI)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		annotations = append(annotations, map[string]any{
			"type": "url_citation",
			"url_citation": map[string]any{
				"start_index": start,
				"end_index":   end,
				"url":         source.URI,
				"title":       source.Title,
			},
		})
	}
	return annotations
}

func marshalGroundingAnnotations(annotations []any) []byte {
	if len(annotations) == 0 {
		return nil
	}
	encoded, err := kitutil.Marshal(annotations)
	if err != nil {
		return nil
	}
	return encoded
}

func locateRenderedGeminiParts(content dto.GeminiChatContent, rendered string) []renderedGeminiPart {
	parts := make([]renderedGeminiPart, len(content.Parts))
	cursor := 0
	for index := range content.Parts {
		part := content.Parts[index]
		text := part.Text
		parts[index] = renderedGeminiPart{text: text, startByte: -1}
		if text == "" || part.Thought || cursor > len(rendered) {
			continue
		}
		relative := strings.Index(rendered[cursor:], text)
		if relative < 0 {
			continue
		}
		start := cursor + relative
		parts[index].startByte = start
		cursor = start + len(text)
	}
	return parts
}

func groundingRuneRange(rendered string, part renderedGeminiPart, startByte int, endByte int) (int, int, bool) {
	if startByte < 0 || endByte <= startByte || endByte > len(part.text) {
		return 0, 0, false
	}
	if !utf8.ValidString(part.text[:startByte]) || !utf8.ValidString(part.text[:endByte]) {
		return 0, 0, false
	}
	partStartRunes := utf8.RuneCountInString(rendered[:part.startByte])
	start := partStartRunes + utf8.RuneCountInString(part.text[:startByte])
	end := partStartRunes + utf8.RuneCountInString(part.text[:endByte])
	return start, end, true
}
