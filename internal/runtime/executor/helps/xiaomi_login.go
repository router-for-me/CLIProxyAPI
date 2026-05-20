package helps

import (
	"context"
	"crypto/md5"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

const (
	xiaomiLoginTimeout       = 30 * time.Second
	xiaomiLoginPageURL       = "https://account.xiaomi.com/fe/service/login/password"
	xiaomiServiceLoginURL    = "https://account.xiaomi.com/pass/serviceLoginAuth2"
	xiaomiPlatformBalanceURL = "https://platform.xiaomimimo.com/console/balance"
	xiaomiPlatformStsURL     = "https://platform.xiaomimimo.com/sts"
	// xiaomiLoginServiceEntry is the SSO entry point that redirects to the
	// login page with all required security params (_sign, callback, qs, sid).
	xiaomiLoginServiceEntry = "https://account.xiaomi.com/pass/serviceLogin?sid=api-platform"
	// xiaomiPublicKeyURL is the RSA public key retrieval endpoint.
	xiaomiPublicKeyURL = "https://account.xiaomi.com/pass/publicKey"
)

// ErrVerificationRequired 表示 login 需要邮箱验证码，纯 Go HTTP 登录无法继续。
var ErrVerificationRequired = errors.New("email verification required")

// xiaomiLoginCache stores the latest login state for automatic cookie refresh.
var (
	xiaomiPlatformCookieMu  sync.RWMutex
	xiaomiPlatformCookies   string // latest platform cookies (api-platform_serviceToken, userId, api-platform_slh, api-platform_ph)
	xiaomiPlatformExpiresAt time.Time
)

// xiaomiPerAccountCookies stores per-account cookies keyed by email.
var (
	xiaomiPerAccountMu      sync.RWMutex
	xiaomiPerAccountCookies = make(map[string]*xiaomiAccountCookieEntry)
)

type xiaomiAccountCookieEntry struct {
	Cookies   string
	ExpiresAt time.Time
}

// GetXiaomiAccountCookies returns cached cookies for a specific account email.
func GetXiaomiAccountCookies(email string) string {
	xiaomiPerAccountMu.RLock()
	defer xiaomiPerAccountMu.RUnlock()
	entry, ok := xiaomiPerAccountCookies[email]
	if ok && time.Now().Before(entry.ExpiresAt) && entry.Cookies != "" {
		return entry.Cookies
	}
	return ""
}

// SetXiaomiAccountCookies stores cookies for a specific account email with a TTL.
func SetXiaomiAccountCookies(email, cookies string, ttl time.Duration) {
	xiaomiPerAccountMu.Lock()
	defer xiaomiPerAccountMu.Unlock()
	xiaomiPerAccountCookies[email] = &xiaomiAccountCookieEntry{
		Cookies:   cookies,
		ExpiresAt: time.Now().Add(ttl),
	}
}

// xiaomiLoginParams holds the parameters extracted from the login page redirect chain.
type xiaomiLoginParams struct {
	sid      string
	callback string
	qs       string
	sign     string
	groupId  string
}

// GetXiaomiPlatformCookies returns cached platform cookies if they are still valid.
func GetXiaomiPlatformCookies() string {
	xiaomiPlatformCookieMu.RLock()
	defer xiaomiPlatformCookieMu.RUnlock()
	if time.Now().Before(xiaomiPlatformExpiresAt) && xiaomiPlatformCookies != "" {
		return xiaomiPlatformCookies
	}
	return ""
}

// SetXiaomiPlatformCookies stores platform cookies with a TTL.
// When TTL is longer than 1 minute, cookies are asynchronously persisted to a JSON file.
func SetXiaomiPlatformCookies(cookies string, ttl time.Duration) {
	xiaomiPlatformCookieMu.Lock()
	defer xiaomiPlatformCookieMu.Unlock()
	xiaomiPlatformCookies = cookies
	xiaomiPlatformExpiresAt = time.Now().Add(ttl)
	if cookies != "" && ttl > time.Minute {
		go func() {
			if err := SaveXiaomiCookiesToFile("", cookies); err != nil {
				log.Warnf("xiaomi: 持久化 cookies 失败: %v", err)
			}
		}()
	}
}

// RefreshXiaomiCookiesFromConfig attempts to log into the Xiaomi platform using
// credentials from config.yaml and caches the resulting platform cookies.
// Returns an error if login fails; the caller should fall back to manually
// provided cookies or display the error to the user.
func RefreshXiaomiCookiesFromConfig(cfg *config.Config, auth *cliproxyauth.Auth) error {
	if cfg == nil {
		return fmt.Errorf("config is nil")
	}
	platform := cfg.XiaomiPlatform
	if !platform.Enabled() {
		return fmt.Errorf("xiaomi-platform email or password not configured")
	}

	return refreshXiaomiCookies(platform.Email, platform.Password, cfg, auth)
}

// RefreshXiaomiCookiesFromCreds attempts to log into the Xiaomi platform using
// the provided email/password credentials and caches the resulting cookies
// per-account. This supports multi-account configurations where each api-key
// entry has its own Xiaomi credentials.
func RefreshXiaomiCookiesFromCreds(email, password string, cfg *config.Config, auth *cliproxyauth.Auth) error {
	if strings.TrimSpace(email) == "" || strings.TrimSpace(password) == "" {
		return fmt.Errorf("email or password is empty")
	}

	return refreshXiaomiCookies(email, password, cfg, auth)
}

// refreshXiaomiCookies performs the actual login and caches cookies both globally
// and per-account.
func refreshXiaomiCookies(email, password string, cfg *config.Config, auth *cliproxyauth.Auth) error {
	log.Infof("xiaomi: refreshXiaomiCookies - 开始, email=%s", email)

	// 1. 检查该账号的持久化 cookie 文件
	if cookies, ok := loadXiaomiCookiesFromFile(email); ok && cookies != "" {
		SetXiaomiPlatformCookies(cookies, xiaomiCookieFileTTL)
		SetXiaomiAccountCookies(email, cookies, xiaomiCookieFileTTL)
		log.Info("xiaomi: 从持久化文件加载 cookies")
		return nil
	}

	// 2. 检查 per-account 缓存
	if cookies := GetXiaomiAccountCookies(email); cookies != "" {
		log.Info("xiaomi: 从 per-account 缓存加载 cookies")
		return nil
	}

	// 3. 直接使用浏览器登录（处理协议同意、验证码等复杂场景）
	log.Info("xiaomi: 使用浏览器登录...")
	return performBrowserLoginWithEmail(email, password)
}

// performBrowserLogin 启动 Playwright 浏览器登录。如果需要邮箱验证，返回 *BrowserVerificationRequired。
func performBrowserLogin(cfg *config.Config) error {
	if !cfg.XiaomiPlatform.Enabled() {
		return fmt.Errorf("xiaomi-platform 未配置")
	}

	goSessionID, eventCh, err := StartXiaomiBrowserLogin(cfg.XiaomiPlatform.Email, cfg.XiaomiPlatform.Password)
	if err != nil {
		return fmt.Errorf("启动浏览器登录: %w", err)
	}

	for evt := range eventCh {
		switch evt.Type {
		case "need_verification":
			return &BrowserVerificationRequired{
				SessionID: goSessionID,
					Email:     cfg.XiaomiPlatform.Email,
				Message:   "需要输入邮箱验证码完成登录",
			}
		case "cookies":
			cookies := evt.Platform
			if cookies == "" {
				cookies = evt.All
			}
			if cookies == "" {
				cookies = evt.Cookies
			}
			SetXiaomiPlatformCookies(cookies, xiaomiCookieFileTTL)
			return nil
		case "error":
			return fmt.Errorf("浏览器登录: %s", evt.Message)
		case "done":
			return nil
		}
	}
	return fmt.Errorf("浏览器登录进程意外终止")
}

// performBrowserLoginWithEmail launches browser login with explicit email/password.
func performBrowserLoginWithEmail(email, password string) error {
	// 防止同一邮箱重复启动浏览器登录（上次可能还在等待验证码）
	if existingID, ok := pendingLogins.Load(email); ok {
		sessID := existingID.(string)
		if _, ok := activeSessions.Load(sessID); ok {
			log.Infof("xiaomi: 邮箱 %s 已有进行中的浏览器登录 session=%s，复用", email, sessID)
			return &BrowserVerificationRequired{
				SessionID: sessID,
					Email:     email,
				Message:   "需要输入邮箱验证码完成登录",
			}
		}
		pendingLogins.Delete(email)
	}

	log.Infof("xiaomi: performBrowserLoginWithEmail - 启动浏览器登录, email=%s", email)

	goSessionID, eventCh, err := StartXiaomiBrowserLogin(email, password)
	if err != nil {
		return fmt.Errorf("启动浏览器登录: %w", err)
	}
	pendingLogins.Store(email, goSessionID)

	for evt := range eventCh {
		switch evt.Type {
		case "need_verification":
			// 使用 Go 侧生成的 sessionID（存储在 activeSessions 中），
			// 而非 Python 侧的 UUID，确保 SubmitVerificationCode 能找到 session。
			return &BrowserVerificationRequired{
				SessionID: goSessionID,
					Email:     email,
				Message:   "需要输入邮箱验证码完成登录",
			}
		case "cookies":
			cookies := evt.Platform
			if cookies == "" {
				cookies = evt.All
			}
			if cookies == "" {
				cookies = evt.Cookies
			}
			SetXiaomiPlatformCookies(cookies, xiaomiCookieFileTTL)
			SetXiaomiAccountCookies(email, cookies, xiaomiCookieFileTTL)
			if err := SaveXiaomiCookiesToFile(email, cookies); err != nil {
				log.Warnf("xiaomi: 持久化 per-key cookies 失败: %v", err)
			}
			return nil
		case "error":
			return fmt.Errorf("浏览器登录: %s", evt.Message)
		case "done":
			return nil
		}
	}
	return fmt.Errorf("浏览器登录进程意外终止")
}

// performXiaomiLogin executes the full Xiaomi login flow and returns the
// platform.xiaomimimo.com cookies required for API authentication.
func performXiaomiLogin(ctx context.Context, email, password string, cfg *config.Config, auth *cliproxyauth.Auth) (string, error) {
	log.Infof("xiaomi: 开始登录流程, email=%s", email)
	client := httpClientForXiaomiLogin(ctx, cfg, auth)

	// Step 1: Follow redirects from platform balance page to get login page URL.
	// The login page URL contains _sign, callback, qs, sid parameters needed for auth.
	log.Info("xiaomi: Step 1 - 获取登录参数...")
	params, cookies, deviceID, err := resolveXiaomiLoginParams(ctx, client)
	if err != nil {
		return "", fmt.Errorf("resolve login params: %w", err)
	}
	log.Infof("xiaomi: Step 1 完成 - sid=%s, deviceId=%s", params.sid, deviceID)

	// Step 2: Compute password hash (MD5 uppercase).
	log.Info("xiaomi: Step 2 - 计算密码哈希...")
	passwordHash := xiaomiPasswordHash(password)

	// Step 3: Attempt to encrypt email with RSA public key.
	log.Info("xiaomi: Step 3 - 加密邮箱地址...")
	encryptedUser, err := xiaomiEncryptUser(ctx, client, email)
	if err != nil {
		log.Warnf("xiaomi: RSA 加密失败，使用 base64 回退: %v", err)
		encryptedUser = base64.StdEncoding.EncodeToString([]byte(email))
	}
	log.Info("xiaomi: Step 3 完成")

	// Step 4: Build and submit login form.
	log.Info("xiaomi: Step 4 - 提交登录表单...")
	loginCookies, err := xiaomiServiceLogin(ctx, client, cookies, deviceID, params, encryptedUser, passwordHash)
	if err != nil {
		return "", fmt.Errorf("serviceLoginAuth2: %w", err)
	}
	log.Info("xiaomi: Step 4 完成 - 登录成功")

	// Step 5: Follow the SSO redirect chain to get platform cookies.
	log.Info("xiaomi: Step 5 - 交换平台 cookies...")
	platformCookies, err := xiaomiExchangePlatformCookies(ctx, client, loginCookies)
	if err != nil {
		return "", fmt.Errorf("exchange platform cookies: %w", err)
	}
	log.Info("xiaomi: Step 5 完成 - 获取平台 cookies 成功")

	return platformCookies, nil
}

// resolveXiaomiLoginParams starts from the Xiaomi SSO serviceLogin entry and follows
// HTTP redirects to reach the login page. It extracts security parameters
// (_sign, callback, qs, sid) from the final URL and collects session cookies.
//
// platform.xiaomimimo.com is a SPA that handles auth client-side and does NOT
// issue HTTP redirects, so we must enter through account.xiaomi.com directly.
func resolveXiaomiLoginParams(ctx context.Context, client *http.Client) (*xiaomiLoginParams, string, string, error) {
	log.Info("xiaomi: resolveXiaomiLoginParams - 创建 cookie jar...")
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, "", "", fmt.Errorf("create cookie jar: %w", err)
	}
	client.Jar = jar

	log.Infof("xiaomi: resolveXiaomiLoginParams - 请求 %s", xiaomiLoginServiceEntry)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, xiaomiLoginServiceEntry, nil)
	if err != nil {
		return nil, "", "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/146.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")

	log.Info("xiaomi: resolveXiaomiLoginParams - 发送 HTTP 请求...")
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", "", fmt.Errorf("fetch serviceLogin entry: %w", err)
	}
	resp.Body.Close()
	log.Infof("xiaomi: resolveXiaomiLoginParams - 请求完成, status=%d", resp.StatusCode)

	finalURL := resp.Request.URL.String()
	parsed, err := url.Parse(finalURL)
	if err != nil {
		return nil, "", "", fmt.Errorf("parse final URL: %w", err)
	}
	query := parsed.Query()

	params := &xiaomiLoginParams{
		sid:      query.Get("sid"),
		callback: query.Get("callback"),
		qs:       query.Get("qs"),
		sign:     query.Get("_sign"),
		groupId:  query.Get("_group"),
	}

	if params.sid == "" {
		return nil, "", "", fmt.Errorf("未能从登录页 URL 提取 sid: %s", finalURL)
	}

	// 收集 SSO 重定向链中设置的全部 cookie（不只是最终 URL 下的）
	accountCookies := collectAllJarCookies(jar)
	deviceID := extractDeviceID(accountCookies)

	log.Debugf("xiaomi login: sid=%s, deviceId=%s, cookies=%s", params.sid, deviceID, accountCookies)
	return params, accountCookies, deviceID, nil
}

