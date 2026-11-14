package cliproxy

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/home"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executionregistry"
)

func TestServiceNormalReplacementDrainsAndFlushesAccountedExecution(t *testing.T) {
	server := newRebalanceAdmissionHome(t, false)
	releaseACK := make(chan struct{})
	var ackOnce sync.Once
	server.mu.Lock()
	server.releaseACK = releaseACK
	server.mu.Unlock()
	forwarder := &rebalanceAdmissionForwarder{bound: make(chan *home.Client, 4), detached: make(chan *home.Client, 4)}
	previousStart := startHomeLogForwarder
	startHomeLogForwarder = func(int) homeLogForwarder { return forwarder }
	t.Cleanup(func() { startHomeLogForwarder = previousStart })
	service := newRegistryTestService(t, server.listener)
	ctx, cancel := context.WithCancel(context.Background())
	service.runCancel = cancel
	service.startHomeSubscriber(ctx)
	var replacementDone chan struct{}
	t.Cleanup(func() {
		ackOnce.Do(func() { close(releaseACK) })
		cancel()
		if replacementDone != nil {
			awaitRebalanceEvent(t, replacementDone)
		}
		awaitRebalanceEvent(t, service.homeSupervisor.done)
		home.ClearCurrent()
	})
	client := awaitRebalanceEvent(t, forwarder.bound)
	awaitRebalanceEvent(t, server.subscribed)
	service.homeMu.Lock()
	registry := service.homeRegistry
	service.homeMu.Unlock()
	oldSupervisor := service.homeSupervisor
	if registry == nil || !client.HeartbeatOK() {
		t.Fatal("initial subscriber is not ready")
	}
	pending, errBegin := registry.BeginDispatch()
	if errBegin != nil {
		t.Fatal(errBegin)
	}
	scope, errInstall := registry.Install(pending, executionregistry.ScopeSpec{
		RequestID: "accounted-request", CredentialID: "credential-a", Model: "test-model", Accounted: true,
	})
	if errInstall != nil {
		t.Fatal(errInstall)
	}
	t.Cleanup(func() { scope.End("test cleanup") })
	draining := make(chan struct{})
	tickets := make(chan *executionregistry.ReleaseTicket, 1)
	if errBind := scope.Bind(func() error {
		close(draining)
		go func() { tickets <- scope.EndWithRelease("normal replacement") }()
		return nil
	}); errBind != nil {
		t.Fatal(errBind)
	}

	replacementDone = make(chan struct{})
	go func() {
		service.startHomeSubscriber(ctx)
		close(replacementDone)
	}()
	awaitRebalanceEvent(t, draining)
	ticket := awaitRebalanceEvent(t, tickets)
	if ticket == nil {
		t.Fatal("accounted execution did not produce a release ticket")
	}
	var args []string
	select {
	case args = <-server.releases:
	case <-oldSupervisor.done:
		t.Fatalf("old supervisor ended without sending its final release: service error=%v", ctx.Err())
	case <-ctx.Done():
		t.Fatalf("normal replacement canceled the entire service: %v", ctx.Err())
	case <-time.After(7 * time.Second):
		t.Fatal("normal replacement did not send its final release")
	}
	var frame home.ConcurrencyReleaseFrame
	if errJSON := json.Unmarshal([]byte(args[2]), &frame); errJSON != nil {
		t.Fatal(errJSON)
	}
	want := home.ConcurrencyReleaseFrame{CredentialID: "credential-a", Model: "test-model", ReleaseSeq: 1}
	if frame != want {
		t.Fatalf("release = %#v, want %#v", frame, want)
	}
	if client.HeartbeatOK() || client.AmbiguousDispatch() || ctx.Err() != nil {
		t.Fatal("normal cancellation retained heartbeat, introduced ambiguity, or canceled the service")
	}
	select {
	case <-oldSupervisor.done:
		t.Fatal("old supervisor finished before release acknowledgement")
	case <-replacementDone:
		t.Fatal("replacement finished before release acknowledgement")
	case <-server.subscribed:
		t.Fatal("new subscription started before release acknowledgement")
	default:
	}
	if late, errLate := registry.BeginDispatch(); !errors.Is(errLate, executionregistry.ErrRegistryNotAccepting) {
		if late != nil {
			late.End()
		}
		t.Fatalf("drained registry accepted new dispatch: %v", errLate)
	}

	ackOnce.Do(func() { close(releaseACK) })
	awaitRebalanceEvent(t, oldSupervisor.done)
	awaitRebalanceEvent(t, replacementDone)
	next := awaitRebalanceEvent(t, forwarder.bound)
	awaitRebalanceEvent(t, server.subscribed)
	if ctx.Err() != nil || next == client || !next.HeartbeatOK() {
		t.Fatalf("replacement did not keep service running: error=%v", ctx.Err())
	}
	waitCtx, cancelWait := context.WithTimeout(context.Background(), time.Second)
	defer cancelWait()
	if errTicket := ticket.Wait(waitCtx); errTicket != nil {
		t.Fatalf("final release ticket was not acknowledged: %v", errTicket)
	}
	if errRelease := client.PushConcurrencyRelease(context.Background(), want); !errors.Is(errRelease, home.ErrDispatchFenced) {
		t.Fatalf("old client remained open after flush: %v", errRelease)
	}
	service.homeMu.Lock()
	nextRegistry := service.homeRegistry
	service.homeMu.Unlock()
	if nextRegistry == nil || nextRegistry == registry {
		t.Fatal("normal replacement did not publish a fresh registry")
	}
}
