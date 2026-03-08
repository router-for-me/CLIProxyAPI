import { useState, useEffect } from 'react';
import {
  LineChart,
  Line,
  XAxis,
  YAxis,
  CartesianGrid,
  Tooltip,
  ResponsiveContainer,
  PieChart,
  Pie,
  Cell,
  Legend,
} from 'recharts';
import {
  fetchDashboardStats,
  fetchRequestTrends,
  fetchModelDistribution,
  fetchRecentAuditLogs,
} from '../../api/admin';
import type {
  DashboardStats,
  RequestTrend,
  ModelDistribution,
  AuditLog,
} from '../../api/admin';

// ============================================================
// 仪表盘页面 — 总览统计 / 趋势图 / 审计日志
// ============================================================

/* ---- 饼图配色 ---- */
const PIE_COLORS = ['#3B82F6', '#10B981', '#F59E0B', '#EF4444', '#8B5CF6', '#EC4899', '#06B6D4', '#F97316'];

/* ---- 健康状态映射 ---- */
const HEALTH_MAP: Record<string, { label: string; color: string }> = {
  healthy:  { label: '正常', color: '#10B981' },
  degraded: { label: '降级', color: '#F59E0B' },
  down:     { label: '异常', color: '#EF4444' },
};

/* ---- 趋势箭头 ---- */
function TrendBadge({ value }: { value: number }) {
  if (value === 0) return <span className="text-sm text-gray-400">--</span>;
  const positive = value > 0;
  return (
    <span className={`inline-flex items-center gap-0.5 text-sm font-medium ${positive ? 'text-green-600' : 'text-red-500'}`}>
      <svg className="h-3.5 w-3.5" viewBox="0 0 20 20" fill="currentColor">
        {positive
          ? <path fillRule="evenodd" d="M10 17a.75.75 0 01-.75-.75V5.612L5.29 9.77a.75.75 0 01-1.08-1.04l5.25-5.5a.75.75 0 011.08 0l5.25 5.5a.75.75 0 11-1.08 1.04l-3.96-4.158V16.25A.75.75 0 0110 17z" clipRule="evenodd" />
          : <path fillRule="evenodd" d="M10 3a.75.75 0 01.75.75v10.638l3.96-4.158a.75.75 0 111.08 1.04l-5.25 5.5a.75.75 0 01-1.08 0l-5.25-5.5a.75.75 0 111.08-1.04l3.96 4.158V3.75A.75.75 0 0110 3z" clipRule="evenodd" />
        }
      </svg>
      {Math.abs(value)}%
    </span>
  );
}

/* ---- 统计卡片图标 ---- */
function StatIcon({ type }: { type: string }) {
  const base = 'h-6 w-6';
  switch (type) {
    case 'users':
      return (
        <svg className={base} fill="none" viewBox="0 0 24 24" strokeWidth={1.5} stroke="currentColor">
          <path strokeLinecap="round" strokeLinejoin="round" d="M15 19.128a9.38 9.38 0 002.625.372 9.337 9.337 0 004.121-.952 4.125 4.125 0 00-7.533-2.493M15 19.128v-.003c0-1.113-.285-2.16-.786-3.07M15 19.128v.106A12.318 12.318 0 018.624 21c-2.331 0-4.512-.645-6.374-1.766l-.001-.109a6.375 6.375 0 0111.964-3.07M12 6.375a3.375 3.375 0 11-6.75 0 3.375 3.375 0 016.75 0zm8.25 2.25a2.625 2.625 0 11-5.25 0 2.625 2.625 0 015.25 0z" />
        </svg>
      );
    case 'requests':
      return (
        <svg className={base} fill="none" viewBox="0 0 24 24" strokeWidth={1.5} stroke="currentColor">
          <path strokeLinecap="round" strokeLinejoin="round" d="M3.75 3v11.25A2.25 2.25 0 006 16.5h2.25M3.75 3h-1.5m1.5 0h16.5m0 0h1.5m-1.5 0v11.25A2.25 2.25 0 0118 16.5h-2.25m-7.5 0h7.5m-7.5 0l-1 3m8.5-3l1 3m0 0l.5 1.5m-.5-1.5h-9.5m0 0l-.5 1.5m.75-9l3-3 2.148 2.148A12.061 12.061 0 0116.5 7.605" />
        </svg>
      );
    case 'credentials':
      return (
        <svg className={base} fill="none" viewBox="0 0 24 24" strokeWidth={1.5} stroke="currentColor">
          <path strokeLinecap="round" strokeLinejoin="round" d="M15.75 5.25a3 3 0 013 3m3 0a6 6 0 01-7.029 5.912c-.563-.097-1.159.026-1.563.43L10.5 17.25H8.25v2.25H6v2.25H2.25v-2.818c0-.597.237-1.17.659-1.591l6.499-6.499c.404-.404.527-1 .43-1.563A6 6 0 1121.75 8.25z" />
        </svg>
      );
    case 'health':
      return (
        <svg className={base} fill="none" viewBox="0 0 24 24" strokeWidth={1.5} stroke="currentColor">
          <path strokeLinecap="round" strokeLinejoin="round" d="M9 12.75L11.25 15 15 9.75m-3-7.036A11.959 11.959 0 013.598 6 11.99 11.99 0 003 9.749c0 5.592 3.824 10.29 9 11.623 5.176-1.332 9-6.03 9-11.622 0-1.31-.21-2.571-.598-3.751h-.152c-3.196 0-6.1-1.248-8.25-3.285z" />
        </svg>
      );
    default:
      return null;
  }
}

