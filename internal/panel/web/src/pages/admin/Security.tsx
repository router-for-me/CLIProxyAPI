// ============================================================
// 安全中心 — 模块开关 / IP 规则 / 风险标记 / 异常事件 / 审计日志
// 五大安全板块统一管理
// ============================================================

import { useState, useEffect, useCallback } from 'react'
import type { FormEvent } from 'react'
import {
  getSecurityStatus,
  toggleSecurityModule,
  getIPRules,
  createIPRule,
  deleteIPRule,
  getRiskMarks,
  createRiskMark,
  removeRiskMark,
  getAnomalyEvents,
  getAuditLogs,
  type SecurityModuleStatus,
  type IPRule,
  type RiskMark,
  type AnomalyEvent,
  type AnomalySeverity,
  type AuditLogEntry,
} from '../../api/admin'

// ============================================================
// 常量 — 样式映射
// ============================================================

const SEVERITY_STYLES: Record<AnomalySeverity, { bg: string; text: string }> = {
  low:      { bg: 'bg-blue-50',   text: 'text-blue-700' },
  medium:   { bg: 'bg-amber-50',  text: 'text-amber-700' },
  high:     { bg: 'bg-orange-50', text: 'text-orange-700' },
  critical: { bg: 'bg-red-50',    text: 'text-red-700' },
}

const MODULE_META: Record<
  keyof SecurityModuleStatus,
  { label: string; desc: string; icon: string }
> = {
  ip_control:        { label: 'IP 访问控制',  desc: 'CIDR 黑白名单，精确控制访问来源',         icon: 'M12 15v2m-6 4h12a2 2 0 002-2v-6a2 2 0 00-2-2H6a2 2 0 00-2 2v6a2 2 0 002 2zm10-10V7a4 4 0 00-8 0v4h8z' },
  rate_limit:        { label: '请求限流',      desc: '全局 QPS 和单 IP RPM 滑动窗口限流',      icon: 'M13 10V3L4 14h7v7l9-11h-7z' },
  anomaly_detection: { label: '异常检测',      desc: '高频 / 模型扫描 / 错误峰值行为检测',      icon: 'M12 9v2m0 4h.01m-6.938 4h13.856c1.54 0 2.502-1.667 1.732-2.5L13.732 4c-.77-.833-1.964-.833-2.732 0L4.082 16.5c-.77.833.192 2.5 1.732 2.5z' },
  audit:             { label: '审计日志',      desc: '记录所有管理操作和安全事件',               icon: 'M9 12h6m-6 4h6m2 5H7a2 2 0 01-2-2V5a2 2 0 012-2h5.586a1 1 0 01.707.293l5.414 5.414a1 1 0 01.293.707V19a2 2 0 01-2 2z' },
}

const ACTION_OPTIONS = [
  { value: '', label: '全部操作' },
  { value: 'user.create', label: '用户创建' },
  { value: 'user.ban', label: '用户封禁' },
  { value: 'user.unban', label: '用户解封' },
  { value: 'credential.add', label: '凭证添加' },
  { value: 'credential.delete', label: '凭证删除' },
  { value: 'settings.update', label: '设置更新' },
  { value: 'security.toggle', label: '安全模块切换' },
  { value: 'invite.create', label: '邀请码创建' },
]

// ============================================================
// 工具函数
// ============================================================

function formatDate(iso: string): string {
  return new Date(iso).toLocaleDateString('zh-CN', {
    year: 'numeric', month: '2-digit', day: '2-digit',
    hour: '2-digit', minute: '2-digit',
  })
}

function formatRelative(iso: string): string {
  const diff = Date.now() - new Date(iso).getTime()
  const m = Math.floor(diff / 60000)
  if (m < 1) return '刚刚'
  if (m < 60) return `${m} 分钟前`
  const h = Math.floor(m / 60)
  if (h < 24) return `${h} 小时前`
  return `${Math.floor(h / 24)} 天前`
}

// ============================================================
// 子组件: 模块卡片 (带开关)
// ============================================================

