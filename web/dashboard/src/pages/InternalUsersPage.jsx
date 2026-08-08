import React, { useState, useMemo, useCallback } from 'react';
import { Link } from 'react-router-dom';
import {
  listInternalUsers,
  createInternalUser,
  deleteInternalUser,
  resetInternalUserSpend,
  getInternalUsersLeaderboard,
} from '../api/client.js';
import { useAsync } from '../hooks/useAsync.js';
import {
  ErrorBanner, EmptyState, Modal,
  KpiCard, KpiSkeleton, CardSkeleton,
} from '../components/Primitives.jsx';
import Pager from '../components/Pager.jsx';
import { useToast } from '../components/Toast.jsx';
import InternalUserPolicyForm, { formToPatch } from '../components/InternalUserPolicyForm.jsx';

const DEFAULT_PAGE_SIZE = 25;

const ROLE_BADGE_CLASS = {
  internal_user: 'badge--active',
  proxy_admin: 'badge--revoked',
  proxy_admin_viewer: 'badge--muted',
};

export default function InternalUsersPage() {
  const toast = useToast();
  const [page, setPage] = useState(1);
  const [role, setRole] = useState('');
  const [search, setSearch] = useState('');
  const [sortBy, setSortBy] = useState('spend');
  const [sortOrder, setSortOrder] = useState('desc');
  const [showCreate, setShowCreate] = useState(false);

  const list = useAsync(
    () => listInternalUsers({ page, pageSize: DEFAULT_PAGE_SIZE, role, search, sortBy, sortOrder }),
    [page, role, search, sortBy, sortOrder],
  );

  // Top-by-spend leaderboard across all users (independent of the page filter).
  const leaderboard = useAsync(
    () => getInternalUsersLeaderboard({ limit: 10 }),
    [],
  );

  const reloadAll = useCallback(() => {
    list.reload();
    leaderboard.reload();
  }, [list, leaderboard]);

  const aggregated = useMemo(() => {
    const users = list.data?.users || [];
    const totalSpend = users.reduce((a, u) => a + (u.spend || 0), 0);
    const activeBudgets = users.filter((u) => u.max_budget && u.spend >= u.max_budget * 0.8).length;
    return {
      total: users.length,
      totalSpend,
      avgSpend: users.length ? totalSpend / users.length : 0,
      activeBudgets,
    };
  }, [list.data]);

  const totalPages = Math.max(1, Math.ceil((list.data?.total || 0) / DEFAULT_PAGE_SIZE));
  const nearBudget = aggregated.activeBudgets > 0;

  return (
    <>
      <div className="main__header">
        <div>
          <h1 className="main__title">Internal Users</h1>
          <div className="main__subtitle">
            LiteLLM-style key owners: each user aggregates spend / tokens /
            RPM across all owned API keys, with per-user budget and rate-limit
            enforcement.
          </div>
        </div>
        <div className="row gap-sm">
          <button onClick={reloadAll}>Refresh</button>
          <button className="primary" onClick={() => setShowCreate(true)}>Create User</button>
        </div>
      </div>

      <ErrorBanner error={list.error} onRetry={list.reload} />

      {/* KPI cards */}
      <div className="kpi-grid">
        {list.loading ? (
          <>
            <KpiSkeleton /><KpiSkeleton /><KpiSkeleton /><KpiSkeleton />
          </>
        ) : (
          <>
            <KpiCard
              label="Total Users"
              value={(list.data?.total || 0).toLocaleString()}
              icon={<UsersIcon />}
            />
            <KpiCard
              label="Aggregate Spend"
              value={`$${aggregated.totalSpend.toFixed(4)}`}
              hint={`avg $${aggregated.avgSpend.toFixed(4)} / user`}
              tone="accent"
              icon={<SpendIcon />}
            />
            <KpiCard
              label="Avg Spend / User"
              value={`$${aggregated.avgSpend.toFixed(4)}`}
              icon={<AvgIcon />}
            />
            <KpiCard
              label="Near Budget Cap"
              value={String(aggregated.activeBudgets)}
              hint={nearBudget ? 'users at ≥80% of max_budget' : 'nobody past 80% of budget'}
              tone={nearBudget ? 'warn' : 'neutral'}
              icon={<BudgetIcon />}
            />
          </>
        )}
      </div>

      {/* Leaderboard */}
      <div className="card dash-row">
        <div className="row row--between" style={{ marginBottom: 4 }}>
          <h3 className="card__title" style={{ margin: 0 }}>Top 10 by Spend</h3>
          <span className="dim" style={{ fontSize: 12 }}>running total in internal_users.spend</span>
        </div>
        {leaderboard.loading && <CardSkeleton rows={5} />}
        {!leaderboard.loading && leaderboard.data?.entries?.length === 0 && (
          <EmptyState title="No internal users created yet" />
        )}
        {!leaderboard.loading && leaderboard.data?.entries?.length > 0 && (
          <UserLeaderboard entries={leaderboard.data.entries} />
        )}
      </div>

      {/* Filter toolbar */}
      <div className="card dash-row">
        <div className="iu-toolbar">
          <div className="iu-toolbar__field iu-toolbar__field--search">
            <label className="iu-toolbar__label" htmlFor="iu-search">Search</label>
            <input
              id="iu-search"
              type="text"
              value={search}
              onChange={(e) => { setSearch(e.target.value); setPage(1); }}
              placeholder="alias or email"
            />
          </div>
          <div className="iu-toolbar__field iu-toolbar__field--role">
            <label className="iu-toolbar__label" htmlFor="iu-role">Role</label>
            <select id="iu-role" value={role} onChange={(e) => { setRole(e.target.value); setPage(1); }}>
              <option value="">any role</option>
              <option value="internal_user">internal_user</option>
              <option value="proxy_admin">proxy_admin</option>
              <option value="proxy_admin_viewer">proxy_admin_viewer</option>
            </select>
          </div>
          <div className="iu-toolbar__field iu-toolbar__field--sort">
            <label className="iu-toolbar__label" htmlFor="iu-sort">Sort by</label>
            <select id="iu-sort" value={sortBy} onChange={(e) => setSortBy(e.target.value)}>
              <option value="spend">spend</option>
              <option value="created_at">created_at</option>
              <option value="user_alias">alias</option>
              <option value="updated_at">updated_at</option>
            </select>
          </div>
          <div className="iu-toolbar__field iu-toolbar__field--sort">
            <label className="iu-toolbar__label" htmlFor="iu-order">Order</label>
            <select id="iu-order" value={sortOrder} onChange={(e) => setSortOrder(e.target.value)}>
              <option value="desc">desc</option>
              <option value="asc">asc</option>
            </select>
          </div>
        </div>
      </div>

      {/* Users table */}
      <div className="card dash-row">
        <h3 className="card__title">All Users</h3>
        {list.loading && <CardSkeleton rows={6} />}
        {!list.loading && !list.error && (list.data?.users || []).length === 0 && (
          <EmptyState title="No users match this filter"
            hint="Try clearing the search or creating a new user." />
        )}
        {!list.loading && !list.error && (list.data?.users || []).length > 0 && (
          <table className="table">
            <thead>
              <tr>
                <th>Alias</th><th>Email</th><th>Role</th><th>Keys</th>
                <th>Spend</th><th>Budget Usage</th><th>RPM</th>
                <th>Reset At</th><th>Actions</th>
              </tr>
            </thead>
            <tbody>
              {(list.data?.users || []).map((u) => (
                <UserRow key={u.id} user={u} onUpdated={list.reload} />
              ))}
            </tbody>
          </table>
        )}
        {totalPages > 1 && (
          <Pager
            page={page}
            totalPages={totalPages}
            total={list.data?.total || 0}
            pageSize={DEFAULT_PAGE_SIZE}
            onPageChange={setPage}
          />
        )}
      </div>

      {showCreate && (
        <CreateUserModal
          onClose={() => setShowCreate(false)}
          onCreated={() => { setShowCreate(false); reloadAll(); }}
        />
      )}
    </>
  );
}

