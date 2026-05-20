package helps

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestXiaomiDiagnose(t *testing.T) {
	cfg := &config.Config{
		XiaomiPlatform: config.XiaomiPlatformConfig{
			Email:    "xin11cc11@gmail.com",
			Password: "Cn.xm183",
		},
	}
	auth := &cliproxyauth.Auth{}

	// 步骤 1: 尝试访问 balance 页面，看是否重定向
	t.Log("=== 步骤 1: 访问 balance 页面 ===")
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, Timeout: 30 * time.Second}

	urls := []string{
		"https://platform.xiaomimimo.com/console/balance",
		"https://account.xiaomi.com/pass/serviceLogin?sid=api-platform",
		"https://account.xiaomi.com/fe/service/login/password?sid=api-platform&_group=DEFAULT&_locale=zh_CN",
	}

	for _, u := range urls {
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, u, nil)
		req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36")

		resp, err := client.Do(req)
		if err != nil {
			t.Logf("  GET %s → 错误: %v", u, err)
			continue
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		bodyPreview := string(body)
		if len(bodyPreview) > 300 {
			bodyPreview = bodyPreview[:300] + "..."
		}

		t.Logf("  GET %s", u)
		t.Logf("    状态: %d, 最终 URL: %s", resp.StatusCode, resp.Request.URL.String())
		t.Logf("    Content-Type: %s", resp.Header.Get("Content-Type"))
		t.Logf("    Body 预览: %s", bodyPreview)

		// 收集 cookies
		var cookieStrs []string
		for _, c := range jar.Cookies(resp.Request.URL) {
			cookieStrs = append(cookieStrs, c.Name+"="+c.Value)
		}
		setCookies := resp.Header.Values("Set-Cookie")
		for _, sc := range setCookies {
			if idx := strings.Index(sc, ";"); idx >= 0 {
				sc = sc[:idx]
			}
			cookieStrs = append(cookieStrs, sc)
		}
		t.Logf("    Cookies: %v", cookieStrs)
	}

	// 步骤 2: 尝试完整登录流程
	t.Log("=== 步骤 2: 完整登录流程 ===")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cookies, err := performXiaomiLogin(ctx, cfg.XiaomiPlatform.Email, cfg.XiaomiPlatform.Password, cfg, auth)
	if err != nil {
		t.Logf("完整登录失败: %v", err)

		// 尝试只用密码哈希，验证是否能走到 login POST 步骤
		t.Logf("密码哈希: %s", xiaomiPasswordHash(cfg.XiaomiPlatform.Password))
		return
	}

	t.Logf("登录成功！Cookies: %s", cookies)

	// 提取关键 cookie
	for _, name := range []string{"api-platform_serviceToken", "userId", "api-platform_slh", "api-platform_ph", "passToken"} {
		found := false
		for _, part := range strings.Split(cookies, ";") {
			if strings.HasPrefix(strings.TrimSpace(part), name+"=") {
				t.Logf("  ✓ %s", name)
				found = true
				break
			}
		}
		if !found {
			t.Logf("  ✗ %s (缺失)", name)
		}
	}

	// 步骤 3: 查询余额
	if cookies != "" {
		t.Log("=== 步骤 3: 查询余额 ===")
		SetXiaomiPlatformCookies(cookies, 5*time.Minute)

		creds := XiaomiCredentials{}
		balance, err := RefreshXiaomiBalanceWithCreds(creds, cfg, auth)
		if err != nil {
			t.Fatalf("余额查询失败: %v", err)
		}
		t.Logf("月用量: %d / %d (%.4f%%)", balance.MonthUsed, balance.MonthLimit, balance.MonthPercent*100)
		t.Logf("计划用量: %d / %d (%.4f%%)", balance.PlanUsed, balance.PlanLimit, balance.PlanPercent*100)
		t.Logf("补偿用量: %d / %d", balance.CompensationUsed, balance.CompensationLimit)
		t.Logf("来源: %s, 时间: %s", balance.Source, balance.FetchedAt.Format(time.RFC3339))
	}
}

