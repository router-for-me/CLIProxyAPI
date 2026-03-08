import { useEffect, useMemo } from 'react'
import {
  BarChart, Bar, XAxis, YAxis, CartesianGrid, Tooltip,
  ResponsiveContainer, Legend,
} from 'recharts'
import { useAuthStore } from '../../stores/auth'
import { useUserStore, type QuotaItem } from '../../stores/user'
import { useI18n } from '../../i18n'

/* ============================================================
 * 额度详情页 — 表格 + 柱状图 + 池模式指示器
 * ============================================================ */

/* 计算距离重置时间的倒计时文案 */
function formatCountdown(periodEnd: string, isZh: boolean): string {
  const diff = new Date(periodEnd).getTime() - Date.now()
  if (diff <= 0) return isZh ? '即将重置' : 'Resetting...'

  const days = Math.floor(diff / 86_400_000)
  const hours = Math.floor((diff % 86_400_000) / 3_600_000)
  const mins = Math.floor((diff % 3_600_000) / 60_000)

  if (days > 0) return isZh ? `${days}天 ${hours}时` : `${days}d ${hours}h`
  if (hours > 0) return isZh ? `${hours}时 ${mins}分` : `${hours}h ${mins}m`
  return isZh ? `${mins}分` : `${mins}m`
}

/* 额度类型展示文案 */
function quotaTypeLabel(type: QuotaItem['quotaType'], isZh: boolean): string {
  const map: Record<string, string> = isZh
    ? { count: '次数', token: 'Token', both: '双计量' }
    : { count: 'Count', token: 'Token', both: 'Both' }
  return map[type] ?? type
}

/* 周期展示文案 */
function periodLabel(period: QuotaItem['period'], isZh: boolean): string {
  const map: Record<string, string> = isZh
    ? { daily: '每日', weekly: '每周', monthly: '每月', total: '总计' }
    : { daily: 'Daily', weekly: 'Weekly', monthly: 'Monthly', total: 'Total' }
  return map[period] ?? period
}

