package util

import "strings"

// XAIImageAspectRatioFromSize maps OpenAI-compatible image size strings and
// xAI aspect-ratio aliases to xAI's aspect_ratio values.
func XAIImageAspectRatioFromSize(size string, fallback string) string {
	size = strings.ToLower(strings.TrimSpace(size))
	switch size {
	case "256x256", "512x512", "1024x1024", "2048x2048", "1:1":
		return "1:1"
	case "1792x1024", "16:9":
		return "16:9"
	case "1024x1792", "9:16":
		return "9:16"
	case "1536x1024", "3:2":
		return "3:2"
	case "1024x1536", "2:3":
		return "2:3"
	default:
		return fallback
	}
}

// XAIImageResolution maps explicit xAI resolution values or infers resolution
// from OpenAI-compatible size strings.
func XAIImageResolution(raw string, size string, fallback string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1k", "2k":
		return strings.ToLower(strings.TrimSpace(raw))
	}
	if strings.Contains(strings.ToLower(strings.TrimSpace(size)), "2048") {
		return "2k"
	}
	return fallback
}

// XAIImageSizeMapping maps a size string into xAI aspect_ratio/resolution.
func XAIImageSizeMapping(size string) (aspectRatio, resolution string, ok bool) {
	aspectRatio = XAIImageAspectRatioFromSize(size, "")
	if aspectRatio == "" {
		return "", "", false
	}
	return aspectRatio, XAIImageResolution("", size, "1k"), true
}
