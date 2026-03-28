package openai

import (
	"fmt"
	"net/http"
	"strings"

	cliproxyfiles "github.com/router-for-me/CLIProxyAPI/v6/internal/files"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/interfaces"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func preprocessResponsesInputFiles(rawJSON []byte, store *cliproxyfiles.Store) ([]byte, *interfaces.ErrorMessage) {
	if len(rawJSON) == 0 || store == nil {
		return rawJSON, nil
	}
	input := gjson.GetBytes(rawJSON, "input")
	if !input.Exists() || !input.IsArray() {
		return rawJSON, nil
	}

	updated := rawJSON
	inputArray := input.Array()
	for i, item := range inputArray {
		content := item.Get("content")
		if !content.Exists() || !content.IsArray() {
			continue
		}
		for j, part := range content.Array() {
			if strings.TrimSpace(part.Get("type").String()) != "input_file" {
				continue
			}
			fileID := strings.TrimSpace(part.Get("file_id").String())
			if fileID == "" {
				continue
			}
			meta, data, err := store.Load(fileID)
			if err != nil {
				status := http.StatusBadRequest
				if err == cliproxyfiles.ErrNotFound {
					status = http.StatusNotFound
				}
				return rawJSON, &interfaces.ErrorMessage{StatusCode: status, Error: fmt.Errorf("resolve input_file %s: %w", fileID, err)}
			}
			text, err := cliproxyfiles.ExtractText(meta, data)
			if err != nil {
				return rawJSON, &interfaces.ErrorMessage{StatusCode: http.StatusBadRequest, Error: fmt.Errorf("extract input_file %s (%s): %w", fileID, meta.Filename, err)}
			}
			replacement := map[string]any{
				"type": "input_text",
				"text": buildResolvedFilePrompt(meta, text),
			}
			path := fmt.Sprintf("input.%d.content.%d", i, j)
			var setErr error
			updated, setErr = sjson.SetBytes(updated, path, replacement)
			if setErr != nil {
				return rawJSON, &interfaces.ErrorMessage{StatusCode: http.StatusInternalServerError, Error: fmt.Errorf("rewrite input_file %s: %w", fileID, setErr)}
			}
		}
	}
	return updated, nil
}

func buildResolvedFilePrompt(meta *cliproxyfiles.Metadata, text string) string {
	text = strings.TrimSpace(text)
	if meta == nil {
		return text
	}
	return fmt.Sprintf("[Uploaded file: %s (%s)]\n%s", meta.Filename, meta.ID, text)
}
