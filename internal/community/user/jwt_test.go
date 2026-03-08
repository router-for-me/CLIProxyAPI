package user_test

import (
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/community/user"
)

// testSecret 满足 >= 32 字节最低安全要求的测试密钥
const testSecret = "test-secret-key-must-be-32-bytes!"

func TestJWTManager_GenerateAndValidate(t *testing.T) {
	mgr, err := user.NewJWTManager(testSecret, 2*time.Hour, 7*24*time.Hour)
	if err != nil {
		t.Fatalf("创建 JWT 管理器失败: %v", err)
	}

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

func TestJWTManager_ShortSecret(t *testing.T) {
	_, err := user.NewJWTManager("too-short", 2*time.Hour, 7*24*time.Hour)
	if err == nil {
		t.Fatal("短密钥应返回错误")
	}
}

func TestJWTManager_ExpiredToken(t *testing.T) {
	mgr, err := user.NewJWTManager(testSecret, 1*time.Millisecond, 1*time.Millisecond)
	if err != nil {
		t.Fatalf("创建 JWT 管理器失败: %v", err)
	}

	pair, _ := mgr.GenerateTokenPair(1, "uuid", "user")
	time.Sleep(10 * time.Millisecond)

	_, err = mgr.ValidateToken(pair.AccessToken)
	if err == nil {
		t.Fatal("过期 token 应该返回错误")
	}
}

func TestJWTManager_InvalidSignature(t *testing.T) {
	mgr1, err := user.NewJWTManager("secret-1-must-be-at-least-32-bytes!", 2*time.Hour, 7*24*time.Hour)
	if err != nil {
		t.Fatalf("创建 JWT 管理器1失败: %v", err)
	}
	mgr2, err := user.NewJWTManager("secret-2-must-be-at-least-32-bytes!", 2*time.Hour, 7*24*time.Hour)
	if err != nil {
		t.Fatalf("创建 JWT 管理器2失败: %v", err)
	}

	pair, _ := mgr1.GenerateTokenPair(1, "uuid", "user")
	_, err = mgr2.ValidateToken(pair.AccessToken)
	if err == nil {
		t.Fatal("不同密钥签名的 token 验证应失败")
	}
}