// collectAllJarCookies iterates over all known cookie jar entries and formats them
// as a Cookie header value. This is needed because the SSO redirect chain sets
// cookies across multiple domains (account.xiaomi.com, api-platform.xiaomimimo.com).
func collectAllJarCookies(jar *cookiejar.Jar) string {
	// Access the internal cookie map via known URLs.
	// We collect cookies from account.xiaomi.com which is the SSO domain.
	accountURL, _ := url.Parse("https://account.xiaomi.com")
	platformURL, _ := url.Parse("https://platform.xiaomimimo.com")

	cookies := collectCookies(jar.Cookies(accountURL))
	cookies = mergeCookies(cookies, collectCookies(jar.Cookies(platformURL)))
	return cookies
}

// xiaomiPasswordHash computes the MD5 hash of the password and returns it as
// an uppercase hex string, matching Xiaomi's browser-side hashing.
func xiaomiPasswordHash(password string) string {
	h := md5.Sum([]byte(password))
	return strings.ToUpper(hex.EncodeToString(h[:]))
}

// xiaomiEncryptUser encrypts the email address using Xiaomi's RSA public key.
// It fetches the public key from the account server and performs PKCS1v15 encryption.
func xiaomiEncryptUser(ctx context.Context, client *http.Client, email string) (string, error) {
	publicKey, err := fetchXiaomiPublicKey(ctx, client)
	if err != nil {
		return "", err
	}

	encrypted, err := rsaEncryptPKCS1v15(publicKey, []byte(email))
	if err != nil {
		return "", fmt.Errorf("rsa encrypt: %w", err)
	}

	return base64.StdEncoding.EncodeToString(encrypted), nil
}

