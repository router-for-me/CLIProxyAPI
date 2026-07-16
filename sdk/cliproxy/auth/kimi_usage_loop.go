package auth

import (
	"context"
	"time"

	log "github.com/sirupsen/logrus"
)

// kimiUsageProbeTimeout 是单个 auth 用量查询的超时，避免某个上游卡住拖慢整轮探针。
const kimiUsageProbeTimeout = 30 * time.Second

// kimiUsageProbeDefaultInterval 是未配置或非法 interval 时的默认周期（5 分钟）。
const kimiUsageProbeDefaultInterval = 5 * time.Minute

// StartKimiUsageProbe 启动后台探针，周期性查询 base_url 为 api.kimi.com/coding 的
// auth 的上游 /v1/usages，在滚动窗口耗尽时按真实 resetTime 冷却该 auth 的所有模型。
// 生命周期由传入的 ctx 控制：ctx 取消即退出。幂等——重复调用会取消上一轮。
func (m *Manager) StartKimiUsageProbe(ctx context.Context, interval time.Duration) {
	if m == nil {
		return
	}
	if interval <= 0 {
		interval = kimiUsageProbeDefaultInterval
	}
	if ctx == nil {
		ctx = context.Background()
	}

	m.usageProbeMu.Lock()
	if m.usageProbeCancel != nil {
		m.usageProbeCancel()
	}
	probeCtx, cancel := context.WithCancel(ctx)
	m.usageProbeCancel = cancel
	m.usageProbeWG.Add(1)
	m.usageProbeMu.Unlock()

	go func() {
		defer m.usageProbeWG.Done()
		log.Infof("kimi usage probe started (interval=%s)", interval)
		m.runKimiUsageProbeOnce(probeCtx)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-probeCtx.Done():
				log.Info("kimi usage probe stopped")
				return
			case <-ticker.C:
				m.runKimiUsageProbeOnce(probeCtx)
			}
		}
	}()
}

// StopKimiUsageProbe 停止探针并等待退出。供 Shutdown 调用。
func (m *Manager) StopKimiUsageProbe() {
	if m == nil {
		return
	}
	m.usageProbeMu.Lock()
	if m.usageProbeCancel != nil {
		m.usageProbeCancel()
		m.usageProbeCancel = nil
	}
	m.usageProbeMu.Unlock()
	m.usageProbeWG.Wait()
}

// runKimiUsageProbeOnce 执行一轮：快照所有 auth，筛出 Kimi 的，逐个查用量并调整冷却。
func (m *Manager) runKimiUsageProbeOnce(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}
	auths := m.snapshotAuths()
	kimiCount := 0
	for i := range auths {
		auth := auths[i]
		if !isKimiUsageAuth(auth) {
			continue
		}
		kimiCount++
		if err := m.probeSingleKimiAuth(ctx, auth); err != nil {
			log.Debugf("kimi usage probe auth=%s: %v", auth.ID, err)
		}
	}
	if kimiCount > 0 {
		log.Debugf("kimi usage probe sweep: %d kimi auth(s) checked", kimiCount)
	}
}

// probeSingleKimiAuth 处理单个 Kimi auth：查用量 → 决定冷却或恢复。
func (m *Manager) probeSingleKimiAuth(ctx context.Context, auth *Auth) error {
	fetchCtx, cancel := context.WithTimeout(ctx, kimiUsageProbeTimeout)
	defer cancel()

	windows, err := m.fetchKimiUsage(fetchCtx, auth)
	if err != nil {
		return err
	}

	now := time.Now()

	if recoverAt, ok := kimiUsageCooldown(windows); ok {
		if _, errSet := m.SetAuthQuotaExceeded(ctx, auth.ID, recoverAt, kimiUsageReason); errSet != nil {
			return errSet
		}
		log.Infof("kimi usage probe auth=%s: quota exhausted, cooled down until %s",
			auth.ID, recoverAt.Format(time.RFC3339))
		return nil
	}

	if kimiUsageFullyAvailable(windows) && hasAuthQuotaExceeded(auth, now) {
		if _, _, errReset := m.ResetQuota(ctx, auth.ID); errReset != nil {
			return errReset
		}
		log.Infof("kimi usage probe auth=%s: quota recovered, cooldown cleared", auth.ID)
	}
	return nil
}
