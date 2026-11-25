package auth

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func allocateFreeCallbackPort(t *testing.T) int {
	t.Helper()
	ln, errListen := net.Listen("tcp", ":0")
	if errListen != nil {
		t.Fatalf("failed to allocate free port: %v", errListen)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if errClose := ln.Close(); errClose != nil {
		t.Fatalf("failed to release temporary port %d: %v", port, errClose)
	}
	return port
}

func TestAntigravityCallbackServerDuplicateRequestDoesNotBlock(t *testing.T) {
	freePort := allocateFreeCallbackPort(t)
	srv, port, cbChan, errServer := startAntigravityCallbackServer(freePort)
	if errServer != nil {
		t.Fatalf("failed to start callback server: %v", errServer)
	}
	defer func() {
		if errClose := srv.Close(); errClose != nil {
			t.Logf("callback server close error: %v", errClose)
		}
	}()

	client := &http.Client{Timeout: 500 * time.Millisecond}
	baseURL := fmt.Sprintf("http://127.0.0.1:%d/oauth-callback", port)

	// First callback fills the buffered channel (capacity 1) without being drained yet.
	resp1, errGet1 := client.Get(baseURL + "?code=code-1&state=state-1")
	if errGet1 != nil {
		t.Fatalf("first callback request failed: %v", errGet1)
	}
	_, _ = io.Copy(io.Discard, resp1.Body)
	if errClose1 := resp1.Body.Close(); errClose1 != nil {
		t.Fatalf("failed to close first response body: %v", errClose1)
	}
	if resp1.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200 for first callback, got %d", resp1.StatusCode)
	}

	// Second duplicate callback must return promptly instead of blocking on resultCh.
	resp2, errGet2 := client.Get(baseURL + "?code=code-2&state=state-2")
	if errGet2 != nil {
		t.Fatalf("duplicate callback request blocked or failed: %v", errGet2)
	}
	_, _ = io.Copy(io.Discard, resp2.Body)
	if errClose2 := resp2.Body.Close(); errClose2 != nil {
		t.Fatalf("failed to close second response body: %v", errClose2)
	}
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200 for duplicate callback, got %d", resp2.StatusCode)
	}

	// The first callback result must be preserved.
	select {
	case got := <-cbChan:
		if got.Code != "code-1" || got.State != "state-1" || got.Error != "" {
			t.Fatalf("unexpected callback result: %+v", got)
		}
	default:
		t.Fatalf("expected first callback result to be buffered in cbChan")
	}

	// Graceful shutdown must complete promptly without stalling on a blocked handler.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	if errShutdown := srv.Shutdown(shutdownCtx); errShutdown != nil {
		t.Fatalf("callback server shutdown stalled or failed: %v", errShutdown)
	}
}

type staticRoundTripper func(*http.Request) (*http.Response, error)

func (f staticRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type doneSignalingContext struct {
	context.Context
	once    sync.Once
	entered chan struct{}
}

func (c *doneSignalingContext) Done() <-chan struct{} {
	c.once.Do(func() {
		close(c.entered)
	})
	return c.Context.Done()
}

func TestAntigravityLoginRespectsContextCancel(t *testing.T) {
	origTransport := http.DefaultTransport
	t.Cleanup(func() {
		http.DefaultTransport = origTransport
	})
	http.DefaultTransport = staticRoundTripper(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("127.0.0.1")),
			Request:    req,
		}, nil
	})

	freePort := allocateFreeCallbackPort(t)
	baseCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	waitCtx := &doneSignalingContext{
		Context: baseCtx,
		entered: make(chan struct{}),
	}

	type loginResult struct {
		auth *coreauth.Auth
		err  error
	}
	doneCh := make(chan loginResult, 1)

	go func() {
		record, errLogin := AntigravityAuthenticator{}.Login(waitCtx, &config.Config{}, &LoginOptions{
			NoBrowser:    true,
			CallbackPort: freePort,
		})
		doneCh <- loginResult{auth: record, err: errLogin}
	}()

	select {
	case <-waitCtx.entered:
	case <-time.After(2 * time.Second):
		t.Fatalf("AntigravityAuthenticator.Login did not enter callback wait loop")
	}

	cancel()

	select {
	case res := <-doneCh:
		if !errors.Is(res.err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got auth=%v err=%v", res.auth, res.err)
		}
	case <-time.After(1 * time.Second):
		t.Fatalf("AntigravityAuthenticator.Login did not return after context cancellation")
	}
}