// fetchXiaomiPublicKey retrieves the RSA public key from Xiaomi's account server.
// It tries the dedicated publicKey endpoint first, then falls back to extracting
// it from the login page HTML.
func fetchXiaomiPublicKey(ctx context.Context, client *http.Client) (string, error) {
	// Try the dedicated publicKey endpoint first.
	log.Infof("xiaomi: fetchXiaomiPublicKey - 请求 %s", xiaomiPublicKeyURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, xiaomiPublicKeyURL, nil)
	if err != nil {
		return "", fmt.Errorf("create publicKey request: %w", err)
	}
	req.Header.Set("User-Agent", "cli-proxy-api/xiaomi-login")

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch publicKey: %w", err)
	}
	log.Infof("xiaomi: fetchXiaomiPublicKey - 请求完成, status=%d", resp.StatusCode)
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read publicKey response: %w", err)
	}

	// Try to find a PEM-encoded RSA public key in the response.
	key := extractPEMPublicKey(string(body))
	if key != "" {
		return key, nil
	}

	// Fallback: Try extracting from login page JavaScript.
	return extractPublicKeyFromLoginPage(ctx, client)
}

// extractPEMPublicKey searches for a PEM-encoded RSA public key in text.
func extractPEMPublicKey(body string) string {
	re := regexp.MustCompile(`-----BEGIN PUBLIC KEY-----[^-]*-----END PUBLIC KEY-----`)
	match := re.FindString(body)
	return strings.TrimSpace(match)
}

