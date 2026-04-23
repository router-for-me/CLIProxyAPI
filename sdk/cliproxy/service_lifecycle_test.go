package cliproxy

import (
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/watcher"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v6/sdk/access"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
)

type runtimeStateManagerStub struct {
	restore       func(ctx context.Context) (bool, bool, error)
	flushInterval int
	startPeriodic func(ctx context.Context, intervalSec int)
	stop          func()
	flushNow      func(ctx context.Context) error
	close         func(ctx context.Context) error
}

func (s *runtimeStateManagerStub) Restore(ctx context.Context) (bool, bool, error) {
	if s != nil && s.restore != nil {
		return s.restore(ctx)
	}
	return false, false, nil
}

func (s *runtimeStateManagerStub) FlushIntervalSeconds() int {
	if s != nil && s.flushInterval > 0 {
		return s.flushInterval
	}
	return 30
}

func (s *runtimeStateManagerStub) StartPeriodic(ctx context.Context, intervalSec int) {
	if s != nil && s.startPeriodic != nil {
		s.startPeriodic(ctx, intervalSec)
	}
}

func (s *runtimeStateManagerStub) Stop() {
	if s != nil && s.stop != nil {
		s.stop()
	}
}

func (s *runtimeStateManagerStub) FlushNow(ctx context.Context) error {
	if s != nil && s.flushNow != nil {
		return s.flushNow(ctx)
	}
	return nil
}

func (s *runtimeStateManagerStub) Close(ctx context.Context) error {
	if s != nil && s.close != nil {
		return s.close(ctx)
	}
	return nil
}

type tokenProviderStub struct{}

func (tokenProviderStub) Load(ctx context.Context, cfg *config.Config) (*TokenClientResult, error) {
	return &TokenClientResult{}, nil
}

type apiKeyProviderStub struct{}

func (apiKeyProviderStub) Load(ctx context.Context, cfg *config.Config) (*APIKeyClientResult, error) {
	return &APIKeyClientResult{}, nil
}

func TestServiceBootstrapWatcherState_ReconcilesSnapshotBeforeRestore(t *testing.T) {
	currentAuthID := "bootstrap-current-auth"
	staleAuthID := "bootstrap-stale-auth"

	t.Cleanup(func() {
		GlobalModelRegistry().UnregisterClient(currentAuthID)
		GlobalModelRegistry().UnregisterClient(staleAuthID)
	})

	service := &Service{
		cfg:         &config.Config{},
		coreManager: coreauth.NewManager(nil, nil, nil),
	}

	if _, err := service.coreManager.Register(context.Background(), &coreauth.Auth{ID: staleAuthID, Provider: "claude", Status: coreauth.StatusActive}); err != nil {
		t.Fatalf("failed to seed stale auth: %v", err)
	}

	var sawCurrentDuringRestore bool
	var sawStaleDisabledDuringRestore bool
	var startPeriodicInterval int
	service.runtimeStateMgr = &runtimeStateManagerStub{
		flushInterval: 17,
		restore: func(ctx context.Context) (bool, bool, error) {
			current, ok := service.coreManager.GetByID(currentAuthID)
			sawCurrentDuringRestore = ok && current != nil && !current.Disabled

			stale, ok := service.coreManager.GetByID(staleAuthID)
			sawStaleDisabledDuringRestore = ok && stale != nil && stale.Disabled && stale.Status == coreauth.StatusDisabled
			return false, false, nil
		},
		startPeriodic: func(ctx context.Context, intervalSec int) {
			startPeriodicInterval = intervalSec
		},
	}
	service.watcher = &WatcherWrapper{
		snapshotAuths: func() []*coreauth.Auth {
			return []*coreauth.Auth{{ID: currentAuthID, Provider: "claude", Status: coreauth.StatusActive}}
		},
	}

	service.bootstrapWatcherState(context.Background())

	if !sawCurrentDuringRestore {
		t.Fatal("expected watcher snapshot auth to be synced before runtime restore")
	}
	if !sawStaleDisabledDuringRestore {
		t.Fatal("expected missing stale auth to be disabled before runtime restore")
	}
	if startPeriodicInterval != 17 {
		t.Fatalf("expected StartPeriodic interval 17, got %d", startPeriodicInterval)
	}

	stale, ok := service.coreManager.GetByID(staleAuthID)
	if !ok || stale == nil || !stale.Disabled {
		t.Fatal("expected stale auth to remain disabled after bootstrap")
	}
}

