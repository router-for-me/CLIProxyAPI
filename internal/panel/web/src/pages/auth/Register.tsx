import { useState, useCallback, useEffect, useRef, type FormEvent } from 'react'
import { useNavigate, Link } from 'react-router-dom'
import { useI18n } from '../../i18n'
import { register, sendVerificationCode } from '../../api/auth'

/* ============================================================
 * 注册页 — 居中卡片布局
 * 包含：用户名 / 邮箱 / 密码 / 确认密码 / 邀请码 / 邮箱验证码
 * ============================================================ */

/* 邮箱正则 */
const EMAIL_RE = /^[^\s@]+@[^\s@]+\.[^\s@]+$/

/* 验证码冷却秒数 */
const CODE_COOLDOWN = 60

export default function Register() {
  const navigate = useNavigate()
  const { t } = useI18n()

  // --------------------------------------------------------
  // 表单状态
  // --------------------------------------------------------
  const [form, setForm] = useState({
    username: '',
    email: '',
    password: '',
    confirmPassword: '',
    inviteCode: '',
    verificationCode: '',
  })
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(false)

  /* 验证码冷却计时器 */
  const [codeCooldown, setCodeCooldown] = useState(0)
  const [codeSending, setCodeSending] = useState(false)
  const timerRef = useRef<ReturnType<typeof setInterval> | null>(null)

  /* 组件卸载时清理计时器 */
  useEffect(() => {
    return () => {
      if (timerRef.current) clearInterval(timerRef.current)
    }
  }, [])

  // --------------------------------------------------------
  // 字段更新器
  // --------------------------------------------------------
  const updateField = useCallback(
    (key: keyof typeof form) => (e: React.ChangeEvent<HTMLInputElement>) => {
      setForm((prev) => ({ ...prev, [key]: e.target.value }))
    },
    [],
  )

  // --------------------------------------------------------
  // 发送验证码
  // --------------------------------------------------------
  const handleSendCode = async () => {
    if (codeCooldown > 0 || codeSending) return

    /* 校验邮箱格式 */
    if (!form.email || !EMAIL_RE.test(form.email)) {
      setError(t('auth.invalidEmail'))
      return
    }

    setCodeSending(true)
    setError('')
    try {
      await sendVerificationCode(form.email)
      /* 启动冷却倒计时 */
      setCodeCooldown(CODE_COOLDOWN)
      timerRef.current = setInterval(() => {
        setCodeCooldown((prev) => {
          if (prev <= 1) {
            if (timerRef.current) clearInterval(timerRef.current)
            timerRef.current = null
            return 0
          }
          return prev - 1
        })
      }, 1000)
    } catch (err) {
      setError(err instanceof Error ? err.message : t('common.operationFailed'))
    } finally {
      setCodeSending(false)
    }
  }

  // --------------------------------------------------------
  // 表单校验
  // --------------------------------------------------------
  const validate = (): string | null => {
    if (!form.username.trim()) return t('auth.usernameRequired')
    if (!form.email || !EMAIL_RE.test(form.email)) return t('auth.invalidEmail')
    if (!form.password) return t('auth.passwordRequired')
    if (form.password.length < 6) return t('auth.passwordMinLength')
    if (form.password !== form.confirmPassword) return t('auth.passwordMismatch')
    if (!form.verificationCode.trim()) {
      return t('auth.emailCodeRequired')
    }
    return null
  }

  // --------------------------------------------------------
  // 提交注册
  // --------------------------------------------------------
  const handleSubmit = async (e: FormEvent) => {
    e.preventDefault()
    setError('')

    const validationError = validate()
    if (validationError) {
      setError(validationError)
      return
    }

    setLoading(true)
    try {
      await register({
        username: form.username.trim(),
        email: form.email.trim(),
        password: form.password,
        invite_code: form.inviteCode.trim() || undefined,
        email_code: form.verificationCode.trim(),
      })
      navigate('/login', { replace: true })
    } catch (err) {
      setError(err instanceof Error ? err.message : t('auth.registerFailed'))
    } finally {
      setLoading(false)
    }
  }

  // --------------------------------------------------------
  // 渲染
  // --------------------------------------------------------
  return (
    <div className="flex min-h-screen items-center justify-center bg-[#F8FAFC] px-4 py-8">
      <div className="w-full max-w-md">
        {/* ---- Logo / 标题 ---- */}
        <div className="mb-8 text-center">
          <div className="mx-auto mb-4 flex h-14 w-14 items-center justify-center rounded-xl bg-[#3B82F6] shadow-sm">
            <svg className="h-7 w-7 text-white" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={2}>
              <path strokeLinecap="round" strokeLinejoin="round" d="M18 9v3m0 0v3m0-3h3m-3 0h-3m-2-5a4 4 0 11-8 0 4 4 0 018 0zM3 20a6 6 0 0112 0v1H3v-1z" />
            </svg>
          </div>
          <h1 className="text-2xl font-bold text-gray-900">{t('auth.registerTitle')}</h1>
          <p className="mt-1 text-sm text-gray-500">{t('auth.registerSubtitle')}</p>
        </div>

        {/* ---- 注册卡片 ---- */}
        <div className="rounded-xl bg-white p-8 shadow-sm">
          {error && (
            <div className="mb-4 rounded-lg bg-red-50 px-4 py-3 text-sm text-red-600">
              {error}
            </div>
          )}

          <form onSubmit={handleSubmit} className="space-y-4">
            {/* 用户名 */}
            <div>
              <label htmlFor="reg-username" className="mb-1.5 block text-sm font-medium text-gray-700">
                {t('auth.username')}
              </label>
              <input
                id="reg-username"
                type="text"
                autoComplete="username"
                value={form.username}
                onChange={updateField('username')}
                placeholder={t('auth.username')}
                className="w-full rounded-lg border border-gray-200 bg-gray-50 px-4 py-2.5 text-sm text-gray-900 outline-none transition focus:border-[#3B82F6] focus:ring-2 focus:ring-[#3B82F6]/20"
              />
            </div>

            {/* 邮箱 */}
            <div>
              <label htmlFor="reg-email" className="mb-1.5 block text-sm font-medium text-gray-700">
                {t('auth.email')}
              </label>
              <input
                id="reg-email"
                type="email"
                autoComplete="email"
                value={form.email}
                onChange={updateField('email')}
                placeholder={t('auth.email')}
                className="w-full rounded-lg border border-gray-200 bg-gray-50 px-4 py-2.5 text-sm text-gray-900 outline-none transition focus:border-[#3B82F6] focus:ring-2 focus:ring-[#3B82F6]/20"
              />
            </div>

            {/* 密码 */}
            <div>
              <label htmlFor="reg-password" className="mb-1.5 block text-sm font-medium text-gray-700">
                {t('auth.password')}
              </label>
              <input
                id="reg-password"
                type="password"
                autoComplete="new-password"
                value={form.password}
                onChange={updateField('password')}
                placeholder={t('auth.password')}
                className="w-full rounded-lg border border-gray-200 bg-gray-50 px-4 py-2.5 text-sm text-gray-900 outline-none transition focus:border-[#3B82F6] focus:ring-2 focus:ring-[#3B82F6]/20"
              />
            </div>

            {/* 确认密码 */}
            <div>
              <label htmlFor="reg-confirm" className="mb-1.5 block text-sm font-medium text-gray-700">
                {t('auth.confirmPassword')}
              </label>
              <input
                id="reg-confirm"
                type="password"
                autoComplete="new-password"
                value={form.confirmPassword}
                onChange={updateField('confirmPassword')}
                placeholder={t('auth.confirmPassword')}
                className="w-full rounded-lg border border-gray-200 bg-gray-50 px-4 py-2.5 text-sm text-gray-900 outline-none transition focus:border-[#3B82F6] focus:ring-2 focus:ring-[#3B82F6]/20"
              />
            </div>

            {/* 邀请码（选填） */}
            <div>
              <label htmlFor="reg-invite" className="mb-1.5 block text-sm font-medium text-gray-700">
                {t('auth.inviteCode')}
                <span className="ml-1 text-xs text-gray-400">
                  ({t('auth.optional')})
                </span>
              </label>
              <input
                id="reg-invite"
                type="text"
                value={form.inviteCode}
                onChange={updateField('inviteCode')}
                placeholder={t('auth.inviteCode')}
                className="w-full rounded-lg border border-gray-200 bg-gray-50 px-4 py-2.5 text-sm text-gray-900 outline-none transition focus:border-[#3B82F6] focus:ring-2 focus:ring-[#3B82F6]/20"
              />
            </div>

            {/* 邮箱验证码 */}
            <div>
              <label htmlFor="reg-code" className="mb-1.5 block text-sm font-medium text-gray-700">
                {t('auth.emailCode')}
              </label>
              <div className="flex gap-3">
                <input
                  id="reg-code"
                  type="text"
                  inputMode="numeric"
                  value={form.verificationCode}
                  onChange={updateField('verificationCode')}
                  placeholder={t('auth.emailCode')}
                  className="flex-1 rounded-lg border border-gray-200 bg-gray-50 px-4 py-2.5 text-sm text-gray-900 outline-none transition focus:border-[#3B82F6] focus:ring-2 focus:ring-[#3B82F6]/20"
                />
                <button
                  type="button"
                  onClick={handleSendCode}
                  disabled={codeCooldown > 0 || codeSending}
                  className="shrink-0 rounded-lg border border-[#3B82F6] px-4 py-2.5 text-sm font-medium text-[#3B82F6] transition hover:bg-[#3B82F6]/5 disabled:cursor-not-allowed disabled:border-gray-200 disabled:text-gray-400"
                >
                  {codeSending
                    ? '...'
                    : codeCooldown > 0
                      ? `${codeCooldown}s`
                      : t('auth.sendCode')}
                </button>
              </div>
            </div>

            {/* 注册按钮 */}
            <button
              type="submit"
              disabled={loading}
              className="w-full rounded-lg bg-[#3B82F6] px-4 py-2.5 text-sm font-medium text-white shadow-sm transition hover:bg-[#2563EB] focus:outline-none focus:ring-2 focus:ring-[#3B82F6]/50 disabled:cursor-not-allowed disabled:opacity-60"
            >
              {loading ? t('common.loading') : t('auth.register')}
            </button>
          </form>

          {/* 跳转登录 */}
          <p className="mt-6 text-center text-sm text-gray-500">
            {t('auth.hasAccount')}{' '}
            <Link to="/login" className="font-medium text-[#3B82F6] hover:text-[#2563EB]">
              {t('auth.goToLogin')}
            </Link>
          </p>
        </div>
      </div>
    </div>
  )
}
