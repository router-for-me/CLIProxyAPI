package openai

import (
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

var chatMarkdownURLCitationPattern = regexp.MustCompile(`\[(\[?[^\]\r\n]+\]?)\]\((https?://[^)\s]+)\)`)

func normalizeChatWebSearchToolType(toolType string) string {
	switch strings.ToLower(strings.TrimSpace(toolType)) {
	case "web_search", "web_search_2025_08_26", "web_search_preview", "web_search_preview_2025_03_11":
		return "web_search"
	default:
		return ""
	}
}

func addChatWebSearchAnnotations(requestRawJSON, responseRawJSON []byte) []byte {
	if !chatRequestUsesWebSearch(requestRawJSON) {
		return responseRawJSON
	}

	choices := gjson.GetBytes(responseRawJSON, "choices")
	if !choices.IsArray() {
		return responseRawJSON
	}
	for i, choice := range choices.Array() {
		message := choice.Get("message")
		if !message.IsObject() || message.Get("annotations").Exists() {
			continue
		}
		content := message.Get("content")
		if content.Type != gjson.String {
			continue
		}
		annotations := chatMarkdownURLCitations(content.String())
		if len(annotations) == 0 {
			continue
		}
		path := "choices." + strconv.Itoa(i) + ".message.annotations"
		responseRawJSON, _ = sjson.SetRawBytes(responseRawJSON, path, []byte(`[]`))
		for _, annotation := range annotations {
			responseRawJSON, _ = sjson.SetRawBytes(responseRawJSON, path+".-1", annotation)
		}
	}
	return responseRawJSON
}

func chatRequestUsesWebSearch(rawJSON []byte) bool {
	if gjson.GetBytes(rawJSON, "web_search_options").IsObject() {
		return true
	}
	for _, tool := range gjson.GetBytes(rawJSON, "tools").Array() {
		if normalizeChatWebSearchToolType(tool.Get("type").String()) != "" {
			return true
		}
	}
	return false
}

func chatMarkdownURLCitations(content string) [][]byte {
	matches := chatMarkdownURLCitationPattern.FindAllStringSubmatchIndex(content, -1)
	annotations := make([][]byte, 0, len(matches))
	for _, match := range matches {
		if len(match) < 6 {
			continue
		}
		title := strings.Trim(strings.TrimSpace(content[match[2]:match[3]]), "[]")
		url := content[match[4]:match[5]]
		if title == "" {
			title = url
		}
		annotation := []byte(`{"type":"url_citation","url_citation":{"url":"","title":"","start_index":0,"end_index":0}}`)
		annotation, _ = sjson.SetBytes(annotation, "url_citation.url", url)
		annotation, _ = sjson.SetBytes(annotation, "url_citation.title", title)
		annotation, _ = sjson.SetBytes(annotation, "url_citation.start_index", utf8.RuneCountInString(content[:match[0]]))
		annotation, _ = sjson.SetBytes(annotation, "url_citation.end_index", utf8.RuneCountInString(content[:match[1]]))
		annotations = append(annotations, annotation)
	}
	return annotations
}
