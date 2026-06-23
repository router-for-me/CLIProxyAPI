package pluginhost

import (
	"sort"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type capabilityRecord struct {
	id       string
	priority int
	meta     pluginapi.Metadata
	plugin   pluginapi.Plugin
}

type Snapshot struct {
	enabled bool
	records []capabilityRecord
}

// RegisteredPluginInfo describes a plugin that is active in the current runtime snapshot.
type RegisteredPluginInfo struct {
	ID            string
	Priority      int
	Metadata      pluginapi.Metadata
	SupportsOAuth bool
	Menus         []RegisteredPluginMenu
}

// RegisteredPluginMenu describes a plugin-owned resource menu entry.
type RegisteredPluginMenu struct {
	Path        string
	Menu        string
	Description string
}

func emptySnapshot() *Snapshot {
	return &Snapshot{}
}

// RegisteredPlugins returns a stable copy of plugin metadata in the current runtime snapshot.
func (h *Host) RegisteredPlugins() []RegisteredPluginInfo {
	snap := h.Snapshot()
	if snap == nil || len(snap.records) == 0 {
		return nil
	}
	menusByPlugin := h.registeredPluginMenus()
	out := make([]RegisteredPluginInfo, 0, len(snap.records))
	for _, record := range snap.records {
		out = append(out, RegisteredPluginInfo{
			ID:            record.id,
			Priority:      record.priority,
			Metadata:      clonePluginMetadata(record.meta),
			SupportsOAuth: record.plugin.Capabilities.AuthProvider != nil,
			Menus:         menusByPlugin[record.id],
		})
	}
	return out
}

// PluginRegistered reports whether a plugin is active in the current runtime snapshot.
func (h *Host) PluginRegistered(id string) bool {
	if h == nil {
		return false
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return false
	}
	snap := h.Snapshot()
	if snap == nil || len(snap.records) == 0 {
		return false
	}
	for _, record := range snap.records {
		if record.id == id {
			return true
		}
	}
	return false
}

func (h *Host) registeredPluginMenus() map[string][]RegisteredPluginMenu {
	out := make(map[string][]RegisteredPluginMenu)
	if h == nil {
		return out
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, record := range h.resourceRoutes {
		menu := strings.TrimSpace(record.route.Menu)
		if menu == "" {
			continue
		}
		out[record.pluginID] = append(out[record.pluginID], RegisteredPluginMenu{
			Path:        strings.TrimSpace(record.route.Path),
			Menu:        menu,
			Description: strings.TrimSpace(record.route.Description),
		})
	}
	for pluginID := range out {
		sort.SliceStable(out[pluginID], func(i, j int) bool {
			return out[pluginID][i].Path < out[pluginID][j].Path
		})
	}
	return out
}

func sortRecords(records []capabilityRecord) {
	sort.SliceStable(records, func(i, j int) bool {
		if records[i].priority == records[j].priority {
			return records[i].id < records[j].id
		}
		return records[i].priority > records[j].priority
	})
}

func clonePluginMetadata(meta pluginapi.Metadata) pluginapi.Metadata {
	if len(meta.ConfigFields) == 0 {
		return meta
	}
	meta.ConfigFields = cloneConfigFields(meta.ConfigFields)
	return meta
}

func cloneConfigFields(fields []pluginapi.ConfigField) []pluginapi.ConfigField {
	if len(fields) == 0 {
		return nil
	}
	out := make([]pluginapi.ConfigField, len(fields))
	copy(out, fields)
	for index := range out {
		out[index].EnumValues = append([]string(nil), fields[index].EnumValues...)
	}
	return out
}
