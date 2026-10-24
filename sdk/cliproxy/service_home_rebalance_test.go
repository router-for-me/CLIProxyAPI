package cliproxy

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/home"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executionregistry"
)

// rebalanceAdmissionHome controls subscription migration and the delivery of an issued RPOP.
type rebalanceAdmissionHome struct {
	listener       net.Listener
	target         string
	migrate        chan struct{}
	reply          chan struct{}
	stop           chan struct{}
	subscribed     chan []string
	configUpdates  chan string
	pluginTasks    atomic.Bool
	releases       chan []string
	releaseACK     <-chan struct{}
	issued         chan struct{}
	transportEnded chan struct{}
	ambiguous      bool
	rpops          atomic.Int32
	mu             sync.Mutex
	connections    map[net.Conn]struct{}
	workers        sync.WaitGroup
}

func newRebalanceAdmissionHome(t *testing.T, ambiguous bool) *rebalanceAdmissionHome {
	t.Helper()
	listener, errListen := net.Listen("tcp", "127.0.0.1:0")
	if errListen != nil {
		t.Fatal(errListen)
	}
	server := &rebalanceAdmissionHome{
		listener: listener, ambiguous: ambiguous,
		migrate: make(chan struct{}), reply: make(chan struct{}), stop: make(chan struct{}),
		subscribed: make(chan []string, 4), configUpdates: make(chan string, 1), releases: make(chan []string, 4),
		issued: make(chan struct{}), transportEnded: make(chan struct{}),
		connections: make(map[net.Conn]struct{}),
	}
	accepted := make(chan struct{})
	go func() {
		defer close(accepted)
		for {
			conn, errAccept := listener.Accept()
			if errAccept != nil {
				return
			}
			server.mu.Lock()
			server.connections[conn] = struct{}{}
			server.mu.Unlock()
			server.workers.Add(1)
			go server.serve(conn)
		}
	}()
	t.Cleanup(func() {
		close(server.stop)
		_ = listener.Close()
		<-accepted
		server.mu.Lock()
		for conn := range server.connections {
			_ = conn.Close()
		}
		server.mu.Unlock()
		server.workers.Wait()
	})
	return server
}

func (s *rebalanceAdmissionHome) serve(conn net.Conn) {
	defer s.workers.Done()
	defer func() {
		_ = conn.Close()
		s.mu.Lock()
		delete(s.connections, conn)
		s.mu.Unlock()
	}()
	reader := bufio.NewReader(conn)
	for {
		args, errRead := readRegistryTestRedisCommand(reader)
		if errRead != nil {
			return
		}
		var reply string
		switch strings.ToUpper(args[0]) {
		case "HELLO":
			reply = "%6\r\n$6\r\nserver\r\n$5\r\nredis\r\n$5\r\nproto\r\n:3\r\n$2\r\nid\r\n:1\r\n$4\r\nmode\r\n$10\r\nstandalone\r\n$4\r\nrole\r\n$6\r\nmaster\r\n$7\r\nmodules\r\n*0\r\n"
		case "GET":
			payload := "credential-concurrency:\n  lifecycle-config-revision: 1\n  cpa-heartbeat-timeout: 10s\n  cpa-cancel-bound: 5s\n"
			if args[1] != "config" {
				payload = "[]"
				if args[1] == "plugin-tasks" && s.pluginTasks.Load() {
					payload = `[{"id":1,"operation":"delete","plugin_id":"test-plugin"}]`
				}
			}
			reply = fmt.Sprintf("$%d\r\n%s\r\n", len(payload), payload)
		case "CLUSTER":
			payload := `{"ok":true,"nodes":[]}`
			if args[1] == "BALANCE" {
				payload = `{"ok":true}`
			}
			reply = fmt.Sprintf("$%d\r\n%s\r\n", len(payload), payload)
		case "RPUSH":
			reply = ":1\r\n"
		case "LPUSH":
			if len(args) == 3 && args[1] == "concurrency-release" {
				select {
				case s.releases <- append([]string(nil), args...):
				case <-s.stop:
					return
				}
				s.mu.Lock()
				ack := s.releaseACK
				s.mu.Unlock()
				if ack != nil {
					select {
					case <-ack:
					case <-s.stop:
						return
					}
				}
			}
			reply = ":1\r\n"
		case "SUBSCRIBE":
			s.subscribed <- append([]string(nil), args...)
			if _, errWrite := io.WriteString(conn, "*3\r\n$9\r\nsubscribe\r\n$6\r\nconfig\r\n:1\r\n"); errWrite != nil {
				return
			}
			s.mu.Lock()
			target := s.target
			s.mu.Unlock()
			if target != "" {
			updates:
				for {
					select {
					case payload := <-s.configUpdates:
						writeRegistryTestMessage(conn, payload)
					case <-s.migrate:
						break updates
					case <-s.stop:
						return
					}
				}
				host, portText, errSplit := net.SplitHostPort(target)
				if errSplit != nil {
					return
				}
				port, errPort := strconv.Atoi(portText)
				if errPort != nil {
					return
				}
				payload, errJSON := json.Marshal(map[string]any{"id": "admission-plan", "term": 1, "target": map[string]any{"ip": host, "port": port}})
				if errJSON != nil {
					return
				}
				if _, errWrite := io.WriteString(conn, fmt.Sprintf("*3\r\n$7\r\nmessage\r\n$9\r\nrebalance\r\n$%d\r\n%s\r\n", len(payload), payload)); errWrite != nil {
					return
				}
			}
			<-s.stop
			return
		case "RPOP":
			if s.rpops.Add(1) != 1 {
				return
			}
			close(s.issued)
			// No more commands are expected on this connection until the old client closes.
			s.workers.Add(1)
			go func() {
				defer s.workers.Done()
				_, _ = reader.ReadByte()
				close(s.transportEnded)
			}()
			select {
			case <-s.reply:
			case <-s.stop:
				return
			}
			if s.ambiguous {
				return
			}
			if _, errWrite := io.WriteString(conn, "$2\r\n{}\r\n"); errWrite != nil {
				return
			}
			select {
			case <-s.transportEnded:
			case <-s.stop:
			}
			return
		default:
			reply = "+OK\r\n"
		}
		if _, errWrite := io.WriteString(conn, reply); errWrite != nil {
			return
		}
	}
}