func TestServiceBootstrapWatcherState_AttachesAuthQueueAfterRestore(t *testing.T) {
	service := &Service{
		cfg:         &config.Config{},
		coreManager: coreauth.NewManager(nil, nil, nil),
	}

	callOrder := make([]string, 0, 4)
	queueAttachedDuringRestore := false
	queueAssigned := false
	service.runtimeStateMgr = &runtimeStateManagerStub{
		flushInterval: 9,
		restore: func(ctx context.Context) (bool, bool, error) {
			callOrder = append(callOrder, "restore")
			queueAttachedDuringRestore = service.authUpdates != nil
			return false, false, nil
		},
		startPeriodic: func(ctx context.Context, intervalSec int) {
			callOrder = append(callOrder, "start-periodic")
		},
	}
	service.watcher = &WatcherWrapper{
		snapshotAuths: func() []*coreauth.Auth {
			callOrder = append(callOrder, "snapshot")
			return nil
		},
		setUpdateQueue: func(queue chan<- watcher.AuthUpdate) {
			callOrder = append(callOrder, "set-queue")
			queueAssigned = queue != nil
		},
	}

	service.bootstrapWatcherState(context.Background())

	if queueAttachedDuringRestore {
		t.Fatal("expected auth update queue to remain detached during restore")
	}
	if !queueAssigned {
		t.Fatal("expected watcher auth update queue to be attached after restore")
	}
	if service.authUpdates == nil {
		t.Fatal("expected service auth update queue to be initialized")
	}

	wantOrder := []string{"snapshot", "restore", "start-periodic", "set-queue"}
	if !reflect.DeepEqual(callOrder, wantOrder) {
		t.Fatalf("unexpected bootstrap order: got %v want %v", callOrder, wantOrder)
	}
	if service.authQueueStop != nil {
		service.authQueueStop()
	}
}

func TestServiceShutdown_RuntimeStateStopFlushCloseOrder(t *testing.T) {
	order := make([]string, 0, 3)
	service := &Service{
		runtimeStateMgr: &runtimeStateManagerStub{
			stop: func() {
				order = append(order, "stop")
			},
			flushNow: func(ctx context.Context) error {
				order = append(order, "flush")
				return nil
			},
			close: func(ctx context.Context) error {
				order = append(order, "close")
				return nil
			},
		},
	}

	if err := service.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}

	wantOrder := []string{"stop", "flush", "close"}
	if !reflect.DeepEqual(order, wantOrder) {
		t.Fatalf("unexpected shutdown order: got %v want %v", order, wantOrder)
	}
}

func TestServiceRun_DoesNotPrintStartedMessageWhenServerStartFailsImmediately(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to reserve port: %v", err)
	}
	defer listener.Close()

	port := listener.Addr().(*net.TCPAddr).Port
	authDir := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	afterStartCalled := false

	service := &Service{
		cfg: &config.Config{
			Host:    "127.0.0.1",
			Port:    port,
			AuthDir: authDir,
			CircuitBreakerAutoRemoval: config.CircuitBreakerAutoRemovalConfig{
				Enabled: func() *bool { v := false; return &v }(),
			},
		},
		configPath:     configPath,
		tokenProvider:  tokenProviderStub{},
		apiKeyProvider: apiKeyProviderStub{},
		watcherFactory: func(configPath, authDir string, reload func(*config.Config), reloadLogging func()) (*WatcherWrapper, error) {
			return &WatcherWrapper{
				start:         func(ctx context.Context) error { return nil },
				stop:          func() error { return nil },
				setConfig:     func(cfg *config.Config) {},
				snapshotAuths: func() []*coreauth.Auth { return nil },
			}, nil
		},
		accessManager: sdkaccess.NewManager(),
		coreManager:   coreauth.NewManager(nil, nil, nil),
		hooks: Hooks{
			OnAfterStart: func(s *Service) {
				afterStartCalled = true
			},
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var runErr error
	stdout := captureStdout(t, func() {
		runErr = service.Run(ctx)
	})

	if runErr == nil || !strings.Contains(runErr.Error(), "address already in use") {
		t.Fatalf("expected address-in-use error, got %v", runErr)
	}
	if strings.Contains(stdout, "API server started successfully") {
		t.Fatalf("expected no optimistic startup message on immediate failure, got %q", stdout)
	}
	if afterStartCalled {
		t.Fatal("expected OnAfterStart hook not to run when server start fails immediately")
	}
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()

	originalStdout := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("failed to create stdout pipe: %v", err)
	}
	defer func() {
		os.Stdout = originalStdout
	}()

	os.Stdout = writer
	fn()
	if err := writer.Close(); err != nil {
		t.Fatalf("failed to close stdout writer: %v", err)
	}

	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("failed to read stdout: %v", err)
	}
	if err := reader.Close(); err != nil {
		t.Fatalf("failed to close stdout reader: %v", err)
	}
	return string(output)
}
