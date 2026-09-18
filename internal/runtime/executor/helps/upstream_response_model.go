package helps

import (
	"context"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	"github.com/tidwall/gjson"
)

var openAITerminalEventTypes = map[string]struct{}{
	"response.completed":  {},
	"response.done":       {},
	"response.failed":     {},
	"response.incomplete": {},
	"response.cancelled":  {},
	"response.canceled":   {},
}

// ObserveUpstreamResponseBytes extracts an upstream-declared model from one payload chunk.
func ObserveUpstreamResponseBytes(ctx context.Context, payload []byte) {
	if len(payload) == 0 {
		return
	}
	line := strings.TrimSpace(string(payload))
	if line == "" {
		return
	}
	if rest, ok := strings.CutPrefix(line, "event:"); ok {
		usage.RememberUpstreamResponseEventType(ctx, strings.TrimSpace(rest))
		return
	}
	if rest, ok := strings.CutPrefix(line, "data:"); ok {
		line = strings.TrimSpace(rest)
	}
	if line == "" {
		return
	}
	data := []byte(line)
	family, bound := usage.UpstreamResponseModelFamilyFromContext(ctx)
	if !bound {
		family = sniffUpstreamResponseModelFamily(data)
		usage.BindUpstreamResponseModelFamily(ctx, family)
	}
	model, terminal, found := extractUpstreamResponseModel(data, family, usage.LastUpstreamResponseEventType(ctx))
	if !found {
		return
	}
	if !gjson.ValidBytes(data) {
		return
	}
	usage.ObserveUpstreamResponseModel(ctx, model, terminal)
}

func sniffUpstreamResponseModelFamily(data []byte) usage.ExtractionFamily {
	if firstNonEmptyJSONString(data, "modelVersion", "response.modelVersion", "response.response.modelVersion") != "" {
		return usage.FamilyGemini
	}
	if firstNonEmptyJSONString(data, "message.model") != "" {
		return usage.FamilyClaude
	}
	return usage.FamilyOpenAI
}

func extractUpstreamResponseModel(data []byte, family usage.ExtractionFamily, lastEvent string) (string, bool, bool) {
	switch family {
	case usage.FamilyClaude:
		model := firstNonEmptyJSONString(data, "message.model", "model")
		return model, false, model != ""
	case usage.FamilyGemini:
		model := firstNonEmptyJSONString(data, "modelVersion", "response.modelVersion", "response.response.modelVersion")
		return model, true, model != ""
	default:
		model := firstNonEmptyJSONString(data, "response.model", "model")
		eventType := strings.TrimSpace(gjson.GetBytes(data, "type").String())
		if eventType == "" {
			eventType = lastEvent
		}
		_, terminal := openAITerminalEventTypes[eventType]
		return model, terminal, model != ""
	}
}

func firstNonEmptyJSONString(data []byte, paths ...string) string {
	for _, path := range paths {
		if value := strings.TrimSpace(gjson.GetBytes(data, path).String()); value != "" {
			return value
		}
	}
	return ""
}
