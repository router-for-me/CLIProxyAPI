import React, { useMemo, useState } from 'react';
import { Link, useNavigate } from 'react-router-dom';
import { listAPIKeys, createAPIKey, listInternalUsers } from '../api/client.js';
import { useAsync } from '../hooks/useAsync.js';
import { Spinner, ErrorBanner, EmptyState, StatusBadge, Modal } from '../components/Primitives.jsx';
import Pager from '../components/Pager.jsx';
import PolicyForm, { formToPolicy } from '../components/PolicyForm.jsx';
import CopyButton from '../components/CopyButton.jsx';
import { useToast } from '../components/Toast.jsx';

const DEFAULT_PAGE_SIZE = 25;
const STATUS_OPTIONS = [
  { value: '', label: 'All' },
  { value: 'active', label: 'Active' },
  { value: 'disabled', label: 'Disabled' },
  { value: 'revoked', label: 'Revoked' },
  { value: 'expired', label: 'Expired' },
];

// shortOwnerId truncates the UUID-style internal user id to a readable hint.
// The full id is visible on hover via the Link title attribute.
function shortOwnerId(id) {
  if (!id) return '';
  return id.length > 12 ? `${id.slice(0, 8)}…` : id;
}

export default function ApiKeysPage() {
  const toast = useToast();
  const navigate = useNavigate();
  const [page, setPage] = useState(1);
  const [status, setStatus] = useState('');
  const [search, setSearch] = useState('');
  const { data, error, loading, reload } = useAsync(
    () => listAPIKeys({ page, pageSize: DEFAULT_PAGE_SIZE, status }),
    [page, status],
  );
  const [showCreate, setShowCreate] = useState(false);

  const keys = data?.api_keys || [];
  const total = data?.total ?? 0;
  const totalPages = data?.total_pages ?? 0;

  // Client-side search within the loaded page. The keys list endpoint does
  // not support server-side q= filtering, so we narrow the current page's
  // rows by name / alias / prefix — matches how most admin UIs behave for
  // paginated small tables.
  const filteredKeys = useMemo(() => {
    const q = search.trim().toLowerCase();
    if (!q) return keys;
    return keys.filter((k) => {
      const hay = [k.name, k.key_prefix, k.user_alias, k.user_email, k.id]
        .filter(Boolean).join(' ').toLowerCase();
      return hay.includes(q);
    });
  }, [keys, search]);

  function handlePageChange(newPage) {
    if (newPage < 1 || newPage > totalPages) return;
    setPage(newPage);
  }
  function handleStatusChange(value) {
    setStatus(value);
    setPage(1);
  }

  function goDetail(id, e) {
    // Only navigate when the click didn't originate from a nested link or
    // actionable element (links/buttons stopPropagation themselves, but this
    // is the safety net).
    if (e.target.closest('a, button')) return;
    navigate(`/api-keys/${encodeURIComponent(id)}`);
  }

  const countLabel = search
    ? `${filteredKeys.length} of ${keys.length} on page`
    : `${total} total`;

  return (
    <>
      <div className="main__header">
        <div>
          <h1 className="main__title">API Keys</h1>
          <div className="main__subtitle">
            PG-managed client keys, each owned by an Internal User (LiteLLM
            workflow). Per-key policy is optional — unset caps fall back to
            the owner's max_budget, RPM, and model allow-list.
          </div>
        </div>
        <div className="row gap-sm">
          <button onClick={() => { reload(); toast.info('Keys refreshed'); }}>Refresh</button>
          <button className="primary" onClick={() => setShowCreate(true)}>+ New Key</button>
        </div>
      </div>

      <div className="card">
        <div className="catalog-toolbar">
          <input
            className="search-input"
            type="text"
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            placeholder="Search name, owner, prefix…"
            aria-label="Search API keys"
          />
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
          <div className="catalog-toolbar__spacer" />
          <span className="catalog-toolbar__count">{countLabel}</span>
        </div>
      </div>

      <ErrorBanner error={error} onRetry={reload} />

      {loading && (
        <div className="card" style={{ padding: 0 }}>
          <table className="table">
            <thead>
              <tr>
                <th>Name</th><th>Owner</th><th>Prefix</th><th>Status</th>
                <th>Expires</th><th>Last used</th><th>Created</th><th aria-label="Actions" />
              </tr>
            </thead>
            <tbody>
              {/* SkeletonRows renders shimmer placeholder rows matching
                  the table column count while the page is fetching. */}
              <SkeletonRows columns={8} rows={6} />
            </tbody>
          </table>
        </div>
      )}

      {!loading && !error && filteredKeys.length === 0 && (
        <EmptyState
          title={search || status ? 'No matching API keys' : 'No PG-managed API keys yet'}
          hint={search || status
            ? 'Try a different search term or status filter.'
            : 'Create your first key to enable per-key policy enforcement. Requires PGSTORE_DSN configured on the server.'}
        />
      )}

      {!loading && !error && filteredKeys.length > 0 && (
        <div className="card" style={{ padding: 0 }}>
          <table className="table">
            <thead>
              <tr>
                <th>Name</th>
                <th>Owner</th>
                <th>Prefix</th>
                <th>Status</th>
                <th>Expires</th>
                <th>Last used</th>
                <th>Created</th>
                <th aria-label="Actions" />
              </tr>
            </thead>
            <tbody>
              {filteredKeys.map((k) => (
                <tr
                  key={k.id}
                  className="row-link"
                  onClick={(e) => goDetail(k.id, e)}
                >
                  <td>
                    <Link to={`/api-keys/${encodeURIComponent(k.id)}`}>{k.name}</Link>
                    <div className="dim mono" style={{ marginTop: 2 }}>{k.id}</div>
                  </td>
                  <td>
                    {k.user_id ? (
                      <Link
                        to={`/internal-users/${encodeURIComponent(k.user_id)}`}
                        className="mono"
                        title={k.user_id}
                        onClick={(e) => e.stopPropagation()}
                      >
                        {k.user_alias
                          ? `${k.user_alias}${k.user_email ? ` <${k.user_email}>` : ''}`
                          : shortOwnerId(k.user_id)}
                      </Link>
                    ) : (
                      <span className="dim">unassigned</span>
                    )}
                  </td>
                  <td>
                    <span className="row" style={{ gap: 6, alignItems: 'center' }}>
                      <code className="mono">{k.key_prefix}…</code>
                      <CopyButton value={`${k.key_prefix}…`} label="Copy" small />
                    </span>
                  </td>
                  <td><StatusBadge status={k.status} /></td>
                  <td>{k.expires_at ? new Date(k.expires_at).toLocaleString() : 'never'}</td>
                  <td>{k.last_used_at ? new Date(k.last_used_at).toLocaleString() : '—'}</td>
                  <td className="dim">{new Date(k.created_at).toLocaleString()}</td>
                  <td>
                    <div className="row-actions">
                      <CopyButton
                        value={k.id}
                        label="ID"
                        small
                        className="row-actions__btn--primary"
                      />
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
          <div style={{ padding: '0 16px 16px' }}>
            <Pager
              page={page}
              totalPages={totalPages}
              total={total}
              pageSize={DEFAULT_PAGE_SIZE}
              onPageChange={handlePageChange}
            />
          </div>
        </div>
      )}

      {showCreate && (
        <CreateKeyModal
          onClose={() => setShowCreate(false)}
          onCreated={() => { setShowCreate(false); setPage(1); reload(); }}
        />
      )}
    </>
  );
}

// SkeletonRows renders CatalogSkeleton-style shimmer rows. Inlined here so
// the list page doesn't import the catalog-specific name; reuses the same CSS.
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

function CreateKeyModal({ onClose, onCreated }) {
  const toast = useToast();
  const [name, setName] = useState('');
  const [search, setSearch] = useState('');
  const [userId, setUserId] = useState('');
  const [expiresAt, setExpiresAt] = useState('');
  const [attachPolicy, setAttachPolicy] = useState(false);
  const [policyForm, setPolicyForm] = useState(null);
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState('');
  const [created, setCreated] = useState(null);
  const [secretCopied, setSecretCopied] = useState(false);

  // Fetch the internal users list once for the owner picker. The LiteLLM
  // workflow requires every API key to be owned by a user (budget/RPM
  // enforcement + spend attribution rely on the user_id link).
  const usersReq = useAsync(
    () => listInternalUsers({ page: 1, pageSize: 200, sortBy: 'user_alias', sortOrder: 'asc' }),
    [],
  );
  const users = usersReq.data?.users || [];
  // Debounced-ish owner search (client side over the 200-row page). This is
  // a plain filter — no extra network round-trip, just narrows the dropdown.
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
      setError('Please select an Internal User owner. The LiteLLM workflow requires every key to belong to a user.');
      return;
    }
    setSubmitting(true);
    setError('');
    try {
      // Only attach a policy when the user opted-in AND filled at least one
      // field. An empty-but-enabled policy is also valid (it just means
      // "unlimited but tracked"); the check below respects that by sending
      // null-policy when the toggle is off. When the per-key policy leaves
      // a cap unset, enforcement falls back to the owner's max_budget.
      let policy = null;
      if (attachPolicy && policyForm) {
        policy = formToPolicy(policyForm, '');
        // Drop the api_key_id placeholder — server assigns it.
        policy.api_key_id = '';
      }
      const result = await createAPIKey({
        name,
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
      <Modal title="Key Created" onClose={onClose}>
        <div className="form__row">
          <label className="form__label">Plaintext secret (shown once)</label>
          <div className="row" style={{ gap: 8, alignItems: 'flex-start' }}>
            <div className="copyable" style={{ flex: 1 }}>{created.secret}</div>
            <CopyButton value={created.secret} label="Copy" small />
          </div>
          <div className="form__hint">
            Store this securely. The dashboard cannot retrieve it later — only its SHA-256 hash is persisted.
          </div>
        </div>
        <div className="row gap-sm" style={{ marginTop: 8 }}>
          <span className="dim mono">id: {created.id}</span>
          {created.user_id && (
            <Link to={`/internal-users/${encodeURIComponent(created.user_id)}`} className="dim mono">
              · owner: {shortOwnerId(created.user_id)}
            </Link>
          )}
        </div>
        <label className="row gap-sm" style={{ cursor: 'pointer', marginTop: 16 }}>
          <input
            type="checkbox"
            checked={secretCopied}
            onChange={(e) => setSecretCopied(e.target.checked)}
            style={{ width: 'auto' }}
          />
          <span className="form__label" style={{ margin: 0 }}>
            I've stored the secret securely
          </span>
        </label>
        <div className="form__actions">
          <button className="primary" onClick={onCreated} disabled={!secretCopied}>Done</button>
        </div>
      </Modal>
    );
  }

  return (
    <Modal title="New API Key" onClose={onClose}>
      {error && <div className="error-banner">{error}</div>}
      <form onSubmit={handleSubmit}>
        <div className="form__row">
          <label className="form__label" htmlFor="name">Name</label>
          <input
            id="name" type="text" value={name} required
            onChange={(e) => setName(e.target.value)}
            placeholder="e.g. production-app"
            disabled={submitting}
          />
        </div>
        <div className="form__row">
          <label className="form__label" htmlFor="owner">Internal User owner</label>
          {usersReq.loading ? (
            <Spinner label="Loading users…" />
          ) : usersReq.error ? (
            <div className="error-banner">
              {usersReq.error.message || 'Failed to load internal users. Create at least one before issuing keys.'}
            </div>
          ) : users.length === 0 ? (
            <div className="dim">
              No Internal Users yet.{' '}
              <Link to="/internal-users">Create one first</Link>, then return
              here — every key requires an owner.
            </div>
          ) : (
            <>
              <input
                type="text"
                value={search}
                onChange={(e) => setSearch(e.target.value)}
                placeholder="Search owner…"
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
                size={Math.min(6, Math.max(3, filteredUsers.length))}
              >
                <option value="">— select owner —</option>
                {filteredUsers.map((u) => (
                  <option key={u.id} value={u.id}>
                    {u.user_alias || u.id}{u.user_email ? ` <${u.user_email}>` : ''}
                    {u.max_budget ? ` · $${Number(u.max_budget).toFixed(2)} cap` : ''}
                    {u.user_role && u.user_role !== 'internal_user' ? ` · ${u.user_role}` : ''}
                  </option>
                ))}
              </select>
            </>
          )}
          <div className="form__hint">
            Each key must belong to an Internal User. When the per-key policy
            leaves a budget cap unset, enforcement falls back to this user's
            max_budget.
          </div>
        </div>
        <div className="form__row">
          <label className="form__label" htmlFor="expires">Expires at (optional)</label>
          <input
            id="expires" type="datetime-local" value={expiresAt}
            onChange={(e) => setExpiresAt(e.target.value)}
            disabled={submitting}
          />
          <div className="form__hint">Leave empty for a key that never expires.</div>
        </div>

        <div className="form__row" style={{ marginTop: 8 }}>
          <label className="row gap-sm" style={{ cursor: 'pointer' }}>
            <input
              type="checkbox"
              checked={attachPolicy}
              onChange={(e) => setAttachPolicy(e.target.checked)}
              style={{ width: 'auto' }}
            />
            <span className="form__label" style={{ margin: 0 }}>
              Attach a per-key policy now (RPM, budgets, model allow/deny)
            </span>
          </label>
          <div className="form__hint">
            Optional. When a cap is left unset, the owner's max_budget /
            rpm_limit / model allow-list applies as the fallback. Editable
            later from the key's detail page.
          </div>
        </div>

        {attachPolicy && (
          <div style={{
            marginTop: 12, padding: 16,
            background: 'var(--bg)', borderRadius: 'var(--radius-sm)',
            border: '1px solid var(--border)',
          }}>
            <PolicyForm initial={null} onChange={setPolicyForm} />
          </div>
        )}

        <div className="form__actions">
          <button type="button" onClick={onClose} disabled={submitting}>Cancel</button>
          <button type="submit" className="primary" disabled={submitting || !name || !userId}>
            {submitting ? 'Creating…' : 'Create Key'}
          </button>
        </div>
      </form>
    </Modal>
  );
}
