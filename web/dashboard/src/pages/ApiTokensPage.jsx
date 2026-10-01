import React, { useState } from 'react';
import { Link, useNavigate } from 'react-router-dom';
import {
  listAPITokens, createAPIToken, regenerateAPIToken, deleteAPIToken,
} from '../api/client.js';
import { useAsync } from '../hooks/useAsync.js';
import { Spinner, ErrorBanner, EmptyState, StatusBadge, Modal } from '../components/Primitives.jsx';
import Pager from '../components/Pager.jsx';
import CopyButton from '../components/CopyButton.jsx';
import { useToast } from '../components/Toast.jsx';

const DEFAULT_PAGE_SIZE = 25;
const STATUS_OPTIONS = [
  { value: '', label: 'All' },
  { value: 'active', label: 'Active' },
  { value: 'revoked', label: 'Revoked' },
  { value: 'expired', label: 'Expired' },
];
const SCOPE_OPTIONS = [
  { value: '', label: 'All' },
  { value: 'read', label: 'Read' },
  { value: 'write', label: 'Write' },
];

export default function ApiTokensPage() {
  const toast = useToast();
  const navigate = useNavigate();
  const [page, setPage] = useState(1);
  const [status, setStatus] = useState('');
  const [scope, setScope] = useState('');
  const [search, setSearch] = useState('');
  const { data, error, loading, reload } = useAsync(
    () => listAPITokens({ page, pageSize: DEFAULT_PAGE_SIZE, status, scope, search }),
    [page, status, scope, search],
  );
  const [showCreate, setShowCreate] = useState(false);

  const tokens = data?.api_tokens || [];
  const total = data?.total ?? 0;
  const totalPages = data?.total_pages ?? 0;

  function handlePageChange(newPage) {
    if (newPage < 1 || newPage > totalPages) return;
    setPage(newPage);
  }
  function handleStatusChange(value) {
    setStatus(value);
    setPage(1);
  }
  function handleScopeChange(value) {
    setScope(value);
    setPage(1);
  }

  function goDetail(id, e) {
    if (e.target.closest('a, button')) return;
    navigate(`/api-tokens/${encodeURIComponent(id)}`);
  }

  return (
    <>
      <div className="main__header">
        <div>
          <h1 className="main__title">API Management</h1>
          <div className="main__subtitle">
            Management API tokens gate access to the <code>/v0/management</code>{' '}
            REST surface. Each token carries a scope (read/write), an optional
            per-endpoint allowlist, rate limits, expiry, and a full audit log of
            every call it makes.
          </div>
        </div>
        <div className="row gap-sm">
          <button onClick={() => { reload(); toast.info('Tokens refreshed'); }}>Refresh</button>
          <button className="primary" onClick={() => setShowCreate(true)}>+ New Token</button>
        </div>
      </div>

      <div className="card">
        <div className="catalog-toolbar">
          <input
            className="search-input"
            type="text"
            value={search}
            onChange={(e) => { setSearch(e.target.value); setPage(1); }}
            placeholder="Search by name…"
            aria-label="Search management tokens"
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
          <div className="seg-group" role="tablist" aria-label="Scope filter" style={{ marginLeft: 8 }}>
            {SCOPE_OPTIONS.map((opt) => (
              <button
                key={opt.value}
                type="button"
                className={`seg-btn ${scope === opt.value ? 'seg-btn--active' : ''}`}
                onClick={() => handleScopeChange(opt.value)}
              >
                {opt.label}
              </button>
            ))}
          </div>
          <div className="catalog-toolbar__spacer" />
          <span className="catalog-toolbar__count">{total} total</span>
        </div>
      </div>

      <ErrorBanner error={error} onRetry={reload} />

      {loading && (
        <div className="card" style={{ padding: 0 }}>
          <table className="table">
            <thead><tr><th>Name</th><th>Prefix</th><th>Status</th><th>Scope</th><th>Expires</th><th>Last used</th><th>Created</th><th aria-label="Actions" /></tr></thead>
            <tbody><SkeletonRows columns={8} rows={6} /></tbody>
          </table>
        </div>
      )}

      {!loading && !error && tokens.length === 0 && (
        <EmptyState
          title={search || status || scope ? 'No matching tokens' : 'No management API tokens yet'}
          hint={search || status || scope
            ? 'Try a different search term or filter.'
            : 'Create a token to enable scoped, policy-gated, audited access to the management REST API. Requires PGSTORE_DSN configured on the server.'}
        />
      )}

      {!loading && !error && tokens.length > 0 && (
        <div className="card" style={{ padding: 0 }}>
          <table className="table">
            <thead>
              <tr>
                <th>Name</th><th>Prefix</th><th>Status</th><th>Scope</th>
                <th>Expires</th><th>Last used</th><th>Created</th><th aria-label="Actions" />
              </tr>
            </thead>
            <tbody>
              {tokens.map((t) => (
                <tr key={t.id} className="row-link" onClick={(e) => goDetail(t.id, e)}>
                  <td>
                    <Link to={`/api-tokens/${encodeURIComponent(t.id)}`}>{t.name}</Link>
                    <div className="dim mono" style={{ marginTop: 2 }}>{t.id}</div>
                  </td>
                  <td>
                    <span className="row" style={{ gap: 6, alignItems: 'center' }}>
                      <code className="mono">{t.key_prefix}…</code>
                      <CopyButton value={`${t.key_prefix}…`} label="Copy" small />
                    </span>
                  </td>
                  <td><StatusBadge status={t.status} /></td>
                  <td>
                    <span className={`badge badge--${t.scope === 'write' ? 'active' : 'muted'}`}>{t.scope}</span>
                  </td>
                  <td>{t.expires_at ? new Date(t.expires_at).toLocaleString() : 'never'}</td>
                  <td>{t.last_used_at ? new Date(t.last_used_at).toLocaleString() : '—'}</td>
                  <td className="dim">{new Date(t.created_at).toLocaleString()}</td>
                  <td>
                    <div className="row-actions">
                      <CopyButton value={t.id} label="ID" small className="row-actions__btn--primary" />
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
        <CreateTokenModal
          onClose={() => setShowCreate(false)}
          onCreated={() => { setShowCreate(false); setPage(1); reload(); }}
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
            <td key={j}><span className="skeleton-line" /></td>
          ))}
        </tr>
      ))}
    </>
  );
}

