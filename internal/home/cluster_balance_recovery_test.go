package home

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestClusterBalanceTargetPreflightFailureRecovery(t *testing.T) {
	for _, report := range []struct {
		name  string
		reply string
	}{
		{name: "acknowledged", reply: "$11\r\n{\"ok\":true}\r\n"},
		{name: "RPC failure", reply: "-TEST_REPORT_FAILURE\r\n"},
		{name: "malformed", reply: "$1\r\n{\r\n"},
		{name: "rejected", reply: "$12\r\n{\"ok\":false}\r\n"},
	} {
		t.Run(report.name, func(t *testing.T) {
			target, _ := newRedisCommandTestClient(t, func(args []string) string {
				if strings.EqualFold(args[0], "PING") {
					return "-TEST_TARGET_UNAVAILABLE\r\n"
				}
				return balanceTestRedisReply(args)
			})
			instruction, errMarshal := json.Marshal(rebalanceInstruction{
				ID: "failed-plan", Term: 1, Target: clusterNode{IP: target.homeCfg.Host, Port: target.homeCfg.Port},
			})
			if errMarshal != nil {
				t.Fatal(errMarshal)
			}
			const marker = `{"after_rebalance":true}`
			client, commands := newRedisCommandTestClient(t, func(args []string) string {
				switch strings.ToUpper(args[0]) {
				case "CLUSTER":
					if args[1] == "BALANCE" {
						if args[2] == "RESULT" {
							return report.reply
						}
						return "$11\r\n{\"ok\":true}\r\n"
					}
					return "$22\r\n{\"ok\":true,\"nodes\":[]}\r\n"
				case "GET", "RPOP":
					return "$2\r\n{}\r\n"
				case "SUBSCRIBE":
					return "*3\r\n$9\r\nsubscribe\r\n$6\r\nconfig\r\n:1\r\n" +
						fmt.Sprintf("*3\r\n$7\r\nmessage\r\n$9\r\nrebalance\r\n$%d\r\n%s\r\n", len(instruction), instruction) +
						fmt.Sprintf("*3\r\n$7\r\nmessage\r\n$6\r\nconfig\r\n$%d\r\n%s\r\n", len(marker), marker)
				default:
					return balanceTestRedisReply(args)
				}
			})
			enableBalanceTestClient(client)
			client.testOperationTimeout = time.Second
			client.balance.registered = false
			origin, _ := client.addr()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var sourceCommand, sourceSubscription *redis.Client
			var dispatchErr error
			var dispatched []byte
			var continued, healthy, samePools bool
			errRun := client.RunConfigSubscriberLifetime(ctx, func(payload []byte) error {
				if string(payload) == marker {
					continued = true
					healthy = client.HeartbeatOK() && !client.dispatchFenced.Load()
					client.mu.Lock()
					samePools = client.cmd == sourceCommand && client.sub == sourceSubscription
					client.mu.Unlock()
					dispatched, dispatchErr = client.RPopAuth(ctx, "test-model", "", nil, 1)
					cancel()
				}
				return nil
			}, func() {
				client.mu.Lock()
				sourceCommand, sourceSubscription = client.cmd, client.sub
				client.mu.Unlock()
			})
			var result []string
			for _, args := range commands.All() {
				if len(args) >= 3 && args[2] == "RESULT" {
					result = args
				}
			}
			if !reflect.DeepEqual(result, []string{"CLUSTER", "BALANCE", "RESULT", "failed-plan", "failed"}) {
				t.Fatalf("failure report = %#v", result)
			}
			if address, _ := client.addr(); address != origin || client.balance.moving {
				t.Fatal("failed preflight switched away from the source")
			}
			if report.name == "acknowledged" {
				if !continued || !healthy || !samePools || !errors.Is(errRun, context.Canceled) {
					t.Fatalf("healthy source was not retained: continued=%v healthy=%v samePools=%v error=%v", continued, healthy, samePools, errRun)
				}
				if dispatchErr != nil || string(dispatched) != "{}" || len(findRedisCommand(commands.All(), "RPOP")) == 0 {
					t.Fatalf("dispatch after failed preflight = %q, %v", dispatched, dispatchErr)
				}
				if client.balance.result != "" || client.balance.request.ID != "" {
					t.Fatal("acknowledged failure report remained pending")
				}
				return
			}
			var targetError redis.Error
			if continued || !errors.As(errRun, &targetError) || targetError.Error() != "TEST_TARGET_UNAVAILABLE" {
				t.Fatalf("unacknowledged failure was swallowed: continued=%v error=%v", continued, errRun)
			}
			if client.balance.result != "failed" || client.balance.request.ID != "failed-plan" {
				t.Fatal("unacknowledged failure report was discarded")
			}
			if report.name == "malformed" {
				var syntaxError *json.SyntaxError
				if !errors.As(errRun, &syntaxError) {
					t.Fatalf("report decoding failure was lost: %v", errRun)
				}
			}
			if report.name == "RPC failure" {
				joined, ok := errRun.(interface{ Unwrap() []error })
				if !ok || len(joined.Unwrap()) != 2 {
					t.Fatalf("report RPC failure was lost: %v", errRun)
				}
				var reportError redis.Error
				if !errors.As(joined.Unwrap()[1], &reportError) || reportError.Error() != "TEST_REPORT_FAILURE" {
					t.Fatalf("report RPC failure code was lost: %v", errRun)
				}
			}
		})
	}
}

