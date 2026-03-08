package stats

import (
	"context"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/db"
)

// ============================================================
// 请求日志采集器 — 负责将每次请求记录写入存储层
// 所有上游调用方（中间件 / executor）通过 Collector 上报数据
// ============================================================

// Collector 请求日志采集器
type Collector struct {
	store db.StatsStore
}

// NewCollector 创建采集器
func NewCollector(store db.StatsStore) *Collector {
	return &Collector{store: store}
}

// ------------------------------------------------------------
// Record 记录一次请求
// 参数说明:
//   - userID:       发起请求的用户 ID
//   - model:        调用的模型名称
//   - provider:     模型提供商标识
//   - credentialID: 使用的凭证 ID
//   - inputTokens:  输入 token 数
//   - outputTokens: 输出 token 数
//   - latencyMs:    请求延迟（毫秒）
//   - statusCode:   响应 HTTP 状态码
// ------------------------------------------------------------

func (c *Collector) Record(ctx context.Context, userID int64, model, provider, credentialID string,
	inputTokens, outputTokens, latencyMs int64, statusCode int) error {
	return c.store.RecordRequest(ctx, &db.RequestLog{
		UserID:       userID,
		Model:        model,
		Provider:     provider,
		CredentialID: credentialID,
		InputTokens:  inputTokens,
		OutputTokens: outputTokens,
		Latency:      latencyMs,
		StatusCode:   statusCode,
		CreatedAt:    time.Now(),
	})
}
