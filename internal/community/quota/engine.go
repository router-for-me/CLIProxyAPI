package quota

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/db"
)

// ============================================================
// 额度引擎 — 检查 / 扣减 / 恢复
// ============================================================

// Engine 额度引擎
type Engine struct {
	store db.QuotaStore
}

// NewEngine 创建额度引擎
func NewEngine(store db.QuotaStore) *Engine {
	return &Engine{store: store}
}

// CheckResult 额度检查结果
type CheckResult struct {
	Allowed       bool   `json:"allowed"`
	Reason        string `json:"reason,omitempty"`
	RemainingReqs int64  `json:"remaining_requests,omitempty"`
	RemainingToks int64  `json:"remaining_tokens,omitempty"`
}

// Check 检查用户是否有足够额度调用指定模型
func (e *Engine) Check(ctx context.Context, userID int64, model string) (*CheckResult, error) {
	// 查找匹配的额度配置
	cfg, err := e.findMatchingConfig(ctx, model)
	if err != nil {
		return nil, err
	}
	if cfg == nil {
		// 无配置 = 无限制
		return &CheckResult{Allowed: true}, nil
	}

	// 获取用户额度状态
	quota, err := e.store.GetUserQuota(ctx, userID, cfg.ModelPattern)
	if err != nil {
		return nil, fmt.Errorf("查询用户额度失败: %w", err)
	}

	// 计算剩余额度
	result := &CheckResult{Allowed: true}

	// 检查请求次数
	if cfg.QuotaType == db.QuotaCount || cfg.QuotaType == db.QuotaBoth {
		if cfg.MaxRequests > 0 {
			totalAllowed := cfg.MaxRequests
			if quota != nil {
				totalAllowed += quota.BonusRequests
			}
			used := int64(0)
			if quota != nil {
				used = quota.UsedRequests
			}
			result.RemainingReqs = totalAllowed - used
			if result.RemainingReqs <= 0 {
				result.Allowed = false
				result.Reason = fmt.Sprintf("模型 %s 请求次数已用尽", model)
				return result, nil
			}
		}
	}

	// 检查 token 额度
	if cfg.QuotaType == db.QuotaToken || cfg.QuotaType == db.QuotaBoth {
		if cfg.MaxTokens > 0 {
			totalAllowed := cfg.MaxTokens
			if quota != nil {
				totalAllowed += quota.BonusTokens
			}
			used := int64(0)
			if quota != nil {
				used = quota.UsedTokens
			}
			result.RemainingToks = totalAllowed - used
			if result.RemainingToks <= 0 {
				result.Allowed = false
				result.Reason = fmt.Sprintf("模型 %s token 额度已用尽", model)
				return result, nil
			}
		}
	}

	return result, nil
}

// Deduct 扣减额度（请求完成后调用）
func (e *Engine) Deduct(ctx context.Context, userID int64, model string, requests int64, tokens int64) error {
	cfg, err := e.findMatchingConfig(ctx, model)
	if err != nil {
		return err
	}
	if cfg == nil {
		return nil // 无配置 = 无需扣减
	}
	return e.store.DeductUserQuota(ctx, userID, cfg.ModelPattern, requests, tokens)
}

// GrantBonus 赠送额度（兑换码/邀请奖励）
func (e *Engine) GrantBonus(ctx context.Context, userID int64, grant *db.QuotaGrant) error {
	if grant == nil {
		return nil
	}
	quota, err := e.store.GetUserQuota(ctx, userID, grant.ModelPattern)
	if err != nil {
		return err
	}
	if quota == nil {
		quota = &db.UserQuota{
			UserID:       userID,
			ModelPattern: grant.ModelPattern,
		}
	}
	quota.BonusRequests += grant.Requests
	quota.BonusTokens += grant.Tokens
	return e.store.UpsertUserQuota(ctx, quota)
}

// findMatchingConfig 查找匹配的额度配置（支持通配符）
func (e *Engine) findMatchingConfig(ctx context.Context, model string) (*db.QuotaConfig, error) {
	configs, err := e.store.GetQuotaConfigs(ctx)
	if err != nil {
		return nil, err
	}
	// 精确匹配优先
	for _, cfg := range configs {
		if cfg.ModelPattern == model {
			return cfg, nil
		}
	}
	// 通配符匹配
	for _, cfg := range configs {
		if strings.Contains(cfg.ModelPattern, "*") {
			matched, _ := filepath.Match(cfg.ModelPattern, model)
			if matched {
				return cfg, nil
			}
		}
	}
	return nil, nil
}
