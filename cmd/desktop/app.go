package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	configaccess "github.com/router-for-me/CLIProxyAPI/v6/internal/access/config_access"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/cmd"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/managementasset"
	_ "github.com/router-for-me/CLIProxyAPI/v6/internal/translator"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/usage"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v6/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// App 桌面端应用主结构
type App struct {
	ctx          context.Context
	serverCancel func()
	serverDone   <-chan struct{}
	configPath   string
	proxyURL     string
	serverPort   int
}

// NewApp 创建应用实例
func NewApp() *App {
	return &App{}
}

// getConfigDir 返回桌面端配置目录（%APPDATA%/CLIProxyAPI-Desktop）
func getConfigDir() string {
	appData := os.Getenv("APPDATA")
	if appData == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			log.Fatalf("could not determine user home directory: %v", err)
		}
		appData = filepath.Join(home, "AppData", "Roaming")
	}
	return filepath.Join(appData, "CLIProxyAPI-Desktop")
}

// ensureConfigFile 确保配置文件存在，不存在则创建默认配置
func ensureConfigFile(configPath string) error {
	dir := filepath.Dir(configPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("创建配置目录失败: %w", err)
	}

	if _, err := os.Stat(configPath); err == nil {
		return nil // 配置文件已存在
	}

	defaultConfig := `host: "127.0.0.1"
port: 8317
auth-dir: "~/.cli-proxy-api"
api-keys:
  - "123"
remote-management:
  allow-remote: false
  secret-key: ""
  disable-control-panel: false
proxy-url: ""
request-retry: 3
usage-statistics-enabled: true
`
	return os.WriteFile(configPath, []byte(defaultConfig), 0644)
}

// startup Wails 生命周期：应用启动时调用
// 初始化流程与官方 cmd/server/main.go 保持一致
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx

	configDir := getConfigDir()
	a.configPath = filepath.Join(configDir, "config.yaml")

	// 确保配置文件存在
	if err := ensureConfigFile(a.configPath); err != nil {
		log.Errorf("配置文件初始化失败: %v", err)
	}

	// 加载配置
	cfg, err := config.LoadConfigOptional(a.configPath, false)
	if err != nil {
		log.Errorf("加载配置失败: %v", err)
		return
	}
	if cfg == nil {
		cfg = &config.Config{}
	}

	// 确保默认端口
	if cfg.Port == 0 {
		cfg.Port = 8317
	}

	// ── 以下初始化步骤与官方 cmd/server/main.go 保持一致 ──

	// 启用统计 & 配额冷却
	usage.SetStatisticsEnabled(cfg.UsageStatisticsEnabled)
	coreauth.SetQuotaCooldownDisabled(cfg.DisableCooling)

	// 配置日志输出
	if err := logging.ConfigureLogOutput(cfg); err != nil {
		log.Errorf("failed to configure log output: %v", err)
	}
	util.SetLogLevel(cfg)

	// 解析 auth-dir（展开 ~ 等路径）
	if resolvedAuthDir, err := util.ResolveAuthDir(cfg.AuthDir); err != nil {
		log.Errorf("failed to resolve auth directory: %v", err)
		return
	} else {
		cfg.AuthDir = resolvedAuthDir
	}

	// 设置 management 面板配置 & 启动自动更新
	managementasset.SetCurrentConfig(cfg)
	managementasset.StartAutoUpdater(context.Background(), a.configPath)

	// 注册文件 token 存储（桌面端使用本地文件存储）
	sdkAuth.RegisterTokenStore(sdkAuth.NewFileTokenStore())

	// 注册内置访问提供者
	configaccess.Register(&cfg.SDKConfig)

	// 后台启动代理服务
	cancel, done := cmd.StartServiceBackground(cfg, a.configPath, "")
	a.serverCancel = cancel
	a.serverDone = done
	a.proxyURL = cfg.ProxyURL
	a.serverPort = cfg.Port

	log.Infof("桌面端服务已启动，端口: %d", cfg.Port)

	// 异步检查更新
	go a.checkForUpdates()
}

// shutdown Wails 生命周期：应用关闭时调用
// 可能被调用多次（手动 + Wails OnShutdown），通过置 nil 保证只执行一次
func (a *App) shutdown(ctx context.Context) {
	cancel := a.serverCancel
	if cancel == nil {
		return
	}
	a.serverCancel = nil
	cancel()
	<-a.serverDone
	log.Info("代理服务已停止")
}

func (a *App) GetServerPort() int {
	if a.serverPort <= 0 {
		return 8317
	}
	return a.serverPort
}
