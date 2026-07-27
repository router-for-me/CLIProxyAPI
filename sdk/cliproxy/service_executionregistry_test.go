package cliproxy

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/home"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/homeplugins"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/pluginhost"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executionregistry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	sdkpluginstore "github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginstore"
)

type blockingServiceCooldownStore struct {
	started chan struct{}
}

func (s *blockingServiceCooldownStore) Load(context.Context) ([]coreauth.CooldownStateRecord, error) {
	return nil, nil
}

func (s *blockingServiceCooldownStore) Save(ctx context.Context, _ []coreauth.CooldownStateRecord) error {
	close(s.started)
	<-ctx.Done()
	return ctx.Err()
}

func TestConfigCommitDoesNotHoldCommitMutexDuringCooldownPersistence(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	auth := &coreauth.Auth{ID: "auth-1", Provider: "xai", Status: coreauth.StatusActive}
	if _, errRegister := manager.Register(coreauth.WithSkipPersist(context.Background()), auth); errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}
	manager.MarkResult(context.Background(), coreauth.Result{
		AuthID: auth.ID, Provider: auth.Provider, Model: "grok-4", Success: false,
		Error: &coreauth.Error{Message: "rate limited", HTTPStatus: http.StatusTooManyRequests},
	})
	store := &blockingServiceCooldownStore{started: make(chan struct{})}
	manager.SetCooldownStateStore(store)
	service := &Service{cfg: &config.Config{}, coreManager: manager}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	applyDone := make(chan bool, 1)
	go func() {
		applyDone <- service.applyConfigUpdateWithAuthSynthesis(ctx, &config.Config{DisableCooling: true}, false)
	}()
	select {
	case <-store.started:
	case <-time.After(time.Second):
		t.Fatal("old cooldown store persistence did not start")
	}

	commitDone := make(chan struct{})
	go func() {
		service.commitConfigUpdate(&config.Config{})
		close(commitDone)
	}()
	select {
	case <-commitDone:
	case <-time.After(time.Second):
		t.Fatal("config commit mutex remained locked during cooldown persistence")
	}

	cancel()
	select {
	case applied := <-applyDone:
		if applied {
			t.Fatal("config runtime apply succeeded after cooldown persistence cancellation")
		}
	case <-time.After(time.Second):
		t.Fatal("config runtime apply did not honor cooldown persistence cancellation")
	}
}

func TestServiceShutdownPreservesReplacementHomeClient(t *testing.T) {
	staleClient := home.New(internalconfig.HomeConfig{Enabled: true})
	replacementClient := home.New(internalconfig.HomeConfig{Enabled: true})
	home.SetCurrent(replacementClient)
	t.Cleanup(home.ClearCurrent)

	service := &Service{homeClient: staleClient}
	if errShutdown := service.Shutdown(context.Background()); errShutdown != nil {
		t.Fatalf("Shutdown() error = %v", errShutdown)
	}
	if current := home.Current(); current != replacementClient {
		t.Fatal("Shutdown() cleared the replacement Home client")
	}
}

func TestServiceConcurrentReplacementWaitsForInFlightDrain(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	registry := executionregistry.New()
	pending, errBegin := registry.BeginDispatch()
	if errBegin != nil {
		t.Fatal(errBegin)
	}
	_, oldCancel := context.WithCancel(context.Background())
	t.Cleanup(oldCancel)
	cfg := &config.Config{}
	cfg.Home.Enabled = true
	service := &Service{
		cfg:            cfg,
		homeCancel:     oldCancel,
		homeClient:     home.New(internalconfig.HomeConfig{Enabled: true}),
		homeRegistry:   registry,
		homeDrainBound: time.Second,
	}

	firstReturned := make(chan struct{})
	go func() {
		service.startHomeSubscriber(ctx)
		close(firstReturned)
	}()
	deadline := time.Now().Add(time.Second)
	for {
		if _, errLate := registry.BeginDispatch(); errLate != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("first replacement did not begin draining")
		}
		time.Sleep(time.Millisecond)
	}

	secondReturned := make(chan struct{})
	go func() {
		service.startHomeSubscriber(ctx)
		close(secondReturned)
	}()
	select {
	case <-secondReturned:
		t.Fatal("concurrent replacement returned before the first drain completed")
	case <-time.After(50 * time.Millisecond):
	}

	pending.End()
	select {
	case <-firstReturned:
	case <-time.After(time.Second):
		t.Fatal("first replacement did not complete after its drain")
	}
	select {
	case <-secondReturned:
	case <-time.After(time.Second):
		t.Fatal("second replacement did not complete after the first drain")
	}
}

func TestServiceReplacementWaitsForPreACKSupervisorExit(t *testing.T) {
	listener, errListen := net.Listen("tcp", "127.0.0.1:0")
	if errListen != nil {
		t.Fatalf("listen: %v", errListen)
	}
	firstSubscribed := make(chan struct{})
	secondStarted := make(chan struct{})
	secondStartedBeforeFirstDone := make(chan struct{})
	stop := make(chan struct{})
	firstDoneForServer := make(chan (<-chan struct{}), 1)
	var configRequests atomic.Int32
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		for {
			conn, errAccept := listener.Accept()
			if errAccept != nil {
				return
			}
			go servePreACKReplacementConnection(conn, &configRequests, firstSubscribed, secondStarted, secondStartedBeforeFirstDone, firstDoneForServer, stop)
		}
	}()
	t.Cleanup(func() {
		close(stop)
		_ = listener.Close()
		<-serverDone
		home.ClearCurrent()
	})

	service := newRegistryTestService(t, listener)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	service.startHomeSubscriber(ctx)
	select {
	case <-firstSubscribed:
	case <-time.After(time.Second):
		t.Fatal("first subscriber did not reach pre-ACK state")
	}

	service.homeLifecycleMu.Lock()
	firstDone := service.homeSupervisor.done
	service.homeLifecycleMu.Unlock()
	if firstDone == nil {
		t.Fatal("first subscriber has no supervisor completion signal")
	}
	firstDoneForServer <- firstDone

	replaced := make(chan struct{})
	go func() {
		service.startHomeSubscriber(ctx)
		close(replaced)
	}()

	select {
	case <-secondStartedBeforeFirstDone:
		t.Fatal("replacement subscriber started before the pre-ACK supervisor exited")
	case <-secondStarted:
	case <-time.After(time.Second):
		t.Fatal("replacement subscriber did not start")
	}
	select {
	case <-firstDone:
	case <-time.After(time.Second):
		t.Fatal("pre-ACK supervisor did not exit")
	}
	select {
	case <-replaced:
	case <-time.After(time.Second):
		t.Fatal("replacement start did not return")
	}
}

func TestServiceReplacementWaitsForPublisherExitAndPinsACKedLifetimeDependencies(t *testing.T) {
	listener, errListen := net.Listen("tcp", "127.0.0.1:0")
	if errListen != nil {
		t.Fatalf("listen: %v", errListen)
	}
	frames := make(chan home.InFlightSnapshotFrame, 64)
	var configRequests atomic.Int32
	firstPublisherDoneForServer := make(chan (<-chan struct{}), 1)
	secondConfigResult := make(chan error, 1)
	allowSecondConfig := make(chan struct{})
	stop := make(chan struct{})
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		for {
			conn, errAccept := listener.Accept()
			if errAccept != nil {
				return
			}
			go servePublisherReplacementConnection(conn, &configRequests, frames, firstPublisherDoneForServer, secondConfigResult, allowSecondConfig, stop)
		}
	}()
	t.Cleanup(func() {
		close(stop)
		_ = listener.Close()
		<-serverDone
		home.ClearCurrent()
	})

	service := newRegistryTestService(t, listener)
	service.coreManager = coreauth.NewManager(nil, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	service.startHomeSubscriber(ctx)

	firstFrame := waitForPublisherReplacementFrame(t, frames, 11)
	firstClient := waitForServiceHomeClient(t, service, time.Second)
	firstRegistry := waitForServiceRegistry(t, service, time.Second)
	service.homeLifecycleMu.Lock()
	firstPublisherDone := service.homeSupervisor.publisherCompletion()
	service.homeLifecycleMu.Unlock()
	if firstPublisherDone == nil {
		t.Fatal("first subscriber did not record publisher completion")
	}
	firstPublisherDoneForServer <- firstPublisherDone
	if firstFrame.BarrierRevision != 11 {
		t.Fatalf("first publisher frame = %#v", firstFrame)
	}

	replaced := make(chan struct{})
	go func() {
		service.startHomeSubscriber(ctx)
		close(replaced)
	}()

	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	select {
	case errSecondConfig := <-secondConfigResult:
		if errSecondConfig != nil {
			t.Fatal(errSecondConfig)
		}
	case <-deadline.C:
		t.Fatal("replacement did not begin its config lifetime")
	}
	close(allowSecondConfig)

	secondFrame := waitForPublisherReplacementFrame(t, frames, 22)
	secondClient := waitForServiceHomeClient(t, service, time.Second)
	secondRegistry := waitForServiceRegistry(t, service, time.Second)
	if secondFrame.BarrierRevision != 22 {
		t.Fatalf("replacement publisher frame = %#v", secondFrame)
	}
	if secondClient == firstClient || secondRegistry == firstRegistry {
		t.Fatal("replacement publisher reused the previous lifetime dependencies")
	}
	select {
	case <-replaced:
	case <-time.After(time.Second):
		t.Fatal("replacement subscriber did not finish setup")
	}
}

func TestHomeConfigWorkerDoesNotApplyCanceledQueuedConfig(t *testing.T) {
	baseCfg := &config.Config{}
	baseCfg.Home.Enabled = true
	baseCfg.Routing.Strategy = "round-robin"
	service := &Service{cfg: baseCfg}
	queue := newHomeConfigWorkQueue()
	queue.enqueue([]byte("routing:\n  strategy: fill-first\n"))
	ready := make(chan struct{})
	close(ready)
	lifetimeCtx, cancelLifetime := context.WithCancel(context.Background())
	cancelLifetime()
	cancelBound := atomic.Int64{}
	cancelBound.Store(int64(time.Second))

	service.runHomeConfigWorker(lifetimeCtx, context.Background(), 1, nil, executionregistry.New(), queue, ready, &atomic.Bool{}, &cancelBound)

	service.cfgMu.RLock()
	strategy := service.cfg.Routing.Strategy
	service.cfgMu.RUnlock()
	if strategy != "round-robin" {
		t.Fatalf("canceled queued config changed routing strategy to %q", strategy)
	}
}

func TestHomeConfigWorkerSkipsStagedConfigWhenReplacementCancels(t *testing.T) {
	client, _ := newHomePluginTaskTestClient(t, nil, 0)
	baseCfg := &config.Config{}
	baseCfg.Home.Enabled = true
	baseCfg.Routing.Strategy = "round-robin"
	parentCtx, cancelParent := context.WithCancel(context.Background())
	t.Cleanup(cancelParent)
	homeCtx, cancelHome := context.WithCancel(parentCtx)
	t.Cleanup(cancelHome)
	lifetimeCtx, cancelLifetime := context.WithCancel(homeCtx)
	t.Cleanup(cancelLifetime)
	stagePaused := make(chan struct{})
	releaseStage := make(chan struct{})
	var releaseStageOnce sync.Once
	t.Cleanup(func() { releaseStageOnce.Do(func() { close(releaseStage) }) })
	cancelled := make(chan struct{})
	workerDone := make(chan struct{})
	service := &Service{
		cfg:            baseCfg,
		homeGeneration: 1,
		homeConfigStageHook: func() {
			close(stagePaused)
			<-releaseStage
		},
		homeSupervisor: &homeSubscriberSupervisor{cancel: func() {
			cancelLifetime()
			close(cancelled)
		}, done: workerDone},
	}
	queue := newHomeConfigWorkQueue()
	queue.enqueue([]byte("routing:\n  strategy: fill-first\n"))
	ready := make(chan struct{})
	close(ready)
	cancelBound := atomic.Int64{}
	cancelBound.Store(int64(time.Second))
	go func() {
		defer close(workerDone)
		service.runHomeConfigWorker(lifetimeCtx, homeCtx, 1, client, executionregistry.New(), queue, ready, &atomic.Bool{}, &cancelBound)
	}()
	select {
	case <-stagePaused:
	case <-time.After(time.Second):
		t.Fatal("config worker did not pause after staging")
	}

	replacementDone := make(chan struct{})
	go func() {
		service.startHomeSubscriber(parentCtx)
		close(replacementDone)
	}()
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("replacement did not cancel the staged Home config")
	}
	releaseStageOnce.Do(func() { close(releaseStage) })
	select {
	case <-workerDone:
	case <-time.After(time.Second):
		t.Fatal("canceled config worker did not exit")
	}

	service.cfgMu.RLock()
	strategy := service.cfg.Routing.Strategy
	service.cfgMu.RUnlock()
	if strategy != "round-robin" {
		t.Fatalf("canceled staged config changed routing strategy to %q", strategy)
	}
	select {
	case <-replacementDone:
	case <-time.After(time.Second):
		t.Fatal("replacement deadlocked after canceling staged config")
	}
}

