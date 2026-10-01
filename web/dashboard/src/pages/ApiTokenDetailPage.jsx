import React, { useState } from 'react';
import { useParams, useNavigate, Link } from 'react-router-dom';
import {
  getAPIToken, patchAPIToken, putAPITokenPolicy, regenerateAPIToken,
  deleteAPIToken, getAPITokenAuditLog, listInternalUsers,
} from '../api/client.js';
import { useAsync } from '../hooks/useAsync.js';
import { Spinner, ErrorBanner, StatusBadge, Modal } from '../components/Primitives.jsx';
import Pager from '../components/Pager.jsx';
import CopyButton from '../components/CopyButton.jsx';
import EndpointMultiSelect from '../components/EndpointMultiSelect.jsx';
import { useToast } from '../components/Toast.jsx';

export default function ApiTokenDetailPage() {
  const { id } = useParams();
  const { data, error, loading, reload } = useAsync(() => getAPIToken(id), [id]);

  if (loading) return <Spinner label="Loading token…" />;
  if (error) return <ErrorBanner error={error} onRetry={reload} />;
  if (!data) return null;

  return (
    <>
      <TokenHeader token={data} onUpdated={reload} />
      <div className="grid grid--2">
        <TokenDetailsCard token={data} onUpdated={reload} />
        <PolicyCard tokenId={data.id} policy={data.policy} onUpdated={reload} />
      </div>
      <AuditLogCard tokenId={data.id} />
    </>
  );
}

function TokenHeader({ token, onUpdated }) {
  const toast = useToast();
  const [showRegen, setShowRegen] = useState(false);
  const [showDelete, setShowDelete] = useState(false);

  return (
    <div className="card key-header-strip">
      <div className="key-header-strip__main">
        <div className="row" style={{ gap: 12, alignItems: 'baseline', flexWrap: 'wrap' }}>
          <h1 className="main__title" style={{ margin: 0 }}>{token.name}</h1>
          <StatusBadge status={token.status} />
          <span className={`badge badge--${token.scope === 'write' ? 'active' : 'muted'}`}>{token.scope}</span>
        </div>
        <div className="key-header-strip__meta">
          <span className="key-header-strip__meta-item">
            <span className="dim">id:</span>
            <span className="mono">{token.id}</span>
            <CopyButton value={token.id} label="Copy" small />
          </span>
          <span className="key-header-strip__meta-item">
            <span className="dim">prefix:</span>
            <code className="mono">{token.key_prefix}…</code>
            <CopyButton value={`${token.key_prefix}…`} label="Copy" small />
          </span>
        </div>
      </div>
      <div className="key-header-strip__actions">
        <button onClick={() => { onUpdated(); toast.info('Token refreshed'); }}>Refresh</button>
        <button onClick={() => setShowRegen(true)}>Regenerate</button>
        <button className="danger" onClick={() => setShowDelete(true)}>Delete</button>
      </div>

      {showRegen && (
        <RegenerateModal
          tokenId={token.id}
          name={token.name}
          onClose={() => setShowRegen(false)}
          onDone={() => { setShowRegen(false); onUpdated(); }}
        />
      )}
      {showDelete && (
        <DeleteModal
          tokenId={token.id}
          name={token.name}
          onClose={() => setShowDelete(false)}
          onDone={() => { setShowDelete(false); }}
        />
      )}
    </div>
  );
}

