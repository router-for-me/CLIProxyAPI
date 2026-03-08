// ============================================================
// 轮询引擎仪表盘 — 策略选择 / 凭证指标 / 熔断状态 / 健康检查
// ============================================================

import { useState, useEffect, useCallback } from 'react'
import {
  getRouterConfig,
  updateRouterStrategy,
  updateCredentialWeight,
  updateHealthCheckerSettings,
  applyRouterChanges,
  type RoutingStrategy,
  type CredentialMetrics,
  type HealthCheckerSettings,
  type RouterConfig,
} from '../../api/admin'

// ============================================================
// 常量 — 策略描述
// ============================================================

const STRATEGY_META: Record<RoutingStrategy, { label: string; desc: string }> = {
  WeightedRoundRobin: {
    label: '加权轮询 (Weighted Round Robin)',
    desc: '根据凭证权重按比例分配请求，权重越高分配越多',
  },
  LeastLoad: {
    label: '最小负载 (Least Load)',
    desc: '优先选择当前活跃连接数最少的凭证，均衡负载',
  },
  FillFirst: {
    label: '填满优先 (Fill First)',
    desc: '优先填满高优先级凭证，满载后切换到下一个',
  },
  Random: {
    label: '随机 (Random)',
    desc: '完全随机选择可用凭证，最简单的分配策略',
  },
}

const STRATEGIES = Object.keys(STRATEGY_META) as RoutingStrategy[]

// ============================================================
// 常量 — 熔断状态样式
// ============================================================

type CircuitState = CredentialMetrics['circuit_state']

const CIRCUIT_STYLES: Record<CircuitState, { bg: string; text: string; dot: string; label: string }> = {
  closed:    { bg: 'bg-emerald-50',  text: 'text-emerald-700', dot: 'bg-[#10B981]', label: 'Closed' },
  open:      { bg: 'bg-red-50',      text: 'text-red-700',     dot: 'bg-[#EF4444]', label: 'Open' },
  half_open: { bg: 'bg-amber-50',    text: 'text-amber-700',   dot: 'bg-[#F59E0B]', label: 'HalfOpen' },
}

// ============================================================
// 子组件: 熔断状态指示器
// ============================================================

function CircuitBadge({ state }: { state: CircuitState }) {
  const s = CIRCUIT_STYLES[state]
  return (
    <span className={`inline-flex items-center gap-1.5 rounded-full px-2.5 py-0.5 text-xs font-medium ${s.bg} ${s.text}`}>
      <span className={`h-1.5 w-1.5 rounded-full ${s.dot}`} />
      {s.label}
    </span>
  )
}

// ============================================================
// 子组件: 权重滑块
// ============================================================

function WeightSlider({
  credentialId,
  value,
  onChange,
  onCommit,
}: {
  credentialId: number
  value: number
  onChange: (id: number, w: number) => void
  onCommit: (id: number, w: number) => void
}) {
  return (
    <div className="flex items-center gap-3">
      <input
        type="range"
        min={0}
        max={100}
        value={value}
        onChange={(e) => onChange(credentialId, Number(e.target.value))}
        onMouseUp={() => onCommit(credentialId, value)}
        onTouchEnd={() => onCommit(credentialId, value)}
        className="h-1.5 w-24 cursor-pointer appearance-none rounded-full bg-gray-200 accent-[#3B82F6]"
      />
      <span className="w-8 text-right text-xs font-medium text-gray-700">{value}</span>
    </div>
  )
}

// ============================================================
// 通用样式
// ============================================================

const INPUT_CLS = 'w-full rounded-lg border border-gray-300 px-3 py-2 text-sm transition focus:border-[#3B82F6] focus:outline-none focus:ring-2 focus:ring-[#3B82F6]/20'

// ============================================================
// 主组件: RouterEngine
// ============================================================

