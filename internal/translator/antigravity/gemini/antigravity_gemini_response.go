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
	"strconv"
	"strings"

	"github.com/MoeForge/moego/ojson"
	translatorcommon "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/common"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
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
		p.SawResponse = translatorcommon.HasAntigravityResponsePayload(rawJSON)
	}
	if !p.SawFinishReason {
		for _, path := range []string{"response.candidates", "candidates"} {
			for _, candidate := range ojson.GetBytesNoCopy(rawJSON, path).Array() {
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
		if usage := ojson.GetBytesNoCopy(rawJSON, path); usage.Exists() {
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
	chunkDoc := ojson.NewDoc([]byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":""}]},"finishReason":"STOP"}]}`))
	if len(p.UsageMetadata) > 0 {
		_ = chunkDoc.SetRawBytes("usageMetadata", p.UsageMetadata)
	}
	if p.ModelVersion != "" {
		_ = chunkDoc.Set("modelVersion", p.ModelVersion)
	}
	if p.ResponseID != "" {
		_ = chunkDoc.Set("responseId", p.ResponseID)
	}
	return chunkDoc.Bytes()
}

func firstStringValue(rawJSON []byte, paths ...string) string {
	for _, path := range paths {
		if value := ojson.GetBytesNoCopy(rawJSON, path).String(); value != "" {
			return strings.Clone(value)
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
			finalChunk, _ = ojson.SetRawBytes([]byte("[]"), "-1", finalChunk)
		}
		return [][]byte{finalChunk}
	}

	if state != nil {
		state.observe(rawJSON)
	}

	if alt, ok := ctx.Value("alt").(string); ok {
		var chunk []byte
		nameMap := util.DisambiguatedToolNameMap(originalRequestRawJSON)
		if alt == "" {
			if response := util.GetGJSONBytesNoCopy(rawJSON, "response"); response.Exists() {
				chunk = geminiResponseBytes(rawJSON, response)
				doc := ojson.NewDoc(chunk)
				if restoreGeminiResponseFields(doc, "", nameMap) {
					chunk = doc.Bytes()
				}
			}
		} else {
			doc := ojson.NewDocString("[]")
			items := util.ParseGJSONBytesNoCopy(rawJSON)
			if items.IsArray() {
				index := 0
				for _, item := range items.Array() {
					if response := item.Get("response"); response.Exists() {
						_ = doc.SetRaw("-1", response.Raw)
						restoreGeminiResponseFields(doc, strconv.Itoa(index)+".", nameMap)
						index++
					}
				}
			}
			chunk = doc.Bytes()
		}
		return [][]byte{chunk}
	}
	return [][]byte{}
}

func geminiResponseBytes(rawJSON []byte, response ojson.Result) []byte {
	if response.Index > 0 && response.Index+len(response.Raw) <= len(rawJSON) {
		return rawJSON[response.Index : response.Index+len(response.Raw)]
	}
	return []byte(response.Raw)
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
	chunk := rawJSON
	response := util.GetGJSONBytesNoCopy(rawJSON, "response")
	if response.Exists() {
		chunk = geminiResponseBytes(rawJSON, response)
	}
	doc := ojson.NewDoc(chunk)
	nameMap := util.DisambiguatedToolNameMap(originalRequestRawJSON)
	var changed bool
	if response.Exists() {
		changed = restoreGeminiResponseFields(doc, "", nameMap)
	} else {
		changed = restoreGeminiFunctionNames(doc, "", nameMap)
	}
	// Default every candidate, not just the first one, so a multi-candidate
	// response cannot leave some of them unterminated.
	if candidates := ojson.GetBytesNoCopy(chunk, "candidates"); candidates.IsArray() {
		idx := 0
		candidates.ForEach(func(_, candidate ojson.Result) bool {
			if candidate.Get("finishReason").String() == "" {
				_ = doc.Set(fmt.Sprintf("candidates.%d.finishReason", idx), "STOP")
				changed = true
			}
			idx++
			return true
		})
	}
	if changed {
		return doc.Bytes()
	}
	return chunk
}

// prefix is empty for a single response, or the output array index followed by a dot.
func restoreGeminiResponseFields(doc *ojson.Doc, prefix string, nameMap map[string]string) bool {
	usageChanged := restoreUsageMetadata(doc, prefix)
	return restoreGeminiFunctionNames(doc, prefix, nameMap) || usageChanged
}

func restoreGeminiFunctionNames(doc *ojson.Doc, prefix string, nameMap map[string]string) bool {
	if len(nameMap) == 0 {
		return false
	}
	changed := false
	candidates := doc.Get(prefix + "candidates")
	for candidateIndex, candidate := range candidates.Array() {
		for partIndex, part := range candidate.Get("content.parts").Array() {
			for _, field := range []string{"functionCall", "functionResponse", "function_call", "function_response"} {
				nameResult := part.Get(field + ".name")
				name := nameResult.String()
				if name == "" {
					continue
				}
				restoredName := util.RestoreSanitizedToolName(nameMap, name)
				if nameResult.Type == ojson.String && restoredName == name {
					continue
				}
				path := fmt.Sprintf("%scandidates.%d.content.parts.%d.%s.name", prefix, candidateIndex, partIndex, field)
				_ = doc.Set(path, restoredName)
				changed = true
			}
		}
	}
	return changed
}

func GeminiTokenCount(ctx context.Context, count int64) []byte {
	return translatorcommon.GeminiTokenCountJSON(count)
}

// restoreUsageMetadata renames cpaUsageMetadata back to usageMetadata.
// The executor renames usageMetadata to cpaUsageMetadata in non-terminal chunks
// to preserve usage data while hiding it from clients that don't expect it.
// When returning standard Gemini API format, we must restore the original name.
func restoreUsageMetadata(doc *ojson.Doc, prefix string) bool {
	usage := doc.Get(prefix + "cpaUsageMetadata")
	if !usage.Exists() {
		return false
	}
	_ = doc.SetRaw(prefix+"usageMetadata", usage.Raw)
	_ = doc.Delete(prefix + "cpaUsageMetadata")
	return true
}
