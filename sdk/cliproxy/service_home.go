package cliproxy

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/home"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/homeplugins"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/redisqueue"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/watcher/diff"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executionregistry"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	log "github.com/sirupsen/logrus"
)

type homeSubscriberSupervisor struct {
	cancel context.CancelFunc
	done   chan struct{}

	publisherMu   sync.Mutex
	publisherDone <-chan struct{}
}

func (s *homeSubscriberSupervisor) setPublisherCompletion(done <-chan struct{}) {
	if s == nil {
		return
	}
	s.publisherMu.Lock()
	s.publisherDone = done
	s.publisherMu.Unlock()
}

func (s *homeSubscriberSupervisor) publisherCompletion() <-chan struct{} {
	if s == nil {
		return nil
	}
	s.publisherMu.Lock()
	defer s.publisherMu.Unlock()
	return s.publisherDone
}

type homeConfigWorkQueue struct {
	mu    sync.Mutex
	items [][]byte
	wake  chan struct{}
}

func newHomeConfigWorkQueue() *homeConfigWorkQueue {
	return &homeConfigWorkQueue{wake: make(chan struct{}, 1)}
}

func (q *homeConfigWorkQueue) enqueue(raw []byte) {
	if q == nil {
		return
	}
	item := append([]byte(nil), raw...)
	q.mu.Lock()
	q.items = append(q.items, item)
	q.mu.Unlock()
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

func (q *homeConfigWorkQueue) dequeue(ctx context.Context) ([]byte, bool) {
	if q == nil || ctx == nil {
		return nil, false
	}
	for {
		if ctx.Err() != nil {
			return nil, false
		}
		q.mu.Lock()
		if ctx.Err() != nil {
			q.mu.Unlock()
			return nil, false
		}
		if len(q.items) > 0 {
			item := q.items[0]
			q.items[0] = nil
			q.items = q.items[1:]
			q.mu.Unlock()
			return item, true
		}
		q.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, false
		case <-q.wake:
		}
	}
}

type homeLogForwarder interface {
	Bind(*home.Client)
	Deactivate(*home.Client)
	Stop()
}

var startHomeLogForwarder = func(queueSize int) homeLogForwarder {
	return logging.StartHomeAppLogForwarder(queueSize)
}

func (s *Service) applyHomeOverlay(remoteCfg *config.Config) {
	if errApply := s.applyHomeOverlayContext(context.Background(), remoteCfg); errApply != nil {
		log.Warnf("failed to apply home config payload: %v", errApply)
	}
}

func (s *Service) applyHomeOverlayContext(ctx context.Context, remoteCfg *config.Config) error {
	return s.applyHomeOverlayWithClient(ctx, remoteCfg, nil)
}

func (s *Service) applyHomeOverlayWithClient(ctx context.Context, remoteCfg *config.Config, client *home.Client) error {
	work, errStage := s.stageHomeOverlayWithClient(ctx, remoteCfg, client)
	if errStage != nil {
		return errStage
	}
	if ctx != nil {
		if errContext := ctx.Err(); errContext != nil {
			return errContext
		}
	}
	if work.config != nil {
		if !s.applyConfigUpdateWithAuthSynthesis(ctx, work.config, true) {
			return context.Canceled
		}
		work.committed = true
	}
	if errFinalize := s.finalizeHomePluginWork(ctx, client, work); errFinalize != nil {
		return errFinalize
	}
	return nil
}

