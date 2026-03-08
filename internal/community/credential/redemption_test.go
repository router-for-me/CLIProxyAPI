package credential_test

import (
	"context"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/community/credential"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/community/quota"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/db"
)

// ============================================================
// 兑换码服务测试
// ============================================================

// ------------------------------------------------------------
// 测试辅助 — 创建内存数据库 + 迁移 + 构造服务
// ------------------------------------------------------------

func newTestRedemptionService(t *testing.T) (*credential.RedemptionService, db.Store) {
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

	engine := quota.NewEngine(store)
	svc := credential.NewRedemptionService(store, engine, store)
	return svc, store
}

// seedUser 向数据库插入一个测试用户，返回其 ID
func seedUser(t *testing.T, store db.Store, username, email string) int64 {
	t.Helper()
	ctx := context.Background()

	user := &db.User{
		UUID:     "uuid-" + username,
		Username: username,
		Email:    email,
		Role:     db.RoleUser,
		Status:   db.StatusActive,
		APIKey:   "key-" + username,
		PoolMode: db.PoolPublic,
	}
	if err := store.CreateUser(ctx, user); err != nil {
		t.Fatalf("创建测试用户 %s 失败: %v", username, err)
	}
	return user.ID
}

// ============================================================
// 生成测试
// ============================================================

func TestGenerateCodes_Success(t *testing.T) {
	svc, _ := newTestRedemptionService(t)
	ctx := context.Background()

	codes, err := svc.GenerateCodes(ctx, 1, 5, credential.CodeGenConfig{
		MaxUses:  10,
		ExpiresIn: 24 * time.Hour,
	})
	if err != nil {
		t.Fatalf("批量生成失败: %v", err)
	}
	if len(codes) != 5 {
		t.Fatalf("期望生成 5 个码，实际: %d", len(codes))
	}

	// 检查码唯一性
	seen := make(map[string]bool, len(codes))
	for _, c := range codes {
		if seen[c] {
			t.Fatalf("生成了重复的码: %s", c)
		}
		seen[c] = true
	}
}

func TestGenerateCodes_InvalidCount(t *testing.T) {
	svc, _ := newTestRedemptionService(t)
	ctx := context.Background()

	_, err := svc.GenerateCodes(ctx, 1, 0, credential.CodeGenConfig{MaxUses: 1})
	if err == nil {
		t.Fatal("count=0 时应该返回错误")
	}

	_, err = svc.GenerateCodes(ctx, 1, 501, credential.CodeGenConfig{MaxUses: 1})
	if err == nil {
		t.Fatal("count=501 时应该返回错误")
	}
}

// ============================================================
// 兑换测试
// ============================================================

func TestRedeem_Success(t *testing.T) {
	svc, store := newTestRedemptionService(t)
	ctx := context.Background()

	userID := seedUser(t, store, "alice", "alice@example.com")

	codes, err := svc.GenerateCodes(ctx, 1, 1, credential.CodeGenConfig{
		MaxUses: 3,
		BonusQuota: &db.QuotaGrant{
			ModelPattern: "claude-*",
			Requests:     100,
			QuotaType:    db.QuotaCount,
		},
	})
	if err != nil {
		t.Fatalf("生成码失败: %v", err)
	}

	// 正常兑换
	if err := svc.Redeem(ctx, userID, codes[0]); err != nil {
		t.Fatalf("兑换失败: %v", err)
	}
}

func TestRedeem_Exhausted(t *testing.T) {
	svc, store := newTestRedemptionService(t)
	ctx := context.Background()

	user1 := seedUser(t, store, "bob", "bob@example.com")
	user2 := seedUser(t, store, "charlie", "charlie@example.com")

	// 生成一次性码
	codes, _ := svc.GenerateCodes(ctx, 1, 1, credential.CodeGenConfig{MaxUses: 1})

	// 第一次兑换成功
	if err := svc.Redeem(ctx, user1, codes[0]); err != nil {
		t.Fatalf("首次兑换失败: %v", err)
	}

	// 第二次兑换应失败（已耗尽）
	if err := svc.Redeem(ctx, user2, codes[0]); err == nil {
		t.Fatal("码已耗尽时应返回错误")
	}
}

func TestRedeem_Expired(t *testing.T) {
	svc, store := newTestRedemptionService(t)
	ctx := context.Background()

	userID := seedUser(t, store, "dave", "dave@example.com")

	// 生成一个极短有效期的码（1 纳秒，生成后立刻过期）
	codes, _ := svc.GenerateCodes(ctx, 1, 1, credential.CodeGenConfig{
		MaxUses:   1,
		ExpiresIn: time.Nanosecond,
	})

	// 等待过期
	time.Sleep(time.Millisecond)

	if err := svc.Redeem(ctx, userID, codes[0]); err == nil {
		t.Fatal("过期码应返回错误")
	}
}

func TestRedeem_InvalidCode(t *testing.T) {
	svc, store := newTestRedemptionService(t)
	ctx := context.Background()

	userID := seedUser(t, store, "eve", "eve@example.com")

	if err := svc.Redeem(ctx, userID, "nonexistent-code"); err == nil {
		t.Fatal("无效码应返回错误")
	}
}

func TestRedeem_RequireEmail_NoEmail(t *testing.T) {
	svc, store := newTestRedemptionService(t)
	ctx := context.Background()

	// 创建一个没有邮箱的用户
	userID := seedUser(t, store, "noemail", "")

	codes, _ := svc.GenerateCodes(ctx, 1, 1, credential.CodeGenConfig{
		MaxUses:      1,
		RequireEmail: true,
	})

	if err := svc.Redeem(ctx, userID, codes[0]); err == nil {
		t.Fatal("需要邮箱但用户无邮箱时应返回错误")
	}
}
