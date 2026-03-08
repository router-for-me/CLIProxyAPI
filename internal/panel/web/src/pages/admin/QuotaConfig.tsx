import { useState, useEffect, useCallback } from 'react';
import {
  fetchQuotaConfigs,
  createQuotaConfig,
  updateQuotaConfig,
  deleteQuotaConfig,
  fetchRPMSettings,
  updateRPMSettings,
} from '../../api/admin';
import type {
  QuotaConfig,
  QuotaType,
  QuotaPeriod,
  RPMSettings,
} from '../../api/admin';

// ============================================================
// 额度配置页面 — 模型额度规则 CRUD + RPM 滑块
// ============================================================

/* ---- 额度类型 / 周期中文映射 ---- */
const QUOTA_TYPE_LABELS: Record<QuotaType, string> = {
  count: '次数',
  token: 'Token',
  both:  '次数+Token',
};
const PERIOD_LABELS: Record<QuotaPeriod, string> = {
  daily:   '每日',
  weekly:  '每周',
  monthly: '每月',
  total:   '总计',
};

/* ---- 空表单 ---- */
interface FormState {
  id: number | null;
  model_pattern: string;
  quota_type: QuotaType;
  max_requests: number;
  request_period: QuotaPeriod;
  max_tokens: number;
  token_period: QuotaPeriod;
}

const EMPTY_FORM: FormState = {
  id: null,
  model_pattern: '',
  quota_type: 'count',
  max_requests: 100,
  request_period: 'daily',
  max_tokens: 0,
  token_period: 'daily',
};