export default function Quota() {
  const { t, language } = useI18n()
  const isZh = language === 'zh'
  const user = useAuthStore((s) => s.user)
  const { quotas, fetchQuotas, loading } = useUserStore()

  useEffect(() => {
    fetchQuotas()
  }, [fetchQuotas])

  // --------------------------------------------------------
  // 柱状图数据
  // --------------------------------------------------------
  const chartData = useMemo(() => {
    return quotas.map((q) => ({
      model: q.modelPattern,
      used: q.quotaType === 'token' ? q.usedTokens : q.usedRequests,
      limit: q.quotaType === 'token' ? q.maxTokens : q.maxRequests + q.bonusRequests,
    }))
  }, [quotas])

  // --------------------------------------------------------
  // 池模式标签
  // --------------------------------------------------------
  const poolMode = useMemo(() => {
    if (!user?.poolMode) return '-'
    const key = user.poolMode
    const map: Record<string, { label: string; color: string }> = {
      private: { label: isZh ? '独立池' : 'Private', color: 'bg-blue-100 text-blue-700' },
      public: { label: isZh ? '公共池' : 'Public', color: 'bg-green-100 text-green-700' },
      contributor: { label: isZh ? '贡献者池' : 'Contributor', color: 'bg-amber-100 text-amber-700' },
    }
    return map[key] ?? { label: key, color: 'bg-gray-100 text-gray-700' }
  }, [user?.poolMode, isZh])

  // --------------------------------------------------------
  // 渲染
  // --------------------------------------------------------
  return (
    <div className="min-h-screen bg-[#F8FAFC] p-6">
      <div className="mx-auto max-w-6xl space-y-6">
        {/* ---- 标题 + 池模式 ---- */}
        <div className="flex items-center justify-between">
          <h1 className="text-2xl font-bold text-gray-900">
            {isZh ? '额度详情' : 'Quota Details'}
          </h1>
          {typeof poolMode === 'object' && (
            <span className={`rounded-full px-3 py-1 text-xs font-medium ${poolMode.color}`}>
              {isZh ? '池模式' : 'Pool Mode'}: {poolMode.label}
            </span>
          )}
        </div>

        {/* ---- 额度表格 ---- */}
        <div className="overflow-hidden rounded-xl bg-white shadow-sm">
          <div className="overflow-x-auto">
            <table className="w-full text-left text-sm">
              <thead>
                <tr className="border-b border-gray-100 bg-gray-50/60">
                  <th className="px-6 py-3 font-medium text-gray-500">{isZh ? '模型' : 'Model'}</th>
                  <th className="px-6 py-3 font-medium text-gray-500">{isZh ? '类型' : 'Type'}</th>
                  <th className="px-6 py-3 font-medium text-gray-500">{isZh ? '已用 / 上限' : 'Used / Limit'}</th>
                  <th className="px-6 py-3 font-medium text-gray-500">{isZh ? '周期' : 'Period'}</th>
                  <th className="px-6 py-3 font-medium text-gray-500">{isZh ? '重置倒计时' : 'Resets In'}</th>
                </tr>
              </thead>
              <tbody>
                {loading ? (
                  <tr>
                    <td colSpan={5} className="px-6 py-10 text-center text-gray-400">
                      {t('common.loading')}
                    </td>
                  </tr>
                ) : quotas.length === 0 ? (
                  <tr>
                    <td colSpan={5} className="px-6 py-10 text-center text-gray-400">
                      {t('common.noData')}
                    </td>
                  </tr>
                ) : (
                  quotas.map((q, idx) => {
                    const used = q.quotaType === 'token' ? q.usedTokens : q.usedRequests
                    const limit = q.quotaType === 'token'
                      ? q.maxTokens + q.bonusTokens
                      : q.maxRequests + q.bonusRequests
                    const pct = limit > 0 ? Math.min(100, (used / limit) * 100) : 0
                    const barColor = pct > 80 ? 'bg-red-400' : pct > 50 ? 'bg-amber-400' : 'bg-[#3B82F6]'

                    return (
                      <tr key={idx} className="border-b border-gray-50 last:border-none hover:bg-gray-50/40">
                        <td className="px-6 py-4 font-medium text-gray-900">{q.modelPattern}</td>
                        <td className="px-6 py-4 text-gray-600">{quotaTypeLabel(q.quotaType, isZh)}</td>
                        <td className="px-6 py-4">
                          <div className="flex items-center gap-3">
                            <span className="text-gray-700">{used.toLocaleString()} / {limit.toLocaleString()}</span>
                            <div className="h-1.5 w-20 overflow-hidden rounded-full bg-gray-100">
                              <div className={`h-full rounded-full ${barColor}`} style={{ width: `${pct}%` }} />
                            </div>
                          </div>
                        </td>
                        <td className="px-6 py-4 text-gray-600">{periodLabel(q.period, isZh)}</td>
                        <td className="px-6 py-4 text-gray-600">
                          {q.period === 'total' ? '-' : formatCountdown(q.periodEnd, isZh)}
                        </td>
                      </tr>
                    )
                  })
                )}
              </tbody>
            </table>
          </div>
        </div>

        {/* ---- 模型使用量柱状图 ---- */}
        {chartData.length > 0 && (
          <div className="rounded-xl bg-white p-6 shadow-sm">
            <h2 className="mb-4 text-sm font-semibold text-gray-500 uppercase tracking-wide">
              {isZh ? '模型使用量' : 'Usage by Model'}
            </h2>
            <ResponsiveContainer width="100%" height={300}>
              <BarChart data={chartData}>
                <CartesianGrid strokeDasharray="3 3" stroke="#E5E7EB" />
                <XAxis
                  dataKey="model"
                  tick={{ fill: '#6B7280', fontSize: 11 }}
                  tickLine={false}
                  axisLine={{ stroke: '#E5E7EB' }}
                />
                <YAxis
                  tick={{ fill: '#6B7280', fontSize: 12 }}
                  tickLine={false}
                  axisLine={false}
                />
                <Tooltip
                  contentStyle={{
                    borderRadius: '0.75rem',
                    border: 'none',
                    boxShadow: '0 1px 3px rgba(0,0,0,.1)',
                  }}
                />
                <Legend />
                <Bar
                  dataKey="used"
                  name={isZh ? '已用' : 'Used'}
                  fill="#3B82F6"
                  radius={[4, 4, 0, 0]}
                />
                <Bar
                  dataKey="limit"
                  name={isZh ? '上限' : 'Limit'}
                  fill="#E5E7EB"
                  radius={[4, 4, 0, 0]}
                />
              </BarChart>
            </ResponsiveContainer>
          </div>
        )}
      </div>
    </div>
  )
}
