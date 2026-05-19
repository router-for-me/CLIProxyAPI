package helps

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

type persistedCookies struct {
	Cookies   string    `json:"cookies"`
	ExpiresAt time.Time `json:"expires_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

const (
	xiaomiCookieFilePrefix = "xiaomi_platform_cookies"
	xiaomiCookieFileTTL    = 12 * time.Hour
)

var (
	cookieStoreOnce sync.Once
	cookieStoreDir  string
)

// InitXiaomiCookieStore 初始化 cookie 持久化目录。
func InitXiaomiCookieStore(authDir string) {
	cookieStoreOnce.Do(func() {
		cookieStoreDir = authDir
		if cookieStoreDir == "" {
			exe, err := os.Executable()
			if err == nil {
				cookieStoreDir = filepath.Dir(exe)
			}
		}
	})
}

// cookieFilePath 返回指定账号的持久化文件路径，email 为空时使用 "global" 作为标识。
func cookieFilePath(email string) string {
	if cookieStoreDir == "" {
		return ""
	}
	key := strings.TrimSpace(email)
	if key == "" {
		key = "global"
	}
	h := sha256.Sum256([]byte(key))
	suffix := hex.EncodeToString(h[:])[:16]
	return filepath.Join(cookieStoreDir, xiaomiCookieFilePrefix+"_"+suffix+".json")
}

func loadXiaomiCookiesFromFile(email string) (string, bool) {
	path := cookieFilePath(email)
	if path == "" {
		return "", false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Debugf("xiaomi: 读取 cookie 文件失败: %v", err)
		}
		return "", false
	}
	var pc persistedCookies
	if err := json.Unmarshal(data, &pc); err != nil {
		log.Debugf("xiaomi: 解析 cookie 文件失败: %v", err)
		return "", false
	}
	if time.Now().After(pc.ExpiresAt) {
		log.Debug("xiaomi: 持久化 cookie 已过期")
		return "", false
	}
	if strings.TrimSpace(pc.Cookies) == "" {
		return "", false
	}
	return pc.Cookies, true
}

// SaveXiaomiCookiesToFile 将指定账号的平台 cookies 持久化到 JSON 文件。
func SaveXiaomiCookiesToFile(email, cookies string) error {
	cookies = strings.TrimSpace(cookies)
	if cookies == "" {
		return fmt.Errorf("empty cookies")
	}
	path := cookieFilePath(email)
	if path == "" {
		return fmt.Errorf("cookie store not initialized")
	}
	pc := persistedCookies{
		Cookies:   cookies,
		ExpiresAt: time.Now().Add(xiaomiCookieFileTTL),
		UpdatedAt: time.Now(),
	}
	data, err := json.MarshalIndent(pc, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal cookies: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("create cookie dir: %w", err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("write cookie file: %w", err)
	}
	log.Infof("xiaomi: cookies 已持久化到 %s", path)
	return nil
}

// DeleteXiaomiCookiesFile 删除指定账号的持久化 cookie 文件。
func DeleteXiaomiCookiesFile(email string) {
	path := cookieFilePath(email)
	if path == "" {
		return
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		log.Warnf("xiaomi: 删除 cookie 文件失败: %v", err)
	}
}
