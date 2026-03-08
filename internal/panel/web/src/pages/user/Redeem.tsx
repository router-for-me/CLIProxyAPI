import { useEffect, useState } from 'react'
import { useUserStore, type RedemptionTemplate } from '../../stores/user'
import { redeemCode, claimTemplate } from '../../api/user'
import { useI18n } from '../../i18n'

/* ============================================================
 * 兑换中心 — 兑换码输入 + 可用模板 + 裂变推荐
 * ============================================================ */

export default function Redeem() {
  const { t, language } = useI18n()
  const isZh = language === 'zh'
  const {
    templates, referral, loading,
    fetchTemplates, fetchReferral,
  } = useUserStore()

  // --------------------------------------------------------
  // 初始加载
  // --------------------------------------------------------
  useEffect(() => {
    fetchTemplates()
    fetchReferral()
  }, [fetchTemplates, fetchReferral])

  // --------------------------------------------------------
  // 兑换码输入
  // --------------------------------------------------------
  const [code, setCode] = useState('')
  const [redeemLoading, setRedeemLoading] = useState(false)
  const [message, setMessage] = useState<{ type: 'success' | 'error'; text: string } | null>(null)

  const handleRedeem = async () => {
    if (!code.trim()) return
    setMessage(null)
    setRedeemLoading(true)
    try {
      await redeemCode(code.trim())
      setMessage({ type: 'success', text: isZh ? '兑换成功' : 'Redeemed successfully' })
      setCode('')
    } catch (err) {
      setMessage({
        type: 'error',
        text: err instanceof Error ? err.message : t('common.operationFailed'),
      })
    } finally {
      setRedeemLoading(false)
    }
  }

  // --------------------------------------------------------
  // 领取模板
  // --------------------------------------------------------
  const [claimingId, setClaimingId] = useState<string | null>(null)

  const handleClaim = async (tmpl: RedemptionTemplate) => {
    if (!tmpl.available || claimingId) return
    setClaimingId(tmpl.id)
    setMessage(null)
    try {
      await claimTemplate(tmpl.id)
      setMessage({ type: 'success', text: isZh ? '领取成功' : 'Claimed successfully' })
      fetchTemplates()
    } catch (err) {
      setMessage({
        type: 'error',
        text: err instanceof Error ? err.message : t('common.operationFailed'),
      })
    } finally {
      setClaimingId(null)
    }
  }

  // --------------------------------------------------------
  // 推荐码复制
  // --------------------------------------------------------
  const [refCopied, setRefCopied] = useState(false)

  const handleCopyReferral = async () => {
    if (!referral?.code) return
    try {
      await navigator.clipboard.writeText(referral.code)
      setRefCopied(true)
      setTimeout(() => setRefCopied(false), 2000)
    } catch { /* 静默降级 */ }
  }

  // --------------------------------------------------------
  // 渲染
  // --------------------------------------------------------
  return (
    <div className="min-h-screen bg-[#F8FAFC] p-6">
      <div className="mx-auto max-w-6xl space-y-6">
        <h1 className="text-2xl font-bold text-gray-900">
          {isZh ? '兑换中心' : 'Redemption Center'}
        </h1>

        {/* ---- 消息提示 ---- */}
        {message && (
          <div
            className={`rounded-lg px-4 py-3 text-sm ${
              message.type === 'success'
                ? 'bg-green-50 text-green-700'
                : 'bg-red-50 text-red-600'
            }`}
          >
            {message.text}
          </div>
        )}

        {/* ---- 兑换码输入 ---- */}
        <div className="rounded-xl bg-white p-6 shadow-sm">
          <h2 className="mb-4 text-sm font-semibold text-gray-500 uppercase tracking-wide">
            {isZh ? '输入兑换码 / 邀请码' : 'Enter Redemption / Invite Code'}
          </h2>
          <div className="flex gap-3">
            <input
              type="text"
              value={code}
              onChange={(e) => setCode(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === 'Enter') handleRedeem()
              }}
              placeholder={isZh ? '请输入兑换码或邀请码' : 'Enter code here'}
              className="flex-1 rounded-lg border border-gray-200 bg-gray-50 px-4 py-2.5 text-sm text-gray-900 outline-none transition focus:border-[#3B82F6] focus:ring-2 focus:ring-[#3B82F6]/20"
            />
            <button
              type="button"
              onClick={handleRedeem}
              disabled={redeemLoading || !code.trim()}
              className="shrink-0 rounded-lg bg-[#3B82F6] px-6 py-2.5 text-sm font-medium text-white shadow-sm transition hover:bg-[#2563EB] disabled:cursor-not-allowed disabled:opacity-60"
            >
              {redeemLoading ? '...' : (isZh ? '兑换' : 'Redeem')}
            </button>
          </div>
        </div>

        {/* ---- 可用兑换模板 ---- */}
        <div className="rounded-xl bg-white p-6 shadow-sm">
          <h2 className="mb-4 text-sm font-semibold text-gray-500 uppercase tracking-wide">
            {isZh ? '可用兑换模板' : 'Available Templates'}
          </h2>
          {loading ? (
            <div className="py-10 text-center text-sm text-gray-400">{t('common.loading')}</div>
          ) : templates.length === 0 ? (
            <div className="py-10 text-center text-sm text-gray-400">
              {t('common.noData')}
            </div>
          ) : (
            <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-3">
              {templates.map((tmpl) => (
                <div
                  key={tmpl.id}
                  className="flex flex-col rounded-xl border border-gray-100 p-5 transition hover:shadow-sm"
                >
                  <h3 className="text-base font-semibold text-gray-900">{tmpl.name}</h3>
                  <p className="mt-1 text-sm text-gray-500">{tmpl.description}</p>

                  {/* 赠送详情 */}
                  <div className="mt-3 space-y-1">
                    {tmpl.bonusRequests > 0 && (
                      <div className="flex items-center gap-2 text-xs text-gray-600">
                        <svg className="h-3.5 w-3.5 text-[#3B82F6]" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={2}>
                          <path strokeLinecap="round" strokeLinejoin="round" d="M12 4v16m8-8H4" />
                        </svg>
                        {tmpl.bonusRequests} {isZh ? '次请求' : 'requests'}
                      </div>
                    )}
                    {tmpl.bonusTokens > 0 && (
                      <div className="flex items-center gap-2 text-xs text-gray-600">
                        <svg className="h-3.5 w-3.5 text-[#10B981]" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={2}>
                          <path strokeLinecap="round" strokeLinejoin="round" d="M12 4v16m8-8H4" />
                        </svg>
                        {tmpl.bonusTokens.toLocaleString()} tokens
                      </div>
                    )}
                    <div className="text-xs text-gray-400">
                      {isZh ? '适用模型' : 'Model'}: {tmpl.modelPattern}
                    </div>
                  </div>

                  {/* 领取按钮 */}
                  <div className="mt-auto pt-4">
                    <button
                      type="button"
                      onClick={() => handleClaim(tmpl)}
                      disabled={!tmpl.available || claimingId === tmpl.id}
                      className={`w-full rounded-lg px-4 py-2 text-sm font-medium transition ${
                        tmpl.available
                          ? 'bg-[#10B981] text-white hover:bg-[#059669] disabled:opacity-60'
                          : 'cursor-not-allowed bg-gray-100 text-gray-400'
                      }`}
                    >
                      {claimingId === tmpl.id
                        ? '...'
                        : tmpl.available
                          ? (isZh ? '领取' : 'Claim')
                          : (isZh ? '已领取' : 'Claimed')}
                    </button>
                  </div>
                </div>
              ))}
            </div>
          )}
        </div>

        {/* ---- 我的推荐 ---- */}
        {referral && (
          <div className="rounded-xl bg-white p-6 shadow-sm">
            <h2 className="mb-4 text-sm font-semibold text-gray-500 uppercase tracking-wide">
              {isZh ? '我的推荐' : 'My Referrals'}
            </h2>
            <div className="grid grid-cols-1 gap-4 sm:grid-cols-3">
              {/* 推荐码 */}
              <div className="rounded-lg border border-gray-100 p-4">
                <p className="text-xs text-gray-500">{isZh ? '推荐码' : 'Referral Code'}</p>
                <div className="mt-2 flex items-center gap-2">
                  <code className="flex-1 truncate font-mono text-sm font-medium text-gray-900">
                    {referral.code}
                  </code>
                  <button
                    type="button"
                    onClick={handleCopyReferral}
                    className="shrink-0 rounded-md border border-gray-200 px-2.5 py-1 text-xs text-gray-600 transition hover:bg-gray-50"
                  >
                    {refCopied ? t('common.copied') : t('common.copy')}
                  </button>
                </div>
              </div>

              {/* 推荐人数 */}
              <div className="rounded-lg border border-gray-100 p-4">
                <p className="text-xs text-gray-500">{isZh ? '已推荐人数' : 'Total Referrals'}</p>
                <p className="mt-2 text-xl font-bold text-gray-900">
                  {referral.totalReferrals}
                </p>
              </div>

              {/* 获得奖励 */}
              <div className="rounded-lg border border-gray-100 p-4">
                <p className="text-xs text-gray-500">{isZh ? '获得奖励' : 'Rewards Earned'}</p>
                <p className="mt-2 text-xl font-bold text-[#10B981]">
                  {referral.totalRewards}
                </p>
              </div>
            </div>
          </div>
        )}
      </div>
    </div>
  )
}
