package helps

import (
	"bufio"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

//go:embed xiaomi_browser.py
var xiaomiBrowserScript string

// browserEvent 是 Python 子进程通过 stdout 发送的 JSON 行。
type browserEvent struct {
	Type      string `json:"type"`
	SessionID string `json:"session_id,omitempty"`
	Platform  string `json:"platform,omitempty"`
	All       string `json:"all,omitempty"`
	Cookies   string `json:"cookies,omitempty"`
	Message   string `json:"message,omitempty"`
	Status    string `json:"status,omitempty"`
}

// BrowserLoginSession 管理一个正在运行的 xiaomi 浏览器登录子进程。
type BrowserLoginSession struct {
	SessionID string
	Email     string
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	cancel    context.CancelFunc
	done      chan struct{}
	eventCh   <-chan browserEvent // 供 WaitForBrowserLogin 消费剩余事件
	resultMu  sync.Mutex
	result    *browserLoginResult
}

type browserLoginResult struct {
	Cookies string
	Error   string
}

// BrowserVerificationRequired 当 Playwright 登录需要邮箱验证码时返回。
type BrowserVerificationRequired struct {
	SessionID string
	Email     string
	Message   string
}

func (e *BrowserVerificationRequired) Error() string {
	return fmt.Sprintf("需要邮箱验证码 (session: %s, email: %s): %s", e.SessionID, e.Email, e.Message)
}

var (
	activeSessions sync.Map // sessionID → *BrowserLoginSession
	pendingLogins  sync.Map // email → sessionID，防止同一邮箱重复启动浏览器登录
)

const (
	browserSessionTimeout = 10 * time.Minute
)

// StartXiaomiBrowserLogin 启动 Python Playwright 子进程进行登录。
// 返回 (sessionID, eventCh, error)；sessionID 用于 SubmitVerificationCode / WaitForBrowserLogin。
func StartXiaomiBrowserLogin(email, password string) (string, <-chan browserEvent, error) {
	scriptPath := findXiaomiBrowserScript()
	if _, err := os.Stat(scriptPath); err != nil {
		return "", nil, fmt.Errorf("xiaomi browser script not found at %s: %w", scriptPath, err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), browserSessionTimeout)
	cmd := exec.CommandContext(ctx, "python3", scriptPath)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return "", nil, fmt.Errorf("stdin pipe: %w", err)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stdin.Close()
		cancel()
		return "", nil, fmt.Errorf("stdout pipe: %w", err)
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		stdin.Close()
		cancel()
		return "", nil, fmt.Errorf("stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		stdin.Close()
		cancel()
		return "", nil, fmt.Errorf("start python: %w", err)
	}

	// 后台记录 stderr
	go func() {
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			line := scanner.Text()
			log.Infof("xiaomi browser: %s", line)
		}
	}()

	// 发送启动命令
	startup := map[string]string{
		"action":   "login",
		"email":    email,
		"password": password,
	}
	startupBytes, _ := json.Marshal(startup)
	if _, err := fmt.Fprintf(stdin, "%s\n", startupBytes); err != nil {
		cmd.Process.Kill()
		stdin.Close()
		cancel()
		return "", nil, fmt.Errorf("send startup: %w", err)
	}

	sessionID := fmt.Sprintf("xiaomi_%d", time.Now().UnixNano())
	ch := make(chan browserEvent, 16)
	sess := &BrowserLoginSession{
		SessionID: sessionID,
		Email:     email,
		cmd:       cmd,
		stdin:     stdin,
		cancel:    cancel,
		done:      make(chan struct{}),
		eventCh:   ch,
	}

	// goroutine 读取 stdout 事件
	go func() {
		defer close(sess.done)
		defer close(ch)
		defer stdin.Close()
		defer pendingLogins.Delete(email)

		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}
			var evt browserEvent
			if err := json.Unmarshal([]byte(line), &evt); err != nil {
				log.Debugf("xiaomi browser: 跳过非 JSON 行: %s", line[:min(80, len(line))])
				continue
			}
			ch <- evt

			switch evt.Type {
			case "cookies":
				sess.resultMu.Lock()
				// 优先使用 platform cookies，退而使用 all cookies
				cookies := evt.Platform
				if cookies == "" {
					cookies = evt.All
				}
				// 也处理 cookies 字段（兼容旧版本）
				if cookies == "" {
					cookies = evt.Cookies
				}
				sess.result = &browserLoginResult{Cookies: cookies}
				sess.resultMu.Unlock()

			case "error":
				sess.resultMu.Lock()
				sess.result = &browserLoginResult{Error: evt.Message}
				sess.resultMu.Unlock()

			case "done":
				return
			}
		}
		if err := scanner.Err(); err != nil {
			log.Errorf("xiaomi browser: stdout 读取错误: %v", err)
		}
	}()

	activeSessions.Store(sessionID, sess)

	return sessionID, ch, nil
}

// SubmitVerificationCode 向运行中的浏览器登录 session 提交验证码。
func SubmitVerificationCode(sessionID, code string) error {
	val, ok := activeSessions.Load(sessionID)
	if !ok {
		return fmt.Errorf("session %s 未找到或已过期", sessionID)
	}
	sess := val.(*BrowserLoginSession)

	select {
	case <-sess.done:
		return fmt.Errorf("session %s 已结束", sessionID)
	default:
	}

	verifyCmd := map[string]string{
		"action": "verify",
		"code":   code,
	}
	verifyBytes, _ := json.Marshal(verifyCmd)
	if _, err := fmt.Fprintf(sess.stdin, "%s\n", verifyBytes); err != nil {
		return fmt.Errorf("发送验证码: %w", err)
	}
	return nil
}

