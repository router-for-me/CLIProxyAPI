package kiro

import (
	"context"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	log "github.com/sirupsen/logrus"
)

// RefreshManager 是后台刷新器的单例管理器
type RefreshManager struct {
	mu               sync.Mutex
	refresher        *BackgroundRefresher
	ctx              context.Context
	cancel           context.CancelFunc
	started          bool
	onTokenRefreshed func(tokenID string, tokenData *KiroTokenData) // 刷新成功回调
}

var (
	globalRefreshManager *RefreshManager
	managerOnce          sync.Once
)

// GetRefreshManager 获取全局刷新管理器实例
func GetRefreshManager() *RefreshManager {
	managerOnce.Do(func() {
		globalRefreshManager = &RefreshManager{}
	})
	return globalRefreshManager
}

// Initialize 初始化后台刷新器
// baseDir: token 文件所在的目录
// cfg: 应用配置
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

	// 创建 token 存储库
	repo := NewFileTokenRepository(baseDir)

	// 创建后台刷新器，配置参数
	opts := []RefresherOption{
		WithInterval(time.Minute), // 每分钟检查一次
		WithBatchSize(50),         // 每批最多处理 50 个 token
		WithConcurrency(10),       // 最多 10 个并发刷新
		WithConfig(cfg),           // 设置 OAuth 和 SSO 客户端
	}

	// 如果已设置回调，传递给 BackgroundRefresher
	if m.onTokenRefreshed != nil {
		opts = append(opts, WithOnTokenRefreshed(m.onTokenRefreshed))
	}

	m.refresher = NewBackgroundRefresher(repo, opts...)

	log.Infof("refresh manager: initialized with base directory %s", baseDir)
	return nil
}

// Start 启动后台刷新
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

// Stop 停止后台刷新
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

// IsRunning 检查后台刷新是否正在运行
func (m *RefreshManager) IsRunning() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.started
}

// UpdateBaseDir 更新 token 目录（用于运行时配置更改）
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

// SetOnTokenRefreshed 设置 token 刷新成功后的回调函数
// 可以在任何时候调用，支持运行时更新回调
// callback: 回调函数，接收 tokenID（文件名）和新的 token 数据
func (m *RefreshManager) SetOnTokenRefreshed(callback func(tokenID string, tokenData *KiroTokenData)) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.onTokenRefreshed = callback

	// 如果 refresher 已经创建，使用并发安全的方式更新它的回调
	if m.refresher != nil {
		m.refresher.callbackMu.Lock()
		m.refresher.onTokenRefreshed = callback
		m.refresher.callbackMu.Unlock()
	}

	log.Debug("refresh manager: token refresh callback registered")
}

// InitializeAndStart 初始化并启动后台刷新（便捷方法）
func InitializeAndStart(baseDir string, cfg *config.Config) {
	manager := GetRefreshManager()
	if err := manager.Initialize(baseDir, cfg); err != nil {
		log.Errorf("refresh manager: initialization failed: %v", err)
		return
	}
	manager.Start()
}

// StopGlobalRefreshManager 停止全局刷新管理器
func StopGlobalRefreshManager() {
	if globalRefreshManager != nil {
		globalRefreshManager.Stop()
	}
}
