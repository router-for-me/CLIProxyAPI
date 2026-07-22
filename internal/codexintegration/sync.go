package codexintegration

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	log "github.com/sirupsen/logrus"
)

const defaultSyncDebounce = 500 * time.Millisecond

type syncRegistry interface {
	GetAvailableModels(handlerType string) []map[string]any
	GetProvidersForModel(modelID string) []string
	SubscribeHook(hook registry.ModelRegistryHook) func()
}

// SyncStatus reports the latest auto-sync attempt without exposing credentials.
type SyncStatus struct {
	Attempts        uint64
	LastAttempt     time.Time
	LastSuccess     time.Time
	LastError       string
	CatalogRevision string
	Applied         bool
}

// SyncWorker debounces registry and template events into serialized catalog syncs.
type SyncWorker struct {
	lifecycle         *Lifecycle
	registry          syncRegistry
	debounce          time.Duration
	events            chan struct{}
	templateSubscribe func(func(uint64)) func()
	statusMu          sync.RWMutex
	status            SyncStatus
}

// NewSyncWorker creates an auto-sync worker without starting goroutines.
func NewSyncWorker(cfg *config.Config, source syncRegistry) (*SyncWorker, error) {
	if cfg == nil {
		return nil, fmt.Errorf("Codex auto-sync requires config")
	}
	if source == nil {
		return nil, fmt.Errorf("Codex auto-sync requires a model registry")
	}
	snapshot := cfg.CloneForRuntime()
	lifecycle, err := NewLifecycle(snapshot, "")
	if err != nil {
		return nil, err
	}
	return &SyncWorker{
		lifecycle:         lifecycle,
		registry:          source,
		debounce:          defaultSyncDebounce,
		events:            make(chan struct{}, 1),
		templateSubscribe: registry.SubscribeCodexClientModelsChanges,
	}, nil
}

// Run blocks until context cancellation and owns all registry subscriptions.
func (worker *SyncWorker) Run(ctx context.Context) {
	if worker == nil || worker.lifecycle == nil || worker.registry == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	hook := &syncRegistryHook{notify: worker.Notify}
	cancelRegistry := worker.registry.SubscribeHook(hook)
	cancelTemplates := func() {}
	if worker.templateSubscribe != nil {
		cancelTemplates = worker.templateSubscribe(func(uint64) { worker.Notify() })
	}
	defer cancelRegistry()
	defer cancelTemplates()

	worker.Notify()
	timer := time.NewTimer(time.Hour)
	if !timer.Stop() {
		<-timer.C
	}
	defer timer.Stop()
	pending := false
	for {
		select {
		case <-ctx.Done():
			return
		case <-worker.events:
			if pending && !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(worker.debounce)
			pending = true
		case <-timer.C:
			pending = false
			worker.syncOnce()
		}
	}
}

// Notify queues a non-blocking, coalesced sync event.
func (worker *SyncWorker) Notify() {
	if worker == nil {
		return
	}
	select {
	case worker.events <- struct{}{}:
	default:
	}
}

// Status returns a race-safe snapshot of the latest auto-sync state.
func (worker *SyncWorker) Status() SyncStatus {
	if worker == nil {
		return SyncStatus{}
	}
	worker.statusMu.RLock()
	defer worker.statusMu.RUnlock()
	return worker.status
}

func (worker *SyncWorker) syncOnce() {
	models := worker.registry.GetAvailableModels("openai")
	providers := func(model string) []string { return worker.registry.GetProvidersForModel(model) }
	result, err := worker.lifecycle.Sync(models, providers, true)
	now := time.Now().UTC()
	worker.statusMu.Lock()
	worker.status.Attempts++
	worker.status.LastAttempt = now
	worker.status.Applied = false
	if err != nil {
		worker.status.LastError = err.Error()
	} else {
		worker.status.LastError = ""
		worker.status.LastSuccess = now
		worker.status.CatalogRevision = result.CatalogRevision
		worker.status.Applied = result.Applied
	}
	worker.statusMu.Unlock()
	if err != nil {
		if strings.Contains(err.Error(), "not set up") {
			log.Debug("Codex Integration auto-sync is waiting for setup")
		} else {
			log.Warnf("Codex Integration auto-sync kept the last-good catalog: %v", err)
		}
		return
	}
	if result.Applied {
		log.Infof("Codex Integration auto-sync published catalog revision %s", result.CatalogRevision)
	}
}

type syncRegistryHook struct {
	notify func()
}

func (hook *syncRegistryHook) OnModelsRegistered(context.Context, string, string, []*registry.ModelInfo) {
	if hook != nil && hook.notify != nil {
		hook.notify()
	}
}

func (hook *syncRegistryHook) OnModelsUnregistered(context.Context, string, string) {
	if hook != nil && hook.notify != nil {
		hook.notify()
	}
}
