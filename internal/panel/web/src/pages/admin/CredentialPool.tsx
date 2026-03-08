import { useState, useEffect, useCallback } from 'react';
import {
  fetchCredentials,
  createCredential,
  bulkHealthCheck,
  getPoolMode,
  updatePoolMode,
} from '../../api/admin';
import type {
  Credential,
  HealthStatus,
  PoolMode,
} from '../../api/admin';

// ============================================================
// 凭证池管理页面 — 池模式切换 / 凭证卡片 / 批量检测
// ============================================================

/* ---- 池模式描述 ---- */
const POOL_MODES: { value: PoolMode; label: string; desc: string }[] = [
  { value: 'public',      label: '公共池',  desc: '所有用户共享公共凭证池，无需自备凭证即可使用服务' },
  { value: 'private',     label: '私有池',  desc: '每个用户必须自行提供凭证，互不共享' },
  { value: 'contributor', label: '贡献者',  desc: '贡献凭证的用户可享受更高的额度和 RPM' },
];

/* ---- 健康状态 ---- */
const HEALTH_CFG: Record<HealthStatus, { dot: string; label: string }> = {
  healthy:  { dot: 'bg-green-500', label: '健康' },
  degraded: { dot: 'bg-amber-500', label: '降级' },
  down:     { dot: 'bg-red-500',   label: '不可用' },
};

