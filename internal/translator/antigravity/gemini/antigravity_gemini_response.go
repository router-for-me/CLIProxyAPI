// Package gemini provides request translation functionality for Gemini to Antigravity API compatibility.
// It handles parsing and transforming Gemini API requests into Antigravity API format,
// extracting model information, system instructions, message contents, and tool declarations.
// The package performs JSON data transformation to ensure compatibility
// between Gemini API format and Antigravity API's expected format.
package gemini

import (
	"bytes"
	"context"
	"fmt"

	translatorcommon "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/common"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type convertAntigravityGeminiParams struct {
	SawResponse     bool
	SawFinishReason bool
	ModelVersion    string
	ResponseID      string
	UsageMetadata   []byte
}

// observe records the metadata required to synthesize a faithful terminal chunk
// when upstream never emits finishReason.
func (p *convertAntigravityGeminiParams) observe(rawJSON []byte) {
	if !p.SawResponse {
		p.SawResponse = hasAntigravityResponsePayload(rawJSON)
	}
	if !p.SawFinishReason {
		for _, path := range []string{"response.candidates", "candidates"} {
			for _, candidate := range gjson.GetBytes(rawJSON, path).Array() {
				if candidate.Get("finishReason").String() != "" {
					p.SawFinishReason = true
					break
				}
			}
			if p.SawFinishReason {
				break
			}
		}
	}
	if p.ModelVersion == "" {
		p.ModelVersion = firstStringValue(rawJSON, "response.modelVersion", "modelVersion")
	}
	if p.ResponseID == "" {
		p.ResponseID = firstStringValue(rawJSON, "response.responseId", "responseId")
	}
	// Keep the latest usage snapshot. FilterSSEUsageMetadata renames non-terminal
	// usage to cpaUsageMetadata, so both spellings must be tracked.
	for _, path := range []string{"response.usageMetadata", "response.cpaUsageMetadata", "usageMetadata", "cpaUsageMetadata"} {
		if usage := gjson.GetBytes(rawJSON, path); usage.Exists() {
			p.UsageMetadata = append(p.UsageMetadata[:0], usage.Raw...)
			break
		}
	}
}

// syntheticTerminalChunk mirrors the terminal chunk shape observed from upstream:
// a model-role candidate carrying an empty text part, finishReason, and the last
// known usage metadata. Fields are set in upstream key order
// (candidates, usageMetadata, modelVersion, responseId).
func (p *convertAntigravityGeminiParams) syntheticTerminalChunk() []byte {
	chunk := []byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":""}]},"finishReason":"STOP"}]}`)
	if len(p.UsageMetadata) > 0 {
		chunk, _ = sjson.SetRawBytes(chunk, "usageMetadata", p.UsageMetadata)
	}
	if p.ModelVersion != "" {
		chunk, _ = sjson.SetBytes(chunk, "modelVersion", p.ModelVersion)
	}
	if p.ResponseID != "" {
		chunk, _ = sjson.SetBytes(chunk, "responseId", p.ResponseID)
	}
	return chunk
}

// hasAntigravityResponsePayload reports whether a chunk carries actual generated
// content or token accounting. Presence alone is not enough: an envelope such as
// `{}`, `{"response":{}}` or `{"response":{"candidates":[]}}` must not count as a
// started stream, otherwise a keepalive or malformed event would be enough to
// finalize a stream that produced nothing.
func hasAntigravityResponsePayload(rawJSON []byte) bool {
	for _, path := range []string{"response.candidates", "candidates"} {
		if candidates := gjson.GetBytes(rawJSON, path); candidates.IsArray() && len(candidates.Array()) > 0 {
			return true
		}
	}
	// FilterSSEUsageMetadata renames non-terminal usage to cpaUsageMetadata before
	// the translator sees it, so both spellings must be accepted.
	for _, path := range []string{
		"response.usageMetadata", "response.cpaUsageMetadata",
		"usageMetadata", "cpaUsageMetadata",
	} {
		if usage := gjson.GetBytes(rawJSON, path); usage.IsObject() && len(usage.Map()) > 0 {
			return true
		}
	}
	return false
}

func firstStringValue(rawJSON []byte, paths ...string) string {
	for _, path := range paths {
		if value := gjson.GetBytes(rawJSON, path).String(); value != "" {
			return value
		}
	}
	return ""
}

// ConvertAntigravityResponseToGemini parses and transforms a Antigravity API request into Gemini API format.
// It extracts the model name, system instruction, message contents, and tool declarations
// from the raw JSON request and returns them in the format expected by the Gemini API.
// The function performs the following transformations:
// 1. Extracts the response data from the request
// 2. Handles alternative response formats
// 3. Processes array responses by extracting individual response objects
// 4. Synthesizes finishReason: "STOP" on [DONE] if omitted by upstream
//
// Parameters:
//   - ctx: The context for the request, used for cancellation and timeout handling
//   - modelName: The name of the model to use for the request (unused in current implementation)
//   - rawJSON: The raw JSON request data from the Antigravity API
//   - param: A pointer to a parameter object for the conversion
//
// Returns:
//   - [][]byte: The transformed response data in Gemini API format.
func ConvertAntigravityResponseToGemini(ctx context.Context, _ string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) [][]byte {
	if bytes.HasPrefix(rawJSON, []byte("data:")) {
		rawJSON = bytes.TrimSpace(rawJSON[5:])
	}

	var state *convertAntigravityGeminiParams
	if param != nil {
		if *param == nil {
			*param = &convertAntigravityGeminiParams{}
		}
		if s, ok := (*param).(*convertAntigravityGeminiParams); ok {
			state = s
		}
	}

	if bytes.Equal(rawJSON, []byte("[DONE]")) {
		// Never finalize a stream that produced no response at all: an empty
		// upstream body must stay detectable as a failure rather than be reported
		// as a successful empty completion.
		if state == nil || !state.SawResponse || state.SawFinishReason {
			return [][]byte{}
		}
		state.SawFinishReason = true
		finalChunk := state.syntheticTerminalChunk()
		if alt, ok := ctx.Value("alt").(string); ok && alt != "" {
			finalChunk, _ = sjson.SetRawBytes([]byte("[]"), "-1", finalChunk)
		}
		return [][]byte{finalChunk}
	}

	if state != nil {
		state.observe(rawJSON)
	}

	if alt, ok := ctx.Value("alt").(string); ok {
		var chunk []byte
		if alt == "" {
			responseResult := gjson.GetBytes(rawJSON, "response")
			if responseResult.Exists() {
				chunk = []byte(responseResult.Raw)
				chunk = restoreUsageMetadata(chunk)
				chunk = restoreGeminiFunctionNames(chunk, originalRequestRawJSON)
			}
		} else {
			chunkTemplate := []byte("[]")
			responseResult := gjson.ParseBytes(rawJSON)
			if responseResult.IsArray() {
				responseResultItems := responseResult.Array()
				for i := 0; i < len(responseResultItems); i++ {
					responseResultItem := responseResultItems[i]
					if responseResultItem.Get("response").Exists() {
						chunkTemplate, _ = sjson.SetRawBytes(chunkTemplate, "-1", []byte(responseResultItem.Get("response").Raw))
					}
				}
			}
			chunk = chunkTemplate
		}
		return [][]byte{chunk}
	}
	return [][]byte{}
}

// ConvertAntigravityResponseToGeminiNonStream converts a non-streaming Antigravity request to a non-streaming Gemini response.
// This function processes the complete Antigravity request and transforms it into a single Gemini-compatible
// JSON response. It extracts the response data from the request and returns it in the expected format.
//
// Parameters:
//   - ctx: The context for the request, used for cancellation and timeout handling
//   - modelName: The name of the model being used for the response (unused in current implementation)
//   - rawJSON: The raw JSON request data from the Antigravity API
//   - param: A pointer to a parameter object for the conversion (unused in current implementation)
//
// Returns:
//   - []byte: A Gemini-compatible JSON response containing the response data.
func ConvertAntigravityResponseToGeminiNonStream(_ context.Context, _ string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, _ *any) []byte {
	var chunk []byte
	responseResult := gjson.GetBytes(rawJSON, "response")
	if responseResult.Exists() {
		chunk = restoreUsageMetadata([]byte(responseResult.Raw))
		chunk = restoreGeminiFunctionNames(chunk, originalRequestRawJSON)
	} else {
		chunk = restoreGeminiFunctionNames(rawJSON, originalRequestRawJSON)
	}
	candidates := gjson.GetBytes(chunk, "candidates")
	if candidates.IsArray() {
		// Default every candidate, not just the first one, so a multi-candidate
		// response cannot leave some of them unterminated.
		for i, candidate := range candidates.Array() {
			if candidate.Get("finishReason").String() == "" {
				chunk, _ = sjson.SetBytes(chunk, fmt.Sprintf("candidates.%d.finishReason", i), "STOP")
			}
		}
	}
	return chunk
}

func restoreGeminiFunctionNames(chunk, originalRequestRawJSON []byte) []byte {
	nameMap := util.DisambiguatedToolNameMap(originalRequestRawJSON)
	if len(nameMap) == 0 {
		return chunk
	}
	candidates := gjson.GetBytes(chunk, "candidates")
	for candidateIndex, candidate := range candidates.Array() {
		for partIndex, part := range candidate.Get("content.parts").Array() {
			for _, field := range []string{"functionCall", "functionResponse", "function_call", "function_response"} {
				nameResult := part.Get(field + ".name")
				name := nameResult.String()
				if name == "" {
					continue
				}
				restoredName := util.RestoreSanitizedToolName(nameMap, name)
				if nameResult.Type == gjson.String && restoredName == name {
					continue
				}
				path := fmt.Sprintf("candidates.%d.content.parts.%d.%s.name", candidateIndex, partIndex, field)
				chunk, _ = sjson.SetBytes(chunk, path, restoredName)
			}
		}
	}
	return chunk
}

func GeminiTokenCount(ctx context.Context, count int64) []byte {
	return translatorcommon.GeminiTokenCountJSON(count)
}

// restoreUsageMetadata renames cpaUsageMetadata back to usageMetadata.
// The executor renames usageMetadata to cpaUsageMetadata in non-terminal chunks
// to preserve usage data while hiding it from clients that don't expect it.
// When returning standard Gemini API format, we must restore the original name.
func restoreUsageMetadata(chunk []byte) []byte {
	if cpaUsage := gjson.GetBytes(chunk, "cpaUsageMetadata"); cpaUsage.Exists() {
		chunk, _ = sjson.SetRawBytes(chunk, "usageMetadata", []byte(cpaUsage.Raw))
		chunk, _ = sjson.DeleteBytes(chunk, "cpaUsageMetadata")
	}
	return chunk
}
