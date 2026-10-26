package home

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	log "github.com/sirupsen/logrus"
)

func TestClusterBalanceResponseErrors(t *testing.T) {
	for _, operation := range []string{"ENABLE", "RESULT", "CLAIM"} {
		for _, response := range []struct {
			name string
			raw  string
			fail bool
		}{
			{name: "malformed", raw: `{`, fail: true},
			{name: "wrong type", raw: `{"ok":"true"}`, fail: true},
			{name: "rejected", raw: `{"ok":false}`},
		} {
			t.Run(operation+"/"+response.name, func(t *testing.T) {
				client, _ := newRedisCommandTestClient(t, func(args []string) string {
					if strings.EqualFold(args[0], "CLUSTER") {
						return fmt.Sprintf("$%d\r\n%s\r\n", len(response.raw), response.raw)
					}
					return balanceTestRedisReply(args)
				})
				enableBalanceTestClient(client)
				var errResponse error
				switch operation {
				case "ENABLE":
					client.balance.registered = false
					errResponse = client.syncClusterBalance(context.Background())
				case "RESULT":
					client.balance.request.ID, client.balance.result = "pending", "failed"
					errResponse = client.syncClusterBalance(context.Background())
					if client.balance.request.ID != "pending" || client.balance.result != "failed" {
						t.Fatal("unacknowledged result was discarded")
					}
				case "CLAIM":
					errResponse = client.executeClusterRebalance(context.Background(), []byte(`{"id":"plan","term":1,"target":{"ip":"other","port":8327}}`))
				}
				if (errResponse != nil) != response.fail {
					t.Fatalf("response error = %v, want failure %v", errResponse, response.fail)
				}
				if response.fail && !strings.Contains(errResponse.Error(), operation+" response") {
					t.Fatalf("error lacks protocol context: %v", errResponse)
				}
				if client.balance.unsupported || client.balance.moving || (operation == "ENABLE" && client.balance.registered) {
					t.Fatal("invalid/rejected response advanced balance state or disabled capability")
				}
			})
		}
	}
}

func TestClusterBalanceRegistrationErrorsDoNotDisableCapability(t *testing.T) {
	client, _ := newRedisCommandTestClient(t, func(args []string) string {
		if strings.EqualFold(args[0], "CLUSTER") {
			return "-ERR membership_required\r\n"
		}
		return balanceTestRedisReply(args)
	})
	enableBalanceTestClient(client)
	client.balance.registered = false
	if errSync := client.syncClusterBalance(context.Background()); errSync == nil {
		t.Fatal("membership failure was swallowed")
	}
	if client.balance.unsupported || client.balance.registered {
		t.Fatal("membership failure disabled capability or registered client")
	}
}