// extractPublicKeyFromLoginPage fetches the login page HTML and tries to find
// the RSA public key embedded in JavaScript.
func extractPublicKeyFromLoginPage(ctx context.Context, client *http.Client) (string, error) {
	loginURL := fmt.Sprintf("%s?_group=DEFAULT&sid=api-platform&_locale=zh_CN", xiaomiLoginPageURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, loginURL, nil)
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("User-Agent", "cli-proxy-api/xiaomi-login")
	req.Header.Set("Accept", "text/html")

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch login page: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read login page: %w", err)
	}

	html := string(body)
	// Try PEM first
	if key := extractPEMPublicKey(html); key != "" {
		return key, nil
	}

	// Search for patterns like: "publicKey":"..." or publicKey:"..." or publicKey = "..."
	patterns := []string{
		`"publicKey"\s*:\s*"([^"]+)"`,
		`publicKey\s*:\s*"([^"]+)"`,
		`publicKey\s*=\s*"([^"]+)"`,
		`PUBLIC_KEY\s*=\s*"([^"]+)"`,
	}
	for _, pattern := range patterns {
		re := regexp.MustCompile(pattern)
		if m := re.FindStringSubmatch(html); len(m) > 1 {
			return strings.TrimSpace(m[1]), nil
		}
	}

	return "", fmt.Errorf("public key not found in login page")
}

