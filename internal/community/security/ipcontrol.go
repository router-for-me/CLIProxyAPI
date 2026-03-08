package security

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/db"
)

// ============================================================
// IP 访问控制器 — 黑白名单中间件
// 白名单优先：配置了白名单时仅放行白名单 IP，否则仅拦截黑名单
// ============================================================

// IPController IP 访问控制器
type IPController struct {
	mu        sync.RWMutex
	enabled   bool
	whitelist []*net.IPNet
	blacklist []*net.IPNet
	store     db.SecurityStore
}

// NewIPController 创建 IP 访问控制器
func NewIPController(store db.SecurityStore, enabled bool) *IPController {
	return &IPController{
		store:   store,
		enabled: enabled,
	}
}

// ============================================================
// 规则加载
// ============================================================

// LoadRules 从数据库加载 IP 规则到内存
func (c *IPController) LoadRules(ctx context.Context) error {
	rules, err := c.store.ListIPRules(ctx)
	if err != nil {
		return fmt.Errorf("加载 IP 规则失败: %w", err)
	}

	var wl, bl []*net.IPNet
	for _, r := range rules {
		cidr := normalizeCIDR(r.CIDR)
		_, ipNet, err := net.ParseCIDR(cidr)
		if err != nil {
			// 跳过格式异常的规则，不中断整体加载
			continue
		}
		switch r.RuleType {
		case "whitelist":
			wl = append(wl, ipNet)
		case "blacklist":
			bl = append(bl, ipNet)
		}
	}

	c.mu.Lock()
	c.whitelist = wl
	c.blacklist = bl
	c.mu.Unlock()

	return nil
}

// SetEnabled 动态开关
func (c *IPController) SetEnabled(v bool) {
	c.mu.Lock()
	c.enabled = v
	c.mu.Unlock()
}

// ============================================================
// Gin 中间件
// ============================================================

// Middleware 返回 Gin IP 访问控制中间件
func (c *IPController) Middleware() gin.HandlerFunc {
	return func(gc *gin.Context) {
		c.mu.RLock()
		enabled := c.enabled
		wl := c.whitelist
		bl := c.blacklist
		c.mu.RUnlock()

		if !enabled {
			gc.Next()
			return
		}

		// 使用 Gin 内置 ClientIP()，遵循 TrustedProxies 配置
		ipStr := gc.ClientIP()
		ip := net.ParseIP(ipStr)
		if ip == nil {
			gc.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"error": "无法解析客户端 IP",
			})
			return
		}

		// 白名单优先：存在白名单规则时，仅允许白名单 IP
		if len(wl) > 0 {
			if !matchAny(ip, wl) {
				gc.AbortWithStatusJSON(http.StatusForbidden, gin.H{
					"error": "IP 不在白名单中",
				})
				return
			}
			gc.Next()
			return
		}

		// 黑名单检查
		if matchAny(ip, bl) {
			gc.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"error": "IP 已被封禁",
			})
			return
		}

		gc.Next()
	}
}

// ============================================================
// 内部工具函数
// ============================================================

// matchAny 检查 IP 是否命中任意一条 CIDR 规则
func matchAny(ip net.IP, nets []*net.IPNet) bool {
	for _, n := range nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// normalizeCIDR 将裸 IP 补全为 CIDR 格式
// 例如: "10.0.0.1" -> "10.0.0.1/32", "::1" -> "::1/128"
func normalizeCIDR(s string) string {
	s = strings.TrimSpace(s)
	if strings.Contains(s, "/") {
		return s
	}
	ip := net.ParseIP(s)
	if ip == nil {
		return s
	}
	if ip.To4() != nil {
		return s + "/32"
	}
	return s + "/128"
}
