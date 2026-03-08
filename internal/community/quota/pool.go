package quota

import (
	"context"
	"fmt"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/db"
)

// ============================================================
// 凭证池管理器 — 独立池 / 公共池 / 贡献者池
// ============================================================

// PoolManager 凭证池管理器
type PoolManager struct {
	credStore db.CredentialStore
	userStore db.UserStore
}

// NewPoolManager 创建池管理器
func NewPoolManager(credStore db.CredentialStore, userStore db.UserStore) *PoolManager {
	return &PoolManager{credStore: credStore, userStore: userStore}
}

// GetAvailableCredentials 根据用户池模式获取可用凭证
func (pm *PoolManager) GetAvailableCredentials(ctx context.Context, userID int64, provider string) ([]*db.Credential, error) {
	user, err := pm.userStore.GetUserByID(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("查询用户失败: %w", err)
	}

	switch user.PoolMode {
	case db.PoolPrivate:
		return pm.credStore.GetUserCredentials(ctx, userID, provider)

	case db.PoolPublic:
		return pm.credStore.GetPublicPoolCredentials(ctx, provider)

	case db.PoolContributor:
		// 检查用户是否贡献过凭证
		userCreds, err := pm.credStore.GetUserCredentials(ctx, userID, "")
		if err != nil {
			return nil, err
		}
		if len(userCreds) == 0 {
			return nil, fmt.Errorf("贡献者模式: 请先上传凭证才能使用公共池")
		}
		// 合并公共池和用户私有池
		publicCreds, err := pm.credStore.GetPublicPoolCredentials(ctx, provider)
		if err != nil {
			return nil, err
		}
		privateCreds, err := pm.credStore.GetUserCredentials(ctx, userID, provider)
		if err != nil {
			return nil, err
		}
		return append(publicCreds, privateCreds...), nil

	default:
		return nil, fmt.Errorf("未知的池模式: %s", user.PoolMode)
	}
}

// IsContributor 检查用户是否为凭证贡献者
func (pm *PoolManager) IsContributor(ctx context.Context, userID int64) (bool, error) {
	creds, err := pm.credStore.GetUserCredentials(ctx, userID, "")
	if err != nil {
		return false, err
	}
	return len(creds) > 0, nil
}
