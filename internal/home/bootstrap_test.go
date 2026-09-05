package home

import (
	"context"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"syscall"
	"testing"
	"time"
)

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
