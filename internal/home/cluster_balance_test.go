package home

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func balanceTestRedisReply(args []string) string {
	if strings.EqualFold(args[0], "HELLO") {
		return "%6\r\n$6\r\nserver\r\n$5\r\nredis\r\n$5\r\nproto\r\n:3\r\n$2\r\nid\r\n:1\r\n$4\r\nmode\r\n$10\r\nstandalone\r\n$4\r\nrole\r\n$6\r\nmaster\r\n$7\r\nmodules\r\n*0\r\n"
	}
	return "+PONG\r\n"
}

func enableBalanceTestClient(client *Client) {
	client.homeCfg.DisableClusterDiscovery = false
	client.managed = true
	client.lifecycle = config.CredentialConcurrencyConfig{LifecycleConfigRevision: 1}.WithDefaults()
	client.heartbeatOK.Store(true)
	client.balance.registered = true
}

func TestClusterBalanceSnapshotsNeverInitiateMigration(t *testing.T) {
	client, commands := newRedisCommandTestClient(t, balanceTestRedisReply)
	enableBalanceTestClient(client)
	payload, errMarshal := json.Marshal(clusterNodesEnvelope{OK: true, Nodes: []clusterNode{
		{IP: client.homeCfg.Host, Port: client.homeCfg.Port, ClientCount: 8}, {IP: "other", Port: 8327},
	}})
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	for range 10 {
		if errUpdate := client.handleSubscriptionPayload(context.Background(), redisChannelCluster, string(payload), nil); errUpdate != nil {
			t.Fatal(errUpdate)
		}
	}
	if client.balance.moving || len(commands.All()) != 0 {
		t.Fatal("load snapshots initiated autonomous migration")
	}
}

func TestClusterBalanceExecutesOnlyClaimedPlans(t *testing.T) {
	for _, mode := range []string{"success", "rejected", "target failure", "claim failure"} {
		t.Run(mode, func(t *testing.T) {
			target, _ := newRedisCommandTestClient(t, func(args []string) string {
				if mode == "target failure" && strings.EqualFold(args[0], "PING") {
					return "-ERR unavailable\r\n"
				}
				return balanceTestRedisReply(args)
			})
			client, commands := newRedisCommandTestClient(t, func(args []string) string {
				if strings.EqualFold(args[0], "CLUSTER") {
					if args[2] == "CLAIM" && mode == "rejected" {
						return "$12\r\n{\"ok\":false}\r\n"
					}
					if args[2] == "CLAIM" && mode == "claim failure" {
						return "-ERR unavailable\r\n"
					}
					return "$11\r\n{\"ok\":true}\r\n"
				}
				return balanceTestRedisReply(args)
			})
			enableBalanceTestClient(client)
			request := rebalanceInstruction{ID: "plan-1", Term: 7, Target: clusterNode{IP: target.homeCfg.Host, Port: target.homeCfg.Port}}
			payload, errMarshal := json.Marshal(request)
			if errMarshal != nil {
				t.Fatal(errMarshal)
			}
			oldCommand, oldSubscription := client.cmd, client.sub
			errExecute := client.executeClusterRebalance(context.Background(), payload)
			if errors.Is(errExecute, ErrRebalanceRequested) != (mode == "success") {
				t.Fatalf("execute = %v", errExecute)
			}
			if client.cmd != oldCommand || client.sub != oldSubscription {
				t.Fatal("preparation closed source pools")
			}
			if mode == "success" {
				next := client.NewLifetime()
				t.Cleanup(next.Close)
				if next.homeCfg.Port != target.homeCfg.Port || client.homeCfg.Port == target.homeCfg.Port || next.balance.registered {
					t.Fatal("replacement target/state not isolated")
				}
				if recoveryState(next.recoveryState.Load()) != recoveryStateSwitchingTakeover {
					t.Fatal("missing takeover")
				}
				next.failClusterRebalance()
				if next.homeCfg.Port != client.homeCfg.Port || !next.balance.returning || next.balance.result != "failed" {
					t.Fatal("failed handoff did not roll back")
				}
				client.SuppressTakeover()
				if recoveryState(client.NewLifetime().recoveryState.Load()) == recoveryStateSwitchingTakeover {
					t.Fatal("suppressed takeover restored")
				}
			}
			if mode == "target failure" {
				found := false
				for _, args := range commands.All() {
					if len(args) == 5 && args[2] == "RESULT" && args[4] == "failed" {
						found = true
					}
				}
				if !found || client.balance.moving {
					t.Fatal("failed probe not reported while keeping source")
				}
			}
		})
	}
}

