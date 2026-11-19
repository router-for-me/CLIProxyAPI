package helps

import (
	"bytes"
	"fmt"
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
		// Batch entries carry their own model fields; rewrites there would change
		// caller-provided ordering and per-entry intent, so only validate the shape.
		requests := doc.Get("requests")
		if !requests.IsArray() {
			return nil, fmt.Errorf("batchEmbedContents requires a requests array")
		}
		seen := false
		requests.ForEach(func(_, _ ojson.Result) bool {
			seen = true
			return false
		})
		if !seen {
			return nil, fmt.Errorf("batchEmbedContents requires at least one request")
		}
	}
	return doc, nil
}