func (s *Service) stageHomeOverlayWithClient(ctx context.Context, remoteCfg *config.Config, client *home.Client) (*homePluginFinalization, error) {
	work := &homePluginFinalization{}
	if s == nil || remoteCfg == nil {
		return work, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if errContext := ctx.Err(); errContext != nil {
		return nil, errContext
	}

	s.cfgMu.RLock()
	baseCfg := s.cfg
	s.cfgMu.RUnlock()
	if baseCfg == nil {
		return work, nil
	}

	merged := *remoteCfg
	merged.Host = baseCfg.Host
	merged.Port = baseCfg.Port
	merged.TLS = baseCfg.TLS
	merged.Home = baseCfg.Home
	storeAuth := merged.Plugins.StoreAuth
	forceHomeRuntimeConfig(&merged)
	syncCfg := merged
	syncCfg.Plugins.StoreAuth = storeAuth

	logHomeConfigChanges(baseCfg, &merged)
	report, syncKey, didSync, errSync := s.syncHomePluginsWithClient(ctx, &syncCfg, client)
	if errSync != nil {
		return nil, fmt.Errorf("sync home plugins: %w", errSync)
	}
	if errContext := ctx.Err(); errContext != nil {
		return nil, errContext
	}
	if didSync {
		if errLoad := homeplugins.MarkLoadResults(&report, s.pluginHost); errLoad != nil {
			return nil, fmt.Errorf("load home plugins: %w", errLoad)
		}
	}
	if strings.TrimSpace(report.Task) != "" {
		work.syncKey = syncKey
		work.markSynced = true
		if strings.TrimSpace(merged.Home.NodeID) != "" {
			work.statusWork = append(work.statusWork, homePluginStatusWork{cfg: &merged, report: report})
		}
	}
	taskWork, errTasks := s.stageHomePluginTasksWithClient(ctx, &merged, client)
	if errTasks != nil {
		return nil, fmt.Errorf("stage home plugin tasks: %w", errTasks)
	}
	work.taskWork = append(work.taskWork, taskWork...)
	if errContext := ctx.Err(); errContext != nil {
		return nil, errContext
	}
	work.config = &merged
	return work, nil
}

func (s *Service) commitHomeConfig(lifetimeCtx, homeCtx context.Context, generation uint64, work *homePluginFinalization) bool {
	if s == nil || work == nil || work.config == nil {
		return false
	}

	s.homeConfigCommitMu.Lock()
	defer s.homeConfigCommitMu.Unlock()
	if !s.homeLifetimeActive(homeCtx, lifetimeCtx, generation) {
		return false
	}
	if s.homeConfigCommitHook != nil {
		s.homeConfigCommitHook()
	}
	if !s.homeLifetimeActive(homeCtx, lifetimeCtx, generation) {
		return false
	}
	commit := s.commitConfigUpdate(work.config)
	if commit.cfg == nil {
		return false
	}
	work.config = commit.cfg
	work.configCommit = commit
	work.committed = true
	return true
}

func (s *Service) homeLifetimeActive(homeCtx, lifetimeCtx context.Context, generation uint64) bool {
	if s == nil || homeCtx.Err() != nil || lifetimeCtx.Err() != nil {
		return false
	}
	s.homeMu.Lock()
	active := s.homeGeneration == generation
	s.homeMu.Unlock()
	return active
}

func (s *Service) finalizeHomePluginWorkUntilDone(ctx, homeCtx context.Context, generation uint64, client *home.Client, work *homePluginFinalization, publish func() bool) error {
	stopClose := closeHomeClientOnCancellation(ctx, client)
	defer stopClose()
	for {
		if errContext := ctx.Err(); errContext != nil {
			return errContext
		}

		s.homeOwnershipMu.Lock()
		if !s.homeLifetimeActive(homeCtx, ctx, generation) {
			s.homeOwnershipMu.Unlock()
			return context.Canceled
		}
		errFinalize := s.finalizeHomePluginWork(ctx, client, work)
		if errFinalize == nil && (publish == nil || publish()) {
			s.homeOwnershipMu.Unlock()
			return nil
		}
		s.homeOwnershipMu.Unlock()
		if errFinalize == nil {
			return context.Canceled
		}

		log.WithError(errFinalize).Warn("failed to finalize home plugins; retrying")
		timer := time.NewTimer(homeSubscriberPreAckRetryBackoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func closeHomeClientOnCancellation(ctx context.Context, client *home.Client) func() {
	if ctx == nil || client == nil {
		return func() {}
	}
	stop := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			client.Close()
		case <-stop:
		}
	}()
	return func() { close(stop) }
}

func logHomeConfigChanges(oldCfg, newCfg *config.Config) {
	if oldCfg == nil || newCfg == nil || !newCfg.Home.Enabled || (!oldCfg.Debug && !newCfg.Debug) {
		return
	}

	details := diff.BuildConfigChangeDetails(oldCfg, newCfg)
	if len(details) == 0 {
		return
	}

	if newCfg.Debug && !log.IsLevelEnabled(log.DebugLevel) {
		util.SetLogLevel(newCfg)
	}

	log.Debugf("home config changes detected:")
	for _, detail := range details {
		log.Debugf("  %s", detail)
	}
}

func (s *Service) startHomeUsageForwarder(ctx context.Context, client *home.Client) {
	if s == nil || client == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}

	sleep := func(d time.Duration) bool {
		if d <= 0 {
			return true
		}
		timer := time.NewTimer(d)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return false
		case <-timer.C:
			return true
		}
	}

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}

			if !client.HeartbeatOK() {
				if !sleep(time.Second) {
					return
				}
				continue
			}

			items := redisqueue.PopOldest(64)
			if len(items) == 0 {
				if !sleep(500 * time.Millisecond) {
					return
				}
				continue
			}

			for i := range items {
				if errPush := client.LPushUsage(ctx, items[i]); errPush != nil {
					for j := i; j < len(items); j++ {
						redisqueue.Enqueue(items[j])
					}
					if !sleep(time.Second) {
						return
					}
					break
				}
			}
		}
	}()
}

