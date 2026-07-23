package pluginhost

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	log "github.com/sirupsen/logrus"
)

type loadedPlugin struct {
	id         string
	path       string
	version    string
	name       string
	registered bool
	client     pluginClient
}

type modelExecutor interface {
	ExecuteModel(context.Context, handlers.ModelExecutionRequest) (handlers.ModelExecutionResponse, *interfaces.ErrorMessage)
	ExecuteModelStream(context.Context, handlers.ModelExecutionRequest) (handlers.ModelExecutionStream, *interfaces.ErrorMessage)
}

type pluginUnloadTarget struct {
	id      string
	name    string
	path    string
	version string
	client  pluginClient
}

type pluginLoadRequest struct {
	result         chan pluginLoadResult
	cleanupStarted bool
}

type pluginLoadResult struct {
	loaded      *loadedPlugin
	plugin      pluginapi.Plugin
	initialized bool
	err         error
}

type Host struct {
	applyMu                chan struct{}
	mu                     sync.Mutex
	loader                 pluginLoader
	loaded                 map[string]*loadedPlugin
	retired                map[string][]*loadedPlugin
	loading                map[string]*pluginLoadRequest
	fused                  map[string]string
	pluginFileVersions     map[string]string
	activePluginVersions   map[string]string
	activePluginPaths      map[string]string
	cleanupFilesPending    bool
	runtimeConfig          *config.Config
	authManager            *coreauth.Manager
	modelExecutor          modelExecutor
	modelClientIDs         map[string]struct{}
	executorModelClientIDs map[string]struct{}
	modelProviders         map[string]string
	modelRegistrations     map[string]pluginModelRegistration
	providerModels         map[string][]*registryModelInfo
	executorProviders      map[string]struct{}
	accessProviderKeys     map[string]struct{}
	commandLineFlags       map[string]commandLineFlagRecord
	commandLineHits        map[string]struct{}
	managementRoutes       map[string]managementRouteRecord
	resourceRoutes         map[string]resourceRouteRecord
	streams                *streamBridge
	httpStreams            *hostHTTPStreamBridge
	modelStreams           *modelStreamBridge
	callbackContexts       *callbackContextRegistry
	snapshot               atomic.Value
}

func New() *Host {
	h := &Host{
		applyMu:                make(chan struct{}, 1),
		loader:                 defaultPluginLoader(),
		loaded:                 make(map[string]*loadedPlugin),
		retired:                make(map[string][]*loadedPlugin),
		loading:                make(map[string]*pluginLoadRequest),
		fused:                  make(map[string]string),
		pluginFileVersions:     make(map[string]string),
		activePluginVersions:   make(map[string]string),
		activePluginPaths:      make(map[string]string),
		cleanupFilesPending:    true,
		modelClientIDs:         make(map[string]struct{}),
		executorModelClientIDs: make(map[string]struct{}),
		modelProviders:         make(map[string]string),
		modelRegistrations:     make(map[string]pluginModelRegistration),
		providerModels:         make(map[string][]*registryModelInfo),
		executorProviders:      make(map[string]struct{}),
		accessProviderKeys:     make(map[string]struct{}),
		commandLineFlags:       make(map[string]commandLineFlagRecord),
		commandLineHits:        make(map[string]struct{}),
		managementRoutes:       make(map[string]managementRouteRecord),
		resourceRoutes:         make(map[string]resourceRouteRecord),
		streams:                newStreamBridge(),
		httpStreams:            newHostHTTPStreamBridge(),
		modelStreams:           newModelStreamBridge(),
		callbackContexts:       newCallbackContextRegistry(),
	}
	h.snapshot.Store(emptySnapshot())
	return h
}

func NewForTest(loader pluginLoader) *Host {
	h := New()
	h.loader = loader
	return h
}

func (h *Host) SetModelExecutor(executor modelExecutor) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.modelExecutor = executor
	h.mu.Unlock()
}

func (h *Host) currentModelExecutor() modelExecutor {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	executor := h.modelExecutor
	h.mu.Unlock()
	return executor
}

func (h *Host) Snapshot() *Snapshot {
	if h == nil {
		return emptySnapshot()
	}
	raw := h.snapshot.Load()
	if snap, ok := raw.(*Snapshot); ok && snap != nil {
		return snap
	}
	return emptySnapshot()
}