func TestClusterBalanceInstructionSafetyGates(t *testing.T) {
	for name, disable := range map[string]func(*Client){
		"unmanaged":          func(c *Client) { c.managed = false },
		"disabled discovery": func(c *Client) { c.homeCfg.DisableClusterDiscovery = true },
		"legacy":             func(c *Client) { c.legacyMembership = true },
		"no revision":        func(c *Client) { c.lifecycle.LifecycleConfigRevision = 0 },
		"fenced":             func(c *Client) { c.dispatchFenced.Store(true) },
		"ambiguous":          func(c *Client) { c.ambiguousDispatch.Store(true) },
		"no registration":    func(c *Client) { c.balance.registered = false },
		"no heartbeat":       func(c *Client) { c.heartbeatOK.Store(false) },
		"recovering":         func(c *Client) { c.recoveryState.Store(uint32(recoveryStateTakeoverEligible)) },
		"unreported result":  func(c *Client) { c.balance.result = "failed" },
	} {
		t.Run(name, func(t *testing.T) {
			client, commands := newRedisCommandTestClient(t, balanceTestRedisReply)
			enableBalanceTestClient(client)
			disable(client)
			errExecute := client.executeClusterRebalance(context.Background(), []byte(`{"id":"plan","term":1,"target":{"ip":"other","port":8327}}`))
			if errExecute != nil || len(commands.All()) != 0 {
				t.Fatalf("unsafe instruction executed: %v", errExecute)
			}
		})
	}
}

func TestClusterBalanceOldHomeAndResultRetry(t *testing.T) {
	for _, oldHome := range []bool{true, false} {
		t.Run(fmt.Sprint(oldHome), func(t *testing.T) {
			var results atomic.Int32
			client, commands := newRedisCommandTestClient(t, func(args []string) string {
				if strings.EqualFold(args[0], "CLUSTER") {
					if oldHome {
						return "-ERR cluster resp: unsupported subcommand\r\n"
					}
					if args[2] == "RESULT" && results.Add(1) == 1 {
						return "-ERR retry later\r\n"
					}
					return "$11\r\n{\"ok\":true}\r\n"
				}
				return balanceTestRedisReply(args)
			})
			enableBalanceTestClient(client)
			client.balance.registered = false
			client.balance.request.ID, client.balance.result = "plan", "failed"
			errFirst := client.syncClusterBalance(context.Background())
			if (errFirst != nil) != !oldHome {
				t.Fatalf("first sync = %v, old Home = %v", errFirst, oldHome)
			}
			if errSecond := client.syncClusterBalance(context.Background()); errSecond != nil {
				t.Fatalf("second sync = %v", errSecond)
			}
			if oldHome {
				if !client.balance.unsupported || commands.CountCommandKey("CLUSTER", "BALANCE") != 1 {
					t.Fatal("old Home was repeatedly probed")
				}
			} else if results.Load() != 2 || client.balance.result != "" {
				t.Fatal("result was not retried")
			}
		})
	}
}