func applyHomeObservationBarrier(registry *executionregistry.Registry, revision int64) {
	if registry != nil {
		registry.ObserveBarrier(revision)
	}
}

func applyHomeInFlightPublisherConfig(manager *coreauth.Manager, cfg internalconfig.CredentialInFlightConfig) error {
	publisherCfg, errConfig := coreauth.HomeInFlightPublisherConfigFromConfig(cfg)
	if errConfig != nil {
		return errConfig
	}
	if manager != nil {
		manager.ApplyHomeInFlightPublisherConfig(publisherCfg)
	}
	return nil
}

func (s *Service) startHomeSubscriber(ctx context.Context) {
	if s == nil {
		return
	}
	s.cfgMu.RLock()
	cfg := s.cfg
	s.cfgMu.RUnlock()
	if cfg == nil || !cfg.Home.Enabled {
		return
	}

	parentCtx := ctx
	if parentCtx == nil {
		parentCtx = context.Background()
	}

	s.homeLifecycleMu.Lock()
	defer s.homeLifecycleMu.Unlock()

	if previousSupervisor := s.homeSupervisor; previousSupervisor != nil {
		s.homeConfigCommitMu.Lock()
		previousSupervisor.cancel()
		s.homeConfigCommitMu.Unlock()
		<-previousSupervisor.done
	}
	if !s.drainDetachedHomeLifetime(parentCtx) {
		return
	}
	if parentCtx.Err() != nil {
		return
	}

	homeCtx, cancel := context.WithCancel(parentCtx)
	done := make(chan struct{})
	s.homeMu.Lock()
	s.homeGeneration++
	generation := s.homeGeneration
	s.homeCancel = cancel
	s.homeMu.Unlock()
	supervisor := &homeSubscriberSupervisor{cancel: cancel, done: done}
	s.homeSupervisor = supervisor
	go s.runHomeSubscriber(homeCtx, parentCtx, cfg.Home, generation, supervisor)
}

func (s *Service) drainDetachedHomeLifetime(parentCtx context.Context) bool {
	s.homeMu.Lock()
	previousCancel := s.homeCancel
	previousClient := s.homeClient
	previousRegistry := s.homeRegistry
	previousBundle := s.homeDispatchBundle
	previousDrainBound := s.homeDrainBound
	previousForwarder := s.homeLogForwarder
	previousForwarderClient := s.homeLogForwarderClient
	s.homeCancel = nil
	s.homeClient = nil
	s.homeRegistry = nil
	s.homeDispatchBundle = nil
	s.homeDrainBound = 0
	s.homeLogForwarderClient = nil
	s.homeMu.Unlock()

	if s.coreManager != nil {
		s.coreManager.ClearHomeDispatchBundle(previousBundle)
	}
	home.ClearCurrentIf(previousClient)
	if previousCancel != nil {
		previousCancel()
	}
	if previousForwarder != nil && previousForwarderClient == previousClient {
		previousForwarder.Deactivate(previousClient)
	}
	if previousRegistry != nil {
		if previousDrainBound <= 0 {
			previousDrainBound = internalconfig.CredentialConcurrencyConfig{}.WithDefaults().CPACancelBound
		}
		drainCtx, cancelDrain := context.WithTimeout(context.WithoutCancel(parentCtx), previousDrainBound)
		errDrain := previousRegistry.Drain(drainCtx)
		cancelDrain()
		if errDrain != nil {
			if previousClient != nil {
				previousClient.Close()
			}
			if parentCtx.Err() == nil {
				log.WithError(errDrain).Error("failed to drain replaced Home execution registry")
				s.cancelServiceRun()
			}
			return false
		}
	}
	if previousClient != nil {
		previousClient.Close()
	}
	return true
}

