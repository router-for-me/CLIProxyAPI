# Phase 2: 用户体系

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** 实现多方式用户注册登录体系——邮箱注册、OAuth、管理员邀请码、用户裂变码、兑换码注册。

**Architecture:** `internal/community/user/` 模块实现用户 CRUD、认证、JWT 签发。与 `internal/db/` 存储层通过接口交互。

**Tech Stack:** Go 1.26 / Gin / JWT (HS256) / bcrypt / SMTP

**Depends on:** Phase 1 (数据库抽象层)

---

### Task 1: 实现用户 Service 层

**Files:**
- Create: `internal/community/user/service.go`
- Test: `internal/community/user/service_test.go`

**Step 1: 编写用户 Service**

创建 `internal/community/user/service.go`：

```go
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
	Username  string
	Email     string
	Password  string
	InviteCode string
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
```

**Step 2: 编写测试**

创建 `internal/community/user/service_test.go`：

```go
package user_test

import (
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/community/user"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/db"
)

func newTestService(t *testing.T) *user.Service {
	t.Helper()
	ctx := context.Background()
	store, err := db.NewSQLiteStore(ctx, ":memory:")
	if err != nil {
		t.Fatalf("创建存储失败: %v", err)
	}
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return user.NewService(store)
}

func TestService_Register(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	u, err := svc.Register(ctx, user.RegisterInput{
		Username: "testuser",
		Email:    "test@example.com",
		Password: "password123",
	})
	if err != nil {
		t.Fatalf("注册失败: %v", err)
	}
	if u.Username != "testuser" {
		t.Errorf("用户名不匹配: got %s", u.Username)
	}
	if u.APIKey == "" {
		t.Error("API Key 不应为空")
	}
	if u.Role != db.RoleUser {
		t.Errorf("角色应为 user, got %s", u.Role)
	}
}

func TestService_Register_DuplicateUsername(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	_, _ = svc.Register(ctx, user.RegisterInput{
		Username: "testuser",
		Password: "password123",
	})

	_, err := svc.Register(ctx, user.RegisterInput{
		Username: "testuser",
		Password: "password456",
	})
	if err == nil {
		t.Fatal("重复用户名应该返回错误")
	}
}

func TestService_Authenticate(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	_, _ = svc.Register(ctx, user.RegisterInput{
		Username: "testuser",
		Password: "correct_password",
	})

	// 正确密码
	u, err := svc.Authenticate(ctx, "testuser", "correct_password")
	if err != nil {
		t.Fatalf("认证失败: %v", err)
	}
	if u.Username != "testuser" {
		t.Error("用户名不匹配")
	}

	// 错误密码
	_, err = svc.Authenticate(ctx, "testuser", "wrong_password")
	if err == nil {
		t.Fatal("错误密码应该返回错误")
	}
}

func TestService_BanUnban(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	u, _ := svc.Register(ctx, user.RegisterInput{
		Username: "testuser",
		Password: "password123",
	})

	// 封禁
	if err := svc.BanUser(ctx, u.ID); err != nil {
		t.Fatalf("封禁失败: %v", err)
	}
	// 被封禁后无法认证
	_, err := svc.Authenticate(ctx, "testuser", "password123")
	if err == nil {
		t.Fatal("封禁用户认证应该失败")
	}

	// 解封
	if err := svc.UnbanUser(ctx, u.ID); err != nil {
		t.Fatalf("解封失败: %v", err)
	}
	_, err = svc.Authenticate(ctx, "testuser", "password123")
	if err != nil {
		t.Fatalf("解封后认证应成功: %v", err)
	}
}
```

**Step 3: 运行测试**

Run: `go test ./internal/community/user/... -v`
Expected: PASS

**Step 4: Commit**

```bash
git add internal/community/user/
git commit -m "feat(user): implement user service with registration and authentication"
```

---

### Task 2: 实现 JWT 鉴权

**Files:**
- Create: `internal/community/user/jwt.go`
- Test: `internal/community/user/jwt_test.go`