func TestHomeConfigWorkerCommitCompletesBeforeReplacementCancellation(t *testing.T) {
	client, _ := newHomePluginTaskTestClient(t, nil, 0)
	baseCfg := &config.Config{}
	baseCfg.Home.Enabled = true
	baseCfg.Routing.Strategy = "round-robin"
	parentCtx, cancelParent := context.WithCancel(context.Background())
	t.Cleanup(cancelParent)
	homeCtx, cancelHome := context.WithCancel(parentCtx)
	t.Cleanup(cancelHome)
	lifetimeCtx, cancelLifetime := context.WithCancel(homeCtx)
	t.Cleanup(cancelLifetime)
	commitPaused := make(chan struct{})
	releaseCommit := make(chan struct{})
	var releaseCommitOnce sync.Once
	t.Cleanup(func() { releaseCommitOnce.Do(func() { close(releaseCommit) }) })
	cancelled := make(chan struct{})
	workerDone := make(chan struct{})
	service := &Service{
		cfg:            baseCfg,
		homeGeneration: 1,
		homeConfigCommitHook: func() {
			close(commitPaused)
			<-releaseCommit
		},
		homeSupervisor: &homeSubscriberSupervisor{cancel: func() {
			cancelLifetime()
			close(cancelled)
		}, done: workerDone},
	}
	queue := newHomeConfigWorkQueue()
	queue.enqueue([]byte("routing:\n  strategy: fill-first\n"))
	ready := make(chan struct{})
	close(ready)
	cancelBound := atomic.Int64{}
	cancelBound.Store(int64(time.Second))
	go func() {
		defer close(workerDone)
		service.runHomeConfigWorker(lifetimeCtx, homeCtx, 1, client, executionregistry.New(), queue, ready, &atomic.Bool{}, &cancelBound)
	}()
	select {
	case <-commitPaused:
	case <-time.After(time.Second):
		t.Fatal("config worker did not pause inside commit")
	}

	replacementDone := make(chan struct{})
	go func() {
		service.startHomeSubscriber(parentCtx)
		close(replacementDone)
	}()
	select {
	case <-cancelled:
		t.Fatal("replacement canceled while config commit owned the commit mutex")
	case <-time.After(50 * time.Millisecond):
	}
	releaseCommitOnce.Do(func() { close(releaseCommit) })
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("replacement did not cancel after config commit completed")
	}
	select {
	case <-workerDone:
	case <-time.After(time.Second):
		t.Fatal("config worker deadlocked after committed config was canceled")
	}

	service.cfgMu.RLock()
	strategy := service.cfg.Routing.Strategy
	service.cfgMu.RUnlock()
	if strategy != "fill-first" {
		t.Fatalf("committed config routing strategy = %q, want fill-first", strategy)
	}
	select {
	case <-replacementDone:
	case <-time.After(time.Second):
		t.Fatal("replacement deadlocked after committed config")
	}
}

func TestHomeConfigWorkerCancellationAtPostCommitBoundarySkipsRuntimePublish(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		cancel func(context.CancelFunc, context.CancelFunc)
	}{
		{name: "parent", cancel: func(cancelParent, _ context.CancelFunc) { cancelParent() }},
		{name: "transport", cancel: func(_, cancelLifetime context.CancelFunc) { cancelLifetime() }},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			client, _ := newHomePluginTaskTestClient(t, nil, 0)
			baseCfg := &config.Config{}
			baseCfg.Home.Enabled = true
			baseCfg.Routing.Strategy = "round-robin"
			parentCtx, cancelParent := context.WithCancel(context.Background())
			t.Cleanup(cancelParent)
			homeCtx, cancelHome := context.WithCancel(parentCtx)
			t.Cleanup(cancelHome)
			lifetimeCtx, cancelLifetime := context.WithCancel(homeCtx)
			t.Cleanup(cancelLifetime)
			runtimePaused := make(chan struct{})
			releaseRuntime := make(chan struct{})
			var releaseRuntimeOnce sync.Once
			t.Cleanup(func() { releaseRuntimeOnce.Do(func() { close(releaseRuntime) }) })
			service := &Service{
				cfg:            baseCfg,
				homeGeneration: 1,
				homeConfigRuntimeHook: func() {
					close(runtimePaused)
					<-releaseRuntime
				},
			}
			queue := newHomeConfigWorkQueue()
			queue.enqueue([]byte("routing:\n  strategy: fill-first\n"))
			ready := make(chan struct{})
			close(ready)
			published := atomic.Bool{}
			cancelBound := atomic.Int64{}
			cancelBound.Store(int64(time.Second))
			workerDone := make(chan struct{})
			go func() {
				defer close(workerDone)
				service.runHomeConfigWorker(lifetimeCtx, homeCtx, 1, client, executionregistry.New(), queue, ready, &published, &cancelBound)
			}()
			select {
			case <-runtimePaused:
			case <-time.After(time.Second):
				t.Fatal("Home config worker did not reach post-commit boundary")
			}

			testCase.cancel(cancelParent, cancelLifetime)
			releaseRuntimeOnce.Do(func() { close(releaseRuntime) })
			select {
			case <-workerDone:
			case <-time.After(time.Second):
				t.Fatal("canceled Home config worker did not exit")
			}
			service.cfgMu.RLock()
			strategy := service.cfg.Routing.Strategy
			service.cfgMu.RUnlock()
			if strategy != "fill-first" {
				t.Fatalf("post-commit cancellation changed committed routing strategy to %q", strategy)
			}
			if published.Load() {
				t.Fatal("canceled post-commit work published Home runtime")
			}
		})
	}
}

func TestHomeConfigWorkerShutdownCancelsBlockedRuntimeUpdatesBeforePublish(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		apply func(*Service, func(context.Context, *config.Config) bool)
	}{
		{
			name: "pprof",
			apply: func(service *Service, blocked func(context.Context, *config.Config) bool) {
				service.applyPprofConfigContextFn = blocked
			},
		},
		{
			name: "server",
			apply: func(service *Service, blocked func(context.Context, *config.Config) bool) {
				service.updateServerClientsContextFn = blocked
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			client, _ := newHomePluginTaskTestClient(t, nil, 0)
			baseCfg := &config.Config{}
			baseCfg.Home.Enabled = true
			baseCfg.Home.NodeID = "node-1"
			parentCtx, cancelParent := context.WithCancel(context.Background())
			t.Cleanup(cancelParent)
			homeCtx, cancelHome := context.WithCancel(parentCtx)
			t.Cleanup(cancelHome)
			lifetimeCtx, cancelLifetime := context.WithCancel(homeCtx)
			t.Cleanup(cancelLifetime)
			started := make(chan struct{})
			workerDone := make(chan struct{})
			service := &Service{
				cfg:            baseCfg,
				homeGeneration: 1,
				homeSupervisor: &homeSubscriberSupervisor{cancel: cancelLifetime, done: workerDone},
			}
			testCase.apply(service, func(ctx context.Context, _ *config.Config) bool {
				close(started)
				<-ctx.Done()
				return false
			})
			queue := newHomeConfigWorkQueue()
			queue.enqueue([]byte("routing:\n  strategy: fill-first\n"))
			ready := make(chan struct{})
			close(ready)
			published := atomic.Bool{}
			cancelBound := atomic.Int64{}
			cancelBound.Store(int64(time.Second))
			go func() {
				defer close(workerDone)
				service.runHomeConfigWorker(lifetimeCtx, homeCtx, 1, client, executionregistry.New(), queue, ready, &published, &cancelBound)
			}()
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("Home config worker did not start blocked runtime update")
			}

			shutdownDone := make(chan error, 1)
			go func() { shutdownDone <- service.Shutdown(context.Background()) }()
			select {
			case <-workerDone:
			case <-time.After(time.Second):
				t.Fatal("shutdown did not cancel blocked runtime update")
			}
			select {
			case errShutdown := <-shutdownDone:
				if errShutdown != nil {
					t.Fatalf("Shutdown() error = %v", errShutdown)
				}
			case <-time.After(time.Second):
				t.Fatal("shutdown waited for blocked runtime update")
			}
			if published.Load() {
				t.Fatal("canceled runtime update published Home state")
			}
		})
	}
}

func TestHomeConfigWorkerCancelsBlockedAntigravityModelRefreshBeforePublish(t *testing.T) {
	modelRefreshStarted := make(chan struct{})
	releaseModelRefresh := make(chan struct{})
	var releaseModelRefreshOnce sync.Once
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(modelRefreshStarted)
		select {
		case <-r.Context().Done():
		case <-releaseModelRefresh:
		}
	}))
	t.Cleanup(modelServer.Close)
	t.Cleanup(func() { releaseModelRefreshOnce.Do(func() { close(releaseModelRefresh) }) })

	client, _ := newHomePluginTaskTestClient(t, nil, 0)
	baseCfg := &config.Config{}
	baseCfg.Home.Enabled = true
	manager := coreauth.NewManager(nil, nil, nil)
	auth := &coreauth.Auth{
		ID:       "blocked-antigravity-refresh",
		Provider: "antigravity",
		Metadata: map[string]any{"access_token": "test-token"},
		Attributes: map[string]string{
			"base_url": modelServer.URL,
		},
	}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatal(errRegister)
	}
	t.Cleanup(func() { GlobalModelRegistry().UnregisterClient(auth.ID) })

	parentCtx, cancelParent := context.WithCancel(context.Background())
	t.Cleanup(cancelParent)
	homeCtx, cancelHome := context.WithCancel(parentCtx)
	t.Cleanup(cancelHome)
	lifetimeCtx, cancelLifetime := context.WithCancel(homeCtx)
	t.Cleanup(cancelLifetime)
	service := &Service{
		cfg:            baseCfg,
		coreManager:    manager,
		pluginHost:     pluginhost.New(),
		homeGeneration: 1,
	}
	queue := newHomeConfigWorkQueue()
	queue.enqueue([]byte("routing:\n  strategy: fill-first\n"))
	ready := make(chan struct{})
	close(ready)
	published := atomic.Bool{}
	cancelBound := atomic.Int64{}
	cancelBound.Store(int64(time.Second))
	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		service.runHomeConfigWorker(lifetimeCtx, homeCtx, 1, client, executionregistry.New(), queue, ready, &published, &cancelBound)
	}()

	select {
	case <-modelRefreshStarted:
	case <-time.After(time.Second):
		t.Fatal("Home config worker did not start Antigravity model refresh")
	}
	cancelLifetime()
	select {
	case <-workerDone:
	case <-time.After(time.Second):
		t.Fatal("Home config worker did not stop after model refresh cancellation")
	}
	if published.Load() {
		t.Fatal("canceled model refresh published Home runtime")
	}
	service.homeMu.Lock()
	publishedClient := service.homeClient
	publishedRegistry := service.homeRegistry
	service.homeMu.Unlock()
	if publishedClient != nil || publishedRegistry != nil {
		t.Fatal("canceled model refresh exposed Home runtime state")
	}
}

