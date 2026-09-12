package openai

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func (h *OpenAIResponsesAPIHandler) codexImageOutputDir() string {
	if h == nil || h.Cfg == nil {
		return ""
	}
	return strings.TrimSpace(h.Cfg.CodexImageOutputDir)
}

func resolveCodexImageOutputDir(configured string) (string, error) {
	dir := os.ExpandEnv(strings.TrimSpace(configured))
	if dir == "" {
		return "", nil
	}
	if dir == "~" || strings.HasPrefix(dir, "~/") {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		if dir == "~" {
			dir = homeDir
		} else {
			dir = filepath.Join(homeDir, strings.TrimPrefix(dir, "~/"))
		}
	}
	if !filepath.IsAbs(dir) {
		absoluteDir, err := filepath.Abs(dir)
		if err != nil {
			return "", err
		}
		dir = absoluteDir
	}
	return filepath.Clean(dir), nil
}

func decodeResponsesImageResult(result string) ([]byte, string, bool) {
	encoded := strings.TrimSpace(result)
	if strings.HasPrefix(encoded, "data:") {
		comma := strings.IndexByte(encoded, ',')
		if comma < 0 || !strings.Contains(strings.ToLower(encoded[:comma]), ";base64") {
			return nil, "", false
		}
		encoded = encoded[comma+1:]
	}

	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		decoded, err = base64.RawStdEncoding.DecodeString(encoded)
	}
	if err != nil || len(decoded) == 0 {
		return nil, "", false
	}

	switch {
	case bytes.HasPrefix(decoded, []byte("\x89PNG\r\n\x1a\n")):
		return decoded, "png", true
	case len(decoded) >= 3 && decoded[0] == 0xff && decoded[1] == 0xd8 && decoded[2] == 0xff:
		return decoded, "jpg", true
	case len(decoded) >= 12 && string(decoded[:4]) == "RIFF" && string(decoded[8:12]) == "WEBP":
		return decoded, "webp", true
	default:
		return nil, "", false
	}
}

func safeImageOutputID(value string, fallbackIndex int) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fmt.Sprintf("image-%d", fallbackIndex)
	}

	var out strings.Builder
	out.Grow(min(len(value), 160))
	for _, r := range value {
		if out.Len() >= 160 {
			break
		}
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			out.WriteRune(r)
		default:
			out.WriteByte('_')
		}
	}
	if out.Len() == 0 {
		return fmt.Sprintf("image-%d", fallbackIndex)
	}
	return out.String()
}

func (f *responsesSSEFramer) resolvedCodexImageOutputDir() (string, error) {
	if f.imageDirResolved {
		return f.resolvedImageDir, nil
	}
	resolved, err := resolveCodexImageOutputDir(f.imageOutputDir)
	if err != nil {
		return "", err
	}
	f.imageDirResolved = true
	f.resolvedImageDir = resolved
	return resolved, nil
}

func (f *responsesSSEFramer) persistImageGenerationResult(payload []byte) {
	if strings.TrimSpace(f.imageOutputDir) == "" {
		return
	}
	item := gjson.GetBytes(payload, "item")
	if item.Get("type").String() != "image_generation_call" {
		return
	}
	result := item.Get("result").String()
	if strings.TrimSpace(result) == "" {
		return
	}

	itemID := strings.TrimSpace(item.Get("id").String())
	key := itemID
	if key == "" {
		key = imageGenerationItemKey(payload, "item.id")
	}
	if f.savedImagePaths != nil {
		if _, exists := f.savedImagePaths[key]; exists {
			return
		}
	}

	imageData, extension, ok := decodeResponsesImageResult(result)
	if !ok {
		log.Warn("responses image output: unsupported or invalid image result")
		return
	}
	dir, err := f.resolvedCodexImageOutputDir()
	if err != nil || dir == "" {
		if err != nil {
			log.WithError(err).Warn("responses image output: resolve output directory")
		}
		return
	}
	if err = os.MkdirAll(dir, 0o700); err != nil {
		log.WithError(err).Warn("responses image output: create output directory")
		return
	}

	filename := safeImageOutputID(itemID, len(f.savedImageOrder)+1) + "." + extension
	outputPath := filepath.Join(dir, filename)
	if err = os.WriteFile(outputPath, imageData, 0o600); err != nil {
		log.WithError(err).Warn("responses image output: write generated image")
		return
	}

	if f.savedImagePaths == nil {
		f.savedImagePaths = make(map[string]string)
	}
	f.savedImagePaths[key] = outputPath
	f.savedImageOrder = append(f.savedImageOrder, key)
}

func (f *responsesSSEFramer) imageMarkdown() string {
	if len(f.savedImageOrder) == 0 {
		return ""
	}
	var out strings.Builder
	for index, key := range f.savedImageOrder {
		imagePath := f.savedImagePaths[key]
		if imagePath == "" {
			continue
		}
		out.WriteString("\n\n![Generated image ")
		out.WriteString(fmt.Sprintf("%d", index+1))
		out.WriteString("](")
		out.WriteString(imagePath)
		out.WriteByte(')')
	}
	return out.String()
}

func appendImageMarkdownAtPath(payload []byte, path, markdown string) []byte {
	current := gjson.GetBytes(payload, path)
	if current.Type != gjson.String || strings.Contains(current.String(), strings.TrimSpace(markdown)) {
		return payload
	}
	updated, err := sjson.SetBytes(payload, path, current.String()+markdown)
	if err != nil {
		return payload
	}
	return updated
}

func appendImageMarkdownToContent(payload []byte, contentPath, markdown string) []byte {
	content := gjson.GetBytes(payload, contentPath)
	if !content.IsArray() {
		return payload
	}
	updated := payload
	for index, part := range content.Array() {
		if part.Get("type").String() != "output_text" {
			continue
		}
		updated = appendImageMarkdownAtPath(updated, fmt.Sprintf("%s.%d.text", contentPath, index), markdown)
	}
	return updated
}

func appendImageMarkdownToResponseOutput(payload []byte, outputPath, markdown string) []byte {
	output := gjson.GetBytes(payload, outputPath)
	if !output.IsArray() {
		return payload
	}
	updated := payload
	for index, item := range output.Array() {
		if item.Get("type").String() != "message" {
			continue
		}
		updated = appendImageMarkdownToContent(updated, fmt.Sprintf("%s.%d.content", outputPath, index), markdown)
	}
	return updated
}

func (f *responsesSSEFramer) injectImageMarkdown(payload []byte) []byte {
	markdown := f.imageMarkdown()
	if markdown == "" {
		return payload
	}

	switch gjson.GetBytes(payload, "type").String() {
	case "response.output_text.delta":
		if f.imageMarkdownInjected {
			return payload
		}
		updated := appendImageMarkdownAtPath(payload, "delta", markdown)
		if !bytes.Equal(updated, payload) {
			f.imageMarkdownInjected = true
		}
		return updated
	case "response.output_text.done":
		return appendImageMarkdownAtPath(payload, "text", markdown)
	case "response.content_part.added", "response.content_part.done":
		if gjson.GetBytes(payload, "part.type").String() == "output_text" {
			return appendImageMarkdownAtPath(payload, "part.text", markdown)
		}
	case "response.output_item.done":
		if gjson.GetBytes(payload, "item.type").String() == "message" {
			return appendImageMarkdownToContent(payload, "item.content", markdown)
		}
	case "response.completed":
		return appendImageMarkdownToResponseOutput(payload, "response.output", markdown)
	}
	return payload
}
