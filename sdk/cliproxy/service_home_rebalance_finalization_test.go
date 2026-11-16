package cliproxy

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/home"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/homeplugins"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executionregistry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func TestServicePlannedRebalanceSettlesDispatchBeforeCancelingPluginFinalization(t *testing.T) {
	source := newRebalanceAdmissionHome(t, false)
	target := newRebalanceAdmissionHome(t, false)
	source.mu.Lock()
	source.target = target.listener.Addr().String()
	source.mu.Unlock()
	forwarder := &rebalanceAdmissionForwarder{bound: make(chan *home.Client, 4), detached: make(chan *home.Client, 4)}
	previousStart := startHomeLogForwarder
	startHomeLogForwarder = func(int) homeLogForwarder { return forwarder }
	t.Cleanup(func() { startHomeLogForwarder = previousStart })
	service := newRegistryTestService(t, source.listener)
	service.cfg.Home.DisableClusterDiscovery = false
	service.cfg.Home.NodeID = "source-node"
	finalizing := make(chan context.Context, 1)
	finalizationCanceled := make(chan struct{})
	allowFinalizationReturn := make(chan struct{})
	var returnOnce sync.Once
	serviceCtx, cancelService := context.WithCancel(context.Background())
	service.homePluginDeleteTask = func(ctx context.Context, _ *config.Config, _ home.PluginTask) homeplugins.SyncReport {
		finalizing <- ctx
		<-ctx.Done()
		close(finalizationCanceled)
		select {
		case <-allowFinalizationReturn:
		case <-serviceCtx.Done():
		}
		return homeplugins.SyncReport{Task: "delete", Error: ctx.Err().Error()}
	}
	service.runCancel = cancelService
	service.startHomeSubscriber(serviceCtx)
	t.Cleanup(func() {
		returnOnce.Do(func() { close(allowFinalizationReturn) })
		cancelService()
		awaitRebalanceEvent(t, service.homeSupervisor.done)
		home.ClearCurrent()
	})
	client := awaitRebalanceEvent(t, forwarder.bound)
	t.Cleanup(client.Close)
	service.homeMu.Lock()
	registry := service.homeRegistry
	service.homeMu.Unlock()
	pendingActive, errBegin := registry.BeginDispatch()
	if errBegin != nil {
		t.Fatal(errBegin)
	}
	active, errInstall := registry.Install(pendingActive, executionregistry.ScopeSpec{RequestID: "running-model"})
	if errInstall != nil {
		t.Fatal(errInstall)
	}
	t.Cleanup(func() { active.End("test cleanup") })
	drained := make(chan struct{})
	if errBind := active.Bind(func() error {
		close(drained)
		go active.End("drained")
		return nil
	}); errBind != nil {
		t.Fatal(errBind)
	}
	pending, errBegin := registry.BeginDispatch()
	if errBegin != nil {
		t.Fatal(errBegin)
	}
	allowProcessing := make(chan struct{})
	var processOnce sync.Once
	t.Cleanup(func() { processOnce.Do(func() { close(allowProcessing) }) })
	response := make(chan error, 1)
	processed := make(chan error, 1)
	go func() {
		defer pending.End()
		_, errDispatch := client.RPopAuth(serviceCtx, "test-model", "", nil, 1)
		response <- errDispatch
		<-allowProcessing
		if home.IsAmbiguousDispatchError(errDispatch) {
			client.AbortAmbiguousDispatch()
		}
		if errDispatch == nil {
			scope, errScope := registry.Install(pending, executionregistry.ScopeSpec{RequestID: "received-dispatch"})
			errDispatch = errScope
			if scope != nil {
				scope.End("processed")
			}
		}
		processed <- errDispatch
	}()
	awaitRebalanceEvent(t, source.issued)
	source.pluginTasks.Store(true)
	source.configUpdates <- "credential-concurrency:\n  lifecycle-config-revision: 2\n  cpa-heartbeat-timeout: 10s\n  cpa-cancel-bound: 5s\n"
	finalizationCtx := awaitRebalanceEvent(t, finalizing)
	close(source.migrate)
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		_, errFence := client.RPopAuth(serviceCtx, "", "", nil, 1)
		if errors.Is(errFence, home.ErrDispatchFenced) {
			break
		}
		select {
		case <-deadline.C:
			t.Fatal("planned migration did not fence dispatch admission")
		default:
			runtime.Gosched()
		}
	}
	if finalizationCtx.Err() != nil {
		t.Fatal("plugin finalization was canceled while its close handler could interrupt the pending dispatch")
	}
	select {
	case <-source.transportEnded:
		t.Fatal("plugin finalization closed the pending RPOP connection")
	case <-drained:
		t.Fatal("planned migration drained a running model")
	default:
	}
	close(source.reply)
	if errResponse := awaitRebalanceEvent(t, response); errResponse != nil {
		t.Fatalf("pending RPOP was interrupted: %v", errResponse)
	}
	if finalizationCtx.Err() != nil {
		t.Fatal("plugin finalization was canceled before CPA processed the dispatch response")
	}
	processOnce.Do(func() { close(allowProcessing) })
	if errProcessed := awaitRebalanceEvent(t, processed); errProcessed != nil {
		t.Fatalf("response processing failed: %v", errProcessed)
	}
	awaitRebalanceEvent(t, finalizationCanceled)
	returnOnce.Do(func() { close(allowFinalizationReturn) })
	subscription := awaitRebalanceEvent(t, target.subscribed)
	next := awaitRebalanceEvent(t, forwarder.bound)
	service.homeMu.Lock()
	nextRegistry := service.homeRegistry
	service.homeMu.Unlock()
	if client.AmbiguousDispatch() || nextRegistry != registry || next == client || !next.HeartbeatOK() || serviceCtx.Err() != nil {
		t.Fatal("planned migration did not preserve the healthy registry and service")
	}
	if len(subscription) < 4 || subscription[3] != "takeover" {
		t.Fatalf("replacement subscription lost takeover: %#v", subscription)
	}
	select {
	case <-drained:
		t.Fatal("plugin finalization cancellation drained the running model")
	default:
	}
}
