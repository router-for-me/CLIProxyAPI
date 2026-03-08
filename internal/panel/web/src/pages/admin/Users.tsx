import { useState, useEffect, useCallback } from 'react';
import {
  fetchUsers,
  updateUserStatus,
  updateUserRole,
} from '../../api/admin';
import type {
  Role,
  UserStatus,
  PaginatedResponse,
} from '../../api/admin';
import type { User } from '../../api/auth';

// ============================================================
// 用户管理页面 — 搜索 / 筛选 / 角色变更 / 封禁操作
// ============================================================

const PAGE_SIZE = 15;

/* ---- 角色徽章 ---- */
function RoleBadge({ role }: { role: Role }) {
  const styles: Record<Role, string> = {
    admin: 'bg-purple-50 text-purple-700',
    user:  'bg-gray-100 text-gray-600',
  };
  const labels: Record<Role, string> = {
    admin: '管理员',
    user:  '用户',
  };
  return (
    <span className={`inline-block rounded-md px-2 py-0.5 text-xs font-medium ${styles[role]}`}>
      {labels[role]}
    </span>
  );
}

/* ---- 状态徽章 ---- */
function StatusBadge({ status }: { status: UserStatus }) {
  const styles: Record<UserStatus, string> = {
    active:  'bg-green-50 text-green-700',
    banned:  'bg-red-50 text-red-700',
    pending: 'bg-amber-50 text-amber-700',
  };
  const labels: Record<UserStatus, string> = {
    active:  '正常',
    banned:  '已封禁',
    pending: '待审核',
  };
  return (
    <span className={`inline-block rounded-md px-2 py-0.5 text-xs font-medium ${styles[status]}`}>
      {labels[status]}
    </span>
  );
}

/* ---- 池模式标签 ---- */
const POOL_LABELS: Record<string, string> = {
  public:      '公共池',
  private:     '私有池',
  contributor: '贡献者',
  hybrid:      '混合',
};

/* ---- 日期格式化 ---- */
function formatDate(iso: string): string {
  return new Date(iso).toLocaleDateString('zh-CN', { year: 'numeric', month: '2-digit', day: '2-digit' });
}

/* ---- 确认弹窗 ---- */
function ConfirmModal({
  open,
  title,
  message,
  confirmLabel,
  onConfirm,
  onCancel,
}: {
  open: boolean;
  title: string;
  message: string;
  confirmLabel: string;
  onConfirm: () => void;
  onCancel: () => void;
}) {
  if (!open) return null;
  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/40">
      <div className="w-full max-w-md rounded-xl bg-white p-6 shadow-lg">
        <h3 className="text-lg font-semibold text-gray-900">{title}</h3>
        <p className="mt-2 text-sm text-gray-600">{message}</p>
        <div className="mt-6 flex justify-end gap-3">
          <button
            onClick={onCancel}
            className="rounded-lg border border-gray-200 px-4 py-2 text-sm font-medium text-gray-700 hover:bg-gray-50"
          >
            取消
          </button>
          <button
            onClick={onConfirm}
            className="rounded-lg bg-red-500 px-4 py-2 text-sm font-medium text-white hover:bg-red-600"
          >
            {confirmLabel}
          </button>
        </div>
      </div>
    </div>
  );
}

