// ============================================================
// 系统设置页 — 池模式 / SMTP / OAuth / 通用设置
// 每个板块独立保存，互不干扰
// ============================================================

import { useState, useEffect, useCallback } from 'react'
import type { FormEvent, ReactNode } from 'react'
import {
  getPoolMode,
  updatePoolMode,
  getSMTPConfig,
  updateSMTPConfig,
  testSMTPConnection,
  getOAuthProviders,
  createOAuthProvider,
  deleteOAuthProvider,
  toggleOAuthProvider,
  getGeneralSettings,
  updateGeneralSettings,
  type PoolMode,
  type SMTPConfig,
  type OAuthProvider,
  type GeneralSettings,
} from '../../api/admin'

// ============================================================
// 常量 — 池模式配置
// ============================================================

const POOL_MODES: { value: PoolMode; label: string; desc: string }[] = [
  { value: 'public',      label: '公共池 (Public)',        desc: '所有用户共享公共凭证池，适合公益开放模式' },
  { value: 'private',     label: '独立池 (Private)',       desc: '用户只能使用自己上传的凭证，适合高级/付费用户' },
  { value: 'contributor', label: '贡献者池 (Contributor)', desc: '只有上传过凭证的用户才能使用公共池，鼓励贡献' },
]

// ============================================================
// 通用样式
// ============================================================

const INPUT_CLS = 'w-full rounded-lg border border-gray-300 px-3 py-2 text-sm transition focus:border-[#3B82F6] focus:outline-none focus:ring-2 focus:ring-[#3B82F6]/20'

// ============================================================
// 子组件: 板块容器
// ============================================================

function Section({ title, children }: { title: string; children: ReactNode }) {
  return (
    <div className="rounded-xl border border-gray-200 bg-white shadow-sm">
      <div className="border-b border-gray-200 px-6 py-4">
        <h2 className="text-lg font-semibold text-gray-900">{title}</h2>
      </div>
      <div className="p-6">{children}</div>
    </div>
  )
}

// ============================================================
// 子组件: 保存按钮
// ============================================================

function SaveBtn({ saving, onClick, label }: { saving: boolean; onClick: () => void; label?: string }) {
  return (
    <button
      type="button"
      disabled={saving}
      onClick={onClick}
      className="rounded-lg bg-[#3B82F6] px-5 py-2 text-sm font-medium text-white transition hover:bg-blue-600 disabled:cursor-not-allowed disabled:opacity-50"
    >
      {saving ? '保存中...' : label ?? '保存'}
    </button>
  )
}

// ============================================================
// 子组件: 开关按钮
// ============================================================

function Toggle({ checked, onChange }: { checked: boolean; onChange: () => void }) {
  return (
    <button
      type="button"
      role="switch"
      aria-checked={checked}
      onClick={onChange}
      className={`relative inline-flex h-6 w-11 shrink-0 cursor-pointer rounded-full transition-colors ${checked ? 'bg-[#3B82F6]' : 'bg-gray-200'}`}
    >
      <span className={`pointer-events-none inline-block h-5 w-5 translate-y-0.5 rounded-full bg-white shadow-sm transition-transform ${checked ? 'translate-x-[22px]' : 'translate-x-0.5'}`} />
    </button>
  )
}

// ============================================================
// 主组件: SystemSettings
// ============================================================

