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
// secret 长度必须 >= 32 字节以满足 HS256 安全要求
func NewJWTManager(secret string, accessTTL, refreshTTL time.Duration) (*JWTManager, error) {
	if len(secret) < 32 {
		return nil, fmt.Errorf("JWT secret 长度不足: 需要 >= 32 字节, 当前 %d 字节", len(secret))
	}
	return &JWTManager{
		secret:          []byte(secret),
		accessTokenTTL:  accessTTL,
		refreshTokenTTL: refreshTTL,
	}, nil
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