function TokenDetailsCard({ token, onUpdated }) {
  const toast = useToast();
  const [savingStatus, setSavingStatus] = useState(false);
  const [newStatus, setNewStatus] = useState(token.status);
  const [savingScope, setSavingScope] = useState(false);
  const [newScope, setNewScope] = useState(token.scope);
  const [savingDefaultUser, setSavingDefaultUser] = useState(false);
  const [defaultUserID, setDefaultUserID] = useState(token.default_user_id || '');
  const [defaultEndpoints, setDefaultEndpoints] = useState((token.default_user_id_endpoints || []).join('\n'));
  const [userSearch, setUserSearch] = useState('');
  const usersReq = useAsync(
    () => listInternalUsers({ page: 1, pageSize: 200, sortBy: 'user_alias', sortOrder: 'asc' }),
    [],
  );
  const users = usersReq.data?.users || [];
  const filteredUsers = filterInternalUsers(users, userSearch);
  const selectedUserMissing = defaultUserID && !users.some((u) => u.id === defaultUserID);
  const isExpired = token.status === 'expired';

  async function handleStatusChange(e) {
    const status = e.target.value;
    setNewStatus(status);
    setSavingStatus(true);
    try {
      await patchAPIToken(token.id, { status });
      toast.success(`Status set to ${status}`);
      onUpdated();
    } catch (err) {
      toast.error(err.message || 'Failed to update status');
      setNewStatus(token.status);
    } finally {
      setSavingStatus(false);
    }
  }

  async function handleScopeChange(e) {
    const scope = e.target.value;
    setNewScope(scope);
    setSavingScope(true);
    try {
      await patchAPIToken(token.id, { scope });
      toast.success(`Scope set to ${scope}`);
      onUpdated();
    } catch (err) {
      toast.error(err.message || 'Failed to update scope');
      setNewScope(token.scope);
    } finally {
      setSavingScope(false);
    }
  }

  async function handleDefaultUserSave(e) {
    e.preventDefault();
    setSavingDefaultUser(true);
    try {
      await patchAPIToken(token.id, {
        default_user_id: defaultUserID.trim(),
        default_user_id_endpoints: parseList(defaultEndpoints),
      });
      toast.success('Default user settings saved');
      onUpdated();
    } catch (err) {
      toast.error(err.message || 'Failed to update default user');
    } finally {
      setSavingDefaultUser(false);
    }
  }

  return (
    <div className="card">
      <h3 className="card__title">Token Details</h3>

      <div className="form__row">
        <div className="form__label">Status</div>
        <div className={`status-control ${savingStatus ? 'status-control--saving' : ''}`}>
          {isExpired ? (
            <StatusBadge status="expired" />
          ) : (
            <>
              <StatusBadge status={token.status} />
              <select value={newStatus} onChange={handleStatusChange} disabled={savingStatus}>
                <option value="active">active</option>
                <option value="revoked">revoked</option>
              </select>
              {savingStatus && <div className="spinner spinner--sm" />}
            </>
          )}
        </div>
      </div>

      <div className="form__row">
        <div className="form__label">Scope</div>
        <div className={`status-control ${savingScope ? 'status-control--saving' : ''}`}>
          <span className={`badge badge--${token.scope === 'write' ? 'active' : 'muted'}`}>{token.scope}</span>
          <select value={newScope} onChange={handleScopeChange} disabled={savingScope}>
            <option value="read">read — GET only</option>
            <option value="write">write — all methods</option>
          </select>
          {savingScope && <div className="spinner spinner--sm" />}
        </div>
      </div>

      {token.scope === 'write' && (
        <form className="form__row" onSubmit={handleDefaultUserSave}>
          <div className="form__label">Default user fallback</div>
          {usersReq.loading ? (
            <Spinner label="Loading Internal Users…" />
          ) : usersReq.error ? (
            <div className="error-banner">
              {usersReq.error.message || 'Failed to load Internal Users.'}
            </div>
          ) : (
            <>
              <input
                type="text"
                className="search-input"
                value={userSearch}
                onChange={(e) => setUserSearch(e.target.value)}
                placeholder="Search by name, email, or ID…"
                aria-label="Search Internal Users"
                disabled={savingDefaultUser}
                style={{ marginBottom: 8, width: '100%' }}
              />
              <select
                value={defaultUserID}
                onChange={(e) => setDefaultUserID(e.target.value)}
                disabled={savingDefaultUser}
                style={{ width: '100%' }}
                size={Math.min(6, Math.max(3, filteredUsers.length + 1))}
              >
                <option value="">— none (disable fallback) —</option>
                {selectedUserMissing && (
                  <option value={defaultUserID}>
                    {defaultUserID} · (not in loaded list)
                  </option>
                )}
                {filteredUsers.map((u) => (
                  <option key={u.id} value={u.id}>
                    {u.user_alias ? `👤 ${u.user_alias}` : '👤 (no alias)'}
                    {u.user_email ? ` <${u.user_email}>` : ''}
                    {` · ${u.id}`}
                  </option>
                ))}
              </select>
              {!filteredUsers.length && userSearch.trim() !== '' && (
                <div className="form__hint">No Internal Users match “{userSearch}”.</div>
              )}
              {!users.length && (
                <div className="form__hint">
                  No Internal Users yet.{' '}
                  <Link to="/internal-users">Create an Internal User first</Link> to use as a fallback.
                </div>
              )}
            </>
          )}
          <textarea
            rows={3}
            value={defaultEndpoints}
            onChange={(e) => setDefaultEndpoints(e.target.value)}
            placeholder={'/v0/management/api-keys-pg\n/v0/management/litellm/key/generate'}
            disabled={savingDefaultUser}
          />
          <div className="form__hint">
            When a request to one of these endpoints omits <code>user_id</code>, the
            default Internal User id is used as the owner. One path per line; an empty
            list disables the fallback.
          </div>
          <div className="form__actions">
            <button type="submit" className="primary" disabled={savingDefaultUser}>
              {savingDefaultUser ? 'Saving…' : 'Save default user'}
            </button>
          </div>
        </form>
      )}

      <div className="grid grid--3" style={{ gap: 10, marginBottom: 16 }}>
        <KeyStat label="Created" value={formatDate(token.created_at)} />
        <KeyStat label="Last used" value={token.last_used_at ? formatDate(token.last_used_at) : 'never'} />
        <KeyStat label="Expires" value={token.expires_at ? formatDate(token.expires_at) : 'never'} />
      </div>

      {token.metadata && Object.keys(token.metadata).length > 0 && (
        <div className="form__row">
          <div className="form__label">Metadata</div>
          <div className="row" style={{ gap: 8, alignItems: 'flex-start' }}>
            <pre className="copyable" style={{ whiteSpace: 'pre-wrap', flex: 1 }}>
              {JSON.stringify(token.metadata, null, 2)}
            </pre>
            <CopyButton value={JSON.stringify(token.metadata, null, 2)} label="Copy" small />
          </div>
        </div>
      )}
    </div>
  );
}