export default function Users() {
  const [data, setData] = useState<PaginatedResponse<User> | null>(null);
  const [page, setPage] = useState(1);
  const [search, setSearch] = useState('');
  const [roleFilter, setRoleFilter] = useState('');
  const [statusFilter, setStatusFilter] = useState('');
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');

  /* ---- 封禁确认弹窗状态 ---- */
  const [banTarget, setBanTarget] = useState<User | null>(null);

  /* ---- 角色下拉状态 ---- */
  const [roleDropdown, setRoleDropdown] = useState<number | null>(null);

  /* ---- 数据加载 ---- */
  const loadUsers = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      const res = await fetchUsers({
        page,
        page_size: PAGE_SIZE,
        search: search || undefined,
        role: roleFilter || undefined,
        status: statusFilter || undefined,
      });
      setData(res.data);
    } catch (err) {
      setError(err instanceof Error ? err.message : '加载失败');
    } finally {
      setLoading(false);
    }
  }, [page, search, roleFilter, statusFilter]);

  useEffect(() => { loadUsers(); }, [loadUsers]);

  /* ---- 搜索防抖 ---- */
  const [searchInput, setSearchInput] = useState('');
  useEffect(() => {
    const timer = setTimeout(() => {
      setSearch(searchInput);
      setPage(1);
    }, 400);
    return () => clearTimeout(timer);
  }, [searchInput]);

  /* ---- 封禁/解封 ---- */
  async function handleToggleBan(user: User) {
    const newStatus: UserStatus = user.status === 'banned' ? 'active' : 'banned';
    try {
      await updateUserStatus(user.id, newStatus);
      setBanTarget(null);
      loadUsers();
    } catch (err) {
      setError(err instanceof Error ? err.message : '操作失败');
    }
  }

  /* ---- 角色变更 ---- */
  async function handleRoleChange(user: User, newRole: Role) {
    try {
      await updateUserRole(user.id, newRole);
      setRoleDropdown(null);
      loadUsers();
    } catch (err) {
      setError(err instanceof Error ? err.message : '操作失败');
    }
  }

  /* ---- 分页计算 ---- */
  const totalPages = data ? Math.ceil(data.total / PAGE_SIZE) : 1;

  return (
    <div className="min-h-screen bg-[#F8FAFC] p-6">
      <h1 className="mb-6 text-2xl font-bold text-gray-900">用户管理</h1>

      {/* ---- 筛选栏 ---- */}
      <div className="mb-5 flex flex-wrap items-center gap-3">
        {/* 搜索框 */}
        <div className="relative min-w-[220px] max-w-sm flex-1">
          <svg className="pointer-events-none absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-gray-400" fill="none" viewBox="0 0 24 24" strokeWidth={2} stroke="currentColor">
            <path strokeLinecap="round" strokeLinejoin="round" d="M21 21l-5.197-5.197m0 0A7.5 7.5 0 105.196 5.196a7.5 7.5 0 0010.607 10.607z" />
          </svg>
          <input
            type="text"
            placeholder="搜索用户名或邮箱..."
            value={searchInput}
            onChange={(e) => setSearchInput(e.target.value)}
            className="w-full rounded-xl border border-gray-200 bg-white py-2 pl-10 pr-4 text-sm shadow-sm outline-none focus:border-blue-400 focus:ring-2 focus:ring-blue-100"
          />
        </div>

        {/* 角色筛选 */}
        <select
          value={roleFilter}
          onChange={(e) => { setRoleFilter(e.target.value); setPage(1); }}
          className="rounded-xl border border-gray-200 bg-white px-3 py-2 text-sm shadow-sm outline-none focus:border-blue-400"
        >
          <option value="">全部角色</option>
          <option value="admin">管理员</option>
          <option value="user">用户</option>
        </select>

        {/* 状态筛选 */}
        <select
          value={statusFilter}
          onChange={(e) => { setStatusFilter(e.target.value); setPage(1); }}
          className="rounded-xl border border-gray-200 bg-white px-3 py-2 text-sm shadow-sm outline-none focus:border-blue-400"
        >
          <option value="">全部状态</option>
          <option value="active">正常</option>
          <option value="banned">已封禁</option>
          <option value="pending">待审核</option>
        </select>
      </div>

      {/* ---- 错误提示 ---- */}
      {error && (
        <div className="mb-4 rounded-xl bg-red-50 p-3 text-sm text-red-600">{error}</div>
      )}

      {/* ---- 数据表格 ---- */}
      <div className="rounded-xl bg-white shadow-sm">
        <div className="overflow-x-auto">
          <table className="w-full text-left text-sm">
            <thead>
              <tr className="border-b border-gray-100 text-gray-500">
                <th className="px-5 py-3 font-medium">用户名</th>
                <th className="px-5 py-3 font-medium">邮箱</th>
                <th className="px-5 py-3 font-medium">角色</th>
                <th className="px-5 py-3 font-medium">状态</th>
                <th className="px-5 py-3 font-medium">池模式</th>
                <th className="px-5 py-3 font-medium">注册时间</th>
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
              ) : !data || data.data.length === 0 ? (
                <tr>
                  <td colSpan={7} className="py-16 text-center text-gray-400">暂无数据</td>
                </tr>
              ) : (
                data.data.map((user) => (
                  <tr key={user.id} className="border-b border-gray-50 hover:bg-gray-50/50">
                    <td className="px-5 py-3 font-medium text-gray-900">{user.username}</td>
                    <td className="px-5 py-3 text-gray-600">{user.email || '--'}</td>
                    <td className="px-5 py-3"><RoleBadge role={user.role} /></td>
                    <td className="px-5 py-3"><StatusBadge status={user.status as UserStatus} /></td>
                    <td className="px-5 py-3 text-gray-600">{POOL_LABELS[user.pool_mode] ?? user.pool_mode}</td>
                    <td className="px-5 py-3 text-gray-500">{formatDate(user.created_at)}</td>
                    <td className="px-5 py-3">
                      <div className="flex items-center gap-2">
                        {/* 封禁/解封按钮 */}
                        <button
                          onClick={() => {
                            if (user.status === 'banned') {
                              handleToggleBan(user);
                            } else {
                              setBanTarget(user);
                            }
                          }}
                          className={`rounded-lg px-3 py-1.5 text-xs font-medium transition-colors ${
                            user.status === 'banned'
                              ? 'bg-green-50 text-green-700 hover:bg-green-100'
                              : 'bg-red-50 text-red-700 hover:bg-red-100'
                          }`}
                        >
                          {user.status === 'banned' ? '解封' : '封禁'}
                        </button>

                        {/* 角色变更下拉 */}
                        <div className="relative">
                          <button
                            onClick={() => setRoleDropdown(roleDropdown === user.id ? null : user.id)}
                            className="rounded-lg bg-gray-50 px-3 py-1.5 text-xs font-medium text-gray-700 hover:bg-gray-100"
                          >
                            角色
                            <svg className="ml-1 inline h-3 w-3" fill="none" viewBox="0 0 24 24" strokeWidth={2} stroke="currentColor">
                              <path strokeLinecap="round" strokeLinejoin="round" d="M19.5 8.25l-7.5 7.5-7.5-7.5" />
                            </svg>
                          </button>
                          {roleDropdown === user.id && (
                            <div className="absolute right-0 z-10 mt-1 w-28 rounded-xl bg-white py-1 shadow-lg ring-1 ring-black/5">
                              {(['admin', 'user'] as Role[]).map((r) => (
                                <button
                                  key={r}
                                  onClick={() => handleRoleChange(user, r)}
                                  className={`block w-full px-4 py-2 text-left text-xs hover:bg-gray-50 ${
                                    user.role === r ? 'font-semibold text-blue-600' : 'text-gray-700'
                                  }`}
                                >
                                  {r === 'admin' ? '管理员' : '用户'}
                                </button>
                              ))}
                            </div>
                          )}
                        </div>
                      </div>
                    </td>
                  </tr>
                ))
              )}
            </tbody>
          </table>
        </div>

        {/* ---- 分页 ---- */}
        {data && data.total > PAGE_SIZE && (
          <div className="flex items-center justify-between border-t border-gray-100 px-5 py-3">
            <span className="text-sm text-gray-500">
              共 {data.total} 条，第 {page}/{totalPages} 页
            </span>
            <div className="flex gap-1">
              <button
                disabled={page <= 1}
                onClick={() => setPage((p) => Math.max(1, p - 1))}
                className="rounded-lg border border-gray-200 px-3 py-1.5 text-sm disabled:opacity-40 hover:bg-gray-50"
              >
                上一页
              </button>
              <button
                disabled={page >= totalPages}
                onClick={() => setPage((p) => p + 1)}
                className="rounded-lg border border-gray-200 px-3 py-1.5 text-sm disabled:opacity-40 hover:bg-gray-50"
              >
                下一页
              </button>
            </div>
          </div>
        )}
      </div>

      {/* ---- 封禁确认弹窗 ---- */}
      <ConfirmModal
        open={banTarget !== null}
        title="确认封禁用户"
        message={`确定要封禁用户「${banTarget?.username ?? ''}」吗？封禁后该用户将无法使用任何服务。`}
        confirmLabel="确认封禁"
        onConfirm={() => banTarget && handleToggleBan(banTarget)}
        onCancel={() => setBanTarget(null)}
      />
    </div>
  );
}