function ModuleCard({
  moduleKey,
  enabled,
  onToggle,
}: {
  moduleKey: keyof SecurityModuleStatus
  enabled: boolean
  onToggle: (k: keyof SecurityModuleStatus, v: boolean) => void
}) {
  const m = MODULE_META[moduleKey]
  return (
    <div className="flex items-start justify-between rounded-xl border border-gray-200 bg-white p-5 shadow-sm transition hover:shadow-md">
      <div className="flex items-start gap-3">
        <div className={`flex h-10 w-10 shrink-0 items-center justify-center rounded-lg ${enabled ? 'bg-blue-50 text-[#3B82F6]' : 'bg-gray-100 text-gray-400'}`}>
          <svg className="h-5 w-5" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={1.5}>
            <path strokeLinecap="round" strokeLinejoin="round" d={m.icon} />
          </svg>
        </div>
        <div>
          <h3 className="text-sm font-semibold text-gray-900">{m.label}</h3>
          <p className="mt-0.5 text-xs text-gray-500">{m.desc}</p>
        </div>
      </div>
      <button
        type="button"
        role="switch"
        aria-checked={enabled}
        onClick={() => onToggle(moduleKey, !enabled)}
        className={`relative inline-flex h-6 w-11 shrink-0 cursor-pointer rounded-full transition-colors ${enabled ? 'bg-[#3B82F6]' : 'bg-gray-200'}`}
      >
        <span className={`pointer-events-none inline-block h-5 w-5 translate-y-0.5 rounded-full bg-white shadow-sm transition-transform ${enabled ? 'translate-x-[22px]' : 'translate-x-0.5'}`} />
      </button>
    </div>
  )
}

// ============================================================
// 子组件: 严重度徽标
// ============================================================

function SeverityBadge({ severity }: { severity: AnomalySeverity }) {
  const s = SEVERITY_STYLES[severity]
  return (
    <span className={`inline-flex items-center rounded-full px-2.5 py-0.5 text-xs font-medium ${s.bg} ${s.text}`}>
      {severity.toUpperCase()}
    </span>
  )
}

// ============================================================
// 审计日志筛选器状态类型
// ============================================================

interface AuditFilter {
  action?: string
  start_date?: string
  end_date?: string
  page: number
  page_size: number
}

// ============================================================
// 主组件: Security
// ============================================================

