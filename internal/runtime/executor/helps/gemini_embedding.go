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

// NormalizeGeminiModelResourceName removes one leading "models/" resource prefix from a
// model identifier. Native endpoints take the bare ID in both URL and payload, so a config
// alias or router target expressed as a resource name must not produce models/models/<id>.
func NormalizeGeminiModelResourceName(model string) string {
	model = strings.TrimSpace(model)
	return strings.TrimPrefix(model, "models/")
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
		SetStringIfDifferentDoc(doc, "model", "models/"+NormalizeGeminiModelResourceName(baseModel))
	case GeminiBatchEmbedContentsAction:
		// Upstream requires requests[].model per entry and ignores the model's value when
		// computing embeddings (the path model decides), but it does enforce the
		// "models/<id>" resource-name format: a missing model or a bare identifier is
		// rejected with a format error. Missing entries therefore inherit the path model,
		// and bare identifiers are normalized to the resource-name form. Entries that are
		// already resource names (including aliases and mixed-model batches) are left
		// untouched, and order is preserved.
		requests := doc.Get("requests")
		if !requests.IsArray() {
			return nil, fmt.Errorf("batchEmbedContents requires a requests array")
		}
		resourceName := "models/" + NormalizeGeminiModelResourceName(baseModel)
		seen := false
		var desired []string
		requests.ForEach(func(_, entry ojson.Result) bool {
			seen = true
			if !entry.IsObject() {
				desired = append(desired, "")
				return true
			}
			current := ""
			if model := entry.Get("model"); model.Type == ojson.String {
				current = strings.TrimSpace(model.String())
			}
			switch {
			case current == "":
				desired = append(desired, resourceName)
			case strings.HasPrefix(current, "models/"):
				// Already the accepted shape; the value does not change the result.
				desired = append(desired, "")
			case strings.Contains(current, "/"):
				// Some other resource form: leave it for upstream to report.
				desired = append(desired, "")
			default:
				desired = append(desired, "models/"+current)
			}
			return true
		})
		if !seen {
			return nil, fmt.Errorf("batchEmbedContents requires at least one request")
		}
		// Mutate only after the read pass: Set invalidates results obtained from doc.
		for position, value := range desired {
			if value == "" {
				continue
			}
			if errSet := doc.Set("requests."+strconv.Itoa(position)+".model", value); errSet != nil {
				return nil, fmt.Errorf("batchEmbedContents entry %d: set model: %w", position, errSet)
			}
		}
	}
	return doc, nil
}
