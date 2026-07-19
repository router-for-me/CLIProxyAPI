package common

import (
	"path/filepath"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/misc"
)

// NormalizeOpenAIFileData returns the MIME type and raw base64 payload for OpenAI file content.
func NormalizeOpenAIFileData(filename, fallbackMIMEType, fileData string) (mimeType, data string, ok bool) {
	if fileData == "" {
		return "", "", false
	}

	if fallbackMIMEType == "" {
		ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(filename), "."))
		fallbackMIMEType = misc.MimeTypes[ext]
	}
	const dataURLPrefix = "data:"
	if len(fileData) < len(dataURLPrefix) || !strings.EqualFold(fileData[:len(dataURLPrefix)], dataURLPrefix) {
		if fallbackMIMEType == "" {
			return "", "", false
		}
		return fallbackMIMEType, fileData, true
	}

	metadata, payload, found := strings.Cut(fileData[len(dataURLPrefix):], ",")
	if !found || payload == "" {
		return "", "", false
	}
	fields := strings.Split(metadata, ";")
	mimeType = strings.TrimSpace(fields[0])
	if mimeType == "" {
		return "", "", false
	}
	for _, field := range fields[1:] {
		if strings.EqualFold(strings.TrimSpace(field), "base64") {
			return mimeType, payload, true
		}
	}
	return "", "", false
}