// rsaEncryptPKCS1v15 encrypts data using RSA PKCS1v15 with the given PEM public key.
func rsaEncryptPKCS1v15(pubKeyPEM string, data []byte) ([]byte, error) {
	block, _ := pem.Decode([]byte(pubKeyPEM))
	if block == nil {
		return nil, fmt.Errorf("failed to decode PEM public key")
	}

	var pub interface{}
	var err error
	if block.Type == "PUBLIC KEY" {
		pub, err = x509.ParsePKIXPublicKey(block.Bytes)
	} else if block.Type == "RSA PUBLIC KEY" {
		pub, err = x509.ParsePKCS1PublicKey(block.Bytes)
	} else {
		return nil, fmt.Errorf("unsupported PEM type: %s", block.Type)
	}
	if err != nil {
		return nil, fmt.Errorf("parse public key: %w", err)
	}

	rsaPub, ok := pub.(*rsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("public key is not RSA")
	}

	// Use crypto/rand for secure random
	encrypted, err := rsa.EncryptPKCS1v15(rand.Reader, rsaPub, data)
	if err != nil {
		return nil, fmt.Errorf("RSA encrypt: %w", err)
	}
	return encrypted, nil
}

// xiaomiServiceLogin submits the login form to serviceLoginAuth2 and returns
// the account.xiaomi.com cookies obtained after successful authentication.
func xiaomiServiceLogin(ctx context.Context, client *http.Client, currentCookies, deviceID string, params *xiaomiLoginParams, encryptedUser, passwordHash string) (string, error) {
	form := url.Values{}
	form.Set("sid", params.sid)
	form.Set("user", encryptedUser)
	form.Set("hash", passwordHash)
	form.Set("cc", "+86")
	form.Set("_json", "true")
	form.Set("policyName", "miaccount")
	form.Set("captCode", "")
	form.Set("bizDeviceType", "")
	form.Set("needTheme", "false")
	form.Set("theme", "")
	form.Set("showActiveX", "false")
	form.Set("serviceParam", `{"checkSafePhone":false,"checkSafeAddress":false,"lsrp_score":0.0}`)
	form.Set("deviceFingerprint", generateDeviceFingerprint())

	if params.callback != "" {
		form.Set("callback", params.callback)
	}
	if params.qs != "" {
		form.Set("qs", params.qs)
	}

	// Build query string for _sign computation
	// Sign is computed over the form data excluding _sign itself
	formStr := form.Encode()
	computedSign := xiaomiComputeSign(formStr)
	form.Set("_sign", computedSign)

	bodyStr := form.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, xiaomiServiceLoginURL, strings.NewReader(bodyStr))
	if err != nil {
		return "", fmt.Errorf("create login request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=UTF-8")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/146.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("Origin", "https://account.xiaomi.com")
	req.Header.Set("Referer", fmt.Sprintf("%s?_group=%s&sid=%s&_locale=zh_CN", xiaomiLoginPageURL, params.groupIdOrDefault(), params.sid))
	req.Header.Set("Cookie", currentCookies)

	log.Infof("xiaomi: xiaomiServiceLogin - 提交登录表单到 %s", xiaomiServiceLoginURL)
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("post login: %w", err)
	}
	defer resp.Body.Close()
	log.Infof("xiaomi: xiaomiServiceLogin - 请求完成, status=%d", resp.StatusCode)

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read login response: %w", err)
	}

	// Parse response (format: &&&START&&&{...})
	respStr := string(respBody)
	if idx := strings.Index(respStr, "START&&&"); idx >= 0 {
		respStr = respStr[idx+len("START&&&"):]
	}

	// Quick check for success
	if strings.Contains(respStr, `"code":0`) || strings.Contains(respStr, `"result":"ok"`) {
		// Collect cookies from response
		cookies := collectCookies(client.Jar.Cookies(req.URL))
		cookies = mergeCookies(cookies, extractSetCookies(resp.Header))
		return cookies, nil
	}

	log.Infof("xiaomi: 登录响应: %s", truncate(respStr, 500))

	// Check for verification needed (code:2 = email verification, code:70016 = captcha/device verification)
	if strings.Contains(respStr, `"code":2`) || strings.Contains(respStr, `"notificationUrl"`) ||
		strings.Contains(respStr, `"code":70016`) || strings.Contains(respStr, `"captchaUrl"`) {
		// Go HTTP login cannot handle captcha/email verification — caller should use browser login.
		log.Info("xiaomi: 检测到需要验证，回退到浏览器登录")
		return "", ErrVerificationRequired
	}

	return "", fmt.Errorf("login response: %s", truncate(respStr, 200))
}

