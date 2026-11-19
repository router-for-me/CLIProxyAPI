package helps

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"

	"github.com/MoeForge/moego/ojson"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// Native Gemini embedding actions. They are served by the Gemini and AI Studio
// /v1beta models endpoints and must bypass all chat request normalization.
const (
	GeminiEmbedContentAction       = "embedContent"
	GeminiBatchEmbedContentsAction = "batchEmbedContents"
)

// GeminiEmbeddingAction returns the embedding action carried by request metadata.
// It returns an empty string when the request is not a native embedding request.
func GeminiEmbeddingAction(req cliproxyexecutor.Request) string {
	if req.Metadata == nil {
		return ""
	}
	action, _ := req.Metadata[cliproxyexecutor.RequestActionMetadataKey].(string)
	action = strings.TrimSpace(action)
	if !IsGeminiEmbeddingAction(action) {
		return ""
	}
	return action
}

// IsGeminiEmbeddingAction reports whether action is a supported native embedding action.
func IsGeminiEmbeddingAction(action string) bool {
	switch strings.TrimSpace(action) {
	case GeminiEmbedContentAction, GeminiBatchEmbedContentsAction:
		return true
	default:
		return false
	}
}

// PrepareGeminiEmbeddingDoc builds the native upstream payload for an embedding action.
//
// Embedding requests are not chat requests: this helper must never inject
// contents, safetySettings, tools, generationConfig, or role normalization.
// Callers must apply payload rules afterwards through the payload finalizer and
// serialize the returned doc only once, after finalization.
func PrepareGeminiEmbeddingDoc(payload []byte, baseModel, action string) (*ojson.Doc, error) {
	action = strings.TrimSpace(action)
	if !IsGeminiEmbeddingAction(action) {
		return nil, fmt.Errorf("unsupported embedding action %q", action)
	}
	baseModel = strings.TrimSpace(baseModel)
	if baseModel == "" {
		return nil, fmt.Errorf("embedding model is required")
	}

	trimmed := bytes.TrimSpace(payload)
	if len(trimmed) == 0 {
		return nil, fmt.Errorf("embedding request body is empty")
	}
	if trimmed[0] != '{' || trimmed[len(trimmed)-1] != '}' || !ojson.ValidBytes(trimmed) {
		return nil, fmt.Errorf("embedding request body must be a JSON object")
	}

	doc := ojson.NewDoc(trimmed)
	switch action {
	case GeminiEmbedContentAction:
		// embedContent identifies the model in both the path and the body, and the
		// body form must use the Google-native "models/<id>" resource name.
		SetStringIfDifferentDoc(doc, "model", "models/"+baseModel)
	case GeminiBatchEmbedContentsAction:
		// Batch entries carry their own model fields and the upstream service validates
		// them per entry, while the path model is not used for that check. Entries that
		// already name a model (including mixed-model batches) are left untouched; only
		// missing/empty/null entries inherit the path model so callers do not have to
		// repeat it. Order and per-entry intent are preserved.
		requests := doc.Get("requests")
		if !requests.IsArray() {
			return nil, fmt.Errorf("batchEmbedContents requires a requests array")
		}
		seen := false
		var modelLessEntries []int
		index := 0
		requests.ForEach(func(_, entry ojson.Result) bool {
			seen = true
			position := index
			index++
			if !entry.IsObject() {
				return true
			}
			model := entry.Get("model")
			if model.Type == ojson.String && strings.TrimSpace(model.String()) != "" {
				return true
			}
			modelLessEntries = append(modelLessEntries, position)
			return true
		})
		if !seen {
			return nil, fmt.Errorf("batchEmbedContents requires at least one request")
		}
		// Mutate only after the read pass: Set invalidates results obtained from doc.
		resourceName := "models/" + strings.TrimPrefix(strings.TrimSpace(baseModel), "models/")
		for _, position := range modelLessEntries {
			if errSet := doc.Set("requests."+strconv.Itoa(position)+".model", resourceName); errSet != nil {
				return nil, fmt.Errorf("batchEmbedContents entry %d: set model: %w", position, errSet)
			}
		}
	}
	return doc, nil
}