function CreateTokenModal({ onClose, onCreated }) {
  const toast = useToast();
  const [name, setName] = useState('');
  const [scope, setScope] = useState('read');
  const [defaultUserID, setDefaultUserID] = useState('');
  const [defaultEndpoints, setDefaultEndpoints] = useState('');
  const [expiresAt, setExpiresAt] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState('');
  const [created, setCreated] = useState(null);
  const [secretCopied, setSecretCopied] = useState(false);

  async function handleSubmit(e) {
    e.preventDefault();
    if (!name) return;
    setSubmitting(true);
    setError('');
    try {
      const endpoints = defaultEndpoints
        .split(/[\n,]/)
        .map((s) => s.trim())
        .filter(Boolean);
      const result = await createAPIToken({
        name,
        scope,
        default_user_id: scope === 'write' ? defaultUserID.trim() : '',
        default_user_id_endpoints: scope === 'write' ? endpoints : [],
        expires_at: expiresAt ? new Date(expiresAt).toISOString() : null,
      });
      setCreated(result);
      toast.success(`Token "${name}" created`);
    } catch (err) {
      setError(err.message || 'Failed to create token.');
      toast.error(err.message || 'Failed to create token');
    } finally {
      setSubmitting(false);
    }
  }

  if (created) {
    return (
      <Modal title="Token Created" onClose={onClose}>
        <div className="form__row">
          <label className="form__label">Plaintext secret (shown once)</label>
          <div className="row" style={{ gap: 8, alignItems: 'flex-start' }}>
            <div className="copyable" style={{ flex: 1 }}>{created.secret}</div>
            <CopyButton value={created.secret} label="Copy" small />
          </div>
          <div className="form__hint">
            Store this securely — the dashboard cannot retrieve it later. Send it as{' '}
            <code>Authorization: Bearer &lt;secret&gt;</code> to{' '}
            <code>/v0/management/*</code>.
          </div>
        </div>
        <div className="row gap-sm" style={{ marginTop: 8 }}>
          <span className="dim mono">id: {created.id}</span>
          <span className="dim">· scope: {created.scope}</span>
          {created.default_user_id && (
            <span className="dim">· default user: {created.default_user_id}</span>
          )}
        </div>
        <label className="row gap-sm" style={{ cursor: 'pointer', marginTop: 16 }}>
          <input
            type="checkbox"
            checked={secretCopied}
            onChange={(e) => setSecretCopied(e.target.checked)}
            style={{ width: 'auto' }}
          />
          <span className="form__label" style={{ margin: 0 }}>I've stored the secret securely</span>
        </label>
        <div className="form__actions">
          <button className="primary" onClick={onCreated} disabled={!secretCopied}>Done</button>
        </div>
      </Modal>
    );
  }

  return (
    <Modal title="New Management API Token" onClose={onClose}>
      {error && <div className="error-banner">{error}</div>}
      <form onSubmit={handleSubmit}>
        <div className="form__row">
          <label className="form__label" htmlFor="name">Name</label>
          <input
            id="name" type="text" value={name} required
            onChange={(e) => setName(e.target.value)}
            placeholder="e.g. ci-read-only"
            disabled={submitting}
          />
        </div>
        <div className="form__row">
          <label className="form__label" htmlFor="scope">Scope</label>
          <select id="scope" value={scope} onChange={(e) => setScope(e.target.value)} disabled={submitting} style={{ width: '100%' }}>
            <option value="read">read — GET only</option>
            <option value="write">write — all methods (incl. mutations)</option>
          </select>
          <div className="form__hint">
            <strong>read</strong> tokens can only call GET endpoints. <strong>write</strong>{' '}
            tokens may create/update/delete resources. Per-endpoint and rate-limit
            policy can be configured after creation.
          </div>
        </div>
        {scope === 'write' && (
          <>
            <div className="form__row">
              <label className="form__label" htmlFor="default-user-id">Default user ID (optional)</label>
              <input
                id="default-user-id" type="text" value={defaultUserID}
                onChange={(e) => setDefaultUserID(e.target.value)}
                placeholder="e.g. internal-user-uuid"
                disabled={submitting}
              />
              <div className="form__hint">
                When a request to one of the endpoints below omits <code>user_id</code>,
                this Internal User id is used as the owner. Leave empty to disable.
              </div>
            </div>
            <div className="form__row">
              <label className="form__label" htmlFor="default-endpoints">Apply default user to endpoints</label>
              <textarea
                id="default-endpoints" rows={3} value={defaultEndpoints}
                onChange={(e) => setDefaultEndpoints(e.target.value)}
                placeholder={'/v0/management/api-keys-pg\n/v0/management/litellm/key/generate'}
                disabled={submitting}
              />
              <div className="form__hint">
                One path per line (a trailing <code>*</code> matches a prefix). An empty
                list disables the fallback even when a default user id is set.
              </div>
            </div>
          </>
        )}
        <div className="form__row">
          <label className="form__label" htmlFor="expires">Expires at (optional)</label>
          <input
            id="expires" type="datetime-local" value={expiresAt}
            onChange={(e) => setExpiresAt(e.target.value)}
            disabled={submitting}
          />
          <div className="form__hint">Leave empty for a token that never expires.</div>
        </div>
        <div className="form__actions">
          <button type="button" onClick={onClose} disabled={submitting}>Cancel</button>
          <button type="submit" className="primary" disabled={submitting || !name}>
            {submitting ? 'Creating…' : 'Create Token'}
          </button>
        </div>
      </form>
    </Modal>
  );
}