**Step 1: 编写 JWT 签发/验证**

创建 `internal/community/user/jwt.go`：

```go
package user

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// ============================================================
// JWT 实现 — 轻量级 HS256，无第三方依赖
// ============================================================

// JWTManager JWT 管理器
type JWTManager struct {
	secret          []byte
	accessTokenTTL  time.Duration
	refreshTokenTTL time.Duration
}

// NewJWTManager 创建 JWT 管理器
func NewJWTManager(secret string, accessTTL, refreshTTL time.Duration) *JWTManager {
	return &JWTManager{
		secret:          []byte(secret),
		accessTokenTTL:  accessTTL,
		refreshTokenTTL: refreshTTL,
	}
}

// TokenPair Access + Refresh Token 对
type TokenPair struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
}

// Claims JWT 载荷
type Claims struct {
	UserID   int64  `json:"uid"`
	UUID     string `json:"sub"`
	Role     string `json:"role"`
	Type     string `json:"type"` // access / refresh
	IssuedAt int64  `json:"iat"`
	ExpireAt int64  `json:"exp"`
}

// GenerateTokenPair 生成 Token 对
func (m *JWTManager) GenerateTokenPair(userID int64, userUUID string, role string) (*TokenPair, error) {
	now := time.Now()
	accessClaims := Claims{
		UserID:   userID,
		UUID:     userUUID,
		Role:     role,
		Type:     "access",
		IssuedAt: now.Unix(),
		ExpireAt: now.Add(m.accessTokenTTL).Unix(),
	}
	accessToken, err := m.sign(accessClaims)
	if err != nil {
		return nil, err
	}

	refreshClaims := Claims{
		UserID:   userID,
		UUID:     userUUID,
		Role:     role,
		Type:     "refresh",
		IssuedAt: now.Unix(),
		ExpireAt: now.Add(m.refreshTokenTTL).Unix(),
	}
	refreshToken, err := m.sign(refreshClaims)
	if err != nil {
		return nil, err
	}

	return &TokenPair{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		ExpiresIn:    int64(m.accessTokenTTL.Seconds()),
	}, nil
}

// ValidateToken 验证 Token 并返回 Claims
func (m *JWTManager) ValidateToken(token string) (*Claims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("无效的 token 格式")
	}

	// 验证签名
	sigInput := parts[0] + "." + parts[1]
	expectedSig := m.hmacSign([]byte(sigInput))
	actualSig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, fmt.Errorf("签名解码失败")
	}
	if !hmac.Equal(expectedSig, actualSig) {
		return nil, fmt.Errorf("签名验证失败")
	}

	// 解析 Claims
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("载荷解码失败")
	}
	var claims Claims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, fmt.Errorf("载荷解析失败")
	}

	// 检查过期
	if time.Now().Unix() > claims.ExpireAt {
		return nil, fmt.Errorf("token 已过期")
	}

	return &claims, nil
}

// sign 签发 JWT
func (m *JWTManager) sign(claims Claims) (string, error) {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	payloadB64 := base64.RawURLEncoding.EncodeToString(payload)
	sigInput := header + "." + payloadB64
	sig := base64.RawURLEncoding.EncodeToString(m.hmacSign([]byte(sigInput)))
	return sigInput + "." + sig, nil
}

func (m *JWTManager) hmacSign(data []byte) []byte {
	h := hmac.New(sha256.New, m.secret)
	h.Write(data)
	return h.Sum(nil)
}
```

**Step 2: 编写 JWT 测试**

创建 `internal/community/user/jwt_test.go`：