export default function SystemSettings() {
  /* ---- 通用状态 ---- */
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [successMsg, setSuccessMsg] = useState('')

  /* ---- 池模式 ---- */
  const [poolMode, setPoolMode] = useState<PoolMode>('public')
  const [savingPool, setSavingPool] = useState(false)

  /* ---- SMTP ---- */
  const [smtp, setSMTP] = useState<SMTPConfig>({ host: '', port: 587, username: '', password: '', from: '', use_tls: true })
  const [savingSMTP, setSavingSMTP] = useState(false)
  const [testingSMTP, setTestingSMTP] = useState(false)
  const [smtpResult, setSMTPResult] = useState<{ success: boolean; message: string } | null>(null)

  /* ---- OAuth ---- */
  const [oauthList, setOAuthList] = useState<OAuthProvider[]>([])
  const [showAddOAuth, setShowAddOAuth] = useState(false)
  const [newOAuth, setNewOAuth] = useState({ name: '', provider: 'github', client_id: '', client_secret: '' })

  /* ---- 通用设置 ---- */
  const [general, setGeneral] = useState<GeneralSettings>({
    jwt_secret_masked: '', access_token_ttl: 7200, refresh_token_ttl: 604800,
    email_register_enabled: true, invite_required: false, referral_enabled: true, daily_register_limit: 100,
  })
  const [savingGeneral, setSavingGeneral] = useState(false)

  // ============================================================
  // 数据加载
  // ============================================================

  const loadAll = useCallback(async () => {
    try {
      setLoading(true)
      const [poolRes, smtpRes, oauthRes, genRes] = await Promise.all([
        getPoolMode(), getSMTPConfig(), getOAuthProviders(), getGeneralSettings(),
      ])
      setPoolMode(poolRes.data.mode)
      setSMTP(smtpRes.data)
      setOAuthList(oauthRes.data)
      setGeneral(genRes.data)
      setError('')
    } catch (err) {
      setError(err instanceof Error ? err.message : '加载系统设置失败')
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => { loadAll() }, [loadAll])

  // 成功消息自动消失
  useEffect(() => {
    if (!successMsg) return
    const t = setTimeout(() => setSuccessMsg(''), 3000)
    return () => clearTimeout(t)
  }, [successMsg])

  // ============================================================
  // 事件处理: 池模式
  // ============================================================

  const handleSavePool = async () => {
    setSavingPool(true)
    try { await updatePoolMode(poolMode); setSuccessMsg('池模式已更新') }
    catch (err) { setError(err instanceof Error ? err.message : '保存池模式失败') }
    finally { setSavingPool(false) }
  }

  // ============================================================
  // 事件处理: SMTP
  // ============================================================

  const handleSaveSMTP = async () => {
    setSavingSMTP(true)
    try { await updateSMTPConfig(smtp); setSuccessMsg('SMTP 配置已更新') }
    catch (err) { setError(err instanceof Error ? err.message : '保存 SMTP 失败') }
    finally { setSavingSMTP(false) }
  }

  const handleTestSMTP = async () => {
    setTestingSMTP(true); setSMTPResult(null)
    try { const r = await testSMTPConnection(); setSMTPResult(r.data) }
    catch (err) { setSMTPResult({ success: false, message: err instanceof Error ? err.message : '测试失败' }) }
    finally { setTestingSMTP(false) }
  }

  // ============================================================
  // 事件处理: OAuth
  // ============================================================

  const handleAddOAuth = async (e: FormEvent) => {
    e.preventDefault()
    try {
      await createOAuthProvider(newOAuth)
      setNewOAuth({ name: '', provider: 'github', client_id: '', client_secret: '' })
      setShowAddOAuth(false)
      const res = await getOAuthProviders()
      setOAuthList(res.data)
      setSuccessMsg('OAuth 提供商已添加')
    } catch (err) { setError(err instanceof Error ? err.message : '添加 OAuth 失败') }
  }

  const handleDeleteOAuth = async (id: number) => {
    try {
      await deleteOAuthProvider(id)
      setOAuthList((p) => p.filter((v) => v.id !== id))
      setSuccessMsg('OAuth 提供商已删除')
    } catch (err) { setError(err instanceof Error ? err.message : '删除 OAuth 失败') }
  }

  const handleToggleOAuth = async (id: number, enabled: boolean) => {
    try {
      await toggleOAuthProvider(id, enabled)
      setOAuthList((p) => p.map((v) => v.id === id ? { ...v, enabled } : v))
    } catch (err) { setError(err instanceof Error ? err.message : '切换 OAuth 失败') }
  }

  // ============================================================
  // 事件处理: 通用设置
  // ============================================================

  const handleSaveGeneral = async () => {
    setSavingGeneral(true)
    try {
      await updateGeneralSettings({
        access_token_ttl: general.access_token_ttl,
        refresh_token_ttl: general.refresh_token_ttl,
        email_register_enabled: general.email_register_enabled,
        invite_required: general.invite_required,
        referral_enabled: general.referral_enabled,
        daily_register_limit: general.daily_register_limit,
      })
      setSuccessMsg('通用设置已更新')
    } catch (err) { setError(err instanceof Error ? err.message : '保存通用设置失败') }
    finally { setSavingGeneral(false) }
  }

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
        <h1 className="text-2xl font-bold text-gray-900">系统设置</h1>
        <p className="mt-1 text-sm text-gray-500">配置凭证池模式、邮件服务、OAuth 登录和通用参数</p>
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
         *  池模式
         * ============================================================ */}
        <Section title="凭证池模式">
          <div className="space-y-3">
            {POOL_MODES.map((opt) => (
              <label
                key={opt.value}
                className={`flex cursor-pointer items-start gap-3 rounded-xl border p-4 transition ${
                  poolMode === opt.value ? 'border-blue-300 bg-blue-50/50 ring-1 ring-blue-200' : 'border-gray-200 hover:border-gray-300'
                }`}
              >
                <input type="radio" name="poolMode" value={opt.value} checked={poolMode === opt.value} onChange={() => setPoolMode(opt.value)} className="mt-0.5 h-4 w-4 border-gray-300 text-[#3B82F6] focus:ring-[#3B82F6]" />
                <div>
                  <span className="text-sm font-semibold text-gray-900">{opt.label}</span>
                  <p className="mt-0.5 text-xs text-gray-500">{opt.desc}</p>
                </div>
              </label>
            ))}
          </div>
          <div className="mt-4 flex justify-end">
            <SaveBtn saving={savingPool} onClick={handleSavePool} />
          </div>
        </Section>

        {/* ============================================================
         *  SMTP 配置
         * ============================================================ */}
        <Section title="SMTP 邮件配置">
          <div className="grid gap-4 sm:grid-cols-2">
            <div>
              <label htmlFor="smtpHost" className="mb-1 block text-sm font-medium text-gray-700">SMTP 主机</label>
              <input id="smtpHost" type="text" value={smtp.host} onChange={(e) => setSMTP((s) => ({ ...s, host: e.target.value }))} placeholder="smtp.qq.com" className={INPUT_CLS} />
            </div>
            <div>
              <label htmlFor="smtpPort" className="mb-1 block text-sm font-medium text-gray-700">端口</label>
              <input id="smtpPort" type="number" value={smtp.port} onChange={(e) => setSMTP((s) => ({ ...s, port: Number(e.target.value) }))} className={INPUT_CLS} />
            </div>
            <div>
              <label htmlFor="smtpUser" className="mb-1 block text-sm font-medium text-gray-700">用户名</label>
              <input id="smtpUser" type="text" value={smtp.username} onChange={(e) => setSMTP((s) => ({ ...s, username: e.target.value }))} className={INPUT_CLS} />
            </div>
            <div>
              <label htmlFor="smtpPass" className="mb-1 block text-sm font-medium text-gray-700">密码</label>
              <input id="smtpPass" type="password" value={smtp.password} onChange={(e) => setSMTP((s) => ({ ...s, password: e.target.value }))} placeholder="SMTP 授权码" className={INPUT_CLS} />
            </div>
            <div>
              <label htmlFor="smtpFrom" className="mb-1 block text-sm font-medium text-gray-700">发件人地址</label>
              <input id="smtpFrom" type="email" value={smtp.from} onChange={(e) => setSMTP((s) => ({ ...s, from: e.target.value }))} placeholder="noreply@example.com" className={INPUT_CLS} />
            </div>
            <div className="flex items-end">
              <label className="flex items-center gap-2 pb-2 text-sm text-gray-700">
                <input type="checkbox" checked={smtp.use_tls} onChange={(e) => setSMTP((s) => ({ ...s, use_tls: e.target.checked }))} className="h-4 w-4 rounded border-gray-300 text-[#3B82F6] focus:ring-[#3B82F6]" />
                启用 TLS 加密
              </label>
            </div>
          </div>

          {smtpResult && (
            <div className={`mt-3 rounded-lg px-4 py-2.5 text-sm ${smtpResult.success ? 'border border-emerald-200 bg-emerald-50 text-[#10B981]' : 'border border-red-200 bg-red-50 text-[#EF4444]'}`}>
              {smtpResult.message}
            </div>
          )}

          <div className="mt-4 flex justify-end gap-3">
            <button type="button" disabled={testingSMTP} onClick={handleTestSMTP} className="rounded-lg border border-gray-300 px-4 py-2 text-sm font-medium text-gray-600 transition hover:bg-gray-50 disabled:opacity-50">
              {testingSMTP ? '测试中...' : '发送测试邮件'}
            </button>
            <SaveBtn saving={savingSMTP} onClick={handleSaveSMTP} />
          </div>
        </Section>

        {/* ============================================================
         *  OAuth 提供商
         * ============================================================ */}
        <Section title="OAuth 登录提供商">
          {oauthList.length > 0 && (
            <div className="mb-4 space-y-2">
              {oauthList.map((prov) => (
                <div key={prov.id} className="flex items-center justify-between rounded-xl border border-gray-200 p-4">
                  <div className="flex items-center gap-3">
                    <div className={`flex h-9 w-9 items-center justify-center rounded-lg text-sm font-bold ${prov.enabled ? 'bg-blue-50 text-[#3B82F6]' : 'bg-gray-100 text-gray-400'}`}>
                      {prov.provider.charAt(0).toUpperCase()}
                    </div>
                    <div>
                      <p className="text-sm font-medium text-gray-900">{prov.name}</p>
                      <p className="text-xs text-gray-500">{prov.provider} &middot; {prov.client_id.slice(0, 8)}...</p>
                    </div>
                  </div>
                  <div className="flex items-center gap-2">
                    <Toggle checked={prov.enabled} onChange={() => handleToggleOAuth(prov.id, !prov.enabled)} />
                    <button type="button" onClick={() => handleDeleteOAuth(prov.id)} className="rounded-lg p-1.5 text-gray-400 transition hover:bg-red-50 hover:text-[#EF4444]">
                      <svg className="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={2}>
                        <path strokeLinecap="round" strokeLinejoin="round" d="M19 7l-.867 12.142A2 2 0 0116.138 21H7.862a2 2 0 01-1.995-1.858L5 7m5 4v6m4-6v6m1-10V4a1 1 0 00-1-1h-4a1 1 0 00-1 1v3M4 7h16" />
                      </svg>
                    </button>
                  </div>
                </div>
              ))}
            </div>
          )}

          {showAddOAuth ? (
            <form onSubmit={handleAddOAuth} className="rounded-xl border border-dashed border-gray-300 p-4">
              <h4 className="mb-3 text-sm font-semibold text-gray-700">添加新提供商</h4>
              <div className="grid gap-3 sm:grid-cols-2">
                <div>
                  <label htmlFor="oName" className="mb-1 block text-sm font-medium text-gray-700">显示名称</label>
                  <input id="oName" type="text" value={newOAuth.name} onChange={(e) => setNewOAuth((o) => ({ ...o, name: e.target.value }))} placeholder="GitHub OAuth" className={INPUT_CLS} required />
                </div>
                <div>
                  <label htmlFor="oProv" className="mb-1 block text-sm font-medium text-gray-700">提供商类型</label>
                  <select id="oProv" value={newOAuth.provider} onChange={(e) => setNewOAuth((o) => ({ ...o, provider: e.target.value }))} className={INPUT_CLS}>
                    <option value="github">GitHub</option>
                    <option value="google">Google</option>
                    <option value="microsoft">Microsoft</option>
                    <option value="discord">Discord</option>
                  </select>
                </div>
                <div>
                  <label htmlFor="oCid" className="mb-1 block text-sm font-medium text-gray-700">Client ID</label>
                  <input id="oCid" type="text" value={newOAuth.client_id} onChange={(e) => setNewOAuth((o) => ({ ...o, client_id: e.target.value }))} className={INPUT_CLS} required />
                </div>
                <div>
                  <label htmlFor="oSec" className="mb-1 block text-sm font-medium text-gray-700">Client Secret</label>
                  <input id="oSec" type="password" value={newOAuth.client_secret} onChange={(e) => setNewOAuth((o) => ({ ...o, client_secret: e.target.value }))} className={INPUT_CLS} required />
                </div>
              </div>
              <div className="mt-4 flex justify-end gap-2">
                <button type="button" onClick={() => setShowAddOAuth(false)} className="rounded-lg border border-gray-300 px-4 py-2 text-sm font-medium text-gray-600 transition hover:bg-gray-50">取消</button>
                <button type="submit" className="rounded-lg bg-[#3B82F6] px-4 py-2 text-sm font-medium text-white transition hover:bg-blue-600">添加</button>
              </div>
            </form>
          ) : (
            <button type="button" onClick={() => setShowAddOAuth(true)} className="flex w-full items-center justify-center gap-2 rounded-xl border border-dashed border-gray-300 py-3 text-sm font-medium text-gray-500 transition hover:border-blue-300 hover:text-[#3B82F6]">
              <svg className="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={2}><path strokeLinecap="round" strokeLinejoin="round" d="M12 4v16m8-8H4" /></svg>
              添加 OAuth 提供商
            </button>
          )}
        </Section>

        {/* ============================================================
         *  通用设置
         * ============================================================ */}
        <Section title="通用设置">
          <div className="space-y-5">
            {/* JWT 密钥 */}
            <div>
              <label htmlFor="jwtSec" className="mb-1 block text-sm font-medium text-gray-700">JWT 密钥</label>
              <input id="jwtSec" type="password" value={general.jwt_secret_masked} readOnly className={`${INPUT_CLS} cursor-not-allowed bg-gray-50`} />
              <p className="mt-1 text-xs text-gray-400">JWT 密钥仅在配置文件中修改，此处仅展示掩码</p>
            </div>

            {/* Token TTL */}
            <div className="grid gap-4 sm:grid-cols-2">
              <div>
                <label htmlFor="aTTL" className="mb-1 block text-sm font-medium text-gray-700">Access Token TTL (秒)</label>
                <input id="aTTL" type="number" min={300} value={general.access_token_ttl} onChange={(e) => setGeneral((g) => ({ ...g, access_token_ttl: Number(e.target.value) }))} className={INPUT_CLS} />
                <p className="mt-1 text-xs text-gray-400">默认 7200 秒 (2 小时)</p>
              </div>
              <div>
                <label htmlFor="rTTL" className="mb-1 block text-sm font-medium text-gray-700">Refresh Token TTL (秒)</label>
                <input id="rTTL" type="number" min={3600} value={general.refresh_token_ttl} onChange={(e) => setGeneral((g) => ({ ...g, refresh_token_ttl: Number(e.target.value) }))} className={INPUT_CLS} />
                <p className="mt-1 text-xs text-gray-400">默认 604800 秒 (7 天)</p>
              </div>
            </div>

            {/* 每日注册上限 */}
            <div>
              <label htmlFor="dLimit" className="mb-1 block text-sm font-medium text-gray-700">每日注册上限</label>
              <input id="dLimit" type="number" min={0} value={general.daily_register_limit} onChange={(e) => setGeneral((g) => ({ ...g, daily_register_limit: Number(e.target.value) }))} className={`${INPUT_CLS} max-w-xs`} />
              <p className="mt-1 text-xs text-gray-400">设置为 0 表示不限制</p>
            </div>

            {/* 注册开关组 */}
            <div className="space-y-3 rounded-xl border border-gray-200 p-4">
              <h4 className="text-sm font-semibold text-gray-700">注册控制</h4>

              <div className="flex items-center justify-between">
                <div>
                  <span className="text-sm text-gray-900">允许邮箱注册</span>
                  <p className="text-xs text-gray-500">启用后用户可通过邮箱验证码注册</p>
                </div>
                <Toggle checked={general.email_register_enabled} onChange={() => setGeneral((g) => ({ ...g, email_register_enabled: !g.email_register_enabled }))} />
              </div>

              <div className="flex items-center justify-between">
                <div>
                  <span className="text-sm text-gray-900">需要邀请码注册</span>
                  <p className="text-xs text-gray-500">启用后新用户必须持有邀请码才能注册</p>
                </div>
                <Toggle checked={general.invite_required} onChange={() => setGeneral((g) => ({ ...g, invite_required: !g.invite_required }))} />
              </div>

              <div className="flex items-center justify-between">
                <div>
                  <span className="text-sm text-gray-900">启用用户裂变</span>
                  <p className="text-xs text-gray-500">启用后老用户可分享裂变码邀请新用户，双方获得奖励</p>
                </div>
                <Toggle checked={general.referral_enabled} onChange={() => setGeneral((g) => ({ ...g, referral_enabled: !g.referral_enabled }))} />
              </div>
            </div>
          </div>

          <div className="mt-5 flex justify-end">
            <SaveBtn saving={savingGeneral} onClick={handleSaveGeneral} />
          </div>
        </Section>
      </div>
    </div>
  )
}