// xiaomiFollowNotificationURL follows the SSO auth chain from the notification URL.
func xiaomiFollowNotificationURL(ctx context.Context, client *http.Client, cookies, notificationURL string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, notificationURL, nil)
	if err != nil {
		return "", fmt.Errorf("create notification request: %w", err)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36")
	req.Header.Set("Cookie", cookies)

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("follow notification URL: %w", err)
	}
	resp.Body.Close()

	// Collect all cookies from the redirect chain
	allCookies := mergeCookies(cookies, collectCookies(client.Jar.Cookies(resp.Request.URL)))
	allCookies = mergeCookies(allCookies, extractSetCookies(resp.Header))
	return allCookies, nil
}

// xiaomiExchangePlatformCookies follows the SSO redirect chain through
// account.xiaomi.com to platform.xiaomimimo.com, collecting platform cookies.
func xiaomiExchangePlatformCookies(ctx context.Context, client *http.Client, accountCookies string) (string, error) {
	// Follow: serviceLoginAuth2/end -> sts -> console/balance
	endURL := fmt.Sprintf("https://account.xiaomi.com/pass/serviceLoginAuth2/end?sid=api-platform")
	log.Infof("xiaomi: xiaomiExchangePlatformCookies - 请求 %s", endURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endURL, nil)
	if err != nil {
		return "", fmt.Errorf("create end request: %w", err)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36")
	req.Header.Set("Cookie", accountCookies)
	req.Header.Set("Referer", "https://account.xiaomi.com/")

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("get serviceLoginAuth2/end: %w", err)
	}
	resp.Body.Close()
	log.Infof("xiaomi: xiaomiExchangePlatformCookies - 请求完成, status=%d, url=%s", resp.StatusCode, resp.Request.URL)

	allCookies := mergeCookies(accountCookies, collectCookies(client.Jar.Cookies(resp.Request.URL)))
	allCookies = mergeCookies(allCookies, extractSetCookies(resp.Header))

	// Check if we got platform cookies already
	platformCookies := extractXiaomiPlatformCookies(allCookies)
	if platformCookies != "" {
		return platformCookies, nil
	}

	// If we have a passToken, try to directly access the STS endpoint
	if !strings.Contains(allCookies, "passToken=") {
		return "", fmt.Errorf("no passToken obtained from login flow; manual cookie extraction may be required")
	}

	// Try accessing the platform balance page directly with passToken cookies
	log.Infof("xiaomi: xiaomiExchangePlatformCookies - 请求平台 %s", xiaomiPlatformBalanceURL)
	stsReq, err := http.NewRequestWithContext(ctx, http.MethodGet, xiaomiPlatformBalanceURL, nil)
	if err != nil {
		return "", fmt.Errorf("create platform request: %w", err)
	}
	stsReq.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36")
	stsReq.Header.Set("Cookie", allCookies)

	stsResp, err := client.Do(stsReq)
	if err != nil {
		return "", fmt.Errorf("access platform: %w", err)
	}
	stsResp.Body.Close()
	log.Infof("xiaomi: xiaomiExchangePlatformCookies - 平台请求完成, status=%d", stsResp.StatusCode)

	allCookies = mergeCookies(allCookies, collectCookies(client.Jar.Cookies(stsResp.Request.URL)))
	allCookies = mergeCookies(allCookies, extractSetCookies(stsResp.Header))

	platformCookies = extractXiaomiPlatformCookies(allCookies)
	if platformCookies == "" {
		return "", fmt.Errorf("failed to obtain platform cookies; email verification may be required")
	}

	return platformCookies, nil
}

