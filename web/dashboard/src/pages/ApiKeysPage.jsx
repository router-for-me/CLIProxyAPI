import React, { useState } from 'react';
import { Link } from 'react-router-dom';
import { listAPIKeys, createAPIKey, listInternalUsers } from '../api/client.js';
import { useAsync } from '../hooks/useAsync.js';
import { Spinner, ErrorBanner, EmptyState, StatusBadge, Modal } from '../components/Primitives.jsx';
import Pager from '../components/Pager.jsx';
import PolicyForm, { formToPolicy } from '../components/PolicyForm.jsx';

const DEFAULT_PAGE_SIZE = 25;

// shortOwnerId truncates the UUID-style internal user id to a readable hint.
// The full id is visible on hover via the Link title attribute.
function shortOwnerId(id) {
  if (!id) return '';
  return id.length > 12 ? `${id.slice(0, 8)}…` : id;
}

export default function ApiKeysPage() {
  const [page, setPage] = useState(1);
  const [status, setStatus] = useState('');
  const { data, error, loading, reload } = useAsync(
    () => listAPIKeys({ page, pageSize: DEFAULT_PAGE_SIZE, status }),
    [page, status],
  );
  const [showCreate, setShowCreate] = useState(false);

  const keys = data?.api_keys || [];
  const total = data?.total ?? 0;
  const totalPages = data?.total_pages ?? 0;

  function handlePageChange(newPage) {
    if (newPage < 1 || newPage > totalPages) return;
    setPage(newPage);
  }
  function handleStatusChange(e) {
    setStatus(e.target.value);
    setPage(1);
  }

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
          <button onClick={reload}>Refresh</button>
          <button className="primary" onClick={() => setShowCreate(true)}>+ New Key</button>
        </div>
      </div>

      <div className="card">
        <div className="row gap-sm">
          <label className="form__label" style={{ marginTop: 6 }}>Status filter</label>
          <select value={status} onChange={handleStatusChange} style={{ width: 180 }}>
            <option value="">All</option>
            <option value="active">active</option>
            <option value="disabled">disabled</option>
            <option value="revoked">revoked</option>
            <option value="expired">expired</option>
          </select>
        </div>
      </div>

      {loading && <Spinner label="Loading keys…" />}
      <ErrorBanner error={error} onRetry={reload} />

      {!loading && !error && keys.length === 0 && (
        <EmptyState
          title="No PG-managed API keys yet"
          hint="Create your first key to enable per-key policy enforcement. Requires PGSTORE_DSN configured on the server."
        />
      )}

      {!loading && !error && keys.length > 0 && (
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
              </tr>
            </thead>
            <tbody>
              {keys.map((k) => (
                <tr key={k.id}>
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
                      >
                        {k.user_alias
                          ? `${k.user_alias}${k.user_email ? ` <${k.user_email}>` : ''}`
                          : shortOwnerId(k.user_id)}
                      </Link>
                    ) : (
                      <span className="dim">unassigned</span>
                    )}
                  </td>
                  <td><code className="mono">{k.key_prefix}…</code></td>
                  <td><StatusBadge status={k.status} /></td>
                  <td>{k.expires_at ? new Date(k.expires_at).toLocaleString() : 'never'}</td>
                  <td>{k.last_used_at ? new Date(k.last_used_at).toLocaleString() : '—'}</td>
                  <td className="dim">{new Date(k.created_at).toLocaleString()}</td>
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

function CreateKeyModal({ onClose, onCreated }) {
  const [name, setName] = useState('');
  const [userId, setUserId] = useState('');
  const [expiresAt, setExpiresAt] = useState('');
  const [attachPolicy, setAttachPolicy] = useState(false);
  const [policyForm, setPolicyForm] = useState(null);
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState('');
  const [created, setCreated] = useState(null);

  // Fetch the internal users list once for the owner picker. The LiteLLM
  // workflow requires every API key to be owned by a user (budget/RPM
  // enforcement + spend attribution rely on the user_id link).
  const usersReq = useAsync(
    () => listInternalUsers({ page: 1, pageSize: 200, sortBy: 'user_alias', sortOrder: 'asc' }),
    [],
  );
  const users = usersReq.data?.users || [];

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
    } catch (err) {
      setError(err.message || 'Failed to create key.');
    } finally {
      setSubmitting(false);
    }
  }

  if (created) {
    return (
      <Modal title="Key Created" onClose={onClose}>
        <div className="form__row">
          <label className="form__label">Plaintext secret (shown once)</label>
          <div className="copyable">{created.secret}</div>
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
        <div className="form__actions">
          <button className="primary" onClick={onCreated}>Done</button>
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
            <select
              id="owner"
              value={userId}
              onChange={(e) => setUserId(e.target.value)}
              required
              disabled={submitting}
              style={{ width: '100%' }}
            >
              <option value="">— select owner —</option>
              {users.map((u) => (
                <option key={u.id} value={u.id}>
                  {u.user_alias || u.id}{u.user_email ? ` <${u.user_email}>` : ''}
                  {u.max_budget ? ` · $${Number(u.max_budget).toFixed(2)} cap` : ''}
                  {u.user_role && u.user_role !== 'internal_user' ? ` · ${u.user_role}` : ''}
                </option>
              ))}
            </select>
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