```go
package user_test

import (
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/community/user"
)

func TestJWTManager_GenerateAndValidate(t *testing.T) {
	mgr := user.NewJWTManager("test-secret-key", 2*time.Hour, 7*24*time.Hour)

	pair, err := mgr.GenerateTokenPair(1, "uuid-123", "user")
	if err != nil {
		t.Fatalf("生成 token 失败: %v", err)
	}
	if pair.AccessToken == "" || pair.RefreshToken == "" {
		t.Fatal("token 不应为空")
	}

	// 验证 Access Token
	claims, err := mgr.ValidateToken(pair.AccessToken)
	if err != nil {
		t.Fatalf("验证 access token 失败: %v", err)
	}
	if claims.UserID != 1 {
		t.Errorf("UserID 不匹配: got %d", claims.UserID)
	}
	if claims.Type != "access" {
		t.Errorf("Type 应为 access, got %s", claims.Type)
	}

	// 验证 Refresh Token
	claims, err = mgr.ValidateToken(pair.RefreshToken)
	if err != nil {
		t.Fatalf("验证 refresh token 失败: %v", err)
	}
	if claims.Type != "refresh" {
		t.Errorf("Type 应为 refresh, got %s", claims.Type)
	}
}

func TestJWTManager_ExpiredToken(t *testing.T) {
	mgr := user.NewJWTManager("test-secret", 1*time.Millisecond, 1*time.Millisecond)

	pair, _ := mgr.GenerateTokenPair(1, "uuid", "user")
	time.Sleep(10 * time.Millisecond)

	_, err := mgr.ValidateToken(pair.AccessToken)
	if err == nil {
		t.Fatal("过期 token 应该返回错误")
	}
}

func TestJWTManager_InvalidSignature(t *testing.T) {
	mgr1 := user.NewJWTManager("secret-1", 2*time.Hour, 7*24*time.Hour)
	mgr2 := user.NewJWTManager("secret-2", 2*time.Hour, 7*24*time.Hour)

	pair, _ := mgr1.GenerateTokenPair(1, "uuid", "user")
	_, err := mgr2.ValidateToken(pair.AccessToken)
	if err == nil {
		t.Fatal("不同密钥签名的 token 验证应失败")
	}
}
```

**Step 3: 运行测试**

Run: `go test ./internal/community/user/... -v -run TestJWT`
Expected: PASS

**Step 4: Commit**

```bash
git add internal/community/user/jwt.go internal/community/user/jwt_test.go
git commit -m "feat(user): implement JWT token generation and validation (HS256)"
```

---

### Task 3: 实现邮箱验证服务

**Files:**
- Create: `internal/community/user/email.go`
- Test: `internal/community/user/email_test.go`

**Step 1: 编写邮箱验证码服务**

创建 `internal/community/user/email.go`：

```go
package user

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"net/smtp"
	"sync"
	"time"
)

// ============================================================
// 邮箱验证码服务 — SMTP 发送 + 内存缓存验证码
// ============================================================

// EmailService 邮件验证服务
type EmailService struct {
	host     string
	port     int
	username string
	password string
	from     string
	useTLS   bool

	mu    sync.RWMutex
	codes map[string]*verifyCode // email -> code
}

type verifyCode struct {
	Code      string
	ExpiresAt time.Time
}

// NewEmailService 创建邮件服务
func NewEmailService(host string, port int, username, password, from string, useTLS bool) *EmailService {
	return &EmailService{
		host:     host,
		port:     port,
		username: username,
		password: password,
		from:     from,
		useTLS:   useTLS,
		codes:    make(map[string]*verifyCode),
	}
}

// SendVerificationCode 发送验证码
func (s *EmailService) SendVerificationCode(email string) error {
	code, err := generateNumericCode(6)
	if err != nil {
		return fmt.Errorf("生成验证码失败: %w", err)
	}

	// 缓存验证码（10分钟有效）
	s.mu.Lock()
	s.codes[email] = &verifyCode{
		Code:      code,
		ExpiresAt: time.Now().Add(10 * time.Minute),
	}
	s.mu.Unlock()

	// 发送邮件
	subject := "CLIProxyAPI 公益站 — 邮箱验证码"
	body := fmt.Sprintf("您的验证码是: %s\n\n此验证码 10 分钟内有效，请勿泄露给他人。", code)
	return s.sendMail(email, subject, body)
}

// VerifyCode 验证验证码
func (s *EmailService) VerifyCode(email, code string) bool {
	s.mu.RLock()
	vc, ok := s.codes[email]
	s.mu.RUnlock()
	if !ok {
		return false
	}
	if time.Now().After(vc.ExpiresAt) {
		s.mu.Lock()
		delete(s.codes, email)
		s.mu.Unlock()
		return false
	}
	if vc.Code != code {
		return false
	}
	// 验证成功后删除
	s.mu.Lock()
	delete(s.codes, email)
	s.mu.Unlock()
	return true
}

// CleanExpired 清理过期验证码
func (s *EmailService) CleanExpired() {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for email, vc := range s.codes {
		if now.After(vc.ExpiresAt) {
			delete(s.codes, email)
		}
	}
}

func (s *EmailService) sendMail(to, subject, body string) error {
	addr := fmt.Sprintf("%s:%d", s.host, s.port)
	auth := smtp.PlainAuth("", s.username, s.password, s.host)
	msg := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n%s",
		s.from, to, subject, body)
	return smtp.SendMail(addr, auth, s.from, []string{to}, []byte(msg))
}

func generateNumericCode(n int) (string, error) {
	code := ""
	for i := 0; i < n; i++ {
		num, err := rand.Int(rand.Reader, big.NewInt(10))
		if err != nil {
			return "", err
		}
		code += fmt.Sprintf("%d", num.Int64())
	}
	return code, nil
}
```