func TestRunConfigSubscriberLifetimeExecutesHomePlanAndReportsSuccess(t *testing.T) {
	var directive atomic.Value
	target, targetCommands := newRedisCommandTestClient(t, func(args []string) string {
		switch strings.ToUpper(args[0]) {
		case "CLUSTER":
			if args[1] == "BALANCE" {
				return "$11\r\n{\"ok\":true}\r\n"
			}
			return "$22\r\n{\"ok\":true,\"nodes\":[]}\r\n"
		case "GET":
			return "$3\r\n{}\n\r\n"
		case "SUBSCRIBE":
			return "*3\r\n$9\r\nsubscribe\r\n$6\r\nconfig\r\n:1\r\n"
		default:
			return balanceTestRedisReply(args)
		}
	})
	client, _ := newRedisCommandTestClient(t, func(args []string) string {
		switch strings.ToUpper(args[0]) {
		case "CLUSTER":
			if args[1] == "BALANCE" {
				return "$11\r\n{\"ok\":true}\r\n"
			}
			return "$22\r\n{\"ok\":true,\"nodes\":[]}\r\n"
		case "GET":
			return "$3\r\n{}\n\r\n"
		case "SUBSCRIBE":
			payload := directive.Load().(string)
			return "*3\r\n$9\r\nsubscribe\r\n$6\r\nconfig\r\n:1\r\n" + fmt.Sprintf("*3\r\n$7\r\nmessage\r\n$9\r\nrebalance\r\n$%d\r\n%s\r\n", len(payload), payload)
		default:
			return balanceTestRedisReply(args)
		}
	})
	payload, errMarshal := json.Marshal(rebalanceInstruction{ID: "plan", Term: 1, Target: clusterNode{IP: target.homeCfg.Host, Port: target.homeCfg.Port}})
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	directive.Store(string(payload))
	enableBalanceTestClient(client)
	client.balance.registered = false
	apply := func([]byte) error {
		return client.SetLifecycleConfig(config.CredentialConcurrencyConfig{LifecycleConfigRevision: 1})
	}
	errRun := client.RunConfigSubscriberLifetime(context.Background(), apply, nil)
	if !errors.Is(errRun, ErrRebalanceRequested) || client.HeartbeatOK() || client.dispatchFenced.Load() {
		t.Fatalf("planned end = %v", errRun)
	}
	next := client.NewLifetime()
	next.SetManagedLifetime(true)
	t.Cleanup(next.Close)
	ready := false
	_ = next.RunConfigSubscriberLifetime(context.Background(), func([]byte) error {
		return next.SetLifecycleConfig(config.CredentialConcurrencyConfig{LifecycleConfigRevision: 1})
	}, func() { ready = next.HeartbeatOK(); next.Close() })
	if !ready || next.balance.moving || next.balance.result != "" {
		t.Fatal("target ACK/probe/result did not complete")
	}
	args := findRedisCommand(targetCommands.All(), "SUBSCRIBE")
	if len(args) < 4 || args[3] != "takeover" || next.homeCfg.Port != target.homeCfg.Port {
		t.Fatalf("replacement subscription = %#v", args)
	}
	found := false
	for _, command := range targetCommands.All() {
		if len(command) == 5 && command[2] == "RESULT" && command[3] == "plan" && command[4] == "success" {
			found = true
		}
	}
	if !found {
		t.Fatal("successful migration not reported")
	}
}

func TestClusterBalanceBootstrapTieAndNormalization(t *testing.T) {
	client := New(config.HomeConfig{Host: "home-0", Port: 8327})
	nodes := normalizeClusterNodes([]clusterNode{{IP: "home-0", Port: 8327}, {IP: "home-1", Port: 8327}, {IP: " home-1 ", Port: 8327}, {IP: "bad", Port: 65536}})
	if len(nodes) != 2 {
		t.Fatalf("nodes = %#v", nodes)
	}
	for index, draw := range []float64{0, 0.9} {
		node, ok := client.chooseClusterNodeLocked(nodes, false, draw)
		if !ok || node.IP != fmt.Sprintf("home-%d", index) {
			t.Fatalf("choice = %#v", node)
		}
	}
}