func TestClusterBalanceRollbackRecoveryAcrossLifetimes(t *testing.T) {
	for _, failure := range []string{"GET", "SUBSCRIBE", "PING"} {
		for _, sourceRecovers := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/source_recovers=%v", failure, sourceRecovers), func(t *testing.T) {
				healthy, healthyCommands := newRedisCommandTestClient(t, func(args []string) string {
					switch strings.ToUpper(args[0]) {
					case "CLUSTER":
						if args[1] == "BALANCE" {
							return "$11\r\n{\"ok\":true}\r\n"
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
				target, _ := newRedisCommandTestClient(t, func(args []string) string {
					switch strings.ToUpper(args[0]) {
					case "CLUSTER":
						return "$22\r\n{\"ok\":true,\"nodes\":[]}\r\n"
					case "GET":
						return "-TEST_TARGET_CONFIG_UNAVAILABLE\r\n"
					default:
						return balanceTestRedisReply(args)
					}
				})
				nodes, errNodes := json.Marshal(clusterNodesEnvelope{OK: true, Nodes: []clusterNode{
					{IP: healthy.homeCfg.Host, Port: healthy.homeCfg.Port},
				}})
				if errNodes != nil {
					t.Fatal(errNodes)
				}
				var sourceRecovered atomic.Bool
				source, sourceCommands := newRedisCommandTestClient(t, func(args []string) string {
					if strings.EqualFold(args[0], failure) && !sourceRecovered.Load() {
						return "-TEST_SOURCE_UNAVAILABLE\r\n"
					}
					switch strings.ToUpper(args[0]) {
					case "CLUSTER":
						if args[1] == "BALANCE" {
							return "$11\r\n{\"ok\":true}\r\n"
						}
						return fmt.Sprintf("$%d\r\n%s\r\n", len(nodes), nodes)
					case "GET":
						return "$2\r\n{}\r\n"
					case "SUBSCRIBE":
						return "*3\r\n$9\r\nsubscribe\r\n$6\r\nconfig\r\n:1\r\n"
					default:
						return balanceTestRedisReply(args)
					}
				})
				enableBalanceTestClient(source)
				source.testOperationTimeout = time.Second
				instruction, errInstruction := json.Marshal(rebalanceInstruction{
					ID: "failed-plan", Term: 1, Target: clusterNode{IP: target.homeCfg.Host, Port: target.homeCfg.Port},
				})
				if errInstruction != nil {
					t.Fatal(errInstruction)
				}
				if errExecute := source.executeClusterRebalance(context.Background(), instruction); !errors.Is(errExecute, ErrRebalanceRequested) {
					t.Fatalf("prepare migration = %v", errExecute)
				}
				source.Close()
				client := source.NewLifetime()
				defer func() { client.Close() }()
				client.SetManagedLifetime(true)
				apply := func([]byte) error {
					return client.SetLifecycleConfig(config.CredentialConcurrencyConfig{LifecycleConfigRevision: 1})
				}
				errTarget := client.RunConfigSubscriberLifetime(context.Background(), apply, nil)
				var targetError redis.Error
				if !errors.As(errTarget, &targetError) || targetError.Error() != "TEST_TARGET_CONFIG_UNAVAILABLE" {
					t.Fatalf("target failure = %v", errTarget)
				}
				if client.homeCfg.Port != source.homeCfg.Port || !client.balance.returning || client.reconnectFailures != 1 {
					t.Fatalf("failed target did not enter rollback: port=%d returning=%v failures=%d", client.homeCfg.Port, client.balance.returning, client.reconnectFailures)
				}

				attempts := homeReconnectFailoverThreshold - 1
				if sourceRecovers {
					attempts--
				}
				for attempt := 1; attempt <= attempts; attempt++ {
					client.Close()
					client = client.NewLifetime()
					client.SetManagedLifetime(true)
					errRun := client.RunConfigSubscriberLifetime(context.Background(), apply, nil)
					var sourceError redis.Error
					if !errors.As(errRun, &sourceError) || sourceError.Error() != "TEST_SOURCE_UNAVAILABLE" {
						t.Fatalf("source attempt %d lost original failure: %v", attempt, errRun)
					}
					wantPort, wantFailures := source.homeCfg.Port, attempt+1
					if wantFailures == homeReconnectFailoverThreshold {
						wantPort, wantFailures = healthy.homeCfg.Port, 0
					}
					if client.homeCfg.Port != wantPort || client.reconnectFailures != wantFailures {
						t.Fatalf("source attempt %d: port=%d failures=%d, want port=%d failures=%d", attempt, client.homeCfg.Port, client.reconnectFailures, wantPort, wantFailures)
					}
					if client.balance.moving || !client.balance.returning || client.balance.result != "failed" || client.balance.request.ID != "failed-plan" {
						t.Fatal("rollback lost its pending failed result")
					}
				}
				if count := sourceCommands.CountCommandKey("CLUSTER", "NODES"); count != attempts {
					t.Fatalf("source discovery count = %d, want %d", count, attempts)
				}

				sourceRecovered.Store(sourceRecovers)
				client.Close()
				client = client.NewLifetime()
				client.SetManagedLifetime(true)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				ready := false
				errRecovered := client.RunConfigSubscriberLifetime(ctx, apply, func() {
					ready = client.HeartbeatOK() && client.reconnectFailures == 0 &&
						recoveryState(client.recoveryState.Load()) == recoveryStateStable
					cancel()
				})
				if !ready || !errors.Is(errRecovered, context.Canceled) {
					t.Fatalf("recovery did not complete: ready=%v error=%v", ready, errRecovered)
				}
				if client.balance.returning || client.balance.result != "" || client.balance.request.ID != "" {
					t.Fatal("recovery did not finish the failed migration")
				}
				recoveredCommands, wantPort := healthyCommands, healthy.homeCfg.Port
				if sourceRecovers {
					recoveredCommands, wantPort = sourceCommands, source.homeCfg.Port
				}
				if client.homeCfg.Port != wantPort {
					t.Fatalf("recovered port = %d, want %d", client.homeCfg.Port, wantPort)
				}
				var subscription, result []string
				for _, args := range recoveredCommands.All() {
					if strings.EqualFold(args[0], "SUBSCRIBE") {
						subscription = args
					}
					if len(args) >= 3 && args[2] == "RESULT" {
						result = args
					}
				}
				wantSubscription := []string{"subscribe", redisChannelConfig, "1", "takeover", source.MembershipInstanceID()}
				if !reflect.DeepEqual(subscription, wantSubscription) {
					t.Fatalf("recovery subscription = %#v, want %#v", subscription, wantSubscription)
				}
				if !reflect.DeepEqual(result, []string{"CLUSTER", "BALANCE", "RESULT", "failed-plan", "failed"}) {
					t.Fatalf("failure report = %#v", result)
				}
			})
		}
	}
}

func TestClusterBalancePostACKFailurePreservesTakeover(t *testing.T) {
	for _, failure := range []struct {
		operation string
		malformed bool
	}{
		{operation: "ENABLE"},
		{operation: "ENABLE", malformed: true},
		{operation: "RESULT"},
		{operation: "RESULT", malformed: true},
		{operation: redisChannelCluster, malformed: true},
	} {
		t.Run(fmt.Sprintf("%s/malformed=%v", failure.operation, failure.malformed), func(t *testing.T) {
			var recovered atomic.Bool
			client, commands := newRedisCommandTestClient(t, func(args []string) string {
				switch strings.ToUpper(args[0]) {
				case "CLUSTER":
					if args[1] == "BALANCE" {
						if args[2] == failure.operation && !recovered.Load() {
							if failure.malformed {
								return "$1\r\n{\r\n"
							}
							return "-TEST_BALANCE_FAILURE\r\n"
						}
						return "$11\r\n{\"ok\":true}\r\n"
					}
					return "$22\r\n{\"ok\":true,\"nodes\":[]}\r\n"
				case "GET":
					return "$2\r\n{}\r\n"
				case "SUBSCRIBE":
					reply := "*3\r\n$9\r\nsubscribe\r\n$6\r\nconfig\r\n:1\r\n"
					if failure.operation == redisChannelCluster && !recovered.Load() {
						reply += fmt.Sprintf("*3\r\n$7\r\nmessage\r\n$%d\r\n%s\r\n$1\r\n{\r\n", len(redisChannelCluster), redisChannelCluster)
					}
					return reply
				default:
					return balanceTestRedisReply(args)
				}
			})
			enableBalanceTestClient(client)
			client.testOperationTimeout = time.Second
			client.balance.registered = false
			if failure.operation == "RESULT" {
				client.balance.request.ID, client.balance.result = "previous-plan", "success"
			}
			errRun := client.RunConfigSubscriberLifetime(context.Background(), func([]byte) error { return nil }, nil)
			if failure.malformed {
				var syntaxError *json.SyntaxError
				if !errors.As(errRun, &syntaxError) {
					t.Fatalf("original JSON failure was lost: %v", errRun)
				}
			} else {
				var redisError redis.Error
				if !errors.As(errRun, &redisError) || redisError.Error() != "TEST_BALANCE_FAILURE" {
					t.Fatalf("original RPC failure was lost: %v", errRun)
				}
			}
			if client.HeartbeatOK() {
				t.Fatal("failed subscriber retained healthy heartbeat")
			}

			next := client.NewLifetime()
			next.SetManagedLifetime(true)
			t.Cleanup(next.Close)
			if errSet := next.SetLifecycleConfig(config.CredentialConcurrencyConfig{LifecycleConfigRevision: 1}); errSet != nil {
				t.Fatal(errSet)
			}
			want := []string{redisChannelConfig, "1", "takeover", client.MembershipInstanceID()}
			if args, _ := next.subscriptionParameters(); !reflect.DeepEqual(args, want) {
				t.Fatalf("replacement subscription = %#v, want %#v", args, want)
			}
			recovered.Store(true)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			ready := false
			errNext := next.RunConfigSubscriberLifetime(ctx, func([]byte) error { return nil }, func() {
				ready = next.HeartbeatOK()
				cancel()
			})
			if !ready || !errors.Is(errNext, context.Canceled) {
				t.Fatalf("replacement did not recover: ready=%v error=%v", ready, errNext)
			}
			var subscriptions [][]string
			for _, args := range commands.All() {
				if strings.EqualFold(args[0], "SUBSCRIBE") {
					subscriptions = append(subscriptions, args)
				}
			}
			if len(subscriptions) != 2 || !reflect.DeepEqual(subscriptions[1][1:], want) {
				t.Fatalf("replacement wire subscriptions = %#v", subscriptions)
			}
		})
	}
}