/* ---- 时间格式化 ---- */
function formatTime(iso: string): string {
  const d = new Date(iso);
  const pad = (n: number) => String(n).padStart(2, '0');
  return `${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

export default function Dashboard() {
  const [stats, setStats] = useState<DashboardStats | null>(null);
  const [trends, setTrends] = useState<RequestTrend[]>([]);
  const [distribution, setDistribution] = useState<ModelDistribution[]>([]);
  const [logs, setLogs] = useState<AuditLog[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');

  useEffect(() => {
    let cancelled = false;

    async function load() {
      try {
        const [sRes, tRes, dRes, lRes] = await Promise.all([
          fetchDashboardStats(),
          fetchRequestTrends(),
          fetchModelDistribution(),
          fetchRecentAuditLogs(),
        ]);
        if (cancelled) return;
        setStats(sRes.data);
        setTrends(tRes.data);
        setDistribution(dRes.data);
        setLogs(lRes.data);
      } catch (err) {
        if (!cancelled) setError(err instanceof Error ? err.message : '加载失败');
      } finally {
        if (!cancelled) setLoading(false);
      }
    }

    load();
    return () => { cancelled = true; };
  }, []);

  /* ---- 加载 / 错误状态 ---- */
  if (loading) {
    return (
      <div className="flex h-96 items-center justify-center">
        <div className="h-8 w-8 animate-spin rounded-full border-4 border-blue-500 border-t-transparent" />
      </div>
    );
  }

  if (error) {
    return (
      <div className="mx-auto mt-16 max-w-md rounded-xl bg-red-50 p-6 text-center">
        <p className="text-red-600">{error}</p>
        <button onClick={() => window.location.reload()} className="mt-4 rounded-lg bg-red-500 px-4 py-2 text-white hover:bg-red-600">
          重试
        </button>
      </div>
    );
  }

  /* ---- 统计卡片数据 ---- */
  const healthKey = stats?.system_health ?? 'healthy';
  const cards = [
    {
      key: 'users',
      title: '总用户数',
      value: stats?.total_users ?? 0,
      trend: stats?.users_trend ?? 0,
      icon: 'users',
      accent: 'bg-blue-50 text-blue-600',
    },
    {
      key: 'requests',
      title: '24h 请求量',
      value: stats?.total_requests_24h ?? 0,
      trend: stats?.requests_trend ?? 0,
      icon: 'requests',
      accent: 'bg-green-50 text-green-600',
    },
    {
      key: 'credentials',
      title: '活跃凭证',
      value: stats?.active_credentials ?? 0,
      trend: stats?.credentials_trend ?? 0,
      icon: 'credentials',
      accent: 'bg-amber-50 text-amber-600',
    },
    {
      key: 'health',
      title: '系统健康',
      value: HEALTH_MAP[healthKey].label,
      trend: 0,
      icon: 'health',
      accent: healthKey === 'healthy'
        ? 'bg-green-50 text-green-600'
        : healthKey === 'degraded'
          ? 'bg-amber-50 text-amber-600'
          : 'bg-red-50 text-red-600',
    },
  ];

  return (
    <div className="min-h-screen bg-[#F8FAFC] p-6">
      {/* ---- 页头 ---- */}
      <h1 className="mb-6 text-2xl font-bold text-gray-900">管理仪表盘</h1>

      {/* ---- 统计卡片网格 ---- */}
      <div className="mb-8 grid grid-cols-1 gap-5 sm:grid-cols-2 xl:grid-cols-4">
        {cards.map((c) => (
          <div key={c.key} className="rounded-xl bg-white p-5 shadow-sm">
            <div className="flex items-center justify-between">
              <div>
                <p className="text-sm text-gray-500">{c.title}</p>
                <p className="mt-1 text-2xl font-semibold text-gray-900">
                  {typeof c.value === 'number' ? c.value.toLocaleString() : c.value}
                </p>
              </div>
              <div className={`flex h-11 w-11 items-center justify-center rounded-xl ${c.accent}`}>
                <StatIcon type={c.icon} />
              </div>
            </div>
            <div className="mt-3">
              <TrendBadge value={c.trend} />
            </div>
          </div>
        ))}
      </div>

      {/* ---- 图表行 ---- */}
      <div className="mb-8 grid grid-cols-1 gap-5 lg:grid-cols-3">
        {/* 折线图: 7 天请求趋势 */}
        <div className="col-span-2 rounded-xl bg-white p-5 shadow-sm">
          <h2 className="mb-4 text-base font-semibold text-gray-800">近 7 天请求趋势</h2>
          <ResponsiveContainer width="100%" height={280}>
            <LineChart data={trends}>
              <CartesianGrid strokeDasharray="3 3" stroke="#E5E7EB" />
              <XAxis dataKey="date" tick={{ fontSize: 12 }} stroke="#9CA3AF" />
              <YAxis tick={{ fontSize: 12 }} stroke="#9CA3AF" />
              <Tooltip
                contentStyle={{ borderRadius: '0.75rem', border: 'none', boxShadow: '0 1px 3px rgba(0,0,0,.1)' }}
              />
              <Line
                type="monotone"
                dataKey="count"
                name="请求数"
                stroke="#3B82F6"
                strokeWidth={2}
                dot={{ r: 3, fill: '#3B82F6' }}
                activeDot={{ r: 5 }}
              />
            </LineChart>
          </ResponsiveContainer>
        </div>

        {/* 饼图: 模型分布 */}
        <div className="rounded-xl bg-white p-5 shadow-sm">
          <h2 className="mb-4 text-base font-semibold text-gray-800">模型调用分布</h2>
          <ResponsiveContainer width="100%" height={280}>
            <PieChart>
              <Pie
                data={distribution}
                dataKey="count"
                nameKey="model"
                cx="50%"
                cy="50%"
                outerRadius={90}
                innerRadius={50}
                paddingAngle={2}
                label={({ model, percent }: { model: string; percent: number }) =>
                  `${model} ${(percent * 100).toFixed(0)}%`
                }
                labelLine={false}
              >
                {distribution.map((_, idx) => (
                  <Cell key={idx} fill={PIE_COLORS[idx % PIE_COLORS.length]} />
                ))}
              </Pie>
              <Tooltip
                contentStyle={{ borderRadius: '0.75rem', border: 'none', boxShadow: '0 1px 3px rgba(0,0,0,.1)' }}
              />
              <Legend verticalAlign="bottom" height={36} />
            </PieChart>
          </ResponsiveContainer>
        </div>
      </div>

      {/* ---- 最近审计日志 ---- */}
      <div className="rounded-xl bg-white p-5 shadow-sm">
        <h2 className="mb-4 text-base font-semibold text-gray-800">最近活动</h2>
        <div className="overflow-x-auto">
          <table className="w-full text-left text-sm">
            <thead>
              <tr className="border-b border-gray-100 text-gray-500">
                <th className="pb-3 pr-4 font-medium">时间</th>
                <th className="pb-3 pr-4 font-medium">操作</th>
                <th className="pb-3 pr-4 font-medium">目标</th>
                <th className="pb-3 pr-4 font-medium">详情</th>
                <th className="pb-3 font-medium">IP</th>
              </tr>
            </thead>
            <tbody>
              {logs.length === 0 ? (
                <tr>
                  <td colSpan={5} className="py-8 text-center text-gray-400">暂无日志</td>
                </tr>
              ) : (
                logs.map((entry) => (
                  <tr key={entry.id} className="border-b border-gray-50 hover:bg-gray-50/50">
                    <td className="whitespace-nowrap py-3 pr-4 text-gray-500">{formatTime(entry.created_at)}</td>
                    <td className="py-3 pr-4">
                      <span className="inline-block rounded-md bg-blue-50 px-2 py-0.5 text-xs font-medium text-blue-700">
                        {entry.action}
                      </span>
                    </td>
                    <td className="py-3 pr-4 text-gray-700">{entry.target}</td>
                    <td className="max-w-xs truncate py-3 pr-4 text-gray-500">{entry.detail || '--'}</td>
                    <td className="py-3 font-mono text-xs text-gray-400">{entry.ip}</td>
                  </tr>
                ))
              )}
            </tbody>
          </table>
        </div>
      </div>
    </div>
  );
}
