// ============================================================
// 邀请码管理页 — 生成 / 查看 / 禁用邀请码
// 支持管理员邀请码和用户裂变码两种类型
// ============================================================

import { useState, useEffect, useCallback } from 'react'
import type { FormEvent } from 'react'
import {
  getInviteCodes,
  createInviteCode,
  disableInviteCode,
  type InviteCode,
} from '../../api/admin'

// ============================================================
// 常量 — 状态样式映射
// ============================================================

type InviteStatus = InviteCode['status']

const STATUS_STYLES: Record<InviteStatus, { bg: string; text: string; label: string }> = {
  active:    { bg: 'bg-emerald-50',  text: 'text-emerald-700', label: '有效' },
  exhausted: { bg: 'bg-gray-100',    text: 'text-gray-600',    label: '已用尽' },
  expired:   { bg: 'bg-red-50',      text: 'text-red-700',     label: '已过期' },
  disabled:  { bg: 'bg-gray-100',    text: 'text-gray-500',    label: '已禁用' },
}

const TYPE_LABELS: Record<string, string> = {
  admin_created: '管理员',
  user_referral: '裂变码',
}

// ============================================================
// 子组件: 状态徽标
// ============================================================

function StatusBadge({ status }: { status: InviteStatus }) {
  const s = STATUS_STYLES[status]
  return (
    <span className={`inline-flex items-center rounded-full px-2.5 py-0.5 text-xs font-medium ${s.bg} ${s.text}`}>
      {s.label}
    </span>
  )
}

// ============================================================
// 子组件: 类型徽标
// ============================================================

function TypeBadge({ type }: { type: string }) {
  const isAdmin = type === 'admin_created'
  return (
    <span
      className={`inline-flex items-center rounded-full px-2.5 py-0.5 text-xs font-medium ${
        isAdmin ? 'bg-blue-50 text-blue-700' : 'bg-purple-50 text-purple-700'
      }`}
    >
      {TYPE_LABELS[type] ?? type}
    </span>
  )
}

// ============================================================
// 子组件: 复制按钮
// ============================================================

function CopyButton({ text }: { text: string }) {
  const [copied, setCopied] = useState(false)

  const handleCopy = useCallback(async () => {
    try {
      await navigator.clipboard.writeText(text)
      setCopied(true)
      setTimeout(() => setCopied(false), 1500)
    } catch {
      // 剪贴板 API 不可用时静默降级
    }
  }, [text])

  return (
    <button
      type="button"
      onClick={handleCopy}
      className="inline-flex items-center gap-1 rounded-lg border border-gray-200 bg-white px-2.5 py-1 text-xs font-medium text-gray-600 transition hover:bg-gray-50 active:scale-95"
    >
      {copied ? (
        <>
          <svg className="h-3.5 w-3.5 text-emerald-500" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={2}>
            <path strokeLinecap="round" strokeLinejoin="round" d="M5 13l4 4L19 7" />
          </svg>
          已复制
        </>
      ) : (
        <>
          <svg className="h-3.5 w-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={2}>
            <path strokeLinecap="round" strokeLinejoin="round" d="M8 16H6a2 2 0 01-2-2V6a2 2 0 012-2h8a2 2 0 012 2v2m-6 12h8a2 2 0 002-2v-8a2 2 0 00-2-2h-8a2 2 0 00-2 2v8a2 2 0 002 2z" />
          </svg>
          复制
        </>
      )}
    </button>
  )
}

// ============================================================
// 工具函数
// ============================================================

function formatDate(iso: string): string {
  return new Date(iso).toLocaleDateString('zh-CN', {
    year: 'numeric', month: '2-digit', day: '2-digit',
    hour: '2-digit', minute: '2-digit',
  })
}

// ============================================================
// 主组件: InviteCodes
// ============================================================

