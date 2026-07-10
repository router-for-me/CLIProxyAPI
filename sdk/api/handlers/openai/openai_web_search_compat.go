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

func normalizeChatWebSearchRequest(rawJSON []byte) []byte {
	tools := gjson.GetBytes(rawJSON, "tools")
	hasWebSearchTool := false
	if tools.IsArray() {
		for i, tool := range tools.Array() {
			normalizedType := normalizeChatWebSearchToolType(tool.Get("type").String())
			if normalizedType == "" {
				continue
			}
			hasWebSearchTool = true
			rawJSON, _ = sjson.SetBytes(rawJSON, "tools."+strconv.Itoa(i)+".type", normalizedType)
		}
	}

	synthesizedWebSearchTool := false
	if webSearchOptions := gjson.GetBytes(rawJSON, "web_search_options"); webSearchOptions.IsObject() && !hasWebSearchTool {
		if !gjson.GetBytes(rawJSON, "tools").IsArray() {
			rawJSON, _ = sjson.SetRawBytes(rawJSON, "tools", []byte(`[]`))
		}
		webSearchTool := []byte(`{"type":"web_search"}`)
		if contextSize := webSearchOptions.Get("search_context_size"); contextSize.Exists() {
			webSearchTool, _ = sjson.SetBytes(webSearchTool, "search_context_size", contextSize.Value())
		}
		if userLocation := webSearchOptions.Get("user_location"); userLocation.IsObject() {
			responsesLocation := userLocation
			if approximate := userLocation.Get("approximate"); approximate.IsObject() {
				responsesLocation = approximate
			}
			location := []byte(responsesLocation.Raw)
			location, _ = sjson.SetBytes(location, "type", "approximate")
			webSearchTool, _ = sjson.SetRawBytes(webSearchTool, "user_location", location)
		}
		rawJSON, _ = sjson.SetRawBytes(rawJSON, "tools.-1", webSearchTool)
		synthesizedWebSearchTool = true
	}

	toolChoice := gjson.GetBytes(rawJSON, "tool_choice")
	if toolChoice.IsObject() {
		if normalizedType := normalizeChatWebSearchToolType(toolChoice.Get("type").String()); normalizedType != "" {
			rawJSON, _ = sjson.SetBytes(rawJSON, "tool_choice.type", normalizedType)
		}
	} else if synthesizedWebSearchTool && !toolChoice.Exists() && len(gjson.GetBytes(rawJSON, "tools").Array()) == 1 {
		rawJSON, _ = sjson.SetBytes(rawJSON, "tool_choice", "required")
	}

	return rawJSON
}

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