function RoleBadge({ role }) {
  const cls = ROLE_BADGE_CLASS[role] || 'badge--muted';
  return <span className={`badge ${cls}`}>{role || 'unknown'}</span>;
}

// UserLeaderboard renders the top-N spenders as a bar list so the ranking
// reads at a glance. Bar fill is tinted by how close spend is to budget.
function UserLeaderboard({ entries }) {
  const max = Math.max(...entries.map((u) => Number(u.spend) || 0), 1);
  return (
    <ul className="dash-list" style={{ padding: '4px 0' }}>
      {entries.map((u, i) => {
        const spend = Number(u.spend) || 0;
        const limit = Number(u.max_budget) || 0;
        const pct = limit > 0 ? (spend / limit) * 100 : null;
        const tone = pct >= 100 ? 'bad' : pct >= 80 ? 'warn' : 'ok';
        return (
          <li key={u.user_id} className="iu-lb-row">
            <span className={`iu-lb-row__rank${i < 3 ? ' iu-lb-row__rank--top' : ''}`}>{i + 1}</span>
            <span className="iu-lb-row__user">
              <Link to={`/internal-users/${encodeURIComponent(u.user_id)}`}>
                {u.user_alias || u.user_id}
              </Link>
            </span>
            <span className="iu-lb-row__bar">
              <span
                className={`iu-lb-row__fill iu-lb-row__fill--${tone}`}
                style={{ width: `${(spend / max) * 100}%` }}
              />
            </span>
            <span className="iu-lb-row__spend">${spend.toFixed(4)}</span>
            <span className="iu-lb-row__keys">{u.key_count ?? 0} keys</span>
          </li>
        );
      })}
    </ul>
  );
}