type rebalanceAdmissionForwarder struct {
	bound    chan *home.Client
	detached chan *home.Client
}

func (f *rebalanceAdmissionForwarder) Bind(client *home.Client) { f.bound <- client }
func (f *rebalanceAdmissionForwarder) Deactivate(client *home.Client) {
	f.detached <- client
}
func (f *rebalanceAdmissionForwarder) Stop() {}

func awaitRebalanceEvent[T any](t *testing.T, events <-chan T) T {
	t.Helper()
	select {
	case event := <-events:
		return event
	case <-time.After(5 * time.Second):
		t.Fatal("rebalance synchronization event did not arrive")
		var zero T
		return zero
	}
}

func TestServicePlannedRebalanceFencesAdmissionAndSettlesResponseProcessing(t *testing.T) {
	for _, ambiguous := range []bool{false, true} {
		t.Run(fmt.Sprintf("ambiguous=%v", ambiguous), func(t *testing.T) {
			source := newRebalanceAdmissionHome(t, ambiguous)
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
			ctx, cancel := context.WithCancel(context.Background())
			service.runCancel = cancel
			service.startHomeSubscriber(ctx)
			t.Cleanup(func() {
				cancel()
				awaitRebalanceEvent(t, service.homeSupervisor.done)
				home.ClearCurrent()
			})
			client := awaitRebalanceEvent(t, forwarder.bound)
			t.Cleanup(client.Close)
			service.homeMu.Lock()
			registry := service.homeRegistry
			service.homeMu.Unlock()
			if registry == nil || !client.HeartbeatOK() {
				t.Fatal("source lifetime was not published healthy")
			}
			activePending, errBegin := registry.BeginDispatch()
			if errBegin != nil {
				t.Fatal(errBegin)
			}
			active, errInstall := registry.Install(activePending, executionregistry.ScopeSpec{RequestID: "already-running"})
			if errInstall != nil {
				t.Fatal(errInstall)
			}
			t.Cleanup(func() { active.End("test cleanup") })
			drained := make(chan struct{})
			var drainOnce sync.Once
			if errBind := active.Bind(func() error {
				drainOnce.Do(func() { close(drained) })
				go active.End("unsafe replacement")
				return nil
			}); errBind != nil {
				t.Fatal(errBind)
			}
			pending, errBegin := registry.BeginDispatch()
			if errBegin != nil {
				t.Fatal(errBegin)
			}
			processResponse := make(chan struct{})
			var processOnce sync.Once
			t.Cleanup(func() { processOnce.Do(func() { close(processResponse) }) })
			response := make(chan error, 1)
			processed := make(chan error, 1)
			go func() {
				defer pending.End()
				raw, errDispatch := client.RPopAuth(ctx, "test-model", "", nil, 1)
				response <- errDispatch
				<-processResponse
				if home.IsAmbiguousDispatchError(errDispatch) {
					// Match conductor ordering: record ambiguity before releasing the pending token.
					client.AbortAmbiguousDispatch()
				}
				if errDispatch != nil {
					processed <- errDispatch
					return
				}
				if string(raw) != "{}" {
					processed <- fmt.Errorf("dispatch response = %q", raw)
					return
				}
				scope, errScope := registry.Install(pending, executionregistry.ScopeSpec{RequestID: "newly-dispatched"})
				if errScope == nil {
					// Keep this execution running across migration, without a model completion barrier.
					errScope = scope.Bind(func() error {
						drainOnce.Do(func() { close(drained) })
						go scope.End("unsafe replacement")
						return nil
					})
				}
				processed <- errScope
			}()
			awaitRebalanceEvent(t, source.issued)
			close(source.migrate)
			// Observe the admission fence without issuing network traffic or adding sleeps.
			fenceDeadline := time.NewTimer(5 * time.Second)
			defer fenceDeadline.Stop()
			for {
				_, errFence := client.RPopAuth(ctx, "", "", nil, 1)
				if errors.Is(errFence, home.ErrDispatchFenced) {
					break
				}
				select {
				case <-fenceDeadline.C:
					t.Fatal("planned migration did not fence admission before settling pending dispatches")
				default:
					runtime.Gosched()
				}
			}
			select {
			case <-source.transportEnded:
				t.Fatal("migration closed an issued dispatch before its response arrived")
			case <-target.subscribed:
				t.Fatal("migration completed before the pending dispatch settled")
			case <-drained:
				t.Fatal("planned migration canceled an active execution")
			default:
			}
			close(source.reply)
			errResponse := awaitRebalanceEvent(t, response)
			if home.IsAmbiguousDispatchError(errResponse) != ambiguous || (!ambiguous && errResponse != nil) {
				t.Fatalf("issued RPOP response = %v", errResponse)
			}
			select {
			case <-target.subscribed:
				t.Fatal("Home sending credentials was treated as completed CPA response processing")
			default:
			}
			processOnce.Do(func() { close(processResponse) })
			errProcessed := awaitRebalanceEvent(t, processed)
			if home.IsAmbiguousDispatchError(errProcessed) != ambiguous || (!ambiguous && errProcessed != nil) {
				t.Fatalf("dispatch processing = %v", errProcessed)
			}
			if detached := awaitRebalanceEvent(t, forwarder.detached); detached != client {
				t.Fatal("migration detached the wrong lifetime")
			}
			subscription := awaitRebalanceEvent(t, target.subscribed)
			next := awaitRebalanceEvent(t, forwarder.bound)
			service.homeMu.Lock()
			nextRegistry := service.homeRegistry
			service.homeMu.Unlock()
			if next == client || !next.HeartbeatOK() {
				t.Fatal("replacement lifetime was not published")
			}
			if ambiguous {
				awaitRebalanceEvent(t, drained)
				if !client.AmbiguousDispatch() || registry == nextRegistry || strings.Contains(strings.Join(subscription, " "), "takeover") {
					t.Fatalf("unsafe replacement preserved registry or takeover: subscription=%#v", subscription)
				}
			} else {
				if registry != nextRegistry || len(subscription) < 4 || subscription[3] != "takeover" {
					t.Fatalf("safe migration did not preserve registry and takeover: %#v", subscription)
				}
				select {
				case <-drained:
					t.Fatal("safe migration drained an active model execution")
				default:
				}
				// A stale request may register after migration's empty pending-set observation.
				late, errLateBegin := registry.BeginDispatch()
				if errLateBegin != nil {
					t.Fatal(errLateBegin)
				}
				_, errLate := client.RPopAuth(ctx, "late-model", "", nil, 1)
				late.End()
				if !errors.Is(errLate, home.ErrDispatchFenced) || source.rpops.Load() != 1 {
					t.Fatalf("stale request issued a new RPOP: error=%v count=%d", errLate, source.rpops.Load())
				}
			}
		})
	}
}
