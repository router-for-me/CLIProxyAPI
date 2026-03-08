package credential

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/community/quota"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/db"
)

// ============================================================
// 兑换码服务 — 批量生成 / 校验 / 核销 / 额度赠予
// ============================================================

// CodeGenConfig 兑换码生成配置
type CodeGenConfig struct {
	MaxUses       int             // 单码最大使用次数
	BonusQuota    *db.QuotaGrant  // 使用者获得的额度奖励
	ReferralBonus *db.QuotaGrant  // 邀请者获得的推荐奖励
	ExpiresIn     time.Duration   // 有效期（零值 = 永不过期）
	RequireEmail  bool            // 是否要求邮箱验证
}

// RedemptionService 兑换码核心服务
type RedemptionService struct {
	inviteStore db.InviteCodeStore
	quotaEngine *quota.Engine
	userStore   db.UserStore
}

// NewRedemptionService 创建兑换码服务
func NewRedemptionService(
	inviteStore db.InviteCodeStore,
	quotaEngine *quota.Engine,
	userStore db.UserStore,
) *RedemptionService {
	return &RedemptionService{
		inviteStore: inviteStore,
		quotaEngine: quotaEngine,
		userStore:   userStore,
	}
}

// ============================================================
// 批量生成
// ============================================================

// GenerateCodes 批量生成邀请 / 兑换码
// 每个码绑定相同的 CodeGenConfig，由管理员指定 creatorID
func (s *RedemptionService) GenerateCodes(
	ctx context.Context,
	creatorID int,
	count int,
	cfg CodeGenConfig,
) ([]string, error) {
	// -- 参数校验 --
	if count <= 0 || count > 500 {
		return nil, fmt.Errorf("生成数量必须在 1~500 之间，当前: %d", count)
	}
	if cfg.MaxUses <= 0 {
		return nil, fmt.Errorf("单码最大使用次数必须 > 0")
	}

	// -- 计算过期时间 --
	var expiresAt *time.Time
	if cfg.ExpiresIn > 0 {
		t := time.Now().Add(cfg.ExpiresIn)
		expiresAt = &t
	}

	codes := make([]string, 0, count)
	for i := 0; i < count; i++ {
		code, err := generateSecureCode()
		if err != nil {
			return nil, fmt.Errorf("生成安全随机码失败: %w", err)
		}

		invite := &db.InviteCode{
			Code:          code,
			Type:          db.InviteAdminCreated,
			CreatorID:     int64(creatorID),
			MaxUses:       cfg.MaxUses,
			UsedCount:     0,
			RequireEmail:  cfg.RequireEmail,
			BonusQuota:    cfg.BonusQuota,
			ReferralBonus: cfg.ReferralBonus,
			ExpiresAt:     expiresAt,
			Status:        db.InviteActive,
			CreatedAt:     time.Now(),
		}

		if err := s.inviteStore.CreateInviteCode(ctx, invite); err != nil {
			return nil, fmt.Errorf("存储第 %d 个邀请码失败: %w", i+1, err)
		}
		codes = append(codes, code)
	}
	return codes, nil
}

// ============================================================
// 兑换
// ============================================================

// Redeem 用户兑换码核销
// 流程: 校验码有效性 -> 检查未过期 -> 检查未耗尽 -> 递增使用量 -> 记录使用 -> 赠予额度
func (s *RedemptionService) Redeem(ctx context.Context, userID int64, code string) error {
	// -- 查询邀请码 --
	invite, err := s.inviteStore.GetInviteCodeByCode(ctx, code)
	if err != nil {
		return fmt.Errorf("无效的兑换码: %w", err)
	}

	// -- 校验邮箱 (如果需要) --
	if invite.RequireEmail {
		user, err := s.userStore.GetUserByID(ctx, userID)
		if err != nil {
			return fmt.Errorf("查询用户信息失败: %w", err)
		}
		if user.Email == "" {
			return fmt.Errorf("此兑换码要求用户已绑定邮箱")
		}
	}

	// -- 检查状态 --
	if invite.Status != db.InviteActive {
		return fmt.Errorf("兑换码状态异常: %s", invite.Status)
	}

	// -- 检查过期 --
	if invite.ExpiresAt != nil && time.Now().After(*invite.ExpiresAt) {
		return fmt.Errorf("兑换码已过期")
	}

	// -- 检查同一用户是否已兑换过该码 --
	used, err := s.inviteStore.HasUserUsedCode(ctx, invite.ID, userID)
	if err != nil {
		return fmt.Errorf("检查兑换记录失败: %w", err)
	}
	if used {
		return fmt.Errorf("您已使用过该兑换码")
	}

	// -- 原子递增使用量（同时检查上限，防止并发超发） --
	if err := s.inviteStore.IncrementInviteCodeUsage(ctx, invite.ID); err != nil {
		return fmt.Errorf("兑换码已被使用完毕或更新失败: %w", err)
	}

	// -- 记录使用 --
	usage := &db.InviteCodeUsage{
		CodeID: invite.ID,
		UserID: userID,
		UsedAt: time.Now(),
	}
	if err := s.inviteStore.RecordInviteCodeUsage(ctx, usage); err != nil {
		return fmt.Errorf("记录使用详情失败: %w", err)
	}

	// -- 赠予额度 --
	if invite.BonusQuota != nil {
		if err := s.quotaEngine.GrantBonus(ctx, userID, invite.BonusQuota); err != nil {
			return fmt.Errorf("赠予额度失败: %w", err)
		}
	}

	return nil
}

// ============================================================
// 内部工具
// ============================================================

// generateSecureCode 生成 16 字节 (32 字符) 的加密安全随机兑换码
func generateSecureCode() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}