function UserRow({ user, onUpdated }) {
  const toast = useToast();
  const [busy, setBusy] = useState(false);
  const [confirmDelete, setConfirmDelete] = useState(false);

  async function handleReset() {
    setBusy(true);
    try {
      await resetInternalUserSpend(user.id);
      toast.success(`Spend reset for "${user.user_alias || user.id}"`);
      onUpdated();
    } catch (err) {
      toast.error(err.message || 'Failed to reset spend');
    } finally {
      setBusy(false);
    }
  }
  async function handleDelete() {
    setBusy(true);
    try {
      await deleteInternalUser(user.id);
      toast.success(`Internal user "${user.user_alias || user.id}" deleted`);
      onUpdated();
    } catch (err) {
      toast.error(err.message || 'Failed to delete internal user');
    } finally {
      setBusy(false);
      setConfirmDelete(false);
    }
  }

  const pct = user.max_budget && Number(user.max_budget) > 0
    ? Math.min(100, (user.spend / Number(user.max_budget)) * 100)
    : null;

  return (
    <tr>
      <td>
        <Link to={`/internal-users/${encodeURIComponent(user.id)}`}>
          {user.user_alias || user.id}
        </Link>
        <div className="dim mono" style={{ fontSize: 11 }}>{user.id}</div>
      </td>
      <td className="mono dim">{user.user_email || '—'}</td>
      <td><RoleBadge role={user.user_role} /></td>
      <td className="mono">{user.key_count ?? 0}</td>
      <td className="mono">${(user.spend || 0).toFixed(4)}</td>
      <td>
        {pct === null ? (
          <span className="dim">unlimited</span>
        ) : (
          <BudgetBar pct={pct} spend={user.spend} limit={user.max_budget} />
        )}
      </td>
      <td className="mono">{user.rpm_limit ?? '—'}</td>
      <td className="mono dim">
        {user.budget_reset_at ? new Date(user.budget_reset_at).toLocaleString() : '—'}
      </td>
      <td>
        <div className="row gap-sm">
          <Link to={`/internal-users/${encodeURIComponent(user.id)}`}>
            <button disabled={busy}>View</button>
          </Link>
          <button onClick={handleReset} disabled={busy} title="Reset cumulative spend">
            {busy ? '…' : 'Reset'}
          </button>
          {confirmDelete ? (
            <>
              <button className="danger" onClick={handleDelete} disabled={busy}>Confirm</button>
              <button onClick={() => setConfirmDelete(false)} disabled={busy}>Cancel</button>
            </>
          ) : (
            <button className="danger" onClick={() => setConfirmDelete(true)} disabled={busy}>Delete</button>
          )}
        </div>
      </td>
    </tr>
  );
}

function BudgetBar({ pct, spend, limit }) {
  const color = pct >= 100 ? 'var(--danger)' : pct >= 80 ? 'var(--warning)' : 'var(--success)';
  return (
    <div>
      <div style={{
        height: 8, width: '100%', background: 'var(--bg)', borderRadius: 4, overflow: 'hidden',
      }}>
        <div style={{ width: `${pct}%`, height: '100%', background: color, minWidth: 2 }} />
      </div>
      <div className="dim mono" style={{ fontSize: 11, marginTop: 2 }}>
        ${Number(spend).toFixed(2)} / ${Number(limit).toFixed(2)} ({pct.toFixed(0)}%)
      </div>
    </div>
  );
}