func TestHomeConfigWorkerRetriesStageFailureForSameQueuedConfig(t *testing.T) {
	client, _ := newHomePluginTaskTestClient(t, nil, 0)
	baseCfg := &config.Config{}
	baseCfg.Home.Enabled = true
	baseCfg.Routing.Strategy = "round-robin"
	var attempts atomic.Int32
	service := &Service{
		cfg:            baseCfg,
		homeGeneration: 1,
		homePluginSyncFetch: func(context.Context, sdkpluginstore.PluginSyncRequest) (sdkpluginstore.PluginSyncResponse, error) {
			if attempts.Add(1) == 1 {
				return sdkpluginstore.PluginSyncResponse{}, fmt.Errorf("plugin sync unavailable")
			}
			return sdkpluginstore.PluginSyncResponse{
				SchemaVersion: sdkpluginstore.PluginSyncSchemaVersion,
				ExpiresAt:     time.Now().Add(time.Minute),
			}, nil
		},
	}
	queue := newHomeConfigWorkQueue()
	queue.enqueue([]byte("plugins:\n  enabled: true\nrouting:\n  strategy: fill-first\n"))
	ready := make(chan struct{})
	close(ready)
	lifetimeCtx, cancelLifetime := context.WithCancel(context.Background())
	t.Cleanup(cancelLifetime)
	cancelBound := atomic.Int64{}
	cancelBound.Store(int64(time.Second))
	published := atomic.Bool{}
	published.Store(true)
	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		service.runHomeConfigWorker(lifetimeCtx, context.Background(), 1, client, executionregistry.New(), queue, ready, &published, &cancelBound)
	}()

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		service.cfgMu.RLock()
		strategy := service.cfg.Routing.Strategy
		service.cfgMu.RUnlock()
		if attempts.Load() >= 2 && strategy == "fill-first" {
			cancelLifetime()
			select {
			case <-workerDone:
			case <-time.After(time.Second):
				t.Fatal("config worker did not stop after cancellation")
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	cancelLifetime()
	<-workerDone
	t.Fatalf("stage attempts = %d and config was not applied after retry", attempts.Load())
}

func TestServiceInitialOverlayStagesPluginWritesUntilReady(t *testing.T) {
	listener, errListen := net.Listen("tcp", "127.0.0.1:0")
	if errListen != nil {
		t.Fatalf("listen: %v", errListen)
	}
	pluginSync := make(chan struct{})
	pluginStatus := make(chan struct{}, 2)
	pluginTasks := make(chan struct{})
	freshCommandProbe := make(chan struct{})
	allowAck := make(chan struct{})
	stop := make(chan struct{})
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		for {
			conn, errAccept := listener.Accept()
			if errAccept != nil {
				return
			}
			go serveInitialOverlayPluginConnection(conn, pluginSync, pluginStatus, pluginTasks, freshCommandProbe, allowAck, stop)
		}
	}()
	t.Cleanup(func() {
		close(stop)
		_ = listener.Close()
		<-serverDone
		home.ClearCurrent()
	})

	host, portText, errSplit := net.SplitHostPort(listener.Addr().String())
	if errSplit != nil {
		t.Fatalf("split listener address: %v", errSplit)
	}
	port, errPort := strconv.Atoi(portText)
	if errPort != nil {
		t.Fatalf("parse port: %v", errPort)
	}
	cfg := &config.Config{}
	cfg.Home.Enabled = true
	cfg.Home.Host = host
	cfg.Home.Port = port
	cfg.Home.NodeID = "node-1"
	cfg.Home.DisableClusterDiscovery = true
	cfg.Plugins.Enabled = true
	cfg.Plugins.Dir = t.TempDir()
	var deletes atomic.Int32
	service := &Service{cfg: cfg, homePluginDeleteTask: func(_ context.Context, _ *config.Config, task home.PluginTask) homeplugins.SyncReport {
		deletes.Add(1)
		return homeplugins.DeleteWithReport(context.Background(), nil, nil, task.ID, task.PluginID)
	}}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	service.startHomeSubscriber(ctx)

	for name, observed := range map[string]<-chan struct{}{
		"plugin sync":   pluginSync,
		"plugin tasks":  pluginTasks,
		"plugin status": pluginStatus,
	} {
		select {
		case <-observed:
			t.Fatalf("initial overlay staged %s before subscription ACK and fresh command probe", name)
		case <-time.After(50 * time.Millisecond):
		}
	}
	if gotDeletes := deletes.Load(); gotDeletes != 0 {
		t.Fatalf("initial overlay executed %d plugin deletes before subscription ACK and fresh command probe", gotDeletes)
	}
	service.homeMu.Lock()
	client := service.homeClient
	registry := service.homeRegistry
	service.homeMu.Unlock()
	if client != nil || registry != nil || home.Current() != nil {
		t.Fatal("initial overlay exposed its Home client or registry before subscription ACK")
	}

	close(allowAck)
	select {
	case <-freshCommandProbe:
	case <-time.After(time.Second):
		t.Fatal("subscription ACK did not rebuild and probe a fresh command connection")
	}
	for name, observed := range map[string]<-chan struct{}{
		"plugin sync":  pluginSync,
		"plugin tasks": pluginTasks,
	} {
		select {
		case <-observed:
		case <-time.After(time.Second):
			t.Fatalf("ready Home lifetime did not stage %s after subscription ACK and fresh command probe", name)
		}
	}
	for range 2 {
		select {
		case <-pluginStatus:
		case <-time.After(time.Second):
			t.Fatal("ready Home lifetime did not flush staged plugin reports")
		}
	}
	if gotDeletes := deletes.Load(); gotDeletes != 1 {
		t.Fatalf("ready Home lifetime executed %d plugin deletes, want 1", gotDeletes)
	}
	if waitForServiceRegistry(t, service, time.Second) == nil || home.Current() == nil {
		t.Fatal("subscription ACK did not expose the Home client and registry")
	}
}

func TestServiceDiscardsStalePreACKPluginWork(t *testing.T) {
	listener, errListen := net.Listen("tcp", "127.0.0.1:0")
	if errListen != nil {
		t.Fatalf("listen: %v", errListen)
	}
	firstSubscribed := make(chan struct{})
	secondSubscribed := make(chan struct{})
	allowSecondAck := make(chan struct{})
	stop := make(chan struct{})
	serverDone := make(chan struct{})
	var subscriptions atomic.Int32
	var pluginWrites atomic.Int32
	go func() {
		defer close(serverDone)
		for {
			conn, errAccept := listener.Accept()
			if errAccept != nil {
				return
			}
			go serveStalePreACKPluginConnection(conn, &subscriptions, &pluginWrites, firstSubscribed, secondSubscribed, allowSecondAck, stop)
		}
	}()
	t.Cleanup(func() {
		close(stop)
		_ = listener.Close()
		<-serverDone
		home.ClearCurrent()
	})

	host, portText, errSplit := net.SplitHostPort(listener.Addr().String())
	if errSplit != nil {
		t.Fatalf("split listener address: %v", errSplit)
	}
	port, errPort := strconv.Atoi(portText)
	if errPort != nil {
		t.Fatalf("parse port: %v", errPort)
	}
	cfg := &config.Config{}
	cfg.Home.Enabled = true
	cfg.Home.Host = host
	cfg.Home.Port = port
	cfg.Home.NodeID = "node-1"
	cfg.Home.DisableClusterDiscovery = true
	cfg.Plugins.Enabled = true
	cfg.Plugins.Dir = t.TempDir()
	service := &Service{cfg: cfg}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	service.startHomeSubscriber(ctx)
	select {
	case <-firstSubscribed:
	case <-time.After(time.Second):
		t.Fatal("first subscriber did not stage plugin work before ACK")
	}

	replaced := make(chan struct{})
	go func() {
		service.startHomeSubscriber(ctx)
		close(replaced)
	}()
	select {
	case <-secondSubscribed:
	case <-time.After(time.Second):
		t.Fatal("replacement subscriber did not reach subscription ACK")
	}
	if got := pluginWrites.Load(); got != 0 {
		t.Fatalf("stale pre-ACK lifetime flushed %d plugin reports", got)
	}
	close(allowSecondAck)
	deadline := time.Now().Add(time.Second)
	for pluginWrites.Load() != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := pluginWrites.Load(); got != 1 {
		t.Fatalf("replacement lifetime plugin reports = %d, want 1", got)
	}
	if waitForServiceRegistry(t, service, time.Second) == nil {
		t.Fatal("replacement subscription did not expose a ready registry")
	}
	select {
	case <-replaced:
	case <-time.After(time.Second):
		t.Fatal("replacement subscriber did not finish setup")
	}
}

func TestServiceExplicitReplacementDrainsPendingAndScopeBeforeStartingNewLifetime(t *testing.T) {
	listener, errListen := net.Listen("tcp", "127.0.0.1:0")
	if errListen != nil {
		t.Fatalf("listen: %v", errListen)
	}
	firstAck := make(chan struct{})
	loseFirst := make(chan struct{})
	secondSubscribe := make(chan struct{})
	var secondSubscribeOnce sync.Once
	allowSecondAck := make(chan struct{})
	stop := make(chan struct{})
	var subscriptionMu sync.Mutex
	subscriptions := 0
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		for {
			conn, errAccept := listener.Accept()
			if errAccept != nil {
				return
			}
			go serveRegistryTestHomeConnection(conn, &subscriptionMu, &subscriptions, firstAck, loseFirst, secondSubscribe, &secondSubscribeOnce, allowSecondAck, stop)
		}
	}()
	t.Cleanup(func() {
		close(stop)
		_ = listener.Close()
		<-serverDone
		home.ClearCurrent()
	})

	service := newRegistryTestService(t, listener)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	service.startHomeSubscriber(ctx)
	select {
	case <-firstAck:
	case <-time.After(time.Second):
		t.Fatal("first subscription was not acknowledged")
	}
	registry := waitForServiceRegistry(t, service, time.Second)
	pending, errBegin := registry.BeginDispatch()
	if errBegin != nil {
		t.Fatal(errBegin)
	}
	scopePending, errBegin := registry.BeginDispatch()
	if errBegin != nil {
		t.Fatal(errBegin)
	}
	scope, errInstall := registry.Install(scopePending, executionregistry.ScopeSpec{})
	if errInstall != nil {
		t.Fatal(errInstall)
	}
	resourceClosed := make(chan struct{})
	if errBind := scope.Bind(func() error {
		close(resourceClosed)
		go scope.End("canceled")
		return nil
	}); errBind != nil {
		t.Fatal(errBind)
	}

	replaced := make(chan struct{})
	go func() {
		service.startHomeSubscriber(ctx)
		close(replaced)
	}()
	select {
	case <-resourceClosed:
	case <-time.After(time.Second):
		t.Fatal("explicit replacement did not start draining the active scope")
	}
	select {
	case <-secondSubscribe:
		t.Fatal("new subscriber started before the old pending dispatch drained")
	case <-time.After(50 * time.Millisecond):
	}
	pending.End()
	select {
	case <-replaced:
	case <-time.After(time.Second):
		t.Fatal("explicit replacement did not finish after pending dispatch ended")
	}
	select {
	case <-secondSubscribe:
	case <-time.After(time.Second):
		t.Fatal("new subscriber did not start after successful drain")
	}
}

func TestServiceReplacementWaitsForBlockedDrainSupervisorExit(t *testing.T) {
	listener, errListen := net.Listen("tcp", "127.0.0.1:0")
	if errListen != nil {
		t.Fatalf("listen: %v", errListen)
	}
	firstAck := make(chan struct{})
	loseFirst := make(chan struct{})
	secondSubscribe := make(chan struct{})
	var secondSubscribeOnce sync.Once
	allowSecondAck := make(chan struct{})
	stop := make(chan struct{})
	var subscriptionMu sync.Mutex
	subscriptions := 0
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		for {
			conn, errAccept := listener.Accept()
			if errAccept != nil {
				return
			}
			go serveRegistryTestHomeConnection(conn, &subscriptionMu, &subscriptions, firstAck, loseFirst, secondSubscribe, &secondSubscribeOnce, allowSecondAck, stop)
		}
	}()
	t.Cleanup(func() {
		close(stop)
		_ = listener.Close()
		<-serverDone
		home.ClearCurrent()
	})

	service := newRegistryTestService(t, listener)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	service.startHomeSubscriber(ctx)
	select {
	case <-firstAck:
	case <-time.After(time.Second):
		t.Fatal("first subscription was not acknowledged")
	}
	service.homeLifecycleMu.Lock()
	firstDone := service.homeSupervisor.done
	service.homeLifecycleMu.Unlock()
	registry := waitForServiceRegistry(t, service, time.Second)
	pending, errBegin := registry.BeginDispatch()
	if errBegin != nil {
		t.Fatal(errBegin)
	}
	scopePending, errBegin := registry.BeginDispatch()
	if errBegin != nil {
		t.Fatal(errBegin)
	}
	scope, errInstall := registry.Install(scopePending, executionregistry.ScopeSpec{})
	if errInstall != nil {
		t.Fatal(errInstall)
	}
	resourceClosed := make(chan struct{})
	if errBind := scope.Bind(func() error {
		close(resourceClosed)
		go scope.End("canceled")
		return nil
	}); errBind != nil {
		t.Fatal(errBind)
	}

	replaced := make(chan struct{})
	go func() {
		service.startHomeSubscriber(ctx)
		close(replaced)
	}()
	select {
	case <-resourceClosed:
	case <-time.After(time.Second):
		t.Fatal("replacement did not begin draining the active scope")
	}
	select {
	case <-firstDone:
		t.Fatal("supervisor exited before the pending dispatch drained")
	case <-secondSubscribe:
		t.Fatal("replacement subscriber started before the old supervisor exited")
	case <-time.After(50 * time.Millisecond):
	}

	pending.End()
	select {
	case <-firstDone:
	case <-time.After(time.Second):
		t.Fatal("old supervisor did not exit after drain completed")
	}
	select {
	case <-secondSubscribe:
	case <-time.After(time.Second):
		t.Fatal("replacement subscriber did not start after old supervisor exit")
	}
	select {
	case <-replaced:
	case <-time.After(time.Second):
		t.Fatal("replacement start did not return")
	}
}