// xiaomiComputeSign computes the SHA-1 based _sign parameter for the login form.
func xiaomiComputeSign(queryString string) string {
	h := sha1.New()
	h.Write([]byte(queryString))
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}

// Helper functions

func (p *xiaomiLoginParams) groupIdOrDefault() string {
	if p.groupId == "" {
		return "DEFAULT"
	}
	return p.groupId
}

// collectCookies formats a slice of http.Cookie into a Cookie header value.
func collectCookies(cookies []*http.Cookie) string {
	parts := make([]string, 0, len(cookies))
	for _, c := range cookies {
		if c.Name != "" && c.Value != "" {
			parts = append(parts, c.Name+"="+c.Value)
		}
	}
	return strings.Join(parts, "; ")
}

// extractDeviceID extracts the deviceId value from a cookie string.
func extractDeviceID(cookies string) string {
	for _, pair := range strings.Split(cookies, ";") {
		pair = strings.TrimSpace(pair)
		if strings.HasPrefix(pair, "deviceId=") {
			return strings.TrimPrefix(pair, "deviceId=")
		}
	}
	// Generate a random device ID if none is found
	return generateRandomDeviceID()
}

// generateRandomDeviceID creates a random device ID similar to Xiaomi's format.
func generateRandomDeviceID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return "wb_" + hex.EncodeToString(b)
}

