package executor

import (
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func codexFallbackDisplayModel(opts cliproxyexecutor.Options) string {
	if v, ok := opts.Metadata[cliproxyexecutor.CodexFallbackDisplayModelMetadataKey].(string); ok {
		return v
	}
	return ""
}

// rewriteCodexResponseModel rewrites the "model" and "response.model" fields in a
// Codex response payload to displayModel, making the model fallback transparent to clients.
func rewriteCodexResponseModel(payload []byte, displayModel string) []byte {
	if displayModel == "" || len(payload) == 0 {
		return payload
	}
	out, err := sjson.SetBytes(payload, "model", displayModel)
	if err != nil {
		return payload
	}
	if gjson.GetBytes(out, "response.model").Exists() {
		if patched, errPatch := sjson.SetBytes(out, "response.model", displayModel); errPatch == nil {
			out = patched
		}
	}
	return out
}
