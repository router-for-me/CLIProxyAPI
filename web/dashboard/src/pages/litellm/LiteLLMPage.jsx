import React, { useMemo, useState, useEffect, useCallback } from 'react';
import {
  listLiteLLMUsers,
  createLiteLLMUser,
  patchLiteLLMUser,
  deleteLiteLLMUser,
  resetLiteLLMUserSpend,
  listLiteLLMKeys,
  createLiteLLMKey,
  patchLiteLLMKey,
  putLiteLLMKeyPolicy,
  regenerateLiteLLMKey,
  deleteLiteLLMKey,
  getLiteLLMKey,
  getLiteLLMSyncSettings,
  putLiteLLMSyncSettings,
  runLiteLLMSyncNow,
  runLiteLLMSyncNixLLM,
  getUsageEvents,
  listLiteLLMOnTheFlyLog,
  clearLiteLLMOnTheFlyLog,
} from '../../api/client.js';
import { useAsync } from '../../hooks/useAsync.js';
import { useAutoRefresh } from '../../hooks/useAutoRefresh.js';
import {
  Spinner, ErrorBanner, EmptyState, StatusBadge, Modal,
  KpiCard, KpiSkeleton, CardSkeleton,
} from '../../components/Primitives.jsx';
import Pager from '../../components/Pager.jsx';
import CopyButton from '../../components/CopyButton.jsx';
import { useToast } from '../../components/Toast.jsx';
import InternalUserPolicyForm, { formToPatch, userToForm } from '../../components/InternalUserPolicyForm.jsx';
import LiteLLMPolicyForm, { liteLLMFormToPolicy, liteLLMPolicyToForm } from './LiteLLMPolicyForm.jsx';
import {
  ONTHEFLY_OUTCOME_OPTIONS, ONTHEFLY_LIMIT_OPTIONS,
  formatOnTheFlyOutcome, onTheFlyOutcomeBadge, summarizeOnTheFlyOutcomes,
  readOnTheFlyAutoRefresh, ONTHEFLY_AUTOREFRESH_STORAGE,
} from './ontheflyLog.js';
import { PasswordInput, ToggleRow } from '../manage-cpa/FormPrimitives.jsx';
import { formatRelativeTime } from '../../utils/formatRelativeTime.js';
import {
  PRESETS, presetToRange, EVENTS_PAGE_SIZE, loadTimezone, formatInTZ,
  EventsTableBody, EventDetailModal,
} from '../usageShared.jsx';

const DEFAULT_PAGE_SIZE = 25;
const STATUS_OPTIONS = [
  { value: '', label: 'All Statuses' },
  { value: 'active', label: 'Active' },
  { value: 'disabled', label: 'Disabled' },
  { value: 'revoked', label: 'Revoked' },
  { value: 'expired', label: 'Expired' },
];

function shortOwnerId(id) {
  if (!id) return '';
  return id.length > 12 ? `${id.slice(0, 8)}…` : id;
}

function timeAgo(dateString) {
  if (!dateString) return 'Never';
  const now = new Date();
  const date = new Date(dateString);
  const diffSec = Math.floor((now - date) / 1000);
  if (diffSec < 60) return 'Just now';
  if (diffSec < 3600) return `${Math.floor(diffSec / 60)}m ago`;
  if (diffSec < 86400) return `${Math.floor(diffSec / 3600)}h ago`;
  if (diffSec < 2592000) return `${Math.floor(diffSec / 86400)}d ago`;
  return date.toLocaleDateString();
}

function formatExpires(dateString) {
  if (!dateString) return 'Never';
  const date = new Date(dateString);
  const now = new Date();
  if (date < now) return 'Expired';
  const diffDays = Math.ceil((date - now) / (1000 * 60 * 60 * 24));
  return `In ${diffDays} day${diffDays === 1 ? '' : 's'}`;
}

function money(v) {
  return `$${Number(v || 0).toFixed(4)}`;
}

export default function LiteLLMPage() {
  const [tab, setTab] = useState('users');

  return (
    <>
      <div className="main__header">
        <div>
          <h1 className="main__title">Manage LiteLLM</h1>
          <div className="main__subtitle">
            Internal Users and API Keys stored in the dedicated <code className="mono">litellm_*</code> tables —
            complete LiteLLM-style data (key spend, budget + duration, tpm_limit, tags, aliases),
            isolated from the runtime tables the proxy serves. Optionally sync from an external LiteLLM instance.
          </div>
        </div>
      </div>

      <div className="card" style={{ marginBottom: 16, padding: 8 }}>
        <div className="seg-group" role="tablist" aria-label="Manage LiteLLM section">
          <button
            type="button"
            className={`seg-btn ${tab === 'users' ? 'seg-btn--active' : ''}`}
            onClick={() => setTab('users')}
          >
            Internal Users
          </button>
          <button
            type="button"
            className={`seg-btn ${tab === 'keys' ? 'seg-btn--active' : ''}`}
            onClick={() => setTab('keys')}
          >
            API Keys
          </button>
          <button
            type="button"
            className={`seg-btn ${tab === 'logs' ? 'seg-btn--active' : ''}`}
            onClick={() => setTab('logs')}
          >
            Usage Logs
          </button>
          <button
            type="button"
            className={`seg-btn ${tab === 'settings' ? 'seg-btn--active' : ''}`}
            onClick={() => setTab('settings')}
          >
            Sync Settings
          </button>
          <button
            type="button"
            className={`seg-btn ${tab === 'onthefly' ? 'seg-btn--active' : ''}`}
            onClick={() => setTab('onthefly')}
          >
            On-the-fly Log
          </button>
        </div>
      </div>

      {tab === 'users' && <UsersTab />}
      {tab === 'keys' && <KeysTab />}
      {tab === 'logs' && <UsageLogsTab />}
      {tab === 'settings' && <SyncSettingsTab />}
      {tab === 'onthefly' && <OnTheFlyLogTab />}
    </>
  );
}

// --- Internal Users tab ------------------------------------------------------

