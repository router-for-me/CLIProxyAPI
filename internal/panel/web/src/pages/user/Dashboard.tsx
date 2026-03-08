import { useEffect, useState, useMemo } from 'react'
import { useNavigate } from 'react-router-dom'
import {
  LineChart, Line, XAxis, YAxis, CartesianGrid, Tooltip,
  ResponsiveContainer, Legend,
} from 'recharts'
import { useAuthStore } from '../../stores/auth'
import { useUserStore } from '../../stores/user'
import { useI18n } from '../../i18n'

/* ============================================================
 * 用户仪表盘 — 欢迎信息 / API Key / 额度概览 / 趋势图 / 快捷操作
 * ============================================================ */

export default function Dashboard() {
  const navigate = useNavigate()
  const { t } = useI18n()
  const user = useAuthStore((s) => s.user)
  const {
    quota, stats,
    fetchQuota, fetchStats,
    quotaLoading, statsLoading,
  } = useUserStore()

  const loading = quotaLoading || statsLoading

  // --------------------------------------------------------
  // 初始加载
  // --------------------------------------------------------
  useEffect(() => {
    fetchQuota()
    fetchStats()
  }, [fetchQuota, fetchStats])

  // --------------------------------------------------------
  // API Key 显示 / 隐藏 / 复制
  // --------------------------------------------------------
  const [keyRevealed, setKeyRevealed] = useState(false)
  const [keyCopied, setKeyCopied] = useState(false)

  const apiKey = user?.api_key ?? ''
  const maskedKey = useMemo(() => {
    if (!apiKey) return '••••••••••••••••'
    if (keyRevealed) return apiKey
    return apiKey.length > 8
      ? apiKey.slice(0, 4) + '••••••••' + apiKey.slice(-4)
      : '••••••••'
  }, [apiKey, keyRevealed])

  const handleCopyKey = async () => {
    if (!apiKey) return
    try {
      await navigator.clipboard.writeText(apiKey)
      setKeyCopied(true)
      setTimeout(() => setKeyCopied(false), 2000)
    } catch { /* 剪贴板不可用时静默降级 */ }
  }

  // --------------------------------------------------------
  // 汇总统计
  // --------------------------------------------------------
  const models = quota?.models ?? []

  const totalRemaining = useMemo(() => {
    return models.reduce((sum, q) => sum + Math.max(0, q.remaining), 0)
  }, [models])

  const totalTokensUsed = useMemo(() => {
    return models.reduce((sum, q) => sum + q.used, 0)
  }, [models])

  const activeModelsCount = useMemo(() => {
    return new Set(models.map((q) => q.model)).size
  }, [models])

  const poolModeLabel = useMemo(() => {
    const mode = user?.pool_mode
    if (!mode) return '-'
    const map: Record<string, string> = {
      private: t('user.privatePool'),
      public: t('user.publicPool'),
      contributor: t('user.contributorPool'),
    }
    return map[mode] ?? mode
  }, [user?.pool_mode, t])

  // --------------------------------------------------------
  // 统计卡片数据
  // --------------------------------------------------------
  const statCards = [
    {
      label: t('user.remainingRequests'),
      value: totalRemaining.toLocaleString(),
      icon: (
        <svg className="h-6 w-6 text-[#3B82F6]" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={1.5}>
          <path strokeLinecap="round" strokeLinejoin="round" d="M3.75 13.5l10.5-11.25L12 10.5h8.25L9.75 21.75 12 13.5H3.75z" />
        </svg>
      ),
      color: 'bg-blue-50',
    },
    {
      label: t('user.tokensUsed'),
      value: totalTokensUsed.toLocaleString(),
      icon: (
        <svg className="h-6 w-6 text-[#10B981]" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={1.5}>
          <path strokeLinecap="round" strokeLinejoin="round" d="M20.25 6.375c0 2.278-3.694 4.125-8.25 4.125S3.75 8.653 3.75 6.375m16.5 0c0-2.278-3.694-4.125-8.25-4.125S3.75 4.097 3.75 6.375m16.5 0v11.25c0 2.278-3.694 4.125-8.25 4.125s-8.25-1.847-8.25-4.125V6.375m16.5 0v3.75c0 2.278-3.694 4.125-8.25 4.125s-8.25-1.847-8.25-4.125v-3.75" />
        </svg>
      ),
      color: 'bg-green-50',
    },
    {
      label: t('user.activeModels'),
      value: String(activeModelsCount),
      icon: (
        <svg className="h-6 w-6 text-purple-500" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={1.5}>
          <path strokeLinecap="round" strokeLinejoin="round" d="M6.429 9.75L2.25 12l4.179 2.25m0-4.5l5.571 3 5.571-3m-11.142 0L2.25 7.5 12 2.25l9.75 5.25-4.179 2.25m0 0L12 12.75 6.429 9.75m11.142 0l4.179 2.25-4.179 2.25m0 0L12 17.25l-5.571-3" />
        </svg>
      ),
      color: 'bg-purple-50',
    },
    {
      label: t('user.poolModeLabel'),
      value: poolModeLabel,
      icon: (
        <svg className="h-6 w-6 text-amber-500" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={1.5}>
          <path strokeLinecap="round" strokeLinejoin="round" d="M20.25 7.5l-.625 10.632a2.25 2.25 0 01-2.247 2.118H6.622a2.25 2.25 0 01-2.247-2.118L3.75 7.5m8.25 3v6.75m0 0l-3-3m3 3l3-3M3.375 7.5h17.25c.621 0 1.125-.504 1.125-1.125v-1.5c0-.621-.504-1.125-1.125-1.125H3.375c-.621 0-1.125.504-1.125 1.125v1.5c0 .621.504 1.125 1.125 1.125z" />
        </svg>
      ),
      color: 'bg-amber-50',
    },
  ]

  // --------------------------------------------------------
  // 快捷操作
  // --------------------------------------------------------
  const quickActions = [
    {
      label: t('user.redeemCode'),
      path: '/redeem',
      icon: (
        <svg className="h-5 w-5" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={1.5}>
          <path strokeLinecap="round" strokeLinejoin="round" d="M21 11.25v8.25a1.5 1.5 0 01-1.5 1.5H5.25a1.5 1.5 0 01-1.5-1.5v-8.25M12 4.875A2.625 2.625 0 109.375 7.5H12m0-2.625V7.5m0-2.625A2.625 2.625 0 1114.625 7.5H12m0 0V21m-8.625-9.75h18c.621 0 1.125-.504 1.125-1.125v-1.5c0-.621-.504-1.125-1.125-1.125h-18c-.621 0-1.125.504-1.125 1.125v1.5c0 .621.504 1.125 1.125 1.125z" />
        </svg>
      ),
    },
    {
      label: t('nav.quota'),
      path: '/quota',
      icon: (
        <svg className="h-5 w-5" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={1.5}>
          <path strokeLinecap="round" strokeLinejoin="round" d="M3 13.125C3 12.504 3.504 12 4.125 12h2.25c.621 0 1.125.504 1.125 1.125v6.75C7.5 20.496 6.996 21 6.375 21h-2.25A1.125 1.125 0 013 19.875v-6.75zM9.75 8.625c0-.621.504-1.125 1.125-1.125h2.25c.621 0 1.125.504 1.125 1.125v11.25c0 .621-.504 1.125-1.125 1.125h-2.25a1.125 1.125 0 01-1.125-1.125V8.625zM16.5 4.125c0-.621.504-1.125 1.125-1.125h2.25C20.496 3 21 3.504 21 4.125v15.75c0 .621-.504 1.125-1.125 1.125h-2.25a1.125 1.125 0 01-1.125-1.125V4.125z" />
        </svg>
      ),
    },
    {
      label: t('user.uploadCredential'),
      path: '/credentials',
      icon: (
        <svg className="h-5 w-5" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={1.5}>
          <path strokeLinecap="round" strokeLinejoin="round" d="M3 16.5v2.25A2.25 2.25 0 005.25 21h13.5A2.25 2.25 0 0021 18.75V16.5m-13.5-9L12 3m0 0l4.5 4.5M12 3v13.5" />
        </svg>
      ),
    },
  ]

  // --------------------------------------------------------
  // 渲染
  // --------------------------------------------------------
  return (
    <div className="min-h-screen bg-[#F8FAFC] p-6">
      <div className="mx-auto max-w-6xl space-y-6">
        {/* ---- 欢迎消息 ---- */}
        <h1 className="text-2xl font-bold text-gray-900">
          {t('user.welcome')}
          {user?.username ? `, ${user.username}` : ''}
        </h1>

        {/* ---- API Key 卡片 ---- */}
        <div className="rounded-xl bg-white p-6 shadow-sm">
          <h2 className="mb-3 text-sm font-semibold text-gray-500 uppercase tracking-wide">
            {t('user.apiKey')}
          </h2>
          <div className="flex items-center gap-3">
            <code className="flex-1 rounded-lg bg-gray-50 px-4 py-2.5 font-mono text-sm text-gray-800 select-all">
              {maskedKey}
            </code>
            <button
              type="button"
              onClick={() => setKeyRevealed((v) => !v)}
              className="rounded-lg border border-gray-200 px-3 py-2 text-sm text-gray-600 transition hover:bg-gray-50"
            >
              {keyRevealed ? t('user.hide') : t('user.show')}
            </button>
            <button
              type="button"
              onClick={handleCopyKey}
              className="rounded-lg border border-gray-200 px-3 py-2 text-sm text-gray-600 transition hover:bg-gray-50"
            >
              {keyCopied ? t('common.copied') : t('common.copy')}
            </button>
          </div>
          <p className="mt-2 text-xs text-gray-400">{t('user.apiKeyHint')}</p>
        </div>

        {/* ---- 额度概览卡片组 ---- */}
        <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-4">
          {statCards.map((card) => (
            <div key={card.label} className="rounded-xl bg-white p-5 shadow-sm">
              <div className="flex items-center gap-3">
                <div className={`flex h-10 w-10 items-center justify-center rounded-lg ${card.color}`}>
                  {card.icon}
                </div>
                <div>
                  <p className="text-xs text-gray-500">{card.label}</p>
                  <p className="text-xl font-bold text-gray-900">{loading ? '-' : card.value}</p>
                </div>
              </div>
            </div>
          ))}
        </div>

        {/* ---- 近 7 天使用趋势 ---- */}
        <div className="rounded-xl bg-white p-6 shadow-sm">
          <h2 className="mb-4 text-sm font-semibold text-gray-500 uppercase tracking-wide">
            {t('user.recentUsage')}
          </h2>
          {(stats?.recent_usage?.length ?? 0) > 0 ? (
            <ResponsiveContainer width="100%" height={280}>
              <LineChart data={stats!.recent_usage}>
                <CartesianGrid strokeDasharray="3 3" stroke="#E5E7EB" />
                <XAxis
                  dataKey="date"
                  tick={{ fill: '#6B7280', fontSize: 12 }}
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
                <Line
                  type="monotone"
                  dataKey="count"
                  name={t('user.requests')}
                  stroke="#3B82F6"
                  strokeWidth={2}
                  dot={{ r: 3, fill: '#3B82F6' }}
                  activeDot={{ r: 5 }}
                />
                <Line
                  type="monotone"
                  dataKey="tokens"
                  name="Tokens"
                  stroke="#10B981"
                  strokeWidth={2}
                  dot={{ r: 3, fill: '#10B981' }}
                  activeDot={{ r: 5 }}
                />
              </LineChart>
            </ResponsiveContainer>
          ) : (
            <div className="flex h-40 items-center justify-center text-sm text-gray-400">
              {t('common.noData')}
            </div>
          )}
        </div>

        {/* ---- 快捷操作 ---- */}
        <div className="rounded-xl bg-white p-6 shadow-sm">
          <h2 className="mb-4 text-sm font-semibold text-gray-500 uppercase tracking-wide">
            {t('user.quickActions')}
          </h2>
          <div className="flex flex-wrap gap-3">
            {quickActions.map((action) => (
              <button
                key={action.path}
                type="button"
                onClick={() => navigate(action.path)}
                className="inline-flex items-center gap-2 rounded-lg border border-gray-200 px-4 py-2.5 text-sm font-medium text-gray-700 transition hover:border-[#3B82F6] hover:text-[#3B82F6] hover:bg-[#3B82F6]/5"
              >
                {action.icon}
                {action.label}
              </button>
            ))}
          </div>
        </div>
      </div>
    </div>
  )
}
