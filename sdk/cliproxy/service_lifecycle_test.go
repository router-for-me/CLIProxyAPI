package cliproxy

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

type noopTokenProvider struct{}

func (noopTokenProvider) Load(context.Context, *config.Config) (*TokenClientResult, error) {
	return &TokenClientResult{}, nil
}

type noopAPIKeyProvider struct{}

func (noopAPIKeyProvider) Load(context.Context, *config.Config) (*APIKeyClientResult, error) {
	return &APIKeyClientResult{}, nil
}

func noopWatcherFactory(string, string, func(*config.Config)) (*WatcherWrapper, error) {
	return &WatcherWrapper{}, nil
}

type proberResultHook struct {
	results chan coreauth.Result
}

func (h *proberResultHook) OnAuthRegistered(context.Context, *coreauth.Auth) {}
func (h *proberResultHook) OnAuthUpdated(context.Context, *coreauth.Auth)    {}
func (h *proberResultHook) OnResult(_ context.Context, result coreauth.Result) {
	h.results <- result
}

func TestServiceRunStartsProberAfterStartupExecutors(t *testing.T) {
	// Pick a free port so we can poll the server for readiness before cancelling.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatalf("close listener: %v", err)
	}

	cfg := &config.Config{
		Host:    "127.0.0.1",
		Port:    port,
		AuthDir: t.TempDir(),
	}

	proberCfg := internalconfig.CredentialProberConfig{
		Enabled:            true,
		Interval:           time.Hour,
		MaxConcurrency:     1,
		RateLimitPerMinute: 1000,
	}
	hook := &proberResultHook{results: make(chan coreauth.Result, 1)}
	manager := coreauth.NewManager(nil, nil, hook)
	manager.SetConfig(&internalconfig.Config{CredentialProber: proberCfg})

	if _, err := manager.Register(coreauth.WithSkipPersist(context.Background()), &coreauth.Auth{
		ID:       "aistudio-auth",
		Provider: "aistudio",
		Status:   coreauth.StatusActive,
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	service := &Service{
		cfg:            cfg,
		coreManager:    manager,
		tokenProvider:  noopTokenProvider{},
		apiKeyProvider: noopAPIKeyProvider{},
		watcherFactory: noopWatcherFactory,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	runErr := make(chan error, 1)
	go func() {
		runErr <- service.Run(ctx)
	}()

	// Wait for the prober to sweep the aistudio executor, which is only
	// registered after the websocket gateway is created.
	var result coreauth.Result
	select {
	case result = <-hook.results:
	case err := <-runErr:
		t.Fatalf("service.Run returned before prober result: %v", err)
	case <-ctx.Done():
		t.Fatal("timeout waiting for prober result")
	}
	if result.Provider != "aistudio" {
		t.Fatalf("probed provider = %q, want aistudio", result.Provider)
	}

	// Wait for the HTTP server to be ready before cancelling, so Shutdown does
	// not race Start.
	healthURL := fmt.Sprintf("http://127.0.0.1:%d/healthz", port)
	if err := waitForHTTP(healthURL, 5*time.Second); err != nil {
		t.Fatalf("server healthz never ready: %v", err)
	}

	cancel()
	select {
	case err := <-runErr:
		if err != nil && err != context.Canceled {
			t.Fatalf("service.Run returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("service.Run did not stop after context cancel")
	}
}

func waitForHTTP(url string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: 100 * time.Millisecond}
	for time.Now().Before(deadline) {
		resp, err := client.Get(url)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	return fmt.Errorf("timeout waiting for %s", url)
}
