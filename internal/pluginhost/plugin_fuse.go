package pluginhost

import (
	"fmt"
	"runtime/debug"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	log "github.com/sirupsen/logrus"
)

func (h *Host) fusePlugin(record capabilityRecord, method string, recovered any) {
	h.fusePluginIdentity(record.id, record.path, record.version, method, recovered)
}

func (h *Host) fusePluginIdentity(id, path, version, method string, recovered any) {
	if h == nil {
		return
	}
	reason := fmt.Sprintf("%s panic: %v", method, recovered)
	h.mu.Lock()
	fuseID := h.pluginIdentityCurrentLocked(id, path, version)
	h.recordPluginFuseLocked(id, path, version, reason, fuseID)
	h.mu.Unlock()
	thinking.UnregisterPluginProvidersGeneration(id, pluginIdentityGeneration(path, version))
	log.WithField("plugin_id", id).WithField("method", method).Errorf("pluginhost: plugin panic recovered: %v\n%s", recovered, debug.Stack())
}

// fuseLoadingPluginIdentity uses the identity being registered because it may
// not be present in h.loaded yet during initial load or hot reload.
func (h *Host) fuseLoadingPluginIdentity(id, path, version, method string, recovered any) {
	if h == nil {
		return
	}
	reason := fmt.Sprintf("%s panic: %v", method, recovered)
	h.mu.Lock()
	fuseID := h.activePluginPaths[id] == "" || h.pluginIdentityCurrentLocked(id, path, version)
	if loaded := h.loaded[id]; loaded != nil {
		fuseID = makePluginIdentityKey(id, loaded.path, loaded.version) == makePluginIdentityKey(id, path, version)
	}
	h.recordPluginFuseLocked(id, path, version, reason, fuseID)
	h.mu.Unlock()
	thinking.UnregisterPluginProvidersGeneration(id, pluginIdentityGeneration(path, version))
	log.WithField("plugin_id", id).WithField("method", method).Errorf("pluginhost: plugin panic recovered: %v\n%s", recovered, debug.Stack())
}

func (h *Host) recordPluginFuseLocked(id, path, version, reason string, fuseID bool) {
	if fuseID {
		h.fused[id] = reason
	}
	identityKey := makePluginIdentityKey(id, path, version)
	if identityKey.path != "" {
		h.nextFuseEpoch++
		h.fusedIdentities[identityKey] = h.nextFuseEpoch
	}
}

func pluginIdentityGeneration(path, version string) string {
	identity := makePluginIdentityKey("", path, version)
	return identity.path + "\x00" + identity.version
}