function KeyStat({ label, value }) {
  return (
    <div className="key-stat">
      <div className="key-stat__label">{label}</div>
      <div className="key-stat__value">{value}</div>
    </div>
  );
}

function formatDate(s) {
  return new Date(s).toLocaleString();
}

function PolicyCard({ tokenId, policy: initial, onUpdated }) {
  const toast = useToast();
  const [editing, setEditing] = useState(false);
  if (editing) {
    return (
      <PolicyEditor
        tokenId={tokenId}
        initial={initial || {}}
        onCancel={() => setEditing(false)}
        onSaved={() => { setEditing(false); onUpdated(); toast.success('Policy saved'); }}
      />
    );
  }
  return (
    <div className="card">
      <h3 className="card__title">Policy</h3>
      {!initial ? (
        <p className="muted" style={{ marginBottom: 12 }}>
          No policy attached. This token is bounded only by its scope and the
          global IP-ban + allow-remote gate.
        </p>
      ) : (
        <>
          <div className="grid grid--2" style={{ gap: 12, marginBottom: 12 }}>
            <PolicyStat label="RPM Limit" value={initial.rpm_limit ?? 'unlimited'} />
            <PolicyStat label="Max Parallel" value={initial.max_parallel_requests ?? 'unlimited'} />
            <PolicyStat label="Hourly Rate" value={initial.hourly_rate_limit ?? 'unlimited'} />
          </div>
          {initial.allowed_endpoints && initial.allowed_endpoints.length > 0 && (
            <div className="form__row">
              <div className="form__label">Allowed endpoints</div>
              <ul className="list-bare">
                {initial.allowed_endpoints.map((m) => <li key={m} className="mono">{m}</li>)}
              </ul>
            </div>
          )}
          {initial.blocked_endpoints && initial.blocked_endpoints.length > 0 && (
            <div className="form__row">
              <div className="form__label">Blocked endpoints</div>
              <ul className="list-bare">
                {initial.blocked_endpoints.map((m) => <li key={m} className="mono">{m}</li>)}
              </ul>
            </div>
          )}
          {initial.allowed_ips && initial.allowed_ips.length > 0 && (
            <div className="form__row">
              <div className="form__label">Allowed IPs</div>
              <ul className="list-bare">
                {initial.allowed_ips.map((m) => <li key={m} className="mono">{m}</li>)}
              </ul>
            </div>
          )}
          {initial.blocked_ips && initial.blocked_ips.length > 0 && (
            <div className="form__row">
              <div className="form__label">Blocked IPs</div>
              <ul className="list-bare">
                {initial.blocked_ips.map((m) => <li key={m} className="mono">{m}</li>)}
              </ul>
            </div>
          )}
        </>
      )}
      <div className="form__actions">
        <button className={initial ? '' : 'primary'} onClick={() => setEditing(true)}>
          {initial ? 'Edit Policy' : 'Attach Policy'}
        </button>
      </div>
    </div>
  );
}

