package helps

import (
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	openAIToolResultImageRelayText   = "[Tool returned image content; the images follow in the next user message.]"
	openAIToolResultImageRelayNotice = "Images returned by the preceding tool call(s):"
)

// RelayOpenAIToolResultImages keeps images out of Chat Completions tool
// messages. Some OpenAI-compatible upstreams reject or drop multimodal tool
// content even when they support image input in user messages.
func RelayOpenAIToolResultImages(payload []byte) []byte {
	messages := gjson.GetBytes(payload, "messages")
	if !messages.IsArray() {
		return payload
	}

	messageResults := messages.Array()
	if !hasRelayableOpenAIToolResultImages(messageResults) {
		return payload
	}

	out := make([][]byte, 0, len(messageResults)+1)
	pendingImages := make([][]byte, 0, 2)

	flushImages := func() {
		if len(pendingImages) == 0 {
			return
		}
		out = append(out, prependOpenAIToolResultImages([]byte(`{"role":"user","content":[]}`), pendingImages))
		pendingImages = pendingImages[:0]
	}

	for _, message := range messageResults {
		messageJSON := []byte(message.Raw)
		role := strings.ToLower(strings.TrimSpace(message.Get("role").String()))
		if role == "tool" {
			if text, images, ok := splitOpenAIToolResultImages(message.Get("content")); ok {
				messageJSON, _ = sjson.SetBytes(messageJSON, "content", text)
				pendingImages = append(pendingImages, images...)
			}
			out = append(out, messageJSON)
			continue
		}

		if len(pendingImages) > 0 {
			if role == "user" {
				messageJSON = prependOpenAIToolResultImages(messageJSON, pendingImages)
				pendingImages = pendingImages[:0]
			} else {
				flushImages()
			}
		}
		out = append(out, messageJSON)
	}
	flushImages()

	updated, err := sjson.SetRawBytes(payload, "messages", joinOpenAIRawJSON(out))
	if err != nil {
		return payload
	}
	return updated
}

func hasRelayableOpenAIToolResultImages(messages []gjson.Result) bool {
	for _, message := range messages {
		if !strings.EqualFold(strings.TrimSpace(message.Get("role").String()), "tool") {
			continue
		}
		content := message.Get("content")
		if content.IsArray() {
			found := false
			content.ForEach(func(_, part gjson.Result) bool {
				if canRelayOpenAIToolResultImagePart(part) {
					found = true
					return false
				}
				return true
			})
			if found {
				return true
			}
			continue
		}
		if content.IsObject() && canRelayOpenAIToolResultImagePart(content) {
			return true
		}
	}
	return false
}

func splitOpenAIToolResultImages(content gjson.Result) (string, [][]byte, bool) {
	if content.IsArray() {
		textParts := make([]string, 0, 4)
		images := make([][]byte, 0, 2)
		hasImage := false
		content.ForEach(func(_, part gjson.Result) bool {
			if image, ok := openAIToolResultImagePart(part); ok {
				hasImage = true
				images = append(images, image)
				return true
			}
			if isOpenAIImageToolResultPart(part) {
				if part.Raw != "" {
					textParts = append(textParts, part.Raw)
				} else {
					textParts = append(textParts, openAIToolResultImageOmittedText)
				}
				return true
			}
			if text, ok := openAIToolResultPartText(part); ok {
				textParts = append(textParts, text)
			}
			return true
		})
		if !hasImage {
			return "", nil, false
		}
		text := strings.Join(textParts, "\n\n")
		if strings.TrimSpace(text) == "" {
			text = openAIToolResultImageRelayText
		}
		return text, images, true
	}

	if content.IsObject() {
		if image, ok := openAIToolResultImagePart(content); ok {
			return openAIToolResultImageRelayText, [][]byte{image}, true
		}
	}
	return "", nil, false
}

func canRelayOpenAIToolResultImagePart(part gjson.Result) bool {
	if !part.IsObject() {
		return false
	}

	typeName := strings.ToLower(strings.TrimSpace(part.Get("type").String()))
	switch typeName {
	case "image_url":
		return openAICompatImageURLValue(part.Get("image_url")) != ""
	case "input_image":
		return openAICompatInputImageURL(part) != ""
	case "image":
		return openAICompatClaudeImageSourceURL(part.Get("source")) != ""
	}

	if part.Get("image_url").Exists() {
		return openAICompatImageURLValue(part.Get("image_url")) != ""
	}
	if part.Get("input_image").Exists() {
		return openAICompatImageURLValue(part.Get("input_image")) != ""
	}
	return false
}