export default function InviteCodes() {
  /* ---- 状态 ---- */
  const [codes, setCodes] = useState<InviteCode[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [submitting, setSubmitting] = useState(false)

  // 生成表单字段
  const [maxUses, setMaxUses] = useState(10)
  const [bonusQuota, setBonusQuota] = useState('')
  const [expiresAt, setExpiresAt] = useState('')

  /* ---- 数据加载 ---- */
  const loadCodes = useCallback(async () => {
    try {
      setLoading(true)
      const res = await getInviteCodes()
      setCodes(res.data)
      setError('')
    } catch (err) {
      setError(err instanceof Error ? err.message : '加载邀请码失败')
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => { loadCodes() }, [loadCodes])

  /* ---- 生成邀请码 ---- */
  const handleGenerate = async (e: FormEvent) => {
    e.preventDefault()
    setSubmitting(true)
    setError('')
    try {
      const params: { max_uses: number; bonus_quota?: number; expires_at?: string } = {
        max_uses: maxUses,
      }
      if (bonusQuota) params.bonus_quota = Number(bonusQuota)
      if (expiresAt) params.expires_at = new Date(expiresAt).toISOString()

      await createInviteCode(params)
      setMaxUses(10)
      setBonusQuota('')
      setExpiresAt('')
      await loadCodes()
    } catch (err) {
      setError(err instanceof Error ? err.message : '生成邀请码失败')
    } finally {
      setSubmitting(false)
    }
  }

  /* ---- 禁用邀请码 ---- */
  const handleDisable = async (id: number) => {
    try {
      await disableInviteCode(id)
      await loadCodes()
    } catch (err) {
      setError(err instanceof Error ? err.message : '禁用邀请码失败')
    }
  }

  // ============================================================
  // 渲染
  // ============================================================

  return (
    <div className="min-h-screen bg-[#F8FAFC] p-6">
      {/* ---- 页头 ---- */}
      <div className="mb-6">
        <h1 className="text-2xl font-bold text-gray-900">邀请码管理</h1>
        <p className="mt-1 text-sm text-gray-500">
          生成和管理管理员邀请码，控制新用户注册
        </p>
      </div>

      {/* ---- 错误提示 ---- */}
      {error && (
        <div className="mb-4 rounded-xl border border-red-200 bg-red-50 px-4 py-3 text-sm text-[#EF4444]">
          {error}
        </div>
      )}

      {/* ---- 生成表单 ---- */}
      <div className="mb-6 rounded-xl border border-gray-200 bg-white p-6 shadow-sm">
        <h2 className="mb-4 text-lg font-semibold text-gray-900">生成邀请码</h2>
        <form onSubmit={handleGenerate} className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
          {/* 最大使用次数 */}
          <div>
            <label htmlFor="maxUses" className="mb-1 block text-sm font-medium text-gray-700">
              最大使用次数
            </label>
            <input
              id="maxUses"
              type="number"
              min={1}
              max={9999}
              value={maxUses}
              onChange={(e) => setMaxUses(Number(e.target.value))}
              className="w-full rounded-lg border border-gray-300 px-3 py-2 text-sm transition focus:border-[#3B82F6] focus:outline-none focus:ring-2 focus:ring-[#3B82F6]/20"
              required
            />
          </div>

          {/* 赠送额度 */}
          <div>
            <label htmlFor="bonusQuota" className="mb-1 block text-sm font-medium text-gray-700">
              赠送额度 <span className="text-gray-400">(可选)</span>
            </label>
            <input
              id="bonusQuota"
              type="number"
              min={0}
              value={bonusQuota}
              onChange={(e) => setBonusQuota(e.target.value)}
              placeholder="如: 1000"
              className="w-full rounded-lg border border-gray-300 px-3 py-2 text-sm transition focus:border-[#3B82F6] focus:outline-none focus:ring-2 focus:ring-[#3B82F6]/20"
            />
          </div>

          {/* 过期时间 */}
          <div>
            <label htmlFor="expiresAt" className="mb-1 block text-sm font-medium text-gray-700">
              过期时间 <span className="text-gray-400">(可选)</span>
            </label>
            <input
              id="expiresAt"
              type="datetime-local"
              value={expiresAt}
              onChange={(e) => setExpiresAt(e.target.value)}
              className="w-full rounded-lg border border-gray-300 px-3 py-2 text-sm transition focus:border-[#3B82F6] focus:outline-none focus:ring-2 focus:ring-[#3B82F6]/20"
            />
          </div>

          {/* 提交按钮 */}
          <div className="flex items-end">
            <button
              type="submit"
              disabled={submitting}
              className="w-full rounded-lg bg-[#3B82F6] px-4 py-2 text-sm font-medium text-white transition hover:bg-blue-600 focus:outline-none focus:ring-2 focus:ring-[#3B82F6]/20 disabled:cursor-not-allowed disabled:opacity-50"
            >
              {submitting ? '生成中...' : '生成邀请码'}
            </button>
          </div>
        </form>
      </div>

      {/* ---- 邀请码表格 ---- */}
      <div className="rounded-xl border border-gray-200 bg-white shadow-sm">
        <div className="border-b border-gray-200 px-6 py-4">
          <h2 className="text-lg font-semibold text-gray-900">邀请码列表</h2>
        </div>

        {loading ? (
          <div className="flex items-center justify-center py-16">
            <div className="h-8 w-8 animate-spin rounded-full border-4 border-gray-200 border-t-[#3B82F6]" />
          </div>
        ) : codes.length === 0 ? (
          <div className="py-16 text-center text-sm text-gray-500">
            暂无邀请码，请先生成
          </div>
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full text-left text-sm">
              <thead>
                <tr className="border-b border-gray-100 bg-gray-50/50">
                  <th className="px-6 py-3 font-medium text-gray-500">邀请码</th>
                  <th className="px-6 py-3 font-medium text-gray-500">类型</th>
                  <th className="px-6 py-3 font-medium text-gray-500">创建者</th>
                  <th className="px-6 py-3 font-medium text-gray-500">使用量</th>
                  <th className="px-6 py-3 font-medium text-gray-500">状态</th>
                  <th className="px-6 py-3 font-medium text-gray-500">过期时间</th>
                  <th className="px-6 py-3 font-medium text-gray-500">创建时间</th>
                  <th className="px-6 py-3 font-medium text-gray-500">操作</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-gray-100">
                {codes.map((code) => (
                  <tr key={code.id} className="transition hover:bg-gray-50/50">
                    {/* 邀请码 + 复制 */}
                    <td className="px-6 py-3">
                      <div className="flex items-center gap-2">
                        <code className="rounded bg-gray-100 px-2 py-0.5 font-mono text-xs text-gray-800">
                          {code.code}
                        </code>
                        <CopyButton text={code.code} />
                      </div>
                    </td>

                    {/* 类型 */}
                    <td className="px-6 py-3">
                      <TypeBadge type={code.type} />
                    </td>

                    {/* 创建者 */}
                    <td className="px-6 py-3 text-gray-700">{code.creator_name}</td>

                    {/* 使用量 */}
                    <td className="px-6 py-3">
                      <span className="text-gray-900">{code.used_count}</span>
                      <span className="text-gray-400"> / {code.max_uses}</span>
                    </td>

                    {/* 状态 */}
                    <td className="px-6 py-3">
                      <StatusBadge status={code.status} />
                    </td>

                    {/* 过期时间 */}
                    <td className="px-6 py-3 text-gray-500">
                      {code.expires_at ? formatDate(code.expires_at) : '永不过期'}
                    </td>

                    {/* 创建时间 */}
                    <td className="px-6 py-3 text-gray-500">{formatDate(code.created_at)}</td>

                    {/* 操作 */}
                    <td className="px-6 py-3">
                      {code.status === 'active' && (
                        <button
                          type="button"
                          onClick={() => handleDisable(code.id)}
                          className="rounded-lg px-2.5 py-1 text-xs font-medium text-[#EF4444] transition hover:bg-red-50"
                        >
                          禁用
                        </button>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>
    </div>
  )
}
