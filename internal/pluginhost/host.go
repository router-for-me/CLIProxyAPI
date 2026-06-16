package pluginhost

import (
	"context"
	"fmt"
	"runtime/debug"
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
	registered bool
	client     pluginClient
}

type modelExecutor interface {
	ExecuteModel(context.Context, handlers.ModelExecutionRequest) (handlers.ModelExecutionResponse, *interfaces.ErrorMessage)
	ExecuteModelStream(context.Context, handlers.ModelExecutionRequest) (handlers.ModelExecutionStream, *interfaces.ErrorMessage)
}

type pluginUnloadTarget struct {
	id     string
	path   string
	client pluginClient
}

type Host struct {
	mu                     sync.Mutex
	loader                 pluginLoader
	loaded                 map[string]*loadedPlugin
	fused                  map[string]string
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
		loader:                 defaultPluginLoader(),
		loaded:                 make(map[string]*loadedPlugin),
		fused:                  make(map[string]string),
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
	return ok
}

func (h *Host) ApplyConfig(ctx context.Context, cfg *config.Config) {
	if h == nil {
		return
	}

	rc := runtimeConfigFromConfig(cfg)
	h.mu.Lock()
	h.runtimeConfig = cfg

	if !rc.Enabled {
		h.managementRoutes = make(map[string]managementRouteRecord)
		h.resourceRoutes = make(map[string]resourceRouteRecord)
		h.snapshot.Store(emptySnapshot())
		h.mu.Unlock()
		h.refreshThinkingProviders(nil)
		return
	}

	files, errSelect := selectPluginFiles(rc.Dir)
	if errSelect != nil {
		log.Warnf("pluginhost: failed to select plugin files: %v", errSelect)
		h.managementRoutes = make(map[string]managementRouteRecord)
		h.resourceRoutes = make(map[string]resourceRouteRecord)
		h.snapshot.Store(emptySnapshot())
		h.mu.Unlock()
		h.refreshThinkingProviders(nil)
		return
	}

	records := make([]capabilityRecord, 0, len(files))
	for _, file := range files {
		item, ok := rc.Items[file.ID]
		if !ok {
			item = defaultRuntimeItemConfig(file.ID)
		}
		if !item.Enabled {
			continue
		}
		if _, disabled := h.fused[file.ID]; disabled {
			continue
		}

		lp := h.loaded[file.ID]
		if lp == nil {
			loaded, errLoad := h.loadLocked(file)
			if errLoad != nil {
				log.Warnf("pluginhost: failed to load plugin %s from %s: %v", file.ID, file.Path, errLoad)
				continue
			}
			lp = loaded
			h.loaded[file.ID] = lp
			log.WithFields(log.Fields{
				"plugin_id": file.ID,
				"path":      file.Path,
			}).Info("pluginhost: plugin loaded")
		}

		plugin, okCall := h.callRegisterLocked(ctx, lp, item)
		if !okCall {
			continue
		}
		plugin.Metadata = clonePluginMetadata(plugin.Metadata)
		records = append(records, capabilityRecord{
			id:       file.ID,
			priority: item.Priority,
			meta:     plugin.Metadata,
			plugin:   plugin,
		})
	}

	sortRecords(records)
	h.snapshot.Store(&Snapshot{enabled: true, records: records})
	h.mu.Unlock()
	h.refreshThinkingProviders(records)
}

func (h *Host) loadLocked(file pluginFile) (*loadedPlugin, error) {
	client, errOpen := h.loader.Open(file, h)
	if errOpen != nil {
		return nil, errOpen
	}

	return &loadedPlugin{
		id:     file.ID,
		path:   file.Path,
		client: newGuardedPluginClient(client),
	}, nil
}

// UnloadPlugin removes one plugin from the active runtime and closes its dynamic library.
func (h *Host) UnloadPlugin(id string) bool {
	if h == nil {
		return false
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return false
	}

	var target pluginUnloadTarget
	h.mu.Lock()
	lp := h.loaded[id]
	if lp == nil {
		h.mu.Unlock()
		return false
	}
	target = pluginUnloadTarget{id: lp.id, path: lp.path, client: lp.client}
	delete(h.loaded, id)
	delete(h.fused, id)
	records, enabled := h.snapshotWithoutPluginLocked(id)
	h.removePluginRuntimeStateLocked(id)
	h.snapshot.Store(&Snapshot{enabled: enabled, records: records})
	h.mu.Unlock()

	h.refreshThinkingProviders(records)
	h.RegisterFrontendAuthProviders()
	if target.client != nil {
		target.client.Shutdown()
	}
	log.WithFields(log.Fields{
		"plugin_id": target.id,
		"path":      target.path,
	}).Info("pluginhost: plugin unloaded")
	return true
}

// ShutdownAll removes active plugin capabilities and closes all loaded dynamic libraries.
func (h *Host) ShutdownAll() {
	if h == nil {
		return
	}

	targets := make([]pluginUnloadTarget, 0)
	h.mu.Lock()
	for _, lp := range h.loaded {
		if lp == nil || lp.client == nil {
			continue
		}
		targets = append(targets, pluginUnloadTarget{
			id:     lp.id,
			path:   lp.path,
			client: lp.client,
		})
	}
	h.loaded = make(map[string]*loadedPlugin)
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
	h.snapshot.Store(emptySnapshot())
	h.mu.Unlock()

	h.refreshThinkingProviders(nil)
	h.RegisterFrontendAuthProviders()
	for _, target := range targets {
		target.client.Shutdown()
		log.WithFields(log.Fields{
			"plugin_id": target.id,
			"path":      target.path,
		}).Info("pluginhost: plugin unloaded")
	}
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

func (h *Host) callRegisterLocked(ctx context.Context, lp *loadedPlugin, item runtimeItemConfig) (pluginapi.Plugin, bool) {
	if lp == nil {
		return pluginapi.Plugin{}, false
	}

	method := pluginabi.MethodPluginRegister
	if lp.registered {
		method = pluginabi.MethodPluginReconfigure
	}

	plugin, okCall := h.safePluginCallLocked(ctx, lp.id, method, func() pluginapi.Plugin {
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
	lp.registered = true
	if !validPlugin(plugin) {
		log.Warnf("pluginhost: plugin %s returned invalid metadata or no capabilities", lp.id)
		return pluginapi.Plugin{}, false
	}
	return plugin, true
}

func (h *Host) safePluginCallLocked(ctx context.Context, id, method string, fn func() pluginapi.Plugin) (out pluginapi.Plugin, ok bool) {
	defer func() {
		if recovered := recover(); recovered != nil {
			h.fused[id] = fmt.Sprintf("%s panic: %v", method, recovered)
			log.WithField("plugin_id", id).WithField("method", method).Errorf("pluginhost: plugin panic recovered: %v\n%s", recovered, debug.Stack())
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
