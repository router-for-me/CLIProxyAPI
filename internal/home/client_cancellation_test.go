package home

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestManagedSubscriberCancellationPreservesFinalRelease(t *testing.T) {
	for _, existingReleaseClient := range []bool{false, true} {
		t.Run(fmt.Sprintf("existingReleaseClient=%v", existingReleaseClient), func(t *testing.T) {
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
			client.testOperationTimeout = time.Second
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			frame := ConcurrencyReleaseFrame{CredentialID: "credential-a", Model: "test-model", ReleaseSeq: 1}
			var errInitial error
			ready := false
			errRun := client.RunConfigSubscriberLifetime(ctx, func([]byte) error {
				return client.SetLifecycleConfig(config.CredentialConcurrencyConfig{LifecycleConfigRevision: 1})
			}, func() {
				ready = client.HeartbeatOK()
				if existingReleaseClient {
					errInitial = client.PushConcurrencyRelease(ctx, frame)
				}
				cancel()
			})
			if !ready || !errors.Is(errRun, context.Canceled) || errInitial != nil {
				t.Fatalf("subscriber cancellation: ready=%v error=%v initial release=%v", ready, errRun, errInitial)
			}
			if client.HeartbeatOK() || client.AmbiguousDispatch() {
				t.Fatal("graceful cancellation retained a heartbeat or introduced ambiguity")
			}
			if _, errDispatch := client.RPopAuth(context.Background(), "late-model", "", nil, 1); !errors.Is(errDispatch, ErrDispatchFenced) {
				t.Fatalf("graceful cancellation reopened dispatch: %v", errDispatch)
			}
			frame.ReleaseSeq = 2
			if errRelease := client.PushConcurrencyRelease(context.WithoutCancel(ctx), frame); errRelease != nil {
				t.Fatalf("final release after normal cancellation: %v", errRelease)
			}
			var releases []ConcurrencyReleaseFrame
			for _, args := range commands.All() {
				if len(args) == 3 && strings.EqualFold(args[0], "LPUSH") && args[1] == redisKeyConcurrencyRelease {
					var sent ConcurrencyReleaseFrame
					if errJSON := json.Unmarshal([]byte(args[2]), &sent); errJSON != nil {
						t.Fatal(errJSON)
					}
					releases = append(releases, sent)
				}
			}
			wantCount := 1
			if existingReleaseClient {
				wantCount++
			}
			if len(releases) != wantCount || releases[len(releases)-1] != frame {
				t.Fatalf("release frames = %#v", releases)
			}
			client.Close()
			if errRelease := client.PushConcurrencyRelease(context.Background(), frame); !errors.Is(errRelease, ErrDispatchFenced) {
				t.Fatalf("release after Close = %v, want fenced", errRelease)
			}
		})
	}
}

func TestManagedSubscriberReceiveFailureKeepsReleaseFenced(t *testing.T) {
	client, commands := newRedisCommandTestClient(t, func(args []string) string {
		switch strings.ToUpper(args[0]) {
		case "GET":
			return "$2\r\n{}\r\n"
		case "SUBSCRIBE":
			// A malformed RESP frame fails reception after the membership ACK.
			return "*3\r\n$9\r\nsubscribe\r\n$6\r\nconfig\r\n:1\r\n?invalid\r\n"
		default:
			return balanceTestRedisReply(args)
		}
	})
	client.SetManagedLifetime(true)
	client.testOperationTimeout = time.Second
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errRun := client.RunConfigSubscriberLifetime(ctx, func([]byte) error {
		return client.SetLifecycleConfig(config.CredentialConcurrencyConfig{LifecycleConfigRevision: 1})
	}, nil)
	if errRun == nil || errors.Is(errRun, context.Canceled) {
		t.Fatalf("receive failure = %v", errRun)
	}
	// Later graceful cancellation must not undo a fence installed by a receive failure.
	cancel()
	if client.HeartbeatOK() || !client.dispatchFenced.Load() {
		t.Fatal("abnormal reception did not fence the lifetime")
	}
	frame := ConcurrencyReleaseFrame{CredentialID: "credential-a", Model: "test-model", ReleaseSeq: 1}
	if errRelease := client.PushConcurrencyRelease(context.WithoutCancel(ctx), frame); !errors.Is(errRelease, ErrDispatchFenced) {
		t.Fatalf("release after receive failure = %v, want fenced", errRelease)
	}
	if _, errDispatch := client.RPopAuth(context.Background(), "test-model", "", nil, 1); !errors.Is(errDispatch, ErrDispatchFenced) {
		t.Fatalf("dispatch after receive failure = %v, want fenced", errDispatch)
	}
	if commands.CountCommandKey("LPUSH", redisKeyConcurrencyRelease) != 0 {
		t.Fatal("fenced lifetime sent a release")
	}
}
