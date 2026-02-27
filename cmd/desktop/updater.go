package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"

	log "github.com/sirupsen/logrus"
	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// Windows process creation flags (not exposed by Go's syscall package)
const (
	_CREATE_NEW_CONSOLE         = 0x00000010
	_CREATE_NEW_PROCESS_GROUP   = 0x00000200
	_CREATE_BREAKAWAY_FROM_JOB  = 0x01000000
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

// updaterLog 同时输出到 logrus 和诊断日志文件（GUI 应用无控制台，日志文件便于排查）
func updaterLog(format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	log.Info(msg)
	logPath := filepath.Join(os.TempDir(), "cliproxyapi_updater.log")
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "[%s] %s\n", time.Now().Format("2006-01-02 15:04:05"), msg)
}

// checkForUpdates 检查后端仓库是否有更新，有则弹窗询问用户
// 注意：前端 management.html 由后端 managementasset 包自动从 GitHub Releases 下载，无需单独检查
func (a *App) checkForUpdates() {
	root := findProjectRoot()
	if root == "" {
		updaterLog("updater: project root not found, skip update check")
		return
	}

	// 只需 git 即可检查更新；go/wails 由 upgrade-desktop.ps1 自行查找
	if _, err := exec.LookPath("git"); err != nil {
		updaterLog("updater: git not found: %v, skip update check", err)
		return
	}

	updaterLog("updater: checking for updates (root=%s, proxy=%q)", root, a.proxyURL)

	if _, err := gitShort(root, a.proxyURL, "fetch", "origin", "main"); err != nil {
		updaterLog("updater: fetch failed: %v", err)
		return
	}

	counts, err := gitShort(root, a.proxyURL, "rev-list", "--left-right", "--count", "HEAD...origin/main")
	if err != nil {
		updaterLog("updater: rev-list failed: %v", err)
		return
	}
	parts := strings.Fields(counts)
	if len(parts) != 2 {
		updaterLog("updater: unexpected rev-list output: %q", counts)
		return
	}
	ahead, err := strconv.Atoi(parts[0])
	if err != nil {
		return
	}
	behind, err := strconv.Atoi(parts[1])
	if err != nil {
		return
	}

	if behind <= 0 {
		updaterLog("updater: already up to date (ahead=%d, behind=%d)", ahead, behind)
		return
	}

	// 获取版本摘要用于弹窗展示
	local, _ := gitShort(root, a.proxyURL, "rev-parse", "--short", "HEAD")
	remote, _ := gitShort(root, a.proxyURL, "rev-parse", "--short", "origin/main")
	changelog, _ := gitShort(root, "", "log", "--oneline", "--no-decorate", local+".."+remote)

	detail := fmt.Sprintf("后端: %s → %s", local, remote)
	msg := "A desktop source update is available.\n\n" + detail
	if changelog != "" {
		msg += "\n\nChangelog:\n" + changelog
	}
	msg += "\n\nUpgrade now?"

	updaterLog("updater: update available: %s (%d behind)", detail, behind)

	result, err := wailsRuntime.MessageDialog(a.ctx, wailsRuntime.MessageDialogOptions{
		Type:    wailsRuntime.QuestionDialog,
		Title:   "CLIProxyAPI Update",
		Message: msg,
	})
	if err != nil || result != "Yes" {
		updaterLog("updater: user skipped update (result=%q)", result)
		return
	}

	updaterLog("updater: user confirmed upgrade, starting...")
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
	flags := uint32(_CREATE_BREAKAWAY_FROM_JOB | _CREATE_NEW_PROCESS_GROUP | _CREATE_NEW_CONSOLE)
	err = syscall.CreateProcess(nil, cmdLinePtr, nil, nil, false, flags, nil, workDirPtr, &si, &pi)
	if err != nil {
		// 降级：不带 BREAKAWAY
		log.Warnf("updater: CreateProcess with BREAKAWAY failed: %v, retrying without", err)
		flags = uint32(_CREATE_NEW_PROCESS_GROUP | _CREATE_NEW_CONSOLE)
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
	lockPath := filepath.Join(os.TempDir(), "cliproxyapi_upgrade.lock")
	batContent := fmt.Sprintf("@echo off\r\nchcp 65001 >nul\r\ntitle CLIProxyAPI Upgrade\r\necho === CLIProxyAPI Upgrade ===\r\necho started > \"%s\"\r\ncd /d \"%s\"\r\npowershell -ExecutionPolicy Bypass -File \"%s\" -AutoLaunch \"%s\"\r\nif %%errorlevel%% neq 0 pause\r\ndel /f \"%s\" 2>nul\r\n", lockPath, root, upgradeScript, newExe, lockPath)
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

	// 等待升级脚本启动（通过 lock 文件确认，超时 10 秒）
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(lockPath); err == nil {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}

	// 退出当前进程
	wailsRuntime.Quit(a.ctx)
}