function UsersTab() {
  const toast = useToast();
  const [page, setPage] = useState(1);
  const [role, setRole] = useState('');
  const [search, setSearch] = useState('');
  const [sortBy, setSortBy] = useState('spend');
  const [sortOrder, setSortOrder] = useState('desc');
  const [showCreate, setShowCreate] = useState(false);
  const [editing, setEditing] = useState(null); // user being edited
  const [confirmResetId, setConfirmResetId] = useState(null);
  const [confirmDeleteId, setConfirmDeleteId] = useState(null);
  const [busy, setBusy] = useState({});

  const list = useAsync(
    () => listLiteLLMUsers({ page, pageSize: DEFAULT_PAGE_SIZE, role, search, sortBy, sortOrder }),
    [page, role, search, sortBy, sortOrder],
  );

  const users = list.data?.users || [];
  const total = list.data?.total ?? 0;
  const totalPages = Math.max(1, Math.ceil(total / DEFAULT_PAGE_SIZE));

  const stats = useMemo(() => {
    const totalSpend = users.reduce((a, u) => a + (u.spend || 0), 0);
    const nearBudget = users.filter((u) => u.max_budget && u.spend >= u.max_budget * 0.8).length;
    return {
      total,
      totalSpend,
      avgSpend: total ? totalSpend / total : 0,
      nearBudget,
    };
  }, [users, total]);

  async function runBusy(id, fn) {
    setBusy((b) => ({ ...b, [id]: true }));
    try {
      await fn();
    } finally {
      setBusy((b) => ({ ...b, [id]: false }));
    }
  }

  async function handleReset(id, alias) {
    await runBusy(`reset-${id}`, async () => {
      await resetLiteLLMUserSpend(id);
      toast.success(`Spend reset for "${alias || id}"`);
      list.reload();
    }).catch((err) => toast.error(err.message || 'Failed to reset spend'));
    setConfirmResetId(null);
  }

  async function handleDelete(id, alias) {
    await runBusy(`del-${id}`, async () => {
      await deleteLiteLLMUser(id);
      toast.success(`Internal user "${alias || id}" deleted (keys detached)`);
      list.reload();
    }).catch((err) => toast.error(err.message || 'Failed to delete user'));
    setConfirmDeleteId(null);
  }

  function handleSort(column) {
    const next = nextSort(sortBy, sortOrder, column);
    setSortBy(next.by);
    setSortOrder(next.order);
    setPage(1);
  }

  const userHeaders = (
    <>
      <SortableTh column="user_alias" label="Alias" active={sortBy === 'user_alias'} asc={sortOrder === 'asc'} onToggle={handleSort} />
      <th>Email</th>
      <th>Role</th>
      <th>Keys</th>
      <SortableTh column="spend" label="Spend" active={sortBy === 'spend'} asc={sortOrder === 'asc'} onToggle={handleSort} />
      <th>Budget Usage</th>
      <th>RPM</th>
      <th>TPM</th>
      <SortableTh column="created_at" label="Created" active={sortBy === 'created_at'} asc={sortOrder === 'asc'} onToggle={handleSort} />
    </>
  );

  return (
    <>
      <ErrorBanner error={list.error} onRetry={list.reload} />

      {/* KPI cards */}
      <div className="kpi-grid">
        {list.loading ? (
          <>
            <KpiSkeleton /><KpiSkeleton /><KpiSkeleton /><KpiSkeleton />
          </>
        ) : (
          <>
            <KpiCard label="Total Users" value={total.toLocaleString()} icon={<UsersIcon />} />
            <KpiCard
              label="Aggregate Spend"
              value={money(stats.totalSpend)}
              hint={`avg ${money(stats.avgSpend)} / user`}
              tone="accent"
              icon={<SpendIcon />}
            />
            <KpiCard
              label="Near Budget Cap"
              value={String(stats.nearBudget)}
              hint={stats.nearBudget > 0 ? 'users at ≥80% of max_budget' : 'nobody past 80% of budget'}
              tone={stats.nearBudget > 0 ? 'warn' : 'neutral'}
              icon={<BudgetIcon />}
            />
            <KpiCard
              label="Role Filter"
              value={role || 'any'}
              hint={role ? `filtered to ${role}` : 'all roles'}
              icon={<RoleIcon />}
            />
          </>
        )}
      </div>

      {/* Toolbar */}
      <div className="card" style={{ marginBottom: 16 }}>
        <div className="catalog-toolbar" style={{ flexWrap: 'wrap', gap: 12 }}>
          <input
            className="search-input"
            type="text"
            value={search}
            onChange={(e) => { setSearch(e.target.value); setPage(1); }}
            placeholder="Search alias or email…"
            aria-label="Search users"
            style={{ flex: '1 1 240px' }}
          />
          <select
            className="sort-select"
            value={role}
            onChange={(e) => { setRole(e.target.value); setPage(1); }}
            aria-label="Filter by role"
          >
            <option value="">any role</option>
            <option value="internal_user">internal_user</option>
            <option value="proxy_admin">proxy_admin</option>
            <option value="proxy_admin_viewer">proxy_admin_viewer</option>
          </select>
          <button type="button" className="primary" onClick={() => setShowCreate(true)}>+ Create User</button>
        </div>
      </div>

      {/* Users table */}
      {list.loading && (
        <div className="card" style={{ padding: 0 }}>
          <table className="table">
            <thead>
              <tr>
                {userHeaders}
                <th>Actions</th>
              </tr>
            </thead>
            <tbody>
              <SkeletonRows columns={10} rows={8} />
            </tbody>
          </table>
        </div>
      )}

      {!list.loading && !list.error && users.length === 0 && (
        <EmptyState
          title={search || role ? 'No matching users' : 'No Manage LiteLLM users yet'}
          hint={
            search || role
              ? 'Try adjusting your search query or role filter.'
              : 'Create your first user to start managing LiteLLM-style keys in the dedicated litellm_* tables.'
          }
        />
      )}

      {!list.loading && !list.error && users.length > 0 && (
        <div className="card" style={{ padding: 0 }}>
          <table className="table">
            <thead>
              <tr>
                {userHeaders}
                <th>Actions</th>
              </tr>
            </thead>
            <tbody>
              {users.map((u) => {
                const pct = u.max_budget && Number(u.max_budget) > 0
                  ? Math.min(100, (u.spend / Number(u.max_budget)) * 100)
                  : null;
                return (
                  <tr key={u.id}>
                    <td>
                      <span style={{ fontWeight: 600 }}>{u.user_alias || u.id}</span>
                      <div className="dim mono" style={{ fontSize: 11 }}>{u.id}</div>
                    </td>
                    <td className="mono dim">{u.user_email || '—'}</td>
                    <td><RoleBadge role={u.user_role} /></td>
                    <td className="mono">{u.key_count ?? 0}</td>
                    <td className="mono">{money(u.spend)}</td>
                    <td>
                      {pct === null ? (
                        <span className="dim">unlimited</span>
                      ) : (
                        <BudgetBar pct={pct} spend={u.spend} limit={u.max_budget} />
                      )}
                    </td>
                    <td className="mono">{u.rpm_limit ?? '—'}</td>
                    <td className="mono">{u.tpm_limit ?? '—'}</td>
                    <td className="mono dim">{new Date(u.created_at).toLocaleDateString()}</td>
                    <td>
                      <div className="row-actions">
                        <button onClick={() => setEditing(u)} disabled={busy[`reset-${u.id}`] || busy[`del-${u.id}`]}>Edit</button>
                        {confirmResetId === u.id ? (
                          <>
                            <button className="danger" disabled={busy[`reset-${u.id}`]}
                              onClick={() => handleReset(u.id, u.user_alias)}>Confirm</button>
                            <button onClick={() => setConfirmResetId(null)}>Cancel</button>
                          </>
                        ) : (
                          <button onClick={() => setConfirmResetId(u.id)} disabled={busy[`reset-${u.id}`]}
                            title="Reset cumulative spend">Reset</button>
                        )}
                        {confirmDeleteId === u.id ? (
                          <>
                            <button className="danger" disabled={busy[`del-${u.id}`]}
                              onClick={() => handleDelete(u.id, u.user_alias)}>Confirm</button>
                            <button onClick={() => setConfirmDeleteId(null)}>Cancel</button>
                          </>
                        ) : (
                          <button className="danger" onClick={() => setConfirmDeleteId(u.id)}
                            disabled={busy[`del-${u.id}`]}>Delete</button>
                        )}
                      </div>
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
          <div style={{ padding: '12px 16px' }}>
            <Pager page={page} totalPages={totalPages} total={total} pageSize={DEFAULT_PAGE_SIZE}
              onPageChange={setPage} />
          </div>
        </div>
      )}

      {showCreate && (
        <CreateUserModal
          onClose={() => setShowCreate(false)}
          onCreated={() => { setShowCreate(false); list.reload(); }}
        />
      )}
      {editing && (
        <EditUserModal
          user={editing}
          onClose={() => setEditing(null)}
          onSaved={() => { setEditing(null); list.reload(); }}
        />
      )}
    </>
  );
}

function CreateUserModal({ onClose, onCreated }) {
  const toast = useToast();
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
    const patch = formToPatch(form);
    patch.auto_create_key = autoCreateKey;
    try {
      const result = await createLiteLLMUser(patch);
      setCreated(result);
      if (!result.api_key && !result.secret && !result.auto_create_error) {
        onCreated();
      }
    } catch (err) {
      setError(err.message);
    } finally {
      setSubmitting(false);
    }
  }

  if (created) {
    return (
      <Modal title="Manage LiteLLM — User Created" onClose={onCreated}>
        <div className="form__row">
          <label className="form__label">User</label>
          <div>{created.user?.user_alias || created.user?.id}</div>
          <div className="dim mono" style={{ fontSize: 11 }}>{created.user?.id}</div>
        </div>
        {created.auto_create_error ? (
          <div className="error-banner">
            Auto-create key failed: {created.auto_create_error}. The user row was
            persisted; attach an API key from the API Keys tab.
          </div>
        ) : created.secret ? (
          <div className="form__row">
            <label className="form__label">API key secret (shown once)</label>
            <div className="row" style={{ gap: 8, alignItems: 'center' }}>
              <div className="copyable" style={{ flex: 1, fontFamily: 'var(--mono)', fontSize: 13 }}>
                {created.secret}
              </div>
              <CopyButton value={created.secret} label="Copy Secret" small />
            </div>
            <div className="form__hint">
              Store this securely. Only its SHA-256 hash is persisted and it
              cannot be retrieved later. The key is auto-attached to this user
              in the litellm_api_keys table.
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
    <Modal title="Create Manage LiteLLM User" onClose={onClose}>
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
            Disable to persist the user row only.
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

function EditUserModal({ user, onClose, onSaved }) {
  const toast = useToast();
  const [form, setForm] = useState(() => userToForm(user));
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState('');

  async function handleSubmit(e) {
    e.preventDefault();
    setSubmitting(true);
    setError('');
    try {
      await patchLiteLLMUser(user.id, formToPatch(form));
      toast.success(`User "${user.user_alias || user.id}" updated`);
      onSaved();
    } catch (err) {
      setError(err.message);
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <Modal title={`Edit User — ${user.user_alias || user.id}`} onClose={onClose}>
      {error && <div className="error-banner">{error}</div>}
      <form onSubmit={handleSubmit}>
        <InternalUserPolicyForm initial={user} onChange={setForm} />
        <div className="form__actions">
          <button type="button" onClick={onClose} disabled={submitting}>Cancel</button>
          <button type="submit" className="primary" disabled={submitting}>
            {submitting ? 'Saving…' : 'Save Changes'}
          </button>
        </div>
      </form>
    </Modal>
  );
}

// --- API Keys tab ------------------------------------------------------------

function KeysTab() {
  const toast = useToast();
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = useState(DEFAULT_PAGE_SIZE);
  const [status, setStatus] = useState('');
  const [search, setSearch] = useState('');
  const [sortBy, setSortBy] = useState('created_at');
  const [sortOrder, setSortOrder] = useState('desc');
  const [showCreate, setShowCreate] = useState(false);
  const [editing, setEditing] = useState(null);
  const [policyFor, setPolicyFor] = useState(null); // key being policy-edited
  const [regenerating, setRegenerating] = useState(null); // key being regenerated
  const [confirmDeleteId, setConfirmDeleteId] = useState(null);
  const [busy, setBusy] = useState({});

  const list = useAsync(
    () => listLiteLLMKeys({ page, pageSize, status, search, sortBy, sortOrder }),
    [page, pageSize, status, search, sortBy, sortOrder],
  );

  const keys = list.data?.api_keys || [];
  const total = list.data?.total ?? 0;
  const totalPages = Math.max(1, Math.ceil(total / pageSize));

  const stats = useMemo(() => {
    const active = keys.filter((k) => k.status === 'active').length;
    const disabled = keys.filter((k) => k.status === 'disabled').length;
    const revoked = keys.filter((k) => k.status === 'revoked' || k.status === 'expired').length;
    const unassigned = keys.filter((k) => !k.user_id).length;
    return { active, disabled, revoked, unassigned };
  }, [keys]);

  async function runBusy(id, fn) {
    setBusy((b) => ({ ...b, [id]: true }));
    try {
      await fn();
    } finally {
      setBusy((b) => ({ ...b, [id]: false }));
    }
  }

  async function handleToggleStatus(k) {
    const next = k.status === 'active' ? 'disabled' : 'active';
    await runBusy(`status-${k.id}`, async () => {
      await patchLiteLLMKey(k.id, { status: next });
      toast.success(`Key "${k.name}" ${next === 'active' ? 'enabled' : 'disabled'}`);
      list.reload();
    }).catch((err) => toast.error(err.message || 'Failed to update key status'));
  }

  function handleSort(column) {
    const next = nextSort(sortBy, sortOrder, column);
    setSortBy(next.by);
    setSortOrder(next.order);
    setPage(1);
  }

  const keyHeaders = (
    <>
      <SortableTh column="name" label="Name & ID" active={sortBy === 'name'} asc={sortOrder === 'asc'} onToggle={handleSort} />
      <SortableTh column="user_alias" label="Owner" active={sortBy === 'user_alias'} asc={sortOrder === 'asc'} onToggle={handleSort} />
      <th>Prefix</th>
      <th>Status</th>
      <SortableTh column="spend" label="Spend" active={sortBy === 'spend'} asc={sortOrder === 'asc'} onToggle={handleSort} />
      <th>Budget</th>
      <th>RPM/TPM</th>
      <th>Expires</th>
      <SortableTh column="last_used_at" label="Last used" active={sortBy === 'last_used_at'} asc={sortOrder === 'asc'} onToggle={handleSort} />
    </>
  );

  async function handleDelete(k) {
    await runBusy(`del-${k.id}`, async () => {
      await deleteLiteLLMKey(k.id);
      toast.success(`Key "${k.name}" deleted`);
      list.reload();
    }).catch((err) => toast.error(err.message || 'Failed to delete key'));
    setConfirmDeleteId(null);
  }

  return (
    <>
      <ErrorBanner error={list.error} onRetry={list.reload} />

      {/* KPI cards */}
      <div className="keys-kpi-grid">
        <div className="keys-kpi-card keys-kpi-card--active">
          <div>
            <div className="keys-kpi-card__val">{total}</div>
            <div className="keys-kpi-card__label">Total API Keys</div>
            <div className="keys-kpi-card__sub">{stats.active} active on this page</div>
          </div>
          <div className="keys-kpi-card__icon"><KeyIcon /></div>
        </div>
        <div className="keys-kpi-card keys-kpi-card--active">
          <div>
            <div className="keys-kpi-card__val" style={{ color: 'var(--success)' }}>{stats.active}</div>
            <div className="keys-kpi-card__label">Active Keys</div>
            <div className="keys-kpi-card__sub">Ready for traffic</div>
          </div>
          <div className="keys-kpi-card__icon" style={{ color: 'var(--success)' }}><CheckCircleIcon /></div>
        </div>
        <div className="keys-kpi-card keys-kpi-card--disabled">
          <div>
            <div className="keys-kpi-card__val" style={{ color: 'var(--warning)' }}>{stats.disabled}</div>
            <div className="keys-kpi-card__label">Disabled Keys</div>
            <div className="keys-kpi-card__sub">Paused by administrator</div>
          </div>
          <div className="keys-kpi-card__icon" style={{ color: 'var(--warning)' }}><PauseCircleIcon /></div>
        </div>
        <div className="keys-kpi-card keys-kpi-card--unassigned">
          <div>
            <div className="keys-kpi-card__val" style={{ color: stats.unassigned > 0 ? '#a78bfa' : 'var(--text-muted)' }}>
              {stats.unassigned}
            </div>
            <div className="keys-kpi-card__label">Unassigned Owner</div>
            <div className="keys-kpi-card__sub">Keys without user link</div>
          </div>
          <div className="keys-kpi-card__icon"><UserAlertIcon /></div>
        </div>
      </div>

      {/* Toolbar */}
      <div className="card" style={{ marginBottom: 16 }}>
        <div className="catalog-toolbar" style={{ flexWrap: 'wrap', gap: 12 }}>
          <input
            className="search-input"
            type="text"
            value={search}
            onChange={(e) => { setSearch(e.target.value); setPage(1); }}
            placeholder="Search name, alias, prefix, key ID…"
            aria-label="Search API keys"
            style={{ flex: '1 1 240px' }}
          />
          <div className="seg-group" role="tablist" aria-label="Status filter">
            {STATUS_OPTIONS.map((opt) => (
              <button
                key={opt.value}
                type="button"
                className={`seg-btn ${status === opt.value ? 'seg-btn--active' : ''}`}
                onClick={() => { setStatus(opt.value); setPage(1); }}
              >
                {opt.label}
              </button>
            ))}
          </div>
          <select
            className="sort-select"
            value={pageSize}
            onChange={(e) => { setPageSize(Number(e.target.value)); setPage(1); }}
            aria-label="Items per page"
          >
            <option value={10}>10 / page</option>
            <option value={25}>25 / page</option>
            <option value={50}>50 / page</option>
            <option value={100}>100 / page</option>
          </select>
          <button type="button" className="primary" onClick={() => setShowCreate(true)}>+ New Key</button>
        </div>
      </div>

      {/* Keys table */}
      {list.loading && (
        <div className="card" style={{ padding: 0 }}>
          <table className="table">
            <thead>
              <tr>
                {keyHeaders}
                <th>Actions</th>
              </tr>
            </thead>
            <tbody>
              <SkeletonRows columns={10} rows={8} />
            </tbody>
          </table>
        </div>
      )}

      {!list.loading && !list.error && keys.length === 0 && (
        <EmptyState
          title={search || status ? 'No matching API keys' : 'No Manage LiteLLM API keys yet'}
          hint={
            search || status
              ? 'Try adjusting your search query or status filter.'
              : 'Create your first key to store complete LiteLLM-style data (spend, budget, tpm_limit, tags, aliases) in litellm_api_keys.'
          }
        />
      )}

      {!list.loading && !list.error && keys.length > 0 && (
        <div className="card" style={{ padding: 0 }}>
          <table className="table">
            <thead>
              <tr>
                {keyHeaders}
                <th>Actions</th>
              </tr>
            </thead>
            <tbody>
              {keys.map((k) => (
                <tr key={k.id}>
                  <td>
                    <span style={{ fontWeight: 600 }}>{k.name}</span>
                    <div className="dim mono" style={{ fontSize: 11 }}>{k.id}</div>
                    {Array.isArray(k.tags) && k.tags.length > 0 && (
                      <div className="row gap-sm" style={{ marginTop: 4 }}>
                        {k.tags.slice(0, 3).map((t) => (
                          <span key={t} className="badge badge--muted" style={{ fontSize: 10 }}>{t}</span>
                        ))}
                        {k.tags.length > 3 && <span className="dim" style={{ fontSize: 10 }}>+{k.tags.length - 3}</span>}
                      </div>
                    )}
                  </td>
                  <td>
                    {k.user_id ? (
                      <span className="owner-pill" title={k.user_id}>
                        <span className="owner-pill__avatar">
                          {(k.user_alias || k.user_email || 'U')[0].toUpperCase()}
                        </span>
                        <span>{k.user_alias || shortOwnerId(k.user_id)}</span>
                      </span>
                    ) : (
                      <span className="dim" style={{ fontSize: 12 }}>unassigned</span>
                    )}
                  </td>
                  <td>
                    <span className="key-prefix-chip">
                      <code>{k.key_prefix}…</code>
                      <CopyButton value={`${k.key_prefix}…`} label="Copy" small />
                    </span>
                  </td>
                  <td><StatusBadge status={k.status} /></td>
                  <td className="mono">{money(k.spend)}</td>
                  <td className="mono dim">
                    {k.policy?.budget_usd ? (
                      <>
                        {money(k.policy.budget_usd)}
                        {k.policy.budget_duration ? (
                          <span className="dim" style={{ fontSize: 11 }}>
                            {' '}· {k.policy.budget_duration}
                          </span>
                        ) : null}
                      </>
                    ) : 'unlimited'}
                  </td>
                  <td className="mono dim">
                    {k.policy?.rpm_limit ?? '∞'} / {k.policy?.tpm_limit ?? '∞'}
                  </td>
                  <td><span style={{ fontSize: 12 }}>{formatExpires(k.expires_at)}</span></td>
                  <td><span style={{ fontSize: 12, color: k.last_used_at ? 'var(--text)' : 'var(--text-dim)' }}>{timeAgo(k.last_used_at)}</span></td>
                  <td>
                    <div className="row-actions" style={{ justifyContent: 'flex-end' }}>
                      <button
                        onClick={() => handleToggleStatus(k)}
                        disabled={busy[`status-${k.id}`]}
                        className={k.status === 'active' ? '' : 'primary'}
                        title={k.status === 'active' ? 'Disable this key' : 'Enable this key'}
                      >
                        {busy[`status-${k.id}`] ? '…' : k.status === 'active' ? 'Disable' : 'Enable'}
                      </button>
                      <button onClick={() => setEditing(k)} disabled={busy[`del-${k.id}`]}>Edit</button>
                      <button onClick={() => setPolicyFor(k)} disabled={busy[`del-${k.id}`]}>Policy</button>
                      <button onClick={() => setRegenerating(k)} disabled={busy[`del-${k.id}`]}>Regenerate</button>
                      {confirmDeleteId === k.id ? (
                        <>
                          <button className="danger" disabled={busy[`del-${k.id}`]}
                            onClick={() => handleDelete(k)}>Confirm</button>
                          <button onClick={() => setConfirmDeleteId(null)}>Cancel</button>
                        </>
                      ) : (
                        <button className="danger" onClick={() => setConfirmDeleteId(k.id)}
                          disabled={busy[`del-${k.id}`]}>Delete</button>
                      )}
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
          <div style={{ padding: '12px 16px' }}>
            <Pager page={page} totalPages={totalPages} total={total} pageSize={pageSize}
              onPageChange={setPage} />
          </div>
        </div>
      )}

      {showCreate && (
        <CreateKeyModal
          onClose={() => setShowCreate(false)}
          onCreated={() => { setShowCreate(false); list.reload(); }}
        />
      )}
      {editing && (
        <EditKeyModal
          keyObj={editing}
          onClose={() => setEditing(null)}
          onSaved={() => { setEditing(null); list.reload(); }}
        />
      )}
      {policyFor && (
        <PolicyModal
          keyObj={policyFor}
          onClose={() => setPolicyFor(null)}
          onSaved={() => { setPolicyFor(null); list.reload(); }}
        />
      )}
      {regenerating && (
        <RegenerateModal
          keyObj={regenerating}
          onClose={() => setRegenerating(null)}
          onDone={() => { setRegenerating(null); list.reload(); }}
        />
      )}
    </>
  );
}

function CreateKeyModal({ onClose, onCreated }) {
  const toast = useToast();
  const [name, setName] = useState('');
  const [search, setSearch] = useState('');
  const [userId, setUserId] = useState('');
  const [expiresAt, setExpiresAt] = useState('');
  const [tagsText, setTagsText] = useState('');
  const [customSecretOn, setCustomSecretOn] = useState(false);
  const [customSecret, setCustomSecret] = useState('');
  const [attachPolicy, setAttachPolicy] = useState(false);
  const [policyForm, setPolicyForm] = useState(null);
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState('');
  const [created, setCreated] = useState(null);
  const [secretCopied, setSecretCopied] = useState(false);

  const usersReq = useAsync(
    () => listLiteLLMUsers({ page: 1, pageSize: 200, sortBy: 'user_alias', sortOrder: 'asc' }),
    [],
  );
  const users = usersReq.data?.users || [];
  const ownerQ = search.trim().toLowerCase();
  const filteredUsers = ownerQ
    ? users.filter((u) => {
        const hay = [u.user_alias, u.user_email, u.id].filter(Boolean).join(' ').toLowerCase();
        return hay.includes(ownerQ);
      })
    : users;

  async function handleSubmit(e) {
    e.preventDefault();
    if (!name) return;
    if (!userId) {
      setError('Please select an Internal User owner. Every key must belong to a user.');
      return;
    }
    const secret = customSecretOn ? customSecret.trim() : '';
    if (customSecretOn && secret.length < 16) {
      setError('Custom secret must be at least 16 characters.');
      return;
    }
    setSubmitting(true);
    setError('');
    try {
      let policy = null;
      if (attachPolicy && policyForm) {
        policy = liteLLMFormToPolicy(policyForm);
      }
      const tags = tagsText.split('\n').map((t) => t.trim()).filter(Boolean);
      const result = await createLiteLLMKey({
        name,
        secret: secret || undefined,
        user_id: userId,
        expires_at: expiresAt ? new Date(expiresAt).toISOString() : null,
        tags,
        policy,
      });
      setCreated(result);
      toast.success(`Key "${name}" created`);
    } catch (err) {
      setError(err.message || 'Failed to create key.');
      toast.error(err.message || 'Failed to create key');
    } finally {
      setSubmitting(false);
    }
  }

  if (created) {
    return (
      <Modal title="Manage LiteLLM — API Key Created" onClose={onClose}>
        <div style={{
          padding: 16,
          background: 'var(--accent-dim)',
          border: '1px solid var(--accent)',
          borderRadius: 'var(--radius)',
          marginBottom: 16,
        }}>
          <div style={{ fontWeight: 600, color: 'var(--accent)', marginBottom: 4 }}>
            🔑 Plaintext Secret (Shown Only Once)
          </div>
          <div className="row" style={{ gap: 8, alignItems: 'center', marginTop: 8 }}>
            <div className="copyable" style={{ flex: 1, fontFamily: 'var(--mono)', fontSize: 13 }}>
              {created.secret}
            </div>
            <CopyButton value={created.secret} label="Copy Secret" small />
          </div>
          <div className="form__hint" style={{ marginTop: 8, color: 'var(--text-muted)' }}>
            Store this key securely now. The litellm_api_keys table persists only its SHA-256 hash.
          </div>
        </div>
        <div className="row gap-sm" style={{ marginTop: 8, fontSize: 12 }}>
          <span className="dim mono">id: {created.id}</span>
          {created.user_id && <span className="dim mono">· owner: {shortOwnerId(created.user_id)}</span>}
        </div>
        <label className="row gap-sm" style={{ cursor: 'pointer', marginTop: 20 }}>
          <input
            type="checkbox"
            checked={secretCopied}
            onChange={(e) => setSecretCopied(e.target.checked)}
            style={{ width: 'auto' }}
          />
          <span className="form__label" style={{ margin: 0 }}>
            I have copied and saved this secret key securely
          </span>
        </label>
        <div className="form__actions">
          <button type="button" className="primary" onClick={onCreated} disabled={!secretCopied}>
            Done
          </button>
        </div>
      </Modal>
    );
  }

  return (
    <Modal title="Issue New Manage LiteLLM Key" onClose={onClose}>
      {error && <div className="error-banner">{error}</div>}
      <form onSubmit={handleSubmit}>
        <div className="form__row">
          <label className="form__label" htmlFor="litellm-key-name">Key Name / Application</label>
          <input
            id="litellm-key-name"
            type="text"
            value={name}
            required
            onChange={(e) => setName(e.target.value)}
            placeholder="e.g. production-rag-service"
            disabled={submitting}
          />
        </div>

        <div className="form__row">
          <label className="form__label" htmlFor="litellm-key-owner">
            Internal User Owner <span style={{ color: 'var(--danger)' }}>*</span>
          </label>
          {usersReq.loading ? (
            <Spinner label="Loading users…" />
          ) : usersReq.error ? (
            <div className="error-banner">{usersReq.error.message || 'Failed to load users.'}</div>
          ) : users.length === 0 ? (
            <div className="dim">
              No Manage LiteLLM users yet. Create one on the Internal Users tab first.
            </div>
          ) : (
            <>
              <input
                type="text"
                value={search}
                onChange={(e) => setSearch(e.target.value)}
                placeholder="Search owner by name, email, or ID…"
                className="search-input"
                aria-label="Search owner"
                disabled={submitting}
                style={{ marginBottom: 8, width: '100%' }}
              />
              <select
                id="litellm-key-owner"
                value={userId}
                onChange={(e) => setUserId(e.target.value)}
                required
                disabled={submitting}
                style={{ width: '100%' }}
                size={Math.min(5, Math.max(3, filteredUsers.length))}
              >
                <option value="">— Select Owner User —</option>
                {filteredUsers.map((u) => (
                  <option key={u.id} value={u.id}>
                    👤 {u.user_alias || u.id}
                    {u.user_email ? ` <${u.user_email}>` : ''}
                    {u.max_budget ? ` · $${Number(u.max_budget).toFixed(2)} budget` : ''}
                  </option>
                ))}
              </select>
            </>
          )}
        </div>

        <div className="grid grid--2">
          <div className="form__row">
            <label className="form__label" htmlFor="litellm-key-expires">Expiration (Optional)</label>
            <input
              id="litellm-key-expires"
              type="datetime-local"
              value={expiresAt}
              onChange={(e) => setExpiresAt(e.target.value)}
              disabled={submitting}
            />
          </div>
          <div className="form__row">
            <label className="form__label" htmlFor="litellm-key-tags">Tags (one per line)</label>
            <textarea
              id="litellm-key-tags"
              rows={2}
              value={tagsText}
              onChange={(e) => setTagsText(e.target.value)}
              placeholder={'production\nrag'}
              disabled={submitting}
            />
          </div>
        </div>

        <div className="form__row" style={{ marginTop: 12 }}>
          <label className="row gap-sm" style={{ cursor: 'pointer' }}>
            <input
              type="checkbox"
              checked={customSecretOn}
              onChange={(e) => {
                setCustomSecretOn(e.target.checked);
                if (!e.target.checked) setCustomSecret('');
              }}
              style={{ width: 'auto' }}
            />
            <span className="form__label" style={{ margin: 0 }}>
              Supply custom secret string (min 16 chars)
            </span>
          </label>
          {customSecretOn && (
            <input
              type="text"
              value={customSecret}
              onChange={(e) => setCustomSecret(e.target.value)}
              placeholder="e.g. sk-my-custom-secret-key-0123456789"
              disabled={submitting}
              style={{ width: '100%', marginTop: 8 }}
              minLength={16}
              required={customSecretOn}
            />
          )}
        </div>

        <div className="form__row" style={{ marginTop: 12 }}>
          <label className="row gap-sm" style={{ cursor: 'pointer' }}>
            <input
              type="checkbox"
              checked={attachPolicy}
              onChange={(e) => setAttachPolicy(e.target.checked)}
              style={{ width: 'auto' }}
            />
            <span className="form__label" style={{ margin: 0 }}>
              Configure per-key Policy (RPM, TPM, Budget, Model access, Aliases)
            </span>
          </label>
        </div>

        {attachPolicy && (
          <div style={{
            marginTop: 12,
            padding: 16,
            background: 'var(--bg)',
            borderRadius: 'var(--radius-sm)',
            border: '1px solid var(--border)',
          }}>
            <LiteLLMPolicyForm initial={null} onChange={setPolicyForm} />
          </div>
        )}

        <div className="form__actions">
          <button type="button" onClick={onClose} disabled={submitting}>Cancel</button>
          <button type="submit" className="primary" disabled={submitting || !name || !userId}>
            {submitting ? 'Issuing Key…' : 'Issue API Key'}
          </button>
        </div>
      </form>
    </Modal>
  );
}

function EditKeyModal({ keyObj, onClose, onSaved }) {
  const toast = useToast();
  const [name, setName] = useState(keyObj.name || '');
  const [alias, setAlias] = useState(keyObj.key_alias || '');
  const [status, setStatus] = useState(keyObj.status || 'active');
  const [expiresAt, setExpiresAt] = useState(
    keyObj.expires_at ? localDateTime(keyObj.expires_at) : '',
  );
  const [clearExpiry, setClearExpiry] = useState(false);
  const [tagsText, setTagsText] = useState(
    Array.isArray(keyObj.tags) ? keyObj.tags.join('\n') : '',
  );
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState('');

  function localDateTime(iso) {
    const d = new Date(iso);
    const pad = (n) => String(n).padStart(2, '0');
    return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
  }

  async function handleSubmit(e) {
    e.preventDefault();
    setSubmitting(true);
    setError('');
    try {
      const patch = {
        name,
        alias,
        status,
        tags: tagsText.split('\n').map((t) => t.trim()).filter(Boolean),
      };
      if (clearExpiry) {
        patch.clear_expiry = true;
      } else if (expiresAt) {
        patch.expires_at = new Date(expiresAt).toISOString();
      }
      await patchLiteLLMKey(keyObj.id, patch);
      toast.success(`Key "${name}" updated`);
      onSaved();
    } catch (err) {
      setError(err.message);
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <Modal title={`Edit Key — ${keyObj.name}`} onClose={onClose}>
      {error && <div className="error-banner">{error}</div>}
      <form onSubmit={handleSubmit}>
        <div className="grid grid--2">
          <div className="form__row">
            <label className="form__label">Name</label>
            <input type="text" value={name} onChange={(e) => setName(e.target.value)} />
          </div>
          <div className="form__row">
            <label className="form__label">Alias</label>
            <input type="text" value={alias} onChange={(e) => setAlias(e.target.value)} placeholder="non-secret label" />
          </div>
          <div className="form__row">
            <label className="form__label">Status</label>
            <select value={status} onChange={(e) => setStatus(e.target.value)}>
              <option value="active">active</option>
              <option value="disabled">disabled</option>
              <option value="revoked">revoked</option>
              <option value="expired">expired</option>
            </select>
          </div>
          <div className="form__row">
            <label className="form__label">Expiration</label>
            <input type="datetime-local" value={expiresAt} onChange={(e) => { setExpiresAt(e.target.value); setClearExpiry(false); }} />
          </div>
        </div>
        <div className="form__row">
          <label className="row gap-sm" style={{ cursor: 'pointer' }}>
            <input
              type="checkbox"
              checked={clearExpiry}
              onChange={(e) => { setClearExpiry(e.target.checked); if (e.target.checked) setExpiresAt(''); }}
              style={{ width: 'auto' }}
            />
            <span className="form__label" style={{ margin: 0 }}>Clear expiry (make key unexpiring)</span>
          </label>
        </div>
        <div className="form__row">
          <label className="form__label" htmlFor="litellm-edit-tags">Tags (one per line)</label>
          <textarea id="litellm-edit-tags" rows={3} value={tagsText} onChange={(e) => setTagsText(e.target.value)} />
        </div>
        <div className="form__actions">
          <button type="button" onClick={onClose} disabled={submitting}>Cancel</button>
          <button type="submit" className="primary" disabled={submitting}>
            {submitting ? 'Saving…' : 'Save Changes'}
          </button>
        </div>
      </form>
    </Modal>
  );
}

function PolicyModal({ keyObj, onClose, onSaved }) {
  const toast = useToast();
  const [loading, setLoading] = useState(true);
  const [policy, setPolicy] = useState(null);
  const [form, setForm] = useState(null);
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState('');

  // Fetch the latest key so the policy row (if any) is fresh.
  const keyReq = useAsync(() => getLiteLLMKey(keyObj.id), []);

  useEffect(() => {
    if (keyReq.data) {
      setPolicy(keyReq.data.policy || null);
      setForm(liteLLMPolicyToForm(keyReq.data.policy || null));
      setLoading(false);
    }
  }, [keyReq.data]);

  async function handleSubmit(e) {
    e.preventDefault();
    if (!form) { onClose(); return; }
    setSubmitting(true);
    setError('');
    try {
      const p = liteLLMFormToPolicy(form);
      await putLiteLLMKeyPolicy(keyObj.id, p);
      toast.success(`Policy updated for "${keyObj.name}"`);
      onSaved();
    } catch (err) {
      setError(err.message);
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <Modal title={`Policy — ${keyObj.name}`} onClose={onClose} size="lg">
      {error && <div className="error-banner">{error}</div>}
      {loading ? (
        <Spinner label="Loading policy…" />
      ) : (
        <form onSubmit={handleSubmit}>
          <LiteLLMPolicyForm initial={policy} onChange={setForm} />
          <div className="form__actions">
            <button type="button" onClick={onClose} disabled={submitting}>Cancel</button>
            <button type="submit" className="primary" disabled={submitting}>
              {submitting ? 'Saving…' : 'Save Policy'}
            </button>
          </div>
        </form>
      )}
    </Modal>
  );
}

function RegenerateModal({ keyObj, onClose, onDone }) {
  const toast = useToast();
  const [customSecretOn, setCustomSecretOn] = useState(false);
  const [customSecret, setCustomSecret] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState('');
  const [newSecret, setNewSecret] = useState(null);
  const [secretCopied, setSecretCopied] = useState(false);

  async function handleSubmit(e) {
    e.preventDefault();
    const secret = customSecretOn ? customSecret.trim() : '';
    if (customSecretOn && secret.length < 16) {
      setError('Custom secret must be at least 16 characters.');
      return;
    }
    setSubmitting(true);
    setError('');
    try {
      const res = await regenerateLiteLLMKey(keyObj.id, secret);
      setNewSecret(res.secret);
      toast.success('Key regenerated — the old secret no longer works');
    } catch (err) {
      setError(err.message);
    } finally {
      setSubmitting(false);
    }
  }

  if (newSecret) {
    return (
      <Modal title="Key Regenerated" onClose={onClose}>
        <div style={{
          padding: 16,
          background: 'var(--accent-dim)',
          border: '1px solid var(--accent)',
          borderRadius: 'var(--radius)',
          marginBottom: 16,
        }}>
          <div style={{ fontWeight: 600, color: 'var(--accent)', marginBottom: 4 }}>
            🔑 New Plaintext Secret (Shown Only Once)
          </div>
          <div className="row" style={{ gap: 8, alignItems: 'center', marginTop: 8 }}>
            <div className="copyable" style={{ flex: 1, fontFamily: 'var(--mono)', fontSize: 13 }}>
              {newSecret}
            </div>
            <CopyButton value={newSecret} label="Copy Secret" small />
          </div>
        </div>
        <label className="row gap-sm" style={{ cursor: 'pointer', marginTop: 8 }}>
          <input
            type="checkbox"
            checked={secretCopied}
            onChange={(e) => setSecretCopied(e.target.checked)}
            style={{ width: 'auto' }}
          />
          <span className="form__label" style={{ margin: 0 }}>
            I have copied and saved this secret key securely
          </span>
        </label>
        <div className="form__actions">
          <button type="button" className="primary" onClick={onDone} disabled={!secretCopied}>Done</button>
        </div>
      </Modal>
    );
  }

  return (
    <Modal title={`Regenerate Key — ${keyObj.name}`} onClose={onClose}>
      {error && <div className="error-banner">{error}</div>}
      <p className="dim" style={{ fontSize: 13 }}>
        Regenerating issues a new secret for <code className="mono">{keyObj.name}</code>.
        The old secret stops working immediately; the key ID, policy, tags, and
        metadata are preserved.
      </p>
      <form onSubmit={handleSubmit}>
        <div className="form__row" style={{ marginTop: 8 }}>
          <label className="row gap-sm" style={{ cursor: 'pointer' }}>
            <input
              type="checkbox"
              checked={customSecretOn}
              onChange={(e) => {
                setCustomSecretOn(e.target.checked);
                if (!e.target.checked) setCustomSecret('');
              }}
              style={{ width: 'auto' }}
            />
            <span className="form__label" style={{ margin: 0 }}>
              Supply custom secret string (min 16 chars)
            </span>
          </label>
          {customSecretOn && (
            <input
              type="text"
              value={customSecret}
              onChange={(e) => setCustomSecret(e.target.value)}
              placeholder="e.g. sk-my-custom-secret-key-0123456789"
              disabled={submitting}
              style={{ width: '100%', marginTop: 8 }}
              minLength={16}
              required={customSecretOn}
            />
          )}
        </div>
        <div className="form__actions">
          <button type="button" onClick={onClose} disabled={submitting}>Cancel</button>
          <button type="submit" className="primary" disabled={submitting}>
            {submitting ? 'Regenerating…' : 'Regenerate Key'}
          </button>
        </div>
      </form>
    </Modal>
  );
}

// --- Sync Settings tab -------------------------------------------------------

function SyncSettingsTab() {
  const toast = useToast();
  const settingsReq = useAsync(() => getLiteLLMSyncSettings(), []);
  const [draft, setDraft] = useState(null);
  const [saving, setSaving] = useState(false);
  const [syncPending, setSyncPending] = useState(false);
  const [masterKeyDirty, setMasterKeyDirty] = useState(false);
  const [masterKey, setMasterKey] = useState('');
  const [nixllmSyncing, setNixllmSyncing] = useState(false);
  const [includeUsage, setIncludeUsage] = useState(false);
  const [includeKeys, setIncludeKeys] = useState(false);
  const [includeLogs, setIncludeLogs] = useState(false);

  useEffect(() => {
    if (settingsReq.data?.settings) {
      const s = settingsReq.data.settings;
      setDraft({
        enabled: !!s.enabled,
        interval_seconds: s.interval_seconds ?? 300,
        base_url: s.base_url ?? '',
      });
    }
  }, [settingsReq.data]);

  function set(patch) {
    setDraft((d) => ({ ...(d || {}), ...patch }));
  }

  async function handleSave() {
    setSaving(true);
    try {
      const body = {
        enabled: draft.enabled,
        interval_seconds: Number(draft.interval_seconds) || 300,
        base_url: draft.base_url || '',
      };
      // master_key only when the operator typed something (or explicitly
      // requested a clear via the checkbox). Omit → keep stored key.
      if (masterKeyDirty) {
        body.master_key = masterKey;
      }
      await putLiteLLMSyncSettings(body);
      toast.success('LiteLLM sync settings saved');
      setMasterKeyDirty(false);
      setMasterKey('');
      settingsReq.reload();
    } catch (err) {
      toast.error(err?.message || 'Failed to save sync settings');
    } finally {
      setSaving(false);
    }
  }

  async function handleToggleEnabled(next) {
    setSaving(true);
    try {
      await putLiteLLMSyncSettings({ enabled: next });
      toast.success(next ? 'Auto-sync enabled' : 'Auto-sync paused');
      setDraft((d) => ({ ...d, enabled: next }));
    } catch (err) {
      toast.error(err?.message || 'Failed to update auto-sync');
    } finally {
      setSaving(false);
    }
  }

  async function handleSyncNow() {
    setSyncPending(true);
    try {
      const res = await runLiteLLMSyncNow();
      const s = res?.settings;
      if (s) {
        setDraft((d) => ({ ...d, enabled: s.enabled, interval_seconds: s.interval_seconds, base_url: s.base_url }));
      }
      toast.success(s?.last_sync_status === 'error'
        ? `Sync failed: ${s.last_sync_error || 'unknown error'}`
        : 'Sync completed');
    } catch (err) {
      toast.error(err?.message || 'Failed to run sync');
    } finally {
      setSyncPending(false);
      settingsReq.reload();
    }
  }

  const settings = settingsReq.data?.settings;
  const lastError = settings?.last_sync_error;
  const hasKey = !!settings?.master_key_set;
  const configured = hasKey && !!settings?.base_url;
  const nixllmLastSyncAt = settings?.last_nixllm_sync_at;
  const nixllmLastError = settings?.last_nixllm_sync_error;
  const nixllmLastCount = settings?.last_nixllm_sync_users ?? 0;
  const nixllmLastKeys = settings?.last_nixllm_sync_keys ?? 0;
  const nixllmLastLogs = settings?.last_nixllm_sync_logs ?? 0;
  const nixllmLastUsage = !!settings?.last_nixllm_sync_usage;
  const liteLLMLastUpdate = settings?.last_litellm_users_updated_at;

  // handleSyncToNixLLM runs the "sync to NixLLM" migration: Internal Users
  // into the runtime internal_users table, plus (when checked) API Keys and
  // usage logs from the external instance. The Manage-LiteLLM tables are
  // unchanged by it.
  async function handleSyncToNixLLM() {
    setNixllmSyncing(true);
    try {
      const res = await runLiteLLMSyncNixLLM({ includeUsage, includeKeys, includeLogs });
      const s = res?.settings;
      if (s?.last_nixllm_sync_status === 'error') {
        toast.error(`Sync to NixLLM failed: ${s.last_nixllm_sync_error || 'unknown error'}`);
      } else {
        toast.success(`Synced ${s?.last_nixllm_sync_users ?? 0} user${(s?.last_nixllm_sync_users ?? 0) === 1 ? '' : 's'} to NixLLM Internal Users`);
      }
    } catch (err) {
      toast.error(err?.message || 'Failed to sync to NixLLM');
    } finally {
      setNixllmSyncing(false);
      settingsReq.reload();
    }
  }

  return (
    <>
      <ErrorBanner error={settingsReq.error} onRetry={settingsReq.reload} />

      {/* Status card */}
      <div className="card" style={{ marginBottom: 16 }}>
        <div className="row row--between" style={{ marginBottom: 12 }}>
          <h3 className="card__title" style={{ margin: 0 }}>External LiteLLM sync</h3>
          <button
            type="button"
            className="primary"
            onClick={handleSyncNow}
            disabled={syncPending || settingsReq.loading}
            title={configured ? 'Pull users + keys from the external instance now' : 'Configure base_url + master key first'}
          >
            {syncPending ? 'Syncing…' : 'Sync now'}
          </button>
        </div>

        {settingsReq.loading ? (
          <CardSkeleton rows={3} />
        ) : (
          <>
            <div className="row gap-sm" style={{ flexWrap: 'wrap', alignItems: 'center', marginBottom: 8 }}>
              <span className={`sync-pill ${lastError ? 'sync-pill--error' : settings?.enabled ? '' : 'sync-pill--idle'}`}>
                {lastError ? (
                  <>
                    <span className="sync-pill__dot sync-pill__dot--err" />
                    <span>Last sync failed</span>
                  </>
                ) : settings?.enabled ? (
                  <>
                    <span className="sync-pill__dot" />
                    <span>Auto-sync on {settings?.last_sync_at ? `· last synced ${formatRelativeTime(settings.last_sync_at)}` : '· not synced yet'}</span>
                  </>
                ) : (
                  <>
                    <span className="sync-pill__dot sync-pill__dot--idle" />
                    <span>Auto-sync paused</span>
                  </>
                )}
              </span>
              {settings?.last_sync_at && !lastError && (
                <span className="dim" style={{ fontSize: 12 }}>
                  {settings.last_sync_users ?? 0} users · {settings.last_sync_keys ?? 0} keys · {settings.last_sync_logs ?? 0} logs pulled
                </span>
              )}
              {hasKey && settings?.master_key_prefix && (
                <span className="dim mono" style={{ fontSize: 12 }}>· master key {settings.master_key_prefix}…</span>
              )}
            </div>
            {lastError && (
              <div className="error-banner" style={{ marginTop: 4 }}>
                {lastError}
              </div>
            )}
            {!configured && (
              <div className="dim" style={{ fontSize: 12, marginTop: 4 }}>
                Configure a base URL + master API key below to enable syncing from an external LiteLLM instance.
              </div>
            )}
          </>
        )}
      </div>

      {/* Sync to NixLLM card */}
      <div className="card" style={{ marginBottom: 16 }}>
        <div className="row row--between" style={{ marginBottom: 12 }}>
          <h3 className="card__title" style={{ margin: 0 }}>Sync to NixLLM</h3>
          <button
            type="button"
            className="primary"
            onClick={handleSyncToNixLLM}
            disabled={nixllmSyncing || settingsReq.loading}
            title="Refreshes from the external LiteLLM instance first, then copies the Manage-LiteLLM Internal Users into the NixLLM Internal Users the proxy enforces. Check the options to also migrate API Keys and usage logs."
          >
            {nixllmSyncing ? 'Syncing…' : 'Sync to NixLLM'}
          </button>
        </div>

        {settingsReq.loading ? (
          <CardSkeleton rows={2} />
        ) : (
          <>
            <div className="row gap-sm" style={{ flexWrap: 'wrap', alignItems: 'center', marginBottom: 8 }}>
              <span className={`sync-pill ${nixllmLastError ? 'sync-pill--error' : nixllmLastSyncAt ? '' : 'sync-pill--idle'}`} title={nixllmLastError || undefined}>
                {nixllmLastError ? (
                  <>
                    <span className="sync-pill__dot sync-pill__dot--err" />
                    <span>Last sync to NixLLM failed</span>
                  </>
                ) : nixllmLastSyncAt ? (
                  <>
                    <span className="sync-pill__dot" />
                    <span>Last synced to NixLLM {formatRelativeTime(nixllmLastSyncAt)}</span>
                  </>
                ) : (
                  <>
                    <span className="sync-pill__dot sync-pill__dot--idle" />
                    <span>Not synced to NixLLM yet</span>
                  </>
                )}
              </span>
              {nixllmLastSyncAt && (
                <span className="dim" style={{ fontSize: 12 }}>
                  {nixllmLastCount} user{nixllmLastCount === 1 ? '' : 's'}
                  {nixllmLastKeys > 0 ? ` · ${nixllmLastKeys} key${nixllmLastKeys === 1 ? '' : 's'}` : ''}
                  {nixllmLastLogs > 0 ? ` · ${nixllmLastLogs} log${nixllmLastLogs === 1 ? '' : 's'}` : ''}
                  {nixllmLastUsage ? ' · usage included' : ''} · last update from LiteLLM{' '}
                  {liteLLMLastUpdate ? formatRelativeTime(liteLLMLastUpdate) : 'unknown'}
                </span>
              )}
            </div>
            {nixllmLastError && (
              <div className="error-banner" style={{ marginTop: 4 }}>
                {nixllmLastError}
              </div>
            )}
            <div className="row gap-lg" style={{ flexWrap: 'wrap', marginTop: 8 }}>
              <label className="row gap-sm" style={{ cursor: 'pointer', alignItems: 'center' }} title="Also overwrite the NixLLM Internal User spend with the LiteLLM usage values.">
                <input
                  type="checkbox"
                  checked={includeUsage}
                  onChange={(e) => setIncludeUsage(e.target.checked)}
                  style={{ width: 'auto' }}
                />
                <span style={{ fontSize: 12 }}>Include usage</span>
              </label>
              <label className="row gap-sm" style={{ cursor: 'pointer', alignItems: 'center' }} title="Migrate the Manage-LiteLLM API Keys into the runtime api_keys table (policies mapped lossily; existing secrets keep working).">
                <input
                  type="checkbox"
                  checked={includeKeys}
                  onChange={(e) => setIncludeKeys(e.target.checked)}
                  style={{ width: 'auto' }}
                />
                <span style={{ fontSize: 12 }}>Include API Keys</span>
              </label>
              <label className="row gap-sm" style={{ cursor: 'pointer', alignItems: 'center' }} title="Pull /spend/logs from the external LiteLLM instance into usage_events and reconcile user spend. Requires base_url + master key below.">
                <input
                  type="checkbox"
                  checked={includeLogs}
                  onChange={(e) => setIncludeLogs(e.target.checked)}
                  style={{ width: 'auto' }}
                />
                <span style={{ fontSize: 12 }}>Include usage logs</span>
              </label>
            </div>
            <div className="dim" style={{ fontSize: 12, marginTop: 8 }}>
              This first runs the external pull (users + keys + spend logs from the configured instance, like "Sync now"), then copies
              Manage-LiteLLM Internal Users into the runtime internal_users table the proxy enforces (upsert-only, never deletes).
              API Keys keep their hashes so existing client secrets keep working; budgets are enforced only where they map exactly
              (7d → weekly, 30d → monthly) and other budget windows are preserved in the key metadata. Usage logs are pulled from
              the external instance and require a base URL + master key; user spend is reconciled from the imported history.
            </div>
          </>
        )}
      </div>

      {/* Settings form */}
      <div className="card">
        <h3 className="card__title">Connection settings</h3>
        {draft && (
          <div style={{ marginTop: 8 }}>
            <div className="form__row" style={{ marginBottom: 12 }}>
              <ToggleRow
                label="Auto-sync enabled"
                hint="Pull Internal Users and API Keys from the external LiteLLM instance on the interval below. Upsert-only: local rows are never deleted."
                checked={!!draft.enabled}
                disabled={saving}
                onChange={handleToggleEnabled}
              />
            </div>

            <div className="grid grid--2">
              <div className="form__row">
                <label className="form__label" htmlFor="litellm-sync-url">Base URL</label>
                <input
                  id="litellm-sync-url"
                  type="url"
                  value={draft.base_url || ''}
                  onChange={(e) => set({ base_url: e.target.value })}
                  placeholder="https://litellm.example.com"
                />
                <div className="form__hint">The external LiteLLM server root (e.g. https://host:4000).</div>
              </div>
              <div className="form__row">
                <label className="form__label" htmlFor="litellm-sync-interval">Interval (seconds)</label>
                <input
                  id="litellm-sync-interval"
                  type="number"
                  min={60}
                  step={30}
                  value={draft.interval_seconds ?? 300}
                  onChange={(e) => set({ interval_seconds: e.target.value })}
                />
                <div className="form__hint">Minimum 60 seconds. The sweep re-reads this live.</div>
              </div>
            </div>

            <div className="form__row" style={{ marginTop: 12 }}>
              <label className="form__label" htmlFor="litellm-sync-key">Master API key</label>
              <div style={{ display: 'flex', gap: 12, alignItems: 'flex-start' }}>
                <div style={{ flex: 1 }}>
                  <PasswordInput
                    id="litellm-sync-key"
                    value={masterKey}
                    defaultShown={false}
                    onChange={(v) => {
                      setMasterKey(v);
                      setMasterKeyDirty(true);
                      if (v === '') setMasterKeyDirty(true);
                    }}
                    placeholder={hasKey ? `configured (${settings.master_key_prefix || ''}…)` : 'sk-…'}
                  />
                </div>
                {hasKey && (
                  <button
                    type="button"
                    className="ghost"
                    onClick={() => {
                      setMasterKey('');
                      setMasterKeyDirty(true);
                    }}
                    disabled={saving}
                  >
                    Clear key
                  </button>
                )}
              </div>
              <div className="form__hint">
                {hasKey
                  ? 'A master key is configured. Leave the field empty to keep it; type a new one to rotate; use Clear to remove it.'
                  : 'The external LiteLLM admin (master) key. Stored sealed at rest when PGSTORE_ENCRYPTION_KEY is set; never returned to the UI.'}
              </div>
            </div>

            <div className="form__actions" style={{ marginTop: 20 }}>
              <button type="button" className="primary" onClick={handleSave} disabled={saving || !draft}>
                {saving ? 'Saving…' : 'Save settings'}
              </button>
            </div>
          </div>
        )}
      </div>
    </>
  );
}

// --- Usage Logs tab ---------------------------------------------------------

// UsageLogsTab shows the per-request spend logs that were pulled from the
// external LiteLLM instance into the runtime usage_events table (provider
// "litellm"), with an option to view all providers. Reuses the shared events
// table + detail modal from usageShared so the columns and drill-down behave
// exactly like the Recent Events page.
function UsageLogsTab() {
  const [presetIdx, setPresetIdx] = useState(3); // "Last 24h" default
  const [provider, setProvider] = useState('litellm');
  const [requestId, setRequestId] = useState('');
  const [page, setPage] = useState(1);
  const [selectedId, setSelectedId] = useState(null);
  const [timezone, setTimezone] = useState(() => loadTimezone());

  const range = useMemo(() => presetToRange(PRESETS[presetIdx]), [presetIdx]);

  const events = useAsync(
    () => getUsageEvents({
      page,
      page_size: EVENTS_PAGE_SIZE,
      provider: provider || undefined,
      request_id: requestId.trim() || undefined,
      from: range.from,
      to: range.to,
    }),
    [page, provider, requestId, range.from, range.to],
  );

  return (
    <>
      <ErrorBanner error={events.error} onRetry={events.reload} />

      {/* Filter toolbar */}
      <div className="card" style={{ marginBottom: 16 }}>
        <div className="catalog-toolbar" style={{ flexWrap: 'wrap', gap: 12 }}>
          <div className="seg-group" role="tablist" aria-label="Time range">
            {PRESETS.map((p, i) => (
              <button
                key={p.label}
                type="button"
                className={`seg-btn ${presetIdx === i ? 'seg-btn--active' : ''}`}
                onClick={() => { setPresetIdx(i); setPage(1); }}
              >
                {p.label}
              </button>
            ))}
          </div>
          <select
            className="sort-select"
            value={provider}
            onChange={(e) => { setProvider(e.target.value); setPage(1); }}
            aria-label="Filter by provider"
          >
            <option value="">all providers</option>
            <option value="litellm">litellm (synced)</option>
          </select>
          <input
            className="search-input"
            type="text"
            value={requestId}
            onChange={(e) => { setRequestId(e.target.value); setPage(1); }}
            placeholder="Search request ID…"
            aria-label="Search request ID"
            style={{ flex: '1 1 240px' }}
          />
        </div>
        <div className="dim" style={{ fontSize: 12, marginTop: 8 }}>
          Per-request spend logs in the runtime usage_events table. Rows with provider "litellm" were pulled from the
          external LiteLLM instance by "Sync now" / "Sync to NixLLM" (Include usage logs). Defaults to the last 24h and
          the litellm provider; switch to "all providers" to include NixLLM's own traffic.
        </div>
      </div>

      <EventsTableBody
        events={events}
        page={page}
        pageSize={EVENTS_PAGE_SIZE}
        onPage={setPage}
        onRowClick={setSelectedId}
        timezone={timezone}
      />
      {selectedId && (
        <EventDetailModal id={selectedId} timezone={timezone} onClose={() => setSelectedId(null)} />
      )}
    </>
  );
}

// --- On-the-fly Log tab -----------------------------------------------------

// OnTheFlyLogTab shows the durable audit trail recorded by the on-the-fly
// LiteLLM API key validation provider (litellm_onthefly_log table): for each
// incoming request whose key was validated against the external LiteLLM
// instance, the outcome (synced / unmatched / invalid / error), the resolved
// key/user, latency, and any error. Read-only + purge; 503 when PG is off.
function OnTheFlyLogTab() {
  const toast = useToast();
  const [autoRefresh, setAutoRefresh] = useState(() => readOnTheFlyAutoRefresh());
  const [outcome, setOutcome] = useState('');
  const [keyPrefix, setKeyPrefix] = useState('');
  const [limit, setLimit] = useState(100);
  const [clearing, setClearing] = useState(false);
  const timezone = useMemo(() => loadTimezone(), []);

  const list = useAsync(
    () => listLiteLLMOnTheFlyLog({ limit, outcome, keyPrefix: keyPrefix.trim() }),
    [limit, outcome, keyPrefix],
  );

  const reload = useCallback(() => { list.reload(); }, [list]);
  useAutoRefresh(reload, 10000, autoRefresh);

  function toggleAutoRefresh() {
    setAutoRefresh((v) => {
      const next = !v;
      try { localStorage.setItem(ONTHEFLY_AUTOREFRESH_STORAGE, next ? '1' : '0'); } catch { /* ignore */ }
      return next;
    });
  }

  function handleRefresh() {
    reload();
    toast.info('On-the-fly log refreshed');
  }

  async function handleClear() {
    if (!window.confirm('Clear all on-the-fly validation log rows? This cannot be undone.')) return;
    setClearing(true);
    try {
      const res = await clearLiteLLMOnTheFlyLog();
      toast.success(`Cleared ${Number(res?.deleted || 0).toLocaleString()} log row(s)`);
      reload();
    } catch (err) {
      toast.error(err?.message || 'Failed to clear on-the-fly log');
    } finally {
      setClearing(false);
    }
  }

  const entries = list.data?.entries || [];
  const counts = summarizeOnTheFlyOutcomes(entries);

  return (
    <>
      <div className="row gap-sm" style={{ justifyContent: 'flex-end', marginBottom: 12 }}>
        <button
          className={`autorefresh-chip ${autoRefresh ? '' : 'autorefresh-chip--off'}`}
          onClick={toggleAutoRefresh}
          title={autoRefresh ? 'Auto-refresh every 10s — click to pause' : 'Auto-refresh paused — click to resume'}
        >
          <span className="autorefresh-chip__dot" />
          {autoRefresh ? 'Live' : 'Paused'}
        </button>
        <button onClick={handleRefresh}>Refresh</button>
        <button onClick={handleClear} disabled={clearing || entries.length === 0}>
          {clearing ? 'Clearing…' : 'Clear log'}
        </button>
      </div>

      <div className="stats-grid">
        <div className="stat-card">
          <div className="stat-card__label">Synced</div>
          <div className="stat-card__value">{counts.synced.toLocaleString()}</div>
          <div className="stat-card__hint">key matched + imported</div>
        </div>
        <div className="stat-card">
          <div className="stat-card__label">Unmatched</div>
          <div className="stat-card__value">{counts.unmatched.toLocaleString()}</div>
          <div className="stat-card__hint">valid upstream, no local row</div>
        </div>
        <div className="stat-card">
          <div className="stat-card__label">Invalid</div>
          <div className="stat-card__value">{counts.invalid.toLocaleString()}</div>
          <div className="stat-card__hint">rejected by LiteLLM</div>
        </div>
        <div className="stat-card">
          <div className="stat-card__label">Error</div>
          <div className="stat-card__value">{counts.error.toLocaleString()}</div>
          <div className="stat-card__hint">validation/upstream failure</div>
        </div>
      </div>

      {list.error && <ErrorBanner error={list.error} onRetry={reload} />}

      <div className="card" style={{ marginTop: 16 }}>
        <div className="catalog-toolbar" style={{ flexWrap: 'wrap', gap: 12 }}>
          <select
            className="sort-select"
            value={outcome}
            onChange={(e) => setOutcome(e.target.value)}
            aria-label="Filter by outcome"
          >
            {ONTHEFLY_OUTCOME_OPTIONS.map((o) => (
              <option key={o.value} value={o.value}>{o.label}</option>
            ))}
          </select>
          <input
            className="search-input"
            type="text"
            value={keyPrefix}
            onChange={(e) => setKeyPrefix(e.target.value)}
            placeholder="Filter by key prefix…"
            aria-label="Filter by key prefix"
            style={{ flex: '1 1 220px' }}
          />
          <label className="filter-label" style={{ marginLeft: 'auto' }}>
            Limit
            <select value={limit} onChange={(e) => setLimit(Number(e.target.value))}>
              {ONTHEFLY_LIMIT_OPTIONS.map((n) => (
                <option key={n} value={n}>{n}</option>
              ))}
            </select>
          </label>
        </div>
        <div className="dim" style={{ fontSize: 12, marginTop: 8 }}>
          Per-request LiteLLM API key validation outcomes from the <code className="mono">litellm_onthefly_log</code> table
          (30-day retention). Enable on-the-fly validation in Sync Settings. Times shown in {timezone}. Auto-refreshes every 10s.
        </div>
      </div>

      <div className="card" style={{ marginTop: 16, padding: 0 }}>
        <OnTheFlyLogTableBody
          loading={list.loading}
          error={list.error}
          entries={entries}
          timezone={timezone}
        />
      </div>
    </>
  );
}

function OnTheFlyLogTableBody({ loading, error, entries, timezone }) {
  const columns = 9;
  if (loading) {
    return (
      <OnTheFlyTableShell timezone={timezone}>
        <SkeletonRows columns={columns} rows={5} />
      </OnTheFlyTableShell>
    );
  }
  if (error) return <ErrorBanner error={error} />;
  return (
    <OnTheFlyTableShell timezone={timezone}>
      {entries.length === 0 && (
        <tr>
          <td colSpan={columns} style={{ textAlign: 'center', padding: '24px 8px' }}>
            No on-the-fly validations recorded yet.
          </td>
        </tr>
      )}
      {entries.map((e) => (
        <tr key={e.id} className="table__row">
          <td className="mono" style={{ whiteSpace: 'nowrap' }}>{formatInTZ(e.occurred_at, timezone)}</td>
          <td><span className={`badge ${onTheFlyOutcomeBadge(e.outcome)}`}>{formatOnTheFlyOutcome(e.outcome)}</span></td>
          <td className="mono">{e.key_prefix || '—'}</td>
          <td className="mono" style={{ maxWidth: 160, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }} title={e.key_id || ''}>{e.key_id || '—'}</td>
          <td className="mono">{e.user_id || '—'}</td>
          <td>{e.source || '—'}</td>
          <td style={{ textAlign: 'right' }} className="mono">{e.latency_ms ?? 0}ms</td>
          <td className="mono">{e.request_id || '—'}</td>
          <td className="mono" style={{ maxWidth: 320, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }} title={e.error_message || ''}>
            {e.error_message || <span style={{ color: 'var(--text-dim)' }}>—</span>}
          </td>
        </tr>
      ))}
    </OnTheFlyTableShell>
  );
}

function OnTheFlyTableShell({ children, timezone }) {
  return (
    <div style={{ overflowX: 'auto' }}>
      <table className="table">
        <thead>
          <tr>
            <th>Time{timezone ? ` (${timezone})` : ''}</th>
            <th>Outcome</th>
            <th>Key prefix</th>
            <th>Key ID</th>
            <th>User ID</th>
            <th>Source</th>
            <th style={{ textAlign: 'right' }}>Latency</th>
            <th>Request ID</th>
            <th>Error</th>
          </tr>
        </thead>
        <tbody>{children}</tbody>
      </table>
    </div>
  );
}

// --- Shared bits -------------------------------------------------------------

// SortableTh renders a clickable table-header that toggles the sort column +
// direction. `column` is the backend sort_by value; `active`/`asc` reflect the
// current sort; `onToggle` is invoked with the (column, nextOrder) pair. Uses
// the shared .th-sortable CSS from global.css (same pattern as Models Catalog).
function SortableTh({ column, label, active, asc, onToggle, className = '' }) {
  return (
    <th
      className={`th-sortable ${active ? 'th-sort--active' : ''} ${className}`}
      onClick={() => onToggle(column, asc ? 'asc' : 'desc')}
    >
      {label}
      <span className="th-sort__icon">
        {active ? (asc ? '▲' : '▼') : '↕'}
      </span>
    </th>
  );
}

// toggleSort builds the next (sortBy, sortOrder) state for a SortableTh click.
// Clicking an inactive column starts descending (newest/highest first);
// clicking the active column flips asc/desc.
function nextSort(currentBy, currentOrder, column) {
  if (currentBy !== column) {
    return { by: column, order: 'desc' };
  }
  return { by: column, order: currentOrder === 'asc' ? 'desc' : 'asc' };
}

function SkeletonRows({ columns, rows }) {
  return (
    <>
      {Array.from({ length: rows }).map((_, i) => (
        <tr key={i} className="skeleton-row">
          {Array.from({ length: columns }).map((__, j) => (
            <td key={j}><span className="skeleton-line" /></td>
          ))}
        </tr>
      ))}
    </>
  );
}

function BudgetBar({ pct, spend, limit }) {
  const color = pct >= 100 ? 'var(--danger)' : pct >= 80 ? 'var(--warning)' : 'var(--success)';
  return (
    <div>
      <div style={{ height: 8, width: '100%', background: 'var(--bg)', borderRadius: 4, overflow: 'hidden' }}>
        <div style={{ width: `${pct}%`, height: '100%', background: color, minWidth: 2 }} />
      </div>
      <div className="dim mono" style={{ fontSize: 11, marginTop: 2 }}>
        ${Number(spend).toFixed(2)} / ${Number(limit).toFixed(2)} ({pct.toFixed(0)}%)
      </div>
    </div>
  );
}

const ROLE_BADGE_CLASS = {
  internal_user: 'badge--active',
  proxy_admin: 'badge--revoked',
  proxy_admin_viewer: 'badge--muted',
};

function RoleBadge({ role }) {
  const cls = ROLE_BADGE_CLASS[role] || 'badge--muted';
  return <span className={`badge ${cls}`}>{role || 'unknown'}</span>;
}

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

function BudgetIcon() {
  return (
    <svg {...ITEM_PROPS}>
      <path d="M8 1.5l6 12.5H2L8 1.5z" />
      <path d="M8 6v3M8 10.5v.5" />
    </svg>
  );
}

function RoleIcon() {
  return (
    <svg {...ITEM_PROPS}>
      <circle cx="8" cy="6" r="2.5" />
      <path d="M3.5 13.5c0-2.2 2-3.5 4.5-3.5s4.5 1.3 4.5 3.5" />
    </svg>
  );
}

function KeyIcon() {
  return (
    <svg {...ITEM_PROPS}>
      <circle cx="5" cy="11" r="2.5" />
      <path d="M7 9l6-6M10 6l2 2" strokeLinecap="round" />
    </svg>
  );
}

function CheckCircleIcon() {
  return (
    <svg {...ITEM_PROPS}>
      <path d="M13.5 8A5.5 5.5 0 1 1 8 2.5a5.5 5.5 0 0 1 3.9 1.6" />
      <path d="M5.5 8l2 2 5.5-5.5" />
    </svg>
  );
}

function PauseCircleIcon() {
  return (
    <svg {...ITEM_PROPS}>
      <circle cx="8" cy="8" r="5.5" />
      <path d="M6 6v4M10 6v4" />
    </svg>
  );
}

function UserAlertIcon() {
  return (
    <svg {...ITEM_PROPS}>
      <circle cx="8" cy="6" r="2.5" />
      <path d="M3.5 14c0-2.2 2-3.5 4.5-3.5s4.5 1.3 4.5 3.5" />
      <path d="M12.5 10l3 3M15.5 10l-3 3" />
    </svg>
  );
}