func TestServiceExplicitReplacementCancelsRunWhenDrainTimesOut(t *testing.T) {
	listener, errListen := net.Listen("tcp", "127.0.0.1:0")
	if errListen != nil {
		t.Fatalf("listen: %v", errListen)
	}
	firstAck := make(chan struct{})
	loseFirst := make(chan struct{})
	secondSubscribe := make(chan struct{})
	var secondSubscribeOnce sync.Once
	allowSecondAck := make(chan struct{})
	stop := make(chan struct{})
	var subscriptionMu sync.Mutex
	subscriptions := 0
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		for {
			conn, errAccept := listener.Accept()
			if errAccept != nil {
				return
			}
			go serveRegistryTestHomeConnection(conn, &subscriptionMu, &subscriptions, firstAck, loseFirst, secondSubscribe, &secondSubscribeOnce, allowSecondAck, stop)
		}
	}()
	t.Cleanup(func() {
		close(stop)
		_ = listener.Close()
		<-serverDone
		home.ClearCurrent()
	})

	service := newRegistryTestService(t, listener)
	serviceCtx, cancelService := context.WithCancel(context.Background())
	t.Cleanup(cancelService)
	service.homeMu.Lock()
	service.runCancel = cancelService
	service.homeMu.Unlock()
	service.startHomeSubscriber(serviceCtx)
	select {
	case <-firstAck:
	case <-time.After(time.Second):
		t.Fatal("first subscription was not acknowledged")
	}
	registry := waitForServiceRegistry(t, service, time.Second)
	pending, errBegin := registry.BeginDispatch()
	if errBegin != nil {
		t.Fatal(errBegin)
	}
	scope, errInstall := registry.Install(pending, executionregistry.ScopeSpec{})
	if errInstall != nil {
		t.Fatal(errInstall)
	}
	resourceClosed := make(chan struct{})
	release := make(chan struct{})
	if errBind := scope.Bind(func() error {
		close(resourceClosed)
		<-release
		return nil
	}); errBind != nil {
		t.Fatal(errBind)
	}

	go service.startHomeSubscriber(serviceCtx)
	select {
	case <-resourceClosed:
	case <-time.After(time.Second):
		t.Fatal("explicit replacement did not start draining the blocking scope")
	}
	select {
	case <-serviceCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("explicit replacement did not cancel the Service run after drain timeout")
	}
	select {
	case <-secondSubscribe:
		t.Fatal("new subscriber started after explicit replacement drain timeout")
	case <-time.After(50 * time.Millisecond):
	}

	close(release)
	scope.End("test cleanup")
}

func TestServiceReplacesRegistryOnlyAfterNewSubscriptionAck(t *testing.T) {
	listener, errListen := net.Listen("tcp", "127.0.0.1:0")
	if errListen != nil {
		t.Fatalf("listen: %v", errListen)
	}
	firstAck := make(chan struct{})
	loseFirst := make(chan struct{})
	secondSubscribe := make(chan struct{})
	var secondSubscribeOnce sync.Once
	allowSecondAck := make(chan struct{})
	stop := make(chan struct{})
	var subscriptionMu sync.Mutex
	subscriptions := 0
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		for {
			conn, errAccept := listener.Accept()
			if errAccept != nil {
				return
			}
			go serveRegistryTestHomeConnection(conn, &subscriptionMu, &subscriptions, firstAck, loseFirst, secondSubscribe, &secondSubscribeOnce, allowSecondAck, stop)
		}
	}()
	t.Cleanup(func() {
		close(stop)
		_ = listener.Close()
		<-serverDone
		home.ClearCurrent()
	})

	host, portText, errSplit := net.SplitHostPort(listener.Addr().String())
	if errSplit != nil {
		t.Fatalf("split listener address: %v", errSplit)
	}
	port, errPort := strconv.Atoi(portText)
	if errPort != nil {
		t.Fatalf("parse port: %v", errPort)
	}
	cfg := &config.Config{}
	cfg.Home.Enabled = true
	cfg.Home.Host = host
	cfg.Home.Port = port
	cfg.Home.DisableClusterDiscovery = true
	service := &Service{cfg: cfg}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	service.startHomeSubscriber(ctx)

	select {
	case <-firstAck:
	case <-time.After(time.Second):
		t.Fatal("first subscription was not acknowledged")
	}
	firstRegistry := waitForServiceRegistry(t, service, time.Second)
	if home.Current() == nil {
		t.Fatal("first client was not exposed after subscription ACK")
	}

	close(loseFirst)
	select {
	case <-secondSubscribe:
	case <-time.After(time.Second):
		t.Fatal("second subscription did not start after heartbeat loss")
	}
	service.homeMu.Lock()
	exposedRegistry := service.homeRegistry
	exposedClient := service.homeClient
	service.homeMu.Unlock()
	if exposedRegistry != nil || exposedClient != nil || home.Current() != nil {
		t.Fatal("old subscriber lifetime remained exposed before the replacement ACK")
	}

	close(allowSecondAck)
	secondRegistry := waitForServiceRegistry(t, service, time.Second)
	if secondRegistry == firstRegistry {
		t.Fatal("replacement subscription reused the old registry")
	}
	if home.Current() == nil {
		t.Fatal("replacement client was not exposed after the replacement ACK")
	}
}

func TestServiceDrainsBeforePreAckRetriesAndExposesOnlyAfterNewAck(t *testing.T) {
	listener, errListen := net.Listen("tcp", "127.0.0.1:0")
	if errListen != nil {
		t.Fatalf("listen: %v", errListen)
	}
	firstAck := make(chan struct{})
	loseFirst := make(chan struct{})
	resourceClosed := make(chan struct{})
	preAckAttempts := make(chan time.Time, 2)
	finalSubscribe := make(chan struct{})
	allowFinalAck := make(chan struct{})
	stop := make(chan struct{})
	var configMu sync.Mutex
	configRequests := 0
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		for {
			conn, errAccept := listener.Accept()
			if errAccept != nil {
				return
			}
			go serveSuccessChainHomeConnection(conn, &configMu, &configRequests, firstAck, loseFirst, preAckAttempts, finalSubscribe, allowFinalAck, stop)
		}
	}()
	t.Cleanup(func() {
		close(stop)
		_ = listener.Close()
		<-serverDone
		home.ClearCurrent()
	})

	service := newRegistryTestService(t, listener)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	service.startHomeSubscriber(ctx)
	select {
	case <-firstAck:
	case <-time.After(time.Second):
		t.Fatal("first subscription was not acknowledged")
	}
	firstRegistry := waitForServiceRegistry(t, service, time.Second)
	pending, errBegin := firstRegistry.BeginDispatch()
	if errBegin != nil {
		t.Fatal(errBegin)
	}
	scope, errInstall := firstRegistry.Install(pending, executionregistry.ScopeSpec{})
	if errInstall != nil {
		t.Fatal(errInstall)
	}
	if errBind := scope.Bind(func() error {
		close(resourceClosed)
		return nil
	}); errBind != nil {
		t.Fatal(errBind)
	}

	close(loseFirst)
	select {
	case <-resourceClosed:
	case <-time.After(time.Second):
		t.Fatal("heartbeat loss did not close the active scope resource")
	}
	select {
	case <-preAckAttempts:
		t.Fatal("pre-ACK retry started before the active scope owner ended")
	case <-time.After(50 * time.Millisecond):
	}
	scope.End("canceled")

	firstPreAck := <-preAckAttempts
	secondPreAck := <-preAckAttempts
	if retryDelay := secondPreAck.Sub(firstPreAck); retryDelay < 75*time.Millisecond {
		t.Fatalf("pre-ACK retry delay = %v, want at least 75ms", retryDelay)
	}
	select {
	case <-finalSubscribe:
	case <-time.After(time.Second):
		t.Fatal("subscriber did not retry after pre-ACK rejections")
	}
	service.homeMu.Lock()
	exposedRegistry := service.homeRegistry
	exposedClient := service.homeClient
	service.homeMu.Unlock()
	if exposedRegistry != nil || exposedClient != nil || home.Current() != nil {
		t.Fatal("new Home lifetime was exposed before its subscription ACK")
	}

	close(allowFinalAck)
	secondRegistry := waitForServiceRegistry(t, service, time.Second)
	if secondRegistry == firstRegistry || home.Current() == nil {
		t.Fatal("new Home lifetime was not exposed only after its subscription ACK")
	}
}

func TestServiceCancelsRunWhenBlockingScopeExceedsDrainBound(t *testing.T) {
	listener, errListen := net.Listen("tcp", "127.0.0.1:0")
	if errListen != nil {
		t.Fatalf("listen: %v", errListen)
	}
	firstAck := make(chan struct{})
	loseFirst := make(chan struct{})
	secondSubscribe := make(chan struct{})
	var secondSubscribeOnce sync.Once
	allowSecondAck := make(chan struct{})
	stop := make(chan struct{})
	var subscriptionMu sync.Mutex
	subscriptions := 0
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		for {
			conn, errAccept := listener.Accept()
			if errAccept != nil {
				return
			}
			go serveRegistryTestHomeConnection(conn, &subscriptionMu, &subscriptions, firstAck, loseFirst, secondSubscribe, &secondSubscribeOnce, allowSecondAck, stop)
		}
	}()
	t.Cleanup(func() {
		close(stop)
		_ = listener.Close()
		<-serverDone
		home.ClearCurrent()
	})

	host, portText, errSplit := net.SplitHostPort(listener.Addr().String())
	if errSplit != nil {
		t.Fatalf("split listener address: %v", errSplit)
	}
	port, errPort := strconv.Atoi(portText)
	if errPort != nil {
		t.Fatalf("parse port: %v", errPort)
	}
	cfg := &config.Config{}
	cfg.Home.Enabled = true
	cfg.Home.Host = host
	cfg.Home.Port = port
	cfg.Home.DisableClusterDiscovery = true
	service := &Service{cfg: cfg}
	serviceCtx, cancelService := context.WithCancel(context.Background())
	t.Cleanup(cancelService)
	service.homeMu.Lock()
	service.runCancel = cancelService
	service.homeMu.Unlock()
	service.startHomeSubscriber(serviceCtx)

	select {
	case <-firstAck:
	case <-time.After(time.Second):
		t.Fatal("first subscription was not acknowledged")
	}
	registry := waitForServiceRegistry(t, service, time.Second)
	pending, errBegin := registry.BeginDispatch()
	if errBegin != nil {
		t.Fatal(errBegin)
	}
	scope, errInstall := registry.Install(pending, executionregistry.ScopeSpec{})
	if errInstall != nil {
		t.Fatal(errInstall)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	if errBind := scope.Bind(func() error {
		close(started)
		<-release
		return nil
	}); errBind != nil {
		t.Fatal(errBind)
	}

	close(loseFirst)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("drain did not start closing the blocking scope")
	}
	select {
	case <-secondSubscribe:
		t.Fatal("new subscription started before the old registry drained")
	case <-time.After(50 * time.Millisecond):
	}
	service.homeMu.Lock()
	exposedRegistry := service.homeRegistry
	service.homeMu.Unlock()
	if exposedRegistry != nil {
		t.Fatal("new registry was exposed before the old registry drained")
	}
	select {
	case <-serviceCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("service run was not canceled after drain timeout")
	}

	close(release)
	scope.End("test cleanup")
}

