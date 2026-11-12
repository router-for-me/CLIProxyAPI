package home

import (
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

func TestManagedSubscriberOrdinaryTrafficDoesNotRenewHeartbeat(t *testing.T) {
	for _, mode := range []string{"config", "cluster", "blocked config consumer"} {
		t.Run(mode, func(t *testing.T) {
			client, _ := newRedisCommandTestClient(t, func(args []string) string {
				if strings.EqualFold(args[0], "GET") {
					return "$2\r\n{}\r\n"
				}
				return balanceTestRedisReply(args)
			})
			client.SetManagedLifetime(true)
			synctest.Test(t, func(t *testing.T) {
				defer client.Close()
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
				defer cancel()
				releaseConsumer := make(chan struct{})
				var messages atomic.Int32
				sub := redis.NewClient(&redis.Options{
					Addr: "127.0.0.1:8327", Protocol: 2, DisableIdentity: true, MaxRetries: -1,
					Dialer: func(context.Context, string, string) (net.Conn, error) {
						local, peer := net.Pipe()
						go serveRedisCommandTestConn(peer, &redisCommandLog{}, func(args []string) string {
							if !strings.EqualFold(args[0], "SUBSCRIBE") {
								return balanceTestRedisReply(args)
							}
							go func() {
								ticker := time.NewTicker(10 * time.Second)
								defer ticker.Stop()
								for {
									select {
									case <-ctx.Done():
										return
									case <-ticker.C:
									}
									channel, payload := redisChannelConfig, "{}"
									if mode == "cluster" {
										channel, payload = redisChannelCluster, `{"ok":true,"nodes":[]}`
									}
									frame := fmt.Sprintf("*3\r\n$7\r\nmessage\r\n$%d\r\n%s\r\n$%d\r\n%s\r\n", len(channel), channel, len(payload), payload)
									if _, errWrite := io.WriteString(peer, frame); errWrite != nil {
										if !errors.Is(errWrite, io.ErrClosedPipe) && !errors.Is(errWrite, net.ErrClosed) {
											t.Errorf("write ordinary subscription message: %v", errWrite)
										}
										return
									}
									messages.Add(1)
								}
							}()
							return "*3\r\n$9\r\nsubscribe\r\n$6\r\nconfig\r\n:1\r\n"
						})
						return local, nil
					},
				})
				defer func() {
					if errClose := sub.Close(); errClose != nil {
						t.Error(errClose)
					}
				}()
				ready := make(chan struct{})
				done := make(chan error, 1)
				go func() {
					initial := true
					done <- client.RunConfigSubscriberLifetime(ctx, func([]byte) error {
						if !initial {
							if mode == "blocked config consumer" {
								<-releaseConsumer
							}
							return nil
						}
						initial = false
						if errConfig := client.SetLifecycleConfig(config.CredentialConcurrencyConfig{
							LifecycleConfigRevision: 1, CPAHeartbeatTimeout: time.Minute,
						}); errConfig != nil {
							return errConfig
						}
						client.mu.Lock()
						previousSub := client.sub
						client.sub = sub
						client.mu.Unlock()
						if errClose := previousSub.Close(); errClose != nil {
							return fmt.Errorf("close bootstrap subscription: %w", errClose)
						}
						return nil
					}, func() { close(ready) })
				}()
				select {
				case <-ready:
				case errRun := <-done:
					close(releaseConsumer)
					t.Fatalf("subscription did not become ready: %v", errRun)
				}
				// Virtual time keeps this deadline test independent of wall-clock scheduling.
				time.Sleep(time.Minute)
				synctest.Wait()
				heartbeatOK := client.HeartbeatOK()
				fenced := client.dispatchFenced.Load() && client.releaseFenced.Load()
				close(releaseConsumer)
				synctest.Wait()
				errRun := <-done
				if heartbeatOK || !fenced || !isTimeoutError(errRun) || ctx.Err() != nil {
					t.Fatalf("ordinary traffic extended heartbeat liveness: healthy=%t fenced=%t error=%v context=%v", heartbeatOK, fenced, errRun, ctx.Err())
				}
				if messages.Load() < 5 {
					t.Fatalf("insufficient ordinary traffic before heartbeat expiry: %d messages", messages.Load())
				}
			})
		})
	}
}
