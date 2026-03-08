package router

import (
	"sync"
)

// ============================================================
//  MetricsCollector — 调度指标采集器
//  聚合 Scheduler 内部的原子指标，生成可序列化的统计快照
//  供管理面板 / Prometheus exporter / 日志等消费
// ============================================================

// CredentialStats 凭证运行时统计快照
// 所有字段为计算后的只读值，可安全序列化
type CredentialStats struct {
	ActiveConns int64   `json:"active_conns"`    // 当前活跃连接数
	TotalReqs   int64   `json:"total_requests"`  // 累计请求数
	ErrorRate   float64 `json:"error_rate"`       // 错误率 [0.0, 1.0]
	AvgLatency  float64 `json:"avg_latency_ms"`   // 平均延迟(ms)
	CircuitOpen bool    `json:"circuit_open"`     // 是否熔断中
}

// MetricsCollector 调度指标采集器
type MetricsCollector struct {
	mu        sync.RWMutex // 保护采集器自身的并发访问
	scheduler *Scheduler   // 关联的调度器实例
}

// NewMetricsCollector 创建指标采集器
func NewMetricsCollector(scheduler *Scheduler) *MetricsCollector {
	return &MetricsCollector{
		scheduler: scheduler,
	}
}

// GetCredentialStats 获取所有凭证的运行时指标快照
// 返回 credentialID -> CredentialStats 的映射
// 每次调用都会从原子变量中读取最新值，保证数据新鲜度
func (m *MetricsCollector) GetCredentialStats() map[string]CredentialStats {
	m.mu.RLock()
	defer m.mu.RUnlock()

	metricsMap := m.scheduler.GetMetrics()
	result := make(map[string]CredentialStats, len(metricsMap))

	for credID, metrics := range metricsMap {
		totalReqs := metrics.TotalReqs.Load()
		totalErrors := metrics.TotalErrors.Load()
		totalLatency := metrics.TotalLatency.Load()

		stats := CredentialStats{
			ActiveConns: metrics.ActiveConns.Load(),
			TotalReqs:   totalReqs,
			CircuitOpen: metrics.CircuitOpen.Load(),
		}

		// 计算错误率和平均延迟（避免除零）
		if totalReqs > 0 {
			stats.ErrorRate = float64(totalErrors) / float64(totalReqs)
			stats.AvgLatency = float64(totalLatency) / float64(totalReqs)
		}

		result[credID] = stats
	}

	return result
}

// GetCredentialStatsByID 获取指定凭证的运行时指标
// 若凭证无记录则返回零值 stats 和 false
func (m *MetricsCollector) GetCredentialStatsByID(credentialID string) (CredentialStats, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	metricsMap := m.scheduler.GetMetrics()
	metrics, ok := metricsMap[credentialID]
	if !ok {
		return CredentialStats{}, false
	}

	totalReqs := metrics.TotalReqs.Load()
	totalErrors := metrics.TotalErrors.Load()
	totalLatency := metrics.TotalLatency.Load()

	stats := CredentialStats{
		ActiveConns: metrics.ActiveConns.Load(),
		TotalReqs:   totalReqs,
		CircuitOpen: metrics.CircuitOpen.Load(),
	}

	if totalReqs > 0 {
		stats.ErrorRate = float64(totalErrors) / float64(totalReqs)
		stats.AvgLatency = float64(totalLatency) / float64(totalReqs)
	}

	return stats, true
}

// Summary 返回聚合摘要（所有凭证的总和/平均值）
// 用于全局健康概览
func (m *MetricsCollector) Summary() OverallStats {
	allStats := m.GetCredentialStats()

	var summary OverallStats
	summary.TotalCredentials = len(allStats)

	var totalLatencySum float64
	var activeCount int

	for _, s := range allStats {
		summary.TotalRequests += s.TotalReqs
		summary.TotalActiveConns += s.ActiveConns

		if s.CircuitOpen {
			summary.CircuitOpenCount++
		}

		if s.TotalReqs > 0 {
			totalLatencySum += s.AvgLatency
			activeCount++
		}
	}

	// 计算全局平均延迟
	if activeCount > 0 {
		summary.AvgLatency = totalLatencySum / float64(activeCount)
	}

	// 计算全局错误率
	if summary.TotalRequests > 0 {
		var totalErrors int64
		for _, s := range allStats {
			totalErrors += int64(s.ErrorRate * float64(s.TotalReqs))
		}
		summary.ErrorRate = float64(totalErrors) / float64(summary.TotalRequests)
	}

	return summary
}

// OverallStats 全局聚合统计
type OverallStats struct {
	TotalCredentials int     `json:"total_credentials"`  // 凭证总数
	TotalRequests    int64   `json:"total_requests"`     // 累计请求总数
	TotalActiveConns int64   `json:"total_active_conns"` // 当前活跃连接总数
	CircuitOpenCount int     `json:"circuit_open_count"` // 熔断中的凭证数
	AvgLatency       float64 `json:"avg_latency_ms"`     // 全局平均延迟(ms)
	ErrorRate        float64 `json:"error_rate"`          // 全局错误率
}