func TestServiceBacksOffAfterRepeatedPreAckFailures(t *testing.T) {
	listener, errListen := net.Listen("tcp", "127.0.0.1:0")
	if errListen != nil {
		t.Fatalf("listen: %v", errListen)
	}
	attempts := make(chan time.Time, 8)
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		for {
			conn, errAccept := listener.Accept()
			if errAccept != nil {
				return
			}
			go servePreAckFailureConnection(conn, attempts)
		}
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		<-serverDone
		home.ClearCurrent()
	})

	host, portText, errSplit := net.SplitHostPort(listener.Addr().String())
	if errSplit != nil {
		t.Fatalf("split listener address: %v", errSplit)
	}
	port, errPort := strconv.Atoi(portText)
	if errPort != nil {
		t.Fatalf("parse port: %v", errPort)
	}
	cfg := &config.Config{}
	cfg.Home.Enabled = true
	cfg.Home.Host = host
	cfg.Home.Port = port
	cfg.Home.DisableClusterDiscovery = true
	service := &Service{cfg: cfg}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	service.startHomeSubscriber(ctx)

	firstAttempt := <-attempts
	secondAttempt := <-attempts
	if retryDelay := secondAttempt.Sub(firstAttempt); retryDelay < 75*time.Millisecond {
		t.Fatalf("pre-ACK retry delay = %v, want at least 75ms", retryDelay)
	}
	cancel()
	select {
	case thirdAttempt := <-attempts:
		t.Fatalf("pre-ACK retry continued after cancellation at %v", thirdAttempt)
	case <-time.After(150 * time.Millisecond):
	}
}

func TestServiceHeartbeatLossCancelsBlockedConfigFinalizationBeforeDrain(t *testing.T) {
	listener, errListen := net.Listen("tcp", "127.0.0.1:0")
	if errListen != nil {
		t.Fatalf("listen: %v", errListen)
	}
	update := make(chan struct{})
	statusStarted := make(chan struct{})
	statusRelease := make(chan struct{})
	secondConfig := make(chan struct{})
	var configRequests atomic.Int32
	var statusWrites atomic.Int32
	stop := make(chan struct{})
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		for {
			conn, errAccept := listener.Accept()
			if errAccept != nil {
				return
			}
			go serveBlockedFinalizationConnection(conn, &configRequests, &statusWrites, update, statusStarted, statusRelease, secondConfig, stop)
		}
	}()
	t.Cleanup(func() {
		close(stop)
		close(statusRelease)
		_ = listener.Close()
		<-serverDone
		home.ClearCurrent()
	})

	service := newRegistryTestService(t, listener)
	service.cfg.Home.NodeID = "node-1"
	service.homePluginSyncKey = homePluginSyncKey(service.cfg)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	service.startHomeSubscriber(ctx)
	registry := waitForServiceRegistry(t, service, time.Second)
	pending, errBegin := registry.BeginDispatch()
	if errBegin != nil {
		t.Fatal(errBegin)
	}
	scope, errInstall := registry.Install(pending, executionregistry.ScopeSpec{})
	if errInstall != nil {
		t.Fatal(errInstall)
	}
	resourceClosed := make(chan struct{})
	if errBind := scope.Bind(func() error {
		close(resourceClosed)
		go scope.End("canceled")
		return nil
	}); errBind != nil {
		t.Fatal(errBind)
	}

	close(update)
	select {
	case <-statusStarted:
	case <-time.After(time.Second):
		t.Fatal("updated config did not enter blocked finalization")
	}
	select {
	case <-resourceClosed:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("heartbeat loss did not cancel the worker and drain the active execution")
	}
	select {
	case <-secondConfig:
	case <-time.After(time.Second):
		t.Fatal("subscriber did not retry after heartbeat loss")
	}
	service.homeMu.Lock()
	currentRegistry := service.homeRegistry
	currentClient := service.homeClient
	service.homeMu.Unlock()
	if currentRegistry != nil || currentClient != nil || home.Current() != nil {
		t.Fatal("heartbeat-lost lifetime left a published Home client or registry")
	}
}

func TestServiceConfigWorkerFinalizesRapidUpdatesInOrder(t *testing.T) {
	listener, errListen := net.Listen("tcp", "127.0.0.1:0")
	if errListen != nil {
		t.Fatalf("listen: %v", errListen)
	}
	updates := make(chan struct{})
	statuses := make(chan homeplugins.SyncReport, 4)
	var taskRequests atomic.Int32
	stop := make(chan struct{})
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		for {
			conn, errAccept := listener.Accept()
			if errAccept != nil {
				return
			}
			go serveOrderedConfigUpdatesConnection(conn, &taskRequests, updates, statuses, stop)
		}
	}()
	t.Cleanup(func() {
		close(stop)
		_ = listener.Close()
		<-serverDone
		home.ClearCurrent()
	})

	service := newRegistryTestService(t, listener)
	service.cfg.Home.NodeID = "node-1"
	service.homePluginSyncKey = homePluginSyncKey(service.cfg)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	service.startHomeSubscriber(ctx)
	waitForServiceRegistry(t, service, time.Second)
	close(updates)

	gotTaskIDs := make([]uint, 0, 2)
	for len(gotTaskIDs) < 2 {
		select {
		case report := <-statuses:
			if report.TaskID != 0 {
				gotTaskIDs = append(gotTaskIDs, report.TaskID)
			}
		case <-time.After(time.Second):
			t.Fatal("rapid config updates did not finalize all ordered task work")
		}
	}
	wantTaskIDs := []uint{1, 2}
	for index := range wantTaskIDs {
		if gotTaskIDs[index] != wantTaskIDs[index] {
			t.Fatalf("plugin task status IDs = %v, want %v", gotTaskIDs, wantTaskIDs)
		}
	}
}

func serveBlockedFinalizationConnection(conn net.Conn, configRequests, statusWrites *atomic.Int32, update <-chan struct{}, statusStarted chan<- struct{}, statusRelease <-chan struct{}, secondConfig chan<- struct{}, stop <-chan struct{}) {
	defer func() { _ = conn.Close() }()
	reader := bufio.NewReader(conn)
	for {
		args, errRead := readRegistryTestRedisCommand(reader)
		if errRead != nil {
			return
		}
		switch {
		case len(args) > 0 && strings.EqualFold(args[0], "HELLO"):
			if _, errWrite := io.WriteString(conn, "%6\r\n$6\r\nserver\r\n$5\r\nredis\r\n$5\r\nproto\r\n:3\r\n$2\r\nid\r\n:1\r\n$4\r\nmode\r\n$10\r\nstandalone\r\n$4\r\nrole\r\n$6\r\nmaster\r\n$7\r\nmodules\r\n*0\r\n"); errWrite != nil {
				return
			}
		case len(args) >= 2 && strings.EqualFold(args[0], "GET") && args[1] == "config":
			if configRequests.Add(1) > 1 {
				select {
				case secondConfig <- struct{}{}:
				case <-stop:
				}
				_, _ = io.WriteString(conn, "-ERR unavailable\r\n")
				return
			}
			writeRegistryTestConfig(conn, "credential-concurrency:\n  lifecycle-config-revision: 1\n  cpa-heartbeat-timeout: 100ms\n  cpa-cancel-bound: 100ms\n")
		case len(args) >= 2 && strings.EqualFold(args[0], "GET") && args[1] == "plugin-tasks":
			_, _ = io.WriteString(conn, "$-1\r\n")
		case len(args) >= 2 && strings.EqualFold(args[0], "GET") && args[1] == "plugin-sync":
			payload := fmt.Sprintf(`{"schema_version":1,"expires_at":%q,"items":[]}`, time.Now().Add(time.Minute).UTC().Format(time.RFC3339Nano))
			writeRegistryTestConfig(conn, payload)
		case len(args) >= 2 && strings.EqualFold(args[0], "RPUSH") && args[1] == "plugin-status":
			if statusWrites.Add(1) == 1 {
				if _, errWrite := io.WriteString(conn, ":1\r\n"); errWrite != nil {
					return
				}
				continue
			}
			select {
			case statusStarted <- struct{}{}:
			case <-stop:
				return
			}
			select {
			case <-statusRelease:
				return
			case <-stop:
				return
			}
		case len(args) >= 2 && strings.EqualFold(args[0], "SUBSCRIBE") && args[1] == "config":
			if _, errWrite := io.WriteString(conn, "*3\r\n$9\r\nsubscribe\r\n$6\r\nconfig\r\n:1\r\n"); errWrite != nil {
				return
			}
			select {
			case <-update:
				writeRegistryTestMessage(conn, "credential-concurrency:\n  lifecycle-config-revision: 2\n  cpa-heartbeat-timeout: 100ms\n  cpa-cancel-bound: 100ms\nplugins:\n  enabled: true\n")
			case <-stop:
				return
			}
			<-stop
			return
		default:
			if _, errWrite := io.WriteString(conn, "+OK\r\n"); errWrite != nil {
				return
			}
		}
	}
}

func serveOrderedConfigUpdatesConnection(conn net.Conn, taskRequests *atomic.Int32, updates <-chan struct{}, statuses chan<- homeplugins.SyncReport, stop <-chan struct{}) {
	defer func() { _ = conn.Close() }()
	reader := bufio.NewReader(conn)
	for {
		args, errRead := readRegistryTestRedisCommand(reader)
		if errRead != nil {
			return
		}
		switch {
		case len(args) > 0 && strings.EqualFold(args[0], "HELLO"):
			_, _ = io.WriteString(conn, "%6\r\n$6\r\nserver\r\n$5\r\nredis\r\n$5\r\nproto\r\n:3\r\n$2\r\nid\r\n:1\r\n$4\r\nmode\r\n$10\r\nstandalone\r\n$4\r\nrole\r\n$6\r\nmaster\r\n$7\r\nmodules\r\n*0\r\n")
		case len(args) >= 2 && strings.EqualFold(args[0], "GET") && args[1] == "config":
			writeRegistryTestConfig(conn, "credential-concurrency:\n  lifecycle-config-revision: 1\n  cpa-heartbeat-timeout: 1s\n  cpa-cancel-bound: 100ms\n")
		case len(args) >= 2 && strings.EqualFold(args[0], "GET") && args[1] == "plugin-sync":
			payload := fmt.Sprintf(`{"schema_version":1,"expires_at":%q,"items":[]}`, time.Now().Add(time.Minute).UTC().Format(time.RFC3339Nano))
			writeRegistryTestConfig(conn, payload)
		case len(args) >= 2 && strings.EqualFold(args[0], "GET") && args[1] == "plugin-tasks":
			request := taskRequests.Add(1)
			if request == 1 {
				_, _ = io.WriteString(conn, "$-1\r\n")
				continue
			}
			payload := fmt.Sprintf(`[{"id":%d,"operation":"delete","plugin_id":"plugin-%d"}]`, request-1, request-1)
			writeRegistryTestConfig(conn, payload)
		case len(args) >= 3 && strings.EqualFold(args[0], "RPUSH") && args[1] == "plugin-status":
			var report homeplugins.SyncReport
			if errUnmarshal := json.Unmarshal([]byte(args[2]), &report); errUnmarshal != nil {
				return
			}
			select {
			case statuses <- report:
			case <-stop:
				return
			}
			_, _ = io.WriteString(conn, ":1\r\n")
		case len(args) >= 2 && strings.EqualFold(args[0], "SUBSCRIBE") && args[1] == "config":
			if _, errWrite := io.WriteString(conn, "*3\r\n$9\r\nsubscribe\r\n$6\r\nconfig\r\n:1\r\n"); errWrite != nil {
				return
			}
			select {
			case <-updates:
				writeRegistryTestMessage(conn, "credential-concurrency:\n  lifecycle-config-revision: 2\n  cpa-heartbeat-timeout: 1s\n  cpa-cancel-bound: 100ms\nplugins:\n  enabled: true\n")
				writeRegistryTestMessage(conn, "credential-concurrency:\n  lifecycle-config-revision: 3\n  cpa-heartbeat-timeout: 1s\n  cpa-cancel-bound: 100ms\n")
			case <-stop:
				return
			}
			<-stop
			return
		default:
			_, _ = io.WriteString(conn, "+OK\r\n")
		}
	}
}

func writeRegistryTestConfig(conn net.Conn, payload string) {
	_, _ = io.WriteString(conn, fmt.Sprintf("$%d\r\n%s\r\n", len(payload), payload))
}

func writeRegistryTestMessage(conn net.Conn, payload string) {
	_, _ = io.WriteString(conn, fmt.Sprintf("*3\r\n$7\r\nmessage\r\n$6\r\nconfig\r\n$%d\r\n%s\r\n", len(payload), payload))
}

