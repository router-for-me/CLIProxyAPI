package pluginhost

import (
	"context"
	"fmt"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
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

type Host struct {
	mu                     sync.Mutex
	loader                 pluginLoader
	loaded                 map[string]*loadedPlugin
	fused                  map[string]string
	runtimeConfig          *config.Config
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
	streams                *streamBridge
	httpStreams            *hostHTTPStreamBridge
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
		streams:                newStreamBridge(),
		httpStreams:            newHostHTTPStreamBridge(),
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

func (h *Host) ApplyConfig(ctx context.Context, cfg *config.Config) {
	if h == nil {
		return
	}

	rc := runtimeConfigFromConfig(cfg)
	h.mu.Lock()
	h.runtimeConfig = cfg

	if !rc.Enabled {
		h.snapshot.Store(emptySnapshot())
		h.mu.Unlock()
		h.refreshThinkingProviders(nil)
		return
	}

	files, errSelect := selectPluginFiles(rc.Dir)
	if errSelect != nil {
		log.Warnf("pluginhost: failed to select plugin files: %v", errSelect)
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
		}

		plugin, okCall := h.callRegisterLocked(ctx, lp, item)
		if !okCall {
			continue
		}
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
	client, errOpen := h.loader.Open(file.Path, h)
	if errOpen != nil {
		return nil, errOpen
	}

	return &loadedPlugin{
		id:     file.ID,
		path:   file.Path,
		client: newGuardedPluginClient(client),
	}, nil
}

// ShutdownAll removes active plugin capabilities and closes all loaded dynamic libraries.
func (h *Host) ShutdownAll() {
	if h == nil {
		return
	}

	clients := make([]pluginClient, 0)
	h.mu.Lock()
	for _, lp := range h.loaded {
		if lp == nil || lp.client == nil {
			continue
		}
		clients = append(clients, lp.client)
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
	h.snapshot.Store(emptySnapshot())
	h.mu.Unlock()

	h.refreshThinkingProviders(nil)
	h.RegisterFrontendAuthProviders()
	for _, client := range clients {
		client.Shutdown()
	}
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
