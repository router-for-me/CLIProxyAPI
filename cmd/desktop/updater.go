package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"

	log "github.com/sirupsen/logrus"
	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// findProjectRoot 从 exe 路径向上查找包含 .git 的项目根目录
func findProjectRoot() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	dir, err := filepath.EvalSymlinks(exe)
	if err != nil {
		dir = exe
	}
	dir = filepath.Dir(dir)
	for i := 0; i < 6; i++ {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return ""
}

// gitShort 在指定目录执行 git 命令，带超时和可选代理
func gitShort(dir string, proxyURL string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	// 隐藏 Windows 下的控制台窗口
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	// 通过环境变量传入代理
	if proxyURL != "" {
		cmd.Env = append(os.Environ(),
			"http_proxy="+proxyURL,
			"https_proxy="+proxyURL,
		)
	}
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// checkForUpdates 检查后端仓库是否有更新，有则弹窗询问用户
// 注意：前端 management.html 由后端 managementasset 包自动从 GitHub Releases 下载，无需单独检查
func (a *App) checkForUpdates() {
	root := findProjectRoot()
	if root == "" {
		log.Debug("updater: project root not found, skip update check")
		return
	}

	log.Info("updater: checking for updates...")

	proxy := a.proxyURL

	// 仅检查后端仓库
	if _, err := os.Stat(filepath.Join(root, ".git")); err != nil {
		return
	}
	if _, err := gitShort(root, proxy, "fetch", "origin", "main"); err != nil {
		log.Debugf("updater: fetch failed: %v", err)
		return
	}
	local, err := gitShort(root, proxy, "rev-parse", "--short", "HEAD")
	if err != nil {
		return
	}
	remote, err := gitShort(root, proxy, "rev-parse", "--short", "origin/main")
	if err != nil {
		return
	}
	if local == remote {
		log.Info("updater: already up to date")
		return
	}

	// 获取更新日志
	changelog, _ := gitShort(root, "", "log", "--oneline", "--no-decorate", local+".."+remote)

	detail := fmt.Sprintf("后端: %s → %s", local, remote)
	msg := "A new desktop update is available.\n\n" + detail
	if changelog != "" {
		msg += "\n\nChangelog:\n" + changelog
	}
	msg += "\n\nUpgrade now?"

	log.Infof("updater: update available: %s", detail)

	const (
		upgradeNow = "Upgrade Now"
		skipUpdate = "Skip"
	)

	result, err := wailsRuntime.MessageDialog(a.ctx, wailsRuntime.MessageDialogOptions{
		Type:          wailsRuntime.QuestionDialog,
		Title:         "CLIProxyAPI Update",
		Message:       msg,
		Buttons:       []string{upgradeNow, skipUpdate},
		DefaultButton: upgradeNow,
		CancelButton:  skipUpdate,
	})
	if err != nil || result != upgradeNow {
		log.Infof("updater: user skipped update (result=%q)", result)
		return
	}

	a.doUpgradeAndRestart(root)
}

// createDetachedProcess 使用 syscall.CreateProcess 创建完全独立的进程
// 关键：不设置 STARTF_USESTDHANDLES，让 CREATE_NEW_CONSOLE 的新控制台正常显示输出
// exec.Command 会自动把 stdout/stderr 重定向到 NUL（GUI 程序无控制台），导致新窗口无输出
func createDetachedProcess(cmdLine string, workDir string) error {
	cmdLinePtr, err := syscall.UTF16PtrFromString(cmdLine)
	if err != nil {
		return err
	}

	var workDirPtr *uint16
	if workDir != "" {
		workDirPtr, err = syscall.UTF16PtrFromString(workDir)
		if err != nil {
			return err
		}
	}

	var si syscall.StartupInfo
	si.Cb = uint32(unsafe.Sizeof(si))
	// 不设置 si.Flags = STARTF_USESTDHANDLES
	// 这样 CREATE_NEW_CONSOLE 会正确分配自己的屏幕缓冲区

	var pi syscall.ProcessInformation

	// 先尝试 BREAKAWAY（脱离 Wails 的 Job Object）
	flags := uint32(0x01000210) // CREATE_BREAKAWAY_FROM_JOB | CREATE_NEW_PROCESS_GROUP | CREATE_NEW_CONSOLE
	err = syscall.CreateProcess(nil, cmdLinePtr, nil, nil, false, flags, nil, workDirPtr, &si, &pi)
	if err != nil {
		// 降级：不带 BREAKAWAY
		log.Warnf("updater: CreateProcess with BREAKAWAY failed: %v, retrying without", err)
		flags = 0x00000210 // CREATE_NEW_PROCESS_GROUP | CREATE_NEW_CONSOLE
		err = syscall.CreateProcess(nil, cmdLinePtr, nil, nil, false, flags, nil, workDirPtr, &si, &pi)
	}
	if err != nil {
		return fmt.Errorf("CreateProcess failed: %w", err)
	}

	// 关闭句柄，彻底脱离父子关系
	syscall.CloseHandle(pi.Thread)
	syscall.CloseHandle(pi.Process)
	return nil
}

// doUpgradeAndRestart 执行升级脚本，然后启动新 exe 并退出当前进程
func (a *App) doUpgradeAndRestart(root string) {
	upgradeScript := filepath.Join(root, "upgrade-desktop.ps1")
	if _, err := os.Stat(upgradeScript); err != nil {
		wailsRuntime.MessageDialog(a.ctx, wailsRuntime.MessageDialogOptions{
			Type:    wailsRuntime.ErrorDialog,
			Title:   "升级失败",
			Message: "未找到升级脚本: " + upgradeScript,
		})
		return
	}

	newExe := filepath.Join(root, "cmd", "desktop", "build", "bin", "CLIProxyAPI.exe")

	// 创建 .cmd 启动器
	batContent := fmt.Sprintf("@echo off\r\nchcp 65001 >nul\r\ntitle CLIProxyAPI Upgrade\r\necho === CLIProxyAPI Upgrade ===\r\ncd /d \"%s\"\r\npowershell -ExecutionPolicy Bypass -File \"%s\" -AutoLaunch \"%s\"\r\nif %%errorlevel%% neq 0 pause\r\n", root, upgradeScript, newExe)
	batPath := filepath.Join(os.TempDir(), "cliproxyapi_upgrade.cmd")
	if err := os.WriteFile(batPath, []byte(batContent), 0644); err != nil {
		wailsRuntime.MessageDialog(a.ctx, wailsRuntime.MessageDialogOptions{
			Type:    wailsRuntime.ErrorDialog,
			Title:   "升级失败",
			Message: "创建启动脚本失败: " + err.Error(),
		})
		return
	}

	// 先关闭后端服务，释放端口和资源
	a.shutdown(a.ctx)

	// 用 syscall.CreateProcess 启动，不设 STARTF_USESTDHANDLES，新控制台输出正常
	cmdLine := fmt.Sprintf(`cmd /c "%s"`, batPath)
	if err := createDetachedProcess(cmdLine, root); err != nil {
		wailsRuntime.MessageDialog(a.ctx, wailsRuntime.MessageDialogOptions{
			Type:    wailsRuntime.ErrorDialog,
			Title:   "升级失败",
			Message: "启动升级脚本失败: " + err.Error(),
		})
		return
	}

	// 等待确保子进程已启动
	time.Sleep(2 * time.Second)

	// 退出当前进程
	wailsRuntime.Quit(a.ctx)
}