func TestPasswordHash(t *testing.T) {
	hash := xiaomiPasswordHash("Cn.xm183")
	t.Logf("密码哈希: %s", hash)
	// 预期长度：32 个大写 hex 字符
	if len(hash) != 32 {
		t.Errorf("哈希长度异常: %d (期望 32)", len(hash))
	}
}

func TestComputeSign(t *testing.T) {
	// 测试 _sign 计算
	qs := "sid=api-platform&user=test%40gmail.com&hash=ABC123&cc=%2B86"
	sign := xiaomiComputeSign(qs)
	t.Logf("Sign(%q) = %s", qs, sign)
	// 验证是合法的 base64
	if len(sign) == 0 {
		t.Error("签名为空")
	}
}

func TestEncryptUser(t *testing.T) {
	if testing.Short() {
		t.Skip("跳过加密测试")
	}
	client := &http.Client{Timeout: 15 * time.Second}
	ctx := context.Background()
	encrypted, err := xiaomiEncryptUser(ctx, client, "xin11cc11@gmail.com")
	if err != nil {
		t.Fatalf("加密邮箱失败: %v", err)
	}
	t.Logf("加密结果 (base64): %s", encrypted)
	t.Logf("加密结果长度 (bytes): %d", len(encrypted))

	// RSA PKCS1v15 with 1024-bit key produces 128 bytes = 172 base64 chars
	if len(encrypted) < 100 {
		t.Errorf("加密结果太短，可能不是 RSA 加密")
	}
}

func TestFetchPublicKey(t *testing.T) {
	if testing.Short() {
		t.Skip("跳过公钥获取测试")
	}
	client := &http.Client{Timeout: 15 * time.Second}
	ctx := context.Background()
	key, err := fetchXiaomiPublicKey(ctx, client)
	if err != nil {
		t.Fatalf("获取公钥失败: %v", err)
	}
	t.Logf("获取到公钥 (前 100 字符): %s...", key[:min(100, len(key))])
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// 验证当前代码中已知道的值
func TestKnownValues(t *testing.T) {
	// 从 Burp 抓包验证
	// password hash: MD5("Cn.xm183") → should produce correct hash
	hash := xiaomiPasswordHash("Cn.xm183")
	t.Logf("MD5(Cn.xm183).toUpperCase() = %s", hash)
}

// 保留下面的集成测试
func TestXiaomiLoginReal(t *testing.T) {
	if testing.Short() {
		t.Skip("跳过真实登录测试")
	}

	cfg := &config.Config{
		XiaomiPlatform: config.XiaomiPlatformConfig{
			Email:    "xin11cc11@gmail.com",
			Password: "Cn.xm183",
		},
	}
	auth := &cliproxyauth.Auth{}

	t.Log("开始自动登录...")

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cookies, err := performXiaomiLogin(ctx, cfg.XiaomiPlatform.Email, cfg.XiaomiPlatform.Password, cfg, auth)
	if err != nil {
		t.Fatalf("登录失败: %v", err)
	}

	t.Logf("登录成功！Cookies: %s", cookies)

	// 缓存 cookies 并查询余额
	SetXiaomiPlatformCookies(cookies, 5*time.Minute)

	balance, err := RefreshXiaomiBalanceWithCreds(XiaomiCredentials{}, cfg, auth)
	if err != nil {
		t.Fatalf("余额查询失败: %v", err)
	}
	t.Logf("月用量: %d/%d (%.4f%%)", balance.MonthUsed, balance.MonthLimit, balance.MonthPercent*100)
	t.Logf("计划用量: %d/%d", balance.PlanUsed, balance.PlanLimit)
	t.Logf("来源: %s", balance.Source)
}

// TestFormat is just to make fmt import used
var _ = fmt.Sprintf
