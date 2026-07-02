package pluginhost

import (
	"sort"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/plugini18n"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type capabilityRecord struct {
	id       string
	path     string
	version  string
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
	OAuthProvider string
	Menus         []RegisteredPluginMenu
}

// RegisteredPluginMenu describes a plugin-owned resource menu entry.
type RegisteredPluginMenu struct {
	Path        string
	Menu        string
	Description string
	Locales     map[string]pluginapi.RouteLocale
}

func emptySnapshot() *Snapshot {
	return &Snapshot{}
}

func (h *Host) activeRecords() []capabilityRecord {
	return h.activeRecordsFromSnapshot(h.Snapshot())
}

func (h *Host) activeRecordsFromSnapshot(snap *Snapshot) []capabilityRecord {
	if snap == nil || len(snap.records) == 0 {
		return nil
	}
	out := make([]capabilityRecord, 0, len(snap.records))
	for _, record := range snap.records {
		if h.recordCurrent(record) {
			out = append(out, record)
		}
	}
	return out
}

// RegisteredPlugins returns a stable copy of plugin metadata in the current runtime snapshot.
func (h *Host) RegisteredPlugins() []RegisteredPluginInfo {
	records := h.activeRecords()
	if len(records) == 0 {
		return nil
	}
	menusByPlugin := h.registeredPluginMenus()
	out := make([]RegisteredPluginInfo, 0, len(records))
	for _, record := range records {
		authProvider := record.plugin.Capabilities.AuthProvider
		oauthProvider := ""
		if authProvider != nil && !h.isPluginFused(record.id) {
			if identifier, okIdentifier := h.callAuthProviderIdentifier(record.id, authProvider); okIdentifier {
				oauthProvider = identifier
			}
		}
		out = append(out, RegisteredPluginInfo{
			ID:            record.id,
			Priority:      record.priority,
			Metadata:      clonePluginMetadata(record.meta),
			SupportsOAuth: authProvider != nil,
			OAuthProvider: oauthProvider,
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
	for _, record := range h.activeRecords() {
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
			Locales:     cloneRouteLocales(record.route.Locales),
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
	meta.Locales = cloneMetadataLocales(meta.Locales)
	meta.ConfigFields = cloneConfigFields(meta.ConfigFields)
	return meta
}

func cloneConfigFields(fields []pluginapi.ConfigField) []pluginapi.ConfigField {
	if fields == nil {
		return nil
	}
	out := make([]pluginapi.ConfigField, len(fields))
	copy(out, fields)
	for index := range out {
		out[index].EnumValues = append([]string(nil), fields[index].EnumValues...)
		out[index].Locales = cloneConfigFieldLocales(fields[index].Locales)
	}
	return out
}

func cloneMetadataLocales(locales map[string]pluginapi.MetadataLocale) map[string]pluginapi.MetadataLocale {
	return cloneLocaleMap(locales)
}

func cloneConfigFieldLocales(locales map[string]pluginapi.ConfigFieldLocale) map[string]pluginapi.ConfigFieldLocale {
	return cloneLocaleMap(locales)
}

func cloneRouteLocales(locales map[string]pluginapi.RouteLocale) map[string]pluginapi.RouteLocale {
	return cloneLocaleMap(locales)
}

func cloneLocaleMap[T any](locales map[string]T) map[string]T {
	if len(locales) == 0 {
		return nil
	}
	keys := make([]string, 0, len(locales))
	for locale := range locales {
		keys = append(keys, locale)
	}
	sort.SliceStable(keys, func(i, j int) bool {
		left := normalizedHostLocaleKey(keys[i])
		right := normalizedHostLocaleKey(keys[j])
		if left == right {
			return keys[i] < keys[j]
		}
		return left < right
	})
	out := make(map[string]T, len(locales))
	for _, rawLocale := range keys {
		locale := normalizedHostLocaleKey(rawLocale)
		if locale == "" {
			continue
		}
		if _, exists := out[locale]; exists {
			continue
		}
		out[locale] = locales[rawLocale]
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func normalizedHostLocaleKey(locale string) string {
	return strings.ToLower(plugini18n.NormalizeLocale(locale))
}
