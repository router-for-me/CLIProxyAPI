package helps

import (
	"fmt"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const openAIToolResultImageOmittedText = "[image omitted: unsupported by upstream]"

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

// NormalizeOpenAIToolResultsTextOnly converts tool message content to strings.
// Text parts are preserved and image parts are replaced with a short marker.
func NormalizeOpenAIToolResultsTextOnly(payload []byte) []byte {
	messages := gjson.GetBytes(payload, "messages")
	if !messages.Exists() || !messages.IsArray() {
		return payload
	}

	out := payload
	messageIndex := 0
	messages.ForEach(func(_, message gjson.Result) bool {
		if message.Get("role").String() == "tool" {
			content := message.Get("content")
			if content.Exists() && content.Type != gjson.String {
				path := fmt.Sprintf("messages.%d.content", messageIndex)
				if updated, errSet := sjson.SetBytes(out, path, flattenOpenAIToolResultContent(content)); errSet == nil {
					out = updated
				}
			}
		}
		messageIndex++
		return true
	})
	return out
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
