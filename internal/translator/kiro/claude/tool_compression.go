// Package claude provides tool compression functionality for Kiro translator.
// This file implements dynamic tool compression to reduce tool payload size
// when it exceeds the target threshold, preventing 500 errors from Kiro API.
package claude

import (
	"encoding/json"
	"unicode/utf8"

	kirocommon "github.com/router-for-me/CLIProxyAPI/v6/internal/translator/kiro/common"
	log "github.com/sirupsen/logrus"
)

// calculateToolsSize calculates the JSON serialized size of the tools list.
// Returns the size in bytes.
func calculateToolsSize(tools []KiroToolWrapper) int {
	if len(tools) == 0 {
		return 0
	}
	data, err := json.Marshal(tools)
	if err != nil {
		log.Warnf("kiro: failed to marshal tools for size calculation: %v", err)
		return 0
	}
	return len(data)
}

// simplifyInputSchema simplifies the input_schema by keeping only essential fields:
// type, enum, required. Recursively processes nested properties.
func simplifyInputSchema(schema interface{}) interface{} {
	if schema == nil {
		return nil
	}

	schemaMap, ok := schema.(map[string]interface{})
	if !ok {
		return schema
	}

	simplified := make(map[string]interface{})

	// Keep essential fields
	if t, ok := schemaMap["type"]; ok {
		simplified["type"] = t
	}
	if enum, ok := schemaMap["enum"]; ok {
		simplified["enum"] = enum
	}
	if required, ok := schemaMap["required"]; ok {
		simplified["required"] = required
	}

	// Recursively process properties
	if properties, ok := schemaMap["properties"].(map[string]interface{}); ok {
		simplifiedProps := make(map[string]interface{})
		for key, value := range properties {
			simplifiedProps[key] = simplifyInputSchema(value)
		}
		simplified["properties"] = simplifiedProps
	}

	// Process items for array types
	if items, ok := schemaMap["items"]; ok {
		simplified["items"] = simplifyInputSchema(items)
	}

	// Process additionalProperties if present
	if additionalProps, ok := schemaMap["additionalProperties"]; ok {
		simplified["additionalProperties"] = simplifyInputSchema(additionalProps)
	}

	// Process anyOf, oneOf, allOf
	for _, key := range []string{"anyOf", "oneOf", "allOf"} {
		if arr, ok := schemaMap[key].([]interface{}); ok {
			simplifiedArr := make([]interface{}, len(arr))
			for i, item := range arr {
				simplifiedArr[i] = simplifyInputSchema(item)
			}
			simplified[key] = simplifiedArr
		}
	}

	return simplified
}

// compressToolDescription compresses a description to the target length.
// Ensures the result is at least MinToolDescriptionLength characters.
// Uses UTF-8 safe truncation.
func compressToolDescription(description string, targetLength int) string {
	if targetLength < kirocommon.MinToolDescriptionLength {
		targetLength = kirocommon.MinToolDescriptionLength
	}

	if len(description) <= targetLength {
		return description
	}

	// Find a safe truncation point (UTF-8 boundary)
	truncLen := targetLength - 3 // Leave room for "..."

	// Ensure we don't cut in the middle of a UTF-8 character
	for truncLen > 0 && !utf8.RuneStart(description[truncLen]) {
		truncLen--
	}

	if truncLen <= 0 {
		return description[:kirocommon.MinToolDescriptionLength]
	}

	return description[:truncLen] + "..."
}

// compressToolsIfNeeded compresses tools if their total size exceeds the target threshold.
// Compression strategy:
// 1. First, check if compression is needed (size > ToolCompressionTargetSize)
// 2. Step 1: Simplify input_schema (keep only type/enum/required)
// 3. Step 2: Proportionally compress descriptions (minimum MinToolDescriptionLength chars)
// Returns the compressed tools list.
func compressToolsIfNeeded(tools []KiroToolWrapper) []KiroToolWrapper {
	if len(tools) == 0 {
		return tools
	}

	originalSize := calculateToolsSize(tools)
	if originalSize <= kirocommon.ToolCompressionTargetSize {
		log.Debugf("kiro: tools size %d bytes is within target %d bytes, no compression needed",
			originalSize, kirocommon.ToolCompressionTargetSize)
		return tools
	}

	log.Infof("kiro: tools size %d bytes exceeds target %d bytes, starting compression",
		originalSize, kirocommon.ToolCompressionTargetSize)

	// Create a copy of tools to avoid modifying the original
	compressedTools := make([]KiroToolWrapper, len(tools))
	for i, tool := range tools {
		compressedTools[i] = KiroToolWrapper{
			ToolSpecification: KiroToolSpecification{
				Name:        tool.ToolSpecification.Name,
				Description: tool.ToolSpecification.Description,
				InputSchema: KiroInputSchema{JSON: tool.ToolSpecification.InputSchema.JSON},
			},
		}
	}

	// Step 1: Simplify input_schema
	for i := range compressedTools {
		compressedTools[i].ToolSpecification.InputSchema.JSON =
			simplifyInputSchema(compressedTools[i].ToolSpecification.InputSchema.JSON)
	}

	sizeAfterSchemaSimplification := calculateToolsSize(compressedTools)
	log.Debugf("kiro: size after schema simplification: %d bytes (reduced by %d bytes)",
		sizeAfterSchemaSimplification, originalSize-sizeAfterSchemaSimplification)

	// Check if we're within target after schema simplification
	if sizeAfterSchemaSimplification <= kirocommon.ToolCompressionTargetSize {
		log.Infof("kiro: compression complete after schema simplification, final size: %d bytes",
			sizeAfterSchemaSimplification)
		return compressedTools
	}

	// Step 2: Compress descriptions proportionally
	sizeToReduce := float64(sizeAfterSchemaSimplification - kirocommon.ToolCompressionTargetSize)
	var totalDescLen float64
	for _, tool := range compressedTools {
		totalDescLen += float64(len(tool.ToolSpecification.Description))
	}

	if totalDescLen > 0 {
		// Assume size reduction comes primarily from descriptions.
		keepRatio := 1.0 - (sizeToReduce / totalDescLen)
		if keepRatio > 1.0 {
			keepRatio = 1.0
		} else if keepRatio < 0 {
			keepRatio = 0
		}

		for i := range compressedTools {
			desc := compressedTools[i].ToolSpecification.Description
			targetLen := int(float64(len(desc)) * keepRatio)
			compressedTools[i].ToolSpecification.Description = compressToolDescription(desc, targetLen)
		}
	}

	finalSize := calculateToolsSize(compressedTools)
	log.Infof("kiro: compression complete, original: %d bytes, final: %d bytes (%.1f%% reduction)",
		originalSize, finalSize, float64(originalSize-finalSize)/float64(originalSize)*100)

	return compressedTools
}
