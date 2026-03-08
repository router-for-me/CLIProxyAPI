import { useState, useEffect, useCallback } from 'react';
import {
  fetchTemplates,
  createTemplate,
  updateTemplateEnabled,
  generateCodes,
  fetchCodeUsageStats,
  fetchRedemptionCodes,
} from '../../api/admin';
import type {
  RedemptionTemplate,
  QuotaGrant,
  QuotaType,
  RedemptionCode,
  CodeUsageStat,
} from '../../api/admin';

// ============================================================
// 兑换码管理页面 — 模板列表 / 创建模板 / 批量生成 / 统计
// ============================================================

/* ---- 额度类型标签 ---- */
const QUOTA_TYPE_LABEL: Record<QuotaType, string> = {
  count: '次数',
  token: 'Token',
  both:  '次数+Token',
};

/* ---- 时间格式化 ---- */
function formatDate(iso: string | null): string {
  if (!iso) return '--';
  return new Date(iso).toLocaleDateString('zh-CN', { year: 'numeric', month: '2-digit', day: '2-digit' });
}

/* ---- Tab ---- */
type Tab = 'templates' | 'generate' | 'stats';
const TABS: { key: Tab; label: string }[] = [
  { key: 'templates', label: '模板管理' },
  { key: 'generate',  label: '批量生成' },
  { key: 'stats',     label: '使用统计' },
];

/* ---- 模板表单状态 ---- */
interface TemplateForm {
  name: string;
  description: string;
  bonus_quota: QuotaGrant;
  max_per_user: number;
  total_limit: number;
  enabled: boolean;
}

const EMPTY_TEMPLATE_FORM: TemplateForm = {
  name: '',
  description: '',
  bonus_quota: { model_pattern: '*', requests: 100, tokens: 0, quota_type: 'count' },
  max_per_user: 1,
  total_limit: 100,
  enabled: true,
};

/* ---- 生成表单 ---- */
interface GenerateForm {
  template_id: number;
  count: number;
  expires_at: string;
}

