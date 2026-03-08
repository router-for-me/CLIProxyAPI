import { useState, useMemo, type FormEvent } from 'react'
import { useNavigate } from 'react-router-dom'
import { useAuthStore } from '../../stores/auth'
import { changePassword, regenerateApiKey } from '../../api/user'
import { useI18n } from '../../i18n'

/* ============================================================
 * 个人设置页 — 个人信息 / API Key / 安全 / 危险操作
 * ============================================================ */

export default function Settings() {
  const navigate = useNavigate()
  const { t, language } = useI18n()
  const isZh = language === 'zh'
  const user = useAuthStore((s) => s.user)
  const setUser = useAuthStore((s) => s.setUser)
  const logout = useAuthStore((s) => s.logout)

  // --------------------------------------------------------
  // 消息提示
  // --------------------------------------------------------
  const [message, setMessage] = useState<{ type: 'success' | 'error'; text: string } | null>(null)

  // --------------------------------------------------------
  // 修改密码表单
  // --------------------------------------------------------
  const [pwForm, setPwForm] = useState({
    currentPassword: '',
    newPassword: '',
    confirmNewPassword: '',
  })
  const [pwLoading, setPwLoading] = useState(false)

  const handleChangePassword = async (e: FormEvent) => {
    e.preventDefault()
    setMessage(null)

    if (!pwForm.currentPassword || !pwForm.newPassword) {
      setMessage({ type: 'error', text: isZh ? '请填写完整' : 'Please fill in all fields' })
      return
    }
    if (pwForm.newPassword.length < 6) {
      setMessage({ type: 'error', text: t('auth.passwordMinLength') })
      return
    }
    if (pwForm.newPassword !== pwForm.confirmNewPassword) {
      setMessage({ type: 'error', text: t('auth.passwordMismatch') })
      return
    }

    setPwLoading(true)
    try {
      await changePassword({
        currentPassword: pwForm.currentPassword,
        newPassword: pwForm.newPassword,
      })
      setMessage({ type: 'success', text: isZh ? '密码已更新' : 'Password updated' })
      setPwForm({ currentPassword: '', newPassword: '', confirmNewPassword: '' })
    } catch (err) {
      setMessage({
        type: 'error',
        text: err instanceof Error ? err.message : t('common.operationFailed'),
      })
    } finally {
      setPwLoading(false)
    }
  }

  // --------------------------------------------------------
  // API Key 显示 / 重新生成
  // --------------------------------------------------------
  const [keyRevealed, setKeyRevealed] = useState(false)
  const [regenLoading, setRegenLoading] = useState(false)

  const maskedKey = useMemo(() => {
    const key = user?.api_key ?? ''
    if (!key) return '••••••••••••••••'
    if (keyRevealed) return key
    return key.length > 8
      ? key.slice(0, 4) + '••••••••' + key.slice(-4)
      : '••••••••'
  }, [user?.api_key, keyRevealed])

  const handleRegenerateKey = async () => {
    const confirmText = isZh
      ? '重新生成 API Key 后，旧 Key 将立即失效。确定继续吗？'
      : 'Regenerating will invalidate your current API Key immediately. Continue?'
    if (!window.confirm(confirmText)) return

    setRegenLoading(true)
    setMessage(null)
    try {
      const data = await regenerateApiKey()
      if (user) {
        setUser({ ...user, api_key: data.api_key })
      }
      setMessage({ type: 'success', text: isZh ? 'API Key 已重新生成' : 'API Key regenerated' })
    } catch (err) {
      setMessage({
        type: 'error',
        text: err instanceof Error ? err.message : t('common.operationFailed'),
      })
    } finally {
      setRegenLoading(false)
    }
  }

  // --------------------------------------------------------
  // 退出登录
  // --------------------------------------------------------
  const handleLogout = () => {
    const confirmText = isZh ? '确定要退出登录吗？' : 'Are you sure you want to logout?'
    if (!window.confirm(confirmText)) return
    logout()
    navigate('/login', { replace: true })
  }

  // --------------------------------------------------------
  // 渲染
  // --------------------------------------------------------
  return (
    <div className="min-h-screen bg-[#F8FAFC] p-6">
      <div className="mx-auto max-w-3xl space-y-6">
        <h1 className="text-2xl font-bold text-gray-900">
          {isZh ? '个人设置' : 'Settings'}
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

        {/* ============================================
         * 个人信息
         * ============================================ */}
        <div className="rounded-xl bg-white p-6 shadow-sm">
          <h2 className="mb-4 text-sm font-semibold text-gray-500 uppercase tracking-wide">
            {isZh ? '个人信息' : 'Profile'}
          </h2>
          <div className="space-y-4">
            {/* 用户名（只读） */}
            <div>
              <label className="mb-1.5 block text-sm font-medium text-gray-700">
                {t('auth.username')}
              </label>
              <input
                type="text"
                value={user?.username ?? ''}
                readOnly
                className="w-full rounded-lg border border-gray-200 bg-gray-100 px-4 py-2.5 text-sm text-gray-500 outline-none cursor-not-allowed"
              />
            </div>
            {/* 邮箱 */}
            <div>
              <label className="mb-1.5 block text-sm font-medium text-gray-700">
                {t('auth.email')}
              </label>
              <input
                type="email"
                value={user?.email ?? ''}
                readOnly
                className="w-full rounded-lg border border-gray-200 bg-gray-100 px-4 py-2.5 text-sm text-gray-500 outline-none cursor-not-allowed"
              />
            </div>
          </div>
        </div>

        {/* ============================================
         * 修改密码
         * ============================================ */}
        <div className="rounded-xl bg-white p-6 shadow-sm">
          <h2 className="mb-4 text-sm font-semibold text-gray-500 uppercase tracking-wide">
            {isZh ? '修改密码' : 'Change Password'}
          </h2>
          <form onSubmit={handleChangePassword} className="space-y-4">
            <div>
              <label htmlFor="set-cur-pw" className="mb-1.5 block text-sm font-medium text-gray-700">
                {isZh ? '当前密码' : 'Current Password'}
              </label>
              <input
                id="set-cur-pw"
                type="password"
                autoComplete="current-password"
                value={pwForm.currentPassword}
                onChange={(e) => setPwForm((p) => ({ ...p, currentPassword: e.target.value }))}
                className="w-full rounded-lg border border-gray-200 bg-gray-50 px-4 py-2.5 text-sm text-gray-900 outline-none transition focus:border-[#3B82F6] focus:ring-2 focus:ring-[#3B82F6]/20"
              />
            </div>
            <div>
              <label htmlFor="set-new-pw" className="mb-1.5 block text-sm font-medium text-gray-700">
                {isZh ? '新密码' : 'New Password'}
              </label>
              <input
                id="set-new-pw"
                type="password"
                autoComplete="new-password"
                value={pwForm.newPassword}
                onChange={(e) => setPwForm((p) => ({ ...p, newPassword: e.target.value }))}
                className="w-full rounded-lg border border-gray-200 bg-gray-50 px-4 py-2.5 text-sm text-gray-900 outline-none transition focus:border-[#3B82F6] focus:ring-2 focus:ring-[#3B82F6]/20"
              />
            </div>
            <div>
              <label htmlFor="set-confirm-pw" className="mb-1.5 block text-sm font-medium text-gray-700">
                {isZh ? '确认新密码' : 'Confirm New Password'}
              </label>
              <input
                id="set-confirm-pw"
                type="password"
                autoComplete="new-password"
                value={pwForm.confirmNewPassword}
                onChange={(e) => setPwForm((p) => ({ ...p, confirmNewPassword: e.target.value }))}
                className="w-full rounded-lg border border-gray-200 bg-gray-50 px-4 py-2.5 text-sm text-gray-900 outline-none transition focus:border-[#3B82F6] focus:ring-2 focus:ring-[#3B82F6]/20"
              />
            </div>
            <button
              type="submit"
              disabled={pwLoading}
              className="rounded-lg bg-[#3B82F6] px-6 py-2.5 text-sm font-medium text-white shadow-sm transition hover:bg-[#2563EB] disabled:cursor-not-allowed disabled:opacity-60"
            >
              {pwLoading ? t('common.loading') : (isZh ? '更新密码' : 'Update Password')}
            </button>
          </form>
        </div>

        {/* ============================================
         * API Key 管理
         * ============================================ */}
        <div className="rounded-xl bg-white p-6 shadow-sm">
          <h2 className="mb-4 text-sm font-semibold text-gray-500 uppercase tracking-wide">
            {isZh ? 'API Key 管理' : 'API Key Management'}
          </h2>
          <div className="flex items-center gap-3">
            <code className="flex-1 truncate rounded-lg bg-gray-50 px-4 py-2.5 font-mono text-sm text-gray-800 select-all">
              {maskedKey}
            </code>
            <button
              type="button"
              onClick={() => setKeyRevealed((v) => !v)}
              className="shrink-0 rounded-lg border border-gray-200 px-3 py-2 text-sm text-gray-600 transition hover:bg-gray-50"
            >
              {keyRevealed ? (isZh ? '隐藏' : 'Hide') : (isZh ? '显示' : 'Show')}
            </button>
            <button
              type="button"
              onClick={handleRegenerateKey}
              disabled={regenLoading}
              className="shrink-0 rounded-lg border border-amber-200 bg-amber-50 px-3 py-2 text-sm font-medium text-amber-700 transition hover:bg-amber-100 disabled:cursor-not-allowed disabled:opacity-60"
            >
              {regenLoading ? '...' : (isZh ? '重新生成' : 'Regenerate')}
            </button>
          </div>
        </div>

        {/* ============================================
         * 安全设置
         * ============================================ */}
        <div className="rounded-xl bg-white p-6 shadow-sm">
          <h2 className="mb-4 text-sm font-semibold text-gray-500 uppercase tracking-wide">
            {isZh ? '安全设置' : 'Security'}
          </h2>
          <div className="rounded-lg border border-gray-100 p-4">
            <p className="text-sm font-medium text-gray-700">
              {isZh ? '活跃会话' : 'Active Sessions'}
            </p>
            <p className="mt-1 text-xs text-gray-400">
              {isZh ? '当前设备已登录' : 'Current device is logged in'}
            </p>
            <div className="mt-3 inline-flex items-center gap-1.5 rounded-full bg-green-50 px-2.5 py-0.5 text-xs font-medium text-green-700">
              <span className="h-1.5 w-1.5 rounded-full bg-[#10B981]" />
              {isZh ? '当前会话' : 'Current Session'}
            </div>
          </div>
        </div>

        {/* ============================================
         * 危险操作
         * ============================================ */}
        <div className="rounded-xl border border-red-100 bg-white p-6 shadow-sm">
          <h2 className="mb-4 text-sm font-semibold text-red-500 uppercase tracking-wide">
            {isZh ? '危险操作' : 'Danger Zone'}
          </h2>
          <p className="mb-4 text-sm text-gray-500">
            {isZh
              ? '退出登录后需要重新输入凭据才能访问面板。'
              : 'You will need to re-enter your credentials to access the panel after logging out.'}
          </p>
          <button
            type="button"
            onClick={handleLogout}
            className="rounded-lg border border-red-200 bg-red-50 px-5 py-2 text-sm font-medium text-red-600 transition hover:bg-red-100"
          >
            {isZh ? '退出登录' : 'Logout'}
          </button>
        </div>
      </div>
    </div>
  )
}
