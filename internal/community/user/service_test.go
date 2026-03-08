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
