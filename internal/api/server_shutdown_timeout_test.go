package api

import (
	"context"
	"io"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

// shutdownTestFixture bundles a test server whose /slow handler blocks until
// release is closed (or the request context is canceled), mirroring the existing
// violent shutdown test setup while remaining deterministic.
type shutdownTestFixture struct {
	server      *Server
	addr        string
	reqStarted  <-chan struct{}
	release     chan struct{}
	releaseOnce sync.Once
}

func (fx *shutdownTestFixture) releaseHandler() {
	fx.releaseOnce.Do(func() { close(fx.release) })
}

func newShutdownTestServer(t *testing.T) *shutdownTestFixture {
	t.Helper()
	gin.SetMode(gin.TestMode)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	if errClose := ln.Close(); errClose != nil {
		t.Fatalf("failed to close probe listener: %v", errClose)
	}
	addr := ln.Addr().String()

	cfg := &config.Config{Host: "127.0.0.1", Port: ln.Addr().(*net.TCPAddr).Port}
	server := NewServer(cfg, nil, nil, "")

	release := make(chan struct{})
	reqStarted := make(chan struct{})
	engine := gin.New()
	engine.GET("/slow", func(c *gin.Context) {
		close(reqStarted)
		select {
		case <-release:
		case <-c.Request.Context().Done():
		}
		c.String(http.StatusOK, "ok")
	})
	engine.GET("/fast", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})
	server.server.Handler = engine

	serveLn, errListen := net.Listen("tcp", addr)
	if errListen != nil {
		t.Fatalf("failed to re-listen on %s: %v", addr, errListen)
	}
	go func() {
		_ = server.server.Serve(serveLn)
	}()
	fx := &shutdownTestFixture{
		server:     server,
		addr:       addr,
		reqStarted: reqStarted,
		release:    release,
	}
	t.Cleanup(func() {
		fx.releaseHandler()
		_ = server.server.Close()
		_ = serveLn.Close()
	})

	return fx
}

// startSlowRequest issues a GET /slow and reports the terminal outcome:
// the error is nil only when the handler completed and a 200 body was read.
func startSlowRequest(t *testing.T, addr string) chan error {
	t.Helper()
	reqDone := make(chan error, 1)
	go func() {
		resp, errGet := http.Get("http://" + addr + "/slow")
		if errGet != nil {
			reqDone <- errGet
			return
		}
		defer func() {
			if errCloseBody := resp.Body.Close(); errCloseBody != nil {
				t.Logf("response body close error: %v", errCloseBody)
			}
		}()
		if _, errRead := io.ReadAll(resp.Body); errRead != nil {
			reqDone <- errRead
			return
		}
		if resp.StatusCode != http.StatusOK {
			reqDone <- errUnexpectedStatus(resp.StatusCode)
			return
		}
		reqDone <- nil
	}()
	return reqDone
}

type errUnexpectedStatus int

func (e errUnexpectedStatus) Error() string { return "unexpected status" }

// TestServerStop_ShutdownTimeoutDrainsInFlightRequests verifies that a positive
// server.shutdown-timeout lets an in-flight request complete before the process
// exits, instead of being cut immediately.
func TestServerStop_ShutdownTimeoutDrainsInFlightRequests(t *testing.T) {
	fx := newShutdownTestServer(t)

	// Opt in to a 5-second drain budget.
	fx.server.getConfig().ShutdownTimeoutSeconds = 5

	reqDone := startSlowRequest(t, fx.addr)
	<-fx.reqStarted

	// Release the handler shortly after Stop begins: it must still be allowed to
	// answer because the drain keeps the connection alive.
	go func() {
		time.Sleep(150 * time.Millisecond)
		fx.releaseHandler()
	}()

	stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if errStop := fx.server.Stop(stopCtx); errStop != nil {
		t.Fatalf("server.Stop error = %v, want nil", errStop)
	}

	select {
	case errReq := <-reqDone:
		if errReq != nil {
			t.Fatalf("in-flight request failed during graceful drain: %v", errReq)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("in-flight request did not complete within the drain budget")
	}
}

// TestServerStop_ShutdownTimeoutFallsBackToClose verifies that an in-flight
// request outliving the configured drain budget is force-closed, and that Stop
// returns within that budget rather than the caller's more generous deadline.
func TestServerStop_ShutdownTimeoutFallsBackToClose(t *testing.T) {
	fx := newShutdownTestServer(t)

	fx.server.getConfig().ShutdownTimeoutSeconds = 1

	reqDone := startSlowRequest(t, fx.addr)
	<-fx.reqStarted

	// Caller deadline is 60s, far beyond the 1s configured budget: Stop must be
	// bounded by the configured value, not the parent context.
	stopCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	stopStart := time.Now()
	errStop := fx.server.Stop(stopCtx)
	stopDuration := time.Since(stopStart)

	if errStop != nil {
		t.Fatalf("server.Stop error = %v, want nil", errStop)
	}
	if stopDuration >= 10*time.Second {
		t.Fatalf("server.Stop took %v, want bounded shutdown (~1s)", stopDuration)
	}
	if stopDuration < 500*time.Millisecond {
		t.Fatalf("server.Stop returned in %v, faster than the 1s drain budget", stopDuration)
	}

	// The request outlived the drain budget and must have been force-closed.
	select {
	case errReq := <-reqDone:
		if errReq == nil {
			t.Fatal("in-flight request completed after the drain budget expired; force Close did not interrupt it")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("in-flight request was not interrupted after the drain budget expired")
	}
}

// TestServerStop_ShutdownTimeoutTighterParentCtx verifies that a caller context
// tighter than the configured budget still wins, ending the drain early.
func TestServerStop_ShutdownTimeoutTighterParentCtx(t *testing.T) {
	fx := newShutdownTestServer(t)

	fx.server.getConfig().ShutdownTimeoutSeconds = 30

	reqDone := startSlowRequest(t, fx.addr)
	<-fx.reqStarted

	stopCtx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	stopStart := time.Now()
	errStop := fx.server.Stop(stopCtx)
	stopDuration := time.Since(stopStart)

	if errStop != nil {
		t.Fatalf("server.Stop error = %v, want nil", errStop)
	}
	if stopDuration >= 10*time.Second {
		t.Fatalf("server.Stop took %v, want ~500ms bounded by parent ctx", stopDuration)
	}

	select {
	case errReq := <-reqDone:
		if errReq == nil {
			t.Fatal("in-flight request completed despite the tighter parent deadline")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("in-flight request was not interrupted after the parent deadline")
	}
}

// TestServerStop_ShutdownTimeoutZeroKeepsImmediateClose verifies that the default
// value 0 keeps today's immediate-close behavior even when the caller supplies a
// generous context deadline.
func TestServerStop_ShutdownTimeoutZeroKeepsImmediateClose(t *testing.T) {
	fx := newShutdownTestServer(t)

	// Default: ShutdownTimeoutSeconds stays 0.
	reqDone := startSlowRequest(t, fx.addr)
	<-fx.reqStarted

	stopCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	stopStart := time.Now()
	errStop := fx.server.Stop(stopCtx)
	stopDuration := time.Since(stopStart)

	if errStop != nil {
		t.Fatalf("server.Stop error = %v, want nil", errStop)
	}
	if stopDuration >= time.Second {
		t.Fatalf("server.Stop took %v, want immediate close (<1s)", stopDuration)
	}

	select {
	case errReq := <-reqDone:
		if errReq == nil {
			t.Fatal("expected in-flight request to be interrupted immediately, but it completed")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for in-flight request to be interrupted")
	}
}