/* ---- 时间格式化 ---- */
function formatTime(iso: string | undefined): string {
  if (!iso) return '--';
  const d = new Date(iso);
  const pad = (n: number) => String(n).padStart(2, '0');
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

export default function CredentialPool() {
  const [credentials, setCredentials] = useState<Credential[]>([]);
  const [total, setTotal] = useState(0);
  const [poolMode, setPoolMode] = useState<PoolMode>('public');
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [checking, setChecking] = useState(false);

  /* ---- 添加凭证弹窗 ---- */
  const [addOpen, setAddOpen] = useState(false);
  const [addForm, setAddForm] = useState({ provider: '', credential_data: '', pool_mode: 'public' as PoolMode });
  const [addSaving, setAddSaving] = useState(false);

  /* ---- 数据加载 ---- */
  const load = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      const [credRes, modeRes] = await Promise.all([
        fetchCredentials({ page: 1, page_size: 100 }),
        getPoolMode(),
      ]);
      setCredentials(credRes.data.data);
      setTotal(credRes.data.total);
      setPoolMode(modeRes.data.mode);
    } catch (err) {
      setError(err instanceof Error ? err.message : '加载失败');
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => { load(); }, [load]);

  /* ---- 切换池模式 ---- */
  async function handlePoolModeChange(mode: PoolMode) {
    try {
      await updatePoolMode(mode);
      setPoolMode(mode);
    } catch (err) {
      setError(err instanceof Error ? err.message : '切换失败');
    }
  }

  /* ---- 批量健康检查 ---- */
  async function handleBulkCheck() {
    setChecking(true);
    try {
      await bulkHealthCheck();
      load();
    } catch (err) {
      setError(err instanceof Error ? err.message : '检查失败');
    } finally {
      setChecking(false);
    }
  }

  /* ---- 添加凭证 ---- */
  async function handleAddCredential() {
    if (!addForm.provider.trim() || !addForm.credential_data.trim()) return;
    setAddSaving(true);
    try {
      await createCredential(addForm);
      setAddOpen(false);
      setAddForm({ provider: '', credential_data: '', pool_mode: 'public' });
      load();
    } catch (err) {
      setError(err instanceof Error ? err.message : '添加失败');
    } finally {
      setAddSaving(false);
    }
  }

  return (
    <div className="min-h-screen bg-[#F8FAFC] p-6">
      <div className="mb-6 flex items-center justify-between">
        <h1 className="text-2xl font-bold text-gray-900">凭证池管理</h1>
        <div className="flex gap-3">
          <button
            onClick={handleBulkCheck}
            disabled={checking}
            className="inline-flex items-center gap-1.5 rounded-xl border border-gray-200 bg-white px-4 py-2.5 text-sm font-medium text-gray-700 shadow-sm hover:bg-gray-50 disabled:opacity-50"
          >
            {checking ? (
              <div className="h-4 w-4 animate-spin rounded-full border-2 border-gray-400 border-t-transparent" />
            ) : (
              <svg className="h-4 w-4" fill="none" viewBox="0 0 24 24" strokeWidth={2} stroke="currentColor">
                <path strokeLinecap="round" strokeLinejoin="round" d="M16.023 9.348h4.992v-.001M2.985 19.644v-4.992m0 0h4.992m-4.993 0l3.181 3.183a8.25 8.25 0 0013.803-3.7M4.031 9.865a8.25 8.25 0 0113.803-3.7l3.181 3.182" />
              </svg>
            )}
            批量检测
          </button>
          <button
            onClick={() => setAddOpen(true)}
            className="inline-flex items-center gap-1.5 rounded-xl bg-[#3B82F6] px-4 py-2.5 text-sm font-medium text-white shadow-sm hover:bg-blue-600"
          >
            <svg className="h-4 w-4" fill="none" viewBox="0 0 24 24" strokeWidth={2} stroke="currentColor">
              <path strokeLinecap="round" strokeLinejoin="round" d="M12 4.5v15m7.5-7.5h-15" />
            </svg>
            添加凭证
          </button>
        </div>
      </div>

      {error && (
        <div className="mb-4 rounded-xl bg-red-50 p-3 text-sm text-red-600">{error}</div>
      )}

      {/* ---- 池模式选择器 ---- */}
      <div className="mb-8 rounded-xl bg-white p-5 shadow-sm">
        <h2 className="mb-4 text-base font-semibold text-gray-800">池模式</h2>
        <div className="grid grid-cols-1 gap-3 md:grid-cols-3">
          {POOL_MODES.map((pm) => (
            <button
              key={pm.value}
              onClick={() => handlePoolModeChange(pm.value)}
              className={`rounded-xl border-2 p-4 text-left transition-all ${
                poolMode === pm.value
                  ? 'border-blue-500 bg-blue-50/50'
                  : 'border-gray-200 hover:border-gray-300'
              }`}
            >
              <div className="flex items-center gap-2">
                <div className={`h-3 w-3 rounded-full ${poolMode === pm.value ? 'bg-blue-500' : 'bg-gray-300'}`} />
                <span className="font-medium text-gray-900">{pm.label}</span>
              </div>
              <p className="mt-2 text-xs text-gray-500">{pm.desc}</p>
            </button>
          ))}
        </div>
      </div>

      {/* ---- 凭证卡片网格 ---- */}
      <div className="mb-4 text-sm text-gray-500">共 {total} 个凭证</div>

      {loading ? (
        <div className="flex h-48 items-center justify-center">
          <div className="h-8 w-8 animate-spin rounded-full border-4 border-blue-500 border-t-transparent" />
        </div>
      ) : credentials.length === 0 ? (
        <div className="rounded-xl bg-white p-12 text-center shadow-sm">
          <p className="text-gray-400">暂无凭证，请点击「添加凭证」开始</p>
        </div>
      ) : (
        <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-4">
          {credentials.map((cred) => {
            const health = HEALTH_CFG[cred.health];
            return (
              <div key={cred.id} className="rounded-xl bg-white p-5 shadow-sm">
                {/* 头部: 供应商 + 健康点 */}
                <div className="mb-3 flex items-center justify-between">
                  <span className="rounded-md bg-gray-100 px-2 py-0.5 text-xs font-medium uppercase text-gray-700">
                    {cred.provider}
                  </span>
                  <div className="flex items-center gap-1.5">
                    <span className={`h-2.5 w-2.5 rounded-full ${health.dot}`} />
                    <span className="text-xs text-gray-500">{health.label}</span>
                  </div>
                </div>

                {/* 所有者 */}
                <p className="mb-2 text-sm text-gray-600">
                  {cred.owner_name ? `归属: ${cred.owner_name}` : '公共池凭证'}
                </p>

                {/* 成功率 */}
                <div className="mb-1 flex items-center justify-between">
                  <span className="text-xs text-gray-400">成功率</span>
                  <span className="text-sm font-medium text-gray-800">
                    {cred.success_rate !== undefined ? `${(cred.success_rate * 100).toFixed(1)}%` : '--'}
                  </span>
                </div>
                {/* 成功率进度条 */}
                <div className="mb-3 h-1.5 overflow-hidden rounded-full bg-gray-100">
                  <div
                    className={`h-full rounded-full transition-all ${
                      (cred.success_rate ?? 0) >= 0.9 ? 'bg-green-500' :
                      (cred.success_rate ?? 0) >= 0.7 ? 'bg-amber-500' : 'bg-red-500'
                    }`}
                    style={{ width: `${(cred.success_rate ?? 0) * 100}%` }}
                  />
                </div>

                {/* 最后检查 */}
                <p className="text-xs text-gray-400">
                  最后检查: {formatTime(cred.last_check)}
                </p>
              </div>
            );
          })}
        </div>
      )}

      {/* ---- 添加凭证弹窗 ---- */}
      {addOpen && (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/40">
          <div className="w-full max-w-lg rounded-xl bg-white p-6 shadow-lg">
            <h3 className="mb-5 text-lg font-semibold text-gray-900">添加凭证</h3>

            <div className="space-y-4">
              <div>
                <label className="mb-1 block text-sm font-medium text-gray-700">提供商</label>
                <select
                  value={addForm.provider}
                  onChange={(e) => setAddForm((f) => ({ ...f, provider: e.target.value }))}
                  className="w-full rounded-xl border border-gray-200 px-4 py-2.5 text-sm outline-none focus:border-blue-400"
                >
                  <option value="">请选择</option>
                  <option value="anthropic">Anthropic</option>
                  <option value="openai">OpenAI</option>
                  <option value="gemini">Gemini</option>
                  <option value="vertex">Vertex AI</option>
                </select>
              </div>

              <div>
                <label className="mb-1 block text-sm font-medium text-gray-700">凭证数据</label>
                <textarea
                  placeholder="API Key 或 JSON 凭证"
                  rows={4}
                  value={addForm.credential_data}
                  onChange={(e) => setAddForm((f) => ({ ...f, credential_data: e.target.value }))}
                  className="w-full rounded-xl border border-gray-200 px-4 py-2.5 text-sm outline-none focus:border-blue-400 focus:ring-2 focus:ring-blue-100"
                />
              </div>

              <div>
                <label className="mb-1 block text-sm font-medium text-gray-700">池归属</label>
                <select
                  value={addForm.pool_mode}
                  onChange={(e) => setAddForm((f) => ({ ...f, pool_mode: e.target.value as PoolMode }))}
                  className="w-full rounded-xl border border-gray-200 px-4 py-2.5 text-sm outline-none focus:border-blue-400"
                >
                  <option value="public">公共池</option>
                  <option value="private">私有</option>
                  <option value="contributor">贡献者</option>
                </select>
              </div>
            </div>

            <div className="mt-6 flex justify-end gap-3">
              <button
                onClick={() => setAddOpen(false)}
                className="rounded-lg border border-gray-200 px-4 py-2 text-sm font-medium text-gray-700 hover:bg-gray-50"
              >
                取消
              </button>
              <button
                onClick={handleAddCredential}
                disabled={addSaving || !addForm.provider || !addForm.credential_data.trim()}
                className="rounded-lg bg-[#3B82F6] px-4 py-2 text-sm font-medium text-white hover:bg-blue-600 disabled:opacity-50"
              >
                {addSaving ? '添加中...' : '添加'}
              </button>
            </div>
          </div>
        </div>
      )}
    </div>
  );
}