// WaitForBrowserLogin 等待 session 完成并返回 cookies。
// 如果 performBrowserLoginWithEmail 已经返回（need_verification），goroutine
// 可能阻塞在 ch <- evt 上。此函数主动消费 eventCh 中的剩余事件以解除阻塞。
// 成功后自动缓存 cookies（全局 + per-account），调用方无需再手动缓存。
func WaitForBrowserLogin(sessionID string) (string, error) {
	val, ok := activeSessions.Load(sessionID)
	if !ok {
		return "", fmt.Errorf("session %s 未找到", sessionID)
	}
	sess := val.(*BrowserLoginSession)

	// 先检查是否已有结果（正常路径：cookies 事件在 need_verification 之前到达）
	sess.resultMu.Lock()
	if sess.result != nil {
		result := sess.result
		sess.resultMu.Unlock()
		if result.Error != "" {
			return "", fmt.Errorf("浏览器登录失败: %s", result.Error)
		}
		if result.Cookies == "" {
			return "", fmt.Errorf("浏览器登录未获取到 cookies")
		}
		cacheBrowserLoginCookies(sess.Email, result.Cookies)
		return result.Cookies, nil
	}
	sess.resultMu.Unlock()

	// 没有结果，主动消费 eventCh 直到 done 或拿到 cookies/error
	if sess.eventCh != nil {
		for evt := range sess.eventCh {
			switch evt.Type {
			case "cookies":
				cookies := evt.Platform
				if cookies == "" {
					cookies = evt.All
				}
				if cookies == "" {
					cookies = evt.Cookies
				}
				sess.resultMu.Lock()
				sess.result = &browserLoginResult{Cookies: cookies}
				sess.resultMu.Unlock()
			case "error":
				sess.resultMu.Lock()
				sess.result = &browserLoginResult{Error: evt.Message}
				sess.resultMu.Unlock()
			case "done":
				// goroutine 退出，close(done) 会被触发
			}
		}
	}

	// 等待 goroutine 完全退出
	<-sess.done

	sess.resultMu.Lock()
	defer sess.resultMu.Unlock()

	if sess.result == nil {
		return "", fmt.Errorf("浏览器登录未产生结果")
	}
	if sess.result.Error != "" {
		return "", fmt.Errorf("浏览器登录失败: %s", sess.result.Error)
	}
	if sess.result.Cookies == "" {
		return "", fmt.Errorf("浏览器登录未获取到 cookies")
	}
	cacheBrowserLoginCookies(sess.Email, sess.result.Cookies)
	return sess.result.Cookies, nil
}

// cacheBrowserLoginCookies 将浏览器登录获取的 cookies 写入缓存和持久化文件。
func cacheBrowserLoginCookies(email, cookies string) {
	SetXiaomiPlatformCookies(cookies, xiaomiCookieFileTTL)
	if email != "" {
		SetXiaomiAccountCookies(email, cookies, xiaomiCookieFileTTL)
		if err := SaveXiaomiCookiesToFile(email, cookies); err != nil {
			log.Warnf("xiaomi: 持久化 per-key cookies 失败: %v", err)
		}
	} else {
		if err := SaveXiaomiCookiesToFile("", cookies); err != nil {
			log.Warnf("xiaomi: 持久化 global cookies 失败: %v", err)
		}
	}
}

// CleanupBrowserSession 清理指定 session（进程终止+资源回收）。
func CleanupBrowserSession(sessionID string) {
	val, ok := activeSessions.LoadAndDelete(sessionID)
	if !ok {
		return
	}
	sess := val.(*BrowserLoginSession)
	sess.cancel()
	select {
	case <-sess.done:
	default:
	}
}

// findXiaomiBrowserScript 返回 Python 脚本的路径。
// 优先使用外部文件，如果不存在则从嵌入的脚本创建临时文件。
func findXiaomiBrowserScript() string {
	// 1. 检查环境变量
	if envPath := os.Getenv("XIAOMI_BROWSER_SCRIPT"); envPath != "" {
		if _, err := os.Stat(envPath); err == nil {
			return envPath
		}
	}

	// 2. 检查外部文件
	externalPaths := []string{
		"xiaomi_browser.py",
		"scripts/xiaomi_browser.py",
		"internal/runtime/executor/helps/xiaomi_browser.py",
	}
	cwd, _ := os.Getwd()
	for _, relPath := range externalPaths {
		candidate := filepath.Join(cwd, relPath)
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}

	// 3. 从嵌入的脚本创建临时文件
	tmpFile := filepath.Join(os.TempDir(), "xiaomi_browser_login.py")
	if err := os.WriteFile(tmpFile, []byte(xiaomiBrowserScript), 0755); err != nil {
		log.Warnf("xiaomi: 无法创建临时脚本: %v", err)
		return tmpFile
	}
	log.Infof("xiaomi: 从嵌入脚本创建临时文件: %s", tmpFile)
	return tmpFile
}

func init() {
	go sessionCleanupLoop()
}

func sessionCleanupLoop() {
	for {
		time.Sleep(1 * time.Minute)
		activeSessions.Range(func(key, value interface{}) bool {
			sess := value.(*BrowserLoginSession)
			select {
			case <-sess.done:
				activeSessions.Delete(key)
			default:
			}
			return true
		})
	}
}