export default function QuotaConfigPage() {
  const [configs, setConfigs] = useState<QuotaConfig[]>([]);
  const [rpmSettings, setRpmSettings] = useState<RPMSettings>({ contributor_rpm: 30, non_contributor_rpm: 10 });
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');

  /* ---- 弹窗状态 ---- */
  const [modalOpen, setModalOpen] = useState(false);
  const [form, setForm] = useState<FormState>(EMPTY_FORM);
  const [saving, setSaving] = useState(false);

  /* ---- 删除确认 ---- */
  const [deleteTarget, setDeleteTarget] = useState<QuotaConfig | null>(null);

  /* ---- 数据加载 ---- */
  const load = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      const [cfgsRes, rpmRes] = await Promise.all([fetchQuotaConfigs(), fetchRPMSettings()]);
      setConfigs(cfgsRes.data);
      setRpmSettings(rpmRes.data);
    } catch (err) {
      setError(err instanceof Error ? err.message : '加载失败');
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => { load(); }, [load]);

  /* ---- 打开新建/编辑弹窗 ---- */
  function openAdd() {
    setForm(EMPTY_FORM);
    setModalOpen(true);
  }
  function openEdit(cfg: QuotaConfig) {
    setForm({
      id: cfg.id,
      model_pattern: cfg.model_pattern,
      quota_type: cfg.quota_type,
      max_requests: cfg.max_requests,
      request_period: cfg.request_period,
      max_tokens: cfg.max_tokens,
      token_period: cfg.token_period,
    });
    setModalOpen(true);
  }

  /* ---- 保存 ---- */
  async function handleSave() {
    if (!form.model_pattern.trim()) return;
    setSaving(true);
    try {
      if (form.id) {
        await updateQuotaConfig(form.id, {
          model_pattern: form.model_pattern,
          quota_type: form.quota_type,
          max_requests: form.max_requests,
          request_period: form.request_period,
          max_tokens: form.max_tokens,
          token_period: form.token_period,
        });
      } else {
        await createQuotaConfig({
          model_pattern: form.model_pattern,
          quota_type: form.quota_type,
          max_requests: form.max_requests,
          request_period: form.request_period,
          max_tokens: form.max_tokens,
          token_period: form.token_period,
        });
      }
      setModalOpen(false);
      load();
    } catch (err) {
      setError(err instanceof Error ? err.message : '保存失败');
    } finally {
      setSaving(false);
    }
  }

  /* ---- 删除 ---- */
  async function handleDelete() {
    if (!deleteTarget) return;
    try {
      await deleteQuotaConfig(deleteTarget.id);
      setDeleteTarget(null);
      load();
    } catch (err) {
      setError(err instanceof Error ? err.message : '删除失败');
    }
  }

  /* ---- RPM 保存 ---- */
  async function handleRpmSave() {
    try {
      await updateRPMSettings(rpmSettings);
    } catch (err) {
      setError(err instanceof Error ? err.message : 'RPM 保存失败');
    }
  }

  return (
    <div className="min-h-screen bg-[#F8FAFC] p-6">
      <div className="mb-6 flex items-center justify-between">
        <h1 className="text-2xl font-bold text-gray-900">额度配置</h1>
        <button
          onClick={openAdd}
          className="inline-flex items-center gap-1.5 rounded-xl bg-[#3B82F6] px-4 py-2.5 text-sm font-medium text-white shadow-sm hover:bg-blue-600"
        >
          <svg className="h-4 w-4" fill="none" viewBox="0 0 24 24" strokeWidth={2} stroke="currentColor">
            <path strokeLinecap="round" strokeLinejoin="round" d="M12 4.5v15m7.5-7.5h-15" />
          </svg>
          添加规则
        </button>
      </div>

      {error && (
        <div className="mb-4 rounded-xl bg-red-50 p-3 text-sm text-red-600">{error}</div>
      )}

      {/* ---- 规则表格 ---- */}
      <div className="mb-8 rounded-xl bg-white shadow-sm">
        <div className="overflow-x-auto">
          <table className="w-full text-left text-sm">
            <thead>
              <tr className="border-b border-gray-100 text-gray-500">
                <th className="px-5 py-3 font-medium">模型匹配</th>
                <th className="px-5 py-3 font-medium">类型</th>
                <th className="px-5 py-3 font-medium">请求限额</th>
                <th className="px-5 py-3 font-medium">请求周期</th>
                <th className="px-5 py-3 font-medium">Token 限额</th>
                <th className="px-5 py-3 font-medium">Token 周期</th>
                <th className="px-5 py-3 font-medium">操作</th>
              </tr>
            </thead>
            <tbody>
              {loading ? (
                <tr>
                  <td colSpan={7} className="py-16 text-center">
                    <div className="mx-auto h-6 w-6 animate-spin rounded-full border-4 border-blue-500 border-t-transparent" />
                  </td>
                </tr>
              ) : configs.length === 0 ? (
                <tr>
                  <td colSpan={7} className="py-16 text-center text-gray-400">暂无额度规则</td>
                </tr>
              ) : (
                configs.map((cfg) => (
                  <tr key={cfg.id} className="border-b border-gray-50 hover:bg-gray-50/50">
                    <td className="px-5 py-3 font-mono text-sm text-gray-900">{cfg.model_pattern}</td>
                    <td className="px-5 py-3">
                      <span className="rounded-md bg-blue-50 px-2 py-0.5 text-xs font-medium text-blue-700">
                        {QUOTA_TYPE_LABELS[cfg.quota_type]}
                      </span>
                    </td>
                    <td className="px-5 py-3 text-gray-700">{cfg.max_requests.toLocaleString()}</td>
                    <td className="px-5 py-3 text-gray-500">{PERIOD_LABELS[cfg.request_period]}</td>
                    <td className="px-5 py-3 text-gray-700">{cfg.max_tokens.toLocaleString()}</td>
                    <td className="px-5 py-3 text-gray-500">{PERIOD_LABELS[cfg.token_period]}</td>
                    <td className="px-5 py-3">
                      <div className="flex gap-2">
                        <button
                          onClick={() => openEdit(cfg)}
                          className="rounded-lg bg-gray-50 px-3 py-1.5 text-xs font-medium text-gray-700 hover:bg-gray-100"
                        >
                          编辑
                        </button>
                        <button
                          onClick={() => setDeleteTarget(cfg)}
                          className="rounded-lg bg-red-50 px-3 py-1.5 text-xs font-medium text-red-600 hover:bg-red-100"
                        >
                          删除
                        </button>
                      </div>
                    </td>
                  </tr>
                ))
              )}
            </tbody>
          </table>
        </div>
      </div>

      {/* ---- RPM 设置 ---- */}
      <div className="rounded-xl bg-white p-6 shadow-sm">
        <h2 className="mb-5 text-base font-semibold text-gray-800">RPM 限制设置</h2>
        <div className="grid grid-cols-1 gap-6 md:grid-cols-2">
          {/* 贡献者 RPM */}
          <div>
            <label className="mb-2 block text-sm font-medium text-gray-700">
              贡献者 RPM: <span className="text-blue-600">{rpmSettings.contributor_rpm}</span>
            </label>
            <input
              type="range"
              min={1}
              max={120}
              value={rpmSettings.contributor_rpm}
              onChange={(e) => setRpmSettings((s) => ({ ...s, contributor_rpm: Number(e.target.value) }))}
              className="w-full accent-blue-500"
            />
            <div className="mt-1 flex justify-between text-xs text-gray-400">
              <span>1</span><span>60</span><span>120</span>
            </div>
          </div>

          {/* 非贡献者 RPM */}
          <div>
            <label className="mb-2 block text-sm font-medium text-gray-700">
              非贡献者 RPM: <span className="text-blue-600">{rpmSettings.non_contributor_rpm}</span>
            </label>
            <input
              type="range"
              min={1}
              max={60}
              value={rpmSettings.non_contributor_rpm}
              onChange={(e) => setRpmSettings((s) => ({ ...s, non_contributor_rpm: Number(e.target.value) }))}
              className="w-full accent-blue-500"
            />
            <div className="mt-1 flex justify-between text-xs text-gray-400">
              <span>1</span><span>30</span><span>60</span>
            </div>
          </div>
        </div>
        <div className="mt-5 flex justify-end">
          <button
            onClick={handleRpmSave}
            className="rounded-xl bg-[#3B82F6] px-5 py-2 text-sm font-medium text-white hover:bg-blue-600"
          >
            保存 RPM 设置
          </button>
        </div>
      </div>

      {/* ---- 新建/编辑弹窗 ---- */}
      {modalOpen && (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/40">
          <div className="w-full max-w-lg rounded-xl bg-white p-6 shadow-lg">
            <h3 className="mb-5 text-lg font-semibold text-gray-900">
              {form.id ? '编辑额度规则' : '添加额度规则'}
            </h3>

            <div className="space-y-4">
              {/* 模型匹配 */}
              <div>
                <label className="mb-1 block text-sm font-medium text-gray-700">模型匹配模式</label>
                <input
                  type="text"
                  placeholder="例如: claude-* 或 gpt-4o"
                  value={form.model_pattern}
                  onChange={(e) => setForm((f) => ({ ...f, model_pattern: e.target.value }))}
                  className="w-full rounded-xl border border-gray-200 px-4 py-2.5 text-sm outline-none focus:border-blue-400 focus:ring-2 focus:ring-blue-100"
                />
              </div>

              {/* 类型 */}
              <div>
                <label className="mb-1 block text-sm font-medium text-gray-700">额度类型</label>
                <select
                  value={form.quota_type}
                  onChange={(e) => setForm((f) => ({ ...f, quota_type: e.target.value as QuotaType }))}
                  className="w-full rounded-xl border border-gray-200 px-4 py-2.5 text-sm outline-none focus:border-blue-400"
                >
                  <option value="count">次数</option>
                  <option value="token">Token</option>
                  <option value="both">次数 + Token</option>
                </select>
              </div>

              {/* 请求限额 + 周期 */}
              {(form.quota_type === 'count' || form.quota_type === 'both') && (
                <div className="grid grid-cols-2 gap-3">
                  <div>
                    <label className="mb-1 block text-sm font-medium text-gray-700">请求限额</label>
                    <input
                      type="number"
                      min={0}
                      value={form.max_requests}
                      onChange={(e) => setForm((f) => ({ ...f, max_requests: Number(e.target.value) }))}
                      className="w-full rounded-xl border border-gray-200 px-4 py-2.5 text-sm outline-none focus:border-blue-400"
                    />
                  </div>
                  <div>
                    <label className="mb-1 block text-sm font-medium text-gray-700">周期</label>
                    <select
                      value={form.request_period}
                      onChange={(e) => setForm((f) => ({ ...f, request_period: e.target.value as QuotaPeriod }))}
                      className="w-full rounded-xl border border-gray-200 px-4 py-2.5 text-sm outline-none focus:border-blue-400"
                    >
                      <option value="daily">每日</option>
                      <option value="weekly">每周</option>
                      <option value="monthly">每月</option>
                      <option value="total">总计</option>
                    </select>
                  </div>
                </div>
              )}

              {/* Token 限额 + 周期 */}
              {(form.quota_type === 'token' || form.quota_type === 'both') && (
                <div className="grid grid-cols-2 gap-3">
                  <div>
                    <label className="mb-1 block text-sm font-medium text-gray-700">Token 限额</label>
                    <input
                      type="number"
                      min={0}
                      value={form.max_tokens}
                      onChange={(e) => setForm((f) => ({ ...f, max_tokens: Number(e.target.value) }))}
                      className="w-full rounded-xl border border-gray-200 px-4 py-2.5 text-sm outline-none focus:border-blue-400"
                    />
                  </div>
                  <div>
                    <label className="mb-1 block text-sm font-medium text-gray-700">周期</label>
                    <select
                      value={form.token_period}
                      onChange={(e) => setForm((f) => ({ ...f, token_period: e.target.value as QuotaPeriod }))}
                      className="w-full rounded-xl border border-gray-200 px-4 py-2.5 text-sm outline-none focus:border-blue-400"
                    >
                      <option value="daily">每日</option>
                      <option value="weekly">每周</option>
                      <option value="monthly">每月</option>
                      <option value="total">总计</option>
                    </select>
                  </div>
                </div>
              )}
            </div>

            <div className="mt-6 flex justify-end gap-3">
              <button
                onClick={() => setModalOpen(false)}
                className="rounded-lg border border-gray-200 px-4 py-2 text-sm font-medium text-gray-700 hover:bg-gray-50"
              >
                取消
              </button>
              <button
                onClick={handleSave}
                disabled={saving || !form.model_pattern.trim()}
                className="rounded-lg bg-[#3B82F6] px-4 py-2 text-sm font-medium text-white hover:bg-blue-600 disabled:opacity-50"
              >
                {saving ? '保存中...' : '保存'}
              </button>
            </div>
          </div>
        </div>
      )}

      {/* ---- 删除确认弹窗 ---- */}
      {deleteTarget && (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/40">
          <div className="w-full max-w-md rounded-xl bg-white p-6 shadow-lg">
            <h3 className="text-lg font-semibold text-gray-900">确认删除</h3>
            <p className="mt-2 text-sm text-gray-600">
              确定删除模型规则「<span className="font-mono">{deleteTarget.model_pattern}</span>」吗？此操作不可恢复。
            </p>
            <div className="mt-6 flex justify-end gap-3">
              <button
                onClick={() => setDeleteTarget(null)}
                className="rounded-lg border border-gray-200 px-4 py-2 text-sm font-medium text-gray-700 hover:bg-gray-50"
              >
                取消
              </button>
              <button
                onClick={handleDelete}
                className="rounded-lg bg-red-500 px-4 py-2 text-sm font-medium text-white hover:bg-red-600"
              >
                确认删除
              </button>
            </div>
          </div>
        </div>
      )}
    </div>
  );
}
