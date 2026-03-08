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
// 推荐邀请服务测试
// ============================================================

// ------------------------------------------------------------
// 测试辅助
// ------------------------------------------------------------

func newTestReferralService(t *testing.T) (*credential.ReferralService, db.Store) {
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
	svc := credential.NewReferralService(store, store, engine)
	return svc, store
}

// createReferralCode 创建推荐码用于测试
func createReferralCode(t *testing.T, store db.Store, creatorID int64) string {
	t.Helper()
	ctx := context.Background()

	code := &db.InviteCode{
		Code:      "ref-" + time.Now().Format("150405.000"),
		Type:      db.InviteUserReferral,
		CreatorID: creatorID,
		MaxUses:   100,
		UsedCount: 0,
		BonusQuota: &db.QuotaGrant{
			ModelPattern: "claude-*",
			Requests:     50,
			QuotaType:    db.QuotaCount,
		},
		ReferralBonus: &db.QuotaGrant{
			ModelPattern: "claude-*",
			Requests:     20,
			QuotaType:    db.QuotaCount,
		},
		Status:    db.InviteActive,
		CreatedAt: time.Now(),
	}
	if err := store.CreateInviteCode(ctx, code); err != nil {
		t.Fatalf("创建推荐码失败: %v", err)
	}
	return code.Code
}

// ============================================================
// 推荐处理测试
// ============================================================

func TestProcessReferral_Success(t *testing.T) {
	svc, store := newTestReferralService(t)
	ctx := context.Background()

	// 创建邀请人和新用户
	inviterID := seedUser(t, store, "inviter", "inviter@example.com")
	newUserID := seedUser(t, store, "newbie", "newbie@example.com")

	// 创建推荐码
	code := createReferralCode(t, store, inviterID)

	// 处理推荐
	if err := svc.ProcessReferral(ctx, newUserID, code); err != nil {
		t.Fatalf("处理推荐失败: %v", err)
	}
}

func TestProcessReferral_AdminCode_Skipped(t *testing.T) {
	svc, store := newTestReferralService(t)
	ctx := context.Background()

	adminID := seedUser(t, store, "admin", "admin@example.com")
	newUserID := seedUser(t, store, "user", "user@example.com")

	// 创建管理员码（非推荐类型）
	code := &db.InviteCode{
		Code:      "admin-code-001",
		Type:      db.InviteAdminCreated,
		CreatorID: adminID,
		MaxUses:   100,
		Status:    db.InviteActive,
		CreatedAt: time.Now(),
	}
	store.CreateInviteCode(ctx, code)

	// 管理员码应跳过推荐处理（无报错、无奖励）
	if err := svc.ProcessReferral(ctx, newUserID, "admin-code-001"); err != nil {
		t.Fatalf("管理员码应静默跳过，但返回了错误: %v", err)
	}
}

func TestProcessReferral_InvalidCode(t *testing.T) {
	svc, store := newTestReferralService(t)
	ctx := context.Background()

	newUserID := seedUser(t, store, "lonely", "lonely@example.com")

	err := svc.ProcessReferral(ctx, newUserID, "does-not-exist")
	if err == nil {
		t.Fatal("无效码应返回错误")
	}
}

func TestProcessReferral_BannedInviter(t *testing.T) {
	svc, store := newTestReferralService(t)
	ctx := context.Background()

	// 创建一个被封禁的邀请人
	inviterID := seedUser(t, store, "banned-inv", "banned@example.com")
	newUserID := seedUser(t, store, "innocent", "innocent@example.com")

	// 封禁邀请人
	inviter, _ := store.GetUserByID(ctx, inviterID)
	inviter.Status = db.StatusBanned
	store.UpdateUser(ctx, inviter)

	code := createReferralCode(t, store, inviterID)

	err := svc.ProcessReferral(ctx, newUserID, code)
	if err == nil {
		t.Fatal("邀请人被封禁时应返回错误")
	}
}

// ============================================================
// 统计查询测试
// ============================================================

func TestGetReferralStats_Empty(t *testing.T) {
	svc, store := newTestReferralService(t)
	ctx := context.Background()

	userID := seedUser(t, store, "noref", "noref@example.com")

	stats, err := svc.GetReferralStats(ctx, userID)
	if err != nil {
		t.Fatalf("查询统计失败: %v", err)
	}
	if stats.TotalInvitees != 0 {
		t.Fatalf("无推荐记录时邀请人数应为 0，实际: %d", stats.TotalInvitees)
	}
}

func TestGetReferralStats_WithUsage(t *testing.T) {
	svc, store := newTestReferralService(t)
	ctx := context.Background()

	inviterID := seedUser(t, store, "popular", "popular@example.com")

	// 创建推荐码并模拟 2 次使用
	code := &db.InviteCode{
		Code:      "pop-ref-001",
		Type:      db.InviteUserReferral,
		CreatorID: inviterID,
		MaxUses:   100,
		UsedCount: 2, // 已被使用 2 次
		ReferralBonus: &db.QuotaGrant{
			ModelPattern: "claude-*",
			Requests:     20,
			QuotaType:    db.QuotaCount,
		},
		Status:    db.InviteActive,
		CreatedAt: time.Now(),
	}
	store.CreateInviteCode(ctx, code)

	stats, err := svc.GetReferralStats(ctx, inviterID)
	if err != nil {
		t.Fatalf("查询统计失败: %v", err)
	}
	if stats.TotalInvitees != 2 {
		t.Fatalf("期望邀请 2 人，实际: %d", stats.TotalInvitees)
	}
	if stats.TotalBonusReqs != 40 { // 20 * 2
		t.Fatalf("期望总奖励请求数 40，实际: %d", stats.TotalBonusReqs)
	}
}
