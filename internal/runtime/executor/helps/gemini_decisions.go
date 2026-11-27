package helps

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/MoeForge/moego/ojson"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

// GeminiDecisionsAction is the model action for the native Gemini Decisions API.
// It is served by POST /v1beta/decisions with the model named in the request body.
const GeminiDecisionsAction = "decisions"

// GeminiDecisionAction returns the decisions action carried by request metadata,
// or an empty string when the request is not a decisions request.
func GeminiDecisionAction(req cliproxyexecutor.Request) string {
	if req.Metadata == nil {
		return ""
	}
	action, _ := req.Metadata[cliproxyexecutor.RequestActionMetadataKey].(string)
	action = strings.TrimSpace(action)
	if !IsGeminiDecisionsAction(action) {
		return ""
	}
	return action
}

// IsGeminiDecisionsAction reports whether action is the decisions action.
func IsGeminiDecisionsAction(action string) bool {
	return strings.TrimSpace(action) == GeminiDecisionsAction
}

// PrepareGeminiDecisionsDoc builds the native upstream payload for the Decisions API.
//
// Decisions requests are not chat requests: this helper validates only the request
// envelope and must never inject contents, safetySettings, tools, generationConfig,
// or role normalization. Deep question validation (key charset, criteria, count) is
// left to the upstream service. Callers must apply payload rules afterwards through
// the payload finalizer and serialize the returned doc only once, after finalization.
func PrepareGeminiDecisionsDoc(payload []byte, baseModel string) (*ojson.Doc, error) {
	baseModel = strings.TrimSpace(baseModel)
	if baseModel == "" {
		return nil, fmt.Errorf("decisions model is required")
	}

	trimmed := bytes.TrimSpace(payload)
	if len(trimmed) == 0 {
		return nil, fmt.Errorf("decisions request body is empty")
	}
	if trimmed[0] != '{' || trimmed[len(trimmed)-1] != '}' || !ojson.ValidBytes(trimmed) {
		return nil, fmt.Errorf("decisions request body must be a JSON object")
	}

	doc := ojson.NewDoc(trimmed)
	questions := doc.Get("questions")
	if !questions.IsObject() {
		return nil, fmt.Errorf("decisions request requires a questions object")
	}
	questionCount := 0
	questions.ForEach(func(_, _ ojson.Result) bool {
		questionCount++
		return true
	})
	if questionCount == 0 {
		return nil, fmt.Errorf("decisions request requires at least one question")
	}
	if input := doc.Get("input"); !input.Exists() || input.Type == ojson.Null {
		return nil, fmt.Errorf("decisions request requires an input field")
	}
	if stream := doc.Get("stream"); stream.IsBool() && stream.Bool() {
		return nil, fmt.Errorf("decisions requests are non-streaming: stream must be false")
	}
	if model := strings.TrimPrefix(baseModel, "models/"); model != "" {
		// The Decisions API identifies the model in the body using the bare model ID,
		// unlike embedContent which uses the models/<id> resource name.
		SetStringIfDifferentDoc(doc, "model", model)
	}
	return doc, nil
}

// ParseGeminiDecisionsUsage extracts usage from a Decisions API response.
//
// Decisions responses report usage with snake_case keys under a top-level "usage"
// object (total_input_tokens / total_output_tokens / total_tokens plus
// input_tokens_by_modality), which the camelCase usageMetadata parser does not read.
// That shape is identical to the Interactions usage shape, so this shares the
// Interactions parser instead of duplicating modality mapping. Output tokens are
// always zero because the API emits no text tokens.
func ParseGeminiDecisionsUsage(data []byte) usage.Detail {
	return ParseInteractionsUsage(data)
}