export default function RedemptionCodes() {
  const [tab, setTab] = useState<Tab>('templates');
  const [error, setError] = useState('');

  /* ================================================================
   * 模板列表
   * ================================================================ */
  const [templates, setTemplates] = useState<RedemptionTemplate[]>([]);
  const [templatesLoading, setTemplatesLoading] = useState(true);

  const loadTemplates = useCallback(async () => {
    setTemplatesLoading(true);
    try {
      const res = await fetchTemplates();
      setTemplates(res.data);
    } catch (err) {
      setError(err instanceof Error ? err.message : '加载模板失败');
    } finally {
      setTemplatesLoading(false);
    }
  }, []);

  useEffect(() => { loadTemplates(); }, [loadTemplates]);

  /* ---- 启用/禁用切换 ---- */
  async function handleToggleEnabled(tpl: RedemptionTemplate) {
    try {
      await updateTemplateEnabled(tpl.id, !tpl.enabled);
      loadTemplates();
    } catch (err) {
      setError(err instanceof Error ? err.message : '操作失败');
    }
  }

  /* ---- 创建模板弹窗 ---- */
  const [createOpen, setCreateOpen] = useState(false);
  const [tplForm, setTplForm] = useState<TemplateForm>(EMPTY_TEMPLATE_FORM);
  const [tplSaving, setTplSaving] = useState(false);

  async function handleCreateTemplate() {
    if (!tplForm.name.trim()) return;
    setTplSaving(true);
    try {
      await createTemplate(tplForm);
      setCreateOpen(false);
      setTplForm(EMPTY_TEMPLATE_FORM);
      loadTemplates();
    } catch (err) {
      setError(err instanceof Error ? err.message : '创建失败');
    } finally {
      setTplSaving(false);
    }
  }

  /* ================================================================
   * 批量生成
   * ================================================================ */
  const [genForm, setGenForm] = useState<GenerateForm>({ template_id: 0, count: 10, expires_at: '' });
  const [generatedCodes, setGeneratedCodes] = useState<string[]>([]);
  const [generating, setGenerating] = useState(false);

  async function handleGenerate() {
    if (!genForm.template_id || genForm.count <= 0) return;
    setGenerating(true);
    setGeneratedCodes([]);
    try {
      const res = await generateCodes({
        template_id: genForm.template_id,
        count: genForm.count,
        expires_at: genForm.expires_at || undefined,
      });
      setGeneratedCodes(res.data.codes);
    } catch (err) {
      setError(err instanceof Error ? err.message : '生成失败');
    } finally {
      setGenerating(false);
    }
  }

  /* ---- 复制到剪贴板 ---- */
  function copyAllCodes() {
    navigator.clipboard.writeText(generatedCodes.join('\n'));
  }

  /* ================================================================
   * 使用统计
   * ================================================================ */
  const [usageStats, setUsageStats] = useState<CodeUsageStat[]>([]);
  const [codes, setCodes] = useState<RedemptionCode[]>([]);
  const [codesTotal, setCodesTotal] = useState(0);
  const [codesPage, setCodesPage] = useState(1);
  const [statsLoading, setStatsLoading] = useState(false);

  const loadStats = useCallback(async () => {
    setStatsLoading(true);
    try {
      const [statsRes, codesRes] = await Promise.all([
        fetchCodeUsageStats(),
        fetchRedemptionCodes({ page: codesPage, page_size: 20 }),
      ]);
      setUsageStats(statsRes.data);
      setCodes(codesRes.data.data);
      setCodesTotal(codesRes.data.total);
    } catch (err) {
      setError(err instanceof Error ? err.message : '加载统计失败');
    } finally {
      setStatsLoading(false);
    }
  }, [codesPage]);

  useEffect(() => {
    if (tab === 'stats') loadStats();
  }, [tab, loadStats]);

  const codesTotalPages = Math.ceil(codesTotal / 20) || 1;

  return (
    <div className="min-h-screen bg-[#F8FAFC] p-6">
      <h1 className="mb-6 text-2xl font-bold text-gray-900">兑换码管理</h1>

      {error && (
        <div className="mb-4 rounded-xl bg-red-50 p-3 text-sm text-red-600">
          {error}
          <button onClick={() => setError('')} className="ml-2 text-red-400 hover:text-red-600">关闭</button>
        </div>
      )}

      {/* ---- Tab 栏 ---- */}
      <div className="mb-6 flex gap-1 rounded-xl bg-gray-100 p-1">
        {TABS.map((t) => (
          <button
            key={t.key}
            onClick={() => setTab(t.key)}
            className={`flex-1 rounded-lg py-2 text-sm font-medium transition-colors ${
              tab === t.key ? 'bg-white text-gray-900 shadow-sm' : 'text-gray-500 hover:text-gray-700'
            }`}
          >
            {t.label}
          </button>
        ))}
      </div>

      {/* ================================================================
       * 模板管理 Tab
       * ================================================================ */}
      {tab === 'templates' && (
        <>
          <div className="mb-4 flex justify-end">
            <button
              onClick={() => setCreateOpen(true)}
              className="inline-flex items-center gap-1.5 rounded-xl bg-[#3B82F6] px-4 py-2.5 text-sm font-medium text-white shadow-sm hover:bg-blue-600"
            >
              <svg className="h-4 w-4" fill="none" viewBox="0 0 24 24" strokeWidth={2} stroke="currentColor">
                <path strokeLinecap="round" strokeLinejoin="round" d="M12 4.5v15m7.5-7.5h-15" />
              </svg>
              创建模板
            </button>
          </div>

          <div className="rounded-xl bg-white shadow-sm">
            <div className="overflow-x-auto">
              <table className="w-full text-left text-sm">
                <thead>
                  <tr className="border-b border-gray-100 text-gray-500">
                    <th className="px-5 py-3 font-medium">名称</th>
                    <th className="px-5 py-3 font-medium">描述</th>
                    <th className="px-5 py-3 font-medium">额度奖励</th>
                    <th className="px-5 py-3 font-medium">每人限制</th>
                    <th className="px-5 py-3 font-medium">总量限制</th>
                    <th className="px-5 py-3 font-medium">已发放</th>
                    <th className="px-5 py-3 font-medium">状态</th>
                  </tr>
                </thead>
                <tbody>
                  {templatesLoading ? (
                    <tr>
                      <td colSpan={7} className="py-16 text-center">
                        <div className="mx-auto h-6 w-6 animate-spin rounded-full border-4 border-blue-500 border-t-transparent" />
                      </td>
                    </tr>
                  ) : templates.length === 0 ? (
                    <tr>
                      <td colSpan={7} className="py-16 text-center text-gray-400">暂无模板</td>
                    </tr>
                  ) : (
                    templates.map((tpl) => (
                      <tr key={tpl.id} className="border-b border-gray-50 hover:bg-gray-50/50">
                        <td className="px-5 py-3 font-medium text-gray-900">{tpl.name}</td>
                        <td className="max-w-xs truncate px-5 py-3 text-gray-500">{tpl.description || '--'}</td>
                        <td className="px-5 py-3">
                          <span className="rounded-md bg-blue-50 px-2 py-0.5 text-xs text-blue-700">
                            {QUOTA_TYPE_LABEL[tpl.bonus_quota.quota_type]}
                          </span>
                          <span className="ml-1 text-xs text-gray-500">
                            {tpl.bonus_quota.requests > 0 && `${tpl.bonus_quota.requests} 次`}
                            {tpl.bonus_quota.requests > 0 && tpl.bonus_quota.tokens > 0 && ' + '}
                            {tpl.bonus_quota.tokens > 0 && `${tpl.bonus_quota.tokens.toLocaleString()} tok`}
                          </span>
                        </td>
                        <td className="px-5 py-3 text-gray-700">{tpl.max_per_user}</td>
                        <td className="px-5 py-3 text-gray-700">{tpl.total_limit}</td>
                        <td className="px-5 py-3 text-gray-700">{tpl.issued_count}</td>
                        <td className="px-5 py-3">
                          <button
                            onClick={() => handleToggleEnabled(tpl)}
                            className={`relative h-6 w-11 rounded-full transition-colors ${
                              tpl.enabled ? 'bg-green-500' : 'bg-gray-300'
                            }`}
                          >
                            <span
                              className={`absolute left-0.5 top-0.5 h-5 w-5 rounded-full bg-white shadow transition-transform ${
                                tpl.enabled ? 'translate-x-5' : 'translate-x-0'
                              }`}
                            />
                          </button>
                        </td>
                      </tr>
                    ))
                  )}
                </tbody>
              </table>
            </div>
          </div>

          {/* ---- 创建模板弹窗 ---- */}
          {createOpen && (
            <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/40">
              <div className="w-full max-w-lg rounded-xl bg-white p-6 shadow-lg">
                <h3 className="mb-5 text-lg font-semibold text-gray-900">创建兑换码模板</h3>
                <div className="space-y-4">
                  <div>
                    <label className="mb-1 block text-sm font-medium text-gray-700">模板名称</label>
                    <input
                      type="text"
                      value={tplForm.name}
                      onChange={(e) => setTplForm((f) => ({ ...f, name: e.target.value }))}
                      className="w-full rounded-xl border border-gray-200 px-4 py-2.5 text-sm outline-none focus:border-blue-400 focus:ring-2 focus:ring-blue-100"
                    />
                  </div>
                  <div>
                    <label className="mb-1 block text-sm font-medium text-gray-700">描述</label>
                    <input
                      type="text"
                      value={tplForm.description}
                      onChange={(e) => setTplForm((f) => ({ ...f, description: e.target.value }))}
                      className="w-full rounded-xl border border-gray-200 px-4 py-2.5 text-sm outline-none focus:border-blue-400"
                    />
                  </div>
                  <div className="grid grid-cols-2 gap-3">
                    <div>
                      <label className="mb-1 block text-sm font-medium text-gray-700">模型匹配</label>
                      <input
                        type="text"
                        placeholder="*"
                        value={tplForm.bonus_quota.model_pattern}
                        onChange={(e) => setTplForm((f) => ({
                          ...f,
                          bonus_quota: { ...f.bonus_quota, model_pattern: e.target.value },
                        }))}
                        className="w-full rounded-xl border border-gray-200 px-4 py-2.5 text-sm outline-none focus:border-blue-400"
                      />
                    </div>
                    <div>
                      <label className="mb-1 block text-sm font-medium text-gray-700">额度类型</label>
                      <select
                        value={tplForm.bonus_quota.quota_type}
                        onChange={(e) => setTplForm((f) => ({
                          ...f,
                          bonus_quota: { ...f.bonus_quota, quota_type: e.target.value as QuotaType },
                        }))}
                        className="w-full rounded-xl border border-gray-200 px-4 py-2.5 text-sm outline-none focus:border-blue-400"
                      >
                        <option value="count">次数</option>
                        <option value="token">Token</option>
                        <option value="both">次数+Token</option>
                      </select>
                    </div>
                  </div>
                  <div className="grid grid-cols-2 gap-3">
                    <div>
                      <label className="mb-1 block text-sm font-medium text-gray-700">赠送次数</label>
                      <input
                        type="number"
                        min={0}
                        value={tplForm.bonus_quota.requests}
                        onChange={(e) => setTplForm((f) => ({
                          ...f,
                          bonus_quota: { ...f.bonus_quota, requests: Number(e.target.value) },
                        }))}
                        className="w-full rounded-xl border border-gray-200 px-4 py-2.5 text-sm outline-none focus:border-blue-400"
                      />
                    </div>
                    <div>
                      <label className="mb-1 block text-sm font-medium text-gray-700">赠送 Token</label>
                      <input
                        type="number"
                        min={0}
                        value={tplForm.bonus_quota.tokens}
                        onChange={(e) => setTplForm((f) => ({
                          ...f,
                          bonus_quota: { ...f.bonus_quota, tokens: Number(e.target.value) },
                        }))}
                        className="w-full rounded-xl border border-gray-200 px-4 py-2.5 text-sm outline-none focus:border-blue-400"
                      />
                    </div>
                  </div>
                  <div className="grid grid-cols-2 gap-3">
                    <div>
                      <label className="mb-1 block text-sm font-medium text-gray-700">每人限领</label>
                      <input
                        type="number"
                        min={1}
                        value={tplForm.max_per_user}
                        onChange={(e) => setTplForm((f) => ({ ...f, max_per_user: Number(e.target.value) }))}
                        className="w-full rounded-xl border border-gray-200 px-4 py-2.5 text-sm outline-none focus:border-blue-400"
                      />
                    </div>
                    <div>
                      <label className="mb-1 block text-sm font-medium text-gray-700">总量限制</label>
                      <input
                        type="number"
                        min={1}
                        value={tplForm.total_limit}
                        onChange={(e) => setTplForm((f) => ({ ...f, total_limit: Number(e.target.value) }))}
                        className="w-full rounded-xl border border-gray-200 px-4 py-2.5 text-sm outline-none focus:border-blue-400"
                      />
                    </div>
                  </div>
                </div>
                <div className="mt-6 flex justify-end gap-3">
                  <button
                    onClick={() => setCreateOpen(false)}
                    className="rounded-lg border border-gray-200 px-4 py-2 text-sm font-medium text-gray-700 hover:bg-gray-50"
                  >
                    取消
                  </button>
                  <button
                    onClick={handleCreateTemplate}
                    disabled={tplSaving || !tplForm.name.trim()}
                    className="rounded-lg bg-[#3B82F6] px-4 py-2 text-sm font-medium text-white hover:bg-blue-600 disabled:opacity-50"
                  >
                    {tplSaving ? '创建中...' : '创建'}
                  </button>
                </div>
              </div>
            </div>
          )}
        </>
      )}

      {/* ================================================================
       * 批量生成 Tab
       * ================================================================ */}
      {tab === 'generate' && (
        <div className="rounded-xl bg-white p-6 shadow-sm">
          <h2 className="mb-5 text-base font-semibold text-gray-800">批量生成兑换码</h2>
          <div className="max-w-lg space-y-4">
            <div>
              <label className="mb-1 block text-sm font-medium text-gray-700">选择模板</label>
              <select
                value={genForm.template_id}
                onChange={(e) => setGenForm((f) => ({ ...f, template_id: Number(e.target.value) }))}
                className="w-full rounded-xl border border-gray-200 px-4 py-2.5 text-sm outline-none focus:border-blue-400"
              >
                <option value={0}>请选择模板</option>
                {templates.filter((t) => t.enabled).map((t) => (
                  <option key={t.id} value={t.id}>{t.name}</option>
                ))}
              </select>
            </div>
            <div>
              <label className="mb-1 block text-sm font-medium text-gray-700">生成数量</label>
              <input
                type="number"
                min={1}
                max={1000}
                value={genForm.count}
                onChange={(e) => setGenForm((f) => ({ ...f, count: Number(e.target.value) }))}
                className="w-full rounded-xl border border-gray-200 px-4 py-2.5 text-sm outline-none focus:border-blue-400"
              />
            </div>
            <div>
              <label className="mb-1 block text-sm font-medium text-gray-700">过期时间 (可选)</label>
              <input
                type="date"
                value={genForm.expires_at}
                onChange={(e) => setGenForm((f) => ({ ...f, expires_at: e.target.value }))}
                className="w-full rounded-xl border border-gray-200 px-4 py-2.5 text-sm outline-none focus:border-blue-400"
              />
            </div>
            <button
              onClick={handleGenerate}
              disabled={generating || !genForm.template_id || genForm.count <= 0}
              className="rounded-xl bg-[#3B82F6] px-5 py-2.5 text-sm font-medium text-white hover:bg-blue-600 disabled:opacity-50"
            >
              {generating ? '生成中...' : '生成兑换码'}
            </button>
          </div>

          {/* 生成结果 */}
          {generatedCodes.length > 0 && (
            <div className="mt-6">
              <div className="mb-2 flex items-center justify-between">
                <span className="text-sm font-medium text-gray-700">已生成 {generatedCodes.length} 个兑换码</span>
                <button
                  onClick={copyAllCodes}
                  className="inline-flex items-center gap-1 rounded-lg bg-gray-100 px-3 py-1.5 text-xs font-medium text-gray-700 hover:bg-gray-200"
                >
                  <svg className="h-3.5 w-3.5" fill="none" viewBox="0 0 24 24" strokeWidth={2} stroke="currentColor">
                    <path strokeLinecap="round" strokeLinejoin="round" d="M15.666 3.888A2.25 2.25 0 0013.5 2.25h-3c-1.03 0-1.9.693-2.166 1.638m7.332 0c.055.194.084.4.084.612v0a.75.75 0 01-.75.75H9.75a.75.75 0 01-.75-.75v0c0-.212.03-.418.084-.612m7.332 0c.646.049 1.288.11 1.927.184 1.1.128 1.907 1.077 1.907 2.185V19.5a2.25 2.25 0 01-2.25 2.25H6.75A2.25 2.25 0 014.5 19.5V6.257c0-1.108.806-2.057 1.907-2.185a48.208 48.208 0 011.927-.184" />
                  </svg>
                  全部复制
                </button>
              </div>
              <div className="max-h-64 overflow-y-auto rounded-xl border border-gray-200 bg-gray-50 p-4">
                <pre className="whitespace-pre-wrap break-all font-mono text-xs text-gray-700">
                  {generatedCodes.join('\n')}
                </pre>
              </div>
            </div>
          )}
        </div>
      )}

      {/* ================================================================
       * 使用统计 Tab
       * ================================================================ */}
      {tab === 'stats' && (
        <>
          {/* 统计概览 */}
          <div className="mb-6 grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-4">
            {statsLoading ? (
              <div className="col-span-full flex h-24 items-center justify-center">
                <div className="h-6 w-6 animate-spin rounded-full border-4 border-blue-500 border-t-transparent" />
              </div>
            ) : (
              usageStats.map((stat) => (
                <div key={stat.template_id} className="rounded-xl bg-white p-5 shadow-sm">
                  <p className="mb-1 text-sm font-medium text-gray-900">{stat.template_name}</p>
                  <div className="flex gap-4 text-xs text-gray-500">
                    <span>生成: <b className="text-gray-700">{stat.total_generated}</b></span>
                    <span>使用: <b className="text-green-600">{stat.total_used}</b></span>
                    <span>过期: <b className="text-red-500">{stat.total_expired}</b></span>
                  </div>
                  {/* 使用率进度条 */}
                  <div className="mt-3 h-1.5 overflow-hidden rounded-full bg-gray-100">
                    <div
                      className="h-full rounded-full bg-green-500"
                      style={{ width: `${stat.total_generated > 0 ? (stat.total_used / stat.total_generated) * 100 : 0}%` }}
                    />
                  </div>
                </div>
              ))
            )}
          </div>

          {/* 兑换码列表 */}
          <div className="rounded-xl bg-white shadow-sm">
            <div className="overflow-x-auto">
              <table className="w-full text-left text-sm">
                <thead>
                  <tr className="border-b border-gray-100 text-gray-500">
                    <th className="px-5 py-3 font-medium">兑换码</th>
                    <th className="px-5 py-3 font-medium">模板</th>
                    <th className="px-5 py-3 font-medium">使用者</th>
                    <th className="px-5 py-3 font-medium">使用时间</th>
                    <th className="px-5 py-3 font-medium">过期时间</th>
                    <th className="px-5 py-3 font-medium">创建时间</th>
                  </tr>
                </thead>
                <tbody>
                  {statsLoading ? (
                    <tr>
                      <td colSpan={6} className="py-16 text-center">
                        <div className="mx-auto h-6 w-6 animate-spin rounded-full border-4 border-blue-500 border-t-transparent" />
                      </td>
                    </tr>
                  ) : codes.length === 0 ? (
                    <tr>
                      <td colSpan={6} className="py-16 text-center text-gray-400">暂无兑换码记录</td>
                    </tr>
                  ) : (
                    codes.map((code) => (
                      <tr key={code.id} className="border-b border-gray-50 hover:bg-gray-50/50">
                        <td className="px-5 py-3 font-mono text-xs text-gray-900">{code.code}</td>
                        <td className="px-5 py-3 text-gray-600">{code.template_name}</td>
                        <td className="px-5 py-3">
                          {code.used_by ? (
                            <span className="text-gray-700">#{code.used_by}</span>
                          ) : (
                            <span className="text-gray-400">未使用</span>
                          )}
                        </td>
                        <td className="px-5 py-3 text-gray-500">{formatDate(code.used_at)}</td>
                        <td className="px-5 py-3 text-gray-500">{formatDate(code.expires_at)}</td>
                        <td className="px-5 py-3 text-gray-500">{formatDate(code.created_at)}</td>
                      </tr>
                    ))
                  )}
                </tbody>
              </table>
            </div>

            {/* 分页 */}
            {codesTotal > 20 && (
              <div className="flex items-center justify-between border-t border-gray-100 px-5 py-3">
                <span className="text-sm text-gray-500">
                  共 {codesTotal} 条，第 {codesPage}/{codesTotalPages} 页
                </span>
                <div className="flex gap-1">
                  <button
                    disabled={codesPage <= 1}
                    onClick={() => setCodesPage((p) => Math.max(1, p - 1))}
                    className="rounded-lg border border-gray-200 px-3 py-1.5 text-sm disabled:opacity-40 hover:bg-gray-50"
                  >
                    上一页
                  </button>
                  <button
                    disabled={codesPage >= codesTotalPages}
                    onClick={() => setCodesPage((p) => p + 1)}
                    className="rounded-lg border border-gray-200 px-3 py-1.5 text-sm disabled:opacity-40 hover:bg-gray-50"
                  >
                    下一页
                  </button>
                </div>
              </div>
            )}
          </div>
        </>
      )}
    </div>
  );
}