func (s *Service) runHomeSubscriber(homeCtx context.Context, parentCtx context.Context, homeCfg internalconfig.HomeConfig, generation uint64, supervisor *homeSubscriberSupervisor) {
	defer func() {
		s.homeMu.Lock()
		if s.homeGeneration == generation {
			s.homeCancel = nil
		}
		s.homeMu.Unlock()
		close(supervisor.done)
	}()

	var previousClient *home.Client
	registry := executionregistry.New()
	cancelBound := atomic.Int64{}
	cancelBound.Store(int64(internalconfig.CredentialConcurrencyConfig{}.WithDefaults().CPACancelBound))
	releaseFlusher := home.NewReleaseFlusher(nil, nil)
	registry.SetReleaseSink(releaseFlusher.MarkDirty)
	defer func() {
		registry.SetReleaseSink(nil)
		drainBound := time.Duration(cancelBound.Load())
		if drainBound <= 0 {
			drainBound = internalconfig.CredentialConcurrencyConfig{}.WithDefaults().CPACancelBound
		}
		drainCtx, cancelDrain := context.WithTimeout(context.WithoutCancel(parentCtx), drainBound)
		errDrain := registry.Drain(drainCtx)
		cancelDrain()
		if errDrain != nil && !errors.Is(errDrain, executionregistry.ErrRegistryClosed) && parentCtx.Err() == nil {
			log.WithError(errDrain).Error("failed to drain detached Home execution registry")
			s.cancelServiceRun()
		}
	}()
	for homeCtx.Err() == nil {
		supervisor.setPublisherCompletion(nil)
		client := previousClient
		if client == nil {
			client = home.New(homeCfg)
		} else {
			client = client.NewLifetime()
		}
		client.SetManagedLifetime(true)
		releaseCtx, releaseCancel := context.WithCancel(context.WithoutCancel(homeCtx))
		releaseFlusher.SetConfigProvider(client.LimiterConfig)
		releaseFlusher.SetSender(client.PushConcurrencyRelease)
		releaseDone := make(chan struct{})
		go func() {
			defer close(releaseDone)
			releaseFlusher.Run(releaseCtx)
		}()
		lifetimeCtx, lifetimeCancel := context.WithCancel(homeCtx)
		queue := newHomeConfigWorkQueue()
		ready := make(chan struct{})
		var readyOnce sync.Once
		var published atomic.Bool
		workerDone := make(chan struct{})

		go func() {
			defer close(workerDone)
			s.runHomeConfigWorkerWithSupervisor(lifetimeCtx, homeCtx, generation, client, registry, queue, ready, &published, &cancelBound, supervisor)
		}()

		errRun := client.RunConfigSubscriberLifetime(lifetimeCtx, func(raw []byte) error {
			parsed, errParse := config.ParseConfigBytes(raw)
			if errParse != nil {
				log.Warnf("failed to parse home config payload: %v", errParse)
				return errParse
			}
			if errSetLifecycle := client.SetLifecycleConfig(parsed.CredentialConcurrency); errSetLifecycle != nil {
				log.Warnf("failed to apply Home lifecycle config: %v", errSetLifecycle)
				return errSetLifecycle
			}
			if errPublisherConfig := applyHomeInFlightPublisherConfig(s.coreManager, parsed.CredentialInFlight); errPublisherConfig != nil {
				log.Warnf("failed to apply Home in-flight publisher config: %v", errPublisherConfig)
				return errPublisherConfig
			}
			applyHomeObservationBarrier(registry, parsed.CredentialConcurrency.ObservationBarrierRevision)
			cancelBound.Store(int64(parsed.CredentialConcurrency.WithDefaults().CPACancelBound))
			queue.enqueue(raw)
			return nil
		}, func() {
			readyOnce.Do(func() { close(ready) })
		})
		lifetimeCancel()
		<-workerDone
		if publisherDone := supervisor.publisherCompletion(); publisherDone != nil {
			<-publisherDone
		}

		s.detachHomeSubscriberLifetime(client, registry)
		retry := errRun != nil && homeCtx.Err() == nil
		if retry {
			releaseCancel()
			<-releaseDone
			client.Close()

			settleBound := time.Duration(cancelBound.Load())
			settleCtx, cancelSettle := context.WithTimeout(context.WithoutCancel(parentCtx), settleBound)
			errPending := registry.WaitPending(settleCtx)
			cancelSettle()
			if errPending != nil {
				log.WithError(errPending).Error("failed to settle pending Home dispatches before subscriber replacement")
				s.cancelServiceRun()
				return
			}
			legacyProtocol := home.IsLegacyMembershipProtocolError(errRun)
			if legacyProtocol {
				client.EnableLegacyMembership()
			}
			if client.AmbiguousDispatch() || home.IsMembershipTakeoverUnavailableError(errRun) || legacyProtocol || client.LegacyMembership() {
				registry.SetReleaseSink(nil)
				drainCtx, cancelDrain := context.WithTimeout(context.WithoutCancel(parentCtx), settleBound)
				errDrain := registry.Drain(drainCtx)
				cancelDrain()
				if errDrain != nil {
					log.WithError(errDrain).Error("failed to drain Home executions after unsafe subscriber replacement")
					s.cancelServiceRun()
					return
				}
				client.SuppressTakeover()
				registry = executionregistry.New()
				releaseFlusher = home.NewReleaseFlusher(nil, nil)
				registry.SetReleaseSink(releaseFlusher.MarkDirty)
			}
			log.WithError(errRun).Warn("home config subscription lifetime ended")
			if !published.Load() && !waitForHomeSubscriberRetry(homeCtx, homeSubscriberPreAckRetryBackoff) {
				return
			}
			previousClient = client
			continue
		}

		drainBound := time.Duration(cancelBound.Load())
		drainCtx, cancelDrain := context.WithTimeout(context.WithoutCancel(parentCtx), drainBound)
		errDrain := registry.Drain(drainCtx)
		var errFlush error
		if errDrain == nil {
			errFlush = releaseFlusher.Flush(drainCtx)
		}
		cancelDrain()
		releaseCancel()
		<-releaseDone
		client.Close()
		if errDrain != nil {
			if parentCtx.Err() == nil {
				log.WithError(errDrain).Error("failed to drain Home execution registry")
				s.cancelServiceRun()
			}
			return
		}
		if errFlush != nil {
			if parentCtx.Err() == nil {
				log.WithError(errFlush).Error("failed to flush Home concurrency releases")
				s.cancelServiceRun()
			}
			return
		}
		return
	}
}

