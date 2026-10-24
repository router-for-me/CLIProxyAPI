package home

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/redis/go-redis/v9"
)

func TestSubscriptionTelemetryPreservesHeartbeatDeadline(t *testing.T) {
	for _, test := range []struct {
		name       string
		timeout    time.Duration
		interval   time.Duration
		stale      bool
		heartbeats bool
	}{
		{name: "default timeout", timeout: homeSubscriptionReceiveTimeout, interval: 30 * time.Second},
		{name: "timeout longer than sampling interval", timeout: 75 * time.Second, interval: 30 * time.Second},
		{name: "stale telemetry", timeout: homeSubscriptionReceiveTimeout, interval: time.Second, stale: true},
		{name: "normal heartbeats renew deadline", timeout: homeSubscriptionReceiveTimeout, interval: time.Second, heartbeats: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				local, peer := net.Pipe()
				defer func() {
					if errClose := peer.Close(); errClose != nil {
						t.Error(errClose)
					}
				}()
				go serveRedisCommandTestConn(peer, &redisCommandLog{}, func(args []string) string {
					if strings.EqualFold(args[0], "SUBSCRIBE") {
						return "*3\r\n$9\r\nsubscribe\r\n$6\r\nconfig\r\n:1\r\n"
					}
					return balanceTestRedisReply(args)
				})
				client := redis.NewClient(&redis.Options{
					Addr: "127.0.0.1:8327", Protocol: 2, DisableIdentity: true, MaxRetries: -1,
					Dialer: func(context.Context, string, string) (net.Conn, error) { return local, nil },
				})
				defer func() {
					if errClose := client.Close(); errClose != nil {
						t.Error(errClose)
					}
				}()
				ctx := context.Background()
				pubsub := client.Subscribe(ctx, redisChannelConfig)
				defer func() {
					if errClose := pubsub.Close(); errClose != nil {
						t.Error(errClose)
					}
				}()
				if errACK := receiveSubscriptionACKs(ctx, pubsub, test.timeout, []string{redisChannelConfig}); errACK != nil {
					t.Fatal(errACK)
				}
				sampler := &clusterRTTSampler{history: &clusterLatencyHistory{}, node: clusterNode{IP: "home", Port: 8327}}
				started := time.Now()
				wantElapsed, wantHeartbeats := test.timeout, 0
				if test.heartbeats {
					wantElapsed += 4 * time.Second
					wantHeartbeats = 2
				}
				stop := make(chan struct{})
				defer close(stop)
				go func() {
					timer := time.NewTimer(2 * time.Second)
					defer timer.Stop()
					for {
						select {
						case <-stop:
							return
						case <-timer.C:
						}
						elapsed := time.Since(started)
						if elapsed >= wantElapsed {
							return
						}
						token := ""
						if !test.heartbeats || (elapsed != 2*time.Second && elapsed != 4*time.Second) {
							token = sampler.begin(time.Now())
							if test.stale {
								sampler.expire(time.Now(), true)
							}
						}
						if _, errWrite := io.WriteString(peer, fmt.Sprintf("*2\r\n$4\r\npong\r\n$%d\r\n%s\r\n", len(token), token)); errWrite != nil {
							// The receiver closes the pipe when its heartbeat deadline expires.
							if !errors.Is(errWrite, io.ErrClosedPipe) && !errors.Is(errWrite, net.ErrClosed) {
								t.Errorf("write telemetry PONG: %v", errWrite)
							}
							return
						}
						timer.Reset(test.interval)
					}
				}()
				heartbeats := 0
				for {
					event, errReceive := receiveSubscriptionEvent(ctx, pubsub, sampler, test.timeout)
					if errReceive != nil {
						if !isTimeoutError(errReceive) {
							t.Fatalf("receive error = %v, want heartbeat timeout", errReceive)
						}
						break
					}
					pong, ok := event.(*redis.Pong)
					if !ok || pong.Payload != "" {
						t.Fatalf("telemetry escaped to the heartbeat consumer: %#v", event)
					}
					heartbeats++
				}
				if elapsed := time.Since(started); elapsed != wantElapsed || heartbeats != wantHeartbeats {
					t.Fatalf("elapsed=%v heartbeats=%d, want %v and %d", elapsed, heartbeats, wantElapsed, wantHeartbeats)
				}
				if samples := sampler.history.report(time.Now()); len(samples) != 1 || samples[0].Valid == test.stale {
					t.Fatalf("telemetry was not sampled independently of liveness: %+v", samples)
				}
			})
		})
	}
}
