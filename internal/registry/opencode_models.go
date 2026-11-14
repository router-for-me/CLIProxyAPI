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

// openCodeRoutesModelInfo declares only facts present in the route table.
// Limits and reasoning capabilities require independent catalog evidence.
func openCodeRoutesModelInfo(gateway, modelID string) *ModelInfo {
	return &ModelInfo{
		ID:          modelID,
		Object:      "model",
		OwnedBy:     "opencode",
		Type:        "opencode",
		DisplayName: modelID,
		Description: "OpenCode " + gateway + " gateway — " + modelID,
	}
}

// openCodeModelsFromRoutes materializes ModelInfo entries from the embedded
// opencode_routes.json route table for the given gateway.
func openCodeModelsFromRoutes(gateway string) []*ModelInfo {
	routes, ok := openCodeRoutes[gateway]
	if !ok || len(routes) == 0 {
		return nil
	}
	out := make([]*ModelInfo, 0, len(routes))
	for id := range routes {
		out = append(out, openCodeRoutesModelInfo(gateway, id))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
