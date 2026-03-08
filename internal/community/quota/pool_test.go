package quota_test

import (
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/community/quota"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/db"
)

func TestPoolManager_PublicMode(t *testing.T) {
	ctx := context.Background()
	store, err := db.NewSQLiteStore(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	store.Migrate(ctx)
	defer store.Close()

	// 创建用户（公共池模式）
	user := &db.User{
		UUID: "test-uuid", Username: "testuser", APIKey: "cpk-test",
		Role: db.RoleUser, Status: db.StatusActive, PoolMode: db.PoolPublic,
		InviteCode: "abc123",
	}
	store.CreateUser(ctx, user)

	// 添加公共凭证
	store.CreateCredential(ctx, &db.Credential{
		ID: "pub-1", Provider: "claude", Health: db.HealthHealthy, Weight: 1, Enabled: true,
	})

	pm := quota.NewPoolManager(store, store)
	creds, err := pm.GetAvailableCredentials(ctx, user.ID, "claude")
	if err != nil {
		t.Fatalf("获取凭证失败: %v", err)
	}
	if len(creds) != 1 {
		t.Fatalf("期望 1 个凭证, got %d", len(creds))
	}
}