func (s *Service) runHomeConfigWorker(lifetimeCtx, homeCtx context.Context, generation uint64, client *home.Client, registry *executionregistry.Registry, queue *homeConfigWorkQueue, ready <-chan struct{}, published *atomic.Bool, cancelBound *atomic.Int64) {
	s.runHomeConfigWorkerWithSupervisor(lifetimeCtx, homeCtx, generation, client, registry, queue, ready, published, cancelBound, nil)
}

func (s *Service) runHomeConfigWorkerWithSupervisor(lifetimeCtx, homeCtx context.Context, generation uint64, client *home.Client, registry *executionregistry.Registry, queue *homeConfigWorkQueue, ready <-chan struct{}, published *atomic.Bool, cancelBound *atomic.Int64, supervisor *homeSubscriberSupervisor) {
	select {
	case <-lifetimeCtx.Done():
		return
	case <-ready:
	}

	for {
		if lifetimeCtx.Err() != nil {
			return
		}
		raw, ok := queue.dequeue(lifetimeCtx)
		if !ok {
			return
		}
		if lifetimeCtx.Err() != nil {
			return
		}

		var work *homePluginFinalization
		for {
			if lifetimeCtx.Err() != nil {
				return
			}
			parsed, errParse := config.ParseConfigBytes(raw)
			if errParse == nil {
				work, errParse = s.stageHomeOverlayWithClient(lifetimeCtx, parsed, client)
			}
			if errParse == nil {
				break
			}
			if lifetimeCtx.Err() != nil {
				return
			}
			log.WithError(errParse).Warn("failed to stage home config; retrying")
			if !waitForHomeSubscriberRetry(lifetimeCtx, homeSubscriberPreAckRetryBackoff) {
				return
			}
		}

		var publish func() bool
		if !published.Load() {
			publish = func() bool {
				s.homeMu.Lock()
				defer s.homeMu.Unlock()
				if homeCtx.Err() != nil || lifetimeCtx.Err() != nil || s.homeGeneration != generation {
					return false
				}
				s.homeClient = client
				s.homeRegistry = registry
				s.homeDrainBound = time.Duration(cancelBound.Load())
				if s.coreManager != nil {
					s.homeDispatchBundle = s.coreManager.PublishHomeDispatch(client, registry, generation)
				}
				home.SetCurrent(client)
				if s.homeLogForwarder == nil {
					s.homeLogForwarder = startHomeLogForwarder(0)
				}
				s.homeLogForwarder.Bind(client)
				s.homeLogForwarderClient = client
				published.Store(true)
				return true
			}
		}
		if s.homeConfigStageHook != nil {
			s.homeConfigStageHook()
		}
		if !s.commitHomeConfig(lifetimeCtx, homeCtx, generation, work) {
			return
		}
		if s.homeConfigRuntimeHook != nil {
			s.homeConfigRuntimeHook()
		}
		if !s.homeLifetimeActive(homeCtx, lifetimeCtx, generation) || !s.applyConfigRuntime(lifetimeCtx, work.configCommit, true) {
			return
		}
		if errFinalize := s.finalizeHomePluginWorkUntilDone(lifetimeCtx, homeCtx, generation, client, work, publish); errFinalize != nil {
			if !errors.Is(errFinalize, context.Canceled) {
				log.WithError(errFinalize).Warn("home plugin finalization ended")
			}
			return
		}
		if publish != nil {
			s.startHomeInFlightPublisher(lifetimeCtx, client, registry, supervisor)
			s.startHomeUsageForwarder(lifetimeCtx, client)
		}
	}
}

