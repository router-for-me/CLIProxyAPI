import { useEffect, useState, type FormEvent } from 'react'
import { useUserStore, type Credential } from '../../stores/user'
import { uploadCredential, deleteCredential } from '../../api/user'
import { useI18n } from '../../i18n'

/* ============================================================
 * 凭证管理页 — 上传表单 + 凭证列表 + 健康状态
 * ============================================================ */

/* 支持的供应商列表 */
const PROVIDERS = [
  { value: 'gemini', label: 'Google Gemini' },
  { value: 'claude', label: 'Anthropic Claude' },
  { value: 'openai', label: 'OpenAI' },
  { value: 'codex', label: 'Codex' },
  { value: 'other', label: 'Other' },
]

/* 供应商图标颜色映射 */
const PROVIDER_COLORS: Record<string, string> = {
  gemini: 'bg-blue-100 text-blue-700',
  claude: 'bg-orange-100 text-orange-700',
  openai: 'bg-emerald-100 text-emerald-700',
  codex: 'bg-purple-100 text-purple-700',
  other: 'bg-gray-100 text-gray-700',
}

/* 健康状态配色 */
const HEALTH_STYLES: Record<Credential['health'], { dot: string; badge: string }> = {
  healthy: { dot: 'bg-[#10B981]', badge: 'bg-green-50 text-green-700' },
  unhealthy: { dot: 'bg-red-400', badge: 'bg-red-50 text-red-600' },
  unknown: { dot: 'bg-gray-300', badge: 'bg-gray-50 text-gray-500' },
}