export default function RouterEngine() {
  /* ---- 状态 ---- */
  const [config, setConfig] = useState<RouterConfig | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [successMsg, setSuccessMsg] = useState('')
  const [applying, setApplying] = useState(false)

  // 本地编辑态 — 与远端快照比对检测变更
  const [strategy, setStrategy] = useState<RoutingStrategy>('WeightedRoundRobin')
  const [credentials, setCredentials] = useState<CredentialMetrics[]>([])
  const [health, setHealth] = useState<HealthCheckerSettings>({
    check_interval_sec: 30,
    failure_threshold: 3,
    recovery_timeout_sec: 60,
  })

  // ============================================================
  // 数据加载
  // ============================================================

  const loadConfig = useCallback(async () => {
    try {
      setLoading(true)
      const res = await getRouterConfig()
      const d = res.data
      setConfig(d)
      setStrategy(d.strategy)
      setCredentials(d.credentials)
      setHealth(d.health)
      setError('')
    } catch (err) {
      setError(err instanceof Error ? err.message : '加载路由引擎配置失败')
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => { loadConfig() }, [loadConfig])

  // 成功消息自动消失
  useEffect(() => {
    if (!successMsg) return
    const t = setTimeout(() => setSuccessMsg(''), 3000)
    return () => clearTimeout(t)
  }, [successMsg])

  // ============================================================
  // 事件处理
  // ============================================================

  const handleStrategyChange = async (s: RoutingStrategy) => {
    setStrategy(s)
    try { await updateRouterStrategy(s) }
    catch (err) { setError(err instanceof Error ? err.message : '更新策略失败') }
  }

  const handleWeightChange = (id: number, weight: number) => {
    setCredentials((prev) =>
      prev.map((c) => (c.credential_id === id ? { ...c, weight } : c)),
    )
  }

  const handleWeightCommit = async (id: number, weight: number) => {
    try { await updateCredentialWeight(id, weight) }
    catch (err) { setError(err instanceof Error ? err.message : '更新权重失败') }
  }

  const handleHealthChange = <K extends keyof HealthCheckerSettings>(
    key: K, value: HealthCheckerSettings[K],
  ) => {
    setHealth((prev) => ({ ...prev, [key]: value }))
  }

  const handleApply = async () => {
    setApplying(true); setError('')
    try {
      await updateHealthCheckerSettings(health)
      await applyRouterChanges()
      setSuccessMsg('配置已应用到路由引擎')
      await loadConfig()
    } catch (err) {
      setError(err instanceof Error ? err.message : '应用配置失败')
    } finally {
      setApplying(false)
    }
  }

  // ============================================================
  // 变更检测
  // ============================================================

  const hasChanges =
    config !== null &&
    (strategy !== config.strategy ||
      JSON.stringify(health) !== JSON.stringify(config.health) ||
      JSON.stringify(credentials.map((c) => c.weight)) !==
        JSON.stringify(config.credentials.map((c) => c.weight)))

  // ============================================================
  // 渲染
  // ============================================================

  if (loading) {
    return (
      <div className="flex min-h-screen items-center justify-center bg-[#F8FAFC]">
        <div className="h-8 w-8 animate-spin rounded-full border-4 border-gray-200 border-t-[#3B82F6]" />
      </div>
    )
  }

  return (
    <div className="min-h-screen bg-[#F8FAFC] p-6">
      {/* ---- 页头 ---- */}
      <div className="mb-6 flex items-start justify-between">
        <div>
          <h1 className="text-2xl font-bold text-gray-900">轮询引擎</h1>
          <p className="mt-1 text-sm text-gray-500">配置凭证调度策略、权重和健康检查参数</p>
        </div>
        <button
          type="button"
          disabled={applying || !hasChanges}
          onClick={handleApply}
          className={`rounded-lg px-5 py-2.5 text-sm font-medium text-white transition ${
            hasChanges ? 'bg-[#3B82F6] hover:bg-blue-600 active:scale-95' : 'cursor-not-allowed bg-gray-300'
          } disabled:opacity-50`}
        >
          {applying ? '应用中...' : '应用变更'}
        </button>
      </div>

      {/* ---- 消息 ---- */}
      {error && (
        <div className="mb-4 rounded-xl border border-red-200 bg-red-50 px-4 py-3 text-sm text-[#EF4444]">
          {error}
          <button type="button" onClick={() => setError('')} className="ml-2 font-medium underline">关闭</button>
        </div>
      )}
      {successMsg && (
        <div className="mb-4 rounded-xl border border-emerald-200 bg-emerald-50 px-4 py-3 text-sm text-[#10B981]">
          {successMsg}
        </div>
      )}

      <div className="space-y-6">
        {/* ============================================================
         *  调度策略
         * ============================================================ */}
        <div className="rounded-xl border border-gray-200 bg-white shadow-sm">
          <div className="border-b border-gray-200 px-6 py-4">
            <h2 className="text-lg font-semibold text-gray-900">调度策略</h2>
          </div>
          <div className="p-6">
            <div className="mb-3">
              <label htmlFor="strategy" className="mb-1 block text-sm font-medium text-gray-700">当前策略</label>
              <select
                id="strategy"
                value={strategy}
                onChange={(e) => handleStrategyChange(e.target.value as RoutingStrategy)}
                className={`${INPUT_CLS} max-w-md`}
              >
                {STRATEGIES.map((s) => (
                  <option key={s} value={s}>{STRATEGY_META[s].label}</option>
                ))}
              </select>
            </div>
            <div className="rounded-lg border border-blue-100 bg-blue-50/50 px-4 py-3">
              <p className="text-sm text-blue-700">{STRATEGY_META[strategy].desc}</p>
            </div>
          </div>
        </div>

        {/* ============================================================
         *  凭证指标表
         * ============================================================ */}
        <div className="rounded-xl border border-gray-200 bg-white shadow-sm">
          <div className="border-b border-gray-200 px-6 py-4">
            <h2 className="text-lg font-semibold text-gray-900">凭证指标</h2>
          </div>

          {credentials.length === 0 ? (
            <div className="py-12 text-center text-sm text-gray-500">暂无凭证数据</div>
          ) : (
            <div className="overflow-x-auto">
              <table className="w-full text-left text-sm">
                <thead>
                  <tr className="border-b border-gray-100 bg-gray-50/50">
                    <th className="px-6 py-3 font-medium text-gray-500">凭证 ID</th>
                    <th className="px-6 py-3 font-medium text-gray-500">提供商</th>
                    <th className="px-6 py-3 font-medium text-gray-500">总请求</th>
                    <th className="px-6 py-3 font-medium text-gray-500">成功率</th>
                    <th className="px-6 py-3 font-medium text-gray-500">平均延迟</th>
                    <th className="px-6 py-3 font-medium text-gray-500">权重</th>
                    <th className="px-6 py-3 font-medium text-gray-500">熔断状态</th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-gray-100">
                  {credentials.map((cred) => {
                    const rateColor =
                      cred.success_rate >= 95 ? 'text-[#10B981]'
                        : cred.success_rate >= 80 ? 'text-[#F59E0B]'
                          : 'text-[#EF4444]'

                    return (
                      <tr key={cred.credential_id} className="transition hover:bg-gray-50/50">
                        <td className="px-6 py-3">
                          <code className="rounded bg-gray-100 px-2 py-0.5 font-mono text-xs">{cred.credential_id}</code>
                        </td>
                        <td className="px-6 py-3">
                          <span className="inline-flex items-center rounded-full bg-blue-50 px-2.5 py-0.5 text-xs font-medium text-blue-700">
                            {cred.provider}
                          </span>
                        </td>
                        <td className="px-6 py-3 text-gray-700">{cred.total_requests.toLocaleString()}</td>
                        <td className="px-6 py-3">
                          <span className={`font-medium ${rateColor}`}>{cred.success_rate.toFixed(1)}%</span>
                        </td>
                        <td className="px-6 py-3 text-gray-700">{cred.avg_latency_ms.toFixed(0)} ms</td>
                        <td className="px-6 py-3">
                          <WeightSlider
                            credentialId={cred.credential_id}
                            value={cred.weight}
                            onChange={handleWeightChange}
                            onCommit={handleWeightCommit}
                          />
                        </td>
                        <td className="px-6 py-3">
                          <CircuitBadge state={cred.circuit_state} />
                        </td>
                      </tr>
                    )
                  })}
                </tbody>
              </table>
            </div>
          )}
        </div>

        {/* ============================================================
         *  健康检查配置
         * ============================================================ */}
        <div className="rounded-xl border border-gray-200 bg-white shadow-sm">
          <div className="border-b border-gray-200 px-6 py-4">
            <h2 className="text-lg font-semibold text-gray-900">健康检查设置</h2>
          </div>
          <div className="grid gap-4 p-6 sm:grid-cols-3">
            <div>
              <label htmlFor="hInterval" className="mb-1 block text-sm font-medium text-gray-700">检查间隔 (秒)</label>
              <input id="hInterval" type="number" min={5} max={600} value={health.check_interval_sec} onChange={(e) => handleHealthChange('check_interval_sec', Number(e.target.value))} className={INPUT_CLS} />
              <p className="mt-1 text-xs text-gray-400">后台定时探测凭证存活状态的间隔</p>
            </div>
            <div>
              <label htmlFor="hThreshold" className="mb-1 block text-sm font-medium text-gray-700">失败阈值</label>
              <input id="hThreshold" type="number" min={1} max={50} value={health.failure_threshold} onChange={(e) => handleHealthChange('failure_threshold', Number(e.target.value))} className={INPUT_CLS} />
              <p className="mt-1 text-xs text-gray-400">连续失败此次数后触发熔断 (Open)</p>
            </div>
            <div>
              <label htmlFor="hRecovery" className="mb-1 block text-sm font-medium text-gray-700">恢复超时 (秒)</label>
              <input id="hRecovery" type="number" min={10} max={3600} value={health.recovery_timeout_sec} onChange={(e) => handleHealthChange('recovery_timeout_sec', Number(e.target.value))} className={INPUT_CLS} />
              <p className="mt-1 text-xs text-gray-400">熔断后等待此时间进入 HalfOpen 尝试恢复</p>
            </div>
          </div>
        </div>

        {/* ============================================================
         *  熔断状态说明
         * ============================================================ */}
        <div className="rounded-xl border border-gray-200 bg-white p-6 shadow-sm">
          <h3 className="mb-3 text-sm font-semibold text-gray-900">熔断状态说明</h3>
          <div className="grid gap-3 sm:grid-cols-3">
            <div className="flex items-start gap-2">
              <span className="mt-0.5 h-3 w-3 shrink-0 rounded-full bg-[#10B981]" />
              <div>
                <p className="text-sm font-medium text-gray-900">Closed (正常)</p>
                <p className="text-xs text-gray-500">凭证正常工作，请求正常分配</p>
              </div>
            </div>
            <div className="flex items-start gap-2">
              <span className="mt-0.5 h-3 w-3 shrink-0 rounded-full bg-[#EF4444]" />
              <div>
                <p className="text-sm font-medium text-gray-900">Open (熔断)</p>
                <p className="text-xs text-gray-500">凭证连续失败，暂停分配请求</p>
              </div>
            </div>
            <div className="flex items-start gap-2">
              <span className="mt-0.5 h-3 w-3 shrink-0 rounded-full bg-[#F59E0B]" />
              <div>
                <p className="text-sm font-medium text-gray-900">HalfOpen (试探)</p>
                <p className="text-xs text-gray-500">熔断超时后放行少量请求测试恢复</p>
              </div>
            </div>
          </div>
        </div>
      </div>
    </div>
  )
}