func newRegistryTestService(t *testing.T, listener net.Listener) *Service {
	t.Helper()
	host, portText, errSplit := net.SplitHostPort(listener.Addr().String())
	if errSplit != nil {
		t.Fatalf("split listener address: %v", errSplit)
	}
	port, errPort := strconv.Atoi(portText)
	if errPort != nil {
		t.Fatalf("parse port: %v", errPort)
	}
	cfg := &config.Config{}
	cfg.Home.Enabled = true
	cfg.Home.Host = host
	cfg.Home.Port = port
	cfg.Home.DisableClusterDiscovery = true
	return &Service{cfg: cfg}
}

func waitForServiceRegistry(t *testing.T, service *Service, timeout time.Duration) *executionregistry.Registry {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		service.homeMu.Lock()
		registry := service.homeRegistry
		service.homeMu.Unlock()
		if registry != nil {
			return registry
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("service did not expose a ready execution registry")
	return nil
}

type testHomeLogForwarder struct {
	mu            sync.Mutex
	owner         *home.Client
	binds         int
	deactivations int
	stops         atomic.Int32
}

func (f *testHomeLogForwarder) Bind(client *home.Client) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.owner = client
	f.binds++
}

func (f *testHomeLogForwarder) Deactivate(client *home.Client) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.owner == client {
		f.owner = nil
	}
	f.deactivations++
}

func (f *testHomeLogForwarder) currentOwner() *home.Client {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.owner
}

func (f *testHomeLogForwarder) bindCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.binds
}

func (f *testHomeLogForwarder) Stop() {
	f.stops.Add(1)
}

func TestServiceReusesHomeLogForwarderAcrossReconnects(t *testing.T) {
	listener, errListen := net.Listen("tcp", "127.0.0.1:0")
	if errListen != nil {
		t.Fatalf("listen: %v", errListen)
	}
	acks := make(chan struct{}, 3)
	stop := make(chan struct{})
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		for {
			conn, errAccept := listener.Accept()
			if errAccept != nil {
				return
			}
			go serveHomeLogForwarderReconnectConnection(conn, acks, stop)
		}
	}()
	t.Cleanup(func() {
		close(stop)
		_ = listener.Close()
		<-serverDone
		home.ClearCurrent()
	})

	forwarder := &testHomeLogForwarder{}
	originalStart := startHomeLogForwarder
	var starts atomic.Int32
	startHomeLogForwarder = func(int) homeLogForwarder {
		starts.Add(1)
		return forwarder
	}
	t.Cleanup(func() { startHomeLogForwarder = originalStart })

	service := newRegistryTestService(t, listener)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	service.startHomeSubscriber(ctx)
	waitForHomeLogForwarderACK(t, acks)
	first := waitForServiceHomeClient(t, service, time.Second)

	service.startHomeSubscriber(ctx)
	waitForHomeLogForwarderACK(t, acks)
	second := waitForServiceHomeClient(t, service, time.Second)
	if second == first {
		t.Fatal("first reconnect reused the previous Home client")
	}

	service.startHomeSubscriber(ctx)
	waitForHomeLogForwarderACK(t, acks)
	third := waitForServiceHomeClient(t, service, time.Second)
	if third == second {
		t.Fatal("second reconnect reused the previous Home client")
	}
	if got := starts.Load(); got != 1 {
		t.Fatalf("Home log forwarder starts = %d, want 1", got)
	}
	if got := forwarder.bindCount(); got != 3 {
		t.Fatalf("Home log forwarder binds = %d, want 3", got)
	}
	if owner := forwarder.currentOwner(); owner != third {
		t.Fatal("Home log forwarder does not target the current Home client")
	}
	if current := home.Current(); current != third {
		t.Fatal("current Home client does not match log forwarder owner")
	}

	if errShutdown := service.Shutdown(context.Background()); errShutdown != nil {
		t.Fatalf("Shutdown() error = %v", errShutdown)
	}
	if got := forwarder.stops.Load(); got != 1 {
		t.Fatalf("Home log forwarder stops = %d, want 1", got)
	}
}

func waitForHomeLogForwarderACK(t *testing.T, acks <-chan struct{}) {
	t.Helper()
	select {
	case <-acks:
	case <-time.After(time.Second):
		t.Fatal("Home subscription was not acknowledged")
	}
}

