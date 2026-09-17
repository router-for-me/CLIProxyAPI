package helps

import (
	"bytes"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	openAIToolResultImageOmittedText = "[image omitted: unsupported by upstream]"
	claudeToolResultImageRelayNotice = "Images returned by the preceding tool call(s):"
	claudeToolResultImagePlaceholder = "[Tool returned image content; the images follow in the next user message.]"
)

// ShouldNormalizeOpenAIToolResultsForModel reports whether the selected model
// explicitly excludes image input through its input-modalities configuration.
func ShouldNormalizeOpenAIToolResultsForModel(compat *config.OpenAICompatibility, upstreamModel, requestedModel string) bool {
	if compat == nil {
		return false
	}

	if normalize, matched := openAICompatibilityModelExcludesImages(compat.Models, upstreamModel); matched {
		return normalize
	}
	normalize, _ := openAICompatibilityModelExcludesImages(compat.Models, requestedModel)
	return normalize
}

// NormalizeOpenAIToolResultsTextOnly converts tool message content to strings
// and strips relayed tool result images for text-only compatibility models.
// Text parts are preserved and image parts are replaced with a short marker.
func NormalizeOpenAIToolResultsTextOnly(payload []byte) []byte {
	messages := gjson.GetBytes(payload, "messages")
	if !messages.Exists() || !messages.IsArray() {
		return payload
	}

	rawList := messages.Array()
	if len(rawList) == 0 {
		return payload
	}

	newMessages := make([][]byte, 0, len(rawList))
	replacedPlaceholderInCurrentTurn := false

	for i := 0; i < len(rawList); i++ {
		msg := rawList[i]
		role := msg.Get("role").String()
		msgBytes := []byte(msg.Raw)

		switch role {
		case "tool":
			content := msg.Get("content")
			if content.Exists() && content.Type != gjson.String {
				if updated, errSet := sjson.SetBytes(msgBytes, "content", flattenOpenAIToolResultContent(content)); errSet == nil {
					msgBytes = updated
				}
			} else if content.Exists() && content.Type == gjson.String && content.String() == claudeToolResultImagePlaceholder {
				if updated, errSet := sjson.SetBytes(msgBytes, "content", openAIToolResultImageOmittedText); errSet == nil {
					msgBytes = updated
					replacedPlaceholderInCurrentTurn = true
				}
			}
			newMessages = append(newMessages, msgBytes)

		case "user":
			content := msg.Get("content")
			if content.IsArray() {
				var remainingParts []string
				hasRelayNotice := false
				hasImages := false

				content.ForEach(func(_, part gjson.Result) bool {
					if part.IsObject() {
						if part.Get("type").String() == "text" && part.Get("text").String() == claudeToolResultImageRelayNotice {
							hasRelayNotice = true
							return true
						}
						if isOpenAIImageToolResultPart(part) {
							hasImages = true
							return true
						}
					}
					remainingParts = append(remainingParts, part.Raw)
					return true
				})

				if hasRelayNotice && hasImages {
					if !replacedPlaceholderInCurrentTurn {
						for j := len(newMessages) - 1; j >= 0; j-- {
							prevRole := gjson.GetBytes(newMessages[j], "role").String()
							if prevRole != "tool" {
								break
							}
							prevContent := gjson.GetBytes(newMessages[j], "content").String()
							if !strings.Contains(prevContent, openAIToolResultImageOmittedText) {
								newContent := prevContent + "\n\n" + openAIToolResultImageOmittedText
								if prevContent == "" {
									newContent = openAIToolResultImageOmittedText
								}
								if updated, errSet := sjson.SetBytes(newMessages[j], "content", newContent); errSet == nil {
									newMessages[j] = updated
								}
							}
							break
						}
					}
					replacedPlaceholderInCurrentTurn = false

					if len(remainingParts) == 0 {
						// Synthetic relay message contained only relayed images; omit entirely.
						continue
					}

					rawArray := "[" + strings.Join(remainingParts, ",") + "]"
					if updated, errSetRaw := sjson.SetRawBytes(msgBytes, "content", []byte(rawArray)); errSetRaw == nil {
						msgBytes = updated
					}
				}
			}
			newMessages = append(newMessages, msgBytes)

		default:
			replacedPlaceholderInCurrentTurn = false
			newMessages = append(newMessages, msgBytes)
		}
	}

	var buf bytes.Buffer
	buf.WriteByte('[')
	for i, m := range newMessages {
		if i > 0 {
			buf.WriteByte(',')
		}
		buf.Write(m)
	}
	buf.WriteByte(']')

	if updated, errSetRaw := sjson.SetRawBytes(payload, "messages", buf.Bytes()); errSetRaw == nil {
		return updated
	}
	return payload
}

func openAICompatibilityModelExcludesImages(models []config.OpenAICompatibilityModel, model string) (bool, bool) {
	model = normalizeOpenAICompatibilityModelName(model)
	if model == "" {
		return false, false
	}

	for i := range models {
		if strings.EqualFold(model, normalizeOpenAICompatibilityModelName(models[i].Name)) {
			return inputModalitiesExcludeImages(models[i].InputModalities), true
		}
	}

	matched := false
	excludesImages := true
	for i := range models {
		if !strings.EqualFold(model, normalizeOpenAICompatibilityModelName(models[i].Alias)) {
			continue
		}
		matched = true
		if !inputModalitiesExcludeImages(models[i].InputModalities) {
			excludesImages = false
		}
	}
	return excludesImages && matched, matched
}

func inputModalitiesExcludeImages(modalities []string) bool {
	if len(modalities) == 0 {
		return false
	}

	hasText := false
	for _, rawModality := range modalities {
		switch strings.ToLower(strings.TrimSpace(rawModality)) {
		case "image":
			return false
		case "text":
			hasText = true
		}
	}
	return hasText
}

func normalizeOpenAICompatibilityModelName(model string) string {
	model = strings.TrimSpace(model)
	if model == "" {
		return ""
	}
	return strings.TrimSpace(thinking.ParseSuffix(model).ModelName)
}

func flattenOpenAIToolResultContent(content gjson.Result) string {
	if content.Type == gjson.String {
		return content.String()
	}

	if content.IsArray() {
		parts := make([]string, 0, 4)
		content.ForEach(func(_, item gjson.Result) bool {
			if part, ok := openAIToolResultPartText(item); ok {
				parts = append(parts, part)
			}
			return true
		})
		return strings.Join(parts, "\n\n")
	}

	if content.IsObject() {
		if isOpenAIImageToolResultPart(content) {
			return openAIToolResultImageOmittedText
		}
		if text := content.Get("text"); text.Type == gjson.String {
			return text.String()
		}
	}

	return content.Raw
}

func openAIToolResultPartText(item gjson.Result) (string, bool) {
	if item.Type == gjson.String {
		return item.String(), true
	}
	if item.IsObject() {
		if isOpenAIImageToolResultPart(item) {
			return openAIToolResultImageOmittedText, true
		}
		if text := item.Get("text"); text.Type == gjson.String {
			return text.String(), true
		}
	}
	if item.Raw == "" {
		return "", false
	}
	return item.Raw, true
}

func isOpenAIImageToolResultPart(item gjson.Result) bool {
	if !item.IsObject() {
		return false
	}

	switch strings.ToLower(strings.TrimSpace(item.Get("type").String())) {
	case "image", "image_url", "input_image":
		return true
	}
	return item.Get("image_url").Exists() || item.Get("input_image").Exists()
}