**Step 2: 编写测试（不依赖真实 SMTP）**

创建 `internal/community/user/email_test.go`：

```go
package user_test

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/community/user"
)

func TestEmailService_VerifyCode(t *testing.T) {
	svc := user.NewEmailService("", 0, "", "", "", false)

	// 手动触发（跳过实际发送，测试验证逻辑）
	// 使用 VerifyCode 直接测试缓存逻辑不可行——需要 SendVerificationCode 先存入
	// 由于 sendMail 会失败（无 SMTP），我们测试 VerifyCode 在无码时返回 false
	if svc.VerifyCode("test@example.com", "123456") {
		t.Fatal("无验证码时应返回 false")
	}
}
```

**Step 3: 运行测试**

Run: `go test ./internal/community/user/... -v -run TestEmailService`
Expected: PASS

**Step 4: Commit**

```bash
git add internal/community/user/email.go internal/community/user/email_test.go
git commit -m "feat(user): implement email verification code service with SMTP"
```

---

### Task 4: 实现用户 API Handler

**Files:**
- Create: `internal/community/user/handler_auth.go`
- Create: `internal/community/user/handler_user.go`
- Create: `internal/community/user/handler_admin.go`

> 按职责拆分为三个 handler 文件，避免单文件过大。

**Step 1: 编写认证 Handler（登录/注册）**

创建 `internal/community/user/handler_auth.go`：

