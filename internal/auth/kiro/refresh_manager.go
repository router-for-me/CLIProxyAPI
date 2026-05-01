package kiro

import (
	"context"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	log "github.com/sirupsen/logrus"
)

// RefreshManager is a singleton manager for background token refreshing.
type RefreshManager struct {
	mu               sync.Mutex
	refresher        *BackgroundRefresher
	ctx              context.Context
	cancel           context.CancelFunc
	started          bool
	onTokenRefreshed func(tokenID string, tokenData *KiroTokenData)
}

var (
	globalRefreshManager *RefreshManager
	managerOnce          sync.Once
)

// GetRefreshManager returns the global RefreshManager singleton.
func GetRefreshManager() *RefreshManager {
	managerOnce.Do(func() {
		globalRefreshManager = &RefreshManager{}
	})
	return globalRefreshManager
}

// Initialize sets up the background refresher.
func (m *RefreshManager) Initialize(baseDir string, cfg *config.Config) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.started {
		log.Debug("refresh manager: already initialized")
		return nil
	}

	if baseDir == "" {
		log.Warn("refresh manager: base directory not provided, skipping initialization")
		return nil
	}

	resolvedBaseDir, err := util.ResolveAuthDir(baseDir)
	if err != nil {
		log.Warnf("refresh manager: failed to resolve auth directory %s: %v", baseDir, err)
	}
	if resolvedBaseDir != "" {
		baseDir = resolvedBaseDir
	}

	repo := NewFileTokenRepository(baseDir)

	opts := []RefresherOption{
		WithInterval(time.Minute),
		WithBatchSize(50),
		WithConcurrency(10),
		WithConfig(cfg),
	}

	// Pass callback to BackgroundRefresher if already set
	if m.onTokenRefreshed != nil {
		opts = append(opts, WithOnTokenRefreshed(m.onTokenRefreshed))
	}

	m.refresher = NewBackgroundRefresher(repo, opts...)

	log.Infof("refresh manager: initialized with base directory %s", baseDir)
	return nil
}

// Start begins background token refreshing.
func (m *RefreshManager) Start() {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.started {
		log.Debug("refresh manager: already started")
		return
	}

	if m.refresher == nil {
		log.Warn("refresh manager: not initialized, cannot start")
		return
	}

	m.ctx, m.cancel = context.WithCancel(context.Background())
	m.refresher.Start(m.ctx)
	m.started = true

	log.Info("refresh manager: background refresh started")
}

// Stop halts background token refreshing.
func (m *RefreshManager) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.started {
		return
	}

	if m.cancel != nil {
		m.cancel()
	}

	if m.refresher != nil {
		m.refresher.Stop()
	}

	m.started = false
	log.Info("refresh manager: background refresh stopped")
}

// IsRunning reports whether background refreshing is active.
func (m *RefreshManager) IsRunning() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.started
}

// UpdateBaseDir changes the token directory at runtime.
func (m *RefreshManager) UpdateBaseDir(baseDir string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.refresher != nil && m.refresher.tokenRepo != nil {
		if repo, ok := m.refresher.tokenRepo.(*FileTokenRepository); ok {
			repo.SetBaseDir(baseDir)
			log.Infof("refresh manager: updated base directory to %s", baseDir)
		}
	}
}

// SetOnTokenRefreshed registers a callback invoked after a successful token refresh.
// Can be called at any time; supports runtime callback updates.
func (m *RefreshManager) SetOnTokenRefreshed(callback func(tokenID string, tokenData *KiroTokenData)) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.onTokenRefreshed = callback

	// Update the refresher's callback in a thread-safe manner if already created
	if m.refresher != nil {
		m.refresher.callbackMu.Lock()
		m.refresher.onTokenRefreshed = callback
		m.refresher.callbackMu.Unlock()
	}

	log.Debug("refresh manager: token refresh callback registered")
}

// InitializeAndStart initializes and starts background refreshing (convenience method).
func InitializeAndStart(baseDir string, cfg *config.Config) {
	// Initialize global fingerprint config
	initGlobalFingerprintConfig(cfg)

	manager := GetRefreshManager()
	if err := manager.Initialize(baseDir, cfg); err != nil {
		log.Errorf("refresh manager: initialization failed: %v", err)
		return
	}
	manager.Start()
}

// initGlobalFingerprintConfig loads fingerprint settings from application config.
func initGlobalFingerprintConfig(cfg *config.Config) {
	if cfg == nil || cfg.KiroFingerprint == nil {
		return
	}
	fpCfg := cfg.KiroFingerprint
	SetGlobalFingerprintConfig(&FingerprintConfig{
		OIDCSDKVersion:      fpCfg.OIDCSDKVersion,
		RuntimeSDKVersion:   fpCfg.RuntimeSDKVersion,
		StreamingSDKVersion: fpCfg.StreamingSDKVersion,
		OSType:              fpCfg.OSType,
		OSVersion:           fpCfg.OSVersion,
		NodeVersion:         fpCfg.NodeVersion,
		KiroVersion:         fpCfg.KiroVersion,
		KiroHash:            fpCfg.KiroHash,
	})
	log.Debug("kiro: global fingerprint config loaded")
}

// InitFingerprintConfig initializes the global fingerprint config from application config.
func InitFingerprintConfig(cfg *config.Config) {
	initGlobalFingerprintConfig(cfg)
}

// StopGlobalRefreshManager stops the global refresh manager.
func StopGlobalRefreshManager() {
	if globalRefreshManager != nil {
		globalRefreshManager.Stop()
	}
}
