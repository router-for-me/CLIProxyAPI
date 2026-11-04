package helps

import (
	"fmt"
	"strings"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
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
	if len(payload) == 0 {
		return payload
	}
	doc := ojson.NewDoc(payload)
	if !NormalizeOpenAIToolResultsTextOnlyDoc(doc) {
		return payload
	}
	return doc.Bytes()
}

// NormalizeOpenAIToolResultsTextOnlyDoc converts tool message content to strings
// and strips relayed tool result images in place on doc for text-only compatibility models.
func NormalizeOpenAIToolResultsTextOnlyDoc(doc *ojson.Doc) bool {
	if doc == nil || doc.Len() == 0 {
		return false
	}
	messages := doc.Get("messages")
	if !messages.Exists() || !messages.IsArray() {
		return false
	}

	rawList := messages.Array()
	if len(rawList) == 0 {
		return false
	}

	modified := false
	replacedPlaceholderInCurrentTurn := false
	omitted := make(map[int]bool)

	for i := 0; i < len(rawList); i++ {
		msg := rawList[i]
		role := msg.Get("role").String()

		switch role {
		case "tool":
			content := msg.Get("content")
			if content.Exists() && content.Type != ojson.String {
				if errSet := doc.Set(fmt.Sprintf("messages.%d.content", i), flattenOpenAIToolResultContent(content)); errSet == nil {
					modified = true
				}
			} else if content.Exists() && content.Type == ojson.String && content.String() == claudeToolResultImagePlaceholder {
				if errSet := doc.Set(fmt.Sprintf("messages.%d.content", i), openAIToolResultImageOmittedText); errSet == nil {
					modified = true
					replacedPlaceholderInCurrentTurn = true
				}
			}

		case "user":
			content := msg.Get("content")
			if content.IsArray() {
				var remainingParts []string
				hasRelayNotice := false
				hasImages := false

				content.ForEach(func(_, part ojson.Result) bool {
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
						for j := i - 1; j >= 0; j-- {
							if omitted[j] {
								continue
							}
							prevRole := doc.Get(fmt.Sprintf("messages.%d.role", j)).String()
							if prevRole != "tool" {
								break
							}
							prevContent := doc.Get(fmt.Sprintf("messages.%d.content", j)).String()
							if !strings.Contains(prevContent, openAIToolResultImageOmittedText) {
								newContent := prevContent + "\n\n" + openAIToolResultImageOmittedText
								if prevContent == "" {
									newContent = openAIToolResultImageOmittedText
								}
								if errSet := doc.Set(fmt.Sprintf("messages.%d.content", j), newContent); errSet == nil {
									modified = true
								}
							}
							break
						}
					}
					replacedPlaceholderInCurrentTurn = false

					if len(remainingParts) == 0 {
						// Synthetic relay message contained only relayed images; omit entirely.
						omitted[i] = true
						modified = true
						continue
					}

					rawArray := "[" + strings.Join(remainingParts, ",") + "]"
					if errSetRaw := doc.SetRaw(fmt.Sprintf("messages.%d.content", i), rawArray); errSetRaw == nil {
						modified = true
					}
				}
			}

		default:
			replacedPlaceholderInCurrentTurn = false
		}
	}

	if len(omitted) > 0 {
		updatedList := doc.Get("messages").Array()
		kept := make([][]byte, 0, len(updatedList)-len(omitted))
		for idx, item := range updatedList {
			if !omitted[idx] {
				kept = append(kept, []byte(item.Raw))
			}
		}
		if errSetRaw := doc.SetRawBytes("messages", JoinRawJSONArray(kept)); errSetRaw != nil {
			return false
		}
	}
	return modified
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

func flattenOpenAIToolResultContent(content ojson.Result) string {
	if content.Type == ojson.String {
		return content.String()
	}

	if content.IsArray() {
		parts := make([]string, 0, 4)
		content.ForEach(func(_, item ojson.Result) bool {
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
		if text := content.Get("text"); text.Type == ojson.String {
			return text.String()
		}
	}

	return content.Raw
}

func openAIToolResultPartText(item ojson.Result) (string, bool) {
	if item.Type == ojson.String {
		return item.String(), true
	}
	if item.IsObject() {
		if isOpenAIImageToolResultPart(item) {
			return openAIToolResultImageOmittedText, true
		}
		if text := item.Get("text"); text.Type == ojson.String {
			return text.String(), true
		}
	}
	if item.Raw == "" {
		return "", false
	}
	return item.Raw, true
}

func isOpenAIImageToolResultPart(item ojson.Result) bool {
	if !item.IsObject() {
		return false
	}

	switch strings.ToLower(strings.TrimSpace(item.Get("type").String())) {
	case "image", "image_url", "input_image":
		return true
	}
	return item.Get("image_url").Exists() || item.Get("input_image").Exists()
}