```go
package user

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// AuthHandler 认证相关 HTTP Handler
type AuthHandler struct {
	userSvc  *Service
	jwtMgr   *JWTManager
	emailSvc *EmailService
}

// NewAuthHandler 创建认证 Handler
func NewAuthHandler(userSvc *Service, jwtMgr *JWTManager, emailSvc *EmailService) *AuthHandler {
	return &AuthHandler{userSvc: userSvc, jwtMgr: jwtMgr, emailSvc: emailSvc}
}

// RegisterRoutes 注册认证路由
func (h *AuthHandler) RegisterRoutes(rg *gin.RouterGroup) {
	auth := rg.Group("/auth")
	auth.POST("/register", h.Register)
	auth.POST("/login", h.Login)
	auth.POST("/refresh", h.RefreshToken)
	auth.POST("/send-code", h.SendVerificationCode)
}

type registerRequest struct {
	Username   string `json:"username" binding:"required"`
	Email      string `json:"email"`
	Password   string `json:"password" binding:"required,min=6"`
	InviteCode string `json:"invite_code"`
	EmailCode  string `json:"email_code"`
}

// Register 用户注册
func (h *AuthHandler) Register(c *gin.Context) {
	var req registerRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求参数无效: " + err.Error()})
		return
	}

	// 邮箱验证码检查（如果提供了邮箱）
	if req.Email != "" && req.EmailCode != "" {
		if !h.emailSvc.VerifyCode(req.Email, req.EmailCode) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "邮箱验证码错误或已过期"})
			return
		}
	}

	user, err := h.userSvc.Register(c.Request.Context(), RegisterInput{
		Username:   req.Username,
		Email:      req.Email,
		Password:   req.Password,
		InviteCode: req.InviteCode,
	})
	if err != nil {
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		return
	}

	tokens, err := h.jwtMgr.GenerateTokenPair(user.ID, user.UUID, string(user.Role))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "生成 Token 失败"})
		return
	}

	c.JSON(http.StatusCreated, gin.H{
		"user":   user,
		"tokens": tokens,
	})
}

type loginRequest struct {
	Login    string `json:"login" binding:"required"`    // 用户名或邮箱
	Password string `json:"password" binding:"required"`
}

// Login 用户登录
func (h *AuthHandler) Login(c *gin.Context) {
	var req loginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求参数无效"})
		return
	}

	user, err := h.userSvc.Authenticate(c.Request.Context(), req.Login, req.Password)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": err.Error()})
		return
	}

	tokens, err := h.jwtMgr.GenerateTokenPair(user.ID, user.UUID, string(user.Role))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "生成 Token 失败"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"user":   user,
		"tokens": tokens,
	})
}

type refreshRequest struct {
	RefreshToken string `json:"refresh_token" binding:"required"`
}

// RefreshToken 刷新 Token
func (h *AuthHandler) RefreshToken(c *gin.Context) {
	var req refreshRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求参数无效"})
		return
	}

	claims, err := h.jwtMgr.ValidateToken(req.RefreshToken)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Refresh Token 无效"})
		return
	}
	if claims.Type != "refresh" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Token 类型错误"})
		return
	}

	tokens, err := h.jwtMgr.GenerateTokenPair(claims.UserID, claims.UUID, claims.Role)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "生成 Token 失败"})
		return
	}

	c.JSON(http.StatusOK, tokens)
}

type sendCodeRequest struct {
	Email string `json:"email" binding:"required,email"`
}

// SendVerificationCode 发送邮箱验证码
func (h *AuthHandler) SendVerificationCode(c *gin.Context) {
	var req sendCodeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求参数无效"})
		return
	}

	if err := h.emailSvc.SendVerificationCode(req.Email); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "发送验证码失败"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "验证码已发送"})
}
```

**Step 2: 编写用户端 Handler**

创建 `internal/community/user/handler_user.go`：

```go
package user

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// UserHandler 用户端 HTTP Handler
type UserHandler struct {
	userSvc *Service
}

// NewUserHandler 创建用户端 Handler
func NewUserHandler(userSvc *Service) *UserHandler {
	return &UserHandler{userSvc: userSvc}
}

// RegisterRoutes 注册用户端路由
func (h *UserHandler) RegisterRoutes(rg *gin.RouterGroup) {
	user := rg.Group("/user")
	user.GET("/profile", h.GetProfile)
	user.PUT("/profile", h.UpdateProfile)
	user.POST("/reset-api-key", h.ResetAPIKey)
}

// GetProfile 获取个人信息
func (h *UserHandler) GetProfile(c *gin.Context) {
	userID := c.GetInt64("userID")
	user, err := h.userSvc.GetByID(c.Request.Context(), userID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "用户不存在"})
		return
	}
	c.JSON(http.StatusOK, user)
}

// UpdateProfile 更新个人信息
func (h *UserHandler) UpdateProfile(c *gin.Context) {
	// 后续实现密码修改、邮箱绑定等
	c.JSON(http.StatusOK, gin.H{"message": "更新成功"})
}

// ResetAPIKey 重置 API Key
func (h *UserHandler) ResetAPIKey(c *gin.Context) {
	userID := c.GetInt64("userID")
	newKey, err := h.userSvc.ResetAPIKey(c.Request.Context(), userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "重置失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"api_key": newKey})
}
```

