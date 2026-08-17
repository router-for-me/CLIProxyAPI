package promptcompat

import "strings"

func CollectOpenAIRefFileIDs(req map[string]any) []string {
	if len(req) == 0 {
		return nil
	}
	out := make([]string, 0, 4)
	seen := map[string]struct{}{}
	for _, key := range []string{
		"ref_file_ids",
		"file_ids",
		"attachments",
		"messages",
		"input",
	} {
		raw := req[key]
		if raw == nil {
			continue
		}
		// Skip top-level strings for 'messages' and 'input' as they are likely plain text content,
		// not file IDs. String file IDs are expected in 'ref_file_ids' or 'file_ids'.
		if key == "messages" || key == "input" {
			if _, ok := raw.(string); ok {
				continue
			}
		}
		appendOpenAIRefFileIDs(&out, seen, raw)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func appendOpenAIRefFileIDs(out *[]string, seen map[string]struct{}, raw any) {
	switch x := raw.(type) {
	case string:
		addOpenAIRefFileID(out, seen, x)
	case []string:
		for _, item := range x {
			addOpenAIRefFileID(out, seen, item)
		}
	case []any:
		for _, item := range x {
			appendOpenAIRefFileIDs(out, seen, item)
		}
	case map[string]any:
		if fileID := strings.TrimSpace(asString(x["file_id"])); fileID != "" {
			addOpenAIRefFileID(out, seen, fileID)
		}
		if strings.Contains(strings.ToLower(strings.TrimSpace(asString(x["type"]))), "file") {
			if fileID := strings.TrimSpace(asString(x["id"])); fileID != "" {
				addOpenAIRefFileID(out, seen, fileID)
			}
		}
		if fileMap, ok := x["file"].(map[string]any); ok {
			if fileID := strings.TrimSpace(asString(fileMap["file_id"])); fileID != "" {
				addOpenAIRefFileID(out, seen, fileID)
			}
			if fileID := strings.TrimSpace(asString(fileMap["id"])); fileID != "" {
				addOpenAIRefFileID(out, seen, fileID)
			}
		}
		// Recurse into potential containers. Note: we do NOT recurse into 'content' or 'input'
		// if they are plain strings (handled by the top-level switch), but they are usually
		// nested inside the map branch anyway.
		// To be safe, we only recurse into these known container keys.
		for _, key := range []string{"ref_file_ids", "file_ids", "attachments", "messages", "input", "content", "files", "items", "data", "source"} {
			if nested, ok := x[key]; ok {
				// If it's a message content that is a string, we must NOT treat it as an ID.
				if key == "content" || key == "input" {
					if _, ok := nested.(string); ok {
						continue
					}
				}
				appendOpenAIRefFileIDs(out, seen, nested)
			}
		}
	}
}

func addOpenAIRefFileID(out *[]string, seen map[string]struct{}, fileID string) {
	fileID = strings.TrimSpace(fileID)
	if fileID == "" {
		return
	}
	if _, ok := seen[fileID]; ok {
		return
	}
	seen[fileID] = struct{}{}
	*out = append(*out, fileID)
}
