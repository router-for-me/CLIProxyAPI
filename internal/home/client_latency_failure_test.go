package home

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

type telemetryPingFailureConn struct {
	net.Conn
}

func (c *telemetryPingFailureConn) Write(payload []byte) (int, error) {
	if bytes.Contains(payload, []byte("home-rtt/")) {
		return 0, io.ErrClosedPipe
	}
	return c.Conn.Write(payload)
}

func newTelemetryPingFailureRedisClient(commands *redisCommandLog) (*redis.Client, *atomic.Int32) {
	dials := &atomic.Int32{}
	client := redis.NewClient(&redis.Options{
		Addr: "127.0.0.1:8327", Protocol: 2, DisableIdentity: true, MaxRetries: -1, DialerRetries: 1,
		Dialer: func(context.Context, string, string) (net.Conn, error) {
			local, peer := net.Pipe()
			go serveRedisCommandTestConn(peer, commands, func(args []string) string {
				if strings.EqualFold(args[0], "SUBSCRIBE") {
					return "*3\r\n$9\r\nsubscribe\r\n$6\r\nconfig\r\n:1\r\n"
				}
				return balanceTestRedisReply(args)
			})
			if dials.Add(1) == 1 {
				return &telemetryPingFailureConn{Conn: local}, nil
			}
			return local, nil
		},
	})
	return client, dials
}

func TestClusterLatencyPingFailureBetweenReceives(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		commands := &redisCommandLog{}
		sub, dials := newTelemetryPingFailureRedisClient(commands)
		defer func() {
			if errClose := sub.Close(); errClose != nil {
				t.Error(errClose)
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		pubsub := sub.Subscribe(ctx, redisChannelConfig, "1", "takeover", "instance")
		defer func() {
			if errClose := pubsub.Close(); errClose != nil {
				t.Error(errClose)
			}
		}()
		if errACK := receiveSubscriptionACKs(ctx, pubsub, time.Second, []string{redisChannelConfig}); errACK != nil {
			t.Fatal(errACK)
		}
		client := New(config.HomeConfig{})
		client.balance.registered, client.balance.latencySupported = true, true
		sampler := &clusterRTTSampler{history: &clusterLatencyHistory{}, node: clusterNode{IP: "home", Port: 8327}}
		// No receive is pending: go-redis reconnects before returning the PING write error.
		errLatency := client.runClusterLatency(ctx, pubsub, sampler)
		if !errors.Is(errLatency, io.ErrClosedPipe) || ctx.Err() != nil {
			t.Fatalf("sampler error = %v, context = %v; want immediate PING transport failure", errLatency, ctx.Err())
		}
		synctest.Wait()
		subscriptions := 0
		for _, args := range commands.All() {
			// go-redis treats membership parameters as channels and may reorder them on resubscribe.
			if strings.EqualFold(args[0], "SUBSCRIBE") {
				subscriptions++
			}
		}
		if dials.Load() != 2 || subscriptions != 2 {
			t.Fatalf("go-redis did not automatically replace the subscription: dials=%d commands=%v", dials.Load(), commands.All())
		}
		if samples := sampler.history.report(time.Now()); len(samples) != 1 || samples[0].Valid {
			t.Fatalf("failed PING was not invalidated: %+v", samples)
		}
		// The new ACK cannot silently keep the old lifetime alive, even if reception wins the race.
		event, errReceive := receiveSubscriptionEvent(ctx, pubsub, sampler, time.Second)
		if errReceive == nil || isTimeoutError(errReceive) || event != nil {
			t.Fatalf("replacement ACK was accepted: event=%v error=%v", event, errReceive)
		}
	})
}