func (s *Service) startHomeInFlightPublisher(ctx context.Context, client *home.Client, registry *executionregistry.Registry, supervisor *homeSubscriberSupervisor) {
	if s == nil || s.coreManager == nil {
		return
	}
	done := make(chan struct{})
	if supervisor != nil {
		supervisor.setPublisherCompletion(done)
	}
	go func() {
		defer close(done)
		s.coreManager.StartHomeInFlightPublisher(ctx, client, registry)
	}()
}

func waitForHomeSubscriberRetry(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (s *Service) detachHomeSubscriberLifetime(client *home.Client, registry *executionregistry.Registry) {
	if s == nil {
		return
	}
	s.homeMu.Lock()
	var bundle *coreauth.HomeDispatchBundle
	if s.homeClient == client && s.homeRegistry == registry {
		bundle = s.homeDispatchBundle
		s.homeClient = nil
		s.homeRegistry = nil
		s.homeDispatchBundle = nil
		s.homeDrainBound = 0
	}
	forwarder := s.homeLogForwarder
	if s.homeLogForwarderClient == client {
		s.homeLogForwarderClient = nil
	} else {
		forwarder = nil
	}
	s.homeMu.Unlock()
	if s.coreManager != nil {
		s.coreManager.ClearHomeDispatchBundle(bundle)
	}
	home.ClearCurrentIf(client)
	if forwarder != nil {
		forwarder.Deactivate(client)
	}
}

func (s *Service) cancelServiceRun() {
	if s == nil {
		return
	}
	s.homeMu.Lock()
	cancel := s.runCancel
	if cancel == nil {
		cancel = s.homeCancel
	}
	s.homeMu.Unlock()
	if cancel != nil {
		cancel()
	}
}
