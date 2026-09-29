package api

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestServerStop_ViolentShutdownImmediatelyClosesWithoutContextDeadlineError(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// Create a listener on a random available port.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}

	port := ln.Addr().(*net.TCPAddr).Port

	cfg := &config.Config{
		Host: "127.0.0.1",
		Port: port,
	}

	server := NewServer(cfg, nil, nil, "")
	engine := gin.New()
	reqStarted := make(chan struct{})
	engine.GET("/slow", func(c *gin.Context) {
		close(reqStarted)
		time.Sleep(2 * time.Second)
		c.String(http.StatusOK, "ok")
	})
	server.server.Handler = engine

	// Start the server with the listener.
	go func() {
		_ = server.server.Serve(ln)
	}()

	// Start an in-flight request.
	reqDone := make(chan error, 1)
	go func() {
		resp, errGet := http.Get("http://" + ln.Addr().String() + "/slow")
		if errGet != nil {
			reqDone <- errGet
			return
		}
		defer func() {
			if errCloseBody := resp.Body.Close(); errCloseBody != nil {
				t.Logf("response body close error: %v", errCloseBody)
			}
		}()
		reqDone <- nil
	}()

	<-reqStarted

	// Call server.Stop with an expired context (simulating the expired shutdown deadline).
	// Under violent shutdown (s.server.Close), the server should immediately close in-flight
	// connections without waiting and without returning context deadline exceeded.
	expiredCtx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()

	stopStart := time.Now()
	errStop := server.Stop(expiredCtx)
	stopDuration := time.Since(stopStart)

	if stopDuration >= time.Second {
		t.Fatalf("server.Stop took %v, want immediate violent shutdown (<1s)", stopDuration)
	}
	if errStop != nil {
		t.Fatalf("server.Stop error = %v, want nil for violent shutdown", errStop)
	}

	// Verify the in-flight request was terminated / interrupted.
	select {
	case errReq := <-reqDone:
		if errReq == nil {
			t.Fatal("expected in-flight request to be interrupted/error, but it completed successfully")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for in-flight request to be interrupted by server.Stop")
	}
}