export default function Security() {
  /* ---- 模块状态 ---- */
  const [moduleStatus, setModuleStatus] = useState<SecurityModuleStatus>({
    ip_control: false, rate_limit: false, anomaly_detection: false, audit: false,
  })

  /* ---- IP 规则 ---- */
  const [ipRules, setIPRules] = useState<IPRule[]>([])
  const [newCIDR, setNewCIDR] = useState('')
  const [newRuleType, setNewRuleType] = useState<'whitelist' | 'blacklist'>('blacklist')
  const [newRuleDesc, setNewRuleDesc] = useState('')

  /* ---- 风险标记 ---- */
  const [riskMarks, setRiskMarks] = useState<RiskMark[]>([])
  const [riskUserId, setRiskUserId] = useState('')
  const [riskType, setRiskType] = useState<RiskMark['type']>('manual')
  const [riskReason, setRiskReason] = useState('')
  const [riskDuration, setRiskDuration] = useState(24)

  /* ---- 异常事件 ---- */
  const [anomalyEvents, setAnomalyEvents] = useState<AnomalyEvent[]>([])

  /* ---- 审计日志 ---- */
  const [auditLogs, setAuditLogs] = useState<AuditLogEntry[]>([])
  const [auditTotal, setAuditTotal] = useState(0)
  const [auditFilter, setAuditFilter] = useState<AuditFilter>({ page: 1, page_size: 15 })

  /* ---- 通用 ---- */
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [activeTab, setActiveTab] = useState<'ip' | 'risk' | 'anomaly' | 'audit'>('ip')

  // ============================================================
  // 数据加载
  // ============================================================

  const loadAll = useCallback(async () => {
    try {
      setLoading(true)
      const [statusRes, rulesRes, marksRes, eventsRes] = await Promise.all([
        getSecurityStatus(), getIPRules(), getRiskMarks(), getAnomalyEvents(50),
      ])
      setModuleStatus(statusRes.data)
      setIPRules(rulesRes.data)
      setRiskMarks(marksRes.data)
      setAnomalyEvents(eventsRes.data)
      setError('')
    } catch (err) {
      setError(err instanceof Error ? err.message : '加载安全数据失败')
    } finally {
      setLoading(false)
    }
  }, [])

  const loadAuditLogs = useCallback(async () => {
    try {
      const res = await getAuditLogs(auditFilter)
      setAuditLogs(res.data.items)
      setAuditTotal(res.data.total)
    } catch (err) {
      setError(err instanceof Error ? err.message : '加载审计日志失败')
    }
  }, [auditFilter])

  useEffect(() => { loadAll() }, [loadAll])
  useEffect(() => { if (activeTab === 'audit') loadAuditLogs() }, [activeTab, loadAuditLogs])

  // ============================================================
  // 事件处理
  // ============================================================

  const handleToggleModule = async (k: keyof SecurityModuleStatus, v: boolean) => {
    try {
      await toggleSecurityModule(k, v)
      setModuleStatus((p) => ({ ...p, [k]: v }))
    } catch (err) {
      setError(err instanceof Error ? err.message : '切换模块失败')
    }
  }

  const handleAddIPRule = async (e: FormEvent) => {
    e.preventDefault()
    try {
      await createIPRule({ cidr: newCIDR, rule_type: newRuleType, description: newRuleDesc })
      setNewCIDR(''); setNewRuleDesc('')
      const res = await getIPRules()
      setIPRules(res.data)
    } catch (err) {
      setError(err instanceof Error ? err.message : '添加 IP 规则失败')
    }
  }

  const handleDeleteIPRule = async (id: number) => {
    try {
      await deleteIPRule(id)
      setIPRules((p) => p.filter((r) => r.id !== id))
    } catch (err) {
      setError(err instanceof Error ? err.message : '删除 IP 规则失败')
    }
  }

  const handleAddRisk = async (e: FormEvent) => {
    e.preventDefault()
    try {
      await createRiskMark({
        user_id: Number(riskUserId), type: riskType,
        reason: riskReason, duration_hours: riskDuration,
      })
      setRiskUserId(''); setRiskReason(''); setRiskDuration(24)
      const res = await getRiskMarks()
      setRiskMarks(res.data)
    } catch (err) {
      setError(err instanceof Error ? err.message : '添加风险标记失败')
    }
  }

  const handleRemoveRisk = async (id: number) => {
    try {
      await removeRiskMark(id)
      setRiskMarks((p) => p.filter((m) => m.id !== id))
    } catch (err) {
      setError(err instanceof Error ? err.message : '移除风险标记失败')
    }
  }

  const totalPages = Math.ceil(auditTotal / auditFilter.page_size)

  // ============================================================
  // 通用样式常量
  // ============================================================

  const inputClass = 'w-full rounded-lg border border-gray-300 px-3 py-2 text-sm focus:border-[#3B82F6] focus:outline-none focus:ring-2 focus:ring-[#3B82F6]/20'

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
      <div className="mb-6">
        <h1 className="text-2xl font-bold text-gray-900">安全中心</h1>
        <p className="mt-1 text-sm text-gray-500">管理安全模块、IP 控制、风险标记和审计日志</p>
      </div>

      {/* ---- 错误提示 ---- */}
      {error && (
        <div className="mb-4 rounded-xl border border-red-200 bg-red-50 px-4 py-3 text-sm text-[#EF4444]">
          {error}
          <button type="button" onClick={() => setError('')} className="ml-2 font-medium underline">关闭</button>
        </div>
      )}

      {/* ---- 模块卡片网格 ---- */}
      <div className="mb-6 grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
        {(Object.keys(MODULE_META) as (keyof SecurityModuleStatus)[]).map((k) => (
          <ModuleCard key={k} moduleKey={k} enabled={moduleStatus[k]} onToggle={handleToggleModule} />
        ))}
      </div>

      {/* ---- Tab 切换 ---- */}
      <div className="mb-4 flex gap-1 rounded-xl bg-gray-100 p-1">
        {([
          { key: 'ip' as const, label: 'IP 规则' },
          { key: 'risk' as const, label: '风险标记' },
          { key: 'anomaly' as const, label: '异常事件' },
          { key: 'audit' as const, label: '审计日志' },
        ]).map((t) => (
          <button
            key={t.key}
            type="button"
            onClick={() => setActiveTab(t.key)}
            className={`flex-1 rounded-lg px-4 py-2 text-sm font-medium transition ${
              activeTab === t.key ? 'bg-white text-gray-900 shadow-sm' : 'text-gray-500 hover:text-gray-700'
            }`}
          >
            {t.label}
          </button>
        ))}
      </div>

      {/* ============================================================
       *  IP 规则
       * ============================================================ */}
      {activeTab === 'ip' && (
        <div className="rounded-xl border border-gray-200 bg-white shadow-sm">
          <div className="border-b border-gray-200 p-6">
            <h3 className="mb-3 text-sm font-semibold text-gray-900">添加 IP 规则</h3>
            <form onSubmit={handleAddIPRule} className="flex flex-wrap items-end gap-3">
              <div className="min-w-[180px] flex-1">
                <label htmlFor="cidr" className="mb-1 block text-xs font-medium text-gray-600">CIDR 地址</label>
                <input id="cidr" type="text" value={newCIDR} onChange={(e) => setNewCIDR(e.target.value)} placeholder="192.168.1.0/24" className={inputClass} required />
              </div>
              <div className="min-w-[140px]">
                <label htmlFor="ruleType" className="mb-1 block text-xs font-medium text-gray-600">类型</label>
                <select id="ruleType" value={newRuleType} onChange={(e) => setNewRuleType(e.target.value as 'whitelist' | 'blacklist')} className={inputClass}>
                  <option value="blacklist">黑名单</option>
                  <option value="whitelist">白名单</option>
                </select>
              </div>
              <div className="min-w-[200px] flex-1">
                <label htmlFor="ruleDesc" className="mb-1 block text-xs font-medium text-gray-600">备注</label>
                <input id="ruleDesc" type="text" value={newRuleDesc} onChange={(e) => setNewRuleDesc(e.target.value)} placeholder="描述此规则的用途" className={inputClass} />
              </div>
              <button type="submit" className="rounded-lg bg-[#3B82F6] px-4 py-2 text-sm font-medium text-white transition hover:bg-blue-600">添加</button>
            </form>
          </div>

          {ipRules.length === 0 ? (
            <div className="py-12 text-center text-sm text-gray-500">暂无 IP 规则</div>
          ) : (
            <div className="overflow-x-auto">
              <table className="w-full text-left text-sm">
                <thead>
                  <tr className="border-b border-gray-100 bg-gray-50/50">
                    <th className="px-6 py-3 font-medium text-gray-500">CIDR</th>
                    <th className="px-6 py-3 font-medium text-gray-500">类型</th>
                    <th className="px-6 py-3 font-medium text-gray-500">备注</th>
                    <th className="px-6 py-3 font-medium text-gray-500">创建时间</th>
                    <th className="px-6 py-3 font-medium text-gray-500">操作</th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-gray-100">
                  {ipRules.map((r) => (
                    <tr key={r.id} className="transition hover:bg-gray-50/50">
                      <td className="px-6 py-3"><code className="rounded bg-gray-100 px-2 py-0.5 font-mono text-xs">{r.cidr}</code></td>
                      <td className="px-6 py-3">
                        <span className={`inline-flex items-center rounded-full px-2.5 py-0.5 text-xs font-medium ${r.rule_type === 'whitelist' ? 'bg-emerald-50 text-emerald-700' : 'bg-red-50 text-red-700'}`}>
                          {r.rule_type === 'whitelist' ? '白名单' : '黑名单'}
                        </span>
                      </td>
                      <td className="px-6 py-3 text-gray-600">{r.description || '-'}</td>
                      <td className="px-6 py-3 text-gray-500">{formatDate(r.created_at)}</td>
                      <td className="px-6 py-3">
                        <button type="button" onClick={() => handleDeleteIPRule(r.id)} className="rounded-lg px-2.5 py-1 text-xs font-medium text-[#EF4444] transition hover:bg-red-50">删除</button>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </div>
      )}

      {/* ============================================================
       *  风险标记
       * ============================================================ */}
      {activeTab === 'risk' && (
        <div className="rounded-xl border border-gray-200 bg-white shadow-sm">
          <div className="border-b border-gray-200 p-6">
            <h3 className="mb-3 text-sm font-semibold text-gray-900">手动添加风险标记</h3>
            <form onSubmit={handleAddRisk} className="flex flex-wrap items-end gap-3">
              <div className="min-w-[120px]">
                <label htmlFor="riskUid" className="mb-1 block text-xs font-medium text-gray-600">用户 ID</label>
                <input id="riskUid" type="number" min={1} value={riskUserId} onChange={(e) => setRiskUserId(e.target.value)} className={inputClass} required />
              </div>
              <div className="min-w-[140px]">
                <label htmlFor="riskTp" className="mb-1 block text-xs font-medium text-gray-600">类型</label>
                <select id="riskTp" value={riskType} onChange={(e) => setRiskType(e.target.value as RiskMark['type'])} className={inputClass}>
                  <option value="manual">手动标记</option>
                  <option value="rpm_exceed">RPM 超限</option>
                  <option value="anomaly">异常行为</option>
                </select>
              </div>
              <div className="min-w-[200px] flex-1">
                <label htmlFor="riskRsn" className="mb-1 block text-xs font-medium text-gray-600">原因</label>
                <input id="riskRsn" type="text" value={riskReason} onChange={(e) => setRiskReason(e.target.value)} placeholder="标记原因说明" className={inputClass} required />
              </div>
              <div className="min-w-[120px]">
                <label htmlFor="riskDur" className="mb-1 block text-xs font-medium text-gray-600">持续(小时)</label>
                <input id="riskDur" type="number" min={1} value={riskDuration} onChange={(e) => setRiskDuration(Number(e.target.value))} className={inputClass} required />
              </div>
              <button type="submit" className="rounded-lg bg-[#F59E0B] px-4 py-2 text-sm font-medium text-white transition hover:bg-amber-600">添加标记</button>
            </form>
          </div>

          {riskMarks.length === 0 ? (
            <div className="py-12 text-center text-sm text-gray-500">当前无活跃风险标记</div>
          ) : (
            <div className="overflow-x-auto">
              <table className="w-full text-left text-sm">
                <thead>
                  <tr className="border-b border-gray-100 bg-gray-50/50">
                    <th className="px-6 py-3 font-medium text-gray-500">用户</th>
                    <th className="px-6 py-3 font-medium text-gray-500">类型</th>
                    <th className="px-6 py-3 font-medium text-gray-500">原因</th>
                    <th className="px-6 py-3 font-medium text-gray-500">过期时间</th>
                    <th className="px-6 py-3 font-medium text-gray-500">操作</th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-gray-100">
                  {riskMarks.map((mk) => (
                    <tr key={mk.id} className="transition hover:bg-gray-50/50">
                      <td className="px-6 py-3 font-medium text-gray-900">
                        {mk.username}<span className="ml-1 text-xs text-gray-400">#{mk.user_id}</span>
                      </td>
                      <td className="px-6 py-3">
                        <span className={`inline-flex items-center rounded-full px-2.5 py-0.5 text-xs font-medium ${
                          mk.type === 'manual' ? 'bg-amber-50 text-amber-700'
                            : mk.type === 'rpm_exceed' ? 'bg-orange-50 text-orange-700'
                              : 'bg-red-50 text-red-700'
                        }`}>
                          {mk.type === 'manual' ? '手动' : mk.type === 'rpm_exceed' ? 'RPM 超限' : '异常行为'}
                        </span>
                      </td>
                      <td className="max-w-xs truncate px-6 py-3 text-gray-600">{mk.reason}</td>
                      <td className="px-6 py-3 text-gray-500">{formatDate(mk.expires_at)}</td>
                      <td className="px-6 py-3">
                        <button type="button" onClick={() => handleRemoveRisk(mk.id)} className="rounded-lg px-2.5 py-1 text-xs font-medium text-[#EF4444] transition hover:bg-red-50">移除</button>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </div>
      )}

      {/* ============================================================
       *  异常事件
       * ============================================================ */}
      {activeTab === 'anomaly' && (
        <div className="rounded-xl border border-gray-200 bg-white shadow-sm">
          <div className="border-b border-gray-200 px-6 py-4">
            <h3 className="text-sm font-semibold text-gray-900">近期异常事件</h3>
          </div>

          {anomalyEvents.length === 0 ? (
            <div className="py-12 text-center text-sm text-gray-500">无异常事件记录</div>
          ) : (
            <div className="divide-y divide-gray-100">
              {anomalyEvents.map((ev) => (
                <div key={ev.id} className="flex items-start gap-4 px-6 py-4 transition hover:bg-gray-50/50">
                  <div className={`mt-1 h-8 w-1 shrink-0 rounded-full ${
                    ev.severity === 'critical' ? 'bg-[#EF4444]'
                      : ev.severity === 'high' ? 'bg-orange-500'
                        : ev.severity === 'medium' ? 'bg-[#F59E0B]'
                          : 'bg-blue-400'
                  }`} />
                  <div className="min-w-0 flex-1">
                    <div className="flex flex-wrap items-center gap-2">
                      <SeverityBadge severity={ev.severity} />
                      <span className="rounded bg-gray-100 px-2 py-0.5 text-xs font-medium text-gray-600">
                        {ev.event_type === 'high_frequency' ? '高频请求'
                          : ev.event_type === 'model_scan' ? '模型扫描' : '错误峰值'}
                      </span>
                      <span className="text-xs text-gray-400">{ev.username} (#{ev.user_id})</span>
                    </div>
                    <p className="mt-1 text-sm text-gray-700">{ev.detail}</p>
                  </div>
                  <span className="shrink-0 text-xs text-gray-400">{formatRelative(ev.created_at)}</span>
                </div>
              ))}
            </div>
          )}
        </div>
      )}

      {/* ============================================================
       *  审计日志时间线
       * ============================================================ */}
      {activeTab === 'audit' && (
        <div className="rounded-xl border border-gray-200 bg-white shadow-sm">
          {/* 筛选栏 */}
          <div className="flex flex-wrap items-end gap-3 border-b border-gray-200 p-6">
            <div className="min-w-[160px]">
              <label htmlFor="aAction" className="mb-1 block text-xs font-medium text-gray-600">操作类型</label>
              <select
                id="aAction"
                value={auditFilter.action ?? ''}
                onChange={(e) => setAuditFilter((p) => ({ ...p, action: e.target.value || undefined, page: 1 }))}
                className={inputClass}
              >
                {ACTION_OPTIONS.map((o) => <option key={o.value} value={o.value}>{o.label}</option>)}
              </select>
            </div>
            <div className="min-w-[130px]">
              <label htmlFor="aStart" className="mb-1 block text-xs font-medium text-gray-600">开始日期</label>
              <input id="aStart" type="date" value={auditFilter.start_date ?? ''} onChange={(e) => setAuditFilter((p) => ({ ...p, start_date: e.target.value || undefined, page: 1 }))} className={inputClass} />
            </div>
            <div className="min-w-[130px]">
              <label htmlFor="aEnd" className="mb-1 block text-xs font-medium text-gray-600">结束日期</label>
              <input id="aEnd" type="date" value={auditFilter.end_date ?? ''} onChange={(e) => setAuditFilter((p) => ({ ...p, end_date: e.target.value || undefined, page: 1 }))} className={inputClass} />
            </div>
            <button type="button" onClick={() => setAuditFilter({ page: 1, page_size: 15 })} className="rounded-lg border border-gray-300 px-4 py-2 text-sm font-medium text-gray-600 transition hover:bg-gray-50">重置</button>
          </div>

          {/* 日志列表 */}
          {auditLogs.length === 0 ? (
            <div className="py-12 text-center text-sm text-gray-500">暂无审计日志</div>
          ) : (
            <div className="divide-y divide-gray-100">
              {auditLogs.map((log) => (
                <div key={log.id} className="flex items-start gap-4 px-6 py-4 transition hover:bg-gray-50/50">
                  <div className="mt-1.5 h-2.5 w-2.5 shrink-0 rounded-full bg-[#3B82F6]" />
                  <div className="min-w-0 flex-1">
                    <div className="flex flex-wrap items-center gap-2 text-sm">
                      <span className="font-medium text-gray-900">{log.username || '系统'}</span>
                      <span className="rounded bg-gray-100 px-2 py-0.5 text-xs text-gray-600">{log.action}</span>
                      {log.target && <span className="text-gray-500">{log.target}</span>}
                    </div>
                    {log.detail && <p className="mt-0.5 text-xs text-gray-500">{log.detail}</p>}
                  </div>
                  <div className="shrink-0 text-right">
                    <div className="text-xs text-gray-400">{formatRelative(log.created_at)}</div>
                    <div className="text-xs text-gray-300">{log.ip}</div>
                  </div>
                </div>
              ))}
            </div>
          )}

          {/* 分页 */}
          {totalPages > 1 && (
            <div className="flex items-center justify-between border-t border-gray-200 px-6 py-3">
              <span className="text-xs text-gray-500">共 {auditTotal} 条，第 {auditFilter.page} / {totalPages} 页</span>
              <div className="flex gap-1">
                <button type="button" disabled={auditFilter.page <= 1} onClick={() => setAuditFilter((p) => ({ ...p, page: p.page - 1 }))} className="rounded-lg border border-gray-300 px-3 py-1.5 text-xs font-medium text-gray-600 transition hover:bg-gray-50 disabled:cursor-not-allowed disabled:opacity-40">上一页</button>
                <button type="button" disabled={auditFilter.page >= totalPages} onClick={() => setAuditFilter((p) => ({ ...p, page: p.page + 1 }))} className="rounded-lg border border-gray-300 px-3 py-1.5 text-xs font-medium text-gray-600 transition hover:bg-gray-50 disabled:cursor-not-allowed disabled:opacity-40">下一页</button>
              </div>
            </div>
          )}
        </div>
      )}
    </div>
  )
}
