package credential

import (
	"context"
	"fmt"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/community/quota"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/db"
)

// ============================================================
// 推荐邀请服务 — 双向奖励 / 统计查询
// 新用户通过邀请码注册时，自动为双方发放额度奖励
// ============================================================

// ReferralStats 推荐统计
type ReferralStats struct {
	TotalInvitees    int64 `json:"total_invitees"`     // 成功邀请的用户总数
	TotalBonusReqs   int64 `json:"total_bonus_reqs"`   // 推荐奖励获得的总请求次数
	TotalBonusTokens int64 `json:"total_bonus_tokens"` // 推荐奖励获得的总 token 数
}

// ReferralService 推荐邀请服务
type ReferralService struct {
	inviteStore db.InviteCodeStore
	userStore   db.UserStore
	quotaEngine *quota.Engine
}

// NewReferralService 创建推荐邀请服务
func NewReferralService(
	inviteStore db.InviteCodeStore,
	userStore db.UserStore,
	quotaEngine *quota.Engine,
) *ReferralService {
	return &ReferralService{
		inviteStore: inviteStore,
		userStore:   userStore,
		quotaEngine: quotaEngine,
	}
}

// ============================================================
// 推荐处理
// ============================================================

// ProcessReferral 处理推荐关系
// 流程: 查找邀请码 -> 确定邀请人 -> 为新用户赠予额度 -> 为邀请人赠予推荐奖励
func (s *ReferralService) ProcessReferral(
	ctx context.Context,
	newUserID int64,
	inviteCode string,
) error {
	// -- 查询邀请码 --
	invite, err := s.inviteStore.GetInviteCodeByCode(ctx, inviteCode)
	if err != nil {
		return fmt.Errorf("查找邀请码失败: %w", err)
	}

	// -- 只处理用户推荐类型的码 (管理员码走 Redeem 流程) --
	if invite.Type != db.InviteUserReferral {
		return nil
	}

	// -- 确定邀请人 --
	inviterID := invite.CreatorID

	// -- 验证邀请人存在 --
	inviter, err := s.userStore.GetUserByID(ctx, inviterID)
	if err != nil {
		return fmt.Errorf("查询邀请人信息失败: %w", err)
	}
	if inviter.Status != db.StatusActive {
		return fmt.Errorf("邀请人账号状态异常: %s", inviter.Status)
	}

	// -- 为新用户赠予额度 (如果配置了 BonusQuota) --
	if invite.BonusQuota != nil {
		if err := s.quotaEngine.GrantBonus(ctx, newUserID, invite.BonusQuota); err != nil {
			return fmt.Errorf("为新用户赠予额度失败: %w", err)
		}
	}

	// -- 为邀请人赠予推荐奖励 (如果配置了 ReferralBonus) --
	if invite.ReferralBonus != nil {
		if err := s.quotaEngine.GrantBonus(ctx, inviterID, invite.ReferralBonus); err != nil {
			return fmt.Errorf("为邀请人赠予推荐奖励失败: %w", err)
		}
	}

	return nil
}

// ============================================================
// 统计查询
// ============================================================

// GetReferralStats 查询用户的推荐统计
// 通过邀请码使用记录统计成功邀请人数和累计奖励额度
func (s *ReferralService) GetReferralStats(
	ctx context.Context,
	userID int64,
) (*ReferralStats, error) {
	stats := &ReferralStats{}

	// -- 查询该用户创建的所有推荐邀请码（按 CreatorID 过滤，分页遍历全部） --
	typeStr := string(db.InviteUserReferral)
	creatorID := userID
	page := 1
	const pageSize = 100

	for {
		codes, total, err := s.inviteStore.ListInviteCodes(ctx, db.ListInviteCodesOpts{
			Page:      page,
			PageSize:  pageSize,
			Type:      &typeStr,
			CreatorID: &creatorID,
		})
		if err != nil {
			return nil, fmt.Errorf("查询邀请码列表失败: %w", err)
		}

		for _, code := range codes {
			stats.TotalInvitees += int64(code.UsedCount)
			if code.ReferralBonus != nil {
				stats.TotalBonusReqs += code.ReferralBonus.Requests * int64(code.UsedCount)
				stats.TotalBonusTokens += code.ReferralBonus.Tokens * int64(code.UsedCount)
			}
		}

		// 所有页已遍历完毕
		if int64(page*pageSize) >= total {
			break
		}
		page++
	}

	return stats, nil
}