func TestManagedSubscriberTelemetryFailureRequiresNewLifetime(t *testing.T) {
	client, commands := newRedisCommandTestClient(t, func(args []string) string {
		switch strings.ToUpper(args[0]) {
		case "GET":
			return "$2\r\n{}\r\n"
		case "SUBSCRIBE":
			return "*3\r\n$9\r\nsubscribe\r\n$6\r\nconfig\r\n:1\r\n"
		case "LPUSH":
			return ":1\r\n"
		default:
			return balanceTestRedisReply(args)
		}
	})
	client.SetManagedLifetime(true)
	client.testOperationTimeout = 5 * time.Second
	client.recoveryState.Store(uint32(recoveryStateTakeoverEligible))
	synctest.Test(t, func(t *testing.T) {
		defer client.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		subCommands := &redisCommandLog{}
		sub, dials := newTelemetryPingFailureRedisClient(subCommands)
		lifecycle := config.CredentialConcurrencyConfig{LifecycleConfigRevision: 1, CPAHeartbeatTimeout: time.Minute}
		frame := ConcurrencyReleaseFrame{CredentialID: "credential-a", Model: "test-model", ReleaseSeq: 1}
		var oldCommand, oldRelease *redis.Client
		errRun := client.RunConfigSubscriberLifetime(ctx, func([]byte) error {
			if errConfig := client.SetLifecycleConfig(lifecycle); errConfig != nil {
				return errConfig
			}
			client.mu.Lock()
			previousSub := client.sub
			client.sub = sub
			client.balance.registered, client.balance.latencySupported = true, true
			client.mu.Unlock()
			if errClose := previousSub.Close(); errClose != nil {
				return fmt.Errorf("close test bootstrap subscription pool: %w", errClose)
			}
			return nil
		}, func() {
			if !client.HeartbeatOK() {
				t.Error("initial subscription was not ready")
			}
			if errRelease := client.PushConcurrencyRelease(ctx, frame); errRelease != nil {
				t.Errorf("initial release: %v", errRelease)
			}
			client.mu.Lock()
			oldCommand, oldRelease = client.cmd, client.release
			client.mu.Unlock()
		})
		if !errors.Is(errRun, io.ErrClosedPipe) || ctx.Err() != nil {
			t.Fatalf("lifetime error = %v, context = %v; want subscription transport failure", errRun, ctx.Err())
		}
		if dials.Load() != 2 || oldCommand == nil || oldRelease == nil {
			t.Fatalf("test missed automatic resubscription with live command pools: dials=%d command=%v release=%v", dials.Load(), oldCommand != nil, oldRelease != nil)
		}
		if client.HeartbeatOK() || !client.dispatchFenced.Load() || !client.releaseFenced.Load() {
			t.Fatal("failed subscription retained heartbeat or unfenced command connections")
		}
		if client.recoveryState.Load() != uint32(recoveryStateTakeoverEligible) || client.AmbiguousDispatch() {
			t.Fatal("subscription failure did not preserve safe membership takeover")
		}
		if _, errDispatch := client.RPopAuth(context.Background(), "test-model", "", nil, 1); !errors.Is(errDispatch, ErrDispatchFenced) {
			t.Fatalf("dispatch after telemetry transport failure = %v", errDispatch)
		}
		if errRelease := client.PushConcurrencyRelease(context.Background(), frame); !errors.Is(errRelease, ErrDispatchFenced) {
			t.Fatalf("release after telemetry transport failure = %v", errRelease)
		}
		// Mirror the supervisor: close the failed client before installing a fresh lifetime.
		client.Close()
		if errPing := oldCommand.Ping(context.Background()).Err(); !errors.Is(errPing, redis.ErrClosed) {
			t.Fatalf("old command pool remained available: %v", errPing)
		}
		if errPing := oldRelease.Ping(context.Background()).Err(); !errors.Is(errPing, redis.ErrClosed) {
			t.Fatalf("old release pool remained available: %v", errPing)
		}
		next := client.NewLifetime()
		next.SetManagedLifetime(true)
		defer next.Close()
		nextCtx, cancelNext := context.WithCancel(context.Background())
		defer cancelNext()
		ready := false
		errNext := next.RunConfigSubscriberLifetime(nextCtx, func([]byte) error {
			return next.SetLifecycleConfig(lifecycle)
		}, func() {
			ready = next.HeartbeatOK() && next.cmd != nil && next.cmd != oldCommand
			cancelNext()
		})
		if !ready || !errors.Is(errNext, context.Canceled) {
			t.Fatalf("coordinated replacement failed: ready=%v error=%v", ready, errNext)
		}
		args := findRedisCommand(commands.All(), "SUBSCRIBE")
		if len(args) != 5 || args[1] != redisChannelConfig || args[2] != "1" || args[3] != "takeover" || args[4] != client.MembershipInstanceID() {
			t.Fatalf("replacement did not validate a fresh takeover subscription: %v", args)
		}
	})
}
