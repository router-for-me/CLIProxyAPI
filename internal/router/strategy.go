package router

import (
	"math/rand"
	"sync/atomic"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/db"
)

// ============================================================
//  调度策略接口与实现
//  - WeightedRoundRobin : 加权轮询
//  - LeastLoad          : 最少连接
//  - FillFirst          : 填充优先（稳定排序）
//  - RandomStrategy     : 随机选择
// ============================================================

// Strategy 调度策略接口
// 所有策略实现应为并发安全，Select 中不得持有长锁
type Strategy interface {
	// Select 从候选列表中选出最优凭证，无可用候选时返回 nil
	Select(candidates []*Candidate) *Candidate
	// Name 返回策略的可读名称，用于日志与指标上报
	Name() string
}

// Candidate 候选凭证（带运行时指标）
// 由调度器在每次选择前构建，策略实现仅做只读访问
type Candidate struct {
	Credential  *db.Credential // 底层凭证实体
	ActiveConns int64          // 当前活跃连接数
	AvgLatency  float64        // 平均延迟(ms)
	SuccessRate float64        // 成功率 [0.0, 1.0]
	Weight      int            // 静态权重（源自凭证配置）
	CircuitOpen bool           // 熔断器是否打开
}

// filterHealthy 过滤掉熔断中的候选凭证
// 所有策略共用此辅助函数，保证行为一致
func filterHealthy(candidates []*Candidate) []*Candidate {
	healthy := make([]*Candidate, 0, len(candidates))
	for _, c := range candidates {
		if !c.CircuitOpen {
			healthy = append(healthy, c)
		}
	}
	return healthy
}

// ============================================================
//  WeightedRoundRobin — 加权轮询
//  按权重比例依次分配请求；跳过熔断中的凭证
// ============================================================

// WeightedRoundRobin 加权轮询策略
type WeightedRoundRobin struct {
	index atomic.Int64 // 全局游标，原子递增保证并发安全
}

// NewWeightedRoundRobin 创建加权轮询策略
func NewWeightedRoundRobin() *WeightedRoundRobin {
	return &WeightedRoundRobin{}
}

func (w *WeightedRoundRobin) Name() string { return "weighted_round_robin" }

func (w *WeightedRoundRobin) Select(candidates []*Candidate) *Candidate {
	healthy := filterHealthy(candidates)
	if len(healthy) == 0 {
		return nil
	}

	// ---- 构建权重展开表 ----
	// 每个候选按其 Weight 值重复出现在展开表中
	// 权重为 0 的候选至少出现 1 次（保底）
	expanded := make([]*Candidate, 0, len(healthy)*2)
	for _, c := range healthy {
		w := c.Weight
		if w <= 0 {
			w = 1
		}
		for i := 0; i < w; i++ {
			expanded = append(expanded, c)
		}
	}
	if len(expanded) == 0 {
		return nil
	}

	// 原子递增游标并取模
	idx := w.index.Add(1) - 1
	return expanded[idx%int64(len(expanded))]
}

// ============================================================
//  LeastLoad — 最少连接
//  选择当前活跃连接数最低的候选凭证
// ============================================================

// LeastLoad 最少连接策略
type LeastLoad struct{}

// NewLeastLoad 创建最少连接策略
func NewLeastLoad() *LeastLoad {
	return &LeastLoad{}
}

func (l *LeastLoad) Name() string { return "least_load" }

func (l *LeastLoad) Select(candidates []*Candidate) *Candidate {
	healthy := filterHealthy(candidates)
	if len(healthy) == 0 {
		return nil
	}

	best := healthy[0]
	for _, c := range healthy[1:] {
		if c.ActiveConns < best.ActiveConns {
			best = c
		}
	}
	return best
}

// ============================================================
//  FillFirst — 填充优先（稳定排序）
//  始终选择列表中第一个非熔断候选，适合固定优先级场景
// ============================================================

// FillFirst 填充优先策略
type FillFirst struct{}

// NewFillFirst 创建填充优先策略
func NewFillFirst() *FillFirst {
	return &FillFirst{}
}

func (f *FillFirst) Name() string { return "fill_first" }

func (f *FillFirst) Select(candidates []*Candidate) *Candidate {
	// 直接遍历原始列表，保持调用方提供的顺序
	for _, c := range candidates {
		if !c.CircuitOpen {
			return c
		}
	}
	return nil
}

// ============================================================
//  RandomStrategy — 随机选择
//  从非熔断候选中随机挑选，用于分散流量
// ============================================================

// RandomStrategy 随机选择策略
type RandomStrategy struct{}

// NewRandomStrategy 创建随机选择策略
func NewRandomStrategy() *RandomStrategy {
	return &RandomStrategy{}
}

func (r *RandomStrategy) Name() string { return "random" }

func (r *RandomStrategy) Select(candidates []*Candidate) *Candidate {
	healthy := filterHealthy(candidates)
	if len(healthy) == 0 {
		return nil
	}
	return healthy[rand.Intn(len(healthy))]
}
