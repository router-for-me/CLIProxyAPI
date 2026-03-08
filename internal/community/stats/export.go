package stats

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/db"
)

// ============================================================
// 数据导出服务 — 支持 CSV / JSON 两种格式
// 从 StatsStore 读取统计数据，序列化后写入任意 io.Writer
// ============================================================

// Exporter 数据导出服务
type Exporter struct {
	store db.StatsStore
}

// NewExporter 创建数据导出服务
func NewExporter(store db.StatsStore) *Exporter {
	return &Exporter{store: store}
}

// ------------------------------------------------------------
// ExportCSV 导出 CSV 格式统计数据
// 输出结构:
//   行 1 — 表头: dimension, key, value
//   行 N — 数据行: summary/by_model/by_provider, 指标名/模型名, 数值
//
// after / before 均为可选时间区间边界
// ------------------------------------------------------------

func (e *Exporter) ExportCSV(ctx context.Context, w io.Writer, after, before *time.Time) error {
	// ---- 查询全局统计 ----
	result, err := e.store.GetRequestStats(ctx, db.RequestStatsOpts{
		After:  after,
		Before: before,
	})
	if err != nil {
		return fmt.Errorf("查询统计数据失败: %w", err)
	}

	// ---- 写入 CSV ----
	cw := csv.NewWriter(w)
	defer cw.Flush()

	// 表头
	if err := cw.Write([]string{"dimension", "key", "value"}); err != nil {
		return fmt.Errorf("写入 CSV 表头失败: %w", err)
	}

	// 汇总行
	summaryRows := [][]string{
		{"summary", "total_requests", strconv.FormatInt(result.TotalRequests, 10)},
		{"summary", "total_tokens", strconv.FormatInt(result.TotalTokens, 10)},
		{"summary", "avg_latency_ms", strconv.FormatFloat(result.AvgLatency, 'f', 2, 64)},
	}
	for _, row := range summaryRows {
		if err := cw.Write(row); err != nil {
			return fmt.Errorf("写入 CSV 汇总行失败: %w", err)
		}
	}

	// 按模型分组
	for model, count := range result.ByModel {
		if err := cw.Write([]string{"by_model", model, strconv.FormatInt(count, 10)}); err != nil {
			return fmt.Errorf("写入 CSV 模型行失败: %w", err)
		}
	}

	// 按提供商分组
	for provider, count := range result.ByProvider {
		if err := cw.Write([]string{"by_provider", provider, strconv.FormatInt(count, 10)}); err != nil {
			return fmt.Errorf("写入 CSV 提供商行失败: %w", err)
		}
	}

	return nil
}

// ------------------------------------------------------------
// ExportJSON 导出 JSON 格式统计数据
// 直接将 RequestStats 序列化为 JSON 写入 writer
// 输出缩进格式，便于人工审查
// ------------------------------------------------------------

func (e *Exporter) ExportJSON(ctx context.Context, w io.Writer, after, before *time.Time) error {
	// ---- 查询全局统计 ----
	result, err := e.store.GetRequestStats(ctx, db.RequestStatsOpts{
		After:  after,
		Before: before,
	})
	if err != nil {
		return fmt.Errorf("查询统计数据失败: %w", err)
	}

	// ---- JSON 序列化 ----
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(result); err != nil {
		return fmt.Errorf("JSON 序列化失败: %w", err)
	}

	return nil
}
