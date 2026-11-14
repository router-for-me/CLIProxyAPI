package registry

import (
	"sort"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/modelrouting"
)

// GetOpenCodeModels returns the OpenCode model definitions for the gateway
// ("zen" or "go"), materialized from the embedded route table.
func GetOpenCodeModels(gateway string) []*ModelInfo {
	key := "zen"
	if strings.EqualFold(gateway, "go") {
		key = "go"
	}
	out := openCodeModelsFromRoutes(key)
	for _, info := range out {
		declareOpenCodeProtocols(key, info)
	}
	return out
}

// openCodeWireProtocols maps the gateway route-table protocol segment to the
// wire protocol the OpenCode executor speaks for it.
var openCodeWireProtocols = map[string]string{
	"messages":  modelrouting.ProtocolAnthropicMessages,
	"responses": modelrouting.ProtocolOpenAIResponses,
	"chat":      modelrouting.ProtocolOpenAIChat,
}

// declareOpenCodeProtocols records the wire protocol the executor resolves for
// the model (ResolveOpenCodeProtocol), so its route is executable in the model
// routing inventory. A model whose gateway segment has no wire protocol keeps
// none and is never offered as a routable candidate.
func declareOpenCodeProtocols(gateway string, info *ModelInfo) {
	if info == nil || len(info.Protocols) > 0 {
		return
	}
	if protocol, ok := openCodeWireProtocols[ResolveOpenCodeProtocol(gateway, info.ID)]; ok {
		info.Protocols = []string{protocol}
	}
}

// openCodeRoutesModelInfo builds a ModelInfo from an embedded OpenCode route-table
// entry (opencode_routes.json). The route table only carries ID -> protocol, so
// capability fields are populated with the OpenCode-wide defaults.
func openCodeRoutesModelInfo(gateway, modelID, protocol string) *ModelInfo {
	info := &ModelInfo{
		ID:                        modelID,
		Object:                    "model",
		OwnedBy:                   "opencode",
		Type:                      "opencode",
		DisplayName:               modelID,
		Description:               "OpenCode " + gateway + " gateway — " + modelID,
		ContextLength:             500000,
		MaxCompletionTokens:       65536,
		SupportedInputModalities:  []string{"text"},
		SupportedOutputModalities: []string{"text"},
		Thinking: &ThinkingSupport{
			Levels: []string{"low", "medium", "high", "xhigh"},
		},
	}
	if protocol == "gemini" {
		info.SupportedEndpoints = []string{"/v1/models/" + modelID}
	} else {
		info.SupportedEndpoints = []string{OpenCodeModelPath(gateway, modelID)}
	}
	return info
}

// openCodeModelsFromRoutes materializes ModelInfo entries from the embedded
// opencode_routes.json route table for the given gateway.
func openCodeModelsFromRoutes(gateway string) []*ModelInfo {
	routes, ok := openCodeRoutes[gateway]
	if !ok || len(routes) == 0 {
		return nil
	}
	out := make([]*ModelInfo, 0, len(routes))
	for id, protocol := range routes {
		out = append(out, openCodeRoutesModelInfo(gateway, id, protocol))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