func openAIToolResultImagePart(part gjson.Result) ([]byte, bool) {
	if !part.IsObject() {
		return nil, false
	}

	var imageURL, detail string
	typeName := strings.ToLower(strings.TrimSpace(part.Get("type").String()))
	switch typeName {
	case "image_url":
		imageURL = openAICompatImageURLValue(part.Get("image_url"))
		detail = normalizeOpenAICompatImageDetail(part.Get("image_url.detail").String())
	case "input_image":
		imageURL = openAICompatInputImageURL(part)
		detail = normalizeOpenAICompatImageDetail(part.Get("detail").String())
	case "image":
		imageURL = openAICompatClaudeImageSourceURL(part.Get("source"))
	default:
		if part.Get("image_url").Exists() {
			imageURL = openAICompatImageURLValue(part.Get("image_url"))
			detail = normalizeOpenAICompatImageDetail(part.Get("image_url.detail").String())
		} else if part.Get("input_image").Exists() {
			imageURL = openAICompatImageURLValue(part.Get("input_image"))
		}
	}
	if imageURL == "" {
		return nil, false
	}

	image := []byte(`{"type":"image_url","image_url":{"url":""}}`)
	image, _ = sjson.SetBytes(image, "image_url.url", imageURL)
	if detail != "" {
		image, _ = sjson.SetBytes(image, "image_url.detail", detail)
	}
	return image, true
}

func openAICompatImageURLValue(value gjson.Result) string {
	if value.Type == gjson.String {
		return strings.TrimSpace(value.String())
	}
	if value.IsObject() {
		if url := value.Get("url"); url.Type == gjson.String {
			return strings.TrimSpace(url.String())
		}
		if imageURL := value.Get("image_url"); imageURL.Type == gjson.String {
			return strings.TrimSpace(imageURL.String())
		}
	}
	return ""
}

func openAICompatInputImageURL(part gjson.Result) string {
	if imageURL := openAICompatImageURLValue(part.Get("image_url")); imageURL != "" {
		return imageURL
	}
	return openAICompatImageURLValue(part.Get("input_image"))
}

func openAICompatClaudeImageSourceURL(source gjson.Result) string {
	if !source.IsObject() {
		return ""
	}
	switch strings.ToLower(strings.TrimSpace(source.Get("type").String())) {
	case "base64":
		mediaType := strings.TrimSpace(source.Get("media_type").String())
		data := strings.TrimSpace(source.Get("data").String())
		if mediaType == "" || data == "" {
			return ""
		}
		return "data:" + mediaType + ";base64," + data
	case "url":
		return strings.TrimSpace(source.Get("url").String())
	default:
		return ""
	}
}

func normalizeOpenAICompatImageDetail(detail string) string {
	switch strings.ToLower(strings.TrimSpace(detail)) {
	case "auto", "low", "high":
		return strings.ToLower(strings.TrimSpace(detail))
	case "original":
		return "high"
	default:
		return ""
	}
}

func prependOpenAIToolResultImages(message []byte, images [][]byte) []byte {
	parts := make([][]byte, 0, len(images)+2)
	notice := []byte(`{"type":"text","text":""}`)
	notice, _ = sjson.SetBytes(notice, "text", openAIToolResultImageRelayNotice)
	parts = append(parts, notice)
	parts = append(parts, images...)

	content := gjson.GetBytes(message, "content")
	switch {
	case content.IsArray():
		content.ForEach(func(_, part gjson.Result) bool {
			parts = append(parts, []byte(part.Raw))
			return true
		})
	case content.Type == gjson.String && content.String() != "":
		text := []byte(`{"type":"text","text":""}`)
		text, _ = sjson.SetBytes(text, "text", content.String())
		parts = append(parts, text)
	}

	updated, err := sjson.SetRawBytes(message, "content", joinOpenAIRawJSON(parts))
	if err != nil {
		return message
	}
	return updated
}

func joinOpenAIRawJSON(items [][]byte) []byte {
	var b strings.Builder
	b.WriteByte('[')
	for i, item := range items {
		if i > 0 {
			b.WriteByte(',')
		}
		b.Write(item)
	}
	b.WriteByte(']')
	return []byte(b.String())
}