export default function Credentials() {
  const { t, language } = useI18n()
  const isZh = language === 'zh'
  const { credentials, fetchCredentials, loading } = useUserStore()

  // --------------------------------------------------------
  // 上传表单状态
  // --------------------------------------------------------
  const [form, setForm] = useState({
    provider: '',
    apiKey: '',
    endpoint: '',
    notes: '',
  })
  const [submitLoading, setSubmitLoading] = useState(false)
  const [message, setMessage] = useState<{ type: 'success' | 'error'; text: string } | null>(null)

  useEffect(() => {
    fetchCredentials()
  }, [fetchCredentials])

  // --------------------------------------------------------
  // 上传凭证
  // --------------------------------------------------------
  const handleUpload = async (e: FormEvent) => {
    e.preventDefault()
    setMessage(null)

    if (!form.provider) {
      setMessage({ type: 'error', text: isZh ? '请选择供应商' : 'Please select a provider' })
      return
    }
    if (!form.apiKey.trim()) {
      setMessage({ type: 'error', text: isZh ? '请输入 API Key' : 'API Key is required' })
      return
    }

    setSubmitLoading(true)
    try {
      await uploadCredential({
        provider: form.provider,
        apiKey: form.apiKey.trim(),
        endpoint: form.endpoint.trim() || undefined,
        notes: form.notes.trim() || undefined,
      })
      setMessage({ type: 'success', text: isZh ? '凭证上传成功' : 'Credential uploaded' })
      setForm({ provider: '', apiKey: '', endpoint: '', notes: '' })
      fetchCredentials()
    } catch (err) {
      setMessage({
        type: 'error',
        text: err instanceof Error ? err.message : t('common.operationFailed'),
      })
    } finally {
      setSubmitLoading(false)
    }
  }

  // --------------------------------------------------------
  // 删除凭证
  // --------------------------------------------------------
  const handleDelete = async (id: string) => {
    const confirmText = isZh ? '确定要删除这个凭证吗？' : 'Are you sure you want to delete this credential?'
    if (!window.confirm(confirmText)) return

    try {
      await deleteCredential(id)
      setMessage({ type: 'success', text: isZh ? '凭证已删除' : 'Credential deleted' })
      fetchCredentials()
    } catch (err) {
      setMessage({
        type: 'error',
        text: err instanceof Error ? err.message : t('common.operationFailed'),
      })
    }
  }

  // --------------------------------------------------------
  // 健康状态文案
  // --------------------------------------------------------
  const healthLabel = (h: Credential['health']): string => {
    const map: Record<string, string> = isZh
      ? { healthy: '正常', unhealthy: '异常', unknown: '未知' }
      : { healthy: 'Healthy', unhealthy: 'Unhealthy', unknown: 'Unknown' }
    return map[h] ?? h
  }

  // --------------------------------------------------------
  // 渲染
  // --------------------------------------------------------
  return (
    <div className="min-h-screen bg-[#F8FAFC] p-6">
      <div className="mx-auto max-w-6xl space-y-6">
        <h1 className="text-2xl font-bold text-gray-900">
          {isZh ? '凭证管理' : 'Credentials'}
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

        {/* ---- 上传表单 ---- */}
        <div className="rounded-xl bg-white p-6 shadow-sm">
          <h2 className="mb-4 text-sm font-semibold text-gray-500 uppercase tracking-wide">
            {isZh ? '上传凭证' : 'Upload Credential'}
          </h2>
          <form onSubmit={handleUpload} className="grid grid-cols-1 gap-4 sm:grid-cols-2">
            {/* 供应商选择 */}
            <div>
              <label htmlFor="cred-provider" className="mb-1.5 block text-sm font-medium text-gray-700">
                {isZh ? '供应商' : 'Provider'}
              </label>
              <select
                id="cred-provider"
                value={form.provider}
                onChange={(e) => setForm((prev) => ({ ...prev, provider: e.target.value }))}
                className="w-full rounded-lg border border-gray-200 bg-gray-50 px-4 py-2.5 text-sm text-gray-900 outline-none transition focus:border-[#3B82F6] focus:ring-2 focus:ring-[#3B82F6]/20"
              >
                <option value="">{isZh ? '请选择供应商' : 'Select a provider'}</option>
                {PROVIDERS.map((p) => (
                  <option key={p.value} value={p.value}>{p.label}</option>
                ))}
              </select>
            </div>

            {/* API Key */}
            <div>
              <label htmlFor="cred-key" className="mb-1.5 block text-sm font-medium text-gray-700">
                API Key
              </label>
              <input
                id="cred-key"
                type="text"
                value={form.apiKey}
                onChange={(e) => setForm((prev) => ({ ...prev, apiKey: e.target.value }))}
                placeholder="sk-..."
                className="w-full rounded-lg border border-gray-200 bg-gray-50 px-4 py-2.5 text-sm text-gray-900 outline-none transition focus:border-[#3B82F6] focus:ring-2 focus:ring-[#3B82F6]/20"
              />
            </div>

            {/* 端点（选填） */}
            <div>
              <label htmlFor="cred-endpoint" className="mb-1.5 block text-sm font-medium text-gray-700">
                {isZh ? '端点（选填）' : 'Endpoint (optional)'}
              </label>
              <input
                id="cred-endpoint"
                type="text"
                value={form.endpoint}
                onChange={(e) => setForm((prev) => ({ ...prev, endpoint: e.target.value }))}
                placeholder="https://..."
                className="w-full rounded-lg border border-gray-200 bg-gray-50 px-4 py-2.5 text-sm text-gray-900 outline-none transition focus:border-[#3B82F6] focus:ring-2 focus:ring-[#3B82F6]/20"
              />
            </div>

            {/* 备注（选填） */}
            <div>
              <label htmlFor="cred-notes" className="mb-1.5 block text-sm font-medium text-gray-700">
                {isZh ? '备注（选填）' : 'Notes (optional)'}
              </label>
              <input
                id="cred-notes"
                type="text"
                value={form.notes}
                onChange={(e) => setForm((prev) => ({ ...prev, notes: e.target.value }))}
                className="w-full rounded-lg border border-gray-200 bg-gray-50 px-4 py-2.5 text-sm text-gray-900 outline-none transition focus:border-[#3B82F6] focus:ring-2 focus:ring-[#3B82F6]/20"
              />
            </div>

            {/* 提交按钮 */}
            <div className="sm:col-span-2">
              <button
                type="submit"
                disabled={submitLoading}
                className="rounded-lg bg-[#3B82F6] px-6 py-2.5 text-sm font-medium text-white shadow-sm transition hover:bg-[#2563EB] disabled:cursor-not-allowed disabled:opacity-60"
              >
                {submitLoading ? t('common.loading') : (isZh ? '上传' : 'Upload')}
              </button>
            </div>
          </form>
        </div>

        {/* ---- 凭证列表 ---- */}
        <div className="rounded-xl bg-white p-6 shadow-sm">
          <h2 className="mb-4 text-sm font-semibold text-gray-500 uppercase tracking-wide">
            {isZh ? '我的凭证' : 'My Credentials'}
          </h2>

          {loading ? (
            <div className="py-10 text-center text-sm text-gray-400">{t('common.loading')}</div>
          ) : credentials.length === 0 ? (
            <div className="py-10 text-center text-sm text-gray-400">{t('common.noData')}</div>
          ) : (
            <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-3">
              {credentials.map((cred) => {
                const style = HEALTH_STYLES[cred.health] ?? HEALTH_STYLES.unknown
                const provColor = PROVIDER_COLORS[cred.provider] ?? PROVIDER_COLORS.other
                return (
                  <div
                    key={cred.id}
                    className="flex flex-col rounded-xl border border-gray-100 p-4 transition hover:shadow-sm"
                  >
                    {/* 头部：供应商 + 健康状态 */}
                    <div className="mb-3 flex items-center justify-between">
                      <span className={`rounded-md px-2 py-0.5 text-xs font-medium ${provColor}`}>
                        {cred.provider.toUpperCase()}
                      </span>
                      <span className={`inline-flex items-center gap-1 rounded-full px-2 py-0.5 text-xs font-medium ${style.badge}`}>
                        <span className={`h-1.5 w-1.5 rounded-full ${style.dot}`} />
                        {healthLabel(cred.health)}
                      </span>
                    </div>

                    {/* ID 和日期 */}
                    <p className="truncate text-xs text-gray-400">ID: {cred.id}</p>
                    <p className="mt-1 text-xs text-gray-400">
                      {isZh ? '创建于' : 'Created'}: {new Date(cred.createdAt).toLocaleDateString()}
                    </p>
                    {cred.lastChecked && (
                      <p className="text-xs text-gray-400">
                        {isZh ? '最后检查' : 'Last checked'}: {new Date(cred.lastChecked).toLocaleString()}
                      </p>
                    )}

                    {/* 删除按钮 */}
                    <div className="mt-auto pt-3">
                      <button
                        type="button"
                        onClick={() => handleDelete(cred.id)}
                        className="text-xs text-red-500 transition hover:text-red-700"
                      >
                        {t('common.delete')}
                      </button>
                    </div>
                  </div>
                )
              })}
            </div>
          )}
        </div>
      </div>
    </div>
  )
}