func TestClusterBalanceBlockedRPCDoesNotDelayHeartbeatExpiry(t *testing.T) {
	for _, blockedOperation := range []string{"ENABLE", "CLAIM", "PING", "RESULT"} {
		t.Run(blockedOperation, func(t *testing.T) {
			blocked := make(chan struct{})
			release := make(chan struct{})
			var blockOnce sync.Once
			block := func() {
				blockOnce.Do(func() { close(blocked) })
				<-release
			}
			t.Cleanup(func() { close(release) })
			target, _ := newRedisCommandTestClient(t, func(args []string) string {
				if blockedOperation == "PING" && strings.EqualFold(args[0], "PING") {
					block()
				}
				return balanceTestRedisReply(args)
			})
			instruction, errMarshal := json.Marshal(rebalanceInstruction{
				ID: "blocked-plan", Term: 1, Target: clusterNode{IP: target.homeCfg.Host, Port: target.homeCfg.Port},
			})
			if errMarshal != nil {
				t.Fatal(errMarshal)
			}
			client, commands := newRedisCommandTestClient(t, func(args []string) string {
				switch strings.ToUpper(args[0]) {
				case "CLUSTER":
					if args[1] == "BALANCE" {
						if args[2] == blockedOperation {
							block()
						}
						return "$11\r\n{\"ok\":true}\r\n"
					}
					return "$22\r\n{\"ok\":true,\"nodes\":[]}\r\n"
				case "GET":
					return "$2\r\n{}\r\n"
				case "SUBSCRIBE":
					reply := "*3\r\n$9\r\nsubscribe\r\n$6\r\nconfig\r\n:1\r\n"
					if blockedOperation == "CLAIM" || blockedOperation == "PING" {
						reply += fmt.Sprintf("*3\r\n$7\r\nmessage\r\n$9\r\nrebalance\r\n$%d\r\n%s\r\n", len(instruction), instruction)
					}
					return reply
				default:
					return balanceTestRedisReply(args)
				}
			})
			enableBalanceTestClient(client)
			client.balance.registered = false
			client.testOperationTimeout = 5 * time.Second
			if blockedOperation == "RESULT" {
				client.balance.request.ID, client.balance.result = "previous-plan", "success"
			}
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			done := make(chan error, 1)
			go func() {
				done <- client.RunConfigSubscriberLifetime(ctx, func([]byte) error {
					return client.SetLifecycleConfig(config.CredentialConcurrencyConfig{
						LifecycleConfigRevision: 1, CPAHeartbeatTimeout: 200 * time.Millisecond,
					})
				}, nil)
			}()
			select {
			case <-blocked:
			case errRun := <-done:
				t.Fatalf("subscriber ended before blocked RPC: %v", errRun)
			case <-time.After(3 * time.Second):
				t.Fatal("RPC did not start")
			}
			select {
			case errRun := <-done:
				if !isTimeoutError(errRun) {
					t.Fatalf("subscriber error = %v, want heartbeat timeout", errRun)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("blocked RPC delayed heartbeat expiry or subscriber shutdown")
			}
			if client.HeartbeatOK() || !client.dispatchFenced.Load() {
				t.Fatal("heartbeat expiry did not fence dispatch admission")
			}
			if _, errDispatch := client.RPopAuth(context.Background(), "test-model", "", nil, 1); !errors.Is(errDispatch, ErrDispatchFenced) {
				t.Fatalf("late dispatch = %v, want fenced", errDispatch)
			}
			if len(findRedisCommand(commands.All(), "RPOP")) != 0 || client.balance.moving {
				t.Fatal("expired subscriber issued dispatch or migrated")
			}
			client.mu.Lock()
			commandPool := client.cmd
			client.mu.Unlock()
			if commandPool == nil {
				t.Fatal("migration cancellation detached the source command pool")
			}
			if errPing := commandPool.Ping(context.Background()).Err(); errPing != nil {
				t.Fatalf("migration cancellation interrupted the source command pool: %v", errPing)
			}
		})
	}
}

func TestClusterBalanceMigrationLogIncludesPlanAndAddresses(t *testing.T) {
	var output bytes.Buffer
	logger := log.StandardLogger()
	previousOutput, previousLevel := logger.Out, logger.GetLevel()
	logger.SetOutput(&output)
	logger.SetLevel(log.InfoLevel)
	t.Cleanup(func() {
		logger.SetOutput(previousOutput)
		logger.SetLevel(previousLevel)
	})
	target, _ := newRedisCommandTestClient(t, balanceTestRedisReply)
	client, _ := newRedisCommandTestClient(t, func(args []string) string {
		if strings.EqualFold(args[0], "CLUSTER") {
			return "$11\r\n{\"ok\":true}\r\n"
		}
		return balanceTestRedisReply(args)
	})
	enableBalanceTestClient(client)
	origin := clusterNodeAddress(clusterNode{IP: client.homeCfg.Host, Port: client.homeCfg.Port})
	request := rebalanceInstruction{ID: "logged-plan", Term: 1, Target: clusterNode{IP: target.homeCfg.Host, Port: target.homeCfg.Port}}
	payload, errMarshal := json.Marshal(request)
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	if errExecute := client.executeClusterRebalance(context.Background(), payload); !errors.Is(errExecute, ErrRebalanceRequested) {
		t.Fatal(errExecute)
	}
	// The message itself must carry these values, independently of formatter field allowlists.
	line := strings.ReplaceAll(output.String(), `\"`, `"`)
	for key, value := range map[string]string{"plan": request.ID, "from": origin, "to": clusterNodeAddress(request.Target)} {
		if !strings.Contains(line, fmt.Sprintf("%s=%q", key, value)) {
			t.Fatalf("migration log missing %s: %q", key, line)
		}
	}
}

func TestClusterBalanceMalformedRegistrationEndsSubscriberBeforeReady(t *testing.T) {
	client, _ := newRedisCommandTestClient(t, func(args []string) string {
		switch strings.ToUpper(args[0]) {
		case "CLUSTER":
			if args[1] == "BALANCE" {
				return "$1\r\n{\r\n"
			}
			return "$22\r\n{\"ok\":true,\"nodes\":[]}\r\n"
		case "GET":
			return "$2\r\n{}\r\n"
		case "SUBSCRIBE":
			return "*3\r\n$9\r\nsubscribe\r\n$6\r\nconfig\r\n:1\r\n"
		default:
			return balanceTestRedisReply(args)
		}
	})
	enableBalanceTestClient(client)
	client.balance.registered = false
	ready := false
	errRun := client.RunConfigSubscriberLifetime(context.Background(), func([]byte) error { return nil }, func() { ready = true })
	var syntaxError *json.SyntaxError
	if !errors.As(errRun, &syntaxError) || !strings.Contains(errRun.Error(), "ENABLE response") {
		t.Fatalf("subscriber did not propagate decoding failure: %v", errRun)
	}
	if ready || client.HeartbeatOK() {
		t.Fatal("malformed registration published a healthy subscriber")
	}
}

type blockingOuterCloseConn struct {
	net.Conn
	enteredClose     chan struct{}
	enterOnce        sync.Once
	underlyingClosed chan struct{}
}

func (c *blockingOuterCloseConn) NetConn() net.Conn {
	return c.Conn
}

func (c *blockingOuterCloseConn) Close() error {
	if c.enteredClose != nil {
		c.enterOnce.Do(func() { close(c.enteredClose) })
	}
	<-c.underlyingClosed
	return c.Conn.Close()
}

type trackingUnderlyingConn struct {
	net.Conn
	closed chan struct{}
	once   sync.Once
}

func (c *trackingUnderlyingConn) Close() error {
	c.once.Do(func() { close(c.closed) })
	return c.Conn.Close()
}

func TestCancelableConnClosesUnderlyingTransportWhenCloseRacesCancel(t *testing.T) {
	for _, mode := range []string{"pre-canceled", "after-wrap", "during-close"} {
		t.Run(mode, func(t *testing.T) {
			for range 32 {
				local, peer := net.Pipe()
				t.Cleanup(func() {
					_ = local.Close()
					_ = peer.Close()
				})
				underlying := &trackingUnderlyingConn{Conn: local, closed: make(chan struct{})}
				enteredClose := make(chan struct{})
				outer := &blockingOuterCloseConn{
					Conn:             underlying,
					enteredClose:     enteredClose,
					underlyingClosed: underlying.closed,
				}

				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				if mode == "pre-canceled" {
					cancel()
				}
				wrapped := newPluginSyncCancelableConn(ctx, outer)
				if mode == "after-wrap" {
					cancel()
				}

				done := make(chan error, 1)
				go func() {
					done <- wrapped.Close()
				}()
				if mode == "during-close" {
					<-enteredClose
					cancel()
				}
				select {
				case errClose := <-done:
					if errClose != nil && !errors.Is(errClose, net.ErrClosed) {
						t.Fatalf("close error = %v", errClose)
					}
				case <-time.After(500 * time.Millisecond):
					_ = underlying.Close()
					t.Fatal("Close blocked because underlying transport was not closed before outer Close")
				}
			}
		})
	}
}
