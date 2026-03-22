// Package gemini provides request translation functionality for Gemini CLI to Gemini API compatibility.
// It handles parsing and transforming Gemini CLI API requests into Gemini API format,
// extracting model information, system instructions, message contents, and tool declarations.
// The package performs JSON data transformation to ensure compatibility
// between Gemini CLI API format and Gemini API's expected format.
package gemini

import (
	"fmt"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/translator/gemini/common"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ConvertGeminiRequestToGeminiCLI parses and transforms a Gemini CLI API request into Gemini API format.
// It extracts the model name, system instruction, message contents, and tool declarations
// from the raw JSON request and returns them in the format expected by the Gemini API.
// The function performs the following transformations:
// 1. Extracts the model information from the request
// 2. Restructures the JSON to match Gemini API format
// 3. Converts system instructions to the expected format
// 4. Fixes CLI tool response format and grouping
//
// Parameters:
//   - modelName: The name of the model to use for the request (unused in current implementation)
//   - rawJSON: The raw JSON request data from the Gemini CLI API
//   - stream: A boolean indicating if the request is for a streaming response (unused in current implementation)
//
// Returns:
//   - []byte: The transformed request data in Gemini API format
func ConvertGeminiRequestToGeminiCLI(_ string, inputRawJSON []byte, _ bool) []byte {
	rawJSON := inputRawJSON
	template := []byte(`{"project":"","request":{},"model":""}`)
	template, _ = sjson.SetRawBytes(template, "request", rawJSON)
	template, _ = sjson.SetBytes(template, "model", gjson.GetBytes(template, "request.model").String())
	template, _ = sjson.DeleteBytes(template, "request.model")

	templateStr, errFixCLIToolResponse := fixCLIToolResponse(string(template))
	if errFixCLIToolResponse != nil {
		return []byte{}
	}
	template = []byte(templateStr)

	systemInstructionResult := gjson.GetBytes(template, "request.system_instruction")
	if systemInstructionResult.Exists() {
		template, _ = sjson.SetRawBytes(template, "request.systemInstruction", []byte(systemInstructionResult.Raw))
		template, _ = sjson.DeleteBytes(template, "request.system_instruction")
	}
	rawJSON = template

	// Normalize roles in request.contents: default to valid values if missing/invalid
	contents := gjson.GetBytes(rawJSON, "request.contents")
	if contents.Exists() {
		prevRole := ""
		idx := 0
		contents.ForEach(func(_ gjson.Result, value gjson.Result) bool {
			role := value.Get("role").String()
			valid := role == "user" || role == "model"
			if role == "" || !valid {
				var newRole string
				if prevRole == "" {
					newRole = "user"
				} else if prevRole == "user" {
					newRole = "model"
				} else {
					newRole = "user"
				}
				path := fmt.Sprintf("request.contents.%d.role", idx)
				rawJSON, _ = sjson.SetBytes(rawJSON, path, newRole)
				role = newRole
			}
			prevRole = role
			idx++
			return true
		})
	}

	toolsResult := gjson.GetBytes(rawJSON, "request.tools")
	if toolsResult.Exists() && toolsResult.IsArray() {
		toolResults := toolsResult.Array()
		for i := 0; i < len(toolResults); i++ {
			functionDeclarationsResult := gjson.GetBytes(rawJSON, fmt.Sprintf("request.tools.%d.function_declarations", i))
			if functionDeclarationsResult.Exists() && functionDeclarationsResult.IsArray() {
				functionDeclarationsResults := functionDeclarationsResult.Array()
				for j := 0; j < len(functionDeclarationsResults); j++ {
					parametersResult := gjson.GetBytes(rawJSON, fmt.Sprintf("request.tools.%d.function_declarations.%d.parameters", i, j))
					if parametersResult.Exists() {
						strJson, _ := util.RenameKey(string(rawJSON), fmt.Sprintf("request.tools.%d.function_declarations.%d.parameters", i, j), fmt.Sprintf("request.tools.%d.function_declarations.%d.parametersJsonSchema", i, j))
						rawJSON = []byte(strJson)
					}
				}
			}
		}
	}

	gjson.GetBytes(rawJSON, "request.contents").ForEach(func(key, content gjson.Result) bool {
		if content.Get("role").String() == "model" {
			content.Get("parts").ForEach(func(partKey, part gjson.Result) bool {
				if part.Get("functionCall").Exists() {
					rawJSON, _ = sjson.SetBytes(rawJSON, fmt.Sprintf("request.contents.%d.parts.%d.thoughtSignature", key.Int(), partKey.Int()), "skip_thought_signature_validator")
				} else if part.Get("thoughtSignature").Exists() {
					rawJSON, _ = sjson.SetBytes(rawJSON, fmt.Sprintf("request.contents.%d.parts.%d.thoughtSignature", key.Int(), partKey.Int()), "skip_thought_signature_validator")
				}
				return true
			})
		}
		return true
	})

	// Filter out contents with empty parts to avoid Gemini API error:
	// "required oneof field 'data' must have one initialized field"
	filteredContents := []byte(`[]`)
	hasFiltered := false
	gjson.GetBytes(rawJSON, "request.contents").ForEach(func(_, content gjson.Result) bool {
		parts := content.Get("parts")
		if !parts.IsArray() || len(parts.Array()) == 0 {
			hasFiltered = true
			return true
		}
		filteredContents, _ = sjson.SetRawBytes(filteredContents, "-1", []byte(content.Raw))
		return true
	})
	if hasFiltered {
		rawJSON, _ = sjson.SetRawBytes(rawJSON, "request.contents", filteredContents)
	}

	return common.AttachDefaultSafetySettings(rawJSON, "request.safetySettings")
}

// FunctionCallGroup represents a group of function calls and their responses
type FunctionCallGroup struct {
	ResponsesNeeded int
	CallNames       []string // ordered function call names for backfilling empty response names
}

// backfillFunctionResponseName ensures that a functionResponse JSON object has a non-empty name,
// falling back to fallbackName if the original is empty.
func backfillFunctionResponseName(raw string, fallbackName string) string {
	name := gjson.Get(raw, "functionResponse.name").String()
	if strings.TrimSpace(name) == "" && fallbackName != "" {
		rawBytes, _ := sjson.SetBytes([]byte(raw), "functionResponse.name", fallbackName)
		raw = string(rawBytes)
	}
	return raw
}

// fixCLIToolResponse performs sophisticated tool response format conversion and grouping.
// This function transforms the CLI tool response format by intelligently grouping function calls
// with their corresponding responses, ensuring proper conversation flow and API compatibility.
// It converts from a linear format (1.json) to a grouped format (2.json) where function calls
// and their responses are properly associated and structured.
//
// Parameters:
//   - input: The input JSON string to be processed
//
// Returns:
//   - string: The processed JSON string with grouped function calls and responses
//   - error: An error if the processing fails
func fixCLIToolResponse(input string) (string, error) {
	// Parse the input JSON to extract the conversation structure
	parsed := gjson.Parse(input)

	// Extract the contents array which contains the conversation messages
	contents := parsed.Get("request.contents")
	if !contents.Exists() {
		// log.Debugf(input)
		return input, fmt.Errorf("contents not found in input")
	}

	// Initialize data structures for processing and grouping
	contentsWrapper := []byte(`{"contents":[]}`)
	var pendingGroups []*FunctionCallGroup // Groups awaiting completion with responses
	var collectedResponses []gjson.Result  // Standalone responses to be matched

	// Process each content object in the conversation
	// This iterates through messages and groups function calls with their responses
	contents.ForEach(func(key, value gjson.Result) bool {
		role := value.Get("role").String()
		parts := value.Get("parts")

		// Check if this content has function responses
		var responsePartsInThisContent []gjson.Result
		parts.ForEach(func(_, part gjson.Result) bool {
			if part.Get("functionResponse").Exists() {
				responsePartsInThisContent = append(responsePartsInThisContent, part)
			}
			return true
		})

		// If this content has function responses, collect them
		if len(responsePartsInThisContent) > 0 {
			collectedResponses = append(collectedResponses, responsePartsInThisContent...)

			// Check if pending groups can be satisfied (FIFO: oldest group first)
			for len(pendingGroups) > 0 && len(collectedResponses) >= pendingGroups[0].ResponsesNeeded {
				group := pendingGroups[0]
				pendingGroups = pendingGroups[1:]

				// Take the needed responses for this group
				groupResponses := collectedResponses[:group.ResponsesNeeded]
				collectedResponses = collectedResponses[group.ResponsesNeeded:]

				// Create merged function response content
				functionResponseContent := []byte(`{"parts":[],"role":"function"}`)
				for ri, response := range groupResponses {
					if !response.IsObject() {
						log.Warnf("failed to parse function response")
						continue
					}
					raw := backfillFunctionResponseName(response.Raw, group.CallNames[ri])
					functionResponseContent, _ = sjson.SetRawBytes(functionResponseContent, "parts.-1", []byte(raw))
				}

				if gjson.GetBytes(functionResponseContent, "parts.#").Int() > 0 {
					contentsWrapper, _ = sjson.SetRawBytes(contentsWrapper, "contents.-1", functionResponseContent)
				}
			}

			return true // Skip adding this content, responses are merged
		}

		// If this is a model with function calls, create a new group
		if role == "model" {
			var callNames []string
			parts.ForEach(func(_, part gjson.Result) bool {
				if part.Get("functionCall").Exists() {
					callNames = append(callNames, part.Get("functionCall.name").String())
				}
				return true
			})

			if len(callNames) > 0 {
				// Add the model content
				if !value.IsObject() {
					log.Warnf("failed to parse model content")
					return true
				}
				contentsWrapper, _ = sjson.SetRawBytes(contentsWrapper, "contents.-1", []byte(value.Raw))

				// Create a new group for tracking responses
				group := &FunctionCallGroup{
					ResponsesNeeded: len(callNames),
					CallNames:       callNames,
				}
				pendingGroups = append(pendingGroups, group)
			} else {
				// Regular model content without function calls
				if !value.IsObject() {
					log.Warnf("failed to parse content")
					return true
				}
				contentsWrapper, _ = sjson.SetRawBytes(contentsWrapper, "contents.-1", []byte(value.Raw))
			}
		} else {
			// Non-model content (user, etc.)
			if !value.IsObject() {
				log.Warnf("failed to parse content")
				return true
			}
			contentsWrapper, _ = sjson.SetRawBytes(contentsWrapper, "contents.-1", []byte(value.Raw))
		}

		return true
	})

	// Handle any remaining pending groups with remaining responses
	for _, group := range pendingGroups {
		if len(collectedResponses) >= group.ResponsesNeeded {
			groupResponses := collectedResponses[:group.ResponsesNeeded]
			collectedResponses = collectedResponses[group.ResponsesNeeded:]

			functionResponseContent := []byte(`{"parts":[],"role":"function"}`)
			for ri, response := range groupResponses {
				if !response.IsObject() {
					log.Warnf("failed to parse function response")
					continue
				}
				raw := backfillFunctionResponseName(response.Raw, group.CallNames[ri])
				functionResponseContent, _ = sjson.SetRawBytes(functionResponseContent, "parts.-1", []byte(raw))
			}

			if gjson.GetBytes(functionResponseContent, "parts.#").Int() > 0 {
				contentsWrapper, _ = sjson.SetRawBytes(contentsWrapper, "contents.-1", functionResponseContent)
			}
		}
	}

	// Update the original JSON with the new contents
	result := []byte(input)
	result, _ = sjson.SetRawBytes(result, "request.contents", []byte(gjson.GetBytes(contentsWrapper, "contents").Raw))

	return string(result), nil
}