function PolicyStat({ label, value }) {
  return (
    <div>
      <div className="form__label">{label}</div>
      <div className="mono">{value}</div>
    </div>
  );
}

function PolicyEditor({ tokenId, initial, onCancel, onSaved }) {
  const [rpmLimit, setRpmLimit] = useState(initial.rpm_limit ?? '');
  const [maxParallel, setMaxParallel] = useState(initial.max_parallel_requests ?? '');
  const [hourlyRate, setHourlyRate] = useState(initial.hourly_rate_limit ?? '');
  const [allowedEndpoints, setAllowedEndpoints] = useState(initial.allowed_endpoints || []);
  const [blockedEndpoints, setBlockedEndpoints] = useState(initial.blocked_endpoints || []);
  const [allowedIPs, setAllowedIPs] = useState((initial.allowed_ips || []).join('\n'));
  const [blockedIPs, setBlockedIPs] = useState((initial.blocked_ips || []).join('\n'));
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState('');

  function numOrUndef(v) {
    if (v === '' || v === null || v === undefined) return undefined;
    const n = Number(v);
    return Number.isFinite(n) ? n : undefined;
  }

  async function handleSubmit(e) {
    e.preventDefault();
    setSubmitting(true);
    setError('');
    const policy = {
      token_id: tokenId,
      rpm_limit: numOrUndef(rpmLimit),
      max_parallel_requests: numOrUndef(maxParallel),
      hourly_rate_limit: numOrUndef(hourlyRate),
      allowed_endpoints: allowedEndpoints,
      blocked_endpoints: blockedEndpoints,
      allowed_ips: parseList(allowedIPs),
      blocked_ips: parseList(blockedIPs),
    };
    try {
      await putAPITokenPolicy(tokenId, policy);
      onSaved();
    } catch (err) {
      setError(err.message);
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <div className="card">
      <h3 className="card__title">Edit Policy</h3>
      {error && <div className="error-banner">{error}</div>}
      <form onSubmit={handleSubmit}>
        <div className="grid grid--3" style={{ gap: 12 }}>
          <div className="form__row">
            <label className="form__label" htmlFor="rpm">RPM limit</label>
            <input id="rpm" type="number" min="0" value={rpmLimit}
              onChange={(e) => setRpmLimit(e.target.value)} placeholder="unlimited" />
          </div>
          <div className="form__row">
            <label className="form__label" htmlFor="parallel">Max parallel</label>
            <input id="parallel" type="number" min="0" value={maxParallel}
              onChange={(e) => setMaxParallel(e.target.value)} placeholder="unlimited" />
          </div>
          <div className="form__row">
            <label className="form__label" htmlFor="hourly">Hourly rate limit</label>
            <input id="hourly" type="number" min="0" value={hourlyRate}
              onChange={(e) => setHourlyRate(e.target.value)} placeholder="unlimited" />
          </div>
        </div>
        <EndpointMultiSelect
          label="Allowed endpoints"
          value={allowedEndpoints}
          onChange={setAllowedEndpoints}
          placeholder="search endpoints to allow…"
          hint={
            <>
              Pick from every registered management route, or press Enter to add a custom glob.
              Patterns: <code>METHOD /path</code> or <code>METHOD /prefix/*</code> (starred ★ entries match a subtree).
              Empty = all endpoints allowed.
            </>
          }
        />
        <EndpointMultiSelect
          label="Blocked endpoints"
          value={blockedEndpoints}
          onChange={setBlockedEndpoints}
          placeholder="search endpoints to block…"
          hint="Blocked patterns take precedence over allowed — a match denies the request even when the endpoint is also allowlisted."
        />
        <div className="form__row">
          <label className="form__label" htmlFor="allowed-ips">Allowed IPs / CIDRs (one per line)</label>
          <textarea id="allowed-ips" rows={3} value={allowedIPs}
            onChange={(e) => setAllowedIPs(e.target.value)}
            placeholder={'10.0.0.5\n10.0.0.0/8\n2001:db8::/32'} />
          <div className="form__hint">
            Single IPs (<code>10.0.0.5</code>) or CIDR ranges (<code>10.0.0.0/8</code>,
            <code>2001:db8::/32</code>). Empty = all IPs allowed (subject to the block list).
          </div>
        </div>
        <div className="form__row">
          <label className="form__label" htmlFor="blocked-ips">Blocked IPs / CIDRs (one per line)</label>
          <textarea id="blocked-ips" rows={3} value={blockedIPs}
            onChange={(e) => setBlockedIPs(e.target.value)}
            placeholder={'203.0.113.0/24'} />
          <div className="form__hint">
            Blocked entries take precedence over the allow list — a match denies
            the request even if the IP is also allowlisted.
          </div>
        </div>
        <div className="form__actions">
          <button type="button" onClick={onCancel} disabled={submitting}>Cancel</button>
          <button type="submit" className="primary" disabled={submitting}>
            {submitting ? 'Saving…' : 'Save Policy'}
          </button>
        </div>
      </form>
    </div>
  );
}

function parseList(text) {
  return text.split('\n').map((s) => s.trim()).filter(Boolean);
}

// filterInternalUsers returns the Internal Users whose alias, email, or id
// contains the query (case-insensitive). An empty query returns every user.
// Exported for unit testing — the dashboard has no React render harness.
export function filterInternalUsers(users, query) {
  const list = Array.isArray(users) ? users : [];
  const q = String(query || '').trim().toLowerCase();
  if (!q) return list;
  return list.filter((u) => [u.user_alias, u.user_email, u.id]
    .filter(Boolean)
    .join(' ')
    .toLowerCase()
    .includes(q));
}

function AuditLogCard({ tokenId }) {
  const [page, setPage] = useState(1);
  const [method, setMethod] = useState('');
  const [path, setPath] = useState('');
  const [errorsOnly, setErrorsOnly] = useState(false);
  const { data, error, loading, reload } = useAsync(
    () => getAPITokenAuditLog({ page, pageSize: 25, tokenId, method, path, errorsOnly }),
    [page, tokenId, method, path, errorsOnly],
  );

  const entries = data?.entries || [];
  const total = data?.total ?? 0;
  const totalPages = data?.total_pages ?? 0;

  return (
    <div className="card" style={{ marginTop: 16 }}>
      <div className="row row--between" style={{ marginBottom: 12 }}>
        <h3 className="card__title" style={{ margin: 0 }}>Audit Log</h3>
        <button onClick={reload}>Refresh</button>
      </div>

      <div className="catalog-toolbar" style={{ marginBottom: 12 }}>
        <select value={method} onChange={(e) => { setMethod(e.target.value); setPage(1); }} aria-label="Method filter">
          <option value="">All methods</option>
          <option value="GET">GET</option>
          <option value="POST">POST</option>
          <option value="PATCH">PATCH</option>
          <option value="PUT">PUT</option>
          <option value="DELETE">DELETE</option>
        </select>
        <input
          className="search-input"
          type="text" value={path}
          onChange={(e) => { setPath(e.target.value); setPage(1); }}
          placeholder="Path contains…"
          aria-label="Path filter"
        />
        <label className="row gap-sm" style={{ cursor: 'pointer' }}>
          <input type="checkbox" checked={errorsOnly}
            onChange={(e) => { setErrorsOnly(e.target.checked); setPage(1); }}
            style={{ width: 'auto' }} />
          <span className="form__label" style={{ margin: 0 }}>Errors only</span>
        </label>
        <div className="catalog-toolbar__spacer" />
        <span className="catalog-toolbar__count">{total} total</span>
      </div>

      {loading && <Spinner label="Loading audit log…" />}
      <ErrorBanner error={error} onRetry={reload} />

      {!loading && !error && entries.length === 0 && (
        <p className="muted dim">No audit entries match the current filters.</p>
      )}

      {!loading && !error && entries.length > 0 && (
        <>
          <table className="table table--compact">
            <thead>
              <tr>
                <th>Time</th><th>Method</th><th>Path</th><th>Status</th>
                <th>Latency</th><th>IP</th><th aria-label="Expand" />
              </tr>
            </thead>
            <tbody>
              {entries.map((e) => (
                <AuditRow key={e.id} entry={e} />
              ))}
            </tbody>
          </table>
          <div style={{ padding: '0 16px 16px' }}>
            <Pager
              page={page}
              totalPages={totalPages}
              total={total}
              pageSize={25}
              onPageChange={(p) => { if (p >= 1 && p <= totalPages) setPage(p); }}
            />
          </div>
        </>
      )}
    </div>
  );
}

function AuditRow({ entry }) {
  const [expanded, setExpanded] = useState(false);
  return (
    <>
      <tr className="row-link" onClick={() => setExpanded((v) => !v)}>
        <td className="mono dim">{new Date(entry.occurred_at).toLocaleString()}</td>
        <td><span className={`badge badge--${entry.method === 'GET' ? 'muted' : 'active'}`}>{entry.method}</span></td>
        <td className="mono" style={{ maxWidth: 320, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }} title={entry.path}>{entry.path}</td>
        <td>
          <span className={`badge badge--${entry.is_error ? 'revoked' : 'active'}`}>{entry.status_code}</span>
        </td>
        <td className="mono">{entry.latency_ms}ms</td>
        <td className="mono dim">{entry.actor_ip || '—'}</td>
        <td><span style={{ fontSize: 10 }}>{expanded ? '▾' : '▸'}</span></td>
      </tr>
      {expanded && (
        <tr>
          <td colSpan={7} style={{ padding: '12px 16px', background: 'var(--bg)' }}>
            <div className="grid grid--2" style={{ gap: 12 }}>
              <div>
                <div className="form__label">Request body</div>
                <pre className="copyable" style={{ whiteSpace: 'pre-wrap', maxHeight: 300, overflow: 'auto' }}>
                  {entry.request_body || '(empty)'}
                </pre>
              </div>
              <div>
                <div className="form__label">Error message</div>
                <pre className="copyable" style={{ whiteSpace: 'pre-wrap' }}>
                  {entry.error_message || '(none)'}
                </pre>
              </div>
            </div>
          </td>
        </tr>
      )}
    </>
  );
}

function RegenerateModal({ tokenId, name, onClose, onDone }) {
  const toast = useToast();
  const [submitting, setSubmitting] = useState(false);
  const [result, setResult] = useState(null);
  const [error, setError] = useState('');
  const [secretCopied, setSecretCopied] = useState(false);

  async function handleRegen() {
    setSubmitting(true);
    setError('');
    try {
      const r = await regenerateAPIToken(tokenId);
      setResult(r);
      toast.success(`Secret regenerated for "${name}"`);
    } catch (err) {
      setError(err.message);
      toast.error(err.message || 'Failed to regenerate');
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <Modal title="Regenerate Secret" onClose={onClose}>
      {result ? (
        <>
          <div className="form__row">
            <label className="form__label">New plaintext secret (shown once)</label>
            <div className="row" style={{ gap: 8, alignItems: 'flex-start' }}>
              <div className="copyable" style={{ flex: 1 }}>{result.secret}</div>
              <CopyButton value={result.secret} label="Copy" small />
            </div>
            <div className="form__hint">The token ID, policy, scope, and metadata are unchanged.</div>
          </div>
          <label className="row gap-sm" style={{ cursor: 'pointer', marginTop: 12 }}>
            <input type="checkbox" checked={secretCopied}
              onChange={(e) => setSecretCopied(e.target.checked)} style={{ width: 'auto' }} />
            <span className="form__label" style={{ margin: 0 }}>I've stored the new secret securely</span>
          </label>
          <div className="form__actions">
            <button className="primary" onClick={onDone} disabled={!secretCopied}>Done</button>
          </div>
        </>
      ) : (
        <>
          {error && <div className="error-banner">{error}</div>}
          <p className="muted">
            This will issue a new secret for the token. The old secret stops working
            immediately; the token ID, policy, and metadata are preserved.
          </p>
          <div className="form__actions">
            <button onClick={onClose} disabled={submitting}>Cancel</button>
            <button className="danger" onClick={handleRegen} disabled={submitting}>
              {submitting ? 'Regenerating…' : 'Regenerate'}
            </button>
          </div>
        </>
      )}
    </Modal>
  );
}

function DeleteModal({ tokenId, name, onClose, onDone }) {
  const toast = useToast();
  const navigate = useNavigate();
  const [confirm, setConfirm] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState('');

  async function handleDelete() {
    if (confirm !== name) return;
    setSubmitting(true);
    setError('');
    try {
      await deleteAPIToken(tokenId);
      toast.success(`Token "${name}" deleted`);
      onDone();
      navigate('/api-tokens', { replace: true });
    } catch (err) {
      setError(err.message);
      toast.error(err.message || 'Failed to delete token');
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <Modal title="Delete Token" onClose={onClose}>
      {error && <div className="error-banner">{error}</div>}
      <p>
        Permanently delete <strong>{name}</strong>? This removes the token, its policy,
        and stops all subsequent calls made with its secret.
      </p>
      <div className="form__row">
        <label className="form__label">Type the token name to confirm</label>
        <input type="text" value={confirm} onChange={(e) => setConfirm(e.target.value)} placeholder={name} />
      </div>
      <div className="form__actions">
        <button onClick={onClose} disabled={submitting}>Cancel</button>
        <button className="danger" onClick={handleDelete} disabled={submitting || confirm !== name}>
          {submitting ? 'Deleting…' : 'Delete Forever'}
        </button>
      </div>
    </Modal>
  );
}
