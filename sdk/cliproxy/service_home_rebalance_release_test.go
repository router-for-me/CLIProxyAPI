package cliproxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/home"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/homeplugins"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executionregistry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func TestServiceRebalanceReleaseOwnershipAcrossCancellation(t *testing.T) {
	for _, mode := range []string{"success", "shutdown", "replacement"} {
		for _, pluginFinalization := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/plugins=%v", mode, pluginFinalization), func(t *testing.T) {
				source := newRebalanceAdmissionHome(t, false)
				target := newRebalanceAdmissionHome(t, false)
				ack := make(chan struct{})
				var ackOnce sync.Once
				source.mu.Lock()
				source.target = target.listener.Addr().String()
				if mode != "success" {
					source.releaseACK = ack
				}
				source.mu.Unlock()
				if mode == "success" {
					target.mu.Lock()
					target.releaseACK = ack
					target.mu.Unlock()
				}
				forwarder := &rebalanceAdmissionForwarder{bound: make(chan *home.Client, 4), detached: make(chan *home.Client, 4)}
				previousStart := startHomeLogForwarder
				startHomeLogForwarder = func(int) homeLogForwarder { return forwarder }
				t.Cleanup(func() { startHomeLogForwarder = previousStart })
				service := newRegistryTestService(t, source.listener)
				service.cfg.Home.DisableClusterDiscovery = false
				service.cfg.Home.NodeID = "source-node"
				finalizing := make(chan context.Context, 1)
				service.homePluginDeleteTask = func(ctx context.Context, _ *config.Config, _ home.PluginTask) homeplugins.SyncReport {
					finalizing <- ctx
					<-ctx.Done()
					return homeplugins.SyncReport{Task: "delete", Error: ctx.Err().Error()}
				}
				ctx, cancel := context.WithCancel(context.Background())
				var canceledBySupervisor atomic.Int32
				service.runCancel = func() { canceledBySupervisor.Add(1); cancel() }
				service.startHomeSubscriber(ctx)
				var replacementDone chan struct{}
				t.Cleanup(func() {
					ackOnce.Do(func() { close(ack) })
					cancel()
					if replacementDone != nil {
						awaitRebalanceEvent(t, replacementDone)
					}
					awaitRebalanceEvent(t, service.homeSupervisor.done)
					home.ClearCurrent()
				})
				client := awaitRebalanceEvent(t, forwarder.bound)
				awaitRebalanceEvent(t, source.subscribed)
				service.homeMu.Lock()
				registry := service.homeRegistry
				service.homeMu.Unlock()
				oldSupervisor := service.homeSupervisor
				activePending, errBegin := registry.BeginDispatch()
				if errBegin != nil {
					t.Fatal(errBegin)
				}
				active, errInstall := registry.Install(activePending, executionregistry.ScopeSpec{
					RequestID: "accounted", CredentialID: "credential-a", Model: "test-model", Accounted: true,
				})
				if errInstall != nil {
					t.Fatal(errInstall)
				}
				t.Cleanup(func() { active.End("test cleanup") })
				drained := make(chan struct{})
				tickets := make(chan *executionregistry.ReleaseTicket, 1)
				if errBind := active.Bind(func() error {
					close(drained)
					go func() { tickets <- active.EndWithRelease("drained") }()
					return nil
				}); errBind != nil {
					t.Fatal(errBind)
				}
				pending, errBegin := registry.BeginDispatch()
				if errBegin != nil {
					t.Fatal(errBegin)
				}
				result := make(chan error, 1)
				go func() {
					defer pending.End()
					_, errDispatch := client.RPopAuth(context.Background(), "test-model", "", nil, 1)
					if home.IsAmbiguousDispatchError(errDispatch) {
						client.AbortAmbiguousDispatch()
					}
					result <- errDispatch
				}()
				t.Cleanup(client.Close)
				awaitRebalanceEvent(t, source.issued)
				if pluginFinalization {
					source.pluginTasks.Store(true)
					source.configUpdates <- "credential-concurrency:\n  lifecycle-config-revision: 2\n  cpa-heartbeat-timeout: 10s\n  cpa-cancel-bound: 5s\n"
					awaitRebalanceEvent(t, finalizing)
					source.pluginTasks.Store(false)
				}
				close(source.migrate)
				deadline := time.NewTimer(5 * time.Second)
				defer deadline.Stop()
				for {
					_, errFence := client.RPopAuth(context.Background(), "", "", nil, 1)
					if errors.Is(errFence, home.ErrDispatchFenced) {
						break
					}
					select {
					case <-deadline.C:
						t.Fatal("migration did not fence admission")
					default:
						runtime.Gosched()
					}
				}
				switch mode {
				case "shutdown":
					cancel()
				case "replacement":
					// Cancel synchronously at the pending wait, then run the normal replacement entrypoint.
					oldSupervisor.cancel()
					source.mu.Lock()
					source.target = ""
					source.mu.Unlock()
					replacementDone = make(chan struct{})
					go func() { service.startHomeSubscriber(ctx); close(replacementDone) }()
				}
				close(source.reply)
				if errDispatch := awaitRebalanceEvent(t, result); errDispatch != nil {
					t.Fatalf("cancellation interrupted issued dispatch: %v", errDispatch)
				}
				owner := source
				if mode == "success" {
					awaitRebalanceEvent(t, target.subscribed)
					next := awaitRebalanceEvent(t, forwarder.bound)
					service.homeMu.Lock()
					nextRegistry := service.homeRegistry
					service.homeMu.Unlock()
					if next == client || !next.HeartbeatOK() || nextRegistry != registry {
						t.Fatal("successful migration did not preserve active execution ownership")
					}
					select {
					case <-drained:
						t.Fatal("successful migration drained the accounted execution")
					default:
					}
					active.End("completed at destination")
					owner = target
				}
				ticket := awaitRebalanceEvent(t, tickets)
				if ticket == nil {
					t.Fatal("accounted execution lost its release ticket")
				}
				var args []string
				select {
				case args = <-owner.releases:
				case <-oldSupervisor.done:
					t.Fatalf("supervisor exited without final release: canceled service=%d", canceledBySupervisor.Load())
				case <-time.After(7 * time.Second):
					t.Fatal("release was not sent to the membership owner")
				}
				var frame home.ConcurrencyReleaseFrame
				if errJSON := json.Unmarshal([]byte(args[2]), &frame); errJSON != nil {
					t.Fatal(errJSON)
				}
				if frame != (home.ConcurrencyReleaseFrame{CredentialID: "credential-a", Model: "test-model", ReleaseSeq: 1}) {
					t.Fatalf("unexpected release: %#v", frame)
				}
				if mode != "success" {
					select {
					case <-oldSupervisor.done:
						t.Fatal("cleanup completed before the final release was acknowledged")
					case <-target.subscribed:
						t.Fatal("canceled migration contacted the target subscription")
					default:
					}
				} else {
					select {
					case <-source.releases:
						t.Fatal("completed migration released through its former source")
					default:
					}
				}
				ackOnce.Do(func() { close(ack) })
				waitCtx, cancelWait := context.WithTimeout(context.Background(), time.Second)
				defer cancelWait()
				if errTicket := ticket.Wait(waitCtx); errTicket != nil {
					t.Fatal(errTicket)
				}
				if mode != "success" {
					awaitRebalanceEvent(t, oldSupervisor.done)
				}
				if mode == "replacement" {
					awaitRebalanceEvent(t, replacementDone)
					next := awaitRebalanceEvent(t, forwarder.bound)
					if next == client || !next.HeartbeatOK() {
						t.Fatal("replacement was not published")
					}
				}
				if canceledBySupervisor.Load() != 0 || (mode != "shutdown" && ctx.Err() != nil) {
					t.Fatal("final release failed and canceled the parent service")
				}
				if _, errLate := client.RPopAuth(context.Background(), "late", "", nil, 1); !errors.Is(errLate, home.ErrDispatchFenced) {
					t.Fatalf("old lifetime reopened dispatch: %v", errLate)
				}
			})
		}
	}
}