**Step 3: 编写管理端 Handler**

创建 `internal/community/user/handler_admin.go`：

```go
package user

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/db"
)

// AdminUserHandler 管理端用户 Handler
type AdminUserHandler struct {
	userSvc *Service
}

// NewAdminUserHandler 创建管理端用户 Handler
func NewAdminUserHandler(userSvc *Service) *AdminUserHandler {
	return &AdminUserHandler{userSvc: userSvc}
}

// RegisterRoutes 注册管理端路由
func (h *AdminUserHandler) RegisterRoutes(rg *gin.RouterGroup) {
	admin := rg.Group("/admin/users")
	admin.GET("", h.ListUsers)
	admin.POST("/:id/ban", h.BanUser)
	admin.POST("/:id/unban", h.UnbanUser)
}

// ListUsers 列出所有用户
func (h *AdminUserHandler) ListUsers(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	search := c.Query("search")

	users, total, err := h.userSvc.ListUsers(c.Request.Context(), db.ListUsersOpts{
		Page:     page,
		PageSize: pageSize,
		Search:   search,
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "查询失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"users": users,
		"total": total,
		"page":  page,
	})
}

// BanUser 封禁用户
func (h *AdminUserHandler) BanUser(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的用户 ID"})
		return
	}
	if err := h.userSvc.BanUser(c.Request.Context(), id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "用户已封禁"})
}

// UnbanUser 解封用户
func (h *AdminUserHandler) UnbanUser(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的用户 ID"})
		return
	}
	if err := h.userSvc.UnbanUser(c.Request.Context(), id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "用户已解封"})
}
```

**Step 4: 运行编译检查**

Run: `go build ./internal/community/user/...`
Expected: 无错误

**Step 5: Commit**

```bash
git add internal/community/user/handler_*.go
git commit -m "feat(user): add HTTP handlers for auth, user profile, and admin management"
```

---

### Task 5: 实现 JWT 鉴权中间件

**Files:**
- Create: `internal/community/user/middleware.go`

**Step 1: 编写 JWT 中间件**

创建 `internal/community/user/middleware.go`：

```go
package user

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// JWTMiddleware JWT 鉴权中间件
func JWTMiddleware(jwtMgr *JWTManager) gin.HandlerFunc {
	return func(c *gin.Context) {
		authHeader := c.GetHeader("Authorization")
		if authHeader == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "缺少 Authorization 头"})
			c.Abort()
			return
		}

		token := strings.TrimPrefix(authHeader, "Bearer ")
		if token == authHeader {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Authorization 格式错误，需要 Bearer token"})
			c.Abort()
			return
		}

		claims, err := jwtMgr.ValidateToken(token)
		if err != nil {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Token 无效: " + err.Error()})
			c.Abort()
			return
		}

		if claims.Type != "access" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "需要 Access Token"})
			c.Abort()
			return
		}

		// 设置用户信息到 Gin Context
		c.Set("userID", claims.UserID)
		c.Set("userUUID", claims.UUID)
		c.Set("userRole", claims.Role)
		c.Next()
	}
}

// AdminMiddleware 管理员权限中间件（需在 JWTMiddleware 之后使用）
func AdminMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		role := c.GetString("userRole")
		if role != string("admin") {
			c.JSON(http.StatusForbidden, gin.H{"error": "需要管理员权限"})
			c.Abort()
			return
		}
		c.Next()
	}
}
```

**Step 2: 运行编译检查**

Run: `go build ./internal/community/user/...`
Expected: 无错误

**Step 3: Commit**

```bash
git add internal/community/user/middleware.go
git commit -m "feat(user): add JWT and admin authorization middleware"
```