func waitForServiceHomeClient(t *testing.T, service *Service, timeout time.Duration) *home.Client {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		service.homeMu.Lock()
		client := service.homeClient
		service.homeMu.Unlock()
		if client != nil {
			return client
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("service did not expose a Home client")
	return nil
}

func TestDetachHomeSubscriberLifetimeKeepsNewForwarderForStaleClient(t *testing.T) {
	staleClient := home.New(internalconfig.HomeConfig{Enabled: true})
	currentClient := home.New(internalconfig.HomeConfig{Enabled: true})
	staleRegistry := executionregistry.New()
	currentRegistry := executionregistry.New()
	staleForwarder := &testHomeLogForwarder{}
	currentForwarder := &testHomeLogForwarder{}
	service := &Service{
		homeClient:             currentClient,
		homeRegistry:           currentRegistry,
		homeLogForwarder:       currentForwarder,
		homeLogForwarderClient: currentClient,
	}

	staleForwarder.Stop()
	service.detachHomeSubscriberLifetime(staleClient, staleRegistry)

	service.homeMu.Lock()
	forwarder := service.homeLogForwarder
	forwarderClient := service.homeLogForwarderClient
	client := service.homeClient
	registry := service.homeRegistry
	service.homeMu.Unlock()
	if forwarder != currentForwarder || forwarderClient != currentClient || client != currentClient || registry != currentRegistry {
		t.Fatal("stale detach cleared the replacement Home lifetime")
	}
	if currentForwarder.stops.Load() != 0 {
		t.Fatal("stale detach stopped the replacement log forwarder")
	}
	if staleForwarder.stops.Load() != 1 {
		t.Fatal("stale forwarder ownership changed during stale detach")
	}
}

func serveHomeLogForwarderReconnectConnection(conn net.Conn, acks chan<- struct{}, stop <-chan struct{}) {
	defer func() { _ = conn.Close() }()
	reader := bufio.NewReader(conn)
	for {
		args, errRead := readRegistryTestRedisCommand(reader)
		if errRead != nil {
			return
		}
		switch {
		case len(args) > 0 && strings.EqualFold(args[0], "HELLO"):
			if _, errWrite := io.WriteString(conn, "%6\r\n$6\r\nserver\r\n$5\r\nredis\r\n$5\r\nproto\r\n:3\r\n$2\r\nid\r\n:1\r\n$4\r\nmode\r\n$10\r\nstandalone\r\n$4\r\nrole\r\n$6\r\nmaster\r\n$7\r\nmodules\r\n*0\r\n"); errWrite != nil {
				return
			}
		case len(args) >= 2 && strings.EqualFold(args[0], "GET") && args[1] == "config":
			payload := "credential-concurrency:\n  lifecycle-config-revision: 1\n  cpa-heartbeat-timeout: 100ms\n  cpa-cancel-bound: 100ms\n"
			if _, errWrite := io.WriteString(conn, fmt.Sprintf("$%d\r\n%s\r\n", len(payload), payload)); errWrite != nil {
				return
			}
		case len(args) >= 2 && strings.EqualFold(args[0], "GET") && args[1] == "plugin-tasks":
			if _, errWrite := io.WriteString(conn, "$2\r\n[]\r\n"); errWrite != nil {
				return
			}
		case len(args) >= 2 && strings.EqualFold(args[0], "SUBSCRIBE") && args[1] == "config":
			if _, errWrite := io.WriteString(conn, "*3\r\n$9\r\nsubscribe\r\n$6\r\nconfig\r\n:1\r\n"); errWrite != nil {
				return
			}
			acks <- struct{}{}
			<-stop
			return
		default:
			if _, errWrite := io.WriteString(conn, "+OK\r\n"); errWrite != nil {
				return
			}
		}
	}
}

func waitForPublisherReplacementFrame(t *testing.T, frames <-chan home.InFlightSnapshotFrame, barrierRevision int64) home.InFlightSnapshotFrame {
	t.Helper()
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	for {
		select {
		case frame := <-frames:
			if frame.BarrierRevision == barrierRevision {
				return frame
			}
		case <-timer.C:
			t.Fatalf("publisher did not send barrier revision %d", barrierRevision)
			return home.InFlightSnapshotFrame{}
		}
	}
}

func servePublisherReplacementConnection(conn net.Conn, configRequests *atomic.Int32, frames chan<- home.InFlightSnapshotFrame, firstPublisherDoneForServer <-chan (<-chan struct{}), secondConfigResult chan<- error, allowSecondConfig <-chan struct{}, stop <-chan struct{}) {
	defer func() { _ = conn.Close() }()
	reader := bufio.NewReader(conn)
	for {
		args, errRead := readRegistryTestRedisCommand(reader)
		if errRead != nil {
			return
		}
		switch {
		case len(args) > 0 && strings.EqualFold(args[0], "HELLO"):
			if _, errWrite := io.WriteString(conn, "%6\r\n$6\r\nserver\r\n$5\r\nredis\r\n$5\r\nproto\r\n:3\r\n$2\r\nid\r\n:1\r\n$4\r\nmode\r\n$10\r\nstandalone\r\n$4\r\nrole\r\n$6\r\nmaster\r\n$7\r\nmodules\r\n*0\r\n"); errWrite != nil {
				return
			}
		case len(args) >= 2 && strings.EqualFold(args[0], "GET") && args[1] == "config":
			request := int(configRequests.Add(1))
			if request == 2 {
				var firstPublisherDone <-chan struct{}
				select {
				case firstPublisherDone = <-firstPublisherDoneForServer:
				case <-stop:
					return
				}
				select {
				case <-firstPublisherDone:
					secondConfigResult <- nil
				default:
					secondConfigResult <- errors.New("replacement began its config lifetime before the previous publisher exited")
				}
				select {
				case <-allowSecondConfig:
				case <-stop:
					return
				}
			}
			barrierRevision := 11
			if request == 2 {
				barrierRevision = 22
			}
			payload := fmt.Sprintf("credential-concurrency:\n  lifecycle-config-revision: %d\n  observation-barrier-revision: %d\n  cpa-heartbeat-timeout: 100ms\n  cpa-cancel-bound: 100ms\ncredential-in-flight:\n  snapshot-interval: 10ms\n", request, barrierRevision)
			writeRegistryTestConfig(conn, payload)
		case len(args) >= 2 && strings.EqualFold(args[0], "GET") && args[1] == "plugin-tasks":
			if _, errWrite := io.WriteString(conn, "$2\r\n[]\r\n"); errWrite != nil {
				return
			}
		case len(args) > 0 && strings.EqualFold(args[0], "PING"):
			if _, errWrite := io.WriteString(conn, "+PONG\r\n"); errWrite != nil {
				return
			}
		case len(args) >= 2 && strings.EqualFold(args[0], "SUBSCRIBE") && args[1] == "config":
			if _, errWrite := io.WriteString(conn, "*3\r\n$9\r\nsubscribe\r\n$6\r\nconfig\r\n:1\r\n"); errWrite != nil {
				return
			}
			select {
			case <-stop:
				return
			case <-time.After(time.Second):
				return
			}
		case len(args) >= 3 && strings.EqualFold(args[0], "LPUSH") && args[1] == "in-flight-snapshot":
			var frame home.InFlightSnapshotFrame
			if errUnmarshal := json.Unmarshal([]byte(args[2]), &frame); errUnmarshal != nil {
				return
			}
			select {
			case frames <- frame:
			case <-stop:
				return
			}
			if _, errWrite := io.WriteString(conn, ":1\r\n"); errWrite != nil {
				return
			}
		default:
			if _, errWrite := io.WriteString(conn, "+OK\r\n"); errWrite != nil {
				return
			}
		}
	}
}

func servePreACKReplacementConnection(conn net.Conn, configRequests *atomic.Int32, firstSubscribed chan struct{}, secondStarted chan struct{}, secondStartedBeforeFirstDone chan struct{}, firstDone <-chan (<-chan struct{}), stop chan struct{}) {
	defer func() { _ = conn.Close() }()
	reader := bufio.NewReader(conn)
	for {
		args, errRead := readRegistryTestRedisCommand(reader)
		if errRead != nil {
			return
		}
		switch {
		case len(args) > 0 && strings.EqualFold(args[0], "HELLO"):
			if _, errWrite := io.WriteString(conn, "%6\r\n$6\r\nserver\r\n$5\r\nredis\r\n$5\r\nproto\r\n:3\r\n$2\r\nid\r\n:1\r\n$4\r\nmode\r\n$10\r\nstandalone\r\n$4\r\nrole\r\n$6\r\nmaster\r\n$7\r\nmodules\r\n*0\r\n"); errWrite != nil {
				return
			}
		case len(args) >= 2 && strings.EqualFold(args[0], "GET") && args[1] == "config":
			if configRequests.Add(1) > 1 {
				supervisorDone := <-firstDone
				select {
				case <-supervisorDone:
				default:
					close(secondStartedBeforeFirstDone)
				}
				close(secondStarted)
			}
			payload := "credential-concurrency:\n  lifecycle-config-revision: 1\n  cpa-heartbeat-timeout: 100ms\n  cpa-cancel-bound: 100ms\n"
			if _, errWrite := io.WriteString(conn, fmt.Sprintf("$%d\r\n%s\r\n", len(payload), payload)); errWrite != nil {
				return
			}
		case len(args) >= 2 && strings.EqualFold(args[0], "SUBSCRIBE") && args[1] == "config":
			if configRequests.Load() == 1 {
				close(firstSubscribed)
			}
			select {
			case <-stop:
				return
			case <-time.After(time.Second):
				return
			}
		default:
			if _, errWrite := io.WriteString(conn, "+OK\r\n"); errWrite != nil {
				return
			}
		}
	}
}

func serveSuccessChainHomeConnection(conn net.Conn, configMu *sync.Mutex, configRequests *int, firstAck chan struct{}, loseFirst chan struct{}, preAckAttempts chan time.Time, finalSubscribe chan struct{}, allowFinalAck chan struct{}, stop chan struct{}) {
	defer func() { _ = conn.Close() }()
	reader := bufio.NewReader(conn)
	for {
		args, errRead := readRegistryTestRedisCommand(reader)
		if errRead != nil {
			return
		}
		switch {
		case len(args) > 0 && strings.EqualFold(args[0], "HELLO"):
			if _, errWrite := io.WriteString(conn, "%6\r\n$6\r\nserver\r\n$5\r\nredis\r\n$5\r\nproto\r\n:3\r\n$2\r\nid\r\n:1\r\n$4\r\nmode\r\n$10\r\nstandalone\r\n$4\r\nrole\r\n$6\r\nmaster\r\n$7\r\nmodules\r\n*0\r\n"); errWrite != nil {
				return
			}
		case len(args) >= 2 && strings.EqualFold(args[0], "GET") && args[1] == "config":
			configMu.Lock()
			*configRequests++
			request := *configRequests
			configMu.Unlock()
			if request == 2 || request == 3 {
				preAckAttempts <- time.Now()
				_, _ = io.WriteString(conn, "-ERR unavailable\r\n")
				return
			}
			payload := "credential-concurrency:\n  lifecycle-config-revision: 1\n  cpa-heartbeat-timeout: 100ms\n  cpa-cancel-bound: 100ms\n"
			if _, errWrite := io.WriteString(conn, fmt.Sprintf("$%d\r\n%s\r\n", len(payload), payload)); errWrite != nil {
				return
			}
		case len(args) >= 2 && strings.EqualFold(args[0], "GET") && args[1] == "plugin-tasks":
			if _, errWrite := io.WriteString(conn, "$2\r\n[]\r\n"); errWrite != nil {
				return
			}
		case len(args) >= 2 && strings.EqualFold(args[0], "SUBSCRIBE") && args[1] == "config":
			configMu.Lock()
			request := *configRequests
			configMu.Unlock()
			if request == 1 {
				if _, errWrite := io.WriteString(conn, "*3\r\n$9\r\nsubscribe\r\n$6\r\nconfig\r\n:1\r\n"); errWrite != nil {
					return
				}
				close(firstAck)
				select {
				case <-loseFirst:
					<-stop
				case <-stop:
				}
				return
			}
			close(finalSubscribe)
			select {
			case <-allowFinalAck:
			case <-stop:
				return
			}
			if _, errWrite := io.WriteString(conn, "*3\r\n$9\r\nsubscribe\r\n$6\r\nconfig\r\n:1\r\n"); errWrite != nil {
				return
			}
			<-stop
			return
		default:
			if _, errWrite := io.WriteString(conn, "+OK\r\n"); errWrite != nil {
				return
			}
		}
	}
}

func serveStalePreACKPluginConnection(conn net.Conn, subscriptions *atomic.Int32, pluginWrites *atomic.Int32, firstSubscribed chan struct{}, secondSubscribed chan struct{}, allowSecondAck chan struct{}, stop chan struct{}) {
	defer func() { _ = conn.Close() }()
	reader := bufio.NewReader(conn)
	for {
		args, errRead := readRegistryTestRedisCommand(reader)
		if errRead != nil {
			return
		}
		switch {
		case len(args) > 0 && strings.EqualFold(args[0], "HELLO"):
			if _, errWrite := io.WriteString(conn, "%6\r\n$6\r\nserver\r\n$5\r\nredis\r\n$5\r\nproto\r\n:3\r\n$2\r\nid\r\n:1\r\n$4\r\nmode\r\n$10\r\nstandalone\r\n$4\r\nrole\r\n$6\r\nmaster\r\n$7\r\nmodules\r\n*0\r\n"); errWrite != nil {
				return
			}
		case len(args) >= 2 && strings.EqualFold(args[0], "GET") && args[1] == "config":
			payload := "credential-concurrency:\n  lifecycle-config-revision: 1\n  cpa-heartbeat-timeout: 100ms\n  cpa-cancel-bound: 100ms\nplugins:\n  enabled: true\n"
			if _, errWrite := io.WriteString(conn, fmt.Sprintf("$%d\r\n%s\r\n", len(payload), payload)); errWrite != nil {
				return
			}
		case len(args) >= 2 && strings.EqualFold(args[0], "GET") && args[1] == "plugin-sync":
			payload := fmt.Sprintf(`{"schema_version":1,"expires_at":%q,"items":[]}`, time.Now().Add(time.Minute).UTC().Format(time.RFC3339Nano))
			if _, errWrite := io.WriteString(conn, fmt.Sprintf("$%d\r\n%s\r\n", len(payload), payload)); errWrite != nil {
				return
			}
		case len(args) >= 2 && strings.EqualFold(args[0], "GET") && args[1] == "plugin-tasks":
			if _, errWrite := io.WriteString(conn, "$-1\r\n"); errWrite != nil {
				return
			}
		case len(args) > 0 && strings.EqualFold(args[0], "PING"):
			if _, errWrite := io.WriteString(conn, "+PONG\r\n"); errWrite != nil {
				return
			}
		case len(args) >= 2 && strings.EqualFold(args[0], "RPUSH") && args[1] == "plugin-status":
			pluginWrites.Add(1)
			if _, errWrite := io.WriteString(conn, ":1\r\n"); errWrite != nil {
				return
			}
		case len(args) >= 2 && strings.EqualFold(args[0], "SUBSCRIBE") && args[1] == "config":
			subscription := subscriptions.Add(1)
			switch subscription {
			case 1:
				close(firstSubscribed)
				<-stop
				return
			case 2:
				close(secondSubscribed)
				select {
				case <-allowSecondAck:
				case <-stop:
					return
				}
				if _, errWrite := io.WriteString(conn, "*3\r\n$9\r\nsubscribe\r\n$6\r\nconfig\r\n:1\r\n"); errWrite != nil {
					return
				}
				<-stop
				return
			}
		default:
			if _, errWrite := io.WriteString(conn, "+OK\r\n"); errWrite != nil {
				return
			}
		}
	}
}

func serveInitialOverlayPluginConnection(conn net.Conn, pluginSync chan struct{}, pluginStatus chan struct{}, pluginTasks chan struct{}, freshCommandProbe chan struct{}, allowAck chan struct{}, stop chan struct{}) {
	defer func() { _ = conn.Close() }()
	reader := bufio.NewReader(conn)
	for {
		args, errRead := readRegistryTestRedisCommand(reader)
		if errRead != nil {
			return
		}
		switch {
		case len(args) > 0 && strings.EqualFold(args[0], "HELLO"):
			if _, errWrite := io.WriteString(conn, "%6\r\n$6\r\nserver\r\n$5\r\nredis\r\n$5\r\nproto\r\n:3\r\n$2\r\nid\r\n:1\r\n$4\r\nmode\r\n$10\r\nstandalone\r\n$4\r\nrole\r\n$6\r\nmaster\r\n$7\r\nmodules\r\n*0\r\n"); errWrite != nil {
				return
			}
		case len(args) >= 2 && strings.EqualFold(args[0], "GET") && args[1] == "config":
			payload := "credential-concurrency:\n  lifecycle-config-revision: 1\n  cpa-heartbeat-timeout: 100ms\n  cpa-cancel-bound: 100ms\nplugins:\n  enabled: true\n"
			if _, errWrite := io.WriteString(conn, fmt.Sprintf("$%d\r\n%s\r\n", len(payload), payload)); errWrite != nil {
				return
			}
		case len(args) >= 2 && strings.EqualFold(args[0], "GET") && args[1] == "plugin-sync":
			if home.Current() != nil {
				return
			}
			close(pluginSync)
			payload := fmt.Sprintf(`{"schema_version":1,"expires_at":%q,"items":[]}`, time.Now().Add(time.Minute).UTC().Format(time.RFC3339Nano))
			if _, errWrite := io.WriteString(conn, fmt.Sprintf("$%d\r\n%s\r\n", len(payload), payload)); errWrite != nil {
				return
			}
		case len(args) >= 2 && strings.EqualFold(args[0], "RPUSH") && args[1] == "plugin-status":
			select {
			case <-freshCommandProbe:
			default:
				return
			}
			pluginStatus <- struct{}{}
			if _, errWrite := io.WriteString(conn, ":1\r\n"); errWrite != nil {
				return
			}
		case len(args) >= 2 && strings.EqualFold(args[0], "GET") && args[1] == "plugin-tasks":
			close(pluginTasks)
			payload := `[{"id":1,"operation":"delete","plugin_id":"plugin-a"}]`
			if _, errWrite := io.WriteString(conn, fmt.Sprintf("$%d\r\n%s\r\n", len(payload), payload)); errWrite != nil {
				return
			}
		case len(args) > 0 && strings.EqualFold(args[0], "PING"):
			close(freshCommandProbe)
			if _, errWrite := io.WriteString(conn, "+PONG\r\n"); errWrite != nil {
				return
			}
		case len(args) >= 2 && strings.EqualFold(args[0], "SUBSCRIBE") && args[1] == "config":
			select {
			case <-allowAck:
			case <-stop:
				return
			}
			if _, errWrite := io.WriteString(conn, "*3\r\n$9\r\nsubscribe\r\n$6\r\nconfig\r\n:1\r\n"); errWrite != nil {
				return
			}
			<-stop
			return
		default:
			if _, errWrite := io.WriteString(conn, "+OK\r\n"); errWrite != nil {
				return
			}
		}
	}
}

func serveRegistryTestHomeConnection(conn net.Conn, subscriptionMu *sync.Mutex, subscriptions *int, firstAck chan struct{}, loseFirst chan struct{}, secondSubscribe chan struct{}, secondSubscribeOnce *sync.Once, allowSecondAck chan struct{}, stop chan struct{}) {
	defer func() { _ = conn.Close() }()
	reader := bufio.NewReader(conn)
	for {
		args, errRead := readRegistryTestRedisCommand(reader)
		if errRead != nil {
			return
		}
		switch {
		case len(args) > 0 && strings.EqualFold(args[0], "HELLO"):
			if _, errWrite := io.WriteString(conn, "%6\r\n$6\r\nserver\r\n$5\r\nredis\r\n$5\r\nproto\r\n:3\r\n$2\r\nid\r\n:1\r\n$4\r\nmode\r\n$10\r\nstandalone\r\n$4\r\nrole\r\n$6\r\nmaster\r\n$7\r\nmodules\r\n*0\r\n"); errWrite != nil {
				return
			}
		case len(args) >= 2 && strings.EqualFold(args[0], "GET") && args[1] == "config":
			payload := "credential-concurrency:\n  lifecycle-config-revision: 1\n  cpa-heartbeat-timeout: 100ms\n  cpa-cancel-bound: 100ms\n"
			if _, errWrite := io.WriteString(conn, fmt.Sprintf("$%d\r\n%s\r\n", len(payload), payload)); errWrite != nil {
				return
			}
		case len(args) >= 2 && strings.EqualFold(args[0], "GET") && args[1] == "plugin-tasks":
			if _, errWrite := io.WriteString(conn, "$2\r\n[]\r\n"); errWrite != nil {
				return
			}
		case len(args) >= 2 && strings.EqualFold(args[0], "SUBSCRIBE") && args[1] == "config":
			subscriptionMu.Lock()
			*subscriptions++
			subscription := *subscriptions
			subscriptionMu.Unlock()
			if subscription == 1 {
				if _, errWrite := io.WriteString(conn, "*3\r\n$9\r\nsubscribe\r\n$6\r\nconfig\r\n:1\r\n"); errWrite != nil {
					return
				}
				close(firstAck)
				select {
				case <-loseFirst:
					<-stop
					return
				case <-stop:
					return
				}
			}
			secondSubscribeOnce.Do(func() { close(secondSubscribe) })
			select {
			case <-allowSecondAck:
			case <-stop:
				return
			}
			if _, errWrite := io.WriteString(conn, "*3\r\n$9\r\nsubscribe\r\n$6\r\nconfig\r\n:1\r\n"); errWrite != nil {
				return
			}
			<-stop
			return
		default:
			if _, errWrite := io.WriteString(conn, "+OK\r\n"); errWrite != nil {
				return
			}
		}
	}
}

func servePreAckFailureConnection(conn net.Conn, attempts chan<- time.Time) {
	defer func() { _ = conn.Close() }()
	reader := bufio.NewReader(conn)
	for {
		args, errRead := readRegistryTestRedisCommand(reader)
		if errRead != nil {
			return
		}
		switch {
		case len(args) > 0 && strings.EqualFold(args[0], "HELLO"):
			if _, errWrite := io.WriteString(conn, "%6\r\n$6\r\nserver\r\n$5\r\nredis\r\n$5\r\nproto\r\n:3\r\n$2\r\nid\r\n:1\r\n$4\r\nmode\r\n$10\r\nstandalone\r\n$4\r\nrole\r\n$6\r\nmaster\r\n$7\r\nmodules\r\n*0\r\n"); errWrite != nil {
				return
			}
		case len(args) >= 2 && strings.EqualFold(args[0], "GET") && args[1] == "config":
			attempts <- time.Now()
			_, _ = io.WriteString(conn, "-ERR unavailable\r\n")
			return
		default:
			if _, errWrite := io.WriteString(conn, "+OK\r\n"); errWrite != nil {
				return
			}
		}
	}
}

func readRegistryTestRedisCommand(reader *bufio.Reader) ([]string, error) {
	line, errRead := reader.ReadString('\n')
	if errRead != nil {
		return nil, errRead
	}
	if !strings.HasPrefix(line, "*") {
		return nil, fmt.Errorf("unexpected RESP command header %q", line)
	}
	count, errCount := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "*")))
	if errCount != nil {
		return nil, errCount
	}
	args := make([]string, 0, count)
	for range count {
		lengthLine, errLength := reader.ReadString('\n')
		if errLength != nil {
			return nil, errLength
		}
		length, errParseLength := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(lengthLine, "$")))
		if errParseLength != nil {
			return nil, errParseLength
		}
		raw := make([]byte, length+2)
		if _, errReadRaw := io.ReadFull(reader, raw); errReadRaw != nil {
			return nil, errReadRaw
		}
		args = append(args, string(raw[:length]))
	}
	return args, nil
}

