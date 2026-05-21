package executor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

const (
	cfClearanceWarmupPath = "/backend-api/wham/usage"
	cfClearanceTTL        = 30 * time.Minute
	cfClearanceBodyLimit  = 4096
)

type cfClearanceEntry struct {
	Cookie    string
	ExpiresAt time.Time
	Proxy     string
}

var cfClearanceStore sync.Map // key: authID (string), value: *cfClearanceEntry

// getCfClearance 从缓存获取有效的 cf_clearance cookie
func getCfClearance(authID string, proxy string) (string, bool) {
	if authID == "" {
		return "", false
	}
	val, ok := cfClearanceStore.Load(authID)
	if !ok {
		return "", false
	}
	entry, ok := val.(*cfClearanceEntry)
	if !ok {
		return "", false
	}
	if time.Now().After(entry.ExpiresAt) {
		cfClearanceStore.Delete(authID)
		return "", false
	}
	if entry.Proxy != proxy {
		cfClearanceStore.Delete(authID)
		return "", false
	}
	return entry.Cookie, true
}

// invalidateCfClearance 失效缓存的 cf_clearance cookie
func invalidateCfClearance(authID string) {
	if authID == "" {
		return
	}
	cfClearanceStore.Delete(authID)
}

// warmupCfClearance 通过 warmup 请求获取 cf_clearance cookie
func warmupCfClearance(ctx context.Context, authID string, cfg *config.Config, auth *cliproxyauth.Auth, baseURL string, bearerToken string) (string, error) {
	origin := codexConversationRequestOriginFromRawURL(baseURL)
	if origin == "" {
		return "", fmt.Errorf("cf_clearance: empty base URL")
	}

	client := newCodexConversationHTTPClient(ctx, cfg, auth, origin+"/backend-api/wham/usage")

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, origin+cfClearanceWarmupPath, nil)
	if err != nil {
		return "", fmt.Errorf("cf_clearance: create warmup request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(bearerToken))
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", codexConversationBrowserUserAgent)
	req.Header.Set("Origin", origin)
	req.Header.Set("Referer", origin+"/")

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("cf_clearance: warmup request failed: %w", err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	// 从 Set-Cookie 中提取 cf_clearance（只提取 cf_clearance，不提取 __cf_bm）
	for _, cookie := range resp.Cookies() {
		if cookie.Name == "cf_clearance" && strings.TrimSpace(cookie.Value) != "" {
			proxy := ""
			if auth != nil {
				proxy = auth.ProxyURL
			}
			cfClearanceStore.Store(authID, &cfClearanceEntry{
				Cookie:    strings.TrimSpace(cookie.Value),
				ExpiresAt: time.Now().Add(cfClearanceTTL),
				Proxy:     proxy,
			})
			log.Infof("codex: acquired cf_clearance for auth %s (proxy=%s)", authID, proxy)
			return strings.TrimSpace(cookie.Value), nil
		}
	}

	return "", fmt.Errorf("cf_clearance: no cf_clearance cookie in warmup response (status=%d)", resp.StatusCode)
}

// ensureCfClearance 确保有有效的 cf_clearance cookie
func ensureCfClearance(ctx context.Context, authID string, cfg *config.Config, auth *cliproxyauth.Auth, baseURL string, bearerToken string) string {
	proxy := ""
	if auth != nil {
		proxy = auth.ProxyURL
	}
	cookie, ok := getCfClearance(authID, proxy)
	if ok {
		return cookie
	}
	cookie, err := warmupCfClearance(ctx, authID, cfg, auth, baseURL, bearerToken)
	if err != nil {
		log.Warnf("codex: warmup cf_clearance failed for auth %s: %v", authID, err)
		return ""
	}
	return cookie
}

// isCfClearanceChallenge 检测 Cloudflare 挑战响应
func isCfClearanceChallenge(statusCode int, body []byte) bool {
	if statusCode == http.StatusForbidden {
		if len(body) == 0 {
			return false
		}
		lower := strings.ToLower(string(body))
		return strings.Contains(lower, "cf_chl_opt") ||
			strings.Contains(lower, "cf-chl-widget") ||
			strings.Contains(lower, "challenge-platform") ||
			strings.Contains(lower, "just a moment") ||
			strings.Contains(lower, "cf-browser-verification")
	}
	if statusCode == http.StatusServiceUnavailable {
		if len(body) > 0 {
			lower := strings.ToLower(string(body))
			if strings.Contains(lower, "cloudflare") {
				return true
			}
		}
	}
	return false
}

// injectCfClearanceCookie 向请求 cookie 中注入 cf_clearance
func injectCfClearanceCookie(existingCookie string, cfCookie string) string {
	if cfCookie == "" {
		return existingCookie
	}
	cfPart := "cf_clearance=" + cfCookie
	if existingCookie == "" {
		return cfPart
	}
	// 检查是否已有 cf_clearance
	if strings.Contains(existingCookie, "cf_clearance=") {
		return existingCookie
	}
	return existingCookie + "; " + cfPart
}

// readBodyPreview 预读响应 body 用于挑战检测，返回预读内容和恢复后的 body
func readBodyPreview(resp *http.Response) ([]byte, error) {
	if resp == nil || resp.Body == nil {
		return nil, nil
	}
	preview := make([]byte, cfClearanceBodyLimit)
	n, err := io.ReadFull(io.LimitReader(resp.Body, cfClearanceBodyLimit), preview)
	if n > 0 {
		preview = preview[:n]
	} else if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return nil, err
	}
	// 恢复 body
	remaining, _ := io.ReadAll(resp.Body)
	resp.Body = io.NopCloser(io.MultiReader(bytes.NewReader(preview), bytes.NewReader(remaining)))
	return preview, nil
}
