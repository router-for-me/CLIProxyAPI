package kiro

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

// FileTokenRepository 实现 TokenRepository 接口，基于文件系统存储
type FileTokenRepository struct {
	mu      sync.RWMutex
	baseDir string
}

// NewFileTokenRepository 创建一个新的文件 token 存储库
func NewFileTokenRepository(baseDir string) *FileTokenRepository {
	return &FileTokenRepository{
		baseDir: baseDir,
	}
}

// SetBaseDir 设置基础目录
func (r *FileTokenRepository) SetBaseDir(dir string) {
	r.mu.Lock()
	r.baseDir = strings.TrimSpace(dir)
	r.mu.Unlock()
}

// FindOldestUnverified 查找需要刷新的 token（按最后验证时间排序）
func (r *FileTokenRepository) FindOldestUnverified(limit int) []*Token {
	r.mu.RLock()
	baseDir := r.baseDir
	r.mu.RUnlock()

	if baseDir == "" {
		log.Debug("token repository: base directory not configured")
		return nil
	}

	var tokens []*Token

	err := filepath.WalkDir(baseDir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil // 忽略错误，继续遍历
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(strings.ToLower(d.Name()), ".json") {
			return nil
		}

		// 只处理 kiro 相关的 token 文件
		if !strings.HasPrefix(d.Name(), "kiro-") {
			return nil
		}

		token, err := r.readTokenFile(path)
		if err != nil {
			log.Debugf("token repository: failed to read token file %s: %v", path, err)
			return nil
		}

		if token != nil && token.RefreshToken != "" {
			// 检查 token 是否需要刷新（过期前 5 分钟）
			if token.ExpiresAt.IsZero() || time.Until(token.ExpiresAt) < 5*time.Minute {
				tokens = append(tokens, token)
			}
		}

		return nil
	})

	if err != nil {
		log.Warnf("token repository: error walking directory: %v", err)
	}

	// 按最后验证时间排序（最旧的优先）
	sort.Slice(tokens, func(i, j int) bool {
		return tokens[i].LastVerified.Before(tokens[j].LastVerified)
	})

	// 限制返回数量
	if limit > 0 && len(tokens) > limit {
		tokens = tokens[:limit]
	}

	return tokens
}

// UpdateToken 更新 token 并持久化到文件
func (r *FileTokenRepository) UpdateToken(token *Token) error {
	if token == nil {
		return fmt.Errorf("token repository: token is nil")
	}

	r.mu.RLock()
	baseDir := r.baseDir
	r.mu.RUnlock()

	if baseDir == "" {
		return fmt.Errorf("token repository: base directory not configured")
	}

	// 构建文件路径
	filePath := filepath.Join(baseDir, token.ID)
	if !strings.HasSuffix(filePath, ".json") {
		filePath += ".json"
	}

	// 读取现有文件内容
	existingData := make(map[string]any)
	if data, err := os.ReadFile(filePath); err == nil {
		_ = json.Unmarshal(data, &existingData)
	}

	// 更新字段
	existingData["access_token"] = token.AccessToken
	existingData["refresh_token"] = token.RefreshToken
	existingData["last_refresh"] = time.Now().Format(time.RFC3339)

	if !token.ExpiresAt.IsZero() {
		existingData["expires_at"] = token.ExpiresAt.Format(time.RFC3339)
	}

	// 保持原有的关键字段
	if token.ClientID != "" {
		existingData["client_id"] = token.ClientID
	}
	if token.ClientSecret != "" {
		existingData["client_secret"] = token.ClientSecret
	}
	if token.AuthMethod != "" {
		existingData["auth_method"] = token.AuthMethod
	}
	if token.Region != "" {
		existingData["region"] = token.Region
	}
	if token.StartURL != "" {
		existingData["start_url"] = token.StartURL
	}

	// 序列化并写入文件
	raw, err := json.MarshalIndent(existingData, "", "  ")
	if err != nil {
		return fmt.Errorf("token repository: marshal failed: %w", err)
	}

	// 原子写入：先写入临时文件，再重命名
	tmpPath := filePath + ".tmp"
	if err := os.WriteFile(tmpPath, raw, 0o600); err != nil {
		return fmt.Errorf("token repository: write temp file failed: %w", err)
	}
	if err := os.Rename(tmpPath, filePath); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("token repository: rename failed: %w", err)
	}

	log.Debugf("token repository: updated token %s", token.ID)
	return nil
}

// readTokenFile 从文件读取 token
func (r *FileTokenRepository) readTokenFile(path string) (*Token, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var metadata map[string]any
	if err := json.Unmarshal(data, &metadata); err != nil {
		return nil, err
	}

	// 检查是否是 kiro token
	tokenType, _ := metadata["type"].(string)
	if tokenType != "kiro" {
		return nil, nil
	}

	// 检查 auth_method (case-insensitive comparison to handle "IdC", "IDC", "idc", etc.)
	authMethod, _ := metadata["auth_method"].(string)
	authMethod = strings.ToLower(authMethod)
	if authMethod != "idc" && authMethod != "builder-id" {
		return nil, nil // 只处理 IDC 和 Builder ID token
	}

	token := &Token{
		ID:         filepath.Base(path),
		AuthMethod: authMethod,
	}

	// 解析各字段
	if v, ok := metadata["access_token"].(string); ok {
		token.AccessToken = v
	}
	if v, ok := metadata["refresh_token"].(string); ok {
		token.RefreshToken = v
	}
	if v, ok := metadata["client_id"].(string); ok {
		token.ClientID = v
	}
	if v, ok := metadata["client_secret"].(string); ok {
		token.ClientSecret = v
	}
	if v, ok := metadata["region"].(string); ok {
		token.Region = v
	}
	if v, ok := metadata["start_url"].(string); ok {
		token.StartURL = v
	}
	if v, ok := metadata["provider"].(string); ok {
		token.Provider = v
	}

	// 解析时间字段
	if v, ok := metadata["expires_at"].(string); ok {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			token.ExpiresAt = t
		}
	}
	if v, ok := metadata["last_refresh"].(string); ok {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			token.LastVerified = t
		}
	}

	return token, nil
}

// ListKiroTokens 列出所有 Kiro token（用于调试）
func (r *FileTokenRepository) ListKiroTokens(ctx context.Context) ([]*Token, error) {
	r.mu.RLock()
	baseDir := r.baseDir
	r.mu.RUnlock()

	if baseDir == "" {
		return nil, fmt.Errorf("token repository: base directory not configured")
	}

	var tokens []*Token

	err := filepath.WalkDir(baseDir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasPrefix(d.Name(), "kiro-") || !strings.HasSuffix(d.Name(), ".json") {
			return nil
		}

		token, err := r.readTokenFile(path)
		if err != nil {
			return nil
		}
		if token != nil {
			tokens = append(tokens, token)
		}
		return nil
	})

	return tokens, err
}