// PluginLoaded reports whether a plugin dynamic library is still loaded by the host.
func (h *Host) PluginLoaded(id string) bool {
	if h == nil {
		return false
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	_, ok := h.loaded[id]
	if ok {
		return true
	}
	return len(h.retired[id]) > 0
}

// PluginBusy reports whether a plugin dynamic library is loaded or being loaded.
func (h *Host) PluginBusy(id string) bool {
	if h == nil {
		return false
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.loaded[id]; ok {
		return true
	}
	if len(h.retired[id]) > 0 {
		return true
	}
	_, ok := h.loading[id]
	return ok
}

func (h *Host) ApplyConfig(ctx context.Context, cfg *config.Config) {
	if h == nil || !h.lockApply(ctx) {
		return
	}
	defer h.unlockApply()
	if ctx == nil {
		ctx = context.Background()
	}
	if errContext := ctx.Err(); errContext != nil {
		return
	}

	rc, errRuntimeConfig := runtimeConfigFromConfig(cfg)
	if errRuntimeConfig != nil {
		log.WithError(errRuntimeConfig).Error("failed to apply plugin runtime config")
		return
	}
	h.mu.Lock()
	h.runtimeConfig = cfg
	h.mu.Unlock()

	if !rc.Enabled {
		h.mu.Lock()
		h.managementRoutes = make(map[string]managementRouteRecord)
		h.resourceRoutes = make(map[string]resourceRouteRecord)
		h.rebuildActivePluginMapsLocked(nil)
		h.snapshot.Store(emptySnapshot())
		h.mu.Unlock()
		h.refreshThinkingProviders(nil)
		return
	}

	desiredVersions := desiredPluginVersions(rc.Items)
	files, errSelect := selectPluginFiles(rc.Dir, desiredVersions)
	if errSelect != nil {
		log.Warnf("pluginhost: failed to select plugin files: %v", errSelect)
		h.mu.Lock()
		h.managementRoutes = make(map[string]managementRouteRecord)
		h.resourceRoutes = make(map[string]resourceRouteRecord)
		h.rebuildActivePluginMapsLocked(nil)
		h.snapshot.Store(emptySnapshot())
		h.mu.Unlock()
		h.refreshThinkingProviders(nil)
		return
	}
	files = h.withLoadedPluginFallbacks(files, rc.Items, desiredVersions)

	records := make([]capabilityRecord, 0, len(files))
	loadedFiles := make([]pluginFile, 0, len(files))
	hotReloadLogs := make([]log.Fields, 0)
	for _, file := range files {
		item, ok := rc.Items[file.ID]
		if !ok {
			item = defaultRuntimeItemConfig(file.ID)
		}
		if !item.Enabled {
			continue
		}
		h.mu.Lock()
		lp := h.loaded[file.ID]
		var replaced *loadedPlugin
		if lp != nil && cleanPluginPath(lp.path) != cleanPluginPath(file.Path) {
			replaced = lp
			lp = nil
		}
		_, disabled := h.fused[file.ID]
		h.mu.Unlock()
		if disabled && replaced == nil {
			continue
		}

		loadedNow := false
		var hotReloadFields log.Fields
		var plugin pluginapi.Plugin
		registeredNow := false
		if lp == nil {
			request := &pluginLoadRequest{result: make(chan pluginLoadResult, 1)}
			h.mu.Lock()
			if _, loading := h.loading[file.ID]; loading {
				h.mu.Unlock()
				continue
			}
			h.loading[file.ID] = request
			h.mu.Unlock()
			h.startPluginLoad(ctx, file, item, request)

			loadResult, completed := h.waitForPluginLoad(ctx, file.ID, request)
			if !completed {
				return
			}
			if loadResult.err != nil {
				h.cleanupPluginLoad(file.ID, request, loadResult.loaded)
				log.Warnf("pluginhost: failed to load plugin %s from %s: %v", file.ID, file.Path, loadResult.err)
				continue
			}

			h.mu.Lock()
			if h.loading[file.ID] != request {
				h.mu.Unlock()
				h.discardLoadedPlugin(loadResult.loaded)
				return
			}
			if errContext := ctx.Err(); errContext != nil {
				h.mu.Unlock()
				h.cleanupPluginLoad(file.ID, request, loadResult.loaded)
				return
			}
			delete(h.loading, file.ID)
			lp = loadResult.loaded
			if replaced != nil {
				hotReloadFields = pluginHotReloadLogFields(file.ID, file.Version, file.Path, replaced.version, replaced.path)
				h.retireLoadedPluginLocked(replaced)
				delete(h.fused, file.ID)
				h.removePluginRuntimeStateLocked(file.ID)
			}
			h.loaded[file.ID] = lp
			loadedNow = true
			plugin = loadResult.plugin
			registeredNow = loadResult.initialized
			h.mu.Unlock()
			log.WithFields(pluginLogFields(file.ID, "", file.Version, file.Path)).Info("pluginhost: plugin loaded")
		}

		if !registeredNow {
			if loadedNow {
				continue
			}
			var okCall bool
			plugin, okCall = h.callRegister(ctx, lp, item)
			if !okCall {
				continue
			}
		}
		plugin.Metadata = clonePluginMetadata(plugin.Metadata)
		h.mu.Lock()
		if lp != nil {
			lp.name = strings.TrimSpace(plugin.Metadata.Name)
			if strings.TrimSpace(lp.version) == "" {
				lp.version = strings.TrimSpace(plugin.Metadata.Version)
			}
		}
		h.mu.Unlock()
		if loadedNow {
			log.WithFields(pluginLogFieldsFromMetadata(file.ID, plugin.Metadata, file.Path)).Info("pluginhost: plugin registered")
		}
		if hotReloadFields != nil {
			hotReloadLogs = append(hotReloadLogs, hotReloadFields)
		}
		records = append(records, capabilityRecord{
			id:       file.ID,
			path:     file.Path,
			version:  file.Version,
			priority: item.Priority,
			meta:     plugin.Metadata,
			plugin:   plugin,
		})
		loadedFiles = append(loadedFiles, file)
	}

	sortRecords(records)
	h.mu.Lock()
	cleanupFiles := h.cleanupFilesPending
	if len(loadedFiles) > 0 {
		h.cleanupFilesPending = false
	}
	h.rebuildActivePluginMapsLocked(records)
	h.snapshot.Store(&Snapshot{enabled: true, records: records})
	h.mu.Unlock()
	h.refreshThinkingProviders(records)
	for _, fields := range hotReloadLogs {
		log.WithFields(fields).Info("pluginhost: plugin hot reloaded")
	}
	if cleanupFiles && len(loadedFiles) > 0 {
		if errCleanup := cleanupUnselectedPluginFiles(rc.Dir, loadedFiles); errCleanup != nil {
			log.Warnf("pluginhost: failed to clean old plugin files: %v", errCleanup)
		}
	}
}

func (h *Host) startPluginLoad(ctx context.Context, file pluginFile, item runtimeItemConfig, request *pluginLoadRequest) {
	if h == nil || request == nil || request.result == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	go func() {
		client, errOpen := h.loader.Open(file, h)
		if errOpen != nil {
			request.result <- pluginLoadResult{err: errOpen}
			return
		}
		if client == nil {
			request.result <- pluginLoadResult{err: fmt.Errorf("plugin loader returned nil client")}
			return
		}
		loaded := &loadedPlugin{
			id:      file.ID,
			path:    file.Path,
			version: file.Version,
			client:  newGuardedPluginClient(client),
		}
		plugin, okCall := h.callRegister(ctx, loaded, item)
		request.result <- pluginLoadResult{loaded: loaded, plugin: plugin, initialized: okCall}
	}()
}

func (h *Host) waitForPluginLoad(ctx context.Context, id string, request *pluginLoadRequest) (pluginLoadResult, bool) {
	if h == nil || request == nil || request.result == nil {
		return pluginLoadResult{}, false
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case result := <-request.result:
		return result, true
	case <-ctx.Done():
		h.cleanupCanceledPluginLoad(id, request)
		return pluginLoadResult{}, false
	}
}

func (h *Host) cleanupCanceledPluginLoad(id string, request *pluginLoadRequest) {
	if h == nil || request == nil || request.result == nil {
		return
	}
	h.mu.Lock()
	if h.loading[id] != request || request.cleanupStarted {
		h.mu.Unlock()
		return
	}
	request.cleanupStarted = true
	h.mu.Unlock()

	go func() {
		result := <-request.result
		h.finishPluginLoadCleanup(id, request, result.loaded)
	}()
}

// cleanupPluginLoad retains the matching load token until the client has physically
// shut down, preventing a replacement ApplyConfig from opening a second client.
func (h *Host) cleanupPluginLoad(id string, request *pluginLoadRequest, loaded *loadedPlugin) {
	if h == nil || request == nil {
		return
	}
	h.mu.Lock()
	if h.loading[id] != request || request.cleanupStarted {
		h.mu.Unlock()
		return
	}
	request.cleanupStarted = true
	h.mu.Unlock()

	h.finishPluginLoadCleanup(id, request, loaded)
}

func (h *Host) finishPluginLoadCleanup(id string, request *pluginLoadRequest, loaded *loadedPlugin) {
	go func() {
		h.discardLoadedPlugin(loaded)
		h.clearLoadingRequest(id, request)
	}()
}

func (h *Host) clearLoadingRequest(id string, request *pluginLoadRequest) {
	if h == nil || request == nil {
		return
	}
	h.mu.Lock()
	if h.loading[id] == request {
		delete(h.loading, id)
	}
	h.mu.Unlock()
}

func (h *Host) discardLoadedPlugin(loaded *loadedPlugin) {
	if loaded == nil || loaded.client == nil {
		return
	}
	shutdownPluginClient(context.Background(), loaded.client)
}

func (h *Host) withLoadedPluginFallbacks(files []pluginFile, items map[string]runtimeItemConfig, desired map[string]string) []pluginFile {
	if h == nil || len(desired) == 0 {
		return files
	}
	selected := make(map[string]struct{}, len(files))
	for _, file := range files {
		id := strings.TrimSpace(file.ID)
		if id != "" {
			selected[id] = struct{}{}
		}
	}
	ids := make([]string, 0, len(desired))
	for id := range desired {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	h.mu.Lock()
	defer h.mu.Unlock()
	for _, id := range ids {
		if _, ok := selected[id]; ok {
			continue
		}
		if item, ok := items[id]; ok && !item.Enabled {
			continue
		}
		lp := h.loaded[id]
		if lp == nil || strings.TrimSpace(lp.path) == "" {
			continue
		}
		files = append(files, pluginFile{
			ID:      id,
			Path:    lp.path,
			Version: strings.TrimSpace(lp.version),
		})
		selected[id] = struct{}{}
	}
	return files
}

// UnloadPlugin removes one plugin from the active runtime and closes its dynamic library.
func (h *Host) UnloadPlugin(id string) bool {
	return h.UnloadPluginContext(context.Background(), id)
}

// UnloadPluginContext detaches a plugin from the runtime before waiting for its
// active calls. Physical client cleanup continues after cancellation if needed.
func (h *Host) UnloadPluginContext(ctx context.Context, id string) bool {
	if h == nil {
		return false
	}
	id = strings.TrimSpace(id)
	if id == "" || !h.lockApply(ctx) {
		return false
	}
	defer h.unlockApply()

	targets := make([]pluginUnloadTarget, 0)
	h.mu.Lock()
	lp := h.loaded[id]
	if lp != nil {
		targets = append(targets, pluginUnloadTarget{id: lp.id, name: lp.name, path: lp.path, version: lp.version, client: lp.client})
	}
	for _, retired := range h.retired[id] {
		if retired == nil {
			continue
		}
		targets = append(targets, pluginUnloadTarget{id: retired.id, name: retired.name, path: retired.path, version: retired.version, client: retired.client})
	}
	if len(targets) == 0 {
		h.mu.Unlock()
		return false
	}
	delete(h.loaded, id)
	delete(h.retired, id)
	delete(h.fused, id)
	delete(h.activePluginVersions, id)
	delete(h.activePluginPaths, id)
	for _, target := range targets {
		delete(h.pluginFileVersions, cleanPluginPath(target.path))
	}
	records, enabled := h.snapshotWithoutPluginLocked(id)
	h.removePluginRuntimeStateLocked(id)
	h.snapshot.Store(&Snapshot{enabled: enabled, records: records})
	h.mu.Unlock()

	h.refreshThinkingProviders(records)
	h.RegisterFrontendAuthProviders()
	for _, target := range targets {
		if target.client != nil {
			shutdownPluginClient(ctx, target.client)
		}
		log.WithFields(pluginLogFields(target.id, target.name, target.version, target.path)).Info("pluginhost: plugin unloaded")
	}
	return true
}

// ShutdownAll removes active plugin capabilities and closes all loaded dynamic libraries.
func (h *Host) ShutdownAll() {
	h.ShutdownAllContext(context.Background())
}

// ShutdownAllContext detaches all plugin runtime state without waiting beyond ctx
// for active plugin calls to complete.
func (h *Host) ShutdownAllContext(ctx context.Context) {
	if h == nil || !h.lockApply(ctx) {
		return
	}
	defer h.unlockApply()

	targets := make([]pluginUnloadTarget, 0)
	var loading map[string]*pluginLoadRequest
	h.mu.Lock()
	loading = make(map[string]*pluginLoadRequest, len(h.loading))
	for id, request := range h.loading {
		loading[id] = request
	}
	for _, lp := range h.loaded {
		if lp == nil || lp.client == nil {
			continue
		}
		targets = append(targets, pluginUnloadTarget{
			id:      lp.id,
			name:    lp.name,
			path:    lp.path,
			version: lp.version,
			client:  lp.client,
		})
	}
	for _, retiredPlugins := range h.retired {
		for _, lp := range retiredPlugins {
			if lp == nil || lp.client == nil {
				continue
			}
			targets = append(targets, pluginUnloadTarget{
				id:      lp.id,
				name:    lp.name,
				path:    lp.path,
				version: lp.version,
				client:  lp.client,
			})
		}
	}
	h.loaded = make(map[string]*loadedPlugin)
	h.retired = make(map[string][]*loadedPlugin)
	h.modelClientIDs = make(map[string]struct{})
	h.executorModelClientIDs = make(map[string]struct{})
	h.modelProviders = make(map[string]string)
	h.modelRegistrations = make(map[string]pluginModelRegistration)
	h.providerModels = make(map[string][]*registryModelInfo)
	h.executorProviders = make(map[string]struct{})
	h.commandLineFlags = make(map[string]commandLineFlagRecord)
	h.commandLineHits = make(map[string]struct{})
	h.managementRoutes = make(map[string]managementRouteRecord)
	h.resourceRoutes = make(map[string]resourceRouteRecord)
	h.pluginFileVersions = make(map[string]string)
	h.activePluginVersions = make(map[string]string)
	h.activePluginPaths = make(map[string]string)
	h.snapshot.Store(emptySnapshot())
	h.mu.Unlock()

	h.refreshThinkingProviders(nil)
	h.RegisterFrontendAuthProviders()
	for id, request := range loading {
		h.cleanupCanceledPluginLoad(id, request)
	}
	for _, target := range targets {
		shutdownPluginClient(ctx, target.client)
		log.WithFields(pluginLogFields(target.id, target.name, target.version, target.path)).Info("pluginhost: plugin unloaded")
	}
}

func (h *Host) lockApply(ctx context.Context) bool {
	if h == nil {
		return false
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case h.applyMu <- struct{}{}:
		return true
	default:
	}
	select {
	case h.applyMu <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
	}
}

func (h *Host) unlockApply() {
	<-h.applyMu
}

func shutdownPluginClient(ctx context.Context, client pluginClient) {
	if client == nil {
		return
	}
	if guarded, ok := client.(*guardedPluginClient); ok {
		guarded.ShutdownContext(ctx)
		return
	}
	client.Shutdown()
}

func cleanPluginPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	return filepath.Clean(path)
}

func (h *Host) retireLoadedPluginLocked(lp *loadedPlugin) {
	if h == nil || lp == nil {
		return
	}
	h.retired[lp.id] = append(h.retired[lp.id], lp)
}

func (h *Host) recordCurrent(record capabilityRecord) bool {
	return h.pluginIdentityCurrent(record.id, record.path, record.version)
}

func (h *Host) pluginIdentityCurrent(id string, path string, version string) bool {
	if h == nil {
		return false
	}
	version = strings.TrimSpace(version)
	h.mu.Lock()
	defer h.mu.Unlock()
	id = strings.TrimSpace(id)
	if id == "" {
		return false
	}
	path = cleanPluginPath(path)
	if path == "" || h.activePluginPaths[id] != path {
		return false
	}
	activePathVersion, okVersion := h.pluginFileVersions[path]
	if !okVersion || activePathVersion != version {
		return false
	}
	return h.activePluginVersions[id] == version
}

func (h *Host) snapshotWithoutPluginLocked(id string) ([]capabilityRecord, bool) {
	raw := h.snapshot.Load()
	snap, _ := raw.(*Snapshot)
	if snap == nil || len(snap.records) == 0 {
		return nil, snap != nil && snap.enabled
	}
	records := make([]capabilityRecord, 0, len(snap.records))
	for _, record := range snap.records {
		if record.id == id {
			continue
		}
		records = append(records, record)
	}
	return records, snap.enabled
}

func (h *Host) removePluginRuntimeStateLocked(id string) {
	for key, record := range h.managementRoutes {
		if record.pluginID == id {
			delete(h.managementRoutes, key)
		}
	}
	for key, record := range h.resourceRoutes {
		if record.pluginID == id {
			delete(h.resourceRoutes, key)
		}
	}
	for name, record := range h.commandLineFlags {
		if record.pluginID == id {
			delete(h.commandLineFlags, name)
			delete(h.commandLineHits, name)
		}
	}
	if registration, ok := h.modelRegistrations[id]; ok {
		delete(h.providerModels, registration.provider)
	}
	delete(h.modelProviders, id)
	delete(h.modelRegistrations, id)
}

func (h *Host) rebuildActivePluginMapsLocked(records []capabilityRecord) {
	h.pluginFileVersions = make(map[string]string, len(records))
	h.activePluginVersions = make(map[string]string, len(records))
	h.activePluginPaths = make(map[string]string, len(records))
	for _, record := range records {
		id := strings.TrimSpace(record.id)
		path := cleanPluginPath(record.path)
		if id == "" || path == "" {
			continue
		}
		h.pluginFileVersions[path] = strings.TrimSpace(record.version)
		h.activePluginVersions[id] = strings.TrimSpace(record.version)
		h.activePluginPaths[id] = path
	}
}

func (h *Host) callRegister(ctx context.Context, lp *loadedPlugin, item runtimeItemConfig) (pluginapi.Plugin, bool) {
	if lp == nil {
		return pluginapi.Plugin{}, false
	}

	method := pluginabi.MethodPluginRegister
	h.mu.Lock()
	registered := lp.registered
	h.mu.Unlock()
	if registered {
		method = pluginabi.MethodPluginReconfigure
	}

	plugin, okCall := h.safePluginCall(ctx, lp.id, method, func() pluginapi.Plugin {
		plugin, errRegister := registerRPCPlugin(ctx, h, lp.id, lp.client, method, item.ConfigYAML)
		if errRegister != nil {
			log.Warnf("pluginhost: plugin %s %s failed: %v", lp.id, method, errRegister)
			return pluginapi.Plugin{}
		}
		return plugin
	})
	if !okCall {
		return pluginapi.Plugin{}, false
	}
	h.mu.Lock()
	lp.registered = true
	h.mu.Unlock()
	if !validPlugin(plugin) {
		log.Warnf("pluginhost: plugin %s returned invalid metadata or no capabilities", lp.id)
		return pluginapi.Plugin{}, false
	}
	return plugin, true
}

func (h *Host) safePluginCall(ctx context.Context, id, method string, fn func() pluginapi.Plugin) (out pluginapi.Plugin, ok bool) {
	defer func() {
		if recovered := recover(); recovered != nil {
			h.fusePlugin(id, method, recovered)
			out = pluginapi.Plugin{}
			ok = false
		}
	}()

	if ctx != nil {
		select {
		case <-ctx.Done():
			return pluginapi.Plugin{}, false
		default:
		}
	}
	return fn(), true
}

func validPlugin(plugin pluginapi.Plugin) bool {
	if strings.TrimSpace(plugin.Metadata.Name) == "" {
		return false
	}
	if strings.TrimSpace(plugin.Metadata.Version) == "" {
		return false
	}
	if strings.TrimSpace(plugin.Metadata.Author) == "" {
		return false
	}
	if strings.TrimSpace(plugin.Metadata.GitHubRepository) == "" {
		return false
	}
	caps := plugin.Capabilities
	return caps.ModelRegistrar != nil ||
		caps.ModelProvider != nil ||
		caps.AuthProvider != nil ||
		caps.FrontendAuthProvider != nil ||
		caps.Scheduler != nil ||
		caps.ModelRouter != nil ||
		caps.Executor != nil ||
		caps.RequestTranslator != nil ||
		caps.RequestNormalizer != nil ||
		caps.RequestInterceptor != nil ||
		caps.ResponseTranslator != nil ||
		caps.ResponseBeforeTranslator != nil ||
		caps.ResponseAfterTranslator != nil ||
		caps.ResponseInterceptor != nil ||
		caps.StreamChunkInterceptor != nil ||
		caps.ThinkingApplier != nil ||
		caps.UsagePlugin != nil ||
		caps.CommandLinePlugin != nil ||
		caps.ManagementAPI != nil
}

func typeName(v any) string {
	return fmt.Sprintf("%T", v)
}
