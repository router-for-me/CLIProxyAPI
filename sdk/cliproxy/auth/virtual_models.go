package auth

import (
	"strings"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

// virtualModelTable stores the mapping of virtual model name to upstream model.
type virtualModelTable struct {
	// name maps virtual model name (lowercase) to upstream model.
	name map[string]string
}

// compileVirtualModelTable builds a lookup table from virtual model definitions.
func compileVirtualModelTable(models []internalconfig.VirtualModel) *virtualModelTable {
	if len(models) == 0 {
		return &virtualModelTable{}
	}
	out := &virtualModelTable{
		name: make(map[string]string, len(models)),
	}
	for _, vm := range models {
		vname := strings.TrimSpace(vm.Name)
		model := strings.TrimSpace(vm.Model)
		if vname == "" || model == "" {
			continue
		}
		key := strings.ToLower(vname)
		if _, exists := out.name[key]; exists {
			// Skip duplicates (first wins due to sanitization dedup)
			continue
		}
		out.name[key] = model
	}
	if len(out.name) == 0 {
		out.name = nil
	}
	return out
}

// SetVirtualModels updates the virtual model table used during request execution.
// Virtual models are resolved before provider selection and work across all auth types.
func (m *Manager) SetVirtualModels(models []internalconfig.VirtualModel) {
	if m == nil {
		return
	}
	table := compileVirtualModelTable(models)
	m.virtualModels.Store(table)
}

// resolveVirtualModelForRequest resolves the virtual model in a request and returns the
// potentially modified request. This avoids repeating the same resolve block in every Execute* method.
func resolveVirtualModelForRequest(m *Manager, req cliproxyexecutor.Request) cliproxyexecutor.Request {
	if virtualModel := m.ResolveVirtualModel(req.Model); virtualModel != "" {
		req.Model = virtualModel
	}
	return req
}

// ResolveVirtualModel attempts to resolve a requested model name through the virtual model table.
// If the model is a virtual model, it returns the upstream model name.
// If not virtual or resolution fails, it returns an empty string.
func (m *Manager) ResolveVirtualModel(requestedModel string) string {
	if m == nil || requestedModel == "" {
		return ""
	}
	key := strings.ToLower(strings.TrimSpace(requestedModel))
	if key == "" {
		return ""
	}
	raw := m.virtualModels.Load()
	if raw == nil {
		return ""
	}
	table, ok := raw.(*virtualModelTable)
	if !ok || table == nil || table.name == nil {
		return ""
	}
	model, exists := table.name[key]
	if !exists {
		return ""
	}
	return model
}
