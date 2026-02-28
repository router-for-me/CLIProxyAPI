package fallback

import (
	"bytes"
	"fmt"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/thinking"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// SetFallbackHeaders adds model fallback observability headers to the response.
func SetFallbackHeaders(headers http.Header, requestedModel, actualModel string, attempts int, cfg *config.Config) {
	if cfg == nil || !cfg.ModelFallback.ExposeActualModelHeader {
		return
	}
	if headers == nil {
		return
	}
	headers.Set(HeaderActualModel, actualModel)
	headers.Set(HeaderRequestedModel, requestedModel)
	headers.Set(HeaderFallbackAttempts, fmt.Sprintf("%d", attempts))
}

// RewriteResponseModel rewrites the model field in a non-streaming JSON response
// from the actual model back to the requested model.
func RewriteResponseModel(payload []byte, requestedModel, actualModel string, cfg *config.Config) []byte {
	if cfg == nil || !cfg.ModelFallback.PreserveRequestedModel {
		return payload
	}
	if requestedModel == actualModel {
		return payload
	}
	if len(payload) == 0 {
		return payload
	}

	// Check if the payload has a "model" field matching the actual model
	modelVal := gjson.GetBytes(payload, "model")
	if !modelVal.Exists() {
		return payload
	}

	// Strip thinking suffix for comparison (response won't include suffix like "(8192)")
	actualBase := thinking.ParseSuffix(actualModel).ModelName
	if modelVal.Str == actualBase || strings.EqualFold(modelVal.Str, actualBase) {
		// Rewrite to the requested model's base name (without suffix)
		requestedBase := thinking.ParseSuffix(requestedModel).ModelName
		result, err := sjson.SetBytes(payload, "model", requestedBase)
		if err != nil {
			return payload
		}
		return result
	}

	return payload
}

// RewriteStreamChunkModel rewrites the model field in a streaming SSE chunk.
// Only processes "data:" prefixed JSON lines.
func RewriteStreamChunkModel(chunk []byte, requestedModel, actualModel string, cfg *config.Config) []byte {
	if cfg == nil || !cfg.ModelFallback.PreserveRequestedModel {
		return chunk
	}
	if requestedModel == actualModel {
		return chunk
	}
	if len(chunk) == 0 {
		return chunk
	}

	// Process each line in the chunk
	lines := bytes.Split(chunk, []byte("\n"))
	modified := false
	for i, line := range lines {
		trimmed := bytes.TrimSpace(line)
		if !bytes.HasPrefix(trimmed, []byte("data:")) {
			continue
		}
		jsonPart := bytes.TrimSpace(trimmed[5:])
		if len(jsonPart) == 0 || jsonPart[0] != '{' {
			continue
		}

		modelVal := gjson.GetBytes(jsonPart, "model")
		if !modelVal.Exists() {
			continue
		}
		actualBase := thinking.ParseSuffix(actualModel).ModelName
		if modelVal.Str == actualBase || strings.EqualFold(modelVal.Str, actualBase) {
			requestedBase := thinking.ParseSuffix(requestedModel).ModelName
			result, err := sjson.SetBytes(jsonPart, "model", requestedBase)
			if err != nil {
				continue
			}
			lines[i] = append([]byte("data: "), result...)
			modified = true
		}
	}

	if !modified {
		return chunk
	}
	return bytes.Join(lines, []byte("\n"))
}
