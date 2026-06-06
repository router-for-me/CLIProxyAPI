package pluginhost

import (
	"context"
	"fmt"
	"reflect"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	log "github.com/sirupsen/logrus"
)

type registerFunc func([]byte) pluginapi.Plugin

type loadedPlugin struct {
	id          string
	path        string
	registered  bool
	register    registerFunc
	reconfigure registerFunc
}

type Host struct {
	mu                     sync.Mutex
	loader                 symbolLoader
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
	snapshot               atomic.Value
}

func New() *Host {
	h := &Host{
		loader:                 defaultSymbolLoader(),
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
	}
	h.snapshot.Store(emptySnapshot())
	return h
}

func NewForTest(loader symbolLoader) *Host {
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
	lookup, errOpen := h.loader.Open(file.Path)
	if errOpen != nil {
		return nil, errOpen
	}

	rawRegister, errRegister := lookup.Lookup("Register")
	if errRegister != nil {
		return nil, errRegister
	}
	register, okRegister := rawRegister.(func([]byte) pluginapi.Plugin)
	if !okRegister {
		return nil, fmt.Errorf("Register has unsupported signature %s", typeName(rawRegister))
	}

	rawReconfigure, errLookup := lookup.Lookup("Reconfigure")
	if errLookup != nil {
		return nil, fmt.Errorf("Reconfigure lookup failed: %w", errLookup)
	}
	reconfigure, okReconfigure := rawReconfigure.(func([]byte) pluginapi.Plugin)
	if !okReconfigure {
		return nil, fmt.Errorf("Reconfigure has unsupported signature %s", typeName(rawReconfigure))
	}

	return &loadedPlugin{
		id:          file.ID,
		path:        file.Path,
		register:    register,
		reconfigure: reconfigure,
	}, nil
}

func (h *Host) callRegisterLocked(ctx context.Context, lp *loadedPlugin, item runtimeItemConfig) (pluginapi.Plugin, bool) {
	if lp == nil {
		return pluginapi.Plugin{}, false
	}

	method := "Register"
	fn := lp.register
	if lp.registered {
		method = "Reconfigure"
		fn = lp.reconfigure
	}

	plugin, okCall := h.safePluginCallLocked(ctx, lp.id, method, func() pluginapi.Plugin {
		return fn(item.ConfigYAML)
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
		caps.ResponseTranslator != nil ||
		caps.ResponseBeforeTranslator != nil ||
		caps.ResponseAfterTranslator != nil ||
		caps.ThinkingApplier != nil ||
		caps.UsagePlugin != nil ||
		caps.CommandLinePlugin != nil ||
		caps.ManagementAPI != nil
}

func typeName(v any) string {
	if v == nil {
		return "<nil>"
	}
	return reflect.TypeOf(v).String()
}