// generateDeviceFingerprint creates a random device fingerprint.
func generateDeviceFingerprint() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// extractSetCookies extracts Set-Cookie headers from an HTTP response and
// formats them as a Cookie header value.
func extractSetCookies(header http.Header) string {
	cookies := header.Values("Set-Cookie")
	parts := make([]string, 0, len(cookies))
	for _, c := range cookies {
		if idx := strings.Index(c, ";"); idx >= 0 {
			c = c[:idx]
		}
		if strings.TrimSpace(c) != "" {
			parts = append(parts, c)
		}
	}
	return strings.Join(parts, "; ")
}

// mergeCookies merges two cookie strings, with cookies2 taking precedence.
func mergeCookies(cookies1, cookies2 string) string {
	if cookies1 == "" {
		return cookies2
	}
	if cookies2 == "" {
		return cookies1
	}
	// Simple merge - cookies2 values override cookies1
	existing := make(map[string]string)
	for _, c := range strings.Split(cookies1, ";") {
		c = strings.TrimSpace(c)
		if idx := strings.Index(c, "="); idx > 0 {
			existing[strings.TrimSpace(c[:idx])] = c
		}
	}
	for _, c := range strings.Split(cookies2, ";") {
		c = strings.TrimSpace(c)
		if idx := strings.Index(c, "="); idx > 0 {
			existing[strings.TrimSpace(c[:idx])] = c
		}
	}
	parts := make([]string, 0, len(existing))
	for _, v := range existing {
		parts = append(parts, v)
	}
	return strings.Join(parts, "; ")
}

// extractXiaomiPlatformCookies filters cookies relevant to platform.xiaomimimo.com.
func extractXiaomiPlatformCookies(cookies string) string {
	required := []string{"api-platform_serviceToken", "userId", "api-platform_slh", "api-platform_ph"}
	parts := strings.Split(cookies, ";")
	found := make(map[string]string)
	for _, p := range parts {
		p = strings.TrimSpace(p)
		for _, req := range required {
			if strings.HasPrefix(p, req+"=") {
				found[req] = p
			}
		}
	}
	// Also include userId
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if strings.HasPrefix(p, "userId=") {
			found["userId"] = p
		}
	}
	if len(found) < 3 {
		return ""
	}
	result := make([]string, 0, len(found))
	for _, v := range found {
		result = append(result, v)
	}
	return strings.Join(result, "; ")
}

// extractJSONString extracts a string value for the given key from a JSON-like string.
func extractJSONString(s, key string) string {
	pattern := fmt.Sprintf(`"%s"\s*:\s*"([^"]+)"`, key)
	re := regexp.MustCompile(pattern)
	if m := re.FindStringSubmatch(s); len(m) > 1 {
		return m[1]
	}
	return ""
}

// truncate truncates a string to maxLen characters.
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// httpClientForXiaomiLogin returns an HTTP client configured for login requests.
func httpClientForXiaomiLogin(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth) *http.Client {
	return NewProxyAwareHTTPClient(ctx, cfg, auth, xiaomiLoginTimeout)
}

// Ensure rsa.EncryptPKCS1v15 is referenced for future proofing
var _ = rsa.EncryptPKCS1v15