func TestServiceSkipsStaleLocalConfigRuntimeApply(t *testing.T) {
	service := &Service{cfg: &config.Config{}}
	var applied []string
	service.applyPprofConfigContextFn = func(_ context.Context, cfg *config.Config) bool {
		applied = append(applied, cfg.Routing.Strategy)
		return true
	}
	first := service.commitConfigUpdate(&config.Config{Routing: internalconfig.RoutingConfig{Strategy: "fill-first"}})
	second := service.commitConfigUpdate(&config.Config{Routing: internalconfig.RoutingConfig{Strategy: "round-robin"}})
	if !service.applyConfigRuntime(context.Background(), second, false) {
		t.Fatal("newest config runtime apply failed")
	}
	if service.applyConfigRuntime(context.Background(), first, false) {
		t.Fatal("stale config runtime apply succeeded")
	}
	if got, want := strings.Join(applied, ","), "round-robin"; got != want {
		t.Fatalf("runtime apply order = %q, want %q", got, want)
	}
}

func TestServiceAppliesSameValueNewestSelectorCommit(t *testing.T) {
	manager := coreauth.NewManager(nil, &coreauth.RoundRobinSelector{}, nil)
	manager.RegisterExecutor(serviceTestPluginExecutor{})
	for _, id := range []string{"auth-b", "auth-a"} {
		if _, errRegister := manager.Register(context.Background(), &coreauth.Auth{ID: id, Provider: "plugin-provider", Status: coreauth.StatusActive}); errRegister != nil {
			t.Fatalf("Register(%s) error = %v", id, errRegister)
		}
	}

	service := &Service{cfg: &config.Config{}, coreManager: manager}
	older := service.commitConfigUpdate(&config.Config{Routing: internalconfig.RoutingConfig{Strategy: "fill-first"}})
	newer := service.commitConfigUpdate(&config.Config{Routing: internalconfig.RoutingConfig{Strategy: "fill-first"}})
	if !service.applyConfigRuntime(context.Background(), newer, false) {
		t.Fatal("newest same-value config runtime apply failed")
	}
	if service.applyConfigRuntime(context.Background(), older, false) {
		t.Fatal("stale same-value config runtime apply succeeded")
	}

	for range 2 {
		selected, errSelect := manager.SelectAuth(context.Background(), "plugin-provider", "", cliproxyexecutor.Options{})
		if errSelect != nil {
			t.Fatalf("SelectAuth() error = %v", errSelect)
		}
		if selected == nil || selected.ID != "auth-a" {
			t.Fatalf("selector picked = %+v, want auth-a from fill-first", selected)
		}
	}
}

func TestBuilderPreservesInitialSelectorForSameRouting(t *testing.T) {
	cfg := &config.Config{
		AuthDir: t.TempDir(),
		Routing: internalconfig.RoutingConfig{
			Strategy:           "fill-first",
			SessionAffinity:    true,
			SessionAffinityTTL: "1h",
		},
	}
	service, errBuild := NewBuilder().
		WithConfig(cfg).
		WithConfigPath(t.TempDir() + "/config.yaml").
		Build()
	if errBuild != nil {
		t.Fatalf("Build() error = %v", errBuild)
	}

	initialSelector := service.coreManager.Selector()
	initialAffinity, ok := initialSelector.(*coreauth.SessionAffinitySelector)
	if !ok {
		t.Fatalf("initial selector = %T, want *SessionAffinitySelector", initialSelector)
	}
	defer initialAffinity.Stop()
	commit := service.commitConfigUpdate(cfg)
	if !service.applyConfigRuntime(context.Background(), commit, false) {
		t.Fatal("same-routing config runtime apply failed")
	}
	if got := service.coreManager.Selector(); got != initialSelector {
		t.Fatalf("same-routing selector = %p, want initial selector %p", got, initialSelector)
	}
}

func TestServiceApplyConfigRuntimePreservesSelectorForUnchangedRouting(t *testing.T) {
	manager := coreauth.NewManager(nil, &coreauth.RoundRobinSelector{}, nil)
	service := &Service{cfg: &config.Config{}, coreManager: manager}

	initial := service.commitConfigUpdate(&config.Config{Routing: internalconfig.RoutingConfig{
		Strategy:           "fill-first",
		SessionAffinity:    true,
		SessionAffinityTTL: "1h",
	}})
	if !service.applyConfigRuntime(context.Background(), initial, false) {
		t.Fatal("initial config runtime apply failed")
	}
	initialSelector := manager.Selector()
	initialAffinity, ok := initialSelector.(*coreauth.SessionAffinitySelector)
	if !ok {
		t.Fatalf("initial selector = %T, want *SessionAffinitySelector", initialSelector)
	}
	defer initialAffinity.Stop()

	older := service.commitConfigUpdate(&config.Config{Routing: internalconfig.RoutingConfig{
		Strategy:           " FILLFIRST ",
		SessionAffinity:    true,
		SessionAffinityTTL: "60m",
	}})
	newer := service.commitConfigUpdate(&config.Config{
		Routing: internalconfig.RoutingConfig{
			Strategy:           "fill-first",
			SessionAffinity:    true,
			SessionAffinityTTL: "1h",
		},
		UsageStatisticsEnabled: true,
	})
	if !service.applyConfigRuntime(context.Background(), newer, false) {
		t.Fatal("newest same-routing config runtime apply failed")
	}
	if got := manager.Selector(); got != initialSelector {
		t.Fatalf("same-routing selector = %p, want original %p", got, initialSelector)
	}
	if service.applyConfigRuntime(context.Background(), older, false) {
		t.Fatal("stale same-routing config runtime apply succeeded")
	}
	if got := manager.Selector(); got != initialSelector {
		t.Fatalf("stale same-routing selector = %p, want original %p", got, initialSelector)
	}

	changed := service.commitConfigUpdate(&config.Config{Routing: internalconfig.RoutingConfig{
		Strategy:           "round-robin",
		SessionAffinity:    true,
		SessionAffinityTTL: "1h",
	}})
	if !service.applyConfigRuntime(context.Background(), changed, false) {
		t.Fatal("changed-routing config runtime apply failed")
	}
	changedSelector := manager.Selector()
	if changedSelector == initialSelector {
		t.Fatal("changed-routing selector retained original identity")
	}
	changedAffinity, ok := changedSelector.(*coreauth.SessionAffinitySelector)
	if !ok {
		t.Fatalf("changed selector = %T, want *SessionAffinitySelector", changedSelector)
	}
	defer changedAffinity.Stop()

	unrelated := service.commitConfigUpdate(&config.Config{
		Routing: internalconfig.RoutingConfig{
			Strategy:           "round-robin",
			SessionAffinity:    true,
			SessionAffinityTTL: "1h",
		},
		UsageStatisticsEnabled: false,
	})
	if !service.applyConfigRuntime(context.Background(), unrelated, false) {
		t.Fatal("unrelated config runtime apply failed")
	}
	if got := manager.Selector(); got != changedSelector {
		t.Fatalf("unrelated-update selector = %p, want changed selector %p", got, changedSelector)
	}
}

func TestServiceSerializesHomeAndWatcherConfigRuntimeApply(t *testing.T) {
	baseCfg := &config.Config{}
	baseCfg.Home.Enabled = true
	service := &Service{cfg: baseCfg, homeGeneration: 1}
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	var appliedMu sync.Mutex
	var applied []string
	service.applyPprofConfigContextFn = func(_ context.Context, cfg *config.Config) bool {
		if cfg.Routing.Strategy == "fill-first" {
			close(firstStarted)
			<-releaseFirst
		}
		appliedMu.Lock()
		applied = append(applied, cfg.Routing.Strategy)
		appliedMu.Unlock()
		return true
	}
	client, _ := newHomePluginTaskTestClient(t, nil, 0)
	queue := newHomeConfigWorkQueue()
	queue.enqueue([]byte("routing:\n  strategy: fill-first\n"))
	ready := make(chan struct{})
	close(ready)
	lifetimeCtx, cancelLifetime := context.WithCancel(context.Background())
	defer cancelLifetime()
	cancelBound := atomic.Int64{}
	cancelBound.Store(int64(time.Second))
	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		service.runHomeConfigWorker(lifetimeCtx, context.Background(), 1, client, executionregistry.New(), queue, ready, &atomic.Bool{}, &cancelBound)
	}()
	select {
	case <-firstStarted:
	case <-time.After(time.Second):
		t.Fatal("Home config runtime apply did not start")
	}

	watcherDone := make(chan struct{})
	go func() {
		service.applyWatcherConfigUpdate(&config.Config{Routing: internalconfig.RoutingConfig{Strategy: "round-robin"}})
		close(watcherDone)
	}()
	select {
	case <-watcherDone:
		t.Fatal("watcher runtime apply completed before the older Home apply")
	case <-time.After(100 * time.Millisecond):
	}
	close(releaseFirst)
	select {
	case <-watcherDone:
	case <-time.After(time.Second):
		t.Fatal("watcher runtime apply did not finish")
	}
	appliedMu.Lock()
	got := strings.Join(applied, ",")
	appliedMu.Unlock()
	if want := "fill-first,round-robin"; got != want {
		t.Fatalf("runtime completion order = %q, want %q", got, want)
	}
	cancelLifetime()
	select {
	case <-workerDone:
	case <-time.After(time.Second):
		t.Fatal("Home config worker did not stop")
	}
}
