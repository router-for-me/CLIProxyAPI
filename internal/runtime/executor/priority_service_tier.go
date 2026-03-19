package executor

import (
	"strconv"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	"github.com/tidwall/sjson"
)

func applyPriorityServiceTierCompatibility(payload []byte, metadata map[string]any) []byte {
	if len(payload) == 0 || !priorityServiceTierRequested(metadata) {
		return payload
	}
	updated, errSet := sjson.SetBytes(payload, "service_tier", "priority")
	if errSet != nil {
		return payload
	}
	return updated
}

func priorityServiceTierRequested(metadata map[string]any) bool {
	if len(metadata) == 0 {
		return false
	}
	if requested, ok := metadata[cliproxyexecutor.PriorityServiceTierRequestedMetadataKey]; ok {
		if value, ok := metadataBool(requested); ok {
			return value
		}
	}
	if requestedModel, ok := metadataString(metadata[cliproxyexecutor.OriginalRequestedModelMetadataKey]); ok {
		return util.HasOpenAIFastModeCompatibility(requestedModel)
	}
	return false
}

func metadataBool(value any) (bool, bool) {
	switch typed := value.(type) {
	case bool:
		return typed, true
	case string:
		parsed, errParse := strconv.ParseBool(strings.TrimSpace(typed))
		if errParse == nil {
			return parsed, true
		}
	}
	return false, false
}

func metadataString(value any) (string, bool) {
	switch typed := value.(type) {
	case string:
		trimmed := strings.TrimSpace(typed)
		if trimmed == "" {
			return "", false
		}
		return trimmed, true
	case []byte:
		trimmed := strings.TrimSpace(string(typed))
		if trimmed == "" {
			return "", false
		}
		return trimmed, true
	}
	return "", false
}
