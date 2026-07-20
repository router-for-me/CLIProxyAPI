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
  Spinner, ErrorBanner, EmptyState, Stat, Modal,
} from '../components/Primitives.jsx';
import InternalUserPolicyForm, { formToPatch } from '../components/InternalUserPolicyForm.jsx';

const DEFAULT_PAGE_SIZE = 25;

const ROLE_BADGE_CLASS = {
  internal_user: 'badge--active',
  proxy_admin: 'badge--revoked',
  proxy_admin_viewer: 'badge--muted',
};

export default function InternalUsersPage() {
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
      <div className="grid grid--4">
        <Stat label="Total Users" value={(list.data?.total || 0).toLocaleString()} />
        <Stat label="Aggregate Spend" value={`$${aggregated.totalSpend.toFixed(4)}`} />
        <Stat label="Avg Spend / User" value={`$${aggregated.avgSpend.toFixed(4)}`} />
        <Stat label="Near Budget Cap" value={String(aggregated.activeBudgets)} delta="≥80% of max_budget" />
      </div>

      {/* Leaderboard */}
      <div className="card" style={{ marginTop: 16 }}>
        <div className="row row--between" style={{ marginBottom: 12 }}>
          <h3 className="card__title" style={{ margin: 0 }}>Top 10 by Spend</h3>
          <span className="dim" style={{ fontSize: 12 }}>running total in internal_users.spend</span>
        </div>
        {leaderboard.loading && <Spinner label="Loading leaderboard…" />}
        {!leaderboard.loading && leaderboard.data?.entries?.length === 0 && (
          <EmptyState title="No internal users created yet" />
        )}
        {!leaderboard.loading && leaderboard.data?.entries?.length > 0 && (
          <table className="table">
            <thead>
              <tr>
                <th>#</th><th>Alias</th><th>Email</th><th>Role</th>
                <th>Spend</th><th>Max Budget</th><th>Keys</th>
              </tr>
            </thead>
            <tbody>
              {leaderboard.data.entries.map((u, i) => (
                <tr key={u.user_id}>
                  <td className="mono dim">{i + 1}</td>
                  <td>
                    <Link to={`/internal-users/${encodeURIComponent(u.user_id)}`}>
                      {u.user_alias || u.user_id}
                    </Link>
                  </td>
                  <td className="mono dim">{u.user_email || '—'}</td>
                  <td><RoleBadge role={u.user_role} /></td>
                  <td className="mono">${(u.spend || 0).toFixed(4)}</td>
                  <td className="mono">{u.max_budget ? `$${Number(u.max_budget).toFixed(2)}` : '—'}</td>
                  <td className="mono">{u.key_count ?? 0}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>

      {/* Filter bar */}
      <div className="card" style={{ marginTop: 16 }}>
        <div className="grid grid--4">
          <div className="form__row">
            <label className="form__label">Role</label>
            <select value={role} onChange={(e) => { setRole(e.target.value); setPage(1); }}>
              <option value="">any role</option>
              <option value="internal_user">internal_user</option>
              <option value="proxy_admin">proxy_admin</option>
              <option value="proxy_admin_viewer">proxy_admin_viewer</option>
            </select>
          </div>
          <div className="form__row">
            <label className="form__label">Search</label>
            <input type="text" value={search}
              onChange={(e) => { setSearch(e.target.value); setPage(1); }}
              placeholder="alias or email" />
          </div>
          <div className="form__row">
            <label className="form__label">Sort by</label>
            <select value={sortBy} onChange={(e) => setSortBy(e.target.value)}>
              <option value="spend">spend</option>
              <option value="created_at">created_at</option>
              <option value="user_alias">alias</option>
              <option value="updated_at">updated_at</option>
            </select>
          </div>
          <div className="form__row">
            <label className="form__label">Order</label>
            <select value={sortOrder} onChange={(e) => setSortOrder(e.target.value)}>
              <option value="desc">desc</option>
              <option value="asc">asc</option>
            </select>
          </div>
        </div>
      </div>

      {/* Users table */}
      <div className="card" style={{ marginTop: 16 }}>
        <h3 className="card__title">All Users</h3>
        {list.loading && <Spinner label="Loading users…" />}
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
        {(list.data?.total || 0) > DEFAULT_PAGE_SIZE && (
          <Pager
            page={page}
            pageSize={DEFAULT_PAGE_SIZE}
            total={list.data?.total || 0}
            onChange={setPage}
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

function UserRow({ user, onUpdated }) {
  const [busy, setBusy] = useState(false);
  const [confirmDelete, setConfirmDelete] = useState(false);

  async function handleReset() {
    setBusy(true);
    try {
      await resetInternalUserSpend(user.id);
      onUpdated();
    } catch (err) {
      alert(err.message);
    } finally {
      setBusy(false);
    }
  }
  async function handleDelete() {
    setBusy(true);
    try {
      await deleteInternalUser(user.id);
      onUpdated();
    } catch (err) {
      alert(err.message);
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
          <button onClick={handleReset} disabled={busy}>Reset</button>
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

function Pager({ page, pageSize, total, onChange }) {
  const totalPages = Math.max(1, Math.ceil(total / pageSize));
  return (
    <div className="row gap-sm" style={{ marginTop: 12, justifyContent: 'flex-end' }}>
      <button onClick={() => onChange(Math.max(1, page - 1))} disabled={page <= 1}>Prev</button>
      <span className="dim">page {page} / {totalPages}</span>
      <button onClick={() => onChange(Math.min(totalPages, page + 1))} disabled={page >= totalPages}>Next</button>
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