function CreateUserModal({ onClose, onCreated }) {
  const [form, setForm] = useState(null);
  const [autoCreateKey, setAutoCreateKey] = useState(true);
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState('');
  const [created, setCreated] = useState(null);

  async function handleSubmit(e) {
    e.preventDefault();
    if (!form) { onClose(); return; }
    setSubmitting(true);
    setError('');
    // Build the create payload — same shape as the PATCH formToPatch but
    // flattened (the create endpoint accepts the full user object).
    const patch = formToPatch(form);
    patch.auto_create_key = autoCreateKey;
    try {
      const result = await createInternalUser(patch);
      setCreated(result);
      if (!result.api_key && !result.secret && !result.auto_create_error) {
        // Payload did not surface a key (auto_create_key=false). Close + refresh.
        onCreated();
        return;
      }
    } catch (err) {
      setError(err.message);
    } finally {
      setSubmitting(false);
    }
  }

  if (created) {
    return (
      <Modal title="Internal User Created" onClose={onCreated}>
        <div className="form__row">
          <label className="form__label">User</label>
          <div>{created.user?.user_alias || created.user?.id}</div>
          <div className="dim mono" style={{ fontSize: 11 }}>{created.user?.id}</div>
        </div>
        {created.auto_create_error ? (
          <div className="error-banner">
            Auto-create key failed: {created.auto_create_error}. The user row was
            persisted; attach an API key from the user's detail page.
          </div>
        ) : created.secret ? (
          <div className="form__row">
            <label className="form__label">API key secret (shown once)</label>
            <div className="copyable">{created.secret}</div>
            <div className="form__hint">
              Store this securely. The dashboard cannot retrieve it later —
              only its SHA-256 hash is persisted. The key is auto-attached to
              this user.
            </div>
            {created.api_key && (
              <div className="dim mono" style={{ marginTop: 6, fontSize: 11 }}>
                id: {created.api_key.id} · prefix: {created.api_key.key_prefix}…
              </div>
            )}
          </div>
        ) : null}
        <div className="form__actions">
          <button className="primary" onClick={onCreated}>Done</button>
        </div>
      </Modal>
    );
  }

  return (
    <Modal title="Create Internal User" onClose={onClose}>
      {error && <div className="error-banner">{error}</div>}
      <form onSubmit={handleSubmit}>
        <InternalUserPolicyForm initial={null} onChange={setForm} />
        <div className="form__row" style={{ marginTop: 8 }}>
          <label className="row gap-sm" style={{ cursor: 'pointer' }}>
            <input
              type="checkbox"
              checked={autoCreateKey}
              onChange={(e) => setAutoCreateKey(e.target.checked)}
              style={{ width: 'auto' }}
            />
            <span className="form__label" style={{ margin: 0 }}>
              Auto-create API key for this user (LiteLLM workflow)
            </span>
          </label>
          <div className="form__hint">
            Default on. The server provisions a default API key bound to this
            user and returns the plaintext secret ONCE in the response.
            Disable to persist the user row only — attach a key from the key detail page later.
          </div>
        </div>
        <div className="form__actions">
          <button type="button" onClick={onClose} disabled={submitting}>Cancel</button>
          <button type="submit" className="primary" disabled={submitting}>
            {submitting ? 'Creating…' : 'Create User'}
          </button>
        </div>
      </form>
    </Modal>
  );
}

// --- Inline KPI icons (dependency-free, matching the dashboard icon style) ---

const ITEM_PROPS = {
  viewBox: '0 0 16 16',
  fill: 'none',
  stroke: 'currentColor',
  strokeWidth: '1.5',
  strokeLinecap: 'round',
  strokeLinejoin: 'round',
};

function UsersIcon() {
  return (
    <svg {...ITEM_PROPS}>
      <circle cx="6" cy="5.5" r="2" />
      <path d="M2.5 13.5c0-2 1.5-3.5 3.5-3.5s3.5 1.5 3.5 3.5" />
      <circle cx="11" cy="6.5" r="1.7" />
      <path d="M9.2 13.5c0-1.6 1-3 2.4-3s2.4 1.4 2.4 3" />
    </svg>
  );
}

function SpendIcon() {
  return (
    <svg {...ITEM_PROPS}>
      <path d="M2 12.5h12" />
      <path d="M3.5 9.5l2.5-3 2 2 4-4.5" />
      <path d="M10 3.5h2v2" />
    </svg>
  );
}

function AvgIcon() {
  return (
    <svg {...ITEM_PROPS}>
      <path d="M3 7l4-3 3 4 3-3" />
      <path d="M2 12h12" />
    </svg>
  );
}

function BudgetIcon() {
  return (
    <svg {...ITEM_PROPS}>
      <path d="M8 1.5l6 12.5H2L8 1.5z" />
      <path d="M8 6v3M8 10.5v.5" />
    </svg>
  );
}
