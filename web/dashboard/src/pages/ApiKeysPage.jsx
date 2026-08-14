import React, { useMemo, useState, useEffect } from 'react';
import { Link, useNavigate } from 'react-router-dom';
import { listAPIKeys, createAPIKey, patchAPIKey, listInternalUsers, importAPIKeys } from '../api/client.js';
import { useAsync } from '../hooks/useAsync.js';
import { Spinner, ErrorBanner, EmptyState, StatusBadge, Modal } from '../components/Primitives.jsx';
import Pager from '../components/Pager.jsx';
import PolicyForm, { formToPolicy } from '../components/PolicyForm.jsx';
import CopyButton from '../components/CopyButton.jsx';
import { useToast } from '../components/Toast.jsx';

const DEFAULT_PAGE_SIZE = 25;
const STATUS_OPTIONS = [
  { value: '', label: 'All Statuses' },
  { value: 'active', label: 'Active' },
  { value: 'disabled', label: 'Disabled' },
  { value: 'revoked', label: 'Revoked' },
  { value: 'expired', label: 'Expired' },
];

const SORT_OPTIONS = [
  { value: 'created_at:desc', label: 'Newest Created' },
  { value: 'created_at:asc', label: 'Oldest Created' },
  { value: 'name:asc', label: 'Name (A-Z)' },
  { value: 'last_used_at:desc', label: 'Recently Used' },
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

export default function ApiKeysPage() {
  const toast = useToast();
  const navigate = useNavigate();
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = useState(DEFAULT_PAGE_SIZE);
  const [status, setStatus] = useState('');
  const [search, setSearch] = useState('');
  const [sortOption, setSortOption] = useState('created_at:desc');
  const [selectedIds, setSelectedIds] = useState([]);
  const [showCreate, setShowCreate] = useState(false);
  const [showImport, setShowImport] = useState(false);
  const [actionLoading, setActionLoading] = useState({});

  const [sortBy, sortOrder] = useMemo(() => {
    const parts = sortOption.split(':');
    return [parts[0] || 'created_at', parts[1] || 'desc'];
  }, [sortOption]);

  const { data, error, loading, reload } = useAsync(
    () => listAPIKeys({ page, pageSize, status, sortBy, sortOrder }),
    [page, pageSize, status, sortBy, sortOrder],
  );

  const keys = data?.api_keys || [];
  const total = data?.total ?? 0;
  const totalPages = data?.total_pages ?? 0;

  // Filter within page by keyword search
  const filteredKeys = useMemo(() => {
    const q = search.trim().toLowerCase();
    if (!q) return keys;
    return keys.filter((k) => {
      const hay = [k.name, k.key_prefix, k.user_alias, k.user_email, k.id]
        .filter(Boolean)
        .join(' ')
        .toLowerCase();
      return hay.includes(q);
    });
  }, [keys, search]);

  // Compute KPI metrics for summary cards
  const stats = useMemo(() => {
    const active = keys.filter((k) => k.status === 'active').length;
    const disabled = keys.filter((k) => k.status === 'disabled').length;
    const revokedOrExpired = keys.filter((k) => k.status === 'revoked' || k.status === 'expired').length;
    const unassigned = keys.filter((k) => !k.user_id).length;
    return { active, disabled, revokedOrExpired, unassigned };
  }, [keys]);

  // Status breakdown of the currently selected rows, for the bulk-action bar.
  const selectedBreakdown = useMemo(() => {
    const byStatus = new Map();
    filteredKeys
      .filter((k) => selectedIds.includes(k.id))
      .forEach((k) => byStatus.set(k.status, (byStatus.get(k.status) || 0) + 1));
    return [...byStatus.entries()].sort((a, b) => b[1] - a[1]);
  }, [filteredKeys, selectedIds]);

  function handlePageChange(newPage) {
    if (newPage < 1 || newPage > totalPages) return;
    setPage(newPage);
    setSelectedIds([]);
  }

  function handleStatusChange(value) {
    setStatus(value);
    setPage(1);
    setSelectedIds([]);
  }

  // Toggle key status directly from row (Quick action)
  async function handleToggleStatus(key, e) {
    e.stopPropagation();
    const nextStatus = key.status === 'active' ? 'disabled' : 'active';
    setActionLoading((prev) => ({ ...prev, [key.id]: true }));
    try {
      await patchAPIKey(key.id, { status: nextStatus });
      toast.success(`Key "${key.name}" ${nextStatus === 'active' ? 'enabled' : 'disabled'}`);
      reload();
    } catch (err) {
      toast.error(err.message || 'Failed to update key status');
    } finally {
      setActionLoading((prev) => ({ ...prev, [key.id]: false }));
    }
  }

  // Selection handlers
  function toggleSelectAll() {
    if (selectedIds.length === filteredKeys.length) {
      setSelectedIds([]);
    } else {
      setSelectedIds(filteredKeys.map((k) => k.id));
    }
  }

  function toggleSelectOne(id, e) {
    e.stopPropagation();
    setSelectedIds((prev) => (prev.includes(id) ? prev.filter((i) => i !== id) : [...prev, id]));
  }

  async function handleBulkStatusChange(newStatus) {
    if (selectedIds.length === 0) return;
    const count = selectedIds.length;
    try {
      await Promise.all(selectedIds.map((id) => patchAPIKey(id, { status: newStatus })));
      toast.success(`Updated ${count} keys to ${newStatus}`);
      setSelectedIds([]);
      reload();
    } catch (err) {
      toast.error(err.message || 'Failed bulk update');
    }
  }

  function goDetail(id, e) {
    if (e.target.closest('a, button, input')) return;
    navigate(`/api-keys/${encodeURIComponent(id)}`);
  }

  return (
    <>
      {/* Header */}
      <div className="main__header">
        <div>
          <h1 className="main__title">API Keys</h1>
          <div className="main__subtitle">
            Client API keys with per-key policy enforcement, usage caps, and owner attribution (LiteLLM workflow).
          </div>
        </div>
        <div className="row gap-sm">
          <button
            type="button"
            onClick={() => {
              reload();
              toast.info('Keys refreshed');
            }}
          >
            <RefreshIcon /> Refresh
          </button>
          <button type="button" className="primary" onClick={() => setShowCreate(true)}>
            + New Key
          </button>
          <button type="button" onClick={() => setShowImport(true)}>⇪ Import Keys</button>
        </div>
      </div>

      {/* Summary KPI Bar */}
      <div className="keys-kpi-grid">
        <div className="keys-kpi-card keys-kpi-card--active">
          <div>
            <div className="keys-kpi-card__val">{total}</div>
            <div className="keys-kpi-card__label">Total API Keys</div>
            <div className="keys-kpi-card__sub">{stats.active} active on this page</div>
          </div>
          <div className="keys-kpi-card__icon">
            <KeyIcon />
          </div>
        </div>

        <div className="keys-kpi-card keys-kpi-card--active">
          <div>
            <div className="keys-kpi-card__val" style={{ color: 'var(--success)' }}>
              {stats.active}
            </div>
            <div className="keys-kpi-card__label">Active Keys</div>
            <div className="keys-kpi-card__sub">Ready for proxy traffic</div>
          </div>
          <div className="keys-kpi-card__icon" style={{ color: 'var(--success)' }}>
            <CheckCircleIcon />
          </div>
        </div>

        <div className="keys-kpi-card keys-kpi-card--disabled">
          <div>
            <div className="keys-kpi-card__val" style={{ color: 'var(--warning)' }}>
              {stats.disabled}
            </div>
            <div className="keys-kpi-card__label">Disabled Keys</div>
            <div className="keys-kpi-card__sub">Paused by administrator</div>
          </div>
          <div className="keys-kpi-card__icon" style={{ color: 'var(--warning)' }}>
            <PauseCircleIcon />
          </div>
        </div>

        <div className="keys-kpi-card keys-kpi-card--unassigned">
          <div>
            <div className="keys-kpi-card__val" style={{ color: stats.unassigned > 0 ? '#a78bfa' : 'var(--text-muted)' }}>
              {stats.unassigned}
            </div>
            <div className="keys-kpi-card__label">Unassigned Owner</div>
            <div className="keys-kpi-card__sub">Keys without user link</div>
          </div>
          <div className="keys-kpi-card__icon">
            <UserAlertIcon />
          </div>
        </div>
      </div>

      {/* Filter & Toolbar */}
      <div className="card" style={{ marginBottom: 16 }}>
        <div className="catalog-toolbar" style={{ flexWrap: 'wrap', gap: 12 }}>
          {/* Search Input */}
          <div style={{ position: 'relative', flex: '1 1 240px' }}>
            <input
              className="search-input"
              type="text"
              value={search}
              onChange={(e) => setSearch(e.target.value)}
              placeholder="Search name, owner, prefix, key ID..."
              aria-label="Search API keys"
              style={{ width: '100%' }}
            />
            {search && (
              <button
                type="button"
                onClick={() => setSearch('')}
                style={{
                  position: 'absolute',
                  right: 8,
                  top: '50%',
                  transform: 'translateY(-50%)',
                  background: 'none',
                  border: 'none',
                  color: 'var(--text-dim)',
                  cursor: 'pointer',
                  padding: 2,
                }}
              >
                ✕
              </button>
            )}
          </div>

          {/* Status Tabs */}
          <div className="seg-group" role="tablist" aria-label="Status filter">
            {STATUS_OPTIONS.map((opt) => (
              <button
                key={opt.value}
                type="button"
                className={`seg-btn ${status === opt.value ? 'seg-btn--active' : ''}`}
                onClick={() => handleStatusChange(opt.value)}
              >
                {opt.label}
              </button>
            ))}
          </div>

          {/* Sort Selector */}
          <select
            className="sort-select"
            value={sortOption}
            onChange={(e) => setSortOption(e.target.value)}
            aria-label="Sort keys"
          >
            {SORT_OPTIONS.map((opt) => (
              <option key={opt.value} value={opt.value}>
                {opt.label}
              </option>
            ))}
          </select>

          {/* Page Size */}
          <select
            className="sort-select"
            value={pageSize}
            onChange={(e) => {
              setPageSize(Number(e.target.value));
              setPage(1);
            }}
            aria-label="Items per page"
          >
            <option value={10}>10 / page</option>
            <option value={25}>25 / page</option>
            <option value={50}>50 / page</option>
            <option value={100}>100 / page</option>
          </select>
        </div>

        {/* Bulk Action Bar */}
        {selectedIds.length > 0 && (
          <BulkActionBar
            total={selectedIds.length}
            breakdown={selectedBreakdown}
            onEnable={() => handleBulkStatusChange('active')}
            onDisable={() => handleBulkStatusChange('disabled')}
            onClear={() => setSelectedIds([])}
          />
        )}
      </div>

      <ErrorBanner error={error} onRetry={reload} />

      {/* Loading Skeleton */}
      {loading && (
        <div className="card" style={{ padding: 0 }}>
          <table className="table">
            <thead>
              <tr>
                <th style={{ width: 36 }} />
                <th>Name & ID</th>
                <th>Owner</th>
                <th>Prefix</th>
                <th>Status</th>
                <th>Expires</th>
                <th>Last used</th>
                <th>Actions</th>
              </tr>
            </thead>
            <tbody>
              <SkeletonRows columns={8} rows={pageSize > 10 ? 8 : pageSize} />
            </tbody>
          </table>
        </div>
      )}

      {/* Empty State */}
      {!loading && !error && filteredKeys.length === 0 && (
        <EmptyState
          title={search || status ? 'No matching API keys' : 'No API keys created yet'}
          hint={
            search || status
              ? 'Try adjusting your search query or status filter.'
              : 'Create your first API key to start proxying LLM requests with rate limits and budget policies.'
          }
        />
      )}

      {/* Data Table */}
      {!loading && !error && filteredKeys.length > 0 && (
        <div className="card" style={{ padding: 0 }}>
          <table className="table">
            <thead>
              <tr>
                <th style={{ width: 36 }}>
                  <input
                    type="checkbox"
                    checked={selectedIds.length > 0 && selectedIds.length === filteredKeys.length}
                    onChange={toggleSelectAll}
                    aria-label="Select all rows"
                    style={{ cursor: 'pointer' }}
                  />
                </th>
                <th>Name & ID</th>
                <th>Owner</th>
                <th>Prefix</th>
                <th>Status</th>
                <th>Expires</th>
                <th>Last used</th>
                <th aria-label="Actions" style={{ textAlign: 'right' }}>
                  Actions
                </th>
              </tr>
            </thead>
            <tbody>
              {filteredKeys.map((k) => {
                const isSelected = selectedIds.includes(k.id);
                const isToggling = actionLoading[k.id];
                return (
                  <tr
                    key={k.id}
                    className={`row-link${isSelected ? ' is-selected' : ''}`}
                    onClick={(e) => goDetail(k.id, e)}
                  >
                    <td>
                      <input
                        type="checkbox"
                        checked={isSelected}
                        onChange={(e) => toggleSelectOne(k.id, e)}
                        onClick={(e) => e.stopPropagation()}
                        aria-label={`Select ${k.name}`}
                        style={{ cursor: 'pointer' }}
                      />
                    </td>
                    <td>
                      <Link to={`/api-keys/${encodeURIComponent(k.id)}`} style={{ fontWeight: 600 }}>
                        {k.name}
                      </Link>
                      <div className="dim mono" style={{ fontSize: 11, marginTop: 2 }}>
                        {k.id}
                      </div>
                    </td>
                    <td>
                      {k.user_id ? (
                        <Link
                          to={`/internal-users/${encodeURIComponent(k.user_id)}`}
                          className="owner-pill"
                          title={k.user_id}
                          onClick={(e) => e.stopPropagation()}
                        >
                          <span className="owner-pill__avatar">
                            {(k.user_alias || k.user_email || 'U')[0].toUpperCase()}
                          </span>
                          <span>{k.user_alias || shortOwnerId(k.user_id)}</span>
                        </Link>
                      ) : (
                        <span className="dim" style={{ fontSize: 12 }}>
                          unassigned
                        </span>
                      )}
                    </td>
                    <td>
                      <span className="key-prefix-chip">
                        <code>{k.key_prefix}…</code>
                        <CopyButton value={`${k.key_prefix}…`} label="Copy" small />
                      </span>
                    </td>
                    <td>
                      <StatusBadge status={k.status} />
                    </td>
                    <td>
                      <span style={{ fontSize: 12 }}>{formatExpires(k.expires_at)}</span>
                    </td>
                    <td>
                      <span style={{ fontSize: 12, color: k.last_used_at ? 'var(--text)' : 'var(--text-dim)' }}>
                        {timeAgo(k.last_used_at)}
                      </span>
                    </td>
                    <td style={{ textAlign: 'right' }}>
                      <div className="row-actions" style={{ justifyContent: 'flex-end', gap: 6 }}>
                        <button
                          type="button"
                          className={k.status === 'active' ? '' : 'primary'}
                          style={{ fontSize: 11, padding: '3px 8px' }}
                          onClick={(e) => handleToggleStatus(k, e)}
                          disabled={isToggling}
                          title={k.status === 'active' ? 'Disable this key' : 'Enable this key'}
                        >
                          {isToggling ? '...' : k.status === 'active' ? 'Disable' : 'Enable'}
                        </button>
                        <CopyButton value={k.id} label="ID" small className="row-actions__btn--primary" />
                      </div>
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
          <div style={{ padding: '12px 16px' }}>
            <Pager
              page={page}
              totalPages={totalPages}
              total={total}
              pageSize={pageSize}
              onPageChange={handlePageChange}
            />
          </div>
        </div>
      )}

      {/* Create Key Modal */}
      {showCreate && (
        <CreateKeyModal
          onClose={() => setShowCreate(false)}
          onCreated={() => {
            setShowCreate(false);
            setPage(1);
            reload();
          }}
        />
      )}

      {/* Import Keys Modal */}
      {showImport && (
        <ImportKeysModal
          onClose={() => setShowImport(false)}
          onImported={() => {
            setShowImport(false);
            setPage(1);
            reload();
          }}
        />
      )}
    </>
  );
}

function SkeletonRows({ columns, rows }) {
  return (
    <>
      {Array.from({ length: rows }).map((_, i) => (
        <tr key={i} className="skeleton-row">
          {Array.from({ length: columns }).map((__, j) => (
            <td key={j}>
              <span className="skeleton-line" />
            </td>
          ))}
        </tr>
      ))}
    </>
  );
}

// BulkActionBar — full-width accent strip shown when rows are selected,
// matching the shared .bulk-action-bar pattern (count + status breakdown
// chips + actions) used on the Upstream Providers page.
function BulkActionBar({ total, breakdown, onEnable, onDisable, onClear }) {
  return (
    <div className="bulk-action-bar" role="region" aria-label="Bulk actions">
      <div className="bulk-action-bar__count">
        <strong>{total}</strong> key(s) selected
      </div>
      <div className="bulk-action-bar__breakdown">
        {breakdown.map(([status, n]) => (
          <span key={status} className="filter-chip" title={`${n} ${status}`}>
            {status}<span className="dim">×{n}</span>
          </span>
        ))}
      </div>
      <div className="bulk-action-bar__actions">
        <button onClick={onEnable} title="Enable selected">✓ Enable</button>
        <button onClick={onDisable} title="Disable selected">⊘ Disable</button>
        <button className="ghost" onClick={onClear} title="Clear selection">Clear</button>
      </div>
    </div>
  );
}

function CreateKeyModal({ onClose, onCreated }) {
  const toast = useToast();
  const [name, setName] = useState('');
  const [search, setSearch] = useState('');
  const [userId, setUserId] = useState('');
  const [expiresAt, setExpiresAt] = useState('');
  const [customSecretOn, setCustomSecretOn] = useState(false);
  const [customSecret, setCustomSecret] = useState('');
  const [attachPolicy, setAttachPolicy] = useState(false);
  const [policyForm, setPolicyForm] = useState(null);
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState('');
  const [created, setCreated] = useState(null);
  const [secretCopied, setSecretCopied] = useState(false);

  const usersReq = useAsync(
    () => listInternalUsers({ page: 1, pageSize: 200, sortBy: 'user_alias', sortOrder: 'asc' }),
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
      setError('Please select an Internal User owner. Every API key must belong to a user.');
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
        policy = formToPolicy(policyForm, '');
        policy.api_key_id = '';
      }
      const result = await createAPIKey({
        name,
        secret: secret || undefined,
        user_id: userId,
        expires_at: expiresAt ? new Date(expiresAt).toISOString() : null,
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
      <Modal title="API Key Created Successfully" onClose={onClose}>
        <div
          style={{
            padding: 16,
            background: 'var(--accent-dim)',
            border: '1px solid var(--accent)',
            borderRadius: 'var(--radius)',
            marginBottom: 16,
          }}
        >
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
            Store this key securely now. NixLLM persists only its SHA-256 hash and cannot retrieve it again later.
          </div>
        </div>

        <div className="row gap-sm" style={{ marginTop: 8, fontSize: 12 }}>
          <span className="dim mono">id: {created.id}</span>
          {created.user_id && (
            <span className="dim mono">· owner: {shortOwnerId(created.user_id)}</span>
          )}
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
    <Modal title="Issue New API Key" onClose={onClose}>
      {error && <div className="error-banner">{error}</div>}
      <form onSubmit={handleSubmit}>
        <div className="form__row">
          <label className="form__label" htmlFor="name">
            Key Name / Application
          </label>
          <input
            id="name"
            type="text"
            value={name}
            required
            onChange={(e) => setName(e.target.value)}
            placeholder="e.g. production-rag-service"
            disabled={submitting}
          />
        </div>

        <div className="form__row">
          <label className="form__label" htmlFor="owner">
            Internal User Owner <span style={{ color: 'var(--danger)' }}>*</span>
          </label>
          {usersReq.loading ? (
            <Spinner label="Loading users…" />
          ) : usersReq.error ? (
            <div className="error-banner">
              {usersReq.error.message || 'Failed to load internal users.'}
            </div>
          ) : users.length === 0 ? (
            <div className="dim">
              No Internal Users yet.{' '}
              <Link to="/internal-users">Create an Internal User first</Link> to assign key ownership.
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
                id="owner"
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
          <div className="form__hint">
            Every key must be linked to an Internal User. When per-key caps are unset, the owner's budget & RPM limits apply automatically.
          </div>
        </div>

        <div className="form__row">
          <label className="form__label" htmlFor="expires">
            Expiration Date (Optional)
          </label>
          <input
            id="expires"
            type="datetime-local"
            value={expiresAt}
            onChange={(e) => setExpiresAt(e.target.value)}
            disabled={submitting}
          />
          <div className="form__hint">Leave empty for an unexpiring key.</div>
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
              Configure per-key Policy (RPM, Budget, Model access rules)
            </span>
          </label>
        </div>

        {attachPolicy && (
          <div
            style={{
              marginTop: 12,
              padding: 16,
              background: 'var(--bg)',
              borderRadius: 'var(--radius-sm)',
              border: '1px solid var(--border)',
            }}
          >
            <PolicyForm initial={null} onChange={setPolicyForm} />
          </div>
        )}

        <div className="form__actions">
          <button type="button" onClick={onClose} disabled={submitting}>
            Cancel
          </button>
          <button type="submit" className="primary" disabled={submitting || !name || !userId}>
            {submitting ? 'Issuing Key…' : 'Issue API Key'}
          </button>
        </div>
      </form>
    </Modal>
  );
}

const IMPORT_EXAMPLE = `[
  { "alias": "key-alpha", "key": "sk-my-custom-alpha-0123456789" },
  { "alias": "key-beta",  "key": "another-custom-key-0123456789" }
]`;

function ImportKeysModal({ onClose, onImported }) {
  const toast = useToast();
  const [raw, setRaw] = useState('');
  const [error, setError] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const [result, setResult] = useState(null);

  async function handleImport() {
    setError('');
    let rows;
    try {
      rows = JSON.parse(raw);
    } catch {
      setError('Invalid JSON. Check the format and try again.');
      return;
    }
    if (!Array.isArray(rows) || rows.length === 0) {
      setError('Provide a JSON array with at least one { alias, key } entry.');
      return;
    }
    const clean = rows.map((r) => ({
      alias: String(r.alias ?? '').trim(),
      key: String(r.key ?? '').trim(),
    }));
    if (clean.some((r) => !r.alias || !r.key)) {
      setError('Every entry needs a non-empty "alias" and "key".');
      return;
    }
    setSubmitting(true);
    try {
      const res = await importAPIKeys(clean);
      setResult(res);
      toast.success(`Imported ${res.imported} of ${res.total} keys`);
    } catch (err) {
      setError(err.message || 'Failed to import keys.');
      toast.error(err.message || 'Failed to import keys');
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <Modal title="Import API Keys by Alias" onClose={onClose}>
      {error && <div className="error-banner">{error}</div>}
      {!result && (
        <>
          <div className="form__hint" style={{ marginBottom: 8 }}>
            Paste a JSON array of <code>{"{ alias, key }"}</code> pairs. Each{' '}
            <code>alias</code> must match an existing key&apos;s alias
            (case-insensitive). The <code>key</code> is applied as that
            key&apos;s new secret.
          </div>
          <textarea
            value={raw}
            onChange={(e) => setRaw(e.target.value)}
            rows={10}
            spellCheck={false}
            placeholder={IMPORT_EXAMPLE}
            style={{ width: '100%', fontFamily: 'var(--mono)', fontSize: 12 }}
          />
          <div className="row gap-sm" style={{ marginTop: 8 }}>
            <button type="button" onClick={() => setRaw(IMPORT_EXAMPLE)}>
              Load example
            </button>
          </div>
          <div className="form__actions" style={{ marginTop: 12 }}>
            <button type="button" onClick={onClose} disabled={submitting}>
              Cancel
            </button>
            <button type="button" className="primary" onClick={handleImport} disabled={submitting}>
              {submitting ? 'Importing…' : 'Import Keys'}
            </button>
          </div>
        </>
      )}
      {result && (
        <>
          <div
            style={{
              padding: 16,
              background: 'var(--accent-dim)',
              border: '1px solid var(--accent)',
              borderRadius: 'var(--radius)',
              marginBottom: 12,
            }}
          >
            <strong>{result.imported}</strong> of <strong>{result.total}</strong> keys imported.
          </div>
          {result.skipped?.length > 0 && (
            <div className="card" style={{ padding: 0, marginBottom: 12 }}>
              <table className="table" style={{ fontSize: 12 }}>
                <thead>
                  <tr><th>Alias</th><th>Reason</th></tr>
                </thead>
                <tbody>
                  {result.skipped.map((s, i) => (
                    <tr key={i}>
                      <td><code>{s.alias}</code></td>
                      <td>{s.reason}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
          <div className="form__actions">
            <button type="button" className="primary" onClick={onImported}>
              Done
            </button>
          </div>
        </>
      )}
    </Modal>
  );
}

// Icons
function KeyIcon() {
  return (
    <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5">
      <circle cx="5" cy="11" r="2.5" />
      <path d="M7 9l6-6M10 6l2 2" strokeLinecap="round" />
    </svg>
  );
}

function CheckCircleIcon() {
  return (
    <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round">
      <path d="M13.5 8A5.5 5.5 0 1 1 8 2.5a5.5 5.5 0 0 1 3.9 1.6" />
      <path d="M5.5 8l2 2 5.5-5.5" />
    </svg>
  );
}

function PauseCircleIcon() {
  return (
    <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round">
      <circle cx="8" cy="8" r="5.5" />
      <path d="M6 6v4M10 6v4" />
    </svg>
  );
}

function UserAlertIcon() {
  return (
    <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round">
      <circle cx="6" cy="5.5" r="2" />
      <path d="M2.5 13.5c0-2 1.5-3.5 3.5-3.5s3.5 1.5 3.5 3.5" />
      <path d="M12 5v3M12 10.5v0.5" />
    </svg>
  );
}

function RefreshIcon() {
  return (
    <svg viewBox="0 0 16 16" width="14" height="14" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round">
      <path d="M13 6A5 5 0 0 0 3.5 5" />
      <path d="M3 2.5V5h2.5" />
      <path d="M3 10A5 5 0 0 0 12.5 11" />
      <path d="M13 13.5V11h-2.5" />
    </svg>
  );
}
