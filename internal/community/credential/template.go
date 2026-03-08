package credential

import (
	"context"
	"fmt"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/community/quota"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/db"
)

// ============================================================
// 兑换码模板服务 — 自助领取 / 模板管理
// 管理员预设模板，用户通过模板自助领取兑换码
// ============================================================

// TemplateStore 模板存储接口
// 与 InviteCodeStore 分离，专注模板自身的 CRUD
type TemplateStore interface {
	CreateTemplate(ctx context.Context, tpl *db.RedemptionTemplate) error
	GetTemplateByID(ctx context.Context, id int64) (*db.RedemptionTemplate, error)
	ListTemplates(ctx context.Context) ([]*db.RedemptionTemplate, error)
	IncrementTemplateIssuedCount(ctx context.Context, id int64) error
	DecrementTemplateIssuedCount(ctx context.Context, id int64) error
	CountTemplateClaimsByUser(ctx context.Context, userID, templateID int64) (int, error)
}

// TemplateService 兑换码模板服务
type TemplateService struct {
	templateStore TemplateStore
	inviteStore   db.InviteCodeStore
	quotaEngine   *quota.Engine
}

// NewTemplateService 创建模板服务
func NewTemplateService(
	templateStore TemplateStore,
	inviteStore db.InviteCodeStore,
	quotaEngine *quota.Engine,
) *TemplateService {
	return &TemplateService{
		templateStore: templateStore,
		inviteStore:   inviteStore,
		quotaEngine:   quotaEngine,
	}
}

// ============================================================
// 模板管理
// ============================================================

// CreateTemplate 创建兑换码模板
// 管理员设定名称、描述、额度奖励、每用户上限、总发放上限
func (s *TemplateService) CreateTemplate(
	ctx context.Context,
	name, desc string,
	bonusQuota db.QuotaGrant,
	maxPerUser, totalLimit int,
) error {
	// -- 参数校验 --
	if name == "" {
		return fmt.Errorf("模板名称不能为空")
	}
	if maxPerUser <= 0 {
		return fmt.Errorf("每用户领取上限必须 > 0")
	}
	if totalLimit <= 0 {
		return fmt.Errorf("总发放上限必须 > 0")
	}

	tpl := &db.RedemptionTemplate{
		Name:        name,
		Description: desc,
		BonusQuota:  bonusQuota,
		MaxPerUser:  maxPerUser,
		TotalLimit:  totalLimit,
		IssuedCount: 0,
		Enabled:     true,
		CreatedAt:   time.Now(),
	}

	if err := s.templateStore.CreateTemplate(ctx, tpl); err != nil {
		return fmt.Errorf("创建模板失败: %w", err)
	}
	return nil
}

// ListTemplates 列出所有可用模板
func (s *TemplateService) ListTemplates(ctx context.Context) ([]*db.RedemptionTemplate, error) {
	templates, err := s.templateStore.ListTemplates(ctx)
	if err != nil {
		return nil, fmt.Errorf("查询模板列表失败: %w", err)
	}
	return templates, nil
}

// ============================================================
// 自助领取
// ============================================================

// ClaimTemplate 用户从模板中领取兑换码
// 流程: 校验模板存在且启用 -> 检查总量上限 -> 检查用户领取上限 -> 自动生成码 -> 赠予额度
func (s *TemplateService) ClaimTemplate(
	ctx context.Context,
	userID, templateID int64,
) (string, error) {
	// -- 查询模板 --
	tpl, err := s.templateStore.GetTemplateByID(ctx, templateID)
	if err != nil {
		return "", fmt.Errorf("模板不存在: %w", err)
	}

	// -- 检查启用状态 --
	if !tpl.Enabled {
		return "", fmt.Errorf("该模板已停用")
	}

	// -- 检查用户领取上限 --
	claimed, err := s.templateStore.CountTemplateClaimsByUser(ctx, userID, templateID)
	if err != nil {
		return "", fmt.Errorf("查询用户领取记录失败: %w", err)
	}
	if claimed >= tpl.MaxPerUser {
		return "", fmt.Errorf("已达到该模板的领取上限 (%d/%d)", claimed, tpl.MaxPerUser)
	}

	// -- 原子递增模板已发放数（同时检查上限，防止并发超发） --
	if err := s.templateStore.IncrementTemplateIssuedCount(ctx, templateID); err != nil {
		return "", fmt.Errorf("模板兑换码已全部发放完毕: %w", err)
	}

	// -- 后续步骤失败时补偿回滚 issued_count --
	rollback := true
	defer func() {
		if rollback {
			_ = s.templateStore.DecrementTemplateIssuedCount(ctx, templateID)
		}
	}()

	// -- 生成兑换码 --
	code, err := generateSecureCode()
	if err != nil {
		return "", fmt.Errorf("生成码失败: %w", err)
	}

	invite := &db.InviteCode{
		Code:      code,
		Type:      db.InviteAdminCreated,
		CreatorID: userID,
		MaxUses:   1,
		UsedCount: 1, // 标记为已使用（额度已直接赠予）
		Status:    db.InviteActive,
		CreatedAt: time.Now(),
	}
	if err := s.inviteStore.CreateInviteCode(ctx, invite); err != nil {
		return "", fmt.Errorf("存储兑换码失败: %w", err)
	}

	// -- 直接赠予额度（不在邀请码上设 BonusQuota，防止二次兑换） --
	if err := s.quotaEngine.GrantBonus(ctx, userID, &tpl.BonusQuota); err != nil {
		return "", fmt.Errorf("赠予额度失败: %w", err)
	}

	rollback = false // 全部成功，取消回滚
	return code, nil
}
