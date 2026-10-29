package home

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func TestWaitForConfigReconnectsNativeClient(t *testing.T) {
	client, commands := newRedisCommandTestClient(t, func(args []string) string {
		if len(args) >= 2 && strings.EqualFold(args[0], "CLUSTER") {
			return "-ERR cluster command unsupported\r\n"
		}
		if len(args) >= 2 && strings.EqualFold(args[0], "GET") && args[1] == redisKeyConfig {
			payload := "port: 8317\n"
			return fmt.Sprintf("$%d\r\n%s\r\n", len(payload), payload)
		}
		return "-ERR unexpected command\r\n"
	})
	options := cloneRedisOptions(client.cmdOptions)
	options.DialerRetries = 1
	var attempts atomic.Int32
	options.Dialer = func(ctx context.Context, network, address string) (net.Conn, error) {
		if attempts.Add(1) == 1 {
			return nil, &net.OpError{Op: "dial", Err: syscall.ECONNREFUSED}
		}
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}
	if err := client.cmd.Close(); err != nil {
		t.Fatal(err)
	}
	client.cmdOptions = cloneRedisOptions(options)
	client.cmd = redis.NewClient(options)
	client.homeCfg.DisableClusterDiscovery = false
	ticks := make(chan time.Time, 1)
	ticks <- time.Time{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	raw, err := waitForConfig(ctx, client.GetConfig, ticks)
	if err != nil || string(raw) != "port: 8317\n" || attempts.Load() != 2 {
		t.Fatalf("result=%q err=%v attempts=%d", raw, err, attempts.Load())
	}
	if count := commands.CountCommandKey("GET", redisKeyConfig); count != 1 {
		t.Fatalf("GET config count=%d, want 1", count)
	}
}

func TestWaitForConfigRecoversTransportWithoutRestart(t *testing.T) {
	ticks := make(chan time.Time, 1)
	ticks <- time.Time{}
	calls := 0
	raw, err := waitForConfig(context.Background(), func(context.Context) ([]byte, error) {
		calls++
		if calls == 1 {
			return nil, &net.OpError{Op: "dial", Err: syscall.ECONNREFUSED}
		}
		return []byte("config"), nil
	}, ticks)
	if err != nil || string(raw) != "config" || calls != 2 {
		t.Fatalf("result=%q err=%v calls=%d", raw, err, calls)
	}
}

func TestWaitForConfigRejectsPermanentFailure(t *testing.T) {
	for _, failure := range []error{ErrConfigNotFound, ErrEmptyResponse, errors.New("authentication rejected"), x509.UnknownAuthorityError{}} {
		calls := 0
		_, err := waitForConfig(context.Background(), func(context.Context) ([]byte, error) {
			calls++
			return nil, failure
		}, nil)
		if !errors.Is(err, failure) || calls != 1 {
			t.Fatalf("err=%v calls=%d", err, calls)
		}
	}
}

func TestWaitForConfigCanceledBeforeFetch(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := waitForConfig(ctx, func(context.Context) ([]byte, error) {
		t.Fatal("canceled bootstrap must not fetch config")
		return nil, nil
	}, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
}

func TestWaitForConfigCancellationStopsRetry(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	_, err := waitForConfig(ctx, func(context.Context) ([]byte, error) {
		calls++
		cancel()
		return nil, io.EOF
	}, nil)
	if !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
}
