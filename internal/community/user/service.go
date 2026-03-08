package user

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/db"
	"golang.org/x/crypto/bcrypt"
)

// ============================================================
// 用户业务逻辑层 — 注册、认证、管理
// ============================================================

// Service 用户业务逻辑层
type Service struct {
	store db.UserStore
}

// NewService 创建用户服务
func NewService(store db.UserStore) *Service {
	return &Service{store: store}
}

// RegisterInput 注册输入
type RegisterInput struct {
	Username      string
	Email         string
	Password      string
	InviteCode    string
	OAuthProvider string
	OAuthID       string
}

// Register 注册新用户
func (s *Service) Register(ctx context.Context, input RegisterInput) (*db.User, error) {
	// 检查用户名是否已存在
	if existing, _ := s.store.GetUserByUsername(ctx, input.Username); existing != nil {
		return nil, fmt.Errorf("用户名已存在: %s", input.Username)
	}
	// 检查邮箱是否已存在
	if input.Email != "" {
		if existing, _ := s.store.GetUserByEmail(ctx, input.Email); existing != nil {
			return nil, fmt.Errorf("邮箱已注册: %s", input.Email)
		}
	}

	// 密码哈希（OAuth 用户无密码）
	var passwordHash string
	if input.Password != "" {
		hash, err := bcrypt.GenerateFromPassword([]byte(input.Password), bcrypt.DefaultCost)
		if err != nil {
			return nil, fmt.Errorf("密码哈希失败: %w", err)
		}
		passwordHash = string(hash)
	}

	// 生成 API Key
	apiKey, err := generateAPIKey()
	if err != nil {
		return nil, fmt.Errorf("生成 API Key 失败: %w", err)
	}

	// 生成用户专属裂变码
	referralCode, err := generateShortCode(8)
	if err != nil {
		return nil, fmt.Errorf("生成裂变码失败: %w", err)
	}

	now := time.Now()
	user := &db.User{
		UUID:          uuid.New().String(),
		Username:      input.Username,
		Email:         input.Email,
		PasswordHash:  passwordHash,
		Role:          db.RoleUser,
		Status:        db.StatusActive,
		APIKey:        apiKey,
		OAuthProvider: input.OAuthProvider,
		OAuthID:       input.OAuthID,
		InviteCode:    referralCode,
		PoolMode:      db.PoolPublic,
		CreatedAt:     now,
		UpdatedAt:     now,
	}

	if err := s.store.CreateUser(ctx, user); err != nil {
		return nil, fmt.Errorf("创建用户失败: %w", err)
	}
	return user, nil
}

// Authenticate 验证用户名/邮箱 + 密码
func (s *Service) Authenticate(ctx context.Context, login, password string) (*db.User, error) {
	// 尝试按用户名查找
	user, err := s.store.GetUserByUsername(ctx, login)
	if err != nil || user == nil {
		// 尝试按邮箱查找
		user, err = s.store.GetUserByEmail(ctx, login)
		if err != nil || user == nil {
			return nil, fmt.Errorf("用户不存在")
		}
	}
	if user.Status == db.StatusBanned {
		return nil, fmt.Errorf("账户已被封禁")
	}
	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)); err != nil {
		return nil, fmt.Errorf("密码错误")
	}
	return user, nil
}

// GetByAPIKey 通过 API Key 查找用户
func (s *Service) GetByAPIKey(ctx context.Context, apiKey string) (*db.User, error) {
	return s.store.GetUserByAPIKey(ctx, apiKey)
}

// GetByID 通过 ID 查找用户
func (s *Service) GetByID(ctx context.Context, id int64) (*db.User, error) {
	return s.store.GetUserByID(ctx, id)
}

// ResetAPIKey 重置用户 API Key
func (s *Service) ResetAPIKey(ctx context.Context, userID int64) (string, error) {
	user, err := s.store.GetUserByID(ctx, userID)
	if err != nil {
		return "", err
	}
	newKey, err := generateAPIKey()
	if err != nil {
		return "", err
	}
	user.APIKey = newKey
	user.UpdatedAt = time.Now()
	if err := s.store.UpdateUser(ctx, user); err != nil {
		return "", err
	}
	return newKey, nil
}

// BanUser 封禁用户
func (s *Service) BanUser(ctx context.Context, userID int64) error {
	user, err := s.store.GetUserByID(ctx, userID)
	if err != nil {
		return err
	}
	user.Status = db.StatusBanned
	user.UpdatedAt = time.Now()
	return s.store.UpdateUser(ctx, user)
}

// UnbanUser 解封用户
func (s *Service) UnbanUser(ctx context.Context, userID int64) error {
	user, err := s.store.GetUserByID(ctx, userID)
	if err != nil {
		return err
	}
	user.Status = db.StatusActive
	user.UpdatedAt = time.Now()
	return s.store.UpdateUser(ctx, user)
}

// ListUsers 列出用户
func (s *Service) ListUsers(ctx context.Context, opts db.ListUsersOpts) ([]*db.User, int64, error) {
	return s.store.ListUsers(ctx, opts)
}

// ============================================================
// 辅助函数
// ============================================================

func generateAPIKey() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "cpk-" + hex.EncodeToString(b), nil
}

func generateShortCode(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b)[:n], nil
}
